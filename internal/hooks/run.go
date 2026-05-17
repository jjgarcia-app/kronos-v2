package hooks

import (
	"context"
	"fmt"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// Run dispatches the named hook using the given store.
// hookName matches the sub-command: session-start, prompt-submit, subagent-stop, session-stop.
func Run(ctx context.Context, hookName string, st *store.Store) error {
	in, err := ReadInput()
	if err != nil {
		return fmt.Errorf("read hook input: %w", err)
	}

	switch hookName {
	case "session-start":
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
