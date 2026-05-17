package hooks

import (
	"context"

	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/secrets"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// RunPromptSubmit handles the UserPromptSubmit hook.
// Saves the prompt (redacted) for future context. Must complete within 2 seconds.
func RunPromptSubmit(ctx context.Context, in Input, st *store.Store) error {
	if in.Prompt == "" {
		return nil
	}

	proj := project.Detect(in.CWD)
	content := secrets.Redact(in.Prompt)

	return st.SavePrompt(ctx, in.SessionID, proj.Name, content)
}
