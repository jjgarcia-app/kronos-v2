package llm_test

import (
	"context"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/llm"
)

func TestNewFromConfig_Disabled(t *testing.T) {
	cfg := config.Config{LLM: config.LLMConfig{Provider: "disabled"}}
	j := llm.NewFromConfig(context.Background(), cfg)
	if j != nil {
		t.Errorf("provider=disabled debería devolver nil, got %T", j)
	}
}

func TestNewFromConfig_UnknownProvider(t *testing.T) {
	cfg := config.Config{LLM: config.LLMConfig{Provider: "no-existe"}}
	j := llm.NewFromConfig(context.Background(), cfg)
	if j != nil {
		t.Errorf("provider desconocido debería devolver nil, got %T", j)
	}
}

func TestNewFromConfig_OpenAI_NoAPIKey_ReturnsNil(t *testing.T) {
	cfg := config.Config{LLM: config.LLMConfig{Provider: "openai"}}
	j := llm.NewFromConfig(context.Background(), cfg)
	if j != nil {
		t.Errorf("openai sin api_key debería devolver nil, got %T", j)
	}
}

func TestNewFromConfig_OpenAI_WithAPIKey_ReturnsClient(t *testing.T) {
	cfg := config.Config{LLM: config.LLMConfig{Provider: "openai", APIKey: "sk-test"}}
	j := llm.NewFromConfig(context.Background(), cfg)
	if j == nil {
		t.Fatal("openai con api_key no debería devolver nil")
	}
	if _, ok := j.(*llm.OpenAIClient); !ok {
		t.Errorf("esperaba *llm.OpenAIClient, got %T", j)
	}
}

func TestNewFromConfig_OpenAICompatible_WithAPIKey_ReturnsClient(t *testing.T) {
	cfg := config.Config{LLM: config.LLMConfig{Provider: "openai-compatible", APIKey: "key", BaseURL: "http://localhost:8080"}}
	j := llm.NewFromConfig(context.Background(), cfg)
	if _, ok := j.(*llm.OpenAIClient); !ok {
		t.Errorf("esperaba *llm.OpenAIClient para openai-compatible, got %T", j)
	}
}

func TestNewFromConfig_Anthropic_NoAPIKey_ReturnsNil(t *testing.T) {
	cfg := config.Config{LLM: config.LLMConfig{Provider: "anthropic"}}
	j := llm.NewFromConfig(context.Background(), cfg)
	if j != nil {
		t.Errorf("anthropic sin api_key debería devolver nil, got %T", j)
	}
}

func TestNewFromConfig_Anthropic_WithAPIKey_ReturnsClient(t *testing.T) {
	cfg := config.Config{LLM: config.LLMConfig{Provider: "anthropic", APIKey: "sk-ant-test"}}
	j := llm.NewFromConfig(context.Background(), cfg)
	if j == nil {
		t.Fatal("anthropic con api_key no debería devolver nil")
	}
	if _, ok := j.(*llm.AnthropicClient); !ok {
		t.Errorf("esperaba *llm.AnthropicClient, got %T", j)
	}
}

// TestNewFromConfig_Ollama_Unreachable_ReturnsNilGracefully verifica que si
// Ollama no responde (puerto que rechaza la conexión), NewFromConfig degrada
// a nil en vez de bloquear o panicar — no hace falta un Ollama real para
// probar el camino de fallback, que es el que más importa en producción
// cuando Ollama no está corriendo.
func TestNewFromConfig_Ollama_Unreachable_ReturnsNilGracefully(t *testing.T) {
	cfg := config.Config{LLM: config.LLMConfig{
		Provider: "ollama",
		BaseURL:  "http://127.0.0.1:1", // puerto reservado, conexión rechazada al instante
	}}

	done := make(chan llm.Judger, 1)
	go func() {
		done <- llm.NewFromConfig(context.Background(), cfg)
	}()

	select {
	case j := <-done:
		if j != nil {
			t.Errorf("Ollama inalcanzable debería devolver nil, got %T", j)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("NewFromConfig no debería colgarse cuando Ollama es inalcanzable (timeout de 2s ya incluido)")
	}
}
