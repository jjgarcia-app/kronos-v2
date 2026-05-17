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
//  1. Active checkpoint (if any) — prominently, as re-orientation banner
//  2. Recent project observations
func RunSessionStart(ctx context.Context, in Input, st *store.Store) error {
	proj := project.Detect(in.CWD)

	_, err := st.CreateSession(ctx, in.SessionID, proj.Name, in.CWD)
	if err != nil {
		// Non-fatal: session may already exist if Claude reconnects.
		_ = err
	}

	var sb strings.Builder

	// Inject active checkpoint first — highest priority context
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
