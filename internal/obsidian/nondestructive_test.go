package obsidian_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/obsidian"
)

// findGeneratedFile busca, dentro de outDir, el primer archivo generado
// (no _index.md/_core.md) cuyo contenido incluye needle.
func findGeneratedFile(t *testing.T, outDir, needle string) string {
	t.Helper()
	var found string
	filepath.WalkDir(outDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		base := filepath.Base(path)
		if base == "_index.md" || base == "_core.md" {
			return nil
		}
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), needle) {
			found = path
		}
		return nil
	})
	if found == "" {
		t.Fatalf("no se encontró archivo generado con %q en %s", needle, outDir)
	}
	return found
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// (a) una nota manual sin marca sobrevive a un export.
func TestExport_PreservesManualNote(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)

	outDir := t.TempDir()
	manualPath := filepath.Join(outDir, "notas-personales.md")
	manualContent := "# Notas mías\n\nEsto lo escribí yo a mano, kronos no lo tocó.\n"
	if err := os.WriteFile(manualPath, []byte(manualContent), 0644); err != nil {
		t.Fatal(err)
	}

	if err := obsidian.Export(context.Background(), st, outDir, ""); err != nil {
		t.Fatalf("Export: %v", err)
	}

	data, err := os.ReadFile(manualPath)
	if err != nil {
		t.Fatalf("la nota manual desapareció: %v", err)
	}
	if string(data) != manualContent {
		t.Errorf("la nota manual fue modificada:\nquería: %q\ntengo: %q", manualContent, data)
	}
}

// (b) un archivo generado que el usuario editó a mano no se pisa y se reporta.
func TestExport_DoesNotOverwriteHandEditedGeneratedFile(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)
	outDir := t.TempDir()

	if err := obsidian.Export(context.Background(), st, outDir, ""); err != nil {
		t.Fatalf("Export inicial: %v", err)
	}

	genPath := findGeneratedFile(t, outDir, "SQLite FTS5 para búsqueda")
	original, err := os.ReadFile(genPath)
	if err != nil {
		t.Fatal(err)
	}

	edited := string(original) + "\n\nAgregué esto a mano después de exportar.\n"
	if err := os.WriteFile(genPath, []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}

	var stats *obsidian.ExportStats
	stderr := captureStderr(t, func() {
		s, err := obsidian.ExportWithOptions(context.Background(), st, outDir, "", obsidian.ExportOptions{})
		if err != nil {
			t.Fatalf("Export segunda vez: %v", err)
		}
		stats = s
	})

	after, err := os.ReadFile(genPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != edited {
		t.Errorf("el archivo editado a mano fue pisado por el export")
	}
	if len(stats.SkippedManual) != 1 {
		t.Errorf("SkippedManual = %v, quería 1 entrada", stats.SkippedManual)
	}
	if !strings.Contains(stderr, genPath) {
		t.Errorf("stderr no menciona el path editado a mano: %q", stderr)
	}
}

// (c) --prune borra solo generados huérfanos (observación borrada del store).
func TestExport_PruneRemovesOnlyOrphanedGeneratedFiles(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)
	outDir := t.TempDir()

	if err := obsidian.Export(context.Background(), st, outDir, ""); err != nil {
		t.Fatalf("Export inicial: %v", err)
	}

	orphanPath := findGeneratedFile(t, outDir, "SQLite FTS5 para búsqueda")
	survivorPath := findGeneratedFile(t, outDir, "Elegimos Go para Kronos v2")

	all, err := st.ListAll(context.Background(), "kronos-v2")
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	var targetID int64
	for _, o := range all {
		if strings.Contains(o.Title, "SQLite FTS5") {
			targetID = o.ID
		}
	}
	if targetID == 0 {
		t.Fatal("no encontré la observación a borrar")
	}
	if err := st.DeleteObservation(context.Background(), targetID); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}

	stats, err := obsidian.ExportWithOptions(context.Background(), st, outDir, "", obsidian.ExportOptions{Prune: true})
	if err != nil {
		t.Fatalf("Export con --prune: %v", err)
	}

	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Errorf("el archivo huérfano %s debería haberse borrado con --prune", orphanPath)
	}
	if _, err := os.Stat(survivorPath); err != nil {
		t.Errorf("--prune borró un archivo que no era huérfano: %v", err)
	}
	if stats.Pruned != 1 {
		t.Errorf("Pruned = %d, quería 1", stats.Pruned)
	}

	// El manual no toca notas manuales ni _index.md/_core.md aunque no tengan
	// observación de origen "vigente" en el sentido de kronos_id.
	if _, err := os.Stat(filepath.Join(outDir, "_index.md")); err != nil {
		t.Errorf("_index.md no debería haberse borrado por --prune: %v", err)
	}
}

func TestExport_WritesCoreNotePerProject(t *testing.T) {
	st := newTestStore(t)
	seedObservations(t, st)
	outDir := t.TempDir()

	if err := obsidian.Export(context.Background(), st, outDir, ""); err != nil {
		t.Fatalf("Export: %v", err)
	}

	corePath := filepath.Join(outDir, "kronos-v2", "_core.md")
	data, err := os.ReadFile(corePath)
	if err != nil {
		t.Fatalf("_core.md no se generó: %v", err)
	}
	content := string(data)
	for _, want := range []string{"generated_by: kronos-export", "kronos_hash:", "bloque core"} {
		if !strings.Contains(content, want) {
			t.Errorf("_core.md no contiene %q:\n%s", want, content)
		}
	}
}
