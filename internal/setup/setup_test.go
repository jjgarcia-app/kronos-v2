package setup_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/setup"
)

// installToDir installs Kronos hooks using a temp directory as the Claude dir.
// It monkey-patches the environment so platform.ClaudeDir() returns tempDir.
func withTempClaudeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Override HOME so ClaudeDir() resolves to dir/.claude
	t.Setenv("HOME", filepath.Dir(dir))
	t.Setenv("USERPROFILE", filepath.Dir(dir))
	return dir
}

func TestInstallClaudeCode_CreatesSettings(t *testing.T) {
	tmpHome := t.TempDir()
	claudeDir := filepath.Join(tmpHome, ".claude")

	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	if err := setup.InstallClaudeCode(); err != nil {
		t.Fatalf("InstallClaudeCode: %v", err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	hooks, ok := m["hooks"].(map[string]any)
	if !ok {
		t.Fatal("missing 'hooks' key in settings.json")
	}

	for _, event := range []string{"SessionStart", "UserPromptSubmit", "SubagentStop", "Stop"} {
		if hooks[event] == nil {
			t.Errorf("missing hook event: %s", event)
		}
	}
}

func TestInstallClaudeCode_Idempotent(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	// Install twice.
	if err := setup.InstallClaudeCode(); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := setup.InstallClaudeCode(); err != nil {
		t.Fatalf("second install: %v", err)
	}

	settingsPath := filepath.Join(tmpHome, ".claude", "settings.json")
	data, _ := os.ReadFile(settingsPath)

	// Count occurrences of the kronos command — should be exactly 1 per event.
	// El comando ahora es una ruta absoluta (kronosBin()), no "kronos" pelado
	// — chequeamos el sufijo del subcomando, no el binario exacto.
	count := strings.Count(string(data), "hook session-start")
	if count != 1 {
		t.Errorf("expected 1 occurrence of session-start command, got %d", count)
	}
}

func TestInstallClaudeCode_MergesExistingSettings(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write pre-existing settings with some content.
	existing := map[string]any{
		"theme": "dark",
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{"type": "command", "command": "my-other-hook"},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644)

	if err := setup.InstallClaudeCode(); err != nil {
		t.Fatalf("InstallClaudeCode: %v", err)
	}

	result, _ := os.ReadFile(filepath.Join(claudeDir, "settings.json"))

	// Both the existing hook and the new Kronos hook should be present.
	if !strings.Contains(string(result), "my-other-hook") {
		t.Error("existing hook was removed")
	}
	if !strings.Contains(string(result), "hook session-start") {
		t.Error("kronos hook not added")
	}
}

// TestInstallClaudeCode_NormalizesBareCommandToAbsolutePath reproduce el bug
// de raiz encontrado en produccion: un comando pelado ("kronos hook X")
// resuelve por PATH -- con mas de un kronos.exe instalado (comun: uno en
// go/bin de un `go install`, otro copiado a mano a otro dir del PATH), el
// pelado puede resolver a un binario viejo mientras el MCP server (que si
// usa ruta absoluta) corre el nuevo. Los hooks quedan ejecutando codigo
// desactualizado sin ningun aviso. Correr setup debe normalizar cualquier
// comando pelado preexistente a la ruta absoluta del binario actual.
// TestInstallClaudeCode_HookCommandHasNoBackslashes reproduce un bug real
// encontrado en producción (Windows): los hooks se ejecutan vía shell
// (bash), no como argv directo. Un path de Windows con backslashes
// ("C:\Users\...") se corrompe ahí — bash los interpreta como escapes y los
// borra, dejando el comando irreconocible ("C:UsersJerry..."). El fix
// normaliza siempre a forward slashes, válidos en Windows tanto para exec
// directo como dentro de un comando de shell.
func TestInstallClaudeCode_HookCommandHasNoBackslashes(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	if err := setup.InstallClaudeCode(); err != nil {
		t.Fatalf("InstallClaudeCode: %v", err)
	}

	result, _ := os.ReadFile(filepath.Join(tmpHome, ".claude", "settings.json"))
	if strings.Contains(string(result), `\\`) {
		t.Errorf("comando de hook con backslashes — se corrompe al ejecutarse vía bash:\n%s", result)
	}
}

func TestInstallClaudeCode_NormalizesBareCommandToAbsolutePath(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}

	existing := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{"type": "command", "command": "kronos hook session-start"},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644)

	if err := setup.InstallClaudeCode(); err != nil {
		t.Fatalf("InstallClaudeCode: %v", err)
	}

	result, _ := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	resultStr := string(result)

	if strings.Contains(resultStr, `"command": "kronos hook session-start"`) {
		t.Error("el comando pelado debería haberse normalizado a ruta absoluta, no quedar tal cual")
	}
	if !strings.Contains(resultStr, "hook session-start") {
		t.Error("SessionStart debería seguir apuntando a algún binario")
	}
}

// TestInstallClaudeCode_RemovesLegacyBashGate reproduce la migración real:
// una instalación vieja tenía PreToolUse apuntando al wrapper bash+python
// (kronos-gate.sh) — correr setup debe sacarlo y dejar solo la entrada
// canónica que llama al binario Go directo.
func TestInstallClaudeCode_RemovesLegacyBashGate(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}

	existing := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{"type": "command", "command": "bash $HOME/.claude/scripts/kronos-gate.sh"},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644)

	if err := setup.InstallClaudeCode(); err != nil {
		t.Fatalf("InstallClaudeCode: %v", err)
	}

	result, _ := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	resultStr := string(result)

	if strings.Contains(resultStr, "kronos-gate.sh") {
		t.Error("el wrapper bash viejo debería haberse sacado de PreToolUse")
	}
	if !strings.Contains(resultStr, "hook pre-tool-use") {
		t.Error("PreToolUse debería apuntar al binario Go directo")
	}
}

func TestInstallClaudeCode_AddsPreCompact(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	if err := setup.InstallClaudeCode(); err != nil {
		t.Fatalf("InstallClaudeCode: %v", err)
	}

	result, _ := os.ReadFile(filepath.Join(tmpHome, ".claude", "settings.json"))
	if !strings.Contains(string(result), "hook pre-compact") {
		t.Error("PreCompact no se registró")
	}
}

// TestInstallClaudeCode_AddsPostToolUse_PreservesExisting reproduce el caso
// real de Jerry: PostToolUse ya tenía una entrada de code-review-graph antes
// de instalar kronos — el merge debe agregar la entrada de kronos al lado,
// no pisar la existente.
func TestInstallClaudeCode_AddsPostToolUse_PreservesExisting(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}

	existing := map[string]any{
		"hooks": map[string]any{
			"PostToolUse": []any{
				map[string]any{
					"matcher": "Edit|Write|Bash",
					"hooks": []any{
						map[string]any{"type": "command", "command": "code-review-graph update --skip-flows"},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644)

	if err := setup.InstallClaudeCode(); err != nil {
		t.Fatalf("InstallClaudeCode: %v", err)
	}

	result, _ := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	resultStr := string(result)

	if !strings.Contains(resultStr, "code-review-graph") {
		t.Error("la entrada existente de code-review-graph se perdió en el merge")
	}
	if !strings.Contains(resultStr, "hook post-tool-use") {
		t.Error("PostToolUse no se registró para kronos")
	}
}

// TestInstallClaudeCode_PreservesMatcherField reproduce un bug real
// encontrado en producción: hookMatcher no tenía campo Matcher, así que
// cualquier entrada existente con "matcher": "Edit|Write|Bash" perdía ese
// campo en el primer round-trip por mergeHooks/toMatcherSlice — cambiando
// silenciosamente de "corre solo en estos tools" a "corre en todos".
func TestInstallClaudeCode_PreservesMatcherField(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}

	existing := map[string]any{
		"hooks": map[string]any{
			"PostToolUse": []any{
				map[string]any{
					"matcher": "Edit|Write|Bash",
					"hooks": []any{
						map[string]any{"type": "command", "command": "code-review-graph update --skip-flows"},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644)

	if err := setup.InstallClaudeCode(); err != nil {
		t.Fatalf("InstallClaudeCode: %v", err)
	}

	result, _ := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if !strings.Contains(string(result), `"matcher": "Edit|Write|Bash"`) {
		t.Errorf("el campo matcher se perdió en el merge, settings.json:\n%s", result)
	}
}

func TestUninstall_RemovesHooks(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	// Install first.
	if err := setup.InstallClaudeCode(); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Then uninstall.
	if err := setup.Uninstall(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(tmpHome, ".claude", "settings.json"))
	if strings.Contains(string(data), "kronos hook") {
		t.Errorf("kronos hooks still present after uninstall: %s", data)
	}
}
