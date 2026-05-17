package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/mcp"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/relations"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func runServe() error {
	ctx := context.Background()

	cfg, _ := config.Load()

	dbPath, err := platform.DBPath()
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}

	dataDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	st, err := openStore(cfg, dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	vs, _ := embeddings.New(ctx, filepath.Join(dataDir, "vectors"))
	rel := relations.New(vs)

	srv := mcp.NewWithRelations(st, cfg.Nudge.ActionsThreshold, cfg.Nudge.FallbackMinutes, rel)
	srv.SetDataDir(dataDir)
	return srv.ServeStdio()
}

// openStore returns the appropriate Storer for the configured backend.
//
// When backend=postgres, creates a DualStore: local SQLite is the source of
// truth, PostgreSQL is an async replica. The remote connection is lazy — the
// server starts immediately even if postgres is unavailable, and the sync
// goroutine retries following the staged backoff in store.retrySchedule.
func openStore(cfg config.Config, localDBPath string) (store.Storer, error) {
	local, err := store.New(localDBPath)
	if err != nil {
		return nil, fmt.Errorf("open local sqlite: %w", err)
	}

	if cfg.DB.Backend != "postgres" || cfg.DB.PostgresDSN == "" {
		return local, nil
	}

	dual, err := store.NewDualFromDSN(local, cfg.DB.PostgresDSN)
	if err != nil {
		// sync_queue table couldn't be created — extremely unlikely
		fmt.Fprintf(os.Stderr, "warn: dual store init failed (%v) — usando solo sqlite\n", err)
		return local, nil
	}
	return dual, nil
}
