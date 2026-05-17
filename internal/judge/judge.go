package judge

import (
	"context"
	"fmt"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/relations"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

const (
	notConflictThreshold = float32(0.30) // similarity < 0.30 → not_conflict
	relatedThreshold     = float32(0.70) // similarity >= 0.70 → related
	batchSize            = 20
	interval             = 5 * time.Minute
)

// AutoJudge starts a background goroutine that periodically resolves
// pending memory_relations using cosine similarity from bge-m3.
// No-op when rel is nil or embeddings are disabled.
func AutoJudge(ctx context.Context, st *store.Store, rel *relations.Detector) {
	if rel == nil || !rel.Enabled() {
		return
	}
	go func() {
		// primera ejecución con delay para no bloquear el startup
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		for {
			runBatch(ctx, st, rel)
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}()
}

func runBatch(ctx context.Context, st *store.Store, rel *relations.Detector) {
	rels, err := st.ListRelations(ctx, "", store.JudgmentPending, batchSize, 0)
	if err != nil || len(rels) == 0 {
		return
	}
	for _, r := range rels {
		if ctx.Err() != nil {
			return
		}
		judgeOne(ctx, st, rel, r)
	}
}

func judgeOne(ctx context.Context, st *store.Store, rel *relations.Detector, r store.Relation) {
	// obtener observaciones por sync_id
	src, err := st.GetObservationBySyncID(ctx, r.SourceID)
	if err != nil || src == nil {
		return
	}
	tgt, err := st.GetObservationBySyncID(ctx, r.TargetID)
	if err != nil || tgt == nil {
		return
	}

	// buscar similitud: query = texto del source, buscar tgt en vector store
	hits, err := rel.Similar(ctx, src.Title+" "+src.Content, 1, src.ID, 0.0)
	if err != nil || len(hits) == 0 {
		return
	}

	// buscar si tgt está en los hits
	var similarity float32
	for _, h := range hits {
		if h.ObsID == tgt.ID {
			similarity = h.Similarity
			break
		}
	}
	if similarity == 0 {
		// tgt no aparece en los hits → similitud muy baja
		similarity = 0.1
	}

	var relation, reason string
	var confidence float64
	if similarity < notConflictThreshold {
		relation = store.RelationNotConflict
		reason = fmt.Sprintf("baja similitud semántica (%.2f) — falso positivo del detector BM25", similarity)
		confidence = 0.90
	} else if similarity >= relatedThreshold {
		relation = store.RelationRelated
		reason = fmt.Sprintf("alta similitud semántica (%.2f) — observaciones relacionadas", similarity)
		confidence = float64(similarity)
	} else {
		// zona media (0.30-0.70): dejar pending para juicio manual
		return
	}

	_, _ = st.JudgeBySemantic(ctx, r.SourceID, r.TargetID, relation, confidence, reason, "bge-m3")
}
