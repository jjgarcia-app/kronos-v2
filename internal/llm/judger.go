package llm

import (
	"context"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
)

// Judger classifies the semantic relationship between two observations.
type Judger interface {
	JudgeRelation(ctx context.Context, aTitle, aContent, bTitle, bContent string, similarity float32) (*JudgeResult, error)
}

// NewFromConfig creates a Judger based on LLM configuration.
// Returns nil when the provider is disabled, not configured, or unavailable.
// For Ollama, pings the server first — returns nil gracefully if unreachable.
func NewFromConfig(ctx context.Context, cfg config.Config) Judger {
	provider := cfg.LLM.Provider
	if provider == "" {
		provider = "ollama"
	}

	switch provider {
	case "ollama":
		baseURL := cfg.LLM.BaseURL
		if baseURL == "" {
			baseURL = cfg.Embeddings.OllamaURL
		}
		if baseURL == "" {
			baseURL = DefaultBase
		}
		model := cfg.LLM.Model
		if model == "" {
			model = cfg.Embeddings.OllamaLLMModel
		}
		if model == "" {
			model = DefaultModel
		}
		c := NewClient(baseURL, model)
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := c.Ping(pingCtx); err != nil {
			return nil // Ollama no disponible — sin LLM judgment
		}
		return c

	case "openai", "openai-compatible":
		if cfg.LLM.APIKey == "" {
			return nil
		}
		baseURL := cfg.LLM.BaseURL
		if baseURL == "" {
			baseURL = "https://api.openai.com"
		}
		model := cfg.LLM.Model
		if model == "" {
			model = "gpt-4o-mini"
		}
		return NewOpenAIClient(baseURL, cfg.LLM.APIKey, model)

	case "anthropic":
		if cfg.LLM.APIKey == "" {
			return nil
		}
		model := cfg.LLM.Model
		if model == "" {
			model = "claude-haiku-4-5-20251001"
		}
		return NewAnthropicClient(cfg.LLM.APIKey, model)

	case "disabled":
		return nil
	}

	return nil
}
