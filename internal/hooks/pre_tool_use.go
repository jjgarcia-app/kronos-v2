package hooks

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
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
// Reset via ResetGatedTools in tests that mutate KRONOS_GATE_TOOLS o la config.
var gatedTools map[string]bool

// ResetGatedTools clears the cached gated tools set.
// Must be called in tests that set KRONOS_GATE_TOOLS via t.Setenv o que
// cambian gate.tools en la config.
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
// Configuración vía config.json (gate.enabled/block/tools/min_observations,
// ver internal/config) con las env vars de siempre ganando si están
// seteadas — para no romper lo ya configurado en ~/.claude/settings.json:
//
//	KRONOS_PRETOOL_GATE  — "off" disables entirely (default: on)
//	KRONOS_GATE_BLOCK    — "1"/"true"/"yes" → exit 2 (default: warn, exit 0)
//	KRONOS_GATE_TOOLS    — comma-separated tool names (default: "Edit,Write,Bash")
//
// Medido en benchmark 2026-09-11: el modo bloqueo agregó 57s (117s -> 174s)
// a una sesión de bugfix — costo real, no gratis. Pero bloquear tiene sentido
// solo si mem_search tiene algo para encontrar: en un proyecto con menos de
// gate.min_observations observaciones (default 5) no hay nada que buscar, así
// que el gate se salta y se loguea en debug (ver resolveMinObservations).
func RunPreToolUse(ctx context.Context, in Input, st store.Storer) error {
	cfg, _ := config.Load()

	if !gateEnabled(cfg) {
		return nil
	}
	if in.SessionID == "" {
		return nil
	}
	gated := resolveGatedTools(cfg)
	if !gated[in.ToolName] {
		return nil
	}
	// proyecto sin detectar → el gate no tiene contra qué medir "ya buscaste
	// en este proyecto", así que no tiene sentido bloquear (mismo bypass que
	// tenía el wrapper bash).
	proj := project.Detect(in.CWD).Name
	if proj == "unknown" {
		return nil
	}
	if count, err := st.CountObservations(ctx, proj); err == nil {
		if min := resolveMinObservations(cfg); count < min {
			slog.Debug("gate: proyecto con pocas observaciones, se salta",
				"project", proj, "observations", count, "min_observations", min)
			return nil
		}
	}
	sess, err := st.GetSession(ctx, in.SessionID)
	if err != nil || sess == nil {
		return nil // fail-open
	}
	if sess.SearchCount > 0 {
		return nil // gate satisfied
	}
	// gate.satisfied_by_injection (default true): si kronos ya inyectó
	// memoria en esta sesión sin que el agente pidiera nada — el bloque core
	// trajo al menos un item del proyecto en SessionStart, o algún recall de
	// UserPromptSubmit inyectó al menos un item — sess.InjectedObservationIDs
	// (misma columna que usa el dedup de recall, ver session_start.go /
	// prompt_submit.go) no está vacía y la sesión ya está informada. Medido
	// 2026-09-11: con el bloque core y el recall por relevancia activos,
	// exigir además una búsqueda explícita es casi siempre redundante — la
	// sesión gasta un turno buscando algo que ya se le mostró.
	if satisfiedByInjection(cfg) && len(sess.InjectedObservationIDs) > 0 {
		slog.Debug("gate: sesión ya recibió memoria inyectada (core block o recall), no bloquea",
			"project", proj, "session_id", in.SessionID, "injected_count", len(sess.InjectedObservationIDs))
		return nil
	}
	// El session_id se incluye literal en el mensaje — mem_search no recibe
	// el session_id real de Claude Code por protocolo MCP y tiene que
	// inferirlo (archivo current_session_<proyecto>.txt o "sesión activa
	// más reciente" en DB); con múltiples sesiones concurrentes del mismo
	// proyecto esa inferencia adivina mal y la búsqueda queda acreditada a
	// la sesión equivocada — el gate sigue bloqueado aunque el agente sí
	// buscó. Pasando session_id explícito acá, en el momento exacto del
	// bloqueo, se elimina la adivinanza.
	//
	// Una sola línea, accionable: session_id + ejemplo de query copiable.
	// El mensaje largo anterior (varias líneas de contexto) no cambiaba la
	// tasa de bloqueo, solo el ruido en stderr.
	fmt.Fprintf(os.Stderr, "[kronos] mem_search primero: session_id=%q query=\"<palabras clave de la tarea>\"\n", in.SessionID)
	if isBlockMode(cfg) {
		slog.Debug("gate: bloqueando tool call sin búsqueda previa", "project", proj, "tool", in.ToolName, "session_id", in.SessionID)
		exitFn(2)
	}
	return nil
}

// gateEnabled resuelve gate.enabled: KRONOS_PRETOOL_GATE="off" gana si está
// seteada (cualquier otro valor, incluido no seteada, deja decidir a la
// config). Backward compatible con el comportamiento pre-config: sin la env
// seteada, antes el gate siempre estaba activo — igual que cfg.Gate.Enabled
// por default (true).
func gateEnabled(cfg config.Config) bool {
	if v, ok := os.LookupEnv("KRONOS_PRETOOL_GATE"); ok {
		return v != "off"
	}
	return cfg.Gate.Enabled
}

// resolveGatedTools returns the set of tool names that the gate checks.
// Cached in a package-level var; safe since each hook invocation is a fresh process.
func resolveGatedTools(cfg config.Config) map[string]bool {
	if gatedTools != nil {
		return gatedTools
	}
	var list []string
	switch {
	case os.Getenv("KRONOS_GATE_TOOLS") != "":
		list = strings.Split(os.Getenv("KRONOS_GATE_TOOLS"), ",")
	case len(cfg.Gate.Tools) > 0:
		list = cfg.Gate.Tools
	default:
		list = []string{"Edit", "Write", "Bash"}
	}
	m := make(map[string]bool, len(list))
	for _, t := range list {
		t = strings.TrimSpace(t)
		if t != "" {
			m[t] = true
		}
	}
	gatedTools = m
	return gatedTools
}

// isBlockMode resuelve gate.block: KRONOS_GATE_BLOCK gana si está seteada
// (mismos valores que siempre: "1"/"true"/"yes"), si no se usa la config.
func isBlockMode(cfg config.Config) bool {
	if v, ok := os.LookupEnv("KRONOS_GATE_BLOCK"); ok {
		return v == "1" || v == "true" || v == "yes"
	}
	return cfg.Gate.Block
}

// resolveMinObservations resuelve gate.min_observations, con 5 como piso si
// la config quedó en 0 (config.Load ya aplica ese default, pero
// RunPreToolUse puede recibir un config.Config armado a mano en tests).
func resolveMinObservations(cfg config.Config) int {
	if cfg.Gate.MinObservations > 0 {
		return cfg.Gate.MinObservations
	}
	return 5
}

// satisfiedByInjection resuelve gate.satisfied_by_injection. Sin env var
// dedicada (a diferencia de enabled/block/tools) — no hay un caso real hoy
// de necesitar pisarla sin tocar config.json.
func satisfiedByInjection(cfg config.Config) bool {
	return cfg.Gate.SatisfiedByInjection
}
