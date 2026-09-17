package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/relations"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// TestReindexRecent_ReadsPrimaryNotStaleBuffer es la regresión del mismo bug
// que TestRunGCConsolidate_PostgresBackend_ReadsPrimaryNotStaleBuffer (ver
// bdbeaaf): reindexRecent recibía *store.Store (el buffer SQLite local vía
// DualStore.LocalStore()) para listar qué observaciones indexar, así que era
// ciego a cualquier fila que el buffer no tuviera sincronizada todavía.
//
// Medido en producción el 2026-09-17: el buffer tenía 865 de 1167
// observaciones reales — reindexRecent nunca indexaba las últimas ~300, y la
// fase vectorial del recall (gatherRecallCandidates) quedaba sin poder
// encontrarlas nunca, sin importar la similitud real del texto.
//
// Este test crea una observación SOLO en el primario (Postgres), simulando
// exactamente ese desfase, y verifica que reindexRecent(ctx, dualStore, ...)
// la indexe igual — porque ahora recibe store.Storer y DualStore.ListAll
// delega al primario.
func TestReindexRecent_ReadsPrimaryNotStaleBuffer(t *testing.T) {
	prim, err := store.NewPostgres(testConsolidateDSN)
	if err != nil {
		t.Skipf("Postgres no disponible en %s, se salta el test de integración: %v", testConsolidateDSN, err)
	}
	t.Cleanup(func() { _ = prim.Close() })

	ctx := context.Background()
	testProject := fmt.Sprintf("kronos-reindex-test-%d", time.Now().UnixNano())
	obs, err := prim.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypeDiscovery,
		Title:   "Hallazgo solo en el primario",
		Content: "contenido que el buffer local nunca vio",
		Project: testProject,
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
	t.Cleanup(func() {
		if _, err := prim.DB().ExecContext(ctx, `DELETE FROM observations WHERE project = $1`, testProject); err != nil {
			t.Logf("cleanup: borrar observations de prueba: %v", err)
		}
	})

	// Buffer local vacío a propósito — nunca sincronizó la fila de arriba,
	// exactamente el escenario real medido.
	bufferDBPath := t.TempDir() + "/buffer.db"
	buffer, err := store.New(bufferDBPath)
	if err != nil {
		t.Fatalf("abrir buffer local: %v", err)
	}
	t.Cleanup(func() { _ = buffer.Close() })

	dual, err := store.NewDualFromDSN(buffer, testConsolidateDSN)
	if err != nil {
		t.Fatalf("NewDualFromDSN: %v", err)
	}

	calls := 0
	fakeEmbed := embeddings.EmbeddingFunc(func(_ context.Context, _ string) ([]float32, error) {
		calls++
		return []float32{0.1, 0.2, 0.3}, nil
	})
	vs, err := embeddings.NewInMemory(fakeEmbed)
	if err != nil {
		t.Fatalf("NewInMemory: %v", err)
	}
	rel := relations.New(vs)

	reindexRecentForProject(ctx, dual, rel, testProject)

	if !vs.Has(ctx, obs.ID) {
		t.Fatalf("la observación #%d (solo en el primario) no quedó indexada — reindexRecent sigue leyendo del buffer local", obs.ID)
	}
	if calls == 0 {
		t.Fatal("el proveedor de embeddings nunca se llamó — reindexRecent no encontró nada para indexar")
	}
}
