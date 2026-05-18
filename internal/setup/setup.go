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

// kronosToolPermissions are the MCP tool names that should be auto-allowed
// in Claude Code so agents never get prompted for confirmation.
var kronosToolPermissions = []string{
	"mcp__kronos__mem_save",
	"mcp__kronos__mem_search",
	"mcp__kronos__mem_context",
	"mcp__kronos__mem_get_observation",
	"mcp__kronos__mem_update",
	"mcp__kronos__mem_delete",
	"mcp__kronos__mem_session_start",
	"mcp__kronos__mem_session_end",
	"mcp__kronos__mem_session_summary",
	"mcp__kronos__mem_checkpoint",
	"mcp__kronos__mem_save_prompt",
	"mcp__kronos__mem_judge",
	"mcp__kronos__mem_compare",
	"mcp__kronos__mem_suggest_topic_key",
	"mcp__kronos__mem_timeline",
	"mcp__kronos__mem_stats",
	"mcp__kronos__mem_current_project",
	"mcp__kronos__mem_capture_passive",
	"mcp__kronos__mem_merge_projects",
	"mcp__kronos__mem_doctor",
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
	normalized := normalizeKronosHooks(hooks)
	hooksChanged := mergeHooks(hooks) || legacyRemoved || normalized
	settings["hooks"] = hooks

	mcpChanged := mergeMCPServer(settings)
	userMCPChanged, _ := mergeUserMCPFile()
	permsChanged := mergePermissions(settings)

	if !hooksChanged && !mcpChanged && !userMCPChanged && !permsChanged {
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
	if mcpChanged || userMCPChanged {
		fmt.Println("  MCP server: kronos serve (stdio)")
	}
	if permsChanged {
		fmt.Println("  permisos: tools mem_* auto-permitidos")
	}
	return nil
}

// mergeUserMCPFile updates mcpServers.kronos in ~/.claude.json, which is the
// main Claude Code config file. MCPs live under the "mcpServers" key.
func mergeUserMCPFile() (bool, error) {
	mcpPath, err := platform.ClaudeMCPFile()
	if err != nil {
		return false, err
	}

	// Load entire ~/.claude.json preserving all existing keys
	root := map[string]any{}
	if data, err := os.ReadFile(mcpPath); err == nil {
		_ = json.Unmarshal(data, &root)
	}

	// Navigate to mcpServers map
	mcpServers, _ := root["mcpServers"].(map[string]any)
	if mcpServers == nil {
		mcpServers = map[string]any{}
	}

	desired := map[string]any{
		"command": kronosBin(),
		"args":    []string{"serve"},
		"type":    "stdio",
	}

	// Skip if already correct
	if existing, ok := mcpServers["kronos"].(map[string]any); ok {
		if cmd, _ := existing["command"].(string); cmd == kronosBin() {
			return false, nil
		}
	}

	mcpServers["kronos"] = desired
	root["mcpServers"] = mcpServers

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(mcpPath, append(out, '\n'), 0644)
}

// mergePermissions adds Kronos tool names to permissions.allow so Claude Code
// never prompts for confirmation when an agent uses any mem_* tool.
// Returns true if any permission was added.
func mergePermissions(settings map[string]any) bool {
	perms, _ := settings["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}

	allowRaw, _ := perms["allow"].([]any)
	existing := make(map[string]bool, len(allowRaw))
	for _, v := range allowRaw {
		if s, ok := v.(string); ok {
			existing[s] = true
		}
	}

	changed := false
	for _, p := range kronosToolPermissions {
		if !existing[p] {
			allowRaw = append(allowRaw, p)
			changed = true
		}
	}

	if changed {
		perms["allow"] = allowRaw
		settings["permissions"] = perms
	}
	return changed
}

// mergeMCPServer adds or updates the Kronos MCP server in settings["mcpServers"].
// Only skips if the entry already has the canonical binary name (no path prefix).
// Returns true if a change was made.
func mergeMCPServer(settings map[string]any) bool {
	servers, ok := settings["mcpServers"].(map[string]any)
	if !ok {
		servers = map[string]any{}
	}
	if existing, exists := servers["kronos"]; exists {
		if isCurrentGoServer(existing) {
			return false // ya está correcto
		}
		// overwrite: legacy Node, absolute path, or anything else
	}
	servers["kronos"] = map[string]any{
		"command": kronosBin(),
		"args":    []string{"serve"},
		"type":    "stdio",
	}
	settings["mcpServers"] = servers
	return true
}

// isCurrentGoServer returns true only when the entry already uses the canonical
// binary name (e.g. "kronos.exe" with no path prefix).
func isCurrentGoServer(entry any) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	cmd, _ := m["command"].(string)
	return cmd == kronosBin()
}

// kronosBin returns the full path of the currently running kronos binary.
// Using os.Executable() ensures Claude Code can find the binary regardless
// of whether its directory is in PATH.
func kronosBin() string {
	exe, err := os.Executable()
	if err != nil {
		if runtime.GOOS == "windows" {
			return "kronos.exe"
		}
		return "kronos"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
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

// normalizeKronosHooks replaces any existing hook entry that calls kronos via an
// absolute path (e.g. /usr/local/bin/kronos hook ...) with the canonical short
// command (e.g. "kronos hook session-start"). Returns true if anything changed.
func normalizeKronosHooks(hooks map[string]any) bool {
	changed := false
	for event, matchers := range kronosHooks {
		canonicalCmd := matchers[0].Hooks[0].Command
		// suffix is everything after the binary name, e.g. "hook session-start"
		suffix := ""
		if parts := strings.SplitN(canonicalCmd, " ", 2); len(parts) == 2 {
			suffix = parts[1]
		}
		if suffix == "" {
			continue
		}
		existing := toMatcherSlice(hooks[event])
		var rebuilt []hookMatcher
		for _, m := range existing {
			var kept []hookEntry
			for _, h := range m.Hooks {
				// absolute-path variant: ends with " <suffix>" and has a path separator
				if strings.HasSuffix(h.Command, " "+suffix) && strings.ContainsAny(h.Command, "/\\") {
					kept = append(kept, hookEntry{Type: "command", Command: canonicalCmd})
					changed = true
				} else {
					kept = append(kept, h)
				}
			}
			if len(kept) > 0 {
				rebuilt = append(rebuilt, hookMatcher{Hooks: kept})
			}
		}
		if changed {
			hooks[event] = rebuilt
		}
	}
	return changed
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
