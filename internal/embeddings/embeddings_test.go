package embeddings_test

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
)

// deterministicFn generates a fake embedding based on character byte values.
// Same text → same vector, different texts → different vectors.
// Not semantically meaningful, but sufficient for unit tests.
func deterministicFn(_ context.Context, text string) ([]float32, error) {
	const dim = 8
	vec := make([]float32, dim)
	for i, ch := range text {
		vec[i%dim] += float32(ch)
	}
	// L2-normalize
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

func TestVectorStore_IndexAndSimilar(t *testing.T) {
	vs, err := embeddings.NewInMemory(deterministicFn)
	if err != nil {
		t.Fatalf("NewInMemory: %v", err)
	}

	ctx := context.Background()

	// Index three observations.
	docs := []struct {
		id      int64
		content string
	}{
		{1, "SQLite FTS5 soporta búsqueda full-text con BM25"},
		{2, "Go compila a binario único sin dependencias externas"},
		{3, "SQLite WAL mode mejora la concurrencia de escritura"},
	}
	for _, d := range docs {
		if err := vs.Index(ctx, d.id, d.content); err != nil {
			t.Fatalf("Index(%d): %v", d.id, err)
		}
	}

	// Query with text similar to docs 1 and 3.
	results, err := vs.Similar(ctx, "SQLite búsqueda full-text", 3, 0, 0.0)
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
}

func TestVectorStore_ExcludesSelf(t *testing.T) {
	vs, err := embeddings.NewInMemory(deterministicFn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := vs.Index(ctx, 42, "golang generics type parameters"); err != nil {
		t.Fatal(err)
	}

	results, err := vs.Similar(ctx, "golang generics type parameters", 5, 42, 0.0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.ObsID == 42 {
			t.Error("self should be excluded from similar results")
		}
	}
}

func TestVectorStore_Remove(t *testing.T) {
	vs, err := embeddings.NewInMemory(deterministicFn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := vs.Index(ctx, 7, "contenido para eliminar"); err != nil {
		t.Fatal(err)
	}
	if err := vs.Remove(ctx, 7); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

func TestVectorStore_NilSafe(t *testing.T) {
	var vs *embeddings.VectorStore
	ctx := context.Background()

	if err := vs.Index(ctx, 1, "text"); err != nil {
		t.Errorf("nil Index should be no-op: %v", err)
	}
	results, err := vs.Similar(ctx, "text", 3, 0, 0.0)
	if err != nil || results != nil {
		t.Errorf("nil Similar should return (nil, nil): results=%v err=%v", results, err)
	}
	if vs.Provider() != "none" {
		t.Errorf("nil Provider should return 'none'")
	}
}

func TestPing_LocalhostNotRunning(t *testing.T) {
	ctx := context.Background()
	// In CI / without Ollama, Ping should return false gracefully.
	result := embeddings.Ping(ctx, "http://localhost:11434")
	// We don't assert true/false — just that it doesn't panic or block.
	_ = result
}

func TestAutoFunc_NoOllamaReturnsError(t *testing.T) {
	ctx := context.Background()
	// If Ollama is not running, AutoFunc returns (nil, "none", err).
	// We can't guarantee Ollama state in CI, so just ensure no panic.
	fn, name, err := embeddings.AutoFunc(ctx)
	if err == nil {
		// Ollama is running — fn should be usable.
		if fn == nil {
			t.Error("AutoFunc returned nil func with nil error")
		}
		if !strings.HasPrefix(name, "ollama:") {
			t.Errorf("expected 'ollama:...' provider name, got: %s", name)
		}
	} else {
		// No provider — fn should be nil.
		if fn != nil {
			t.Error("AutoFunc returned non-nil func with error")
		}
	}
}
