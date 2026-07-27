package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/judge"
	"github.com/jjgarcia-app/kronos-v2/internal/llm"
	"github.com/jjgarcia-app/kronos-v2/internal/mcp"
	"github.com/jjgarcia-app/kronos-v2/internal/obsidian"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/relations"
	httpserver "github.com/jjgarcia-app/kronos-v2/internal/server"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
	mcpgoserver "github.com/mark3labs/mcp-go/server"
)

func runServe(args ...string) error {
	// parse --port=N, --tools=PROFILE y --daemon-mode (uso interno, ver mcp_proxy.go)
	port := 4317
	toolsFlag := ""
	daemonMode := false
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--port="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--port="))
			if err == nil && n > 0 {
				port = n
			}
		case strings.HasPrefix(a, "--tools="):
			toolsFlag = strings.TrimPrefix(a, "--tools=")
		case a == "--daemon-mode":
			daemonMode = true
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

	if daemonMode {
		// el daemon no tiene consola propia (proceso detached) — todo warn/log
		// va a un archivo en vez de perderse o colgar esperando un stdout que
		// nadie lee.
		if err := redirectLogsToFile(filepath.Join(dataDir, "daemon.log")); err != nil {
			return fmt.Errorf("redirect daemon logs: %w", err)
		}
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt)
		go func() {
			<-sigCh
			cancel()
		}()
	}

	st, err := openStore(cfg, dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// en modo daemon el filtro de tools se aplica del lado del proxy (kronos
	// mcp), no acá — el daemon siempre expone el set completo.
	effectiveToolsFlag := toolsFlag
	if daemonMode {
		effectiveToolsFlag = ""
	}
	mcpSrv, err := buildMCPServer(ctx, cfg, st, dataDir, effectiveToolsFlag)
	if err != nil {
		return fmt.Errorf("build mcp server: %w", err)
	}

	hs := httpserver.New(st, port, cfg.APIToken)
	if daemonMode {
		streamable := mcpgoserver.NewStreamableHTTPServer(mcpSrv.MCPServer())
		hs.Handle("/mcp", streamable)
	}
	if err := hs.Start(); err != nil {
		if daemonMode {
			return fmt.Errorf("bind :%d: %w (¿ya hay un daemon de kronos corriendo?)", port, err)
		}
		fmt.Fprintf(os.Stderr, "warn: http server no pudo arrancar: %v\n", err)
	}
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		_ = hs.Stop(shutCtx)
	}()

	if daemonMode {
		fmt.Fprintf(os.Stderr, "kronos daemon listo — MCP en http://127.0.0.1:%d/mcp\n", port)
		<-ctx.Done()
		return nil
	}

	return mcpSrv.ServeStdio()
}

// runMCP vive en cmd/kronos/mcp_proxy.go — proxy stdio hacia el daemon
// compartido (Fase 3 del plan de daemon único).

// buildMCPServer arma todo el estado del servidor MCP (embeddings, detector
// de relaciones, reindex incremental, AutoJudge) sin servirlo — el caller
// decide el transporte (stdio o StreamableHTTP).
func buildMCPServer(ctx context.Context, cfg config.Config, st store.Storer, dataDir, toolsFlag string) (*mcp.Server, error) {
	vs, _ := embeddings.New(ctx, filepath.Join(dataDir, "vectors"))
	rel := relations.New(vs)

	var local *store.Store
	if ls, ok := st.(*store.Store); ok {
		local = ls
	} else if ds, ok := st.(interface{ LocalStore() *store.Store }); ok {
		local = ds.LocalStore()
	}

	llmJudger := llm.NewFromConfig(ctx, cfg)

	// reindexDone se cierra cuando reindexRecent termina. AutoJudge lo espera
	// antes de arrancar su loop — evita que ambos peguen contra Ollama al
	// mismo tiempo (reindexado en frío puede tardar minutos con cientos de
	// observaciones sin indexar, mucho más que el delay fijo de AutoJudge).
	var reindexDone chan struct{}
	if rel.Enabled() {
		reindexDone = make(chan struct{})
		go func() {
			defer close(reindexDone)
			reindexRecent(ctx, local, rel)
		}()
	}

	toolFilter := mcp.ResolveTools(toolsFlag)
	srv := mcp.NewWithOptions(st, cfg.Nudge.ActionsThreshold, cfg.Nudge.FallbackMinutes, rel, toolFilter)
	srv.SetDataDir(dataDir)
	if ls := srv.LocalStoreForJudge(); ls != nil {
		judge.AutoJudge(ctx, ls, rel, llmJudger, reindexDone)
	}
	return srv, nil
}

// redirectLogsToFile manda stdout/stderr del proceso a un archivo — usado en
// modo daemon, donde no hay consola que lea nada. Rotación simple: si el
// archivo ya pesa más de 10MB, se corre a .1 antes de abrir el nuevo.
func redirectLogsToFile(path string) error {
	const maxSize = 10 * 1024 * 1024
	if info, err := os.Stat(path); err == nil && info.Size() > maxSize {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	os.Stdout = f
	os.Stderr = f
	return nil
}

// reindexRecent indexa en background las observaciones que todavía no tienen
// embedding en el vector store — sin techo, cubre todo el historial, no solo
// las últimas 200. Es incremental: rel.Has() salta en el acto lo que ya está
// indexado (sin llamar a Ollama), así que en cada arranque normal esto es
// barato — solo el primer arranque tras un hueco real de indexación (o una
// importación de sync --import) hace trabajo pesado.
// Usa timeout por item y pausa entre llamadas para no bloquear el read lock de chromem-go
// mientras el MCP server atiende requests concurrentes.
func reindexRecent(ctx context.Context, st *store.Store, rel *relations.Detector) {
	if st == nil || rel == nil {
		return
	}
	obs, err := st.ListAll(ctx, "")
	if err != nil {
		return
	}
	indexed := 0
	for _, o := range obs {
		if ctx.Err() != nil {
			return
		}
		if rel.Has(ctx, o.ID) {
			continue
		}
		itemCtx, itemCancel := context.WithTimeout(ctx, 30*time.Second)
		if err := rel.Index(itemCtx, o.ID, o.Title+" "+o.Content); err == nil {
			indexed++
		}
		itemCancel()
		// yield para no saturar el lock de chromem-go: permite que Query() pase entre items
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	if indexed > 0 {
		fmt.Fprintf(os.Stderr, "reindex: %d observaciones nuevas indexadas (de %d totales)\n", indexed, len(obs))
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
		return wrapExportMirror(local, cfg), nil
	}

	dual, err := store.NewDualFromDSN(local, cfg.DB.PostgresDSN)
	if err != nil {
		// sync_queue table couldn't be created — extremely unlikely
		fmt.Fprintf(os.Stderr, "warn: dual store init failed (%v) — usando solo sqlite\n", err)
		return wrapExportMirror(local, cfg), nil
	}
	return wrapExportMirror(dual, cfg), nil
}

// wrapExportMirror envuelve st en un obsidian.MirrorStore cuando el usuario
// activó export.enabled — cada save/update/delete se refleja en vivo en el
// vault de Obsidian (export.default_output), en vez de depender del dump
// manual `kronos export` que queda desactualizado apenas se corre una vez.
func wrapExportMirror(st store.Storer, cfg config.Config) store.Storer {
	if !cfg.Export.Enabled {
		return st
	}
	return obsidian.NewMirrorStore(st, obsidian.ExpandPath(cfg.Export.DefaultOutput))
}
