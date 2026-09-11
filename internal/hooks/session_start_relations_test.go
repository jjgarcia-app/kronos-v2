package hooks_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// storerWithRelations envuelve un *store.Store real y pisa ListRelations —
// a propósito NO implementa LocalStore(), así que localStoreOf(st) (el
// helper que printBacklogWarnings usaba antes) no puede resolverlo a un
// *store.Store concreto. Simula un DualStore primary-aware cuyo backlog de
// relaciones no vive en el buffer local accesible por type assertion.
type storerWithRelations struct {
	store.Storer
	rels []store.Relation
}

func (s *storerWithRelations) ListRelations(context.Context, string, string, int, int) ([]store.Relation, error) {
	return s.rels, nil
}

// TestRunSessionStart_WarnsOnRelationsBacklog reproduce el bug real: antes,
// printBacklogWarnings resolvía las relaciones pendientes vía
// localStoreOf(st) — un type assertion contra LocalStore()/*store.Store que
// falla en silencio para cualquier otro store.Storer, dejando el aviso de
// backlog de relaciones invisible sin importar cuántas relaciones pending
// hubiera. Ahora usa st.ListRelations(...) directo — parte de la interfaz
// Storer — así que el aviso dispara sin importar el tipo concreto detrás.
//
// 21 relaciones > backlogRelationsThreshold (20, no exportado en
// session_start.go) — ese número está acoplado a esa constante a propósito.
func TestRunSessionStart_WarnsOnRelationsBacklog(t *testing.T) {
	setupTempDataDir(t)
	base := newTestStore(t)

	rels := make([]store.Relation, 21)
	for i := range rels {
		rels[i] = store.Relation{ID: int64(i + 1), JudgmentStatus: store.JudgmentPending}
	}
	st := &storerWithRelations{Storer: base, rels: rels}

	in := hooks.Input{SessionID: "sess-rel-backlog", CWD: t.TempDir()}
	out := captureStdout(t, func() {
		if err := hooks.RunSessionStart(context.Background(), in, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	if !strings.Contains(out, "relaciones sin juzgar") {
		t.Errorf("esperaba aviso de backlog de relaciones, salida: %s", out)
	}
}

// TestRunSessionStart_NoRelationsWarningBelowThreshold — mismo criterio que
// TestRunSessionStart_NoWarningBelowThreshold pero para relaciones: no
// avisar por debajo del umbral es tan importante como avisar por encima.
func TestRunSessionStart_NoRelationsWarningBelowThreshold(t *testing.T) {
	setupTempDataDir(t)
	base := newTestStore(t)

	st := &storerWithRelations{Storer: base, rels: []store.Relation{
		{ID: 1, JudgmentStatus: store.JudgmentPending},
	}}

	in := hooks.Input{SessionID: "sess-rel-nobacklog", CWD: t.TempDir()}
	out := captureStdout(t, func() {
		if err := hooks.RunSessionStart(context.Background(), in, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	if strings.Contains(out, "relaciones sin juzgar") {
		t.Errorf("no esperaba aviso de backlog de relaciones por debajo del umbral, salida: %s", out)
	}
}
