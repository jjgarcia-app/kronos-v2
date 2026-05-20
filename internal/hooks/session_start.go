package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// ReasonCompact is the value of Input.Reason (or Input.Source) that indicates
// the session started after a context compaction event.
const ReasonCompact = "compact"

// RunSessionStart handles the SessionStart hook.
//
// Normal start: emits a 2-line bootstrapping signal only — no observation content.
// Post-compaction (reason == "compact"): delegates to RunPostCompaction.
func RunSessionStart(ctx context.Context, in Input, st store.Storer) error {
	if in.EffectiveReason() == ReasonCompact {
		return RunPostCompaction(ctx, in, st)
	}

	proj := project.Detect(in.CWD)

	_, err := st.CreateSession(ctx, in.SessionID, proj.Name, in.CWD)
	if err != nil {
		// Non-fatal: session may already exist if Claude reconnects.
		_ = err
	}
	if in.SessionID != "" {
		if p, pErr := platform.CurrentSessionPath(); pErr == nil {
			_ = os.WriteFile(p, []byte(in.SessionID), 0o644)
		}
	}

	n, _ := st.CountObservations(ctx, proj.Name)
	fmt.Printf("[kronos] %d observations available for %s\n", n, proj.Name)
	fmt.Println("[kronos] call mem_search with keywords from your task before editing")

	// Persist empty set as dedup baseline for RunPromptSubmit.
	_ = st.PersistInjectedIDs(ctx, in.SessionID, nil)

	return nil
}

// kronosRulesInCLAUDEMD checks whether a CLAUDE.md file (project or global)
// already contains Kronos usage rules. Kept for potential use by other callers.
func kronosRulesInCLAUDEMD(cwd string) bool {
	const marker = "Kronos"

	// Check project-level CLAUDE.md
	if cwd != "" {
		if data, err := os.ReadFile(filepath.Join(cwd, "CLAUDE.md")); err == nil {
			if strings.Contains(string(data), marker) {
				return true
			}
		}
	}

	// Check global ~/.claude/CLAUDE.md
	if claudeDir, err := platform.ClaudeDir(); err == nil {
		if data, err := os.ReadFile(filepath.Join(claudeDir, "CLAUDE.md")); err == nil {
			if strings.Contains(string(data), marker) {
				return true
			}
		}
	}

	return false
}

// kronosRules is retained for reference by other callers.
// It is no longer injected at session start; the bootstrapping signal replaces it.
const kronosRules = `## Kronos — Reglas de memoria (seguir siempre)

1. **Tarea multi-paso → checkpoint inmediato:** Al recibir cualquier tarea que tome más de un turno, llama ` + "`mem_checkpoint`" + ` ahora con ` + "`task`" + ` y ` + "`next_step`" + `. Actualiza el checkpoint después de cada paso completado.
2. **Buscar antes de responder:** Antes de responder sobre historial, decisiones o errores del proyecto → ` + "`mem_search`" + ` primero. No asumas — busca.
3. **Guardar descubrimientos:** Bug resuelto, decisión tomada, algo no obvio encontrado → ` + "`mem_save`" + ` inmediatamente. Usa ` + "`topic_key`" + ` para types decision/architecture/pattern/config.
4. **Cerrar correctamente:** Al terminar, llama ` + "`mem_session_summary`" + `. Si había checkpoint activo: ` + "`mem_checkpoint(status:\"completed\")`" + `.
5. **Contexto perdido → checkpoint:** Si sientes que perdiste contexto de lo que hacías, revisa el bloque "TAREA EN PROGRESO" de más arriba o llama ` + "`mem_context`" + `.
6. **No duplicar:** Usa ` + "`mem_search`" + ` antes de ` + "`mem_save`" + ` para verificar si ya existe. Prefiere ` + "`mem_update`" + ` sobre crear duplicados.

---

`
