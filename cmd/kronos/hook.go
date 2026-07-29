package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// hookConnectTimeout acota cuánto puede tardar un hook intentando conectar a
// Postgres antes de rendirse y usar el buffer SQLite local. Los hooks corren
// en cada prompt del usuario — el timeout de 3s pensado para el arranque del
// daemon (una sola vez) sería una demora perceptible en cada mensaje si
// Postgres está caído. Da igual si se rinde: la escritura sigue yendo a
// buffer + queda encolada en sync_queue, y el daemon (que sí vive lo
// suficiente como para reintentar con el backoff completo) la sincroniza
// más tarde.
//
// 800ms, no menos: medido en vivo, un connect+migrate exitoso contra un
// Postgres sano puede tardar ~450ms en la primera conexión del proceso —
// un timeout de 300ms lo tumbaba igual que si estuviera caído, encolando de
// más innecesariamente. Con Postgres genuinamente caído (puerto sin nada
// escuchando), el rechazo de conexión es casi instantáneo de todos modos —
// este timeout es el techo del peor caso, no el tiempo típico.
const hookConnectTimeout = 800 * time.Millisecond

// runHook dispatches a named hook.
//
// Usage:
//
//	kronos hook <name> [--reason <reason>]
//	kronos hook <name> <reason>
//
// Supported hooks: session-start, prompt-submit, subagent-stop, session-stop,
// pre-tool-use, pre-compact, post-tool-use.
// For session-start, reason="compact" triggers post-compaction recovery.
// Reason "startup", "clear", or empty all trigger the normal session start.
func runHook(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("uso: kronos hook <session-start|prompt-submit|subagent-stop|session-stop|pre-tool-use|pre-compact|post-tool-use> [--reason compact]")
	}

	hookName := args[0]
	reason := parseReason(args[1:])

	if hookName == "prompt-submit" {
		return runPromptSubmitHook(reason)
	}

	dbPath, err := platform.DBPath()
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	cfg, _ := config.Load()

	// antes esto era store.New(dbPath) — SQLite puro, siempre, sin importar
	// si Postgres está configurado. Eso significaba que TODO lo que escriben
	// los hooks (sesiones, prompts, aprendizajes pasivos de subagentes) nunca
	// pasaba por sync_queue y nunca llegaba a Postgres — solo mem_save (vía
	// el daemon, que sí usa openStore) sincronizaba de verdad. openStore acá
	// también, con un timeout de conexión corto (ver hookConnectTimeout) para
	// no volver lento cada prompt cuando Postgres está caído.
	prevTimeout := store.ConnectTimeout
	store.ConnectTimeout = hookConnectTimeout
	st, err := openStore(cfg, dbPath)
	store.ConnectTimeout = prevTimeout
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	return hooks.RunWithReason(context.Background(), hookName, reason, st, nil)
}

// promptSubmitDaemonURL apunta al mismo puerto que usa el daemon compartido
// (ver cmd/kronos/serve.go / mcp_proxy.go) — sin config de puerto custom
// hoy en día, así que hardcodear 4317 acá es consistente con el resto.
const promptSubmitDaemonURL = "http://127.0.0.1:4317/hooks/prompt-submit"

// runPromptSubmitHook intenta que el daemon compartido procese el prompt —
// evita que este proceso corto (uno por CADA prompt del usuario) abra su
// propia conexión a Postgres y su propio vector store desde cero, multiplicado
// por cuántas sesiones de Claude Code estén abiertas a la vez. Si el daemon
// no responde a tiempo (caído, arrancando), cae al camino local original —
// esto es una optimización, no una dependencia dura: el hook sigue
// funcionando exactamente igual que antes si el daemon no está disponible.
func runPromptSubmitHook(reason string) error {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read hook input: %w", err)
	}

	if tryDaemonPromptSubmit(promptSubmitDaemonURL, data, os.Stdout) {
		return nil
	}

	in := hooks.ParseInput(data)
	if reason != "" {
		in.Reason = reason
	}

	dbPath, err := platform.DBPath()
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	cfg, _ := config.Load()
	prevTimeout := store.ConnectTimeout
	store.ConnectTimeout = hookConnectTimeout
	st, err := openStore(cfg, dbPath)
	store.ConnectTimeout = prevTimeout
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// Nil es manejado con gracia por RunPromptSubmit (cae a FTS-only). Timeout
	// corto: esto es un hook fire-and-forget, no vale la pena esperar 2s+ a
	// que Ollama responda si está ocupado — mejor degradar rápido a FTS.
	var vs *embeddings.VectorStore
	if dataDir, err := platform.DataDir(); err == nil {
		vsCtx, vsCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		vs, _ = embeddings.New(vsCtx, dataDir)
		vsCancel()
	}

	return hooks.RunPromptSubmit(context.Background(), in, st, vs, os.Stdout)
}

// tryDaemonPromptSubmit devuelve true si el daemon respondió OK (ya escribió
// su salida a out) — false ante cualquier falla, sin importar la causa
// (daemon caído, timeout, error HTTP): el caller cae al camino local sin
// necesidad de distinguir por qué. url parametrizado para poder testear
// contra un httptest.Server sin depender del puerto real 4317.
func tryDaemonPromptSubmit(url string, data []byte, out io.Writer) bool {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg, _ := config.Load(); cfg.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIToken)
	}

	client := &http.Client{Timeout: hookConnectTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	_, _ = io.Copy(out, resp.Body)
	return true
}

// parseReason extracts the reason value from remaining args.
// Accepts --reason <value> or a bare positional argument.
func parseReason(args []string) string {
	for i, arg := range args {
		if arg == "--reason" && i+1 < len(args) {
			return strings.TrimSpace(args[i+1])
		}
		if strings.HasPrefix(arg, "--reason=") {
			return strings.TrimSpace(strings.TrimPrefix(arg, "--reason="))
		}
		// bare positional argument that is not a flag
		if !strings.HasPrefix(arg, "-") {
			return strings.TrimSpace(arg)
		}
	}
	return ""
}
