package hooks_test

import (
	"context"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
)

// TestRunSessionEnd_ClosesOpenSession cubre el caso principal: a diferencia
// de Stop (que dispara por turno y por eso no puede cerrar sesiones),
// SessionEnd dispara una sola vez al final real — acá sí debe setear
// ended_at.
func TestRunSessionEnd_ClosesOpenSession(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateSession(ctx, "sess-end", "p", "/tmp"); err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{SessionID: "sess-end", CWD: "/tmp", Reason: "prompt_input_exit"}
	if err := hooks.RunSessionEnd(ctx, in, st); err != nil {
		t.Fatalf("RunSessionEnd: %v", err)
	}

	sess, err := st.GetSession(ctx, "sess-end")
	if err != nil {
		t.Fatal(err)
	}
	if sess == nil || sess.EndedAt == nil {
		t.Errorf("SessionEnd debería cerrar la sesión — got sess=%+v", sess)
	}
}

// TestRunSessionEnd_PreservesExistingSummary confirma que no pisa un resumen
// real ya guardado por mem_session_summary (mismo contrato que EndSession).
func TestRunSessionEnd_PreservesExistingSummary(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateSession(ctx, "sess-end-sum", "p", "/tmp"); err != nil {
		t.Fatal(err)
	}
	if err := st.EndSession(ctx, "sess-end-sum", "resumen real ya guardado"); err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{SessionID: "sess-end-sum", CWD: "/tmp"}
	if err := hooks.RunSessionEnd(ctx, in, st); err != nil {
		t.Fatalf("RunSessionEnd: %v", err)
	}

	sess, err := st.GetSession(ctx, "sess-end-sum")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Summary != "resumen real ya guardado" {
		t.Errorf("Summary = %q, no debería haberse pisado", sess.Summary)
	}
}

func TestRunSessionEnd_SessionNeverCreated_NoError(t *testing.T) {
	st := newTestStore(t)
	in := hooks.Input{SessionID: "sess-nunca-creada", CWD: "/tmp"}
	if err := hooks.RunSessionEnd(context.Background(), in, st); err != nil {
		t.Errorf("RunSessionEnd no debería fallar aunque la sesión nunca se haya creado: %v", err)
	}
}

func TestRunSessionEnd_EmptySessionID_Noop(t *testing.T) {
	st := newTestStore(t)
	in := hooks.Input{CWD: "/tmp"}
	if err := hooks.RunSessionEnd(context.Background(), in, st); err != nil {
		t.Fatalf("empty session_id should be a no-op: %v", err)
	}
}

func TestRunSessionEnd_AlreadyEnded_NoError(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateSession(ctx, "sess-end-twice", "p", "/tmp"); err != nil {
		t.Fatal(err)
	}
	if err := st.EndSession(ctx, "sess-end-twice", "ya cerrada"); err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{SessionID: "sess-end-twice", CWD: "/tmp"}
	if err := hooks.RunSessionEnd(ctx, in, st); err != nil {
		t.Errorf("RunSessionEnd sobre una sesión ya cerrada no debería fallar: %v", err)
	}
}
