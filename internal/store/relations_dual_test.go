package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestDualStore_ListRelations_ReadsFromPrimaryNotBuffer reproduce el bug real
// de mem_doctor: mem_doctor (internal/mcp/handlers.go) y el aviso de backlog
// de SessionStart (internal/hooks/session_start.go) alcanzaban el buffer
// SQLite local a mano vía LocalStore()/localStoreOf() para listar relaciones
// pendientes — DualStore ni siquiera tenía un ListRelations propio. Antes de
// este fix el código ni compilaba contra st.ListRelations(...) en la
// interfaz Storer; ahora debe leer primary-first: con primary y buffer
// teniendo relaciones pending DISTINTAS, el resultado tiene que ser el del
// primary, no el del buffer.
func TestDualStore_ListRelations_ReadsFromPrimaryNotBuffer(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	if _, err := ds.buffer.insertRelationPending(ctx, "buf-src", "buf-tgt"); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.primary.insertRelationPending(ctx, "pg-src", "pg-tgt"); err != nil {
		t.Fatal(err)
	}

	rels, err := ds.ListRelations(ctx, "", JudgmentPending, 100, 0)
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("len(rels) = %d, want 1 (solo la del primary — con primary sano no se debe leer del buffer)", len(rels))
	}
	if rels[0].SourceID != "pg-src" {
		t.Errorf("SourceID = %q, want %q — ListRelations está devolviendo la relación del buffer en vez de la del primary", rels[0].SourceID, "pg-src")
	}

	// primary sigue "up" — leer una relación real no debe marcarlo down.
	if ds.isPrimaryDown() {
		t.Error("primary no debería marcarse down por una lectura exitosa")
	}
}

// testRelationsPostgresDSN — misma DB local de desarrollo que el resto de los
// tests de integración de este paquete (ver store_postgres_test.go).
const testRelationsPostgresDSN = "postgresql://postgres:kronos@localhost:5432/kronos?sslmode=disable"

// TestDualStore_ListRelations_ReadsFromPrimaryNotBuffer_RealPostgres es la
// regresión que de verdad importa: relations.go usaba s.db.QueryContext
// directo con placeholders "?" en vez de s.query (que aplica rebind a
// "$1"...) — mismo patrón de bug ya encontrado en CountSessionPrompts/
// CountSessionObservations (ver TestCountSessionMethods_RealPostgres en
// store_postgres_test.go). Contra Postgres real (driver pgx), "?" es
// syntax error: ListRelations en el primary siempre fallaba, DualStore lo
// interpretaba como "primary caído" y caía al buffer — el mismo síntoma
// exacto reportado en mem_doctor (849 obs/3 relaciones del buffer en vez de
// las 880 obs/0 relaciones reales del primary). Solo un test contra
// Postgres de verdad puede atrapar esto — contra SQLite "?" siempre funciona.
func TestDualStore_ListRelations_ReadsFromPrimaryNotBuffer_RealPostgres(t *testing.T) {
	primary, err := NewPostgres(testRelationsPostgresDSN)
	if err != nil {
		t.Skipf("Postgres no disponible en %s, se salta el test de integración: %v", testRelationsPostgresDSN, err)
	}
	t.Cleanup(func() { primary.Close() })

	buffer, err := New(filepath.Join(t.TempDir(), "buffer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { buffer.Close() })

	ctx := context.Background()

	if _, err := buffer.insertRelationPending(ctx, "buf-src-only", "buf-tgt-only"); err != nil {
		t.Fatal(err)
	}

	// source_id único por corrida — Postgres acá es compartida con procesos
	// kronos reales, un valor fijo podría colisionar con filas de corridas
	// anteriores (esta tabla no tiene UNIQUE en source_id/target_id).
	primarySource := "pg-src-only-" + time.Now().UTC().Format("20060102150405.000000000")
	primaryID, err := primary.insertRelationPending(ctx, primarySource, "pg-tgt-only")
	if err != nil {
		t.Skipf("Postgres no disponible para escribir, se salta: %v", err)
	}
	t.Cleanup(func() {
		_, _ = primary.DB().ExecContext(context.Background(), `DELETE FROM memory_relations WHERE id = $1`, primaryID)
	})

	q, err := newSyncQueue(buffer.DB())
	if err != nil {
		t.Fatal(err)
	}
	ds := &DualStore{primary: primary, buffer: buffer, down: false, queue: q}

	rels, err := ds.ListRelations(ctx, "", JudgmentPending, 1000, 0)
	if err != nil {
		t.Fatalf("ListRelations contra Postgres real: %v (¿volvió a romperse el rebind de placeholders?)", err)
	}

	var foundPrimary, foundBuffer bool
	for _, r := range rels {
		switch r.SourceID {
		case primarySource:
			foundPrimary = true
		case "buf-src-only":
			foundBuffer = true
		}
	}
	if !foundPrimary {
		t.Error("ListRelations no devolvió la relación que solo existe en Postgres primary — ¿sigue leyendo el buffer?")
	}
	if foundBuffer {
		t.Error("ListRelations devolvió una relación que solo existe en el buffer, con el primary sano — no debería caer al buffer")
	}
	if ds.isPrimaryDown() {
		t.Error("primary no debería marcarse down tras una lectura exitosa de ListRelations")
	}
}
