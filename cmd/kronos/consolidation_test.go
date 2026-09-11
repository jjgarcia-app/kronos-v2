package main

import (
	"context"
	"os"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func newConsolidationTestStore(t *testing.T) *store.Store {
	t.Helper()
	f, err := os.CreateTemp("", "kronos-consolidation-test-*.db")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	s, err := store.New(f.Name())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// (d) el daemon no corre la consolidación con consolidation.enabled=false
// (default de config.Default()).
func TestStartConsolidationLoopIfEnabled_DefaultDisabled(t *testing.T) {
	cfg := config.Default()
	if cfg.Consolidation.Enabled {
		t.Fatal("config.Default() debería traer consolidation.enabled=false")
	}
	st := newConsolidationTestStore(t)

	started := startConsolidationLoopIfEnabled(context.Background(), st, nil, cfg)
	if started {
		t.Error("no debería arrancar el loop con consolidation.enabled=false")
	}
}

func TestStartConsolidationLoopIfEnabled_EnabledStarts(t *testing.T) {
	cfg := config.Default()
	cfg.Consolidation.Enabled = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := newConsolidationTestStore(t)

	started := startConsolidationLoopIfEnabled(ctx, st, nil, cfg)
	if !started {
		t.Error("debería arrancar el loop con consolidation.enabled=true")
	}
}

func TestStartConsolidationLoopIfEnabled_NilStoreNeverStarts(t *testing.T) {
	cfg := config.Default()
	cfg.Consolidation.Enabled = true

	started := startConsolidationLoopIfEnabled(context.Background(), nil, nil, cfg)
	if started {
		t.Error("no debería arrancar el loop sin store local (ej. backend sin sqlite local)")
	}
}
