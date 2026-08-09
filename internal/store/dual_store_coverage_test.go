package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// newInternalTestStore es el equivalente, en package store (white-box), del
// newTestStore de store_test.go (package store_test) — no son visibles
// entre sí al estar en paquetes distintos pese a compartir directorio.
func newInternalTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "internal-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Este archivo cubre el resto de la superficie de DualStore que
// dual_store_test.go no ejercitaba (varios métodos estaban en 0% de
// cobertura pese a ser la pieza central del sistema — ver docs/architecture.md).
// Mismo patrón que los tests existentes: ds.primary.Close() + ds.down=false
// simula un primary que aparenta estar sano pero falla en la llamada real.

func TestDualStore_UpdateObservation_FallsBackAndEnqueues(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	obs, err := ds.buffer.SaveObservation(ctx, SaveParams{
		Type: TypeDiscovery, Title: "t1", Content: "c1", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}

	ds.primary.Close()
	ds.down = false

	newTitle := "t1 actualizado"
	updated, err := ds.UpdateObservation(ctx, UpdateParams{ID: obs.ID, Title: &newTitle})
	if err != nil {
		t.Fatalf("UpdateObservation: %v", err)
	}
	if updated.Title != newTitle {
		t.Errorf("Title = %q, want %q", updated.Title, newTitle)
	}
	if !ds.isPrimaryDown() {
		t.Error("primary debería marcarse down tras el error real")
	}
	if ds.PendingCount() != 1 {
		t.Errorf("PendingCount = %d, want 1", ds.PendingCount())
	}
}

func TestDualStore_DeleteObservation_FallsBackAndEnqueues(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	obs, err := ds.buffer.SaveObservation(ctx, SaveParams{
		Type: TypeDiscovery, Title: "t1", Content: "c1", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}

	ds.primary.Close()
	ds.down = false

	if err := ds.DeleteObservation(ctx, obs.ID); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}
	got, err := ds.buffer.GetObservation(ctx, obs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.DeletedAt == nil {
		t.Error("la observación debería estar soft-deleted (DeletedAt seteado) en el buffer")
	}
	if ds.PendingCount() != 1 {
		t.Errorf("PendingCount = %d, want 1", ds.PendingCount())
	}
}

func TestDualStore_SavePassive_SavesWithPassiveType(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	sess, err := ds.CreateSession(ctx, "s-passive", "p", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	obs, err := ds.SavePassive(ctx, sess.ID, "p", "contenido capturado pasivamente")
	if err != nil {
		t.Fatalf("SavePassive: %v", err)
	}
	if obs.Type != TypePassive {
		t.Errorf("Type = %q, want %q", obs.Type, TypePassive)
	}
	if obs.Title == "" {
		t.Error("SavePassive debería autogenerar un título")
	}
}

func TestDualStore_EndSession_FallsBackAndEnqueues(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	if _, err := ds.buffer.CreateSession(ctx, "s-end", "p", "/tmp"); err != nil {
		t.Fatal(err)
	}

	ds.primary.Close()
	ds.down = false

	if err := ds.EndSession(ctx, "s-end", "resumen final"); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	got, err := ds.buffer.GetSession(ctx, "s-end")
	if err != nil {
		t.Fatal(err)
	}
	if got.EndedAt == nil {
		t.Error("la sesión debería tener EndedAt seteado en el buffer")
	}
	if ds.PendingCount() != 1 {
		t.Errorf("PendingCount = %d, want 1", ds.PendingCount())
	}
}

func TestDualStore_ReadMethods_DelegateToHealthyPrimary(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	sess, err := ds.CreateSession(ctx, "s-reads", "p-reads", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	obs, err := ds.SaveObservation(ctx, SaveParams{
		SessionID: sess.ID, Type: TypeDiscovery, Title: "buscar esto", Content: "contenido de prueba", Project: "p-reads",
	})
	if err != nil {
		t.Fatal(err)
	}

	if n, err := ds.CountObservations(ctx, "p-reads"); err != nil || n != 1 {
		t.Errorf("CountObservations = (%d, %v), want (1, nil)", n, err)
	}

	obss, err := ds.ListObservations(ctx, "p-reads", 10, 0)
	if err != nil || len(obss) != 1 {
		t.Errorf("ListObservations = (%d items, %v), want (1, nil)", len(obss), err)
	}

	all, err := ds.ListAll(ctx, "p-reads")
	if err != nil || len(all) != 1 {
		t.Errorf("ListAll = (%d items, %v), want (1, nil)", len(all), err)
	}

	sessObs, err := ds.ListSessionObservations(ctx, sess.ID)
	if err != nil || len(sessObs) != 1 {
		t.Errorf("ListSessionObservations = (%d items, %v), want (1, nil)", len(sessObs), err)
	}

	active, err := ds.GetActiveSession(ctx, "p-reads")
	if err != nil || active == nil || active.ID != sess.ID {
		t.Errorf("GetActiveSession = (%v, %v), want sesión %q", active, err, sess.ID)
	}

	sessions, err := ds.ListSessions(ctx, "p-reads", 10)
	if err != nil || len(sessions) != 1 {
		t.Errorf("ListSessions = (%d items, %v), want (1, nil)", len(sessions), err)
	}

	results, err := ds.Search(ctx, SearchParams{Query: "buscar", Project: "p-reads", Limit: 10})
	if err != nil || len(results) != 1 {
		t.Errorf("Search = (%d items, %v), want (1, nil)", len(results), err)
	}

	if err := ds.PersistInjectedIDs(ctx, sess.ID, []string{"1", "2"}); err != nil {
		t.Fatalf("PersistInjectedIDs: %v", err)
	}
	ids, err := ds.LoadInjectedIDs(ctx, sess.ID)
	if err != nil || len(ids) != 2 {
		t.Errorf("LoadInjectedIDs = (%v, %v), want ([1 2], nil)", ids, err)
	}

	_ = obs // usado arriba solo para poblar datos
}

func TestDualStore_LocalStoreAndClose(t *testing.T) {
	ds := newTestDualStore(t)
	if ds.LocalStore() != ds.buffer {
		t.Error("LocalStore() debería devolver el buffer")
	}
	ds.cancel = func() {} // evitar panic por cancel nil (newTestDualStore no arranca syncLoop)
	if err := ds.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestDualStore_FlushPending_ReplaysAllEntityTypes(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	ds.primary.Close()
	ds.down = false

	sess, err := ds.CreateSession(ctx, "s-flush", "p", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	obs, err := ds.SaveObservation(ctx, SaveParams{
		SessionID: sess.ID, Type: TypeDiscovery, Title: "flush me", Content: "contenido", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ds.RecordToolUse(ctx, sess.ID, "p", "Edit"); err != nil {
		t.Fatal(err)
	}
	if err := ds.EndSession(ctx, sess.ID, "listo"); err != nil {
		t.Fatal(err)
	}
	if ds.PendingCount() == 0 {
		t.Fatal("esperaba operaciones encoladas antes del flush")
	}

	// "reconectar" primary — un *Store SQLite nuevo y sano en vez del cerrado.
	freshPrimary := newInternalTestStore(t)
	ds.mu.Lock()
	ds.primary = freshPrimary
	ds.down = false
	ds.mu.Unlock()

	if !ds.FlushPending(context.Background()) {
		t.Fatal("FlushPending debería drenar toda la cola contra el primary reconectado")
	}
	if ds.PendingCount() != 0 {
		t.Errorf("PendingCount tras flush = %d, want 0", ds.PendingCount())
	}

	gotSess, err := freshPrimary.GetSession(ctx, sess.ID)
	if err != nil || gotSess == nil {
		t.Fatalf("la sesión debería haber llegado al primary tras el flush: %v", err)
	}
	if gotSess.EndedAt == nil {
		t.Error("EndSession también debería haberse reproducido")
	}
	// El ID de la observación replicada en freshPrimary NO es el mismo que
	// obs.ID — primary y buffer tienen secuencias de ID independientes (ver
	// TestDualStore_GetObservation_FallsBackWhenPrimaryLacksID). SyncID es
	// el identificador estable que sí viaja igual entre ambos.
	gotObs, err := freshPrimary.GetObservationBySyncID(ctx, obs.SyncID)
	if err != nil || gotObs == nil {
		t.Fatalf("la observación debería haber llegado al primary tras el flush: %v", err)
	}
}

func TestRetryState_NextInterval_EscalatesThroughSchedule(t *testing.T) {
	rs := &retryState{}
	first := rs.nextInterval()
	if first != 60*time.Second {
		t.Errorf("primer intervalo = %v, want 60s", first)
	}
	// agotar los 3 intentos de la primera etapa
	rs.nextInterval()
	rs.nextInterval()
	next := rs.nextInterval()
	if next != 5*time.Minute {
		t.Errorf("tras agotar la etapa de 60s, intervalo = %v, want 5m", next)
	}
}

func TestRetryState_Reset_ReturnsToFirstStage(t *testing.T) {
	rs := &retryState{phase: 3, attempts: 2}
	rs.reset()
	if rs.phase != 0 || rs.attempts != 0 {
		t.Errorf("reset() dejó phase=%d attempts=%d, want 0,0", rs.phase, rs.attempts)
	}
}

func TestIsFKError_DetectsBothDialects(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"pq: insert or update on table violates foreign key constraint", true},
		{"FOREIGN KEY constraint failed", true},
		{"syntax error near SELECT", false},
	}
	for _, c := range cases {
		if got := isFKError(errString(c.msg)); got != c.want {
			t.Errorf("isFKError(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
	if isFKError(nil) {
		t.Error("isFKError(nil) debería ser false")
	}
}

// errString es un error mínimo para probar isFKError/isDuplicateError sin
// depender de un driver SQL real.
type errString string

func (e errString) Error() string { return string(e) }

func TestIsDuplicateError_DetectsBothDialects(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"pq: duplicate key value violates unique constraint", true},
		{"UNIQUE constraint failed: sessions.id", true},
		{"connection refused", false},
	}
	for _, c := range cases {
		if got := isDuplicateError(errString(c.msg)); got != c.want {
			t.Errorf("isDuplicateError(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

func TestNewDualFromDSN_PrimaryUnreachable_DegradesGracefully(t *testing.T) {
	buffer := newInternalTestStore(t)
	ds, err := NewDualFromDSN(buffer, "postgresql://user:pass@127.0.0.1:1/nope")
	if err != nil {
		t.Fatalf("NewDualFromDSN no debería fallar aunque el primary sea inalcanzable: %v", err)
	}
	defer ds.Close()
	if !ds.isPrimaryDown() {
		t.Error("con un DSN inalcanzable, isPrimaryDown() debería ser true desde el arranque")
	}
}
