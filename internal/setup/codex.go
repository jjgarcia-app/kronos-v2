package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/platform"
)

// InstallCodex registra los hooks de Kronos en hooks.json de Codex CLI y el
// servidor MCP vía `codex mcp add`. Idempotente.
//
// Codex (openai/codex) lee hooks.json con el MISMO shape que Claude Code
// (evento -> []{matcher, hooks:[{type, command}]}), así que reusamos
// kronosHooksMap() y los mismos helpers de merge que InstallClaudeCode —
// la única diferencia es DÓNDE vive el archivo (platform.CodexDir(), que
// respeta $CODEX_HOME para instalaciones multi-cuenta como las de Orca) y
// que el servidor MCP se registra con el comando `codex mcp add`, no
// escribiendo TOML a mano (este repo no trae una librería TOML y
// corromper config.toml a mano es más riesgoso que invocar el propio CLI).
func InstallCodex() error {
	codexDir, err := platform.CodexDir()
	if err != nil {
		return err
	}

	hooksChanged, err := installCodexHooks(codexDir)
	if err != nil {
		return fmt.Errorf("instalar hooks de Codex: %w", err)
	}

	mcpChanged, mcpErr := registerCodexMCP()
	if mcpErr != nil {
		// No hacemos fatal esto: los hooks pueden quedar instalados igual
		// aunque el binario `codex` no esté en PATH en este momento del
		// setup (ej. recién instalado, shell sin recargar PATH todavía).
		fmt.Printf("advertencia: no se pudo registrar el MCP server en Codex: %v\n", mcpErr)
	}

	if !hooksChanged && !mcpChanged {
		fmt.Println("Kronos ya está configurado en Codex — sin cambios.")
		return nil
	}

	fmt.Printf("Kronos configurado en Codex (%s)\n", codexDir)
	if hooksChanged {
		fmt.Println("  hooks: SessionStart, UserPromptSubmit, SubagentStop, Stop, SessionEnd, PreToolUse, PreCompact, PostToolUse")
	}
	if mcpChanged {
		fmt.Println("  MCP server: kronos mcp (proxy stdio → daemon compartido)")
	}
	return nil
}

// installCodexHooks fusiona kronosHooksMap() dentro de <codexDir>/hooks.json,
// preservando cualquier hook de otro origen que ya exista ahí. Mismo formato
// y misma lógica de idempotencia/normalización que InstallClaudeCode usa
// para ~/.claude/settings.json["hooks"] — acá el documento es solo hooks,
// sin el resto de las claves de settings.
func installCodexHooks(codexDir string) (bool, error) {
	hooksPath := filepath.Join(codexDir, "hooks.json")

	doc, err := loadHooksDoc(hooksPath)
	if err != nil {
		return false, fmt.Errorf("load %s: %w", hooksPath, err)
	}

	hooks := getOrInitHooks(doc)
	legacyRemoved := removeLegacyNodeHooks(hooks)
	bashGateRemoved := removeLegacyBashGate(hooks)
	normalized := normalizeKronosHooks(hooks)
	changed := mergeHooks(hooks) || legacyRemoved || bashGateRemoved || normalized
	doc["hooks"] = hooks

	if !changed {
		return false, nil
	}

	if err := saveHooksDoc(hooksPath, doc); err != nil {
		return false, fmt.Errorf("save %s: %w", hooksPath, err)
	}
	return true, nil
}

func loadHooksDoc(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

func saveHooksDoc(path string, doc map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

// registerCodexMCP invoca `codex mcp add kronos -- <bin> mcp`. Devuelve
// false (sin cambios) cuando ya está registrado con el comando/args
// correctos — evita reescribir config.toml en cada corrida de setup.
func registerCodexMCP() (bool, error) {
	if _, err := exec.LookPath("codex"); err != nil {
		return false, fmt.Errorf("binario `codex` no encontrado en PATH: %w", err)
	}

	if alreadyRegistered, err := codexMCPAlreadyCorrect(); err == nil && alreadyRegistered {
		return false, nil
	}

	cmd := exec.Command("codex", "mcp", "add", "kronos", "--", kronosBin(), "mcp")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("codex mcp add: %w (%s)", err, string(out))
	}
	return true, nil
}

// codexMCPAlreadyCorrect corre `codex mcp get kronos` y revisa que apunte al
// binario actual con los args correctos ("mcp"), para no reescribir
// config.toml en cada `kronos setup codex` si ya está bien.
func codexMCPAlreadyCorrect() (bool, error) {
	out, err := exec.Command("codex", "mcp", "get", "kronos").CombinedOutput()
	if err != nil {
		// no registrado todavía, o `codex mcp get` falló — tratamos como
		// "no está correcto" y dejamos que registerCodexMCP reintente.
		return false, nil
	}
	text := string(out)
	return strings.Contains(text, kronosBin()) && strings.Contains(text, " mcp"), nil
}

// UninstallCodex quita los hooks de Kronos de hooks.json y el servidor MCP
// vía `codex mcp remove`.
func UninstallCodex() error {
	codexDir, err := platform.CodexDir()
	if err != nil {
		return err
	}

	hooksPath := filepath.Join(codexDir, "hooks.json")
	doc, err := loadHooksDoc(hooksPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", hooksPath, err)
	}
	hooks := getOrInitHooks(doc)
	removeKronosHooks(hooks)
	doc["hooks"] = hooks
	if err := saveHooksDoc(hooksPath, doc); err != nil {
		return fmt.Errorf("save %s: %w", hooksPath, err)
	}

	if _, err := exec.LookPath("codex"); err == nil {
		_ = exec.Command("codex", "mcp", "remove", "kronos").Run()
	}

	fmt.Println("Kronos eliminado de Codex.")
	return nil
}
