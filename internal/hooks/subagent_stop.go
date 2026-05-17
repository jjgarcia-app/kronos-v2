package hooks

import (
	"context"
	"fmt"

	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/secrets"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// RunSubagentStop handles the SubagentStop hook.
// Extracts passive learnings from the subagent's response and saves them.
// This hook runs async so it has no tight timeout constraint.
func RunSubagentStop(ctx context.Context, in Input, st *store.Store) error {
	if in.Response == "" {
		return nil
	}

	items := store.ExtractLearnings(in.Response)
	if len(items) == 0 {
		return nil
	}

	proj := project.Detect(in.CWD)

	for i, item := range items {
		content := secrets.Redact(item)
		_, err := st.SaveObservation(ctx, store.SaveParams{
			SessionID: in.SessionID,
			Type:      store.TypePassive,
			Title:     fmt.Sprintf("Aprendizaje pasivo %d", i+1),
			Content:   content,
			Project:   proj.Name,
			Scope:     store.ScopeProject,
		})
		if err != nil {
			return err
		}
	}
	return nil
}
