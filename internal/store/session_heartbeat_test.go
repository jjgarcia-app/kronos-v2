package store

import (
	"context"
	"testing"
	"time"
)

// Este archivo cubre DualStore.TouchSessionActivity — separado de
// dual_store_coverage_test.go para no cruzar las 500 líneas.

func TestDualStore_TouchSessionActivity_ReadsFromPrimary(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	sess, err := ds.primary.CreateSession(ctx, "sess-touch", "p", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	before := sess.LastActivityAt
	time.Sleep(1100 * time.Millisecond)

	if err := ds.TouchSessionActivity(ctx, "sess-touch", "p"); err != nil {
		t.Fatalf("TouchSessionActivity: %v", err)
	}

	got, err := ds.primary.GetSession(ctx, "sess-touch")
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastActivityAt.After(before) {
		t.Errorf("LastActivityAt en primary no avanzó: before=%v after=%v", before, got.LastActivityAt)
	}
}

func TestDualStore_TouchSessionActivity_FallsBackAndEnqueues(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	sess, err := ds.buffer.CreateSession(ctx, "sess-touch-fb", "p", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	before := sess.LastActivityAt

	ds.primary.Close()
	ds.down = false

	time.Sleep(1100 * time.Millisecond)

	if err := ds.TouchSessionActivity(ctx, "sess-touch-fb", "p"); err != nil {
		t.Fatalf("TouchSessionActivity: %v", err)
	}

	got, err := ds.buffer.GetSession(ctx, "sess-touch-fb")
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastActivityAt.After(before) {
		t.Errorf("LastActivityAt en buffer no avanzó: before=%v after=%v", before, got.LastActivityAt)
	}
	if !ds.isPrimaryDown() {
		t.Error("primary debería marcarse down tras el error real")
	}
	if ds.PendingCount() != 1 {
		t.Errorf("PendingCount = %d, want 1", ds.PendingCount())
	}
}

// TestDualStore_TouchSessionActivity_LocalOnlyProject_NeverReachesPrimary
// reproduce el bug real encontrado en el review de este mismo cambio: antes,
// TouchSessionActivity no chequeaba isLocalOnly como el resto de los métodos
// de escritura de DualStore, así que mandaba el session_id de un proyecto
// local-only al primary compartido en cada prompt.
func TestDualStore_TouchSessionActivity_LocalOnlyProject_NeverReachesPrimary(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()
	ds.SetLocalOnlyProjects([]string{"proyecto-privado"})

	if _, err := ds.buffer.CreateSession(ctx, "sess-local", "proyecto-privado", "/tmp"); err != nil {
		t.Fatal(err)
	}

	if err := ds.TouchSessionActivity(ctx, "sess-local", "proyecto-privado"); err != nil {
		t.Fatalf("TouchSessionActivity: %v", err)
	}

	if ds.PendingCount() != 0 {
		t.Errorf("PendingCount = %d, want 0 — un proyecto local-only nunca debe encolarse para sync", ds.PendingCount())
	}
	if sess, err := ds.primary.GetSession(ctx, "sess-local"); err != nil || sess != nil {
		t.Errorf("la sesión local-only no debería existir en primary — got sess=%+v err=%v", sess, err)
	}
}

// TestTouchSessionActivity_SoftDeletedSession_StillUpdates reproduce el
// segundo bug del review: touchSessionActivityAffected filtraba deleted_at
// IS NULL (a diferencia de incrementSearchCountAffected, el patrón que
// copia), así que una sesión soft-deleted siempre reportaba 0 filas
// afectadas en primary y encolaba un touch_session_activity sin límite en
// cada prompt. Sin el filtro, el UPDATE simplemente tiene efecto (no hay
// nada más leyendo last_activity_at de una sesión borrada).
func TestTouchSessionActivity_SoftDeletedSession_StillUpdates(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateSession(ctx, "sess-deleted", "p", "/tmp"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, "sess-deleted"); err != nil {
		t.Fatal(err)
	}

	n, err := s.touchSessionActivityAffected(ctx, "sess-deleted")
	if err != nil {
		t.Fatalf("touchSessionActivityAffected: %v", err)
	}
	if n == 0 {
		t.Error("una sesión soft-deleted debería seguir reportando la fila afectada — si no, DualStore la encola sin límite en sync_queue")
	}
}
