package hooks

import (
	"context"
	"fmt"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// RunPostCompaction handles the SessionStart hook when triggered after a
// compaction event. Emits the bootstrapping signal, then injectContinuity
// (see session_start.go) — same content injection RunSessionStart's normal
// path now uses too.
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
	fmt.Println("[kronos] call mem_search with keywords from your task before editing OR before answering questions about past work — don't answer 'I don't know/have no record' from memory alone")

	injectContinuity(ctx, st, proj.Name, in.SessionID)

	return nil
}

// pickRestoreObs returns the k most recent observations for the project,
// ordered by created_at DESC. Used by the post-compaction branch to rebuild
// minimal continuity.
func pickRestoreObs(ctx context.Context, st store.Storer, project, sessionID string, k int) ([]*store.Observation, error) {
	obs, err := st.ListObservations(ctx, project, k, 0)
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
