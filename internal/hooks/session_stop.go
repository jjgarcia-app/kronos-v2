package hooks

import (
	"context"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// RunSessionStop handles the Stop hook.
// Closes the active memory session. Runs async so failures are non-critical.
func RunSessionStop(ctx context.Context, in Input, st *store.Store) error {
	if in.SessionID == "" {
		return nil
	}
	return st.EndSession(ctx, in.SessionID, "")
}
