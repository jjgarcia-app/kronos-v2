package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/mcp"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/relations"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func runServe() error {
	ctx := context.Background()

	dbPath, err := platform.DBPath()
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}

	dataDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	st, err := store.New(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// Embeddings are optional — if Ollama is not running, vs is nil and
	// the MCP server operates without semantic search.
	vs, _ := embeddings.New(ctx, filepath.Join(dataDir, "vectors"))
	rel := relations.New(vs)

	srv := mcp.NewWithRelations(st, 10, 20, rel)
	return srv.ServeStdio()
}
