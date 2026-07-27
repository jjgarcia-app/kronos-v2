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

// dualLikeStore simula la forma de *store.DualStore (LocalStore + PendingCount)
// sin depender de Postgres — reproduce el bug real: MirrorStore embebe
// store.Storer como interfaz, así que sin passthrough explícito estos dos
// métodos quedaban invisibles y handleMemDoctor/sqliteStore dejaban de
// detectar el backend real en cuanto el store quedaba envuelto.
type dualLikeStore struct {
	*store.Store
	pending int
}

func (d *dualLikeStore) LocalStore() *store.Store { return d.Store }
func (d *dualLikeStore) PendingCount() int        { return d.pending }

func TestMirrorStore_ForwardsLocalStoreAndPendingCount(t *testing.T) {
	inner := &dualLikeStore{Store: newTestStore(t), pending: 3}
	m := obsidian.NewMirrorStore(inner, t.TempDir())

	if got := m.LocalStore(); got != inner.Store {
		t.Errorf("LocalStore() = %v, want %v", got, inner.Store)
	}
	if got := m.PendingCount(); got != 3 {
		t.Errorf("PendingCount() = %d, want 3", got)
	}

	sess, err := m.CreateSession(context.Background(), "s1", "kronos-v2", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SavePrompt(context.Background(), sess.ID, "kronos-v2", "hola"); err != nil {
		t.Fatal(err)
	}
	if n := m.CountSessionPrompts(context.Background(), sess.ID); n != 1 {
		t.Errorf("CountSessionPrompts() = %d, want 1 (nudge de guardado roto)", n)
	}
	if n := m.CountSessionObservations(context.Background(), sess.ID); n != 0 {
		t.Errorf("CountSessionObservations() = %d, want 0", n)
	}
}

func TestMirrorStore_SaveObservation_WritesFileImmediately(t *testing.T) {
	st := newTestStore(t)
	outDir := t.TempDir()
	m := obsidian.NewMirrorStore(st, outDir)

	obs, err := m.SaveObservation(context.Background(), store.SaveParams{
		Title: "Decisión de prueba", Content: "contenido", Type: "decision", Project: "kronos-v2",
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}

	var found string
	filepath.WalkDir(outDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".md") && filepath.Base(path) != "_index.md" {
			found = path
		}
		return nil
	})
	if found == "" {
		t.Fatal("MirrorStore no escribió el .md al guardar")
	}
	data, _ := os.ReadFile(found)
	if !strings.Contains(string(data), "Decisión de prueba") {
		t.Errorf("archivo mirror no tiene el título esperado: %s", data)
	}

	if _, err := os.Stat(filepath.Join(outDir, "_index.md")); err != nil {
		t.Errorf("_index.md no se refrescó tras el save: %v", err)
	}
	_ = obs
}

func TestMirrorStore_UpdateObservation_RemovesStaleFileOnRename(t *testing.T) {
	st := newTestStore(t)
	outDir := t.TempDir()
	m := obsidian.NewMirrorStore(st, outDir)

	obs, err := m.SaveObservation(context.Background(), store.SaveParams{
		Title: "Titulo original", Content: "c", Type: "decision", Project: "kronos-v2",
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}

	newTitle := "Titulo renombrado"
	if _, err := m.UpdateObservation(context.Background(), store.UpdateParams{ID: obs.ID, Title: &newTitle}); err != nil {
		t.Fatalf("UpdateObservation: %v", err)
	}

	var mdFiles []string
	filepath.WalkDir(outDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".md") && filepath.Base(path) != "_index.md" {
			mdFiles = append(mdFiles, path)
		}
		return nil
	})
	if len(mdFiles) != 1 {
		t.Fatalf("esperaba 1 archivo tras el rename (sin huérfanos), encontré %d: %v", len(mdFiles), mdFiles)
	}
	data, _ := os.ReadFile(mdFiles[0])
	if !strings.Contains(string(data), newTitle) {
		t.Errorf("archivo remanente no tiene el título nuevo: %s", data)
	}
}

func TestMirrorStore_DeleteObservation_RemovesFile(t *testing.T) {
	st := newTestStore(t)
	outDir := t.TempDir()
	m := obsidian.NewMirrorStore(st, outDir)

	obs, err := m.SaveObservation(context.Background(), store.SaveParams{
		Title: "A borrar", Content: "c", Type: "decision", Project: "kronos-v2",
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}

	if err := m.DeleteObservation(context.Background(), obs.ID); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}

	var mdFiles int
	filepath.WalkDir(outDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".md") && filepath.Base(path) != "_index.md" {
			mdFiles++
		}
		return nil
	})
	if mdFiles != 0 {
		t.Errorf("esperaba 0 archivos tras delete, encontré %d", mdFiles)
	}
}
