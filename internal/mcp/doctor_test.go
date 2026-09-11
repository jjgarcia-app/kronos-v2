package mcp_test

import (
	"context"
	"strings"
	"testing"

	kronosmcp "github.com/jjgarcia-app/kronos-v2/internal/mcp"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// fakeDoctorStore envuelve store.Storer (nil, nunca se llama) y solo pisa
// Stats/ListRelations — a propósito NO implementa LocalStore(), así que
// s.localStore() (internal/mcp/server.go) no puede resolverlo a un
// *store.Store concreto. Simula el caso real de un backend primary-aware
// (DualStore) cuyos datos NO son accesibles vía el buffer local.
type fakeDoctorStore struct {
	store.Storer
	stats *store.Stats
	rels  []store.Relation
}

func (f *fakeDoctorStore) Stats(context.Context) (*store.Stats, error) {
	return f.stats, nil
}

func (f *fakeDoctorStore) ListRelations(context.Context, string, string, int, int) ([]store.Relation, error) {
	return f.rels, nil
}

// TestMemDoctor_ReadsFromStoreInterface_NotLocalStoreOnly reproduce el bug
// real: handleMemDoctor usaba s.localStore() — que solo resuelve a un
// *store.Store cuando el store implementa LocalStore() (DualStore) o ES
// directamente un *store.Store. Con cualquier otro store.Storer (o un
// DualStore cuyo primary está sano y no necesita pasar por el buffer),
// mem_doctor imprimía "Store: no disponible" y nunca mostraba los conteos
// reales — el síntoma exacto reportado: mem_doctor mostraba datos del buffer
// (849 obs / 3 relaciones pendientes) mientras mem_stats, ya primary-first,
// mostraba los 880 obs reales de Postgres.
//
// Este fake expone 880 obs / 0 relaciones pendientes (el estado real del
// primary en el bug reportado) — el código viejo no puede llegar a estos
// datos porque nunca llama a s.store.Stats()/ListRelations() directamente
// cuando localStore() no resuelve; el código nuevo sí.
func TestMemDoctor_ReadsFromStoreInterface_NotLocalStoreOnly(t *testing.T) {
	fake := &fakeDoctorStore{
		stats: &store.Stats{TotalObservations: 880, TotalSessions: 575, Projects: []string{"kronos-v2"}},
		rels:  nil,
	}
	srv := kronosmcp.New(fake, 10, 20)

	out := call(t, srv, "mem_doctor", map[string]any{})

	if !strings.Contains(out, "880 obs") {
		t.Errorf("mem_doctor no muestra los 880 obs del store — sigue dependiendo de localStore(): %s", out)
	}
	if !strings.Contains(out, "575 sesiones") {
		t.Errorf("mem_doctor no muestra las 575 sesiones del store: %s", out)
	}
	if !strings.Contains(out, "Relaciones pendientes**: ninguna") {
		t.Errorf("mem_doctor no reporta el estado real de relaciones (ninguna pendiente en el primary): %s", out)
	}
}

// TestMemDoctor_ReportsPendingRelationsFromStore confirma que, cuando el
// store SÍ tiene relaciones pendientes, mem_doctor las refleja — sin esto,
// un handler que ignorara el error de ListRelations silenciosamente
// mostraría siempre "ninguna" sin importar el estado real.
func TestMemDoctor_ReportsPendingRelationsFromStore(t *testing.T) {
	fake := &fakeDoctorStore{
		stats: &store.Stats{TotalObservations: 5, TotalSessions: 1},
		rels: []store.Relation{
			{ID: 1, SourceID: "a", TargetID: "b", JudgmentStatus: store.JudgmentPending},
		},
	}
	srv := kronosmcp.New(fake, 10, 20)

	out := call(t, srv, "mem_doctor", map[string]any{})

	if !strings.Contains(out, "Relaciones pendientes**: 1") {
		t.Errorf("mem_doctor no reflejó la relación pendiente del store: %s", out)
	}
}
