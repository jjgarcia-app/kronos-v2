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

// TestDualStore_CountSessionMethods cubre la delegación de DualStore hacia
// primary/buffer para CountSessionPrompts/CountSessionObservations/
// CountSessionPromptsSinceLastSave. NOTA: newTestDualStore usa store.New
// (SQLite) para primary Y buffer — este test NO ejercita el dialecto SQL de
// Postgres. El bug real de producción (placeholders "?" sin rebind,
// rechazados por Postgres con syntax error y silenciados por
// "_ = row.Scan(&n)") solo lo atrapa TestCountSessionMethods_RealPostgres
// (store_postgres_test.go), que corre contra Postgres de verdad.
func TestDualStore_CountSessionMethods(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	sess, err := ds.CreateSession(ctx, "s1", "p", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if err := ds.SavePrompt(ctx, sess.ID, "p", "prompt uno"); err != nil {
		t.Fatal(err)
	}
	if err := ds.SavePrompt(ctx, sess.ID, "p", "prompt dos"); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.SaveObservation(ctx, SaveParams{
		SessionID: sess.ID, Type: TypeDiscovery, Title: "obs", Content: "c", Project: "p",
	}); err != nil {
		t.Fatal(err)
	}

	if n := ds.CountSessionPrompts(ctx, sess.ID); n != 2 {
		t.Errorf("CountSessionPrompts = %d, want 2", n)
	}
	if n := ds.CountSessionObservations(ctx, sess.ID); n != 1 {
		t.Errorf("CountSessionObservations = %d, want 1", n)
	}
	if n := ds.CountSessionPromptsSinceLastSave(ctx, sess.ID); n != 0 {
		t.Errorf("CountSessionPromptsSinceLastSave = %d, want 0 (ambos prompts son anteriores al save)", n)
	}
}

// TestDualStore_IncrementSearchCount_FallsBackWhenPrimaryLacksSession
// reproduce el bug real: si primary está sano pero no tiene la fila de esta
// sesión (ej. sesión creada en buffer mientras primary estaba caído), el
// UPDATE en primary afecta 0 filas SIN error (*store.Store tiene contrato
// fail-open a propósito) — antes del fix, DualStore trataba eso como "listo"
// y jamás probaba el buffer, así que search_count nunca se incrementaba de
// verdad para esas sesiones y el gate de pre-tool-use quedaba bloqueado para
// siempre pese a buscar en serio. Confirmado en vivo contra la sesión real
// de esta conversación.
func TestDualStore_IncrementSearchCount_FallsBackWhenPrimaryLacksSession(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	// Solo se crea en buffer, simulando que primary nunca recibió esta sesión.
	sess, err := ds.buffer.CreateSession(ctx, "s-solo-buffer-search", "p", "/tmp")
	if err != nil {
		t.Fatal(err)
	}

	if err := ds.IncrementSearchCount(ctx, sess.ID); err != nil {
		t.Fatalf("IncrementSearchCount: %v", err)
	}

	got, err := ds.buffer.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SearchCount != 1 {
		t.Errorf("SearchCount = %d, want 1 — el incremento no llegó al buffer", got.SearchCount)
	}

	// primary sigue "up" — un simple "0 filas afectadas" no debe marcarlo down.
	if ds.isPrimaryDown() {
		t.Error("primary no debería marcarse down por 0 filas afectadas (no es un error real)")
	}

	if ds.PendingCount() != 1 {
		t.Errorf("PendingCount = %d, want 1 (debería quedar encolado para replay)", ds.PendingCount())
	}
}

// TestDualStore_RecordToolUse_FallsBackAndEnqueues sigue el mismo patrón de
// escritura que el resto de DualStore: si primary falla, va a buffer y
// queda encolada para replay.
func TestDualStore_RecordToolUse_FallsBackAndEnqueues(t *testing.T) {
	ds := newTestDualStore(t)
	ds.primary.Close() // simular primary caído
	ds.down = false    // isPrimaryDown() lo detecta recién al fallar la llamada

	ctx := context.Background()
	if err := ds.RecordToolUse(ctx, "s1", "kronos-v2", "Edit"); err != nil {
		t.Fatalf("RecordToolUse: %v", err)
	}

	stats, err := ds.buffer.ToolUsageStats(ctx, "kronos-v2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].ToolName != "Edit" {
		t.Errorf("buffer no tiene el registro esperado: %+v", stats)
	}
	if ds.PendingCount() != 1 {
		t.Errorf("PendingCount = %d, want 1 (debería haber quedado encolado para replay)", ds.PendingCount())
	}
}
