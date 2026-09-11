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

const exportUsage = `Uso: kronos export [ruta] [flags]

Exporta las observaciones de kronos a un vault de Obsidian (Markdown).
La exportación es no destructiva: solo toca archivos que ella misma generó
en un export anterior (marcados con "generated_by: kronos-export" en el
frontmatter). Una nota escrita a mano, o un archivo generado que editaste
a mano después de exportarlo, nunca se sobreescriben — se avisan por stderr
y se cuentan aparte en el resumen final.

Flags:
  -o, --output <ruta>     Directorio de salida (default: %s)
  -p, --project <nombre>  Exporta solo un proyecto
      --prune             Borra archivos generados cuya observación de
                           origen ya no existe (nunca borra notas manuales
                           ni archivos generados editados a mano)
      --adopt             Migra al formato nuevo los archivos generados por
                           una versión ANTERIOR del export (sin la marca
                           "generated_by: kronos-export"), verificando que
                           id/project/type y el contenido coincidan con la
                           base antes de tocar nada. No corre el export
                           normal — es un modo aparte, ver --dry-run.
      --dry-run           Junto con --adopt, solo reporta qué se adoptaría
                           sin escribir nada.
  -h, --help               Muestra esta ayuda y no exporta nada

Ejemplos:
  kronos export
  kronos export --project kronos-v2
  kronos export -o ~/otro-vault --prune
  kronos export --adopt --dry-run
  kronos export --adopt
`

func runExport(args []string) error {
	cfg, _ := config.Load()

	if hasHelpFlag(args) {
		fmt.Printf(exportUsage, obsidian.ExpandPath(cfg.Export.DefaultOutput))
		return nil
	}

	outDir := obsidian.ExpandPath(cfg.Export.DefaultOutput)
	project := ""
	opts := obsidian.ExportOptions{}
	adopt := false
	dryRun := false

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
		case "--prune":
			opts.Prune = true
		case "--adopt":
			adopt = true
		case "--dry-run":
			dryRun = true
		default:
			if !strings.HasPrefix(args[i], "-") {
				outDir = args[i]
			}
		}
	}

	if dryRun && !adopt {
		return fmt.Errorf("--dry-run solo tiene efecto junto con --adopt")
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

	if adopt {
		_, err := obsidian.AdoptWithOptions(context.Background(), st, outDir, project, dryRun)
		return err
	}

	// vault.auto_import_on_export (default false): antes de exportar, corre
	// el import en dry-run y avisa conflictos. Nunca escribe en la base por
	// sí solo — el import automático siempre es dry-run, aunque el usuario
	// no haya pasado --dry-run acá (ese flag es de --adopt).
	if cfg.Vault.AutoImportOnExport {
		if _, err := obsidian.ImportVault(context.Background(), st, outDir, obsidian.ImportOptions{
			Apply:              false,
			Project:            project,
			MaxConflictsReport: cfg.Vault.ImportMaxConflictsReport,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "aviso: fallo el import automático antes de exportar: %v\n", err)
		}
	}

	_, err = obsidian.ExportWithOptions(context.Background(), st, outDir, project, opts)
	return err
}
