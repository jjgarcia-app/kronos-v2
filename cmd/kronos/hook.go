package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func runHook(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("uso: kronos hook <session-start|prompt-submit|subagent-stop|session-stop>")
	}

	dbPath, err := platform.DBPath()
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	st, err := store.New(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	return hooks.Run(context.Background(), args[0], st)
}
