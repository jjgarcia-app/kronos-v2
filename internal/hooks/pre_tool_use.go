package hooks

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// exitFn is the exit function used by RunPreToolUse. Overridden in tests.
var exitFn = os.Exit

// SetExitFn replaces the exit function for testing purposes.
// Pass nil to restore os.Exit.
func SetExitFn(fn func(int)) {
	if fn == nil {
		exitFn = os.Exit
	} else {
		exitFn = fn
	}
}

// gatedTools is a package-level cached map of tool names checked by the gate.
// Reset via ResetGatedTools in tests that mutate KRONOS_GATE_TOOLS.
var gatedTools map[string]bool

// ResetGatedTools clears the cached gated tools set.
// Must be called in tests that set KRONOS_GATE_TOOLS via t.Setenv.
func ResetGatedTools() {
	gatedTools = nil
}

// RunPreToolUse implements the deterministic memory-search gate (spec S3).
//
// Antes esto vivía parcialmente en un wrapper bash+python
// (~/.claude/scripts/kronos-gate.sh) que reimplementaba el mismo chequeo de
// "¿ya buscó esta sesión?" con una consulta SQL cruda a un path hardcodeado
// de Windows, además de un bypass por "proyecto desconocido" que el binario
// Go no tenía. Todo eso vive acá ahora — un solo camino, un solo lenguaje,
// pasando por el mismo Storer (DualStore-aware) que el resto del sistema.
//
// Env vars:
//
//	KRONOS_PRETOOL_GATE  — "off" disables entirely (default: on)
//	KRONOS_GATE_BLOCK    — "1"/"true"/"yes" → exit 2 (default: warn, exit 0)
//	KRONOS_GATE_TOOLS    — comma-separated tool names (default: "Edit,Write,Bash")
func RunPreToolUse(ctx context.Context, in Input, st store.Storer) error {
	if os.Getenv("KRONOS_PRETOOL_GATE") == "off" {
		return nil
	}
	if in.SessionID == "" {
		return nil
	}
	gated := resolveGatedTools()
	if !gated[in.ToolName] {
		return nil
	}
	// proyecto sin detectar → el gate no tiene contra qué medir "ya buscaste
	// en este proyecto", así que no tiene sentido bloquear (mismo bypass que
	// tenía el wrapper bash).
	if project.Detect(in.CWD).Name == "unknown" {
		return nil
	}
	sess, err := st.GetSession(ctx, in.SessionID)
	if err != nil || sess == nil {
		return nil // fail-open
	}
	if sess.SearchCount > 0 {
		return nil // gate satisfied
	}
	fmt.Fprintln(os.Stderr, "[kronos] consult kronos before editing. run mem_search with keywords from your task.")
	if isBlockMode() {
		exitFn(2)
	}
	return nil
}

// resolveGatedTools returns the set of tool names that the gate checks.
// Cached in a package-level var; safe since each hook invocation is a fresh process.
func resolveGatedTools() map[string]bool {
	if gatedTools != nil {
		return gatedTools
	}
	env := os.Getenv("KRONOS_GATE_TOOLS")
	if env == "" {
		gatedTools = map[string]bool{"Edit": true, "Write": true, "Bash": true}
		return gatedTools
	}
	m := make(map[string]bool)
	for _, t := range strings.Split(env, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			m[t] = true
		}
	}
	gatedTools = m
	return gatedTools
}

func isBlockMode() bool {
	v := os.Getenv("KRONOS_GATE_BLOCK")
	return v == "1" || v == "true" || v == "yes"
}
