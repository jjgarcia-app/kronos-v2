package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/judge"
	"github.com/jjgarcia-app/kronos-v2/internal/llm"
	"github.com/jjgarcia-app/kronos-v2/internal/mcp"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/relations"
	httpserver "github.com/jjgarcia-app/kronos-v2/internal/server"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func runServe(args ...string) error {
	// parse --port=N and --tools=PROFILE flags
	port := 4317
	toolsFlag := ""
	for _, a := range args {
		if strings.HasPrefix(a, "--port=") {
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--port="))
			if err == nil && n > 0 {
				port = n
			}
		} else if strings.HasPrefix(a, "--tools=") {
			toolsFlag = strings.TrimPrefix(a, "--tools=")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	// arrancar HTTP server en background; detener con graceful shutdown al salir
	hs := httpserver.New(st, port, cfg.APIToken)
	if err := hs.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "warn: http server no pudo arrancar: %v\n", err)
	}
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		_ = hs.Stop(shutCtx)
	}()

	return runMCPCore(ctx, cfg, st, dataDir, toolsFlag)
}

// runMCP arranca solo el servidor MCP stdio, sin HTTP REST.
// Es el punto de entrada para sesiones de Claude Code: no hay puerto que
// conflictúe, múltiples sesiones pueden correr en paralelo sin problemas.
func runMCP(args ...string) error {
	toolsFlag := ""
	for _, a := range args {
		if strings.HasPrefix(a, "--tools=") {
			toolsFlag = strings.TrimPrefix(a, "--tools=")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	return runMCPCore(ctx, cfg, st, dataDir, toolsFlag)
}

func runMCPCore(ctx context.Context, cfg config.Config, st store.Storer, dataDir, toolsFlag string) error {
	vs, _ := embeddings.New(ctx, filepath.Join(dataDir, "vectors"))
	rel := relations.New(vs)

	var local *store.Store
	if ls, ok := st.(*store.Store); ok {
		local = ls
	} else if ds, ok := st.(interface{ LocalStore() *store.Store }); ok {
		local = ds.LocalStore()
	}

	llmJudger := llm.NewFromConfig(ctx, cfg)

	if rel.Enabled() {
		go reindexRecent(ctx, local, rel)
	}

	toolFilter := mcp.ResolveTools(toolsFlag)
	srv := mcp.NewWithOptions(st, cfg.Nudge.ActionsThreshold, cfg.Nudge.FallbackMinutes, rel, toolFilter)
	srv.SetDataDir(dataDir)
	if ls := srv.LocalStoreForJudge(); ls != nil {
		judge.AutoJudge(ctx, ls, rel, llmJudger)
	}
	return srv.ServeStdio()
}

// reindexRecent indexa en background las observaciones más recientes en el vector store.
// Captura observaciones importadas via sync --import mientras el servidor estaba apagado.
// Usa timeout por item y pausa entre llamadas para no bloquear el read lock de chromem-go
// mientras el MCP server atiende requests concurrentes.
func reindexRecent(ctx context.Context, st *store.Store, rel *relations.Detector) {
	if st == nil || rel == nil {
		return
	}
	obs, err := st.ListRecent(ctx, 200)
	if err != nil {
		return
	}
	for _, o := range obs {
		if ctx.Err() != nil {
			return
		}
		itemCtx, itemCancel := context.WithTimeout(ctx, 30*time.Second)
		_ = rel.Index(itemCtx, o.ID, o.Title+" "+o.Content)
		itemCancel()
		// yield para no saturar el lock de chromem-go: permite que Query() pase entre items
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
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
