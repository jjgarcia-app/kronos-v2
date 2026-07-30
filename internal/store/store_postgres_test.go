package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// testPostgresDSN es la misma base de datos que usa kronos en esta máquina
// (ver ~/AppData/Roaming/kronos/config.json). Los tests de este archivo
// escriben y borran filas propias identificadas por un session_id único —
// no tocan datos reales.
const testPostgresDSN = "postgresql://postgres:kronos@localhost:5432/kronos?sslmode=disable"

func newTestPostgresStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.NewPostgres(testPostgresDSN)
	if err != nil {
		t.Skipf("Postgres no disponible en %s, se salta el test de integración: %v", testPostgresDSN, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestCountSessionMethods_RealPostgres es la regresión que faltaba: todos
// los tests de CountSessionPrompts/CountSessionObservations corrían contra
// SQLite (incluso los que dicen en su comentario "simula Postgres" —
// newTestDualStore usa store.New, o sea SQLite, para primary Y buffer). En
// producción real, estos métodos usaban s.db.QueryRowContext directo con
// placeholders "?" en vez de s.queryRow (que aplica rebind a "$1"...) —
// Postgres rechaza "?" con syntax error, el error quedaba silenciado por
// "_ = row.Scan(&n)", y el conteo volvía 0 siempre. El nudge de guardado
// jamás disparó en producción por esto. Este test corre contra Postgres
// real — es el único que hubiera atrapado el bug antes de que llegara a
// producción.
func TestCountSessionMethods_RealPostgres(t *testing.T) {
	s := newTestPostgresStore(t)
	ctx := context.Background()

	sessionID := "test-pg-count-" + time.Now().UTC().Format("20060102150405")
	t.Cleanup(func() {
		_, _ = s.DB().ExecContext(ctx, `DELETE FROM observations WHERE session_id = $1`, sessionID)
		_, _ = s.DB().ExecContext(ctx, `DELETE FROM user_prompts WHERE session_id = $1`, sessionID)
		_, _ = s.DB().ExecContext(ctx, `DELETE FROM sessions WHERE id = $1`, sessionID)
	})

	if _, err := s.CreateSession(ctx, sessionID, "p", "/tmp"); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePrompt(ctx, sessionID, "p", "prompt uno"); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePrompt(ctx, sessionID, "p", "prompt dos"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveObservation(ctx, store.SaveParams{
		SessionID: sessionID, Type: store.TypeDiscovery, Title: "obs", Content: "c", Project: "p",
	}); err != nil {
		t.Fatal(err)
	}

	if n := s.CountSessionPrompts(ctx, sessionID); n != 2 {
		t.Errorf("CountSessionPrompts contra Postgres real = %d, want 2 (¿volvió a romperse el rebind?)", n)
	}
	if n := s.CountSessionObservations(ctx, sessionID); n != 1 {
		t.Errorf("CountSessionObservations contra Postgres real = %d, want 1 (¿volvió a romperse el rebind?)", n)
	}
	if n := s.CountSessionPromptsSinceLastSave(ctx, sessionID); n != 0 {
		t.Errorf("CountSessionPromptsSinceLastSave contra Postgres real = %d, want 0 (ambos prompts son anteriores al save)", n)
	}
}
