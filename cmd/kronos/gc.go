package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/consolidate"
	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	kproject "github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/relations"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

const gcUsage = `Uso: kronos gc [días]
       kronos gc --consolidate [--dry-run|--no-dry-run] [--project NOMBRE]
                                [--threshold 0.93] [--max-pairs 50] [--no-embeddings]

Sin --consolidate: elimina observaciones sin actualizar hace más de <días>
(default: 90, o memory.retention_days en config.json) y relaciones
huérfanas/pendientes de más de 30 días. Es destructivo: borra filas del store.

  -h, --help  Muestra esta ayuda y no borra nada

Ejemplos:
  kronos gc
  kronos gc 30

--consolidate: busca observaciones semánticamente duplicadas dentro del
MISMO proyecto y del MISMO tipo. Primero compara por topic_key (gratis, en
memoria); recién después gasta llamadas al proveedor de embeddings
(internal/embeddings, típicamente Ollama) en lo que topic_key no resolvió —
cada observación evaluada así es una llamada de red, así que ese paso está
topeado por --max-pairs. Nunca borra filas: la superviviente sube su
revision_count y la reemplazada queda marcada con una relación "supersedes"
(mem_judge) — ambas siguen existiendo y son consultables.

  --dry-run           Solo reporta candidatos, no escribe nada (default)
  --no-dry-run        Aplica la fusión de los pares candidatos
  --project NOMBRE    Limita la consolidación a un proyecto
  --threshold N       Similitud coseno mínima (default: 0.93, o
                      consolidation.threshold en config.json)
  --max-pairs N       Tope de observaciones consultadas contra el proveedor
                      de embeddings en esta corrida (default: 50). No afecta
                      la comparación por topic_key, que siempre corre entera.
  --no-embeddings     Corre solo el camino topic_key — ni una llamada al
                      proveedor de embeddings. Milisegundos en vez de minutos;
                      forma rápida de tener un primer reporte.

Ejemplos:
  kronos gc --consolidate --dry-run --no-embeddings --project mi-proyecto
  kronos gc --consolidate --dry-run --max-pairs 20 --project mi-proyecto
  kronos gc --consolidate --no-dry-run --project mi-proyecto --threshold 0.95
`

func runGC(args []string) error {
	if hasHelpFlag(args) {
		fmt.Print(gcUsage)
		return nil
	}

	if containsArg(args, "--consolidate") {
		return runGCConsolidate(args)
	}

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

func containsArg(args []string, target string) bool {
	for _, a := range args {
		if a == target {
			return true
		}
	}
	return false
}

// runGCConsolidate implementa `kronos gc --consolidate`. dry-run por default:
// hace falta --no-dry-run explícito para que escriba algo (ver consolidate.Run).
func runGCConsolidate(args []string) error {
	if hasHelpFlag(args) {
		fmt.Print(gcUsage)
		return nil
	}

	cfg, _ := config.Load()

	dryRun := true
	project := ""
	threshold := float32(cfg.Consolidation.Threshold)
	maxPairs := consolidate.DefaultMaxPairs
	noEmbeddings := false

	for i, a := range args {
		switch {
		case a == "--consolidate":
			// consumido por runGC, nada que hacer acá
		case a == "--dry-run":
			dryRun = true
		case a == "--no-dry-run":
			dryRun = false
		case a == "--no-embeddings":
			noEmbeddings = true
		case a == "--project":
			if i+1 < len(args) {
				project = args[i+1]
			}
		case strings.HasPrefix(a, "--project="):
			project = strings.TrimPrefix(a, "--project=")
		case a == "--threshold":
			if i+1 < len(args) {
				if f, err := strconv.ParseFloat(args[i+1], 32); err == nil {
					threshold = float32(f)
				}
			}
		case strings.HasPrefix(a, "--threshold="):
			if f, err := strconv.ParseFloat(strings.TrimPrefix(a, "--threshold="), 32); err == nil {
				threshold = float32(f)
			}
		case a == "--max-pairs":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
					maxPairs = n
				}
			}
		case strings.HasPrefix(a, "--max-pairs="):
			if n, err := strconv.Atoi(strings.TrimPrefix(a, "--max-pairs=")); err == nil && n > 0 {
				maxPairs = n
			}
		}
	}
	if project != "" {
		project = kproject.Normalize(project)
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
	var rel *relations.Detector
	if noEmbeddings {
		// --no-embeddings: ni siquiera abrimos el vector store (evita el ping
		// a Ollama) — la corrida queda en milisegundos, solo topic_key.
		rel = relations.New(nil)
	} else {
		dataDir := filepath.Dir(dbPath)
		// mismo directorio de vectores que usa el daemon (ver buildMCPServer
		// en serve.go) — si el daemon ya indexó, gc --consolidate ve esos
		// embeddings sin necesitar que el daemon esté corriendo ahora.
		vs, _ := embeddings.New(ctx, filepath.Join(dataDir, "vectors"))
		rel = relations.New(vs)
	}

	report, err := consolidate.Run(ctx, st, rel, consolidate.Options{
		Project:            project,
		Threshold:          threshold,
		RequireSameType:    cfg.Consolidation.RequireSameType,
		RequireSameProject: cfg.Consolidation.RequireSameProject,
		DryRun:             dryRun,
		NoEmbeddings:       noEmbeddings,
		MaxPairs:           maxPairs,
	})
	if err != nil {
		return fmt.Errorf("consolidar: %w", err)
	}

	printConsolidateReport(report)
	return nil
}

func printConsolidateReport(r *consolidate.Report) {
	mode := "dry-run"
	if !r.DryRun {
		mode = "ejecutado"
	}
	source := "topic_key (sin proveedor de embeddings)"
	if r.EmbeddingsUsed {
		source = fmt.Sprintf("topic_key + similitud semántica (%d evaluadas, %d fuera del tope --max-pairs)",
			r.PairsEvaluated, r.PairsSkippedByCap)
	}
	fmt.Printf("Consolidación (%s, %s): %d pares candidatos, %d fusionados\n", mode, source, len(r.Pairs), r.Merged)
	for _, p := range r.Pairs {
		status := "candidato"
		if p.Applied {
			status = "fusionado"
		}
		fmt.Printf("  [%s] superviviente=#%d %q <- reemplazada=#%d %q (project=%s type=%s) %s\n",
			status, p.SurvivorID, p.SurvivorTitle, p.ReplacedID, p.ReplacedTitle, p.Project, p.Type, p.Reason)
	}
	if r.DryRun && len(r.Pairs) > 0 {
		fmt.Println("\nNada se escribió (dry-run). Usá --no-dry-run para aplicar la fusión.")
	}
}
