package obsidian_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/obsidian"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	f, err := os.CreateTemp("", "kronos-obsidian-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	st, err := store.New(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seedObservations(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	fixtures := []store.SaveParams{
		{Title: "Elegimos Go para Kronos v2", Content: "Go compila a binario único sin CGO.", Type: "decision", Project: "kronos-v2", TopicKey: "arch/lang"},
		{Title: "SQLite FTS5 para búsqueda", Content: "FTS5 con unicode61 maneja español correctamente.", Type: "discovery", Project: "kronos-v2"},
		{Title: "Arquitectura hexagonal ATISA", Content: "Separamos dominio de infraestructura.", Type: "architecture", Project: "atisa"},
	}
	for _, p := range fixtures {
		if _, err := st.SaveObservation(ctx, p); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func TestExport_CreatesFiles(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)

	outDir := t.TempDir()
	if err := obsidian.Export(context.Background(), st, outDir, ""); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// _index.md must exist.
	if _, err := os.Stat(filepath.Join(outDir, "_index.md")); err != nil {
		t.Errorf("_index.md not created: %v", err)
	}
}

func TestExport_IndexContainsProjects(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)

	outDir := t.TempDir()
	if err := obsidian.Export(context.Background(), st, outDir, ""); err != nil {
		t.Fatalf("Export: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "_index.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	for _, want := range []string{"kronos-v2", "atisa"} {
		if !strings.Contains(content, want) {
			t.Errorf("_index.md missing project %q", want)
		}
	}
}

func TestExport_ObservationFileHasFrontmatter(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)

	outDir := t.TempDir()
	if err := obsidian.Export(context.Background(), st, outDir, "kronos-v2"); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Walk to find any .md file that's not _index.md.
	var found string
	filepath.WalkDir(outDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(path) == "_index.md" {
			return nil
		}
		if strings.HasSuffix(path, ".md") && found == "" {
			found = path
		}
		return nil
	})

	if found == "" {
		t.Fatal("no observation .md files created")
	}

	data, _ := os.ReadFile(found)
	content := string(data)

	for _, want := range []string{"---", "title:", "type:", "project:", "created_at:"} {
		if !strings.Contains(content, want) {
			t.Errorf("frontmatter missing %q in:\n%s", want, content)
		}
	}
}

func TestExport_ProjectFilter(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)

	outDir := t.TempDir()
	if err := obsidian.Export(context.Background(), st, outDir, "atisa"); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// ATISA observations should be present.
	data, _ := os.ReadFile(filepath.Join(outDir, "_index.md"))
	if !strings.Contains(string(data), "atisa") {
		t.Error("_index.md should contain atisa project")
	}
}

func TestExport_EmptyStore(t *testing.T) {
	st := newTestStore(t)
	outDir := t.TempDir()

	if err := obsidian.Export(context.Background(), st, outDir, ""); err != nil {
		t.Fatalf("Export on empty store: %v", err)
	}

	// Index must still be created.
	if _, err := os.Stat(filepath.Join(outDir, "_index.md")); err != nil {
		t.Errorf("_index.md not created for empty store: %v", err)
	}
}

func TestExport_WikilinksInIndex(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)

	outDir := t.TempDir()
	if err := obsidian.Export(context.Background(), st, outDir, "kronos-v2"); err != nil {
		t.Fatalf("Export: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(outDir, "_index.md"))
	if !strings.Contains(string(data), "[[") {
		t.Error("_index.md should contain wikilinks")
	}
}

func TestExport_DirectoryStructure(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)

	outDir := t.TempDir()
	if err := obsidian.Export(context.Background(), st, outDir, ""); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Observations should be in <project>/<type>/ subdirs.
	var mdCount int
	filepath.WalkDir(outDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".md") && filepath.Base(path) != "_index.md" {
			mdCount++
			// Each file should be at least 2 levels deep inside outDir.
			rel, _ := filepath.Rel(outDir, path)
			parts := strings.Split(rel, string(filepath.Separator))
			if len(parts) < 3 {
				t.Errorf("observation file not in <project>/<type>/ subdir: %s", rel)
			}
		}
		return nil
	})

	if mdCount == 0 {
		t.Error("expected observation .md files, found none")
	}
}
