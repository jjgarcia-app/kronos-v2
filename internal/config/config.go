package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/platform"
)

type DBConfig struct {
	Backend        string `json:"backend"`
	SQLitePath     string `json:"sqlite_path"`
	PostgresDSN    string `json:"postgres_dsn"`
	PostgresDocker bool   `json:"postgres_docker"`
}

type EmbeddingsConfig struct {
	Provider        string `json:"provider"`
	OllamaURL       string `json:"ollama_url"`
	OllamaModel     string `json:"ollama_model"`
	OllamaLLMModel  string `json:"ollama_llm_model"`
	OllamaDocker    bool   `json:"ollama_docker"`
	AnthropicKey    string `json:"anthropic_api_key"`
	OpenAIKey       string `json:"openai_api_key"`
}

type MemoryConfig struct {
	MaxObservationLength int `json:"max_observation_length"`
	MaxSearchResults     int `json:"max_search_results"`
	MaxContextResults    int `json:"max_context_results"`
	DedupeWindowMinutes  int `json:"dedupe_window_minutes"`
	RetentionDays        int `json:"retention_days"`
}

type NudgeConfig struct {
	ActionsThreshold int `json:"actions_threshold"`
	FallbackMinutes  int `json:"fallback_minutes"`
}

type SecretsConfig struct {
	Enabled bool `json:"enabled"`
}

type ExportConfig struct {
	DefaultOutput string `json:"default_output"`
}

type LLMConfig struct {
	Provider string `json:"provider"` // ollama | openai | openai-compatible | anthropic | disabled
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
	BaseURL  string `json:"base_url"`
}

type Config struct {
	DB         DBConfig         `json:"db"`
	Embeddings EmbeddingsConfig `json:"embeddings"`
	LLM        LLMConfig        `json:"llm"`
	Memory     MemoryConfig     `json:"memory"`
	Nudge      NudgeConfig      `json:"nudge"`
	Secrets    SecretsConfig    `json:"secrets"`
	Export     ExportConfig     `json:"export"`
	APIToken   string           `json:"api_token"`
}

// Default returns a Config populated with sensible defaults.
func Default() Config {
	return Config{
		DB: DBConfig{
			Backend: "sqlite",
		},
		Embeddings: EmbeddingsConfig{
			Provider:       "ollama",
			OllamaURL:      "http://localhost:11434",
			OllamaModel:    "nomic-embed-text",
			OllamaLLMModel: "llama3.2",
		},
		Memory: MemoryConfig{
			MaxObservationLength: 50000,
			MaxSearchResults:     20,
			MaxContextResults:    10,
			DedupeWindowMinutes:  15,
		},
		Nudge: NudgeConfig{
			ActionsThreshold: 10,
			FallbackMinutes:  20,
		},
		Secrets: SecretsConfig{
			Enabled: true,
		},
		Export: ExportConfig{
			DefaultOutput: "~/kronos-vault",
		},
	}
}

// ConfigPath returns the path to the config file.
func ConfigPath() (string, error) {
	dir, err := platform.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config from disk, applying defaults for missing fields.
func Load() (Config, error) {
	cfg := Default()

	path, err := ConfigPath()
	if err != nil {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}

	// Apply defaults for zero-value fields
	def := Default()
	if cfg.DB.Backend == "" {
		cfg.DB.Backend = def.DB.Backend
	}
	if cfg.Embeddings.Provider == "" {
		cfg.Embeddings.Provider = def.Embeddings.Provider
	}
	if cfg.Embeddings.OllamaURL == "" {
		cfg.Embeddings.OllamaURL = def.Embeddings.OllamaURL
	}
	if cfg.Embeddings.OllamaModel == "" {
		cfg.Embeddings.OllamaModel = def.Embeddings.OllamaModel
	}
	if cfg.Embeddings.OllamaLLMModel == "" {
		cfg.Embeddings.OllamaLLMModel = def.Embeddings.OllamaLLMModel
	}
	if cfg.Memory.MaxObservationLength == 0 {
		cfg.Memory.MaxObservationLength = def.Memory.MaxObservationLength
	}
	if cfg.Memory.MaxSearchResults == 0 {
		cfg.Memory.MaxSearchResults = def.Memory.MaxSearchResults
	}
	if cfg.Memory.MaxContextResults == 0 {
		cfg.Memory.MaxContextResults = def.Memory.MaxContextResults
	}
	if cfg.Memory.DedupeWindowMinutes == 0 {
		cfg.Memory.DedupeWindowMinutes = def.Memory.DedupeWindowMinutes
	}
	if cfg.Nudge.ActionsThreshold == 0 {
		cfg.Nudge.ActionsThreshold = def.Nudge.ActionsThreshold
	}
	if cfg.Nudge.FallbackMinutes == 0 {
		cfg.Nudge.FallbackMinutes = def.Nudge.FallbackMinutes
	}
	if cfg.Export.DefaultOutput == "" {
		cfg.Export.DefaultOutput = def.Export.DefaultOutput
	}

	return cfg, nil
}

// Save writes the config to disk.
func (c Config) Save() error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

// Set updates a config field by dot-notation key (e.g. "db.backend").
func (c *Config) Set(key, value string) error {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid key %q — use format: section.field", key)
	}
	section, field := parts[0], parts[1]

	switch section {
	case "db":
		switch field {
		case "backend":
			c.DB.Backend = value
		case "sqlite_path":
			c.DB.SQLitePath = value
		case "postgres_dsn":
			c.DB.PostgresDSN = value
		case "postgres_docker":
			c.DB.PostgresDocker = parseBool(value)
		default:
			return fmt.Errorf("unknown db field: %s", field)
		}
	case "embeddings":
		switch field {
		case "provider":
			c.Embeddings.Provider = value
		case "ollama_url":
			c.Embeddings.OllamaURL = value
		case "ollama_model":
			c.Embeddings.OllamaModel = value
		case "ollama_llm_model":
			c.Embeddings.OllamaLLMModel = value
		case "ollama_docker":
			c.Embeddings.OllamaDocker = parseBool(value)
		case "anthropic_api_key":
			c.Embeddings.AnthropicKey = value
		case "openai_api_key":
			c.Embeddings.OpenAIKey = value
		default:
			return fmt.Errorf("unknown embeddings field: %s", field)
		}
	case "memory":
		switch field {
		case "max_observation_length":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Memory.MaxObservationLength = n
		case "max_search_results":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Memory.MaxSearchResults = n
		case "max_context_results":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Memory.MaxContextResults = n
		case "dedupe_window_minutes":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Memory.DedupeWindowMinutes = n
		case "retention_days":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Memory.RetentionDays = n
		default:
			return fmt.Errorf("unknown memory field: %s", field)
		}
	case "nudge":
		switch field {
		case "actions_threshold":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Nudge.ActionsThreshold = n
		case "fallback_minutes":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid int: %s", value)
			}
			c.Nudge.FallbackMinutes = n
		default:
			return fmt.Errorf("unknown nudge field: %s", field)
		}
	case "secrets":
		switch field {
		case "enabled":
			c.Secrets.Enabled = parseBool(value)
		default:
			return fmt.Errorf("unknown secrets field: %s", field)
		}
	case "llm":
		switch field {
		case "provider":
			c.LLM.Provider = value
		case "model":
			c.LLM.Model = value
		case "api_key":
			c.LLM.APIKey = value
		case "base_url":
			c.LLM.BaseURL = value
		default:
			return fmt.Errorf("unknown llm field: %s", field)
		}
	case "export":
		switch field {
		case "default_output":
			c.Export.DefaultOutput = value
		default:
			return fmt.Errorf("unknown export field: %s", field)
		}
	default:
		return fmt.Errorf("unknown config section: %s", section)
	}
	return nil
}

func parseBool(s string) bool {
	b, _ := strconv.ParseBool(s)
	return b
}
