package hooks

import (
	"context"
	"fmt"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// Run dispatches the named hook using the given store.
// hookName matches the sub-command: session-start, prompt-submit, subagent-stop, session-stop.
// For session-start, an optional reason argument ("compact") selects RunPostCompaction.
func Run(ctx context.Context, hookName string, st *store.Store) error {
	return RunWithReason(ctx, hookName, "", st)
}

// RunWithReason is like Run but accepts a reason that modifies dispatch for session-start.
// reason "compact" → RunPostCompaction; empty/"startup"/"clear" → RunSessionStart.
func RunWithReason(ctx context.Context, hookName string, reason string, st *store.Store) error {
	in, err := ReadInput()
	if err != nil {
		return fmt.Errorf("read hook input: %w", err)
	}

	switch hookName {
	case "session-start":
		if reason == "compact" {
			return RunPostCompaction(ctx, in, st)
		}
		return RunSessionStart(ctx, in, st)
	case "prompt-submit":
		return RunPromptSubmit(ctx, in, st)
	case "subagent-stop":
		return RunSubagentStop(ctx, in, st)
	case "session-stop":
		return RunSessionStop(ctx, in, st)
	default:
		return fmt.Errorf("hook desconocido: %s", hookName)
	}
}
