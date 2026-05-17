package hooks

import (
	"encoding/json"
	"io"
	"os"
)

// Input is the JSON payload that Claude Code sends to each hook via stdin.
// Fields vary by event; unknown fields are silently ignored.
type Input struct {
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
	// UserPromptSubmit
	Prompt string `json:"prompt"`
	// SubagentStop / Stop — full assistant response text
	Response string `json:"response"`
	// PostToolUse / PreToolUse
	ToolName string `json:"tool_name"`
}

// ReadInput reads and parses the hook input from stdin.
// Returns a zero-value Input if stdin is empty or not valid JSON.
func ReadInput() (Input, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return Input{}, err
	}
	var in Input
	if len(data) > 0 {
		_ = json.Unmarshal(data, &in) // best-effort
	}
	return in, nil
}
