package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/jjgarcia-app/kronos-v2/internal/platform"
)

// hookEntry matches Claude Code's settings.json hooks format.
type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// hookMatcher is one element of the per-event array in settings.json.
type hookMatcher struct {
	Hooks []hookEntry `json:"hooks"`
}

// kronosHooks are the hooks we inject into Claude Code settings.json.
var kronosHooks = map[string][]hookMatcher{
	"SessionStart": {
		{Hooks: []hookEntry{{Type: "command", Command: "kronos hook session-start"}}},
	},
	"UserPromptSubmit": {
		{Hooks: []hookEntry{{Type: "command", Command: "kronos hook prompt-submit"}}},
	},
	"SubagentStop": {
		{Hooks: []hookEntry{{Type: "command", Command: "kronos hook subagent-stop"}}},
	},
	"Stop": {
		{Hooks: []hookEntry{{Type: "command", Command: "kronos hook session-stop"}}},
	},
}

// InstallClaudeCode merges Kronos hooks and MCP server into ~/.claude/settings.json.
// Creates the file if it does not exist. Idempotent.
func InstallClaudeCode() error {
	claudeDir, err := platform.ClaudeDir()
	if err != nil {
		return err
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")

	settings, err := loadSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}

	hooks := getOrInitHooks(settings)
	hooksChanged := mergeHooks(hooks)
	settings["hooks"] = hooks

	mcpChanged := mergeMCPServer(settings)

	if !hooksChanged && !mcpChanged {
		fmt.Println("Kronos ya está configurado en Claude Code — sin cambios.")
		return nil
	}

	if err := saveSettings(settingsPath, settings); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}

	fmt.Printf("Kronos configurado en %s\n", settingsPath)
	if hooksChanged {
		fmt.Println("  hooks: SessionStart, UserPromptSubmit, SubagentStop, Stop")
	}
	if mcpChanged {
		fmt.Println("  MCP server: kronos serve (stdio)")
	}
	return nil
}

// mergeMCPServer adds Kronos to settings["mcpServers"] if not already present.
// Returns true if a change was made.
func mergeMCPServer(settings map[string]any) bool {
	servers, ok := settings["mcpServers"].(map[string]any)
	if !ok {
		servers = map[string]any{}
	}
	if _, exists := servers["kronos"]; exists {
		return false
	}
	servers["kronos"] = map[string]any{
		"command": kronosBin(),
		"args":    []string{"serve"},
	}
	settings["mcpServers"] = servers
	return true
}

func kronosBin() string {
	if runtime.GOOS == "windows" {
		return "kronos.exe"
	}
	return "kronos"
}

// Uninstall removes Kronos hooks from ~/.claude/settings.json.
func Uninstall() error {
	claudeDir, err := platform.ClaudeDir()
	if err != nil {
		return err
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")
	settings, err := loadSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}

	hooks := getOrInitHooks(settings)
	removeKronosHooks(hooks)
	settings["hooks"] = hooks

	if err := saveSettings(settingsPath, settings); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}

	fmt.Println("Kronos hooks eliminados.")
	return nil
}

// --- helpers ---

func loadSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
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

func saveSettings(path string, settings map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func getOrInitHooks(settings map[string]any) map[string]any {
	if h, ok := settings["hooks"].(map[string]any); ok {
		return h
	}
	return map[string]any{}
}

// mergeHooks adds Kronos hooks that are not already present.
// Returns true if any change was made.
func mergeHooks(hooks map[string]any) bool {
	changed := false
	for event, matchers := range kronosHooks {
		cmd := matchers[0].Hooks[0].Command
		if !hasKronosCommand(hooks[event], cmd) {
			existing := toMatcherSlice(hooks[event])
			existing = append(existing, matchers...)
			hooks[event] = existing
			changed = true
		}
	}
	return changed
}

func removeKronosHooks(hooks map[string]any) {
	for event, matchers := range kronosHooks {
		cmd := matchers[0].Hooks[0].Command
		hooks[event] = filterKronosCommand(hooks[event], cmd)
	}
}

// hasKronosCommand checks if the event already has a hook with the given command.
func hasKronosCommand(raw any, cmd string) bool {
	for _, m := range toMatcherSlice(raw) {
		for _, h := range m.Hooks {
			if h.Command == cmd {
				return true
			}
		}
	}
	return false
}

func filterKronosCommand(raw any, cmd string) []hookMatcher {
	var out []hookMatcher
	for _, m := range toMatcherSlice(raw) {
		var kept []hookEntry
		for _, h := range m.Hooks {
			if h.Command != cmd {
				kept = append(kept, h)
			}
		}
		if len(kept) > 0 {
			out = append(out, hookMatcher{Hooks: kept})
		}
	}
	return out
}

// toMatcherSlice normalizes the raw hooks value (which may be []any from JSON parsing)
// into []hookMatcher so we can work with it uniformly.
func toMatcherSlice(raw any) []hookMatcher {
	if raw == nil {
		return nil
	}
	// If already a []hookMatcher (from our own writes), return directly.
	if ms, ok := raw.([]hookMatcher); ok {
		return ms
	}
	// If it came from JSON deserialization, it will be []any with map[string]any inside.
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []hookMatcher
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		hooksRaw, _ := m["hooks"].([]any)
		var entries []hookEntry
		for _, he := range hooksRaw {
			hem, ok := he.(map[string]any)
			if !ok {
				continue
			}
			entries = append(entries, hookEntry{
				Type:    strVal(hem, "type"),
				Command: strVal(hem, "command"),
			})
		}
		out = append(out, hookMatcher{Hooks: entries})
	}
	return out
}

func strVal(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
