package obsidian

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// Export writes all non-deleted observations to outDir as an Obsidian vault.
// If project is non-empty only observations for that project are exported.
// The directory structure is:
//
//	<outDir>/
//	  _index.md                        ← master index
//	  <project>/<type>/<id>-<slug>.md  ← one file per observation
func Export(ctx context.Context, st *store.Store, outDir, project string) error {
	observations, err := st.ListAll(ctx, project)
	if err != nil {
		return fmt.Errorf("list observations: %w", err)
	}

	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	for _, o := range observations {
		if err := writeObservation(outDir, o); err != nil {
			return fmt.Errorf("write observation %d: %w", o.ID, err)
		}
	}

	if err := writeIndex(outDir, observations); err != nil {
		return fmt.Errorf("write index: %w", err)
	}

	fmt.Printf("Exportadas %d observaciones a %s\n", len(observations), outDir)
	return nil
}

// writeObservation creates <outDir>/<project>/<type>/<id>-<slug>.md
func writeObservation(outDir string, o *store.Observation) error {
	dir := filepath.Join(outDir, safeName(o.Project), safeName(string(o.Type)))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	name := obsName(o.ID, o.Title) + ".md"
	path := filepath.Join(dir, name)

	var sb strings.Builder
	writeObsFrontmatter(&sb, o)
	writeObsBody(&sb, o)

	return os.WriteFile(path, []byte(sb.String()), 0644)
}

func writeObsFrontmatter(sb *strings.Builder, o *store.Observation) {
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

// writeIndex creates <outDir>/_index.md
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
	fmt.Fprintf(&sb, "# Kronos — Índice de Memoria\n\n")
	fmt.Fprintf(&sb, "> Exportado: %s | Total: %d observaciones\n\n", time.Now().Format("2006-01-02"), len(observations))

	for _, proj := range projects {
		obs := byProject[proj]
		fmt.Fprintf(&sb, "## %s (%d)\n\n", proj, len(obs))
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
