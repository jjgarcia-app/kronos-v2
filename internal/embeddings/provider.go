package embeddings

import (
	"context"
	"fmt"
	"net/http"
	"time"

	chromem "github.com/philippgille/chromem-go"
)

const (
	DefaultOllamaURL   = "http://localhost:11434"
	DefaultOllamaModel = "bge-m3"
)

// EmbeddingFunc is the signature chromem-go uses internally.
// We expose it so callers can pass it directly to Collection creation.
type EmbeddingFunc = chromem.EmbeddingFunc

// NewOllamaFunc returns a chromem-go EmbeddingFunc backed by a local Ollama server.
func NewOllamaFunc(model, baseURL string) EmbeddingFunc {
	return chromem.NewEmbeddingFuncOllama(model, baseURL)
}

// Ping checks whether Ollama is reachable at baseURL.
func Ping(ctx context.Context, baseURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/tags", nil)
	if err != nil {
		return false
	}
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// AutoFunc returns the best available EmbeddingFunc:
//  1. Ollama at localhost:11434 if running
//  2. nil if nothing is available (caller must handle)
func AutoFunc(ctx context.Context) (EmbeddingFunc, string, error) {
	if Ping(ctx, DefaultOllamaURL) {
		fn := NewOllamaFunc(DefaultOllamaModel, DefaultOllamaURL)
		return fn, fmt.Sprintf("ollama:%s", DefaultOllamaModel), nil
	}
	return nil, "none", fmt.Errorf("no embedding provider available (Ollama not running)")
}
