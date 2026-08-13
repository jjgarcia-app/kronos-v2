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

	report, err := s.Timesheet(ctx, day.Add(-time.Hour), day.Add(24*time.Hour), "p")
	if err != nil {
		t.Fatalf("Timesheet: %v", err)
	}
	if len(report.Sessions) != 1 {
		t.Fatalf("len(Sessions) = %d, want 1", len(report.Sessions))
	}
	e := report.Sessions[0]
	if e.ActiveMinutes != 30 {
		t.Errorf("ActiveMinutes = %d, want 30", e.ActiveMinutes)
	}
	if len(e.Observations) != 1 || e.Observations[0].Title != "fix real encontrado" {
		t.Errorf("Observations = %+v, want 1 con el fix guardado", e.Observations)
	}
	if report.TotalMinutes != 30 {
		t.Errorf("TotalMinutes = %d, want 30", report.TotalMinutes)
	}
	if got := report.DailyMinutes[day.Format("2006-01-02")]; got != 30 {
		t.Errorf("DailyMinutes[%s] = %d, want 30", day.Format("2006-01-02"), got)
	}
}

// TestTimesheet_OverlappingSessionsDoNotDoubleCountTotal es el caso real que
// motiva separar TotalMinutes de la suma de ActiveMinutes por sesión: un
// fork o subagente en background recibe su propio session_id del harness,
// así que dos sesiones pueden tener actividad real y solapada en el mismo
// rango de reloj. Sumar sus ActiveMinutes por separado contaría ese solape
// dos veces; TotalMinutes (fusionado desde el timeline combinado) no debe.
func TestTimesheet_OverlappingSessionsDoNotDoubleCountTotal(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	day := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)

	// Sesión principal: eventos a 0, 10, 20 min.
	main, err := s.CreateSession(ctx, "sess-main", "p", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	s.exec(ctx, `UPDATE sessions SET started_at = ? WHERE id = ?`, day.Format(time.RFC3339), main.ID)
	insertActivityAt(t, s, main.ID, "p", day)
	insertActivityAt(t, s, main.ID, "p", day.Add(10*time.Minute))
	insertActivityAt(t, s, main.ID, "p", day.Add(20*time.Minute))

	// Fork/subagente en background, corriendo en paralelo (mismo rango de
	// reloj: 5 a 15 min), con su propio session_id.
	fork, err := s.CreateSession(ctx, "sess-fork", "p", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	s.exec(ctx, `UPDATE sessions SET started_at = ? WHERE id = ?`, day.Add(5*time.Minute).Format(time.RFC3339), fork.ID)
	insertActivityAt(t, s, fork.ID, "p", day.Add(5*time.Minute))
	insertActivityAt(t, s, fork.ID, "p", day.Add(15*time.Minute))

	report, err := s.Timesheet(ctx, day.Add(-time.Hour), day.Add(24*time.Hour), "p")
	if err != nil {
		t.Fatalf("Timesheet: %v", err)
	}
	if len(report.Sessions) != 2 {
		t.Fatalf("len(Sessions) = %d, want 2", len(report.Sessions))
	}

	// Cada sesión por separado ve sus propios 20/10 min.
	var sumPerSession int
	for _, e := range report.Sessions {
		sumPerSession += e.ActiveMinutes
	}
	if sumPerSession != 30 { // 20 (main) + 10 (fork)
		t.Errorf("sumPerSession = %d, want 30 (20 main + 10 fork, contado por separado)", sumPerSession)
	}

	// Pero el timeline real solo tuvo actividad entre minuto 0 y 20 — el
	// fork corrió DENTRO de ese rango, no lo extendió. El total fusionado
	// debe ser 20, no 30.
	if report.TotalMinutes != 20 {
		t.Errorf("TotalMinutes = %d, want 20 (fusionado — el solape del fork no debe contarse aparte)", report.TotalMinutes)
	}
	if got := report.DailyMinutes[day.Format("2006-01-02")]; got != 20 {
		t.Errorf("DailyMinutes[%s] = %d, want 20", day.Format("2006-01-02"), got)
	}
}

func TestDailyActiveMinutes_SplitsGapAtMidnight(t *testing.T) {
	// Gap de 20min que cruza medianoche: 23:50 -> 00:10 del día siguiente.
	// 10min deben quedar en el día 1, 10min en el día 2.
	day1 := time.Date(2026, 8, 5, 23, 50, 0, 0, time.UTC)
	day2 := day1.Add(20 * time.Minute)

	got := dailyActiveMinutes([]time.Time{day1, day2})
	if got["2026-08-05"] != 10 {
		t.Errorf("day1 minutes = %d, want 10", got["2026-08-05"])
	}
	if got["2026-08-06"] != 10 {
		t.Errorf("day2 minutes = %d, want 10", got["2026-08-06"])
	}
}

// TestDailyActiveMinutes_ManyShortGapsMatchesActiveMinutes reproduce el bug
// real encontrado al probar con datos de produccion (557 eventos reales,
// TotalMinutes=268 pero DailyMinutes=111 para el unico dia involucrado):
// truncar cada gap individual a minutos enteros ANTES de sumar pierde todo
// gap menor a 1 minuto (normal entre tool calls consecutivos). Con
// suficientes gaps cortos, dailyActiveMinutes divergia de activeMinutes
// incluso cuando todos los eventos caen en el mismo dia — deben coincidir
// siempre en ese caso.
func TestDailyActiveMinutes_ManyShortGapsMatchesActiveMinutes(t *testing.T) {
	base := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	var events []time.Time
	// 200 eventos separados por 45s (0.75min) — cada uno individualmente
	// trunca a 0 si se redondea antes de sumar, pero deberian acumular
	// 200*45s = 150min reales.
	for i := 0; i < 200; i++ {
		events = append(events, base.Add(time.Duration(i)*45*time.Second))
	}

	want := activeMinutes(events)
	daily := dailyActiveMinutes(events)
	got := daily["2026-08-11"]
	if got != want {
		t.Errorf("dailyActiveMinutes = %d, want %d (debe coincidir con activeMinutes cuando todo cae en un solo dia)", got, want)
	}
	if got == 0 {
		t.Error("got 0 — el bug de truncar antes de sumar volvio")
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

	report, err := s.Timesheet(ctx, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), "p")
	if err != nil {
		t.Fatalf("Timesheet: %v", err)
	}
	if len(report.Sessions) != 1 || report.Sessions[0].Session.ID != "sess-in" {
		t.Errorf("Sessions = %+v, want solo sess-in (fuera de rango y de otro proyecto excluidos)", report.Sessions)
	}
}
