package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	DefaultModel   = "llama3.2"
	DefaultBase    = "http://localhost:11434"
	defaultTimeout = 45 * time.Second
)

// JudgeResult is the structured judgment returned by the LLM.
type JudgeResult struct {
	Relation   string  `json:"relation"`
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
}

// Client is a minimal Ollama HTTP client for generative judgment.
type Client struct {
	base  string
	model string
	http  *http.Client
}

// New creates a Client with default settings (localhost:11434, llama3.2).
func New() *Client {
	return NewClient(DefaultBase, DefaultModel)
}

// NewClient creates a Client with explicit base URL and model.
func NewClient(base, model string) *Client {
	if base == "" {
		base = DefaultBase
	}
	if model == "" {
		model = DefaultModel
	}
	return &Client{
		base:  base,
		model: model,
		http:  &http.Client{Timeout: defaultTimeout},
	}
}

// Ping verifies the Ollama server is reachable and the model is available.
// Returns nil on success.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ollama unreachable: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama /api/tags returned %d", resp.StatusCode)
	}
	return nil
}

// JudgeRelation asks the LLM to classify the semantic relationship between
// two observations that have already been screened by cosine similarity.
// Returns nil (no error) when Ollama is unavailable — callers should fall back gracefully.
func (c *Client) JudgeRelation(ctx context.Context, aTitle, aContent, bTitle, bContent string, similarity float32) (*JudgeResult, error) {
	prompt := buildJudgePrompt(aTitle, aContent, bTitle, bContent, similarity)

	payload, err := json.Marshal(map[string]any{
		"model":  c.model,
		"prompt": prompt,
		"stream": false,
		"format": "json",
		"options": map[string]any{
			"temperature": 0.1,
			"num_predict": 200,
		},
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/generate", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama unavailable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read ollama response: %w", err)
	}

	var outer struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil, fmt.Errorf("parse ollama wrapper: %w", err)
	}

	var jr JudgeResult
	if err := json.Unmarshal([]byte(outer.Response), &jr); err != nil {
		return nil, fmt.Errorf("parse llm judgment: %w", err)
	}

	// validate relation verb
	valid := map[string]bool{
		"conflicts_with": true,
		"supersedes":     true,
		"related":        true,
		"compatible":     true,
		"scoped":         true,
		"not_conflict":   true,
	}
	if !valid[jr.Relation] {
		return nil, fmt.Errorf("invalid relation verb from llm: %q", jr.Relation)
	}

	return &jr, nil
}

func buildJudgePrompt(aTitle, aContent, bTitle, bContent string, similarity float32) string {
	return fmt.Sprintf(
		`You are a knowledge conflict analyzer for a persistent memory system.
Two memory observations have %.0f%% semantic similarity and require classification.

Observation A:
Title: %s
Content: %s

Observation B:
Title: %s
Content: %s

Classify their relationship by choosing exactly ONE verb:
- "conflicts_with"  → contradictory or mutually exclusive information
- "supersedes"      → A replaces/updates B with newer or more accurate info
- "related"         → same topic, complementary, should coexist
- "compatible"      → different aspects of a shared domain, no conflict
- "scoped"          → A is a specific instance/subset of B (or vice versa)
- "not_conflict"    → topically unrelated despite surface similarity

Respond ONLY with valid JSON (no markdown, no explanation outside JSON):
{"relation": "<verb>", "reason": "<one concise sentence>", "confidence": <0.0-1.0>}`,
		float64(similarity)*100,
		aTitle, truncate(aContent, 400),
		bTitle, truncate(bContent, 400),
	)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
