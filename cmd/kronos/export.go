package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/obsidian"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
)

func runExport(args []string) error {
	cfg, _ := config.Load()
	outDir := obsidian.ExpandPath(cfg.Export.DefaultOutput)
	project := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--output", "-o":
			if i+1 >= len(args) {
				return fmt.Errorf("--output requires a path")
			}
			i++
			outDir = args[i]
		case "--project", "-p":
			if i+1 >= len(args) {
				return fmt.Errorf("--project requires a name")
			}
			i++
			project = args[i]
		default:
			if !strings.HasPrefix(args[i], "-") {
				outDir = args[i]
			}
		}
	}

	outDir = obsidian.ExpandPath(outDir)

	dbPath, err := platform.DBPath()
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	st, err := openStore(cfg, dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	return obsidian.Export(context.Background(), st, outDir, project)
}
