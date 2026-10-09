package setup_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/setup"
)

// sandboxCodexEnv aísla HOME/CODEX_HOME y vacía PATH para que
// InstallCodex/UninstallCodex nunca invoquen el binario `codex` real del
// sistema durante los tests (coherente con el resto del repo, donde ningún
// test de setup debe tocar configuración real del usuario). El fallo al
// invocar `codex mcp add` es no-fatal (ver registerCodexMCP), así que
// InstallCodex sigue completando el merge de hooks.json igual.
func sandboxCodexEnv(t *testing.T, tmpHome string) {
	t.Helper()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("PATH", "")
}

func TestInstallCodex_CreatesHooksJSON(t *testing.T) {
	tmpHome := t.TempDir()
	sandboxCodexEnv(t, tmpHome)

	if err := setup.InstallCodex(); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}

	hooksPath := filepath.Join(tmpHome, ".codex", "hooks.json")
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("hooks.json not created: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	hooks, ok := doc["hooks"].(map[string]any)
	if !ok {
		t.Fatal("missing 'hooks' key in hooks.json")
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "SubagentStop", "Stop", "SessionEnd", "PreToolUse", "PreCompact", "PostToolUse"} {
		if hooks[event] == nil {
			t.Errorf("missing hook event: %s", event)
		}
	}
}

func TestInstallCodex_RespectsCodexHomeEnv(t *testing.T) {
	tmpHome := t.TempDir()
	customCodexHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("CODEX_HOME", customCodexHome)
	t.Setenv("PATH", "")

	if err := setup.InstallCodex(); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}

	if _, err := os.Stat(filepath.Join(customCodexHome, "hooks.json")); err != nil {
		t.Fatalf("hooks.json not created under CODEX_HOME: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpHome, ".codex", "hooks.json")); err == nil {
		t.Fatal("hooks.json should NOT be written under ~/.codex when CODEX_HOME is set")
	}
}

func TestInstallCodex_Idempotent(t *testing.T) {
	tmpHome := t.TempDir()
	sandboxCodexEnv(t, tmpHome)

	if err := setup.InstallCodex(); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := setup.InstallCodex(); err != nil {
		t.Fatalf("second install: %v", err)
	}

	hooksPath := filepath.Join(tmpHome, ".codex", "hooks.json")
	data, _ := os.ReadFile(hooksPath)

	count := strings.Count(string(data), "hook session-start")
	if count != 1 {
		t.Errorf("expected 1 occurrence of session-start command, got %d", count)
	}
}

func TestInstallCodex_PreservesExistingHooks(t *testing.T) {
	tmpHome := t.TempDir()
	sandboxCodexEnv(t, tmpHome)

	codexDir := filepath.Join(tmpHome, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		t.Fatal(err)
	}
	existing := `{
		"description": "Optional lifecycle hooks for this workspace.",
		"hooks": {
			"PreToolUse": [
				{"matcher": "Bash", "hooks": [{"type": "command", "command": "python3 ~/.codex/hooks/policy.py"}]}
			]
		}
	}`
	if err := os.WriteFile(filepath.Join(codexDir, "hooks.json"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	if err := setup.InstallCodex(); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(codexDir, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "policy.py") {
		t.Error("existing non-kronos hook was lost during merge")
	}
	if !strings.Contains(string(data), "hook pre-tool-use") {
		t.Error("kronos PreToolUse hook was not merged alongside existing one")
	}
}

func TestUninstallCodex_RemovesKronosHooks(t *testing.T) {
	tmpHome := t.TempDir()
	sandboxCodexEnv(t, tmpHome)

	if err := setup.InstallCodex(); err != nil {
		t.Fatalf("InstallCodex: %v", err)
	}
	if err := setup.UninstallCodex(); err != nil {
		t.Fatalf("UninstallCodex: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpHome, ".codex", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hook session-start") {
		t.Error("kronos hooks still present after uninstall")
	}
}
