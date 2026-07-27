package judge

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/llm"
	"github.com/jjgarcia-app/kronos-v2/internal/relations"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// fixedVectorEmbedFn devuelve el vector fijo de la tabla si el texto coincide
// exacto, o un vector "neutro" por default — alcanza para controlar la
// similitud coseno de forma determinística en los tests.
func fixedVectorEmbedFn(vectors map[string][]float32) embeddings.EmbeddingFunc {
	return func(_ context.Context, text string) ([]float32, error) {
		if v, ok := vectors[text]; ok {
			return v, nil
		}
		return []float32{0.5, 0.5}, nil
	}
}

// seedPendingRelation crea dos observaciones en el mismo proyecto y una
// relación pending real entre ellas vía FindCandidates (mismo camino que usa
// mem_save en producción) — evita depender de la función privada
// insertRelationPending directamente.
func seedPendingRelation(t *testing.T, st *store.Store, titleA, titleB string) (a, b *store.Observation, rel store.Relation) {
	t.Helper()
	ctx := context.Background()

	obsA, err := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDiscovery, Title: titleA, Content: "contenido A", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	obsB, err := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDiscovery, Title: titleB, Content: "contenido B", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}

	cands, err := st.FindCandidates(ctx, obsB, store.CandidateOptions{Project: "p", Limit: 5})
	if err != nil {
		t.Fatalf("FindCandidates: %v", err)
	}
	if len(cands) == 0 {
		t.Fatal("FindCandidates no encontró ningún candidato — el fixture de títulos no comparte términos BM25")
	}

	rels, err := st.ListRelations(ctx, "p", store.JudgmentPending, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) == 0 {
		t.Fatal("no se creó ninguna relación pending")
	}
	return obsA, obsB, rels[0]
}

func TestRunBatch_NoPendingRelations_NoOp(t *testing.T) {
	st := newTestStore(t)
	vs, err := embeddings.NewInMemory(fixedVectorEmbedFn(nil))
	if err != nil {
		t.Fatal(err)
	}
	rel := relations.New(vs)

	// no debe paniquear ni bloquear con la tabla de relaciones vacía
	runBatch(context.Background(), st, rel, nil)
}

func TestJudgeOne_HighSimilarity_MarksRelated(t *testing.T) {
	st := newTestStore(t)
	a, b, r := seedPendingRelation(t, st, "fix postgres deadlock", "fix postgres deadlock issue")

	textA := a.Title + " " + a.Content
	textB := b.Title + " " + b.Content
	vs, err := embeddings.NewInMemory(fixedVectorEmbedFn(map[string][]float32{
		textA: {1, 0},
		textB: {1, 0}, // idéntico → similitud coseno 1.0, >= relatedThreshold
	}))
	if err != nil {
		t.Fatal(err)
	}
	rel := relations.New(vs)
	ctx := context.Background()
	if err := rel.Index(ctx, a.ID, textA); err != nil {
		t.Fatal(err)
	}
	if err := rel.Index(ctx, b.ID, textB); err != nil {
		t.Fatal(err)
	}

	judgeOne(ctx, st, rel, nil, r)

	assertJudgedAs(t, st, "p", store.RelationRelated)
}

func TestJudgeOne_LowSimilarity_MarksNotConflict(t *testing.T) {
	st := newTestStore(t)
	a, b, r := seedPendingRelation(t, st, "fix postgres deadlock", "fix postgres deadlock issue")

	textA := a.Title + " " + a.Content
	textB := b.Title + " " + b.Content
	vs, err := embeddings.NewInMemory(fixedVectorEmbedFn(map[string][]float32{
		textA: {1, 0},
		textB: {0, 1}, // ortogonal → similitud coseno 0.0, < notConflictThreshold
	}))
	if err != nil {
		t.Fatal(err)
	}
	rel := relations.New(vs)
	ctx := context.Background()
	if err := rel.Index(ctx, a.ID, textA); err != nil {
		t.Fatal(err)
	}
	if err := rel.Index(ctx, b.ID, textB); err != nil {
		t.Fatal(err)
	}

	judgeOne(ctx, st, rel, nil, r)

	assertJudgedAs(t, st, "p", store.RelationNotConflict)
}

// assertJudgedAs busca la única relación 'judged' del proyecto y verifica
// que su veredicto (campo Relation) sea el esperado.
func assertJudgedAs(t *testing.T, st *store.Store, project, wantRelation string) {
	t.Helper()
	got, err := st.ListRelations(context.Background(), project, "judged", 10, 0)
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("esperaba 1 relación 'judged', hay %d", len(got))
	}
	if got[0].Relation != wantRelation {
		t.Errorf("Relation = %q, want %q", got[0].Relation, wantRelation)
	}
}

// fakeJudger implementa llm.Judger devolviendo un resultado fijo — no pega
// contra ningún LLM real.
type fakeJudger struct {
	result *llm.JudgeResult
	err    error
	calls  int
}

func (f *fakeJudger) JudgeRelation(_ context.Context, _, _, _, _ string, _ float32) (*llm.JudgeResult, error) {
	f.calls++
	return f.result, f.err
}

func TestJudgeAmbiguous_UsesLLMResult(t *testing.T) {
	st := newTestStore(t)
	_, _, r := seedPendingRelation(t, st, "fix postgres deadlock", "fix postgres deadlock issue")
	ctx := context.Background()

	src, _ := st.GetObservationBySyncID(ctx, r.SourceID)
	tgt, _ := st.GetObservationBySyncID(ctx, r.TargetID)

	fake := &fakeJudger{result: &llm.JudgeResult{
		Relation: store.RelationCompatible, Confidence: 0.8, Reason: "parecen compatibles",
	}}

	judgeAmbiguous(ctx, st, fake, r, src, tgt, 0.5)

	if fake.calls != 1 {
		t.Errorf("JudgeRelation calls = %d, want 1", fake.calls)
	}
	assertJudgedAs(t, st, "p", store.RelationCompatible)
}

func TestJudgeAmbiguous_NilLLMClient_LeavesPending(t *testing.T) {
	st := newTestStore(t)
	_, _, r := seedPendingRelation(t, st, "fix postgres deadlock", "fix postgres deadlock issue")
	ctx := context.Background()

	src, _ := st.GetObservationBySyncID(ctx, r.SourceID)
	tgt, _ := st.GetObservationBySyncID(ctx, r.TargetID)

	judgeAmbiguous(ctx, st, nil, r, src, tgt, 0.5)

	got, err := st.ListRelations(ctx, "p", store.JudgmentPending, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("relación debería seguir pending sin LLM, encontré %d pending", len(got))
	}
}

func TestJudgeAmbiguous_LLMErrorOnFirstTry_RetriesOnce(t *testing.T) {
	st := newTestStore(t)
	_, _, r := seedPendingRelation(t, st, "fix postgres deadlock", "fix postgres deadlock issue")
	ctx := context.Background()

	src, _ := st.GetObservationBySyncID(ctx, r.SourceID)
	tgt, _ := st.GetObservationBySyncID(ctx, r.TargetID)

	fake := &erroringThenSucceedingJudger{
		failFirst: true,
		result:    &llm.JudgeResult{Relation: store.RelationScoped, Confidence: 0.6, Reason: "ok en el segundo intento"},
	}

	judgeAmbiguous(ctx, st, fake, r, src, tgt, 0.5)

	if fake.calls != 2 {
		t.Errorf("esperaba retry (2 llamadas), hubo %d", fake.calls)
	}
	assertJudgedAs(t, st, "p", store.RelationScoped)
}

type erroringThenSucceedingJudger struct {
	failFirst bool
	calls     int
	result    *llm.JudgeResult
}

func (f *erroringThenSucceedingJudger) JudgeRelation(_ context.Context, _, _, _, _ string, _ float32) (*llm.JudgeResult, error) {
	f.calls++
	if f.calls == 1 && f.failFirst {
		return nil, errors.New("llm temporalmente no disponible")
	}
	return f.result, nil
}
