package obsidian

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// TestImportVault_RewriteLeavesFileAsExportWouldGenerate cubre el contrato que
// se rompió una vez: después de aplicar una edición manual, el archivo tiene
// que quedar EXACTAMENTE como lo generaría el export (mismo cuerpo y mismo
// kronos_hash canónico). Si no, la próxima exportación lo marca como "editado
// a mano" para siempre y deja de refrescarlo.
//
// Va como test interno (package obsidian) a propósito: la comprobación usa
// writeGenerated, que es el mismo camino que usa el export para decidir si un
// archivo cambió.
func TestImportVault_RewriteLeavesFileAsExportWouldGenerate(t *testing.T) {
	ctx := context.Background()

	dbPath := filepath.Join(t.TempDir(), "kronos.db")
	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer st.Close()

	obs, err := st.SaveObservation(ctx, store.SaveParams{
		Title:    "Nota de prueba para el import",
		Content:  "Contenido original de la nota.",
		Type:     "discovery",
		Project:  "kronos-v2",
		TopicKey: "test/import-hash",
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}

	outDir := t.TempDir()
	if err := Export(ctx, st, outDir, ""); err != nil {
		t.Fatalf("Export: %v", err)
	}

	var path string
	if err := filepath.WalkDir(outDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), obs.Content) {
			path = p
		}
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if path == "" {
		t.Fatal("no encontré la nota exportada")
	}

	// Edición a mano, como la haría el usuario en Obsidian.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), obs.Content, obs.Content+" Añadido a mano.", 1)
	if edited == string(raw) {
		t.Fatal("la edición de prueba no cambió nada")
	}
	if err := os.WriteFile(path, []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}

	stats, err := ImportVault(ctx, st, outDir, ImportOptions{Apply: true})
	if err != nil {
		t.Fatalf("ImportVault: %v", err)
	}
	if len(stats.Updates) != 1 {
		t.Fatalf("Updates = %d, quería 1", len(stats.Updates))
	}

	updated, err := st.GetObservation(ctx, obs.ID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}

	res, err := writeGenerated(path, observationBuilder(updated))
	if err != nil {
		t.Fatalf("writeGenerated: %v", err)
	}
	if res != resUnchanged {
		t.Errorf("después del import el export igual ve el archivo como editado a mano (res=%v): el hash quedó mal calculado", res)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "Añadido a mano.") {
		t.Error("la edición manual no sobrevivió al import")
	}
	wantRev := "revision: " + strconv.Itoa(updated.RevisionCount)
	if !strings.Contains(string(after), wantRev) {
		t.Errorf("el frontmatter del archivo no refleja la revisión de la base (%q)", wantRev)
	}
}
