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
		c := NewOllamaFromConfig(ctx, cfg)
		if c == nil {
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

// NewOllamaFromConfig builds a *Client for the local Ollama server using the
// same base-URL/model resolution as NewFromConfig's "ollama" branch, but
// ALWAYS Ollama — independent of cfg.LLM.Provider. Pulled out as its own
// function for features that must stay local no matter what the configurable
// judge provider is set to (ver internal/hooks/pre_compact_capture.go: manda
// texto de la conversación a un LLM, nunca a un provider externo sin que el
// usuario lo pida explícitamente para esa feature puntual).
// Returns nil if Ollama doesn't respond to a ping within 2s.
func NewOllamaFromConfig(ctx context.Context, cfg config.Config) *Client {
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
		return nil
	}
	return c
}
