package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/platform"
)

// hookEntry matches Claude Code's settings.json hooks format.
type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
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
	legacyRemoved := removeLegacyNodeHooks(hooks)
	hooksChanged := mergeHooks(hooks) || legacyRemoved
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

// mergeMCPServer adds or updates the Kronos MCP server in settings["mcpServers"].
// Updates if the existing entry still points to the old Node.js binary.
// Returns true if a change was made.
func mergeMCPServer(settings map[string]any) bool {
	servers, ok := settings["mcpServers"].(map[string]any)
	if !ok {
		servers = map[string]any{}
	}
	if existing, exists := servers["kronos"]; exists {
		if !isLegacyNodeServer(existing) {
			return false
		}
		// fall through to overwrite the legacy entry
	}
	servers["kronos"] = map[string]any{
		"command": kronosBin(),
		"args":    []string{"serve"},
	}
	settings["mcpServers"] = servers
	return true
}

// isLegacyNodeServer returns true when the MCP entry is the old Node.js Kronos v1 config.
func isLegacyNodeServer(entry any) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	cmd, _ := m["command"].(string)
	if cmd == "node" {
		return true
	}
	// also check args for .js files, just in case
	if args, ok := m["args"].([]any); ok {
		for _, a := range args {
			if s, ok := a.(string); ok && len(s) > 3 && s[len(s)-3:] == ".js" {
				return true
			}
		}
	}
	return false
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

// removeLegacyNodeHooks removes any hook entry whose command invokes a kronos-*.js
// file via Node.js (left over from Kronos v1). Returns true if anything was removed.
func removeLegacyNodeHooks(hooks map[string]any) bool {
	changed := false
	for event, raw := range hooks {
		filtered := filterLegacyNode(raw)
		if len(toMatcherSlice(filtered)) != len(toMatcherSlice(raw)) {
			hooks[event] = filtered
			changed = true
		}
	}
	return changed
}

func filterLegacyNode(raw any) []hookMatcher {
	var out []hookMatcher
	for _, m := range toMatcherSlice(raw) {
		var kept []hookEntry
		for _, h := range m.Hooks {
			if isLegacyNodeHook(h.Command) {
				continue
			}
			kept = append(kept, h)
		}
		if len(kept) > 0 {
			out = append(out, hookMatcher{Hooks: kept})
		}
	}
	return out
}

func isLegacyNodeHook(cmd string) bool {
	return strings.HasPrefix(cmd, "node ") && strings.Contains(cmd, "kronos") && strings.HasSuffix(cmd, ".js")
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
			e := hookEntry{
				Type:    strVal(hem, "type"),
				Command: strVal(hem, "command"),
				Prompt:  strVal(hem, "prompt"),
			}
			// skip broken agent hooks with no prompt
			if e.Type == "agent" && e.Prompt == "" {
				continue
			}
			// skip command hooks with no command
			if e.Type == "command" && e.Command == "" {
				continue
			}
			entries = append(entries, e)
		}
		if len(entries) == 0 {
			continue
		}
		out = append(out, hookMatcher{Hooks: entries})
	}
	return out
}

func strVal(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
