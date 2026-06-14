package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
	kronosSync "github.com/jjgarcia-app/kronos-v2/internal/sync"
)

// runSync maneja el subcomando `kronos sync`.
//
// Modos:
//
//	kronos sync                              → export (modo default)
//	kronos sync --export [--project=name] [--dir=path]
//	kronos sync --import [--dir=path]
//	kronos sync --pg-flush                   → flush pending ops to PostgreSQL
//
// --dir apunta al directorio raíz del proyecto (default: cwd).
// Los archivos de sync viven en {dir}/.kronos/.
func runSync(args []string) error {
	var (
		doExport  = false
		doImport  = false
		doPGFlush = false
		project   = ""
		targetDir = ""
		createdBy = "local"
	)

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--export" || args[i] == "-e":
			doExport = true
		case args[i] == "--import" || args[i] == "-i":
			doImport = true
		case args[i] == "--pg-flush":
			doPGFlush = true
		case strings.HasPrefix(args[i], "--project="):
			project = strings.TrimPrefix(args[i], "--project=")
		case args[i] == "--project" || args[i] == "-p":
			if i+1 >= len(args) {
				return fmt.Errorf("--project requiere un nombre")
			}
			i++
			project = args[i]
		case strings.HasPrefix(args[i], "--dir="):
			targetDir = strings.TrimPrefix(args[i], "--dir=")
		case args[i] == "--dir" || args[i] == "-d":
			if i+1 >= len(args) {
				return fmt.Errorf("--dir requiere una ruta")
			}
			i++
			targetDir = args[i]
		case strings.HasPrefix(args[i], "--created-by="):
			createdBy = strings.TrimPrefix(args[i], "--created-by=")
		}
	}

	// --pg-flush: drain the SQLite sync_queue to PostgreSQL
	if doPGFlush {
		return runPGFlush()
	}

	// default: export
	if !doExport && !doImport {
		doExport = true
	}

	// resolver directorio
	if targetDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("obtener cwd: %w", err)
		}
		targetDir = cwd
	}
	targetDir = filepath.Clean(targetDir)

	// abrir store
	dbPath, err := platform.DBPath()
	if err != nil {
		return fmt.Errorf("resolver db path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return fmt.Errorf("crear data dir: %w", err)
	}

	st, err := store.New(dbPath)
	if err != nil {
		return fmt.Errorf("abrir store: %w", err)
	}
	defer st.Close()

	syncer := kronosSync.New(st, targetDir)

	if doExport {
		result, err := syncer.Export(createdBy, project)
		if err != nil {
			return fmt.Errorf("export: %w", err)
		}
		if result.IsEmpty {
			fmt.Println("Nada nuevo para exportar.")
			return nil
		}
		fmt.Printf("Chunk exportado: %s\n", result.ChunkID)
		fmt.Printf("  Sessions: %d  |  Memories: %d  |  Prompts: %d\n",
			result.Sessions, result.Memories, result.Prompts)
		fmt.Printf("  Directorio: %s/.kronos/\n", targetDir)
		return nil
	}

	if doImport {
		result, err := syncer.Import()
		if err != nil {
			return fmt.Errorf("import: %w", err)
		}
		if result.Chunks == 0 && result.Skipped == 0 {
			fmt.Println("No hay chunks para importar.")
			return nil
		}
		fmt.Printf("Import completado:\n")
		fmt.Printf("  Chunks aplicados: %d  |  Ya importados: %d\n", result.Chunks, result.Skipped)
		fmt.Printf("  Sessions: %d  |  Memories: %d  |  Prompts: %d\n",
			result.Sessions, result.Memories, result.Prompts)
		return nil
	}

	return nil
}

// runPGFlush drains the SQLite sync_queue to PostgreSQL.
// Called by `kronos sync --pg-flush`.
func runPGFlush() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("cargar config: %w", err)
	}
	if cfg.DB.Backend != "postgres" {
		fmt.Println("Backend no es PostgreSQL — nada que sincronizar.")
		return nil
	}

	dbPath, err := platform.DBPath()
	if err != nil {
		return fmt.Errorf("resolver db path: %w", err)
	}
	if cfg.DB.SQLitePath != "" {
		dbPath = cfg.DB.SQLitePath
	}

	buffer, err := store.New(dbPath)
	if err != nil {
		return fmt.Errorf("abrir store local: %w", err)
	}

	dual, err := store.NewDualFromDSN(buffer, cfg.DB.PostgresDSN)
	if err != nil {
		return fmt.Errorf("abrir dual store: %w", err)
	}
	defer dual.Close()

	ctx := context.Background()
	pending := dual.PendingCount()
	if pending == 0 {
		fmt.Println("Sync queue vacía — nada que sincronizar.")
		return nil
	}

	fmt.Printf("Sincronizando %d operación(es) pendiente(s) a PostgreSQL...\n", pending)
	ok, flushErr := dual.FlushPendingVerbose(ctx)
	if !ok {
		return fmt.Errorf("flush a PostgreSQL falló: %w", flushErr)
	}

	remaining := dual.PendingCount()
	flushed := pending - remaining
	fmt.Printf("Sincronizadas: %d  |  Pendientes restantes: %d\n", flushed, remaining)
	return nil
}
