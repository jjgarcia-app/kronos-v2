package relations_test

import (
	"context"
	"math"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/relations"
)

func deterministicFn(_ context.Context, text string) ([]float32, error) {
	const dim = 8
	vec := make([]float32, dim)
	for i, ch := range text {
		vec[i%dim] += float32(ch)
	}
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] = float32(float64(vec[i]) / norm)
		}
	}
	return vec, nil
}

func newDetector(t *testing.T) *relations.Detector {
	t.Helper()
	vs, err := embeddings.NewInMemory(deterministicFn)
	if err != nil {
		t.Fatal(err)
	}
	return relations.New(vs)
}

func TestDetector_Enabled(t *testing.T) {
	d := newDetector(t)
	if !d.Enabled() {
		t.Error("detector with VectorStore should be enabled")
	}
}

func TestDetector_NilDisabled(t *testing.T) {
	d := relations.New(nil)
	if d.Enabled() {
		t.Error("detector with nil VectorStore should be disabled")
	}
}

func TestDetector_IndexAndCheck(t *testing.T) {
	d := newDetector(t)
	ctx := context.Background()

	// Index two similar observations and one unrelated.
	if err := d.Index(ctx, 1, "SQLite FTS5 permite búsqueda full-text en español"); err != nil {
		t.Fatal(err)
	}
	if err := d.Index(ctx, 2, "FTS5 usa BM25 para ranking de resultados de búsqueda"); err != nil {
		t.Fatal(err)
	}
	if err := d.Index(ctx, 3, "Go es un lenguaje compilado con garbage collection"); err != nil {
		t.Fatal(err)
	}

	// Check with a new text similar to 1 and 2.
	related, err := d.Check(ctx, 99, "búsqueda full-text SQLite")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	// Results depend on embedding similarity — just verify no panic and valid data.
	for _, r := range related {
		if r.Level != 2 {
			t.Errorf("expected level 2, got %d", r.Level)
		}
		if r.ObsID == 0 {
			t.Error("ObsID should not be zero")
		}
		if r.Similarity < 0 || r.Similarity > 1 {
			t.Errorf("similarity %f out of [0,1]", r.Similarity)
		}
	}
}

func TestDetector_CheckNilStore(t *testing.T) {
	d := relations.New(nil)
	ctx := context.Background()

	related, err := d.Check(ctx, 1, "any text here")
	if err != nil {
		t.Errorf("nil store Check should not error: %v", err)
	}
	if related != nil {
		t.Errorf("nil store Check should return nil: %v", related)
	}
}

func TestDetector_IndexNilStore(t *testing.T) {
	d := relations.New(nil)
	if err := d.Index(context.Background(), 1, "text"); err != nil {
		t.Errorf("nil store Index should be no-op: %v", err)
	}
}

func TestDetector_RemoveNilStore(t *testing.T) {
	d := relations.New(nil)
	if err := d.Remove(context.Background(), 1); err != nil {
		t.Errorf("nil store Remove should be no-op: %v", err)
	}
}
