// Package transcript reads a Claude Code session transcript (.jsonl, one
// JSON event per line) and extracts a bounded, plain-text excerpt of the
// recent conversation — just the user/assistant text turns, none of the
// tool_use/tool_result/thinking payloads that make up most of a real
// transcript's bytes and would blow a small local LLM's context budget.
package transcript

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
)

type transcriptLine struct {
	Type    string `json:"type"`
	Message *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// rawWindowMultiplier: how many raw file bytes to read per desired excerpt
// char. Most transcript bytes are tool_use/tool_result JSON we discard, so
// reading a much wider raw window than the target excerpt size is what
// keeps the tail from being all-noise for tool-call-heavy stretches.
const rawWindowMultiplier = 12

// TailExcerpt reads the tail of a transcript file and returns up to
// maxChars of plain-text "Role: text" turns, most recent last. Returns ""
// (no error) if the file can't be read or has no extractable text near the
// tail — callers should treat that as "nothing to judge", not a failure.
func TailExcerpt(path string, maxChars int) (string, error) {
	if path == "" || maxChars <= 0 {
		return "", nil
	}

	f, err := os.Open(path)
	if err != nil {
		return "", nil // missing/unreadable transcript — not fatal, just nothing to extract
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", nil
	}

	rawWindow := int64(maxChars * rawWindowMultiplier)
	start := int64(0)
	if info.Size() > rawWindow {
		start = info.Size() - rawWindow
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", nil
	}

	r := bufio.NewReader(f)
	if start > 0 {
		// el seek probablemente cayó a mitad de una línea — descartarla,
		// json.Unmarshal fallaría igual pero mejor no arrancar con basura.
		_, _ = r.ReadString('\n')
	}

	var turns []string
	for {
		line, readErr := r.ReadString('\n')
		if text := extractTurnText(line); text != "" {
			turns = append(turns, text)
		}
		if readErr != nil {
			break
		}
	}

	excerpt := strings.Join(turns, "\n")
	if len(excerpt) > maxChars {
		excerpt = excerpt[len(excerpt)-maxChars:]
	}
	return excerpt, nil
}

// extractTurnText decodes one transcript line and returns "Role: text" if
// it's a user/assistant message with at least one text content block, or
// "" otherwise (tool calls, thinking blocks, non-message events, junk).
func extractTurnText(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	var tl transcriptLine
	if err := json.Unmarshal([]byte(line), &tl); err != nil {
		return ""
	}
	if tl.Type != "user" && tl.Type != "assistant" {
		return ""
	}
	if tl.Message == nil || len(tl.Message.Content) == 0 {
		return ""
	}

	var text string
	// content puede ser un string plano o un array de content blocks.
	var plain string
	if err := json.Unmarshal(tl.Message.Content, &plain); err == nil {
		text = plain
	} else {
		var blocks []contentBlock
		if err := json.Unmarshal(tl.Message.Content, &blocks); err == nil {
			var sb strings.Builder
			for _, b := range blocks {
				if b.Type == "text" && b.Text != "" {
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(b.Text)
				}
			}
			text = sb.String()
		}
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	role := tl.Message.Role
	if role == "" {
		role = tl.Type
	}
	return role + ": " + text
}
