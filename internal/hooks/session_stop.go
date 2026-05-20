package hooks

import (
	"context"
	"os"

	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// RunSessionStop handles the Stop hook.
// Closes the active memory session. Runs async so failures are non-critical.
func RunSessionStop(ctx context.Context, in Input, st store.Storer) error {
	if in.SessionID == "" {
		return nil
	}
	if p, err := platform.CurrentSessionPath(); err == nil {
		_ = os.Remove(p)
	}
	return st.EndSession(ctx, in.SessionID, "")
}
