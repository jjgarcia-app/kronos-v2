package hooks

import (
	"context"
	"fmt"

	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// Run dispatches the named hook using the given store.
// hookName matches the sub-command: session-start, prompt-submit, subagent-stop, session-stop.
// For session-start, the reason field in the hook input JSON selects the branch.
func Run(ctx context.Context, hookName string, st store.Storer, vs *embeddings.VectorStore) error {
	return RunWithReason(ctx, hookName, "", st, vs)
}

// RunWithReason is like Run but accepts an explicit reason that overrides the
// reason field in the hook input JSON for session-start dispatch.
// reason "compact" → post-compaction branch; empty/"startup"/"clear" → normal start.
func RunWithReason(ctx context.Context, hookName string, reason string, st store.Storer, vs *embeddings.VectorStore) error {
	in, err := ReadInput()
	if err != nil {
		return fmt.Errorf("read hook input: %w", err)
	}

	// If an explicit reason was passed via CLI (--reason compact), inject it
	// into the input so RunSessionStart can branch on it.
	if reason != "" {
		in.Reason = reason
	}

	switch hookName {
	case "session-start":
		return RunSessionStart(ctx, in, st)
	case "prompt-submit":
		return RunPromptSubmit(ctx, in, st, vs)
	case "subagent-stop":
		return RunSubagentStop(ctx, in, st)
	case "session-stop":
		return RunSessionStop(ctx, in, st)
	default:
		return fmt.Errorf("hook desconocido: %s", hookName)
	}
}
