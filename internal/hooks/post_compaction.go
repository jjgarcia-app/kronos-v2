package hooks

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/checkpoint"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// RunPostCompaction handles the SessionStart hook when triggered after a
// compaction event. Emits the bootstrapping signal, an active checkpoint
// (if any), and up to 3 most-recent observations for the project.
func RunPostCompaction(ctx context.Context, in Input, st store.Storer) error {
	proj := project.Detect(in.CWD)

	_, err := st.CreateSession(ctx, in.SessionID, proj.Name, in.CWD)
	if err != nil {
		// Non-fatal: session may already exist if Claude reconnects.
		_ = err
	}

	// Bootstrapping signal — same as normal start.
	n, _ := st.CountObservations(ctx, proj.Name)
	fmt.Printf("[kronos] %d observations available for %s\n", n, proj.Name)
	fmt.Println("[kronos] call mem_search with keywords from your task before editing")

	// Active checkpoint — brief single-line re-orientation.
	if dataDir, err := platform.DataDir(); err == nil {
		if cp, err := checkpoint.Load(dataDir, proj.Name); err == nil && cp != nil {
			fmt.Printf("[kronos] active task: %s | next: %s\n", cp.Task, cp.NextStep)
		}
	}

	// Up to 3 most-recent observations.
	obs, err := pickRestoreObs(ctx, st, proj.Name, in.SessionID, 3)
	if err == nil && len(obs) > 0 {
		var injectedIDs []string
		for _, o := range obs {
			fmt.Printf("[kronos] %s (%s): %s\n", o.Title, o.Type, preview80(o.Content))
			injectedIDs = append(injectedIDs, strconv.FormatInt(o.ID, 10))
		}
		_ = st.PersistInjectedIDs(ctx, in.SessionID, injectedIDs)
	} else {
		_ = st.PersistInjectedIDs(ctx, in.SessionID, nil)
	}

	return nil
}

// pickRestoreObs returns the k most recent observations for the project,
// ordered by created_at DESC. Used by the post-compaction branch to rebuild
// minimal continuity.
func pickRestoreObs(ctx context.Context, st store.Storer, project, sessionID string, k int) ([]*store.Observation, error) {
	obs, err := st.ListObservations(ctx, project, k)
	if err != nil {
		return nil, err
	}
	return obs, nil
}

// preview80 returns the first 80 characters of s, appending "..." if truncated.
func preview80(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 80 {
		return s
	}
	return s[:77] + "..."
}
