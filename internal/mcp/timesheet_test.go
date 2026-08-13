package mcp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// TestMemTimesheet_ReportsSessionWithObservations verifica el wiring de
// punta a punta del tool mem_timesheet: sesión creada hoy, con actividad
// real y una observación guardada, aparece en el reporte con su narrativa.
func TestMemTimesheet_ReportsSessionWithObservations(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	ctx := context.Background()

	sess, err := st.CreateSession(ctx, "sess-timesheet-1", "kronos-v2", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordToolUse(ctx, sess.ID, "kronos-v2", "Edit"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeBugfix, Title: "bug de timesheet arreglado", Content: "c",
		Project: "kronos-v2", SessionID: sess.ID,
	}); err != nil {
		t.Fatal(err)
	}

	today := time.Now().UTC().Format("2006-01-02")
	out := call(t, srv, "mem_timesheet", map[string]any{
		"from":    today,
		"to":      today,
		"project": "kronos-v2",
	})

	if !strings.Contains(out, "sess-tim") {
		t.Errorf("output no menciona la sesión: %s", out)
	}
	if !strings.Contains(out, "bug de timesheet arreglado") {
		t.Errorf("output no lista la observación guardada: %s", out)
	}
}

// TestMemTimesheet_NoSessions_ReportsEmptyWithoutError confirma que un rango
// sin sesiones no falla — responde con un mensaje claro, no un error.
func TestMemTimesheet_NoSessions_ReportsEmptyWithoutError(t *testing.T) {
	srv := newTestServer(t)

	out := call(t, srv, "mem_timesheet", map[string]any{
		"from": "2020-01-01",
		"to":   "2020-01-02",
	})

	if !strings.Contains(out, "Sin sesiones") {
		t.Errorf("output = %q, quería un mensaje de 'sin sesiones'", out)
	}
}

// TestMemTimesheet_InvalidDate_ReturnsError confirma que una fecha mal
// formada devuelve error en vez de un panic o una interpretación silenciosa.
func TestMemTimesheet_InvalidDate_ReturnsError(t *testing.T) {
	srv := newTestServer(t)
	callExpectError(t, srv, "mem_timesheet", map[string]any{"from": "no-es-una-fecha"})
}
