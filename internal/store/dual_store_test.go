package store

import (
	"context"
	"path/filepath"
	"testing"
)

// newTestDualStore arma un DualStore con dos Store SQLite reales (uno hace
// de "primary", otro de "buffer") sin necesitar Postgres — alcanza para
// probar la lógica de fallback de DualStore en sí.
func newTestDualStore(t *testing.T) *DualStore {
	t.Helper()
	primary, err := New(filepath.Join(t.TempDir(), "primary.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { primary.Close() })

	buffer, err := New(filepath.Join(t.TempDir(), "buffer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { buffer.Close() })

	q, err := newSyncQueue(buffer.DB())
	if err != nil {
		t.Fatal(err)
	}

	return &DualStore{
		primary: primary,
		buffer:  buffer,
		down:    false,
		queue:   q,
	}
}

// TestDualStore_GetObservation_FallsBackWhenPrimaryLacksID reproduce el bug
// real encontrado en producción: primary (Postgres) y buffer (SQLite) tienen
// secuencias de autoincrement independientes, así que un ID que existe en
// buffer puede simplemente no existir en primary. scanObservation devuelve
// (nil, nil) para "no encontrado" — antes del fix, DualStore.GetObservation
// interpretaba err==nil como "listo, esta es la respuesta" y jamás probaba
// el buffer, devolviendo un falso "no encontrado" pese a que el buffer sí
// tenía la observación.
func TestDualStore_GetObservation_FallsBackWhenPrimaryLacksID(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	// Solo se guarda en buffer, simulando que primary nunca recibió esta
	// fila (o la tiene con un ID distinto por el drift de autoincrement).
	obs, err := ds.buffer.SaveObservation(ctx, SaveParams{
		Type:    TypeDiscovery,
		Title:   "Solo en buffer",
		Content: "contenido que primary no tiene",
		Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := ds.GetObservation(ctx, obs.ID)
	if err != nil {
		t.Fatalf("GetObservation error inesperado: %v", err)
	}
	if got == nil {
		t.Fatal("GetObservation devolvió nil — no cayó al buffer pese a que primary no tenía la fila")
	}
	if got.Title != "Solo en buffer" {
		t.Errorf("Title = %q, want %q", got.Title, "Solo en buffer")
	}

	// primary sigue "up" — un simple "no encontrado" no debe marcarlo down.
	if ds.isPrimaryDown() {
		t.Error("primary no debería marcarse down por un simple 'no encontrado'")
	}
}

// TestDualStore_GetSession_FallsBackWhenPrimaryLacksID — mismo bug, misma
// familia de fix (GetSession/GetActiveSession comparten el patrón).
func TestDualStore_GetSession_FallsBackWhenPrimaryLacksID(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	sess, err := ds.buffer.CreateSession(ctx, "s-solo-buffer", "p", "/tmp")
	if err != nil {
		t.Fatal(err)
	}

	got, err := ds.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession error inesperado: %v", err)
	}
	if got == nil {
		t.Fatal("GetSession devolvió nil — no cayó al buffer pese a que primary no tenía la sesión")
	}
}
