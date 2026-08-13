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

// dayEntries flattens every SessionDayEntry across all days in the report —
// helper for tests that just want "every session-day contribution", not the
// per-day grouping itself.
func dayEntries(report *TimesheetReport) []*SessionDayEntry {
	var out []*SessionDayEntry
	for _, d := range report.Days {
		out = append(out, d.Sessions...)
	}
	return out
}

// dayMinutes returns the deduplicated minutes for a given day, or 0 if that
// day isn't in the report.
func dayMinutes(report *TimesheetReport, day string) int {
	for _, d := range report.Days {
		if d.Day == day {
			return d.Minutes
		}
	}
	return 0
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
	entries := dayEntries(report)
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Minutes != 30 {
		t.Errorf("Minutes = %d, want 30", e.Minutes)
	}
	if len(e.Observations) != 1 || e.Observations[0].Title != "fix real encontrado" {
		t.Errorf("Observations = %+v, want 1 con el fix guardado", e.Observations)
	}
	if report.TotalMinutes != 30 {
		t.Errorf("TotalMinutes = %d, want 30", report.TotalMinutes)
	}
	if got := dayMinutes(report, day.Format("2006-01-02")); got != 30 {
		t.Errorf("dayMinutes(%s) = %d, want 30", day.Format("2006-01-02"), got)
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
	entries := dayEntries(report)
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}

	// Cada sesión por separado ve sus propios 20/10 min.
	var sumPerSession int
	for _, e := range entries {
		sumPerSession += e.Minutes
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
	if got := dayMinutes(report, day.Format("2006-01-02")); got != 20 {
		t.Errorf("dayMinutes(%s) = %d, want 20", day.Format("2006-01-02"), got)
	}
}

// TestTimesheet_FindsLongLivedSessionByActivityNotStartDate reproduce el
// caso real que motiva este fix: una conversacion de Claude Code arranco
// semanas antes del rango pedido (esta misma sesion, por ejemplo, lleva
// desde el 27/jul activa) pero tuvo actividad real DENTRO del rango. Filtrar
// por started_at la dejaba invisible aunque casi todo su trabajo real cayera
// dentro de la ventana consultada.
func TestTimesheet_FindsLongLivedSessionByActivityNotStartDate(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	longAgo := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	rangeStart := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	activityDay := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)

	sess, err := s.CreateSession(ctx, "sess-long-lived", "p", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	// started_at queda MUY antes del rango consultado — el caso real.
	if _, err := s.exec(ctx, `UPDATE sessions SET started_at = ? WHERE id = ?`,
		longAgo.Format(time.RFC3339), sess.ID); err != nil {
		t.Fatal(err)
	}

	insertActivityAt(t, s, sess.ID, "p", activityDay)
	insertActivityAt(t, s, sess.ID, "p", activityDay.Add(15*time.Minute))

	report, err := s.Timesheet(ctx, rangeStart, rangeStart.AddDate(0, 0, 14), "p")
	if err != nil {
		t.Fatalf("Timesheet: %v", err)
	}
	entries := dayEntries(report)
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 (la sesión longeva debe aparecer por su actividad, no por started_at)", len(entries))
	}
	if entries[0].Minutes != 15 {
		t.Errorf("Minutes = %d, want 15", entries[0].Minutes)
	}
	if report.TotalMinutes != 15 {
		t.Errorf("TotalMinutes = %d, want 15", report.TotalMinutes)
	}
	// La sesión debe agruparse bajo el día REAL de su actividad
	// (activityDay, 2026-08-11), no bajo su started_at (longAgo, 2026-07-27
	// — fuera incluso del rango consultado).
	if len(report.Days) != 1 || report.Days[0].Day != "2026-08-11" {
		t.Fatalf("Days = %+v, want un solo día '2026-08-11'", report.Days)
	}
}

// TestTimesheet_ActivityTimestampsBoundedToRange confirma que una sesión
// longeva con actividad TANTO dentro como fuera del rango consultado solo
// aporta al reporte los minutos que realmente caen dentro de la ventana —
// no arrastra semanas de historia solo porque coincide el session_id.
func TestTimesheet_ActivityTimestampsBoundedToRange(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	longAgo := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	beforeRange := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC) // fuera de rango
	inRange := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)

	sess, err := s.CreateSession(ctx, "sess-mixed", "p", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	s.exec(ctx, `UPDATE sessions SET started_at = ? WHERE id = ?`, longAgo.Format(time.RFC3339), sess.ID)

	// Actividad ANTES del rango — no debe contarse.
	insertActivityAt(t, s, sess.ID, "p", beforeRange)
	insertActivityAt(t, s, sess.ID, "p", beforeRange.Add(20*time.Minute))
	// Actividad DENTRO del rango — sí debe contarse.
	insertActivityAt(t, s, sess.ID, "p", inRange)
	insertActivityAt(t, s, sess.ID, "p", inRange.Add(10*time.Minute))

	report, err := s.Timesheet(ctx,
		time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC),
		"p")
	if err != nil {
		t.Fatalf("Timesheet: %v", err)
	}
	entries := dayEntries(report)
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].Minutes != 10 {
		t.Errorf("Minutes = %d, want 10 (solo el gap dentro del rango, no los 20min de antes)", entries[0].Minutes)
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
	insertActivityAt(t, s, sessIn.ID, "p", inRange)

	sessOut, _ := s.CreateSession(ctx, "sess-out-date", "p", "/tmp")
	s.exec(ctx, `UPDATE sessions SET started_at = ? WHERE id = ?`, outOfRange.Format(time.RFC3339), sessOut.ID)
	insertActivityAt(t, s, sessOut.ID, "p", outOfRange)

	sessOtherProject, _ := s.CreateSession(ctx, "sess-other-proj", "otro-proyecto", "/tmp")
	s.exec(ctx, `UPDATE sessions SET started_at = ? WHERE id = ?`, inRange.Format(time.RFC3339), sessOtherProject.ID)
	insertActivityAt(t, s, sessOtherProject.ID, "otro-proyecto", inRange)

	report, err := s.Timesheet(ctx, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), "p")
	if err != nil {
		t.Fatalf("Timesheet: %v", err)
	}
	entries := dayEntries(report)
	if len(entries) != 1 || entries[0].Session.ID != "sess-in" {
		t.Errorf("entries = %+v, want solo sess-in (fuera de rango y de otro proyecto excluidos)", entries)
	}
}
