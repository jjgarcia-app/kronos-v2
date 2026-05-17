package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
)

func setTempConfigDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", dir)
	} else {
		t.Setenv("XDG_CONFIG_HOME", dir)
		t.Setenv("HOME", dir)
	}
	_ = os.MkdirAll(filepath.Join(dir, "kronos"), 0755)
}

func TestDefault_HasExpectedValues(t *testing.T) {
	cfg := config.Default()
	if cfg.DB.Backend != "sqlite" {
		t.Errorf("default db.backend = %q, want sqlite", cfg.DB.Backend)
	}
	if cfg.Embeddings.Provider != "ollama" {
		t.Errorf("default embeddings.provider = %q, want ollama", cfg.Embeddings.Provider)
	}
	if cfg.Memory.MaxObservationLength != 50000 {
		t.Errorf("default max_observation_length = %d, want 50000", cfg.Memory.MaxObservationLength)
	}
	if !cfg.Secrets.Enabled {
		t.Error("default secrets.enabled should be true")
	}
}

func TestSave_Load_Roundtrip(t *testing.T) {
	setTempConfigDir(t)

	cfg := config.Default()
	cfg.DB.Backend = "postgres"
	cfg.DB.PostgresDSN = "postgres://localhost/test"
	cfg.Memory.MaxSearchResults = 42

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.DB.Backend != "postgres" {
		t.Errorf("loaded db.backend = %q, want postgres", loaded.DB.Backend)
	}
	if loaded.DB.PostgresDSN != "postgres://localhost/test" {
		t.Errorf("loaded postgres_dsn = %q", loaded.DB.PostgresDSN)
	}
	if loaded.Memory.MaxSearchResults != 42 {
		t.Errorf("loaded max_search_results = %d, want 42", loaded.Memory.MaxSearchResults)
	}
}

func TestLoad_NoFile_ReturnsDefaults(t *testing.T) {
	setTempConfigDir(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if cfg.DB.Backend != "sqlite" {
		t.Errorf("expected default backend, got %q", cfg.DB.Backend)
	}
}

func TestSet_ValidFields(t *testing.T) {
	cfg := config.Default()
	cases := []struct{ key, val string }{
		{"db.backend", "postgres"},
		{"db.sqlite_path", "/tmp/test.db"},
		{"db.postgres_dsn", "postgres://localhost/db"},
		{"embeddings.provider", "openai"},
		{"embeddings.ollama_url", "http://localhost:11435"},
		{"memory.max_search_results", "50"},
		{"memory.dedupe_window_minutes", "30"},
		{"nudge.actions_threshold", "5"},
		{"secrets.enabled", "false"},
		{"export.default_output", "/tmp/vault"},
	}
	for _, c := range cases {
		if err := cfg.Set(c.key, c.val); err != nil {
			t.Errorf("Set(%q, %q): %v", c.key, c.val, err)
		}
	}
	if cfg.DB.Backend != "postgres" {
		t.Errorf("db.backend not set: got %q", cfg.DB.Backend)
	}
	if cfg.Memory.MaxSearchResults != 50 {
		t.Errorf("max_search_results not set: got %d", cfg.Memory.MaxSearchResults)
	}
	if cfg.Secrets.Enabled {
		t.Error("secrets.enabled should be false")
	}
}

func TestSet_InvalidKey_ReturnsError(t *testing.T) {
	cfg := config.Default()
	cases := []string{
		"invalid",
		"db.nonexistent",
		"unknown.field",
		"memory.not_a_field",
	}
	for _, key := range cases {
		if err := cfg.Set(key, "value"); err == nil {
			t.Errorf("Set(%q) expected error, got nil", key)
		}
	}
}

func TestConfigPath_ReturnsPath(t *testing.T) {
	path, err := config.ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if path == "" {
		t.Error("ConfigPath returned empty string")
	}
}
