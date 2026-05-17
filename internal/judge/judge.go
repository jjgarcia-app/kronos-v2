package judge

import (
	"context"
	"fmt"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/llm"
	"github.com/jjgarcia-app/kronos-v2/internal/relations"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

const (
	notConflictThreshold = float32(0.30) // similarity < 0.30 → not_conflict (no LLM needed)
	relatedThreshold     = float32(0.70) // similarity >= 0.70 → related (no LLM needed)
	batchSize            = 20
	interval             = 5 * time.Minute
)

// AutoJudge starts a background goroutine that periodically resolves
// pending memory_relations using cosine similarity from bge-m3.
// For the ambiguous range (0.30–0.70), uses the Ollama LLM (llmClient may be nil).
// No-op when rel is nil or embeddings are disabled.
func AutoJudge(ctx context.Context, st *store.Store, rel *relations.Detector, llmClient *llm.Client) {
	if rel == nil || !rel.Enabled() {
		return
	}
	go func() {
		// delay inicial para no interferir con el startup
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		for {
			runBatch(ctx, st, rel, llmClient)
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}()
}

func runBatch(ctx context.Context, st *store.Store, rel *relations.Detector, llmClient *llm.Client) {
	rels, err := st.ListRelations(ctx, "", store.JudgmentPending, batchSize, 0)
	if err != nil || len(rels) == 0 {
		return
	}
	for _, r := range rels {
		if ctx.Err() != nil {
			return
		}
		judgeOne(ctx, st, rel, llmClient, r)
	}
}

func judgeOne(ctx context.Context, st *store.Store, rel *relations.Detector, llmClient *llm.Client, r store.Relation) {
	src, err := st.GetObservationBySyncID(ctx, r.SourceID)
	if err != nil || src == nil {
		return
	}
	tgt, err := st.GetObservationBySyncID(ctx, r.TargetID)
	if err != nil || tgt == nil {
		return
	}

	// buscar similitud coseno entre source y target
	hits, err := rel.Similar(ctx, src.Title+" "+src.Content, 1, src.ID, 0.0)
	if err != nil {
		return
	}

	var similarity float32
	for _, h := range hits {
		if h.ObsID == tgt.ID {
			similarity = h.Similarity
			break
		}
	}
	if similarity == 0 && len(hits) == 0 {
		similarity = 0.1 // tgt no indexado aún → tratar como baja similitud
	}

	switch {
	case similarity < notConflictThreshold:
		// similitud muy baja → falso positivo del detector BM25
		reason := fmt.Sprintf("baja similitud semántica (%.2f) — falso positivo del detector BM25", similarity)
		_, _ = st.JudgeBySemantic(ctx, r.SourceID, r.TargetID, store.RelationNotConflict, 0.90, reason, "bge-m3")

	case similarity >= relatedThreshold:
		// similitud alta → relacionadas
		reason := fmt.Sprintf("alta similitud semántica (%.2f) — observaciones relacionadas", similarity)
		_, _ = st.JudgeBySemantic(ctx, r.SourceID, r.TargetID, store.RelationRelated, float64(similarity), reason, "bge-m3")

	default:
		// zona ambigua (0.30–0.70): delegar al LLM generativo
		judgeAmbiguous(ctx, st, llmClient, r, src, tgt, similarity)
	}
}

// judgeAmbiguous uses the generative LLM to resolve ambiguous relations (0.30–0.70 similarity).
// Falls back to leaving pending when the LLM is unavailable or returns an invalid response.
func judgeAmbiguous(ctx context.Context, st *store.Store, llmClient *llm.Client, r store.Relation,
	src, tgt *store.Observation, similarity float32) {

	if llmClient == nil {
		return // sin LLM → dejar pending para resolución manual
	}

	// timeout acotado para no bloquear el batch completo
	judgeCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()

	result, err := llmClient.JudgeRelation(judgeCtx, src.Title, src.Content, tgt.Title, tgt.Content, similarity)
	if err != nil || result == nil {
		return // LLM no disponible → dejar pending
	}

	model := "llama3.2"
	reason := fmt.Sprintf("[llm] %s (cosine=%.2f) — %s", result.Relation, similarity, result.Reason)
	_, _ = st.JudgeBySemantic(ctx, r.SourceID, r.TargetID, result.Relation, result.Confidence, reason, model)
}
