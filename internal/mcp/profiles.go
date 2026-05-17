package mcp

import "strings"

// ProfileAgent lists tools that AI agents need during coding sessions.
// These are tools referenced in the memory protocol and skill files.
var ProfileAgent = map[string]bool{
	"mem_save":              true,
	"mem_search":            true,
	"mem_context":           true,
	"mem_session_summary":   true,
	"mem_session_start":     true,
	"mem_session_end":       true,
	"mem_get_observation":   true,
	"mem_update":            true,
	"mem_suggest_topic_key": true,
	"mem_capture_passive":   true,
	"mem_current_project":   true,
	"mem_judge":             true,
	"mem_compare":           true,
	"mem_doctor":            true,
	"mem_checkpoint":        true,
}

// ProfileAdmin lists tools for manual curation, TUI, and dashboards.
// These are not referenced in agent skill files.
var ProfileAdmin = map[string]bool{
	"mem_delete":         true,
	"mem_stats":          true,
	"mem_timeline":       true,
	"mem_merge_projects": true,
	"mem_save_prompt":    true,
}

// Profiles maps profile names to their tool sets.
var Profiles = map[string]map[string]bool{
	"agent": ProfileAgent,
	"admin": ProfileAdmin,
}

// ResolveTools takes a comma-separated string of profile names and/or
// individual tool names and returns the resolved set.
// Empty input or "all" → nil (register every tool).
func ResolveTools(input string) map[string]bool {
	input = strings.TrimSpace(input)
	if input == "" || input == "all" {
		return nil
	}

	result := make(map[string]bool)
	for _, token := range strings.Split(input, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if token == "all" {
			return nil
		}
		if profile, ok := Profiles[token]; ok {
			for tool := range profile {
				result[tool] = true
			}
		} else {
			result[token] = true
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
