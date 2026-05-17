package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func runGC(args []string) error {
	days := 90
	for _, a := range args {
		if n, err := strconv.Atoi(a); err == nil && n > 0 {
			days = n
		}
	}

	cfg, _ := config.Load()
	if cfg.Memory.RetentionDays > 0 && days == 90 {
		days = cfg.Memory.RetentionDays
	}

	dbPath, err := platform.DBPath()
	if err != nil {
		return fmt.Errorf("db path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return err
	}
	st, err := store.New(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	ctx := context.Background()
	n, err := st.GCStale(ctx, days)
	if err != nil {
		return err
	}
	fmt.Printf("GC completado: %d observaciones eliminadas (sin actualizar en %d días)\n", n, days)

	cleaned, err := st.GCRelations(ctx, 30)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: gc relations: %v\n", err)
	} else if cleaned > 0 {
		fmt.Printf("GC relaciones: %d relaciones eliminadas (dangling o pending > 30 días)\n", cleaned)
	}
	return nil
}
