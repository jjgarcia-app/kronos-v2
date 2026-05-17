package hooks

import (
	"context"
	"fmt"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// RunPostCompaction handles the SessionStart hook when triggered after a
// compaction event. It injects a recovery-oriented header and mandatory
// instructions for the model to reconstruct context.
//
// Output order:
//  1. Post-compaction recovery header with mandatory tool-call instructions
//  2. Recent project observations (max 8)
func RunPostCompaction(ctx context.Context, in Input, st *store.Store) error {
	proj := project.Detect(in.CWD)

	_, err := st.CreateSession(ctx, in.SessionID, proj.Name, in.CWD)
	if err != nil {
		// Non-fatal: session may already exist if Claude reconnects.
		_ = err
	}

	var sb strings.Builder

	fmt.Fprintf(&sb, "## Kronos — Recuperación post-compactación\n\n")
	fmt.Fprintf(&sb, "El contexto de conversación fue compactado. Para recuperar el estado de trabajo, ejecuta **en este orden**:\n\n")
	fmt.Fprintf(&sb, "1. Llama `mem_session_summary` con el resumen de lo que se compactó\n")
	fmt.Fprintf(&sb, "2. Llama `mem_context` para recuperar contexto adicional\n")
	fmt.Fprintf(&sb, "3. Llama `mem_search` si necesitas información específica\n")
	fmt.Fprintf(&sb, "4. Continúa el trabajo desde donde estabas\n\n")
	fmt.Fprintf(&sb, "---\n\n")

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
