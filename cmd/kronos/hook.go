package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// runHook dispatches a named hook.
//
// Usage:
//
//	kronos hook <name> [--reason <reason>]
//	kronos hook <name> <reason>
//
// Supported hooks: session-start, prompt-submit, subagent-stop, session-stop.
// For session-start, reason="compact" triggers post-compaction recovery.
// Reason "startup", "clear", or empty all trigger the normal session start.
func runHook(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("uso: kronos hook <session-start|prompt-submit|subagent-stop|session-stop|pre-tool-use> [--reason compact]")
	}

	hookName := args[0]
	reason := parseReason(args[1:])

	dbPath, err := platform.DBPath()
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	st, err := store.New(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// Build VectorStore solo para prompt-submit — es el único hook que lo usa
	// (ver hooks.RunWithReason). Los otros 4 hooks (session-start, session-stop,
	// subagent-stop, pre-tool-use) antes pagaban igual el costo de un ping a
	// Ollama + cargar el vector store entero desde disco en CADA invocación,
	// sin usarlo nunca — puro desperdicio y contención innecesaria contra
	// Ollama en cada evento de sesión, no solo en cada prompt.
	//
	// Nil es manejado con gracia por RunPromptSubmit (cae a FTS-only). Timeout
	// corto: esto es un hook fire-and-forget, no vale la pena esperar 2s+ a
	// que Ollama responda si está ocupado — mejor degradar rápido a FTS.
	var vs *embeddings.VectorStore
	if hookName == "prompt-submit" {
		if dataDir, err := platform.DataDir(); err == nil {
			vsCtx, vsCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			vs, _ = embeddings.New(vsCtx, dataDir)
			vsCancel()
		}
	}

	return hooks.RunWithReason(context.Background(), hookName, reason, st, vs)
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
