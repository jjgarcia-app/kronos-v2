package store

import (
	"context"
	"path/filepath"
	"testing"
)

// strictOpts son los defaults endurecidos de config.RelationsConfig
// (ver internal/config/config.go) replicados a mano para no importar el
// paquete config desde internal/store (evita el ciclo store->config->...).
func strictOpts(project string) CandidateOptions {
	return CandidateOptions{
		Project:         project,
		Limit:           3,
		BM25Floor:       -6.0,
		MinSharedTokens: 2,
		RequireSameType: true,
	}
}

// TestFindCandidates_OneSharedWordDifferentType_NoPending reproduce el
// hallazgo medido en producción el 2026-09-11: con el filtro viejo (BM25Floor
// permisivo, sin min de tokens compartidos ni de tipo), dos observaciones que
// solo comparten UNA palabra común ("postgres") y son de tipos distintos
// generaban un pending — de los 11 pendientes juzgados a mano ese día, los 11
// eran falsos positivos de este tipo. Con los knobs endurecidos, no debe
// insertarse nada.
func TestFindCandidates_OneSharedWordDifferentType_NoPending(t *testing.T) {
	st, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()

	_, err = st.SaveObservation(ctx, SaveParams{
		Type: TypeBugfix, Title: "Arreglamos bug de reconexión postgres", Content: "c", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	obsB, err := st.SaveObservation(ctx, SaveParams{
		Type: TypePreference, Title: "Preferencia de reportes postgres export", Content: "c", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}

	cands, err := st.FindCandidates(ctx, obsB, strictOpts("p"))
	if err != nil {
		t.Fatalf("FindCandidates: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("FindCandidates = %d candidatos, want 0 (solo comparten 1 token significativo y son de tipo distinto)", len(cands))
	}

	rels, err := st.ListRelations(ctx, "p", JudgmentPending, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 0 {
		t.Fatalf("ListRelations pending = %d, want 0 — no debería haberse insertado ninguna fila", len(rels))
	}
}

// TestFindCandidates_TwoSharedWordsSameType_GeneratesPending es la
// contraparte: con ≥2 tokens significativos compartidos y el mismo tipo, el
// candidato es real y debe insertarse como pending.
func TestFindCandidates_TwoSharedWordsSameType_GeneratesPending(t *testing.T) {
	st, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()

	_, err = st.SaveObservation(ctx, SaveParams{
		Type: TypeConfig, Title: "Migrado kronos de SQLite a Postgres", Content: "c", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	obsB, err := st.SaveObservation(ctx, SaveParams{
		Type: TypeConfig, Title: "Migrado sistema kronos a Docker", Content: "c", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}

	cands, err := st.FindCandidates(ctx, obsB, strictOpts("p"))
	if err != nil {
		t.Fatalf("FindCandidates: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("FindCandidates = %d candidatos, want 1 (comparten 'migrado' y 'kronos', mismo tipo config)", len(cands))
	}

	rels, err := st.ListRelations(ctx, "p", JudgmentPending, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 {
		t.Fatalf("ListRelations pending = %d, want 1", len(rels))
	}
}

// TestFindCandidates_RequireSameType_SharedTopicKeyOverridesType cubre la
// otra mitad de require_same_type: dos observaciones de tipos DISTINTOS pero
// que comparten un topic_key no vacío también cuentan como "mismo tema" — el
// caso real es una decisión y su bugfix asociado sobre el mismo topic_key.
func TestFindCandidates_RequireSameType_SharedTopicKeyOverridesType(t *testing.T) {
	st, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()

	obsA, err := st.SaveObservation(ctx, SaveParams{
		Type: TypeDecision, Title: "Decision de arquitectura autenticación jwt", Content: "c", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	obsB, err := st.SaveObservation(ctx, SaveParams{
		Type: TypeBugfix, Title: "Bug en autenticación jwt token expirado", Content: "c", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Asignamos el mismo topic_key a mano vía SQL directo (no vía SaveParams:
	// SaveObservation hace upsert por topic_key+project, así que pasarlo ahí
	// fusionaría obsB dentro de obsA en vez de dejarlas como dos filas
	// independientes que comparten topic_key).
	const topicKey = "auth/estrategia-jwt"
	if _, err := st.exec(ctx, `UPDATE observations SET topic_key = ? WHERE id = ?`, topicKey, obsA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(ctx, `UPDATE observations SET topic_key = ? WHERE id = ?`, topicKey, obsB.ID); err != nil {
		t.Fatal(err)
	}
	obsB, err = st.GetObservation(ctx, obsB.ID)
	if err != nil {
		t.Fatal(err)
	}

	opts := CandidateOptions{
		Project:         "p",
		Limit:           3,
		BM25Floor:       -6.0,
		MinSharedTokens: 0, // deshabilitado a propósito: aislar el gate de require_same_type
		RequireSameType: true,
	}
	cands, err := st.FindCandidates(ctx, obsB, opts)
	if err != nil {
		t.Fatalf("FindCandidates: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("FindCandidates = %d candidatos, want 1 (tipos distintos pero mismo topic_key debe contar como mismo tema)", len(cands))
	}
}
