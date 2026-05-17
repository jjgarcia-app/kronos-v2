package hooks

import (
	"context"
	"fmt"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// RunSessionStart handles the SessionStart hook.
// It detects the project, creates a memory session, and prints recent context
// to stdout so Claude picks it up at the start of the conversation.
func RunSessionStart(ctx context.Context, in Input, st *store.Store) error {
	proj := project.Detect(in.CWD)

	_, err := st.CreateSession(ctx, in.SessionID, proj.Name, in.CWD)
	if err != nil {
		// Non-fatal: session may already exist if Claude reconnects.
		_ = err
	}

	observations, err := st.ListObservations(ctx, proj.Name, 8)
	if err != nil || len(observations) == 0 {
		return nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Kronos — Contexto previo (%s)\n\n", proj.Name)
	for _, o := range observations {
		fmt.Fprintf(&sb, "**[%d] %s** (%s) — %s\n", o.ID, o.Title, o.Type, o.CreatedAt.Format("2006-01-02"))
		preview := o.Content
		if len(preview) > 120 {
			preview = preview[:117] + "..."
		}
		fmt.Fprintf(&sb, "%s\n\n", preview)
	}

	fmt.Print(sb.String())
	return nil
}
