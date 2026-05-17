package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func runSync() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("cargar config: %w", err)
	}

	if cfg.DB.Backend != "postgres" || cfg.DB.PostgresDSN == "" {
		fmt.Println("sync solo aplica cuando el backend es postgres.")
		return nil
	}

	dbPath, err := platform.DBPath()
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	buffer, err := store.New(dbPath)
	if err != nil {
		return fmt.Errorf("abrir buffer sqlite: %w", err)
	}

	dual, err := store.NewDualFromDSN(buffer, cfg.DB.PostgresDSN)
	if err != nil {
		return fmt.Errorf("init dual store: %w", err)
	}
	defer dual.Close()

	pending := dual.PendingCount()
	if pending == 0 {
		fmt.Println("No hay operaciones pendientes de sincronizar.")
		return nil
	}

	fmt.Printf("Sincronizando %d operaciones pendientes con PostgreSQL...\n", pending)

	ctx := context.Background()
	flushed, syncErr := dual.FlushPendingVerbose(ctx)
	if syncErr != nil {
		return fmt.Errorf("sync falló: %w", syncErr)
	}
	if !flushed {
		return fmt.Errorf("no se pudo conectar a PostgreSQL en %s", cfg.DB.PostgresDSN)
	}

	remaining := dual.PendingCount()
	synced := pending - remaining
	fmt.Printf("Sincronizadas: %d  |  Pendientes: %d\n", synced, remaining)
	if remaining == 0 {
		fmt.Println("Sincronización completa.")
	}
	return nil
}
