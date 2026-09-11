package llm

import (
	"context"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
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

	case "claude-cli":
		c := NewClaudeCLIFromConfig(ctx, cfg)
		if c == nil {
			return nil // sin credenciales o config dir — sin LLM judgment
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

// NewGenerationClientFromConfig arma el *Client concreto que usan los
// consumidores que necesitan el tipo concreto en vez del Judger interface
// (digest y captura pasiva, ver internal/hooks/digest.go y
// internal/hooks/pre_compact_capture.go) — elige backend según
// cfg.LLM.Provider igual que NewFromConfig, pero sin las ramas
// openai/anthropic/disabled que no tiene sentido usar para estas dos
// features (mandan texto de la conversación a un LLM: por default se
// quedan locales — ver el "ollama" default de abajo).
func NewGenerationClientFromConfig(ctx context.Context, cfg config.Config) *Client {
	provider := cfg.LLM.Provider
	if provider == "" {
		provider = "ollama"
	}
	if provider == "claude-cli" {
		return NewClaudeCLIFromConfig(ctx, cfg)
	}
	return NewOllamaFromConfig(ctx, cfg)
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
	c.maxLoadPerCPU = cfg.LLM.MaxLoadPerCPU
	if dataDir, err := platform.DataDir(); err == nil {
		failures := cfg.LLM.BreakerFailures
		minutes := cfg.LLM.BreakerMinutes
		var openFor time.Duration
		if minutes > 0 {
			openFor = time.Duration(minutes) * time.Minute
		}
		c.SetBreaker(NewBreaker(DefaultBreakerPath(dataDir), failures, openFor))
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := c.Ping(pingCtx); err != nil {
		return nil
	}
	return c
}
