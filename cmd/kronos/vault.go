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

const vaultImportUsage = `Uso: kronos vault import [flags]

Camino de vuelta del export: trae a la base los cambios hechos a mano en el
vault de Obsidian. Para cada archivo marcado "generated_by: kronos-export",
compara su contenido actual contra lo que el export generaría hoy a partir
de la observación en la base:

  - si coincide, no hay nada que hacer;
  - si difiere porque el archivo fue editado a mano y la observación NO
    cambió en la base desde ese export, es una edición manual: se actualiza
    el contenido (y el título, si cambió el H1) y sube revision_count;
  - si la observación TAMBIÉN cambió en la base, es un conflicto: no se
    toca nada, se reporta el par para resolución manual.

Nunca toca tipo/proyecto/scope/topic_key (son metadatos de kronos, no del
vault). Sin --apply no escribe nada en la base: solo reporta (default).

Flags:
  --vault <dir>       Directorio del vault (default: export.default_output)
  --project <nombre>  Solo importa observaciones de este proyecto
  --dry-run           No escribe nada (default explícito)
  --apply             Aplica los cambios detectados a la base
  -h, --help          Muestra esta ayuda y no hace nada

Ejemplos:
  kronos vault import
  kronos vault import --apply
  kronos vault import --project kronos-v2 --apply
`

func runVault(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("uso: kronos vault import [flags] — ver \"kronos vault import --help\"")
	}
	switch args[0] {
	case "import":
		return runVaultImport(args[1:])
	default:
		return fmt.Errorf("subcomando desconocido %q — usa: import", args[0])
	}
}

func runVaultImport(args []string) error {
	cfg, _ := config.Load()

	if hasHelpFlag(args) {
		fmt.Print(vaultImportUsage)
		return nil
	}

	vaultDir := obsidian.ExpandPath(cfg.Export.DefaultOutput)
	project := ""
	apply := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--vault":
			if i+1 >= len(args) {
				return fmt.Errorf("--vault requiere un directorio")
			}
			i++
			vaultDir = args[i]
		case "--project", "-p":
			if i+1 >= len(args) {
				return fmt.Errorf("--project requiere un nombre")
			}
			i++
			project = args[i]
		case "--apply":
			apply = true
		case "--dry-run":
			// default explícito: no-op.
		default:
			if !strings.HasPrefix(args[i], "-") {
				vaultDir = args[i]
			}
		}
	}

	vaultDir = obsidian.ExpandPath(vaultDir)

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

	_, err = obsidian.ImportVault(context.Background(), st, vaultDir, obsidian.ImportOptions{
		Apply:              apply,
		Project:            project,
		MaxConflictsReport: cfg.Vault.ImportMaxConflictsReport,
	})
	return err
}
