package store

import (
	"context"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := New(path)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return s
}

// TestNewID_DejaHuecosEntreIdsDelMismoMilisegundo fija el invariante que
// arregla la pérdida silenciosa de observaciones: SQLite numera las filas
// insertadas sin id explícito con max(id)+1, así que los ids que genera este
// proceso nunca pueden estar a menos de idStride de distancia — si lo
// estuvieran, ese max(id)+1 caería en el siguiente id de snowflake y, con
// INSERT OR IGNORE, la observación se perdía sin error.
func TestNewID_DejaHuecosEntreIdsDelMismoMilisegundo(t *testing.T) {
	prev := NewID()
	for i := 0; i < 500; i++ {
		got := NewID()
		if got <= prev {
			t.Fatalf("id %d no es mayor que el anterior %d", got, prev)
		}
		if dif := got - prev; dif < idStride {
			t.Fatalf("ids consecutivos demasiado cerca: %d y %d (distancia %d, mínimo %d) — SQLite puede asignar el de en medio", prev, got, dif, idStride)
		}
		prev = got
	}
}

// TestSaveObservation_ReintentaSiElIdChoca cubre la red de seguridad: si el id
// generado colisiona con una fila existente (el caso real: una fila insertada
// sin id explícito se quedó con el número que este proceso iba a usar), la
// observación NO se pierde — se reintenta con un id nuevo.
func TestSaveObservation_ReintentaSiElIdChoca(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "k.db"))
	defer s.Close()

	// Una fila que ocupa el id que le vamos a pedir al generador.
	const idOcupado = int64(900000000000000001)
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO observations
		(id, sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
		 normalized_hash, revision_count, duplicate_count, last_seen_at, created_at, updated_at)
		VALUES (?, 'ocupado', NULL, 'config', 'ocupa el id', 'x', '', 'p', 'project', '', 'h0', 1, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		idOcupado); err != nil {
		t.Fatalf("preparar la fila que ocupa el id: %v", err)
	}

	// El generador devuelve primero el id ocupado (colisión) y después ids libres.
	restore := idGen
	defer func() { idGen = restore }()
	colisiones := 0
	idGen = func() int64 {
		if colisiones == 0 {
			colisiones++
			return idOcupado
		}
		return restore()
	}

	obs, err := s.SaveObservation(ctx, SaveParams{
		Project: "p", Type: TypeConfig, Title: "config nueva", Content: "valor nuevo",
	})
	if err != nil {
		t.Fatalf("SaveObservation devolvió error en vez de reintentar: %v", err)
	}
	if obs == nil || obs.ID == idOcupado {
		t.Fatalf("esperaba la observación con un id nuevo, got %+v", obs)
	}

	// Y quedó guardada de verdad.
	got, err := s.GetObservation(ctx, obs.ID)
	if err != nil || got == nil {
		t.Fatalf("la observación no quedó en la base: obs=%+v err=%v", got, err)
	}
	if got.Title != "config nueva" {
		t.Errorf("guardada con otro título: %q", got.Title)
	}
}
