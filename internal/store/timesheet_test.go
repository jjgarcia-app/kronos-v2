package store

import (
	"context"
	"testing"
	"time"
)

func TestActiveMinutes_SumsGapsUnder30Min(t *testing.T) {
	base := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	events := []time.Time{
		base,
		base.Add(10 * time.Minute),
		base.Add(20 * time.Minute), // +10min desde el anterior
	}
	if got := activeMinutes(events); got != 20 {
		t.Errorf("activeMinutes = %d, want 20", got)
	}
}

func TestActiveMinutes_DiscardsGapsOver30Min(t *testing.T) {
	base := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	events := []time.Time{
		base,
		base.Add(10 * time.Minute),  // +10min, cuenta
		base.Add(2 * time.Hour),     // +110min, gap > 30min, se descarta
		base.Add(2*time.Hour + 5*time.Minute), // +5min, cuenta
	}
	if got := activeMinutes(events); got != 15 {
		t.Errorf("activeMinutes = %d, want 15 (10 + 5, el hueco de 110min no cuenta)", got)
	}
}

func TestActiveMinutes_FewerThanTwoEvents_ReturnsZero(t *testing.T) {
	if got := activeMinutes(nil); got != 0 {
		t.Errorf("activeMinutes(nil) = %d, want 0", got)
	}
	if got := activeMinutes([]time.Time{time.Now()}); got != 0 {
		t.Errorf("activeMinutes(1 evento) = %d, want 0", got)
	}
}

// insertActivityAt inserta filas de tool_usage/user_prompts con created_at
// controlado — RecordToolUse/SavePrompt usan now(), que no sirve para
// probar el descuento de huecos con precisión.
func insertActivityAt(t *testing.T, s *Store, sessionID, project string, ts time.Time) {
	t.Helper()
	_, err := s.exec(context.Background(),
		`INSERT INTO tool_usage (session_id, project, tool_name, created_at) VALUES (?, ?, ?, ?)`,
		sessionID, project, "Edit", ts.UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
}

func TestTimesheet_ComputesActiveMinutesAndListsObservations(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	day := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	sess, err := s.CreateSession(ctx, "sess-ts-1", "p", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	// forzar started_at al día de prueba, no "ahora" (CreateSession usa now())
	if _, err := s.exec(ctx, `UPDATE sessions SET started_at = ? WHERE id = ?`,
		day.Format(time.RFC3339), sess.ID); err != nil {
		t.Fatal(err)
	}

	insertActivityAt(t, s, sess.ID, "p", day)
	insertActivityAt(t, s, sess.ID, "p", day.Add(15*time.Minute))
	insertActivityAt(t, s, sess.ID, "p", day.Add(30*time.Minute))

	if _, err := s.SaveObservation(ctx, SaveParams{
		Type: TypeBugfix, Title: "fix real encontrado", Content: "c", Project: "p", SessionID: sess.ID,
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := s.Timesheet(ctx, day.Add(-time.Hour), day.Add(24*time.Hour), "p")
	if err != nil {
		t.Fatalf("Timesheet: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.ActiveMinutes != 30 {
		t.Errorf("ActiveMinutes = %d, want 30", e.ActiveMinutes)
	}
	if len(e.Observations) != 1 || e.Observations[0].Title != "fix real encontrado" {
		t.Errorf("Observations = %+v, want 1 con el fix guardado", e.Observations)
	}
}

func TestTimesheet_FiltersByProjectAndDateRange(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	inRange := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	outOfRange := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	sessIn, _ := s.CreateSession(ctx, "sess-in", "p", "/tmp")
	s.exec(ctx, `UPDATE sessions SET started_at = ? WHERE id = ?`, inRange.Format(time.RFC3339), sessIn.ID)

	sessOut, _ := s.CreateSession(ctx, "sess-out-date", "p", "/tmp")
	s.exec(ctx, `UPDATE sessions SET started_at = ? WHERE id = ?`, outOfRange.Format(time.RFC3339), sessOut.ID)

	sessOtherProject, _ := s.CreateSession(ctx, "sess-other-proj", "otro-proyecto", "/tmp")
	s.exec(ctx, `UPDATE sessions SET started_at = ? WHERE id = ?`, inRange.Format(time.RFC3339), sessOtherProject.ID)

	entries, err := s.Timesheet(ctx, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), "p")
	if err != nil {
		t.Fatalf("Timesheet: %v", err)
	}
	if len(entries) != 1 || entries[0].Session.ID != "sess-in" {
		t.Errorf("entries = %+v, want solo sess-in (fuera de rango y de otro proyecto excluidos)", entries)
	}
}
