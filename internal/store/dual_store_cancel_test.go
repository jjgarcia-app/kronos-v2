package store

import (
	"context"
	"errors"
	"testing"
)

// TestDualStore_MarkDown_IgnoresCallerCancellation reproduce el hallazgo del
// benchmark real (2026-09-11, 8 sesiones de Claude Code en paralelo): los dos
// únicos eventos "primary caído" del día fueron "context canceled" — un
// llamador que se fue (hook abortado, sesión cerrando, timeout del cliente),
// no una falla del store. markDown no debe degradar el primary por esto.
func TestDualStore_MarkDown_IgnoresCallerCancellation(t *testing.T) {
	ds := newTestDualStore(t)

	ds.markDown(context.Canceled)

	if ds.isPrimaryDown() {
		t.Fatal("markDown(context.Canceled) dejó el primary caído — un llamador cancelado no es una falla del store")
	}

	// Una lectura siguiente debe seguir yendo al primary: guardamos una
	// observación SOLO en primary (nunca tocamos buffer) y confirmamos que
	// GetObservation la encuentra sin caer al buffer.
	ctx := context.Background()
	obs, err := ds.primary.SaveObservation(ctx, SaveParams{
		Type: TypeDiscovery, Title: "solo en primary", Content: "c", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ds.GetObservation(ctx, obs.ID)
	if err != nil {
		t.Fatalf("GetObservation error inesperado: %v", err)
	}
	if got == nil || got.Title != "solo en primary" {
		t.Fatalf("GetObservation no leyó del primary tras markDown(context.Canceled): got=%+v", got)
	}
}

// TestDualStore_MarkDown_IgnoresCallerDeadlineExceeded — mismo caso que
// context.Canceled pero con el timeout del cliente en vez de una
// cancelación explícita.
func TestDualStore_MarkDown_IgnoresCallerDeadlineExceeded(t *testing.T) {
	ds := newTestDualStore(t)

	ds.markDown(context.DeadlineExceeded)

	if ds.isPrimaryDown() {
		t.Fatal("markDown(context.DeadlineExceeded) dejó el primary caído — un timeout del llamador no es una falla del store")
	}
}

// TestDualStore_MarkDown_RealFailureStillMarksDown asegura que NO se aflojó
// la detección real de fallas: un error de conexión genuino (no relacionado
// con cancelación del contexto) debe seguir marcando el primary caído.
func TestDualStore_MarkDown_RealFailureStillMarksDown(t *testing.T) {
	ds := newTestDualStore(t)

	ds.markDown(errors.New("connection reset by peer"))

	if !ds.isPrimaryDown() {
		t.Fatal("markDown(connection reset by peer) debería dejar el primary caído — es una falla real, no cancelación del llamador")
	}
}

// TestIsCallerCanceled cubre isCallerCanceled directamente, incluyendo el
// respaldo por string para errores de pgx que no preservan la cadena
// errors.Is hasta context.Canceled.
func TestIsCallerCanceled(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context.Canceled directo", context.Canceled, true},
		{"context.DeadlineExceeded directo", context.DeadlineExceeded, true},
		{"wrapped context.Canceled", errWrapped(context.Canceled), true},
		{"string fallback pgx", errors.New("failed to connect: context canceled"), true},
		{"falla real de conexión", errors.New("connection reset by peer"), false},
	}

	for _, c := range cases {
		if got := isCallerCanceled(c.err); got != c.want {
			t.Errorf("%s: isCallerCanceled(%v) = %v, want %v", c.name, c.err, got, c.want)
		}
	}
}

func errWrapped(err error) error {
	return &wrappedErr{err}
}

type wrappedErr struct{ inner error }

func (w *wrappedErr) Error() string { return "consulta: " + w.inner.Error() }
func (w *wrappedErr) Unwrap() error { return w.inner }
