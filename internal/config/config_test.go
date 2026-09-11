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
	if !cfg.Core.Enabled {
		t.Error("default core.enabled should be true")
	}
	if cfg.Core.CharsLimit != 2000 {
		t.Errorf("default core.chars_limit = %d, want 2000", cfg.Core.CharsLimit)
	}
	if cfg.Core.MaxItems != 12 {
		t.Errorf("default core.max_items = %d, want 12", cfg.Core.MaxItems)
	}
	if !cfg.Core.IncludeCheckpoint {
		t.Error("default core.include_checkpoint should be true")
	}
	if cfg.Core.MaxGlobalChars != 800 {
		t.Errorf("default core.max_global_chars = %d, want 800", cfg.Core.MaxGlobalChars)
	}
	if cfg.Core.ProjectMinChars != 600 {
		t.Errorf("default core.project_min_chars = %d, want 600", cfg.Core.ProjectMinChars)
	}
	if cfg.Core.MaxPerType != 3 {
		t.Errorf("default core.max_per_type = %d, want 3", cfg.Core.MaxPerType)
	}
	if cfg.Core.MaxItemChars != 110 {
		t.Errorf("default core.max_item_chars = %d, want 110", cfg.Core.MaxItemChars)
	}
	if cfg.Core.StaleDays != 90 {
		t.Errorf("default core.stale_days = %d, want 90", cfg.Core.StaleDays)
	}
	if !cfg.Gate.Enabled {
		t.Error("default gate.enabled should be true")
	}
	if cfg.Gate.Block {
		t.Error("default gate.block should be false")
	}
	if len(cfg.Gate.Tools) != 3 || cfg.Gate.Tools[0] != "Edit" || cfg.Gate.Tools[1] != "Write" || cfg.Gate.Tools[2] != "Bash" {
		t.Errorf("default gate.tools = %v, want [Edit Write Bash]", cfg.Gate.Tools)
	}
	if cfg.Gate.MinObservations != 5 {
		t.Errorf("default gate.min_observations = %d, want 5", cfg.Gate.MinObservations)
	}
}

func TestLoad_PartialGateSection_KeepsDefaults(t *testing.T) {
	setTempConfigDir(t)
	path, err := config.ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"gate":{"block":true}}`), 0644); err != nil {
		t.Fatalf("write partial config: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Gate.Block {
		t.Error("gate.block explícito en true debería respetarse")
	}
	if !cfg.Gate.Enabled {
		t.Error("gate.enabled ausente debería conservar el default true")
	}
	if len(cfg.Gate.Tools) != 3 {
		t.Errorf("gate.tools ausente debería conservar el default, got %v", cfg.Gate.Tools)
	}
	if cfg.Gate.MinObservations != 5 {
		t.Errorf("gate.min_observations ausente debería conservar el default 5, got %d", cfg.Gate.MinObservations)
	}
}

func TestLoad_PartialCoreSection_KeepsDefaults(t *testing.T) {
	setTempConfigDir(t)
	path, err := config.ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"core":{"enabled":false}}`), 0644); err != nil {
		t.Fatalf("write partial config: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Core.Enabled {
		t.Error("core.enabled explícito en false debería respetarse")
	}
	if cfg.Core.CharsLimit != 2000 {
		t.Errorf("core.chars_limit ausente debería conservar el default 2000, got %d", cfg.Core.CharsLimit)
	}
	if cfg.Core.MaxItems != 12 {
		t.Errorf("core.max_items ausente debería conservar el default 12, got %d", cfg.Core.MaxItems)
	}
	if cfg.Core.MaxGlobalChars != 800 {
		t.Errorf("core.max_global_chars ausente debería conservar el default 800, got %d", cfg.Core.MaxGlobalChars)
	}
	if cfg.Core.ProjectMinChars != 600 {
		t.Errorf("core.project_min_chars ausente debería conservar el default 600, got %d", cfg.Core.ProjectMinChars)
	}
	if cfg.Core.MaxPerType != 3 {
		t.Errorf("core.max_per_type ausente debería conservar el default 3, got %d", cfg.Core.MaxPerType)
	}
	if cfg.Core.MaxItemChars != 110 {
		t.Errorf("core.max_item_chars ausente debería conservar el default 110, got %d", cfg.Core.MaxItemChars)
	}
	if cfg.Core.StaleDays != 90 {
		t.Errorf("core.stale_days ausente debería conservar el default 90, got %d", cfg.Core.StaleDays)
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

func TestLoad_NoRelationsSection_KeepsDefaults(t *testing.T) {
	setTempConfigDir(t)
	path, err := config.ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	// Simula un config.json de una versión anterior a relations: la sección
	// no existe en absoluto en el archivo.
	if err := os.WriteFile(path, []byte(`{"db":{"backend":"sqlite"}}`), 0644); err != nil {
		t.Fatalf("write config sin relations: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Relations.BM25Floor != -6.0 {
		t.Errorf("relations.bm25_floor ausente debería conservar el default -6.0, got %v", cfg.Relations.BM25Floor)
	}
	if cfg.Relations.MinSharedTokens != 2 {
		t.Errorf("relations.min_shared_tokens ausente debería conservar el default 2, got %d", cfg.Relations.MinSharedTokens)
	}
	if !cfg.Relations.RequireSameType {
		t.Error("relations.require_same_type ausente debería conservar el default true")
	}
	if cfg.Relations.CandidatesLimit != 3 {
		t.Errorf("relations.candidates_limit ausente debería conservar el default 3, got %d", cfg.Relations.CandidatesLimit)
	}
}

func TestLoad_PartialRelationsSection_KeepsOtherDefaults(t *testing.T) {
	setTempConfigDir(t)
	path, err := config.ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"relations":{"min_shared_tokens":3}}`), 0644); err != nil {
		t.Fatalf("write config parcial: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Relations.MinSharedTokens != 3 {
		t.Errorf("relations.min_shared_tokens explícito debería respetarse, got %d", cfg.Relations.MinSharedTokens)
	}
	if cfg.Relations.BM25Floor != -6.0 {
		t.Errorf("relations.bm25_floor ausente debería conservar el default -6.0, got %v", cfg.Relations.BM25Floor)
	}
	if !cfg.Relations.RequireSameType {
		t.Error("relations.require_same_type ausente debería conservar el default true")
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
		{"export.enabled", "true"},
		{"db.local_only_projects", "proyecto-a, proyecto-b"},
		{"root.api_token", "sekret-token"},
		{"core.enabled", "false"},
		{"core.chars_limit", "1500"},
		{"core.max_items", "8"},
		{"core.include_checkpoint", "false"},
		{"core.max_global_chars", "500"},
		{"core.project_min_chars", "400"},
		{"core.max_per_type", "2"},
		{"core.max_item_chars", "90"},
		{"core.stale_days", "30"},
		{"gate.enabled", "false"},
		{"gate.block", "true"},
		{"gate.tools", "Edit, Bash"},
		{"gate.min_observations", "3"},
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
	if !cfg.Export.Enabled {
		t.Error("export.enabled should be true")
	}
	if len(cfg.DB.LocalOnlyProjects) != 2 || cfg.DB.LocalOnlyProjects[0] != "proyecto-a" || cfg.DB.LocalOnlyProjects[1] != "proyecto-b" {
		t.Errorf("local_only_projects not parsed: got %v", cfg.DB.LocalOnlyProjects)
	}
	if cfg.APIToken != "sekret-token" {
		t.Errorf("api_token not set: got %q", cfg.APIToken)
	}
	if cfg.Core.Enabled {
		t.Error("core.enabled should be false")
	}
	if cfg.Core.CharsLimit != 1500 {
		t.Errorf("core.chars_limit not set: got %d", cfg.Core.CharsLimit)
	}
	if cfg.Core.MaxItems != 8 {
		t.Errorf("core.max_items not set: got %d", cfg.Core.MaxItems)
	}
	if cfg.Core.IncludeCheckpoint {
		t.Error("core.include_checkpoint should be false")
	}
	if cfg.Core.MaxGlobalChars != 500 {
		t.Errorf("core.max_global_chars not set: got %d", cfg.Core.MaxGlobalChars)
	}
	if cfg.Core.ProjectMinChars != 400 {
		t.Errorf("core.project_min_chars not set: got %d", cfg.Core.ProjectMinChars)
	}
	if cfg.Core.MaxPerType != 2 {
		t.Errorf("core.max_per_type not set: got %d", cfg.Core.MaxPerType)
	}
	if cfg.Core.MaxItemChars != 90 {
		t.Errorf("core.max_item_chars not set: got %d", cfg.Core.MaxItemChars)
	}
	if cfg.Core.StaleDays != 30 {
		t.Errorf("core.stale_days not set: got %d", cfg.Core.StaleDays)
	}
	if cfg.Gate.Enabled {
		t.Error("gate.enabled should be false")
	}
	if !cfg.Gate.Block {
		t.Error("gate.block should be true")
	}
	if len(cfg.Gate.Tools) != 2 || cfg.Gate.Tools[0] != "Edit" || cfg.Gate.Tools[1] != "Bash" {
		t.Errorf("gate.tools not parsed: got %v", cfg.Gate.Tools)
	}
	if cfg.Gate.MinObservations != 3 {
		t.Errorf("gate.min_observations not set: got %d", cfg.Gate.MinObservations)
	}
}

func TestSet_InvalidKey_ReturnsError(t *testing.T) {
	cfg := config.Default()
	cases := []string{
		"invalid",
		"db.nonexistent",
		"unknown.field",
		"memory.not_a_field",
		"root.not_a_field",
		"export.not_a_field",
		"gate.not_a_field",
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
