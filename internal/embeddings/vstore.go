package embeddings

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	chromem "github.com/philippgille/chromem-go"
)

const collectionName = "observations"

// SimilarResult holds a single vector-similarity hit.
type SimilarResult struct {
	ObsID      int64
	Similarity float32
	Snippet    string // first 100 chars of content
}

// VectorStore wraps a chromem-go collection for observation embeddings.
// All methods are safe to call when vs is nil — they become no-ops.
type VectorStore struct {
	db         *chromem.DB
	collection *chromem.Collection
	embedFn    EmbeddingFunc
	provider   string
}

// New opens (or creates) a persistent vector store at dataDir.
// Returns (nil, nil) when no embedding provider is available — callers
// must treat a nil *VectorStore as "embeddings disabled".
func New(ctx context.Context, dataDir string) (*VectorStore, error) {
	fn, provider, err := AutoFunc(ctx)
	if err != nil {
		return nil, nil // graceful: no provider, not an error for the caller
	}

	db, err := chromem.NewPersistentDB(dataDir, false)
	if err != nil {
		return nil, fmt.Errorf("open vector db: %w", err)
	}

	col, err := db.GetOrCreateCollection(collectionName, nil, fn)
	if err != nil {
		return nil, fmt.Errorf("create collection: %w", err)
	}

	return &VectorStore{
		db:         db,
		collection: col,
		embedFn:    fn,
		provider:   provider,
	}, nil
}

// NewInMemory creates an in-memory vector store with the given embedding function.
// Intended for tests.
func NewInMemory(fn EmbeddingFunc) (*VectorStore, error) {
	db := chromem.NewDB()
	col, err := db.GetOrCreateCollection(collectionName, nil, fn)
	if err != nil {
		return nil, err
	}
	return &VectorStore{db: db, collection: col, embedFn: fn, provider: "test"}, nil
}

// Provider returns a description of the active embedding provider.
func (vs *VectorStore) Provider() string {
	if vs == nil {
		return "none"
	}
	return vs.provider
}

// Index adds or replaces an observation's embedding.
func (vs *VectorStore) Index(ctx context.Context, id int64, content string) error {
	if vs == nil {
		return nil
	}
	docID := obsDocID(id)
	snippet := content
	if len(snippet) > 100 {
		snippet = snippet[:100]
	}
	doc := chromem.Document{
		ID:      docID,
		Content: content,
		Metadata: map[string]string{
			"obs_id":  strconv.FormatInt(id, 10),
			"snippet": snippet,
		},
	}
	return vs.collection.AddDocument(ctx, doc)
}

// Remove deletes an observation's embedding by ID.
func (vs *VectorStore) Remove(ctx context.Context, id int64) error {
	if vs == nil {
		return nil
	}
	return vs.collection.Delete(ctx, nil, nil, obsDocID(id))
}

// Similar returns observations whose embeddings are most similar to text,
// excluding the observation with excludeID (pass 0 to skip no one).
// Returns at most limit results with similarity >= minSimilarity.
func (vs *VectorStore) Similar(ctx context.Context, text string, limit int, excludeID int64, minSimilarity float32) ([]SimilarResult, error) {
	if vs == nil {
		return nil, nil
	}
	count := vs.collection.Count()
	if count == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}

	// Ask for limit+1 so we can drop excludeID if it appears; cap at count.
	nResults := limit + 1
	if nResults > count {
		nResults = count
	}

	raw, err := vs.collection.Query(ctx, text, nResults, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("vector query: %w", err)
	}

	var out []SimilarResult
	for _, r := range raw {
		if r.Similarity < minSimilarity {
			continue
		}
		id, _ := strconv.ParseInt(r.Metadata["obs_id"], 10, 64)
		if excludeID != 0 && id == excludeID {
			continue
		}
		out = append(out, SimilarResult{
			ObsID:      id,
			Similarity: r.Similarity,
			Snippet:    strings.TrimSpace(r.Metadata["snippet"]),
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func obsDocID(id int64) string {
	return fmt.Sprintf("obs-%d", id)
}
