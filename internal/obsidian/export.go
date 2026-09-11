package obsidian

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// ExportOptions configura el comportamiento de Export además del filtro de
// proyecto, que ya viaja como parámetro propio por compatibilidad.
type ExportOptions struct {
	// Prune borra archivos generados cuya observación de origen ya no
	// existe (fue borrada en el store). Nunca borra notas escritas a mano
	// ni archivos generados editados a mano.
	Prune bool
}

// ExportStats resume el resultado de un export no destructivo.
type ExportStats struct {
	Written       int
	Unchanged     int
	SkippedManual []string
	Pruned        int
}

// Export escribe todas las observaciones no borradas en outDir como un
// vault de Obsidian. Si project no es vacío, solo se exportan las
// observaciones de ese proyecto. Wrapper de compatibilidad sobre
// ExportWithOptions — ver esa función para el detalle del comportamiento
// no destructivo.
//
//	<outDir>/
//	  _index.md                        ← índice maestro (siempre se regenera)
//	  <project>/_core.md               ← bloque core siempre-presente del proyecto
//	  <project>/<type>/<id>-<slug>.md  ← un archivo por observación
func Export(ctx context.Context, st store.Storer, outDir, project string) error {
	_, err := ExportWithOptions(ctx, st, outDir, project, ExportOptions{})
	return err
}

// ExportWithOptions es la exportación no destructiva: cada archivo que
// genera lleva en su frontmatter generated_by/kronos_hash (ver
// nondestructive.go). Un archivo sin esa marca (nota escrita a mano) nunca
// se toca. Un archivo marcado pero editado a mano después de exportarse
// (el hash actual no coincide con el guardado) tampoco se pisa — se avisa
// por stderr y se cuenta en el resumen. _index.md es la única excepción:
// es puramente derivado, así que siempre se regenera.
func ExportWithOptions(ctx context.Context, st store.Storer, outDir, project string, opts ExportOptions) (*ExportStats, error) {
	observations, err := st.ListAll(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("list observations: %w", err)
	}

	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	stats := &ExportStats{}
	validIDs := make(map[int64]bool, len(observations))
	projects := map[string]bool{}

	for _, o := range observations {
		validIDs[o.ID] = true
		projects[o.Project] = true

		result, err := writeObservationSafe(outDir, o)
		if err != nil {
			return nil, fmt.Errorf("write observation %d: %w", o.ID, err)
		}
		recordResult(stats, result, obsPath(outDir, o))
	}

	for proj := range projects {
		result, err := writeCoreNote(ctx, st, outDir, proj)
		if err != nil {
			return nil, fmt.Errorf("write core note %q: %w", proj, err)
		}
		recordResult(stats, result, coreNotePath(outDir, proj))
	}

	if err := writeIndex(outDir, observations); err != nil {
		return nil, fmt.Errorf("write index: %w", err)
	}

	if opts.Prune {
		// Con filtro de proyecto, el barrido se limita a ese subárbol: fuera
		// de él, validIDs no representa el universo completo de
		// observaciones y podaría archivos de otros proyectos por error.
		root := outDir
		if project != "" {
			root = filepath.Join(outDir, safeName(project))
		}
		pruned, err := pruneOrphans(root, validIDs)
		if err != nil {
			return nil, fmt.Errorf("prune: %w", err)
		}
		stats.Pruned = pruned
	}

	printSummary(len(observations), outDir, stats)
	return stats, nil
}

func recordResult(stats *ExportStats, result writeResult, path string) {
	switch result {
	case resWritten:
		stats.Written++
	case resUnchanged:
		stats.Unchanged++
	case resSkippedManual:
		stats.SkippedManual = append(stats.SkippedManual, path)
		fmt.Fprintf(os.Stderr, "aviso: editado a mano, no se sobreescribe: %s\n", path)
	}
}

func printSummary(total int, outDir string, stats *ExportStats) {
	fmt.Printf("Export a %s: %d observaciones\n", outDir, total)
	fmt.Printf("  escritos: %d | sin cambios: %d | editados a mano (salteados): %d | borrados (--prune): %d\n",
		stats.Written, stats.Unchanged, len(stats.SkippedManual), stats.Pruned)
}

// obsPath returns <outDir>/<project>/<type>/<id>-<slug>.md for o.
func obsPath(outDir string, o *store.Observation) string {
	dir := filepath.Join(outDir, safeName(o.Project), safeName(string(o.Type)))
	return filepath.Join(dir, obsName(o.ID, o.Title)+".md")
}

// coreNotePath returns <outDir>/<project>/_core.md.
func coreNotePath(outDir, project string) string {
	return filepath.Join(outDir, safeName(project), "_core.md")
}

// observationBuilder arma el contenido completo del archivo de o, con hash
// insertado en el campo kronos_hash — ver writeGenerated.
func observationBuilder(o *store.Observation) func(hash string) string {
	return func(hash string) string {
		var sb strings.Builder
		writeObsFrontmatter(&sb, o, hash)
		writeObsBody(&sb, o)
		return sb.String()
	}
}

// writeObservation escribe (sin gate) <outDir>/<project>/<type>/<id>-<slug>.md.
// La usa el mirror en vivo (mirror.go), que siempre refleja el estado actual
// de la observación — el gate no destructivo es específico de `kronos export`
// (ver writeObservationSafe).
func writeObservation(outDir string, o *store.Observation) error {
	path := obsPath(outDir, o)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	build := observationBuilder(o)
	final := build(contentHash(build(hashPlaceholder)))
	return os.WriteFile(path, []byte(final), 0644)
}

// writeObservationSafe es la versión no destructiva de writeObservation,
// usada por Export: no pisa notas escritas a mano ni archivos generados que
// el usuario editó después de exportarlos.
func writeObservationSafe(outDir string, o *store.Observation) (writeResult, error) {
	return writeGenerated(obsPath(outDir, o), observationBuilder(o))
}

func writeObsFrontmatter(sb *strings.Builder, o *store.Observation, hash string) {
	fmt.Fprintf(sb, "---\n")
	fmt.Fprintf(sb, "id: %d\n", o.ID)
	fmt.Fprintf(sb, "title: %q\n", o.Title)
	fmt.Fprintf(sb, "type: %s\n", o.Type)
	fmt.Fprintf(sb, "project: %s\n", o.Project)
	fmt.Fprintf(sb, "scope: %s\n", o.Scope)
	if o.TopicKey != "" {
		fmt.Fprintf(sb, "topic_key: %s\n", o.TopicKey)
	}
	fmt.Fprintf(sb, "created_at: %s\n", o.CreatedAt.Format("2006-01-02"))
	fmt.Fprintf(sb, "revision: %d\n", o.RevisionCount)
	fmt.Fprintf(sb, "tags: [%s, %s]\n", o.Type, safeName(o.Project))
	fmt.Fprintf(sb, "%s: %s\n", markerKey, markerValue)
	fmt.Fprintf(sb, "kronos_id: %d\n", o.ID)
	fmt.Fprintf(sb, "%s: %s\n", hashKey, hash)
	fmt.Fprintf(sb, "---\n\n")
}

func writeObsBody(sb *strings.Builder, o *store.Observation) {
	fmt.Fprintf(sb, "# %s\n\n", o.Title)
	fmt.Fprintf(sb, "**ID**: %d | **Tipo**: %s | **Proyecto**: %s\n",
		o.ID, o.Type, o.Project)
	fmt.Fprintf(sb, "**Scope**: %s", o.Scope)
	if o.TopicKey != "" {
		fmt.Fprintf(sb, " | **Topic key**: %s", o.TopicKey)
	}
	fmt.Fprintf(sb, "\n**Creado**: %s | **Rev**: %d\n\n", o.CreatedAt.Format("2006-01-02"), o.RevisionCount)
	fmt.Fprintf(sb, "---\n\n")
	fmt.Fprintf(sb, "%s\n", o.Content)
}

// writeCoreNote escribe <outDir>/<project>/_core.md: la salida de
// hooks.BuildCoreBlock para ese proyecto, con presupuesto por defecto (ver
// hooks.CoreBlockOptions). Es el puente entre el vault y la memoria que
// kronos realmente usa — a diferencia del resto del vault (un dump de
// observaciones), esto es exactamente lo que se inyecta SIEMPRE al arrancar
// una sesión de ese proyecto.
func writeCoreNote(ctx context.Context, st store.Storer, outDir, project string) (writeResult, error) {
	block, err := hooks.BuildCoreBlock(ctx, st, project, hooks.CoreBlockOptions{IncludeCheckpoint: true})
	if err != nil {
		return 0, err
	}

	build := func(hash string) string {
		var sb strings.Builder
		fmt.Fprintf(&sb, "---\n")
		fmt.Fprintf(&sb, "project: %s\n", project)
		fmt.Fprintf(&sb, "updated_at: %s\n", time.Now().Format("2006-01-02"))
		fmt.Fprintf(&sb, "%s: %s\n", markerKey, markerValue)
		fmt.Fprintf(&sb, "%s: %s\n", hashKey, hash)
		fmt.Fprintf(&sb, "---\n\n")
		fmt.Fprintf(&sb, "# %s — bloque core\n\n", project)
		sb.WriteString("> Esto es lo que kronos inyecta SIEMPRE al arrancar una sesión de este " +
			"proyecto (ver `internal/hooks/core_block.go`). No es un resumen manual: se " +
			"recalcula en cada `kronos export`.\n\n")
		if block == "" {
			sb.WriteString("_(sin observaciones suficientes todavía para armar el bloque core)_\n")
		} else {
			sb.WriteString(block)
			sb.WriteString("\n")
		}
		return sb.String()
	}

	return writeGenerated(coreNotePath(outDir, project), build)
}

// writeIndex creates <outDir>/_index.md. Siempre se regenera por completo —
// es puramente derivado del estado actual del store, no hay forma de
// "editarlo a mano" que tenga sentido conservar.
func writeIndex(outDir string, observations []*store.Observation) error {
	// Group by project.
	byProject := map[string][]*store.Observation{}
	for _, o := range observations {
		byProject[o.Project] = append(byProject[o.Project], o)
	}

	projects := make([]string, 0, len(byProject))
	for p := range byProject {
		projects = append(projects, p)
	}
	sort.Strings(projects)

	var sb strings.Builder
	fmt.Fprintf(&sb, "---\n")
	fmt.Fprintf(&sb, "%s: %s\n", markerKey, markerValue)
	fmt.Fprintf(&sb, "---\n\n")
	fmt.Fprintf(&sb, "# Kronos — Índice de Memoria\n\n")
	fmt.Fprintf(&sb, "> Exportado: %s | Total: %d observaciones\n\n", time.Now().Format("2006-01-02"), len(observations))

	for _, proj := range projects {
		obs := byProject[proj]
		fmt.Fprintf(&sb, "## %s (%d)\n\n", proj, len(obs))
		fmt.Fprintf(&sb, "[[%s]] — bloque core del proyecto\n\n", safeName(proj)+"/_core")
		fmt.Fprintf(&sb, "| ID | Título | Tipo | Fecha |\n")
		fmt.Fprintf(&sb, "|----|--------|------|-------|\n")
		for _, o := range obs {
			fmt.Fprintf(&sb, "| %d | %s | %s | %s |\n",
				o.ID,
				wikilink(o.ID, o.Title),
				o.Type,
				o.CreatedAt.Format("2006-01-02"),
			)
		}
		sb.WriteString("\n")
	}

	return os.WriteFile(filepath.Join(outDir, "_index.md"), []byte(sb.String()), 0644)
}

// safeName converts a string to a safe directory/file component.
func safeName(s string) string {
	if s == "" {
		return "unknown"
	}
	return slug(s)
}
