package store

import (
	"context"
	"testing"
)

// TestDualStore_TwoMachinesSharingPrimary_SecondMachineSeesExistingData
// verifica el escenario real de "sync entre dispositivos" del roadmap
// (docs/architecture.md, sección de limitaciones conocidas): un
// desarrollador cambia de laptop a desktop, ambas apuntando al mismo
// Postgres. Cada máquina tiene su PROPIO buffer SQLite local (vacío en la
// máquina nueva) pero comparten el mismo primary.
//
// No hace falta código nuevo para el camino de lectura — DualStore.Get*/
// List*/Search ya prueban primary antes que buffer (ver GetObservation,
// GetSession, etc.) — así que una máquina nueva, con buffer vacío, ya lee
// en tiempo real todo lo que el primary compartido tiene. Este test
// verifica exactamente eso, de punta a punta, para dejarlo probado en vez
// de asumido.
func TestDualStore_TwoMachinesSharingPrimary_SecondMachineSeesExistingData(t *testing.T) {
	sharedPrimary := newInternalTestStore(t) // simula el Postgres compartido

	machineA := &DualStore{
		primary: sharedPrimary,
		buffer:  newInternalTestStore(t), // buffer local de la laptop
		down:    false,
	}
	qA, err := newSyncQueue(machineA.buffer.DB())
	if err != nil {
		t.Fatal(err)
	}
	machineA.queue = qA

	ctx := context.Background()
	sess, err := machineA.CreateSession(ctx, "s-cross-device", "p", "/home/laptop")
	if err != nil {
		t.Fatal(err)
	}
	obs, err := machineA.SaveObservation(ctx, SaveParams{
		SessionID: sess.ID, Type: TypeDecision, Title: "decisión tomada en la laptop",
		Content: "contenido real", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}

	// "Máquina nueva" — mismo primary compartido, buffer local vacío, recién
	// arrancado. Simula sentarse en el desktop por primera vez.
	machineB := &DualStore{
		primary: sharedPrimary,
		buffer:  newInternalTestStore(t),
		down:    false,
	}
	qB, err := newSyncQueue(machineB.buffer.DB())
	if err != nil {
		t.Fatal(err)
	}
	machineB.queue = qB

	gotSess, err := machineB.GetSession(ctx, sess.ID)
	if err != nil || gotSess == nil {
		t.Fatalf("la máquina B debería ver la sesión creada en A vía el primary compartido: sess=%v err=%v", gotSess, err)
	}
	gotObs, err := machineB.GetObservation(ctx, obs.ID)
	if err != nil || gotObs == nil || gotObs.Content != "contenido real" {
		t.Fatalf("la máquina B debería ver la observación creada en A vía el primary compartido: obs=%v err=%v", gotObs, err)
	}

	results, err := machineB.Search(ctx, SearchParams{Query: "decisión", Project: "p", Limit: 10})
	if err != nil || len(results) != 1 {
		t.Fatalf("Search desde la máquina B debería encontrar la observación de A: %d resultados, err=%v", len(results), err)
	}

	// Y en la otra dirección: algo guardado en B (con su propio buffer vacío
	// al arrancar, primary sano) debe llegar al primary compartido y quedar
	// visible para A en su siguiente lectura — sin pasar por sync_queue,
	// porque el primary está sano en ambas.
	obsFromB, err := machineB.SaveObservation(ctx, SaveParams{
		SessionID: sess.ID, Type: TypeDiscovery, Title: "hallazgo desde el desktop",
		Content: "contenido B", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	gotFromA, err := machineA.GetObservation(ctx, obsFromB.ID)
	if err != nil || gotFromA == nil {
		t.Fatalf("la máquina A debería ver lo guardado en B vía el primary compartido: %v, err=%v", gotFromA, err)
	}

	if machineA.PendingCount() != 0 || machineB.PendingCount() != 0 {
		t.Error("con primary sano en ambas máquinas, sync_queue debería quedar vacía en las dos — nada debería caer a buffer-only")
	}
}

// TestDualStore_TwoMachines_OneOffline_CatchesUpOnReconnect cubre el caso
// más realista: la laptop está offline (sin acceso al Postgres compartido,
// ej. viajando) y sigue guardando en su buffer local + sync_queue. Al
// reconectar, FlushPending drena esa cola contra el primary compartido, y
// recién ahí el desktop (que nunca perdió conexión) puede verlo.
func TestDualStore_TwoMachines_OneOffline_CatchesUpOnReconnect(t *testing.T) {
	sharedPrimary := newInternalTestStore(t)

	laptop := &DualStore{primary: sharedPrimary, buffer: newInternalTestStore(t), down: false}
	q, err := newSyncQueue(laptop.buffer.DB())
	if err != nil {
		t.Fatal(err)
	}
	laptop.queue = q

	ctx := context.Background()
	sess, err := laptop.CreateSession(ctx, "s-offline", "p", "/home/laptop")
	if err != nil {
		t.Fatal(err)
	}

	// se cae la conexión al primary compartido (ej. sin internet)
	laptop.mu.Lock()
	laptop.down = true
	laptop.mu.Unlock()

	obs, err := laptop.SaveObservation(ctx, SaveParams{
		SessionID: sess.ID, Type: TypeDiscovery, Title: "hallazgo offline",
		Content: "descubierto sin conexión", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	if laptop.PendingCount() == 0 {
		t.Fatal("mientras está offline, la escritura debería quedar encolada")
	}

	desktop := &DualStore{primary: sharedPrimary, buffer: newInternalTestStore(t), down: false}
	qd, err := newSyncQueue(desktop.buffer.DB())
	if err != nil {
		t.Fatal(err)
	}
	desktop.queue = qd

	if gotObs, _ := desktop.GetObservation(ctx, obs.ID); gotObs != nil {
		t.Error("antes de reconectar, el desktop NO debería ver todavía el hallazgo offline de la laptop")
	}

	// la laptop recupera conexión
	laptop.mu.Lock()
	laptop.down = false
	laptop.mu.Unlock()
	if !laptop.FlushPending(ctx) {
		t.Fatal("FlushPending debería drenar la cola contra el primary reconectado")
	}

	gotFromSyncID, err := sharedPrimary.GetObservationBySyncID(ctx, obs.SyncID)
	if err != nil || gotFromSyncID == nil {
		t.Fatalf("tras el flush, el hallazgo debería existir en el primary compartido: %v, err=%v", gotFromSyncID, err)
	}

	results, err := desktop.Search(ctx, SearchParams{Query: "offline", Project: "p", Limit: 10})
	if err != nil || len(results) != 1 {
		t.Errorf("tras el flush de la laptop, el desktop debería encontrarlo por Search: %d resultados, err=%v", len(results), err)
	}
}
