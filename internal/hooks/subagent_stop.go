package hooks

import (
	"context"

	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/secrets"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// RunSubagentStop handles the SubagentStop hook.
// Extracts passive learnings from the subagent's response and saves them
// using SavePassive, which auto-generates a meaningful title from the content.
func RunSubagentStop(ctx context.Context, in Input, st *store.Store) error {
	if in.Response == "" {
		return nil
	}

	items := store.ExtractLearnings(in.Response)
	if len(items) == 0 {
		return nil
	}

	proj := project.Detect(in.CWD)

	for _, item := range items {
		content := secrets.Redact(item)
		if _, err := st.SavePassive(ctx, in.SessionID, proj.Name, content); err != nil {
			return err
		}
	}
	return nil
}
