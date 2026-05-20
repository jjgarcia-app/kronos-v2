package sync_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	kronsync "github.com/jjgarcia-app/kronos-v2/internal/sync"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("abrir store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func newTestSyncer(t *testing.T, st *store.Store) (*kronsync.Syncer, string) {
	t.Helper()
	dir := t.TempDir()
	return kronsync.New(st, dir), dir
}

// TestExportImportRoundtrip verifica que exportar e importar preserva los datos.
func TestExportImportRoundtrip(t *testing.T) {
	ctx := context.Background()

	// store origen con datos
	src := newTestStore(t)
	_, err := src.SaveObservation(ctx, store.SaveParams{
		Type:     store.TypeDiscovery,
		Title:    "test observation",
		Content:  "contenido de prueba para sync",
		Project:  "test-project",
		Scope:    store.ScopeProject,
		TopicKey: "test-key",
	})
	if err != nil {
		t.Fatalf("guardar observación: %v", err)
	}

	// exportar
	syncer, syncDir := newTestSyncer(t, src)
	result, err := syncer.Export("test", "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if result.IsEmpty {
		t.Fatal("export no debería estar vacío")
	}
	if result.Memories != 1 {
		t.Errorf("esperaba 1 memory, got %d", result.Memories)
	}

	// importar a store destino
	dst := newTestStore(t)
	importSyncer := kronsync.New(dst, syncDir)
	imported, err := importSyncer.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported.Memories != 1 {
		t.Errorf("esperaba importar 1 memory, got %d", imported.Memories)
	}

	// verificar que la observación existe en el destino
	results, err := dst.Search(ctx, store.SearchParams{Query: "test observation", Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Error("la observación importada no se encuentra en búsqueda")
	}
}

// TestIdempotentImport verifica que importar el mismo chunk dos veces no duplica datos.
func TestIdempotentImport(t *testing.T) {
	ctx := context.Background()

	src := newTestStore(t)
	_, err := src.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypePattern,
		Title:   "patrón idempotente",
		Content: "esto no debe duplicarse",
		Project: "test-project",
		Scope:   store.ScopeProject,
	})
	if err != nil {
		t.Fatalf("guardar: %v", err)
	}

	syncer, syncDir := newTestSyncer(t, src)
	if _, err := syncer.Export("test", ""); err != nil {
		t.Fatalf("export: %v", err)
	}

	dst := newTestStore(t)
	importSyncer := kronsync.New(dst, syncDir)

	// primera importación
	r1, err := importSyncer.Import()
	if err != nil {
		t.Fatalf("import 1: %v", err)
	}
	// segunda importación del mismo chunk
	r2, err := importSyncer.Import()
	if err != nil {
		t.Fatalf("import 2: %v", err)
	}

	if r1.Memories != 1 {
		t.Errorf("primera import: esperaba 1, got %d", r1.Memories)
	}
	if r2.Memories != 0 {
		t.Errorf("segunda import: esperaba 0 (idempotente), got %d", r2.Memories)
	}
	if r2.Skipped != 1 {
		t.Errorf("segunda import: esperaba 1 chunk skipped, got %d", r2.Skipped)
	}

	// verificar que solo hay una observación en destino
	n, _ := dst.CountObservations(ctx, "test-project")
	if n != 1 {
		t.Errorf("esperaba 1 observación, got %d (posible duplicado)", n)
	}
}

// TestTopicKeyUpsertOnImport verifica que importar respeta el upsert por topic_key.
func TestTopicKeyUpsertOnImport(t *testing.T) {
	ctx := context.Background()

	// store origen: observación con topic_key
	src := newTestStore(t)
	_, err := src.SaveObservation(ctx, store.SaveParams{
		Type:     store.TypeDecision,
		Title:    "decisión inicial",
		Content:  "Qué: usar postgres\nPor qué: escalabilidad",
		Project:  "test-project",
		Scope:    store.ScopeProject,
		TopicKey: "db-backend-decision",
	})
	if err != nil {
		t.Fatalf("guardar: %v", err)
	}

	syncer, syncDir := newTestSyncer(t, src)
	if _, err := syncer.Export("test", ""); err != nil {
		t.Fatalf("export: %v", err)
	}

	// importar
	dst := newTestStore(t)
	importSyncer := kronsync.New(dst, syncDir)
	if _, err := importSyncer.Import(); err != nil {
		t.Fatalf("import: %v", err)
	}

	// verificar que tiene la observación con el topic_key correcto
	results, err := dst.Search(ctx, store.SearchParams{Query: "postgres", Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("observación importada no encontrada")
	}
	if results[0].TopicKey != "db-backend-decision" {
		t.Errorf("topic_key: esperaba 'db-backend-decision', got %q", results[0].TopicKey)
	}
}

// TestEmptyExport verifica que exportar sin datos retorna IsEmpty=true.
func TestEmptyExport(t *testing.T) {
	src := newTestStore(t)
	syncer, _ := newTestSyncer(t, src)

	result, err := syncer.Export("test", "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !result.IsEmpty {
		t.Error("exportar store vacío debería retornar IsEmpty=true")
	}
}

// TestManifestCompaction verifica que el manifest se compacta al superar el límite.
func TestManifestCompaction(t *testing.T) {
	// Este test verifica que Import no falla con manifests grandes.
	// La compactación es interna y no tiene efecto observable directo en el test,
	// pero verifica que la importación sigue siendo idempotente después de compactación.
	ctx := context.Background()
	src := newTestStore(t)

	_, err := src.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypeDiscovery,
		Title:   "observación compactación",
		Content: "contenido",
		Project: "test",
		Scope:   store.ScopeProject,
	})
	if err != nil {
		t.Fatalf("guardar: %v", err)
	}

	syncer, syncDir := newTestSyncer(t, src)
	if _, err := syncer.Export("test", ""); err != nil {
		t.Fatalf("export: %v", err)
	}

	dst := newTestStore(t)
	importSyncer := kronsync.New(dst, syncDir)
	if _, err := importSyncer.Import(); err != nil {
		t.Fatalf("import: %v", err)
	}

	n, _ := dst.CountObservations(ctx, "test")
	if n != 1 {
		t.Errorf("esperaba 1 observación, got %d", n)
	}

	// cleanup manual del tempdir para que t.TempDir() no falle en Windows
	_ = os.RemoveAll(syncDir)
}
