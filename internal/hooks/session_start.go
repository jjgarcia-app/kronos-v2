package hooks

import (
	"context"
	"fmt"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/checkpoint"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// RunSessionStart handles the SessionStart hook.
// It detects the project, creates a memory session, and prints context
// to stdout so Claude picks it up at the start of the conversation.
//
// Output order:
//  1. Kronos usage rules — injected always, act as the built-in harness
//  2. Active checkpoint (if any) — re-orientation banner for in-progress tasks
//  3. Recent project observations
func RunSessionStart(ctx context.Context, in Input, st *store.Store) error {
	proj := project.Detect(in.CWD)

	_, err := st.CreateSession(ctx, in.SessionID, proj.Name, in.CWD)
	if err != nil {
		// Non-fatal: session may already exist if Claude reconnects.
		_ = err
	}

	var sb strings.Builder

	// Layer 1 harness — rules injected on every session start.
	// These are read by the agent before any user message, making them
	// the baseline enforcement mechanism even without a CLAUDE.md.
	fmt.Fprintf(&sb, kronosRules)

	// Inject active checkpoint — re-orientation for in-progress tasks
	if dataDir, err := platform.DataDir(); err == nil {
		if cp, err := checkpoint.Load(dataDir, proj.Name); err == nil && cp != nil {
			fmt.Fprintf(&sb, "## TAREA EN PROGRESO — retomar exactamente desde aquí\n\n")
			fmt.Fprintf(&sb, "**Estabas trabajando en:** %s\n\n", cp.Task)
			if cp.Progress != "" {
				fmt.Fprintf(&sb, "**Último paso completado:** %s\n\n", cp.Progress)
			}
			fmt.Fprintf(&sb, "**Próximo paso (ejecutar esto):** %s\n\n", cp.NextStep)
			if cp.Files != "" {
				fmt.Fprintf(&sb, "**Archivos activos:** %s\n\n", cp.Files)
			}
			if cp.Notes != "" {
				fmt.Fprintf(&sb, "**Restricciones/notas importantes:** %s\n\n", cp.Notes)
			}
			fmt.Fprintf(&sb, "_Checkpoint: %s_\n\n", cp.UpdatedAt.Format("2006-01-02 15:04"))
			fmt.Fprintf(&sb, "---\n\n")
		}
	}

	// Recent project observations
	observations, err := st.ListObservations(ctx, proj.Name, 8)
	if err == nil && len(observations) > 0 {
		fmt.Fprintf(&sb, "## Kronos — Contexto previo (%s)\n\n", proj.Name)
		for _, o := range observations {
			fmt.Fprintf(&sb, "**[%d] %s** (%s) — %s\n", o.ID, o.Title, o.Type, o.CreatedAt.Format("2006-01-02"))
			preview := o.Content
			if len(preview) > 120 {
				preview = preview[:117] + "..."
			}
			fmt.Fprintf(&sb, "%s\n\n", preview)
		}
	}

	if sb.Len() > 0 {
		fmt.Print(sb.String())
	}
	return nil
}

// kronosRules is injected at every session start as the built-in harness.
// Kept short so it doesn't bloat context — 6 rules, action-oriented.
const kronosRules = `## Kronos — Reglas de memoria (seguir siempre)

1. **Tarea multi-paso → checkpoint inmediato:** Al recibir cualquier tarea que tome más de un turno, llama ` + "`mem_checkpoint`" + ` ahora con ` + "`task`" + ` y ` + "`next_step`" + `. Actualiza el checkpoint después de cada paso completado.
2. **Buscar antes de responder:** Antes de responder sobre historial, decisiones o errores del proyecto → ` + "`mem_search`" + ` primero. No asumas — busca.
3. **Guardar descubrimientos:** Bug resuelto, decisión tomada, algo no obvio encontrado → ` + "`mem_save`" + ` inmediatamente. Usa ` + "`topic_key`" + ` para types decision/architecture/pattern/config.
4. **Cerrar correctamente:** Al terminar, llama ` + "`mem_session_summary`" + `. Si había checkpoint activo: ` + "`mem_checkpoint(status:\"completed\")`" + `.
5. **Contexto perdido → checkpoint:** Si sientes que perdiste contexto de lo que hacías, revisa el bloque "TAREA EN PROGRESO" de más arriba o llama ` + "`mem_context`" + `.
6. **No duplicar:** Usa ` + "`mem_search`" + ` antes de ` + "`mem_save`" + ` para verificar si ya existe. Prefiere ` + "`mem_update`" + ` sobre crear duplicados.

---

`
