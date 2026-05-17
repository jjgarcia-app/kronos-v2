package main

import (
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
	"github.com/jjgarcia-app/kronos-v2/internal/tui"
)

func runTUI() error {
	cfg, _ := config.Load()

	dbPath := resolveDBPath(cfg)

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	st, err := store.New(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	m := tui.New(st, cfg)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func resolveDBPath(cfg config.Config) string {
	if cfg.DB.SQLitePath != "" {
		return cfg.DB.SQLitePath
	}
	path, err := platform.DBPath()
	if err != nil {
		return "kronos.db"
	}
	return path
}
