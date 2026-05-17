package relations

import (
	"context"

	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
)

const (
	// SimilarityThreshold is the minimum cosine similarity to flag two
	// observations as potentially related.
	SimilarityThreshold float32 = 0.85

	// MaxRelated is the maximum number of related observations returned.
	MaxRelated = 3
)

// Related describes one observation that is semantically related to another.
type Related struct {
	ObsID      int64
	Similarity float32
	Snippet    string
	Level      int // 2 = vector similarity (Level 3 = LLM judge, not yet implemented)
}

// Detector uses the vector store to find semantically related observations.
// Safe to use when the underlying VectorStore is nil (embeddings disabled).
type Detector struct {
	vs *embeddings.VectorStore
}

// New creates a Detector. vs may be nil when embeddings are not available.
func New(vs *embeddings.VectorStore) *Detector {
	return &Detector{vs: vs}
}

// Check returns observations that are semantically similar to text,
// excluding the observation with selfID (0 = no exclusion).
// Returns nil when embeddings are disabled or no similar observations found.
func (d *Detector) Check(ctx context.Context, selfID int64, text string) ([]Related, error) {
	if d.vs == nil {
		return nil, nil
	}

	hits, err := d.vs.Similar(ctx, text, MaxRelated, selfID, SimilarityThreshold)
	if err != nil {
		return nil, err
	}

	out := make([]Related, 0, len(hits))
	for _, h := range hits {
		out = append(out, Related{
			ObsID:      h.ObsID,
			Similarity: h.Similarity,
			Snippet:    h.Snippet,
			Level:      2,
		})
	}
	return out, nil
}

// Index adds or updates an observation's embedding in the vector store.
// No-op when embeddings are disabled.
func (d *Detector) Index(ctx context.Context, id int64, content string) error {
	if d.vs == nil {
		return nil
	}
	return d.vs.Index(ctx, id, content)
}

// Remove deletes an observation's embedding from the vector store.
// No-op when embeddings are disabled.
func (d *Detector) Remove(ctx context.Context, id int64) error {
	if d.vs == nil {
		return nil
	}
	return d.vs.Remove(ctx, id)
}

// Similar returns observations whose embeddings are most similar to text,
// excluding the observation with excludeID (pass 0 to skip no one).
// Returns at most limit results with similarity >= minSimilarity.
// No-op when embeddings are disabled.
func (d *Detector) Similar(ctx context.Context, text string, limit int, excludeID int64, minSimilarity float32) ([]embeddings.SimilarResult, error) {
	if d.vs == nil {
		return nil, nil
	}
	return d.vs.Similar(ctx, text, limit, excludeID, minSimilarity)
}

// Enabled reports whether the detector has an active vector store.
func (d *Detector) Enabled() bool {
	return d.vs != nil
}
