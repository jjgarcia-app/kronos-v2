package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
)

// claudeCLIProvider identifica este backend en Usage/LastFailure — mismo
// string que cfg.LLM.Provider="claude-cli", para que `kronos doctor` no
// tenga que reconciliar dos vocabularios distintos.
const claudeCLIProvider = "claude-cli"

const (
	// DefaultClaudeCLIPath es el binario que se ejecuta si llm.cli_path no
	// está configurado — se resuelve por PATH, igual que si el usuario
	// tipeara "claude" a mano.
	DefaultClaudeCLIPath = "claude"
	// DefaultClaudeCLIModel: haiku es el modelo más barato/rápido de la
	// suscripción de Claude Code — medido en 6s para una generación mínima,
	// suficiente para digest/captura pasiva/judge, que no necesitan un
	// modelo grande.
	DefaultClaudeCLIModel = "haiku"
	// defaultClaudeCLITimeoutMs: 30s de default — `claude -p` tardó 6s en una
	// llamada trivial, así que 30s deja margen sin dejar un hook colgado
	// demasiado tiempo si la llamada real (con más contexto) tarda más.
	defaultClaudeCLITimeoutMs = 30000
	// claudeCLIConfigDirName es el subdirectorio dentro del data dir de
	// kronos donde vive el config dir aislado de Claude Code (ver
	// prepareClaudeCLIConfigDir).
	claudeCLIConfigDirName = "claude-cli"
)

// errNoClaudeCredentials distingue, dentro de los errores que puede devolver
// prepareClaudeCLIConfigDir, el caso puntual de "no hay nada para linkear" —
// los otros (crear directorio, escribir settings.json) son fallas de
// filesystem genéricas que no ameritan la clasificación "sin credenciales" en
// diagnostics.go.
var errNoClaudeCredentials = errors.New("no se encontraron credenciales de Claude Code (~/.claude/.credentials.json ni ~/.claude.json)")

// claudeCLIBackend implementa generateBackend invocando `claude -p` como
// subproceso — alternativa a Ollama para máquinas donde el modelo local no
// responde bajo carga, aprovechando una suscripción de Claude Code ya
// autenticada en vez de pagar una API aparte.
//
// Corre con CLAUDE_CONFIG_DIR apuntando a configDir, un directorio de
// configuración AISLADO del ~/.claude real (ver prepareClaudeCLIConfigDir).
// Esto es imprescindible, no cosmético: sin aislar la config, el subproceso
// hereda los hooks de kronos instalados en ~/.claude/settings.json —
// UserPromptSubmit, PreCompact, etc. — y terminaría llamando de vuelta a
// kronos desde dentro de la propia llamada de kronos al LLM (recursión).
// Probado en vivo: pasar `--settings '{"hooks":{}}'` NO alcanza, porque
// Claude Code mezcla esa config con la de ~/.claude y los hooks corren
// igual; con CLAUDE_CONFIG_DIR apuntando a un directorio con su propio
// settings.json (hooks:{}) no se disparó ninguna sesión nueva de kronos.
type claudeCLIBackend struct {
	cliPath   string
	model     string
	configDir string
	timeout   time.Duration

	// lastFailurePath: dónde persistir la última clasificación de falla (ver
	// diagnostics.go) — "" (Client armado directo en tests) deja el registro
	// como no-op.
	lastFailurePath string
}

func (b *claudeCLIBackend) generate(ctx context.Context, prompt string, _ int) (string, error) {
	genCtx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	// --output-format text: nada de JSON envolvente que parsear del lado del
	// CLI — el prompt de cada método ya le pide al modelo que responda con
	// el JSON de negocio directamente, y extractJSONObject limpia cualquier
	// prosa o fence alrededor.
	cmd := exec.CommandContext(genCtx, b.cliPath, "-p", "--model", b.model, "--output-format", "text")
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+b.configDir)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// exec.CommandContext mata el proceso (SIGKILL) apenas vence genCtx — no
	// hace falta un kill manual acá. WaitDelay acota además cuánto espera
	// Wait a que se cierren los pipes de stdout/stderr tras esa señal: sin
	// esto, si el CLI dejó algún proceso hijo vivo con esos descriptores
	// heredados (un shell intermedio, una tool en curso), Run() se queda
	// colgado hasta que ESE proceso termine por su cuenta, no hasta el
	// timeout configurado.
	cmd.WaitDelay = 2 * time.Second
	runErr := cmd.Run()
	timedOut := genCtx.Err() != nil
	if runErr == nil && !timedOut {
		return strings.TrimSpace(stdout.String()), nil
	}

	kind, advice := classifyClaudeCLIFailure(runErr, timedOut, stderr.String())
	slog.Warn("claude-cli: llamada de generación falló",
		"clasificación", kind, "acción", advice, "error", runErr,
		"stderr", truncate(strings.TrimSpace(stderr.String()), 500))
	recordLastFailure(b.lastFailurePath, claudeCLIProvider, kind, advice)

	if timedOut {
		return "", fmt.Errorf("claude cli: excedió el timeout de %s — %s", b.timeout, advice)
	}
	return "", fmt.Errorf("claude cli (%s) falló [%s]: %s — %w — stderr: %s",
		b.cliPath, kind, advice, runErr, truncate(strings.TrimSpace(stderr.String()), 500))
}

// classifyClaudeCLIFailure interpreta por qué falló `claude -p` a partir del
// error de exec y del stderr crudo — motivado por que hoy un CLI
// actualizado, una sesión deslogueada o un modelo inexistente lucen igual
// desde afuera (exit != 0 o timeout), y el usuario tiene que adivinar qué
// pasó leyendo un stderr crudo en el mejor de los casos. El matching de
// stderr es por substring, sin distinguir mayúsculas, porque los textos
// reales del CLI pueden variar entre versiones — si no matchea ninguno, se
// clasifica como "desconocido" con el stderr recortado en vez de fallar el
// diagnóstico.
func classifyClaudeCLIFailure(runErr error, timedOut bool, stderr string) (kind, advice string) {
	low := strings.ToLower(stderr)

	if timedOut || strings.Contains(low, "timed out") || strings.Contains(low, "context deadline exceeded") {
		return ClaudeCLIFailureTimeout, "el modelo no respondió dentro de llm.timeout_ms (súbelo o usá un modelo más rápido)"
	}
	// exec.ErrNotFound: el nombre no se encontró recorriendo $PATH (cli_path
	// pelado, ej. "claude"). os.ErrNotExist: cli_path es una ruta absoluta
	// que no existe — LookPath ni se intenta en ese caso, así que el error
	// de exec es un *PathError distinto.
	if errors.Is(runErr, exec.ErrNotFound) || errors.Is(runErr, os.ErrNotExist) {
		return ClaudeCLIFailureBinaryMissing, "no encuentro el CLI de claude: revisá llm.cli_path"
	}
	if errors.Is(runErr, os.ErrPermission) {
		return ClaudeCLIFailurePermission, "sin permiso para ejecutar el CLI"
	}
	switch {
	case strings.Contains(low, "not logged in") || strings.Contains(low, "/login"):
		return ClaudeCLIFailureNotLoggedIn, "no hay sesión de Claude Code iniciada: corré `claude login`"
	case strings.Contains(low, "unknown flag") || strings.Contains(low, "unrecognized") ||
		strings.Contains(low, "invalid model") || strings.Contains(low, "model not found"):
		return ClaudeCLIFailureIncompatible, "el CLI o el modelo configurado no son compatibles: revisá `claude --version` y llm.model"
	case strings.Contains(low, "permission denied"):
		return ClaudeCLIFailurePermission, "sin permiso para ejecutar el CLI"
	}
	return ClaudeCLIFailureUnknown, "no se pudo clasificar la falla — stderr: " + truncate(strings.TrimSpace(stderr), 300)
}

// NewClaudeCLIFromConfig arma un *Client que genera invocando `claude -p` en
// vez de Ollama. A diferencia de NewOllamaFromConfig, no hay un ping barato
// equivalente a /api/tags: la única forma de saber si el CLI+auth funcionan
// es invocarlo, así que esta función solo verifica que haya credenciales
// para linkear al config dir aislado — un fallo real de auth o del binario
// se descubre en la primera llamada y lo cuenta el cortacircuitos, igual que
// un fallo de Ollama.
//
// Devuelve nil (sin loguear como error — es una config válida, solo no
// disponible en esta máquina) si no se pudo resolver el data dir o si no hay
// credenciales de Claude Code para aislar.
func NewClaudeCLIFromConfig(ctx context.Context, cfg config.Config) *Client {
	_ = ctx // sin round-trip de red al construir, ver comentario arriba

	cliPath := cfg.LLM.CLIPath
	if cliPath == "" {
		cliPath = DefaultClaudeCLIPath
	}
	model := cfg.LLM.Model
	if model == "" {
		model = DefaultClaudeCLIModel
	}
	timeoutMs := cfg.LLM.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = defaultClaudeCLITimeoutMs
	}

	dataDir, err := platform.DataDir()
	if err != nil {
		slog.Warn("claude-cli: no se pudo resolver el data dir, sin LLM por CLI", "error", err)
		return nil
	}

	configDir := filepath.Join(dataDir, claudeCLIConfigDirName)
	if err := prepareClaudeCLIConfigDir(configDir); err != nil {
		slog.Warn("claude-cli: config dir aislado no disponible, sin LLM por CLI", "error", err)
		if errors.Is(err, errNoClaudeCredentials) {
			// Abstención, no fallo: se cuenta y se clasifica para que `kronos
			// doctor` la muestre, pero sin pasar por beginGeneration (no hay
			// *Client todavía) ni por el cortacircuitos — no es una falla del
			// CLI, es que no se lo llegó a invocar.
			recordLastFailure(DefaultLastFailurePath(dataDir), claudeCLIProvider, ClaudeCLIFailureNoCreds,
				"no encuentro las credenciales de Claude Code (~/.claude/.credentials.json): iniciá sesión con `claude login` o revisá llm.cli_path")
			NewUsage(DefaultUsagePath(dataDir)).Record(claudeCLIProvider, UsageResultSkippedNoCreds)
		}
		return nil
	}

	c := &Client{
		model:         model,
		provider:      claudeCLIProvider,
		maxLoadPerCPU: cfg.LLM.MaxLoadPerCPU,
		backend: &claudeCLIBackend{
			cliPath:         cliPath,
			model:           model,
			configDir:       configDir,
			timeout:         time.Duration(timeoutMs) * time.Millisecond,
			lastFailurePath: DefaultLastFailurePath(dataDir),
		},
	}

	failures := cfg.LLM.BreakerFailures
	var openFor time.Duration
	if cfg.LLM.BreakerMinutes > 0 {
		openFor = time.Duration(cfg.LLM.BreakerMinutes) * time.Minute
	}
	c.SetBreaker(NewBreaker(DefaultBreakerPath(dataDir), failures, openFor))
	c.SetUsage(NewUsage(DefaultUsagePath(dataDir)))

	return c
}

// prepareClaudeCLIConfigDir arma, si hace falta, el config dir aislado de
// Claude Code que claudeCLIBackend usa vía CLAUDE_CONFIG_DIR:
//   - settings.json con {"hooks":{}} — por qué CLAUDE_CONFIG_DIR y no
//     --settings, ver el comentario de claudeCLIBackend.
//   - symlinks (o copias, si el symlink falla) a ~/.claude/.credentials.json
//     y ~/.claude.json, las dos fuentes de auth que lee Claude Code — sin
//     esto el subproceso no está autenticado.
//
// Es idempotente: si el directorio ya está armado de una corrida anterior,
// no toca nada. Devuelve error si no encontró NINGUNA credencial para
// linkear — en ese caso el caller (NewClaudeCLIFromConfig) devuelve nil y
// arriba se hace fail-open, igual que Ollama inalcanzable.
func prepareClaudeCLIConfigDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("crear config dir: %w", err)
	}

	settingsPath := filepath.Join(dir, "settings.json")
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		if err := os.WriteFile(settingsPath, []byte(`{"hooks":{}}`), 0o644); err != nil {
			return fmt.Errorf("escribir settings.json: %w", err)
		}
	}

	credsDst := filepath.Join(dir, ".credentials.json")
	mcpDst := filepath.Join(dir, ".claude.json")

	if claudeDir, err := platform.ClaudeDir(); err == nil {
		_ = ensureLinked(filepath.Join(claudeDir, ".credentials.json"), credsDst)
	}
	if mcpFile, err := platform.ClaudeMCPFile(); err == nil {
		_ = ensureLinked(mcpFile, mcpDst)
	}

	if !fileExists(credsDst) && !fileExists(mcpDst) {
		return errNoClaudeCredentials
	}
	return nil
}

// ensureLinked crea dst como symlink a src la primera vez que hace falta —
// si dst ya existe (symlink o archivo, de una corrida anterior) no hace
// nada. Si el symlink falla (permisos, filesystem sin soporte), copia el
// contenido de src como fallback. No es error que src no exista: es normal
// que falte una de las dos fuentes de credenciales.
func ensureLinked(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return nil
	}
	if _, err := os.Stat(src); err != nil {
		return err
	}
	if err := os.Symlink(src, dst); err == nil {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
