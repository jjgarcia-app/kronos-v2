package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ── OpenAI-compatible client ──────────────────────────────────────────────────

// OpenAIClient implements Judger using the OpenAI Chat Completions API.
// Also works with any OpenAI-compatible endpoint (LocalAI, LM Studio, etc.).
type OpenAIClient struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewOpenAIClient creates an OpenAI-compatible client.
// baseURL should be e.g. "https://api.openai.com" (no trailing slash).
func NewOpenAIClient(baseURL, apiKey, model string) *OpenAIClient {
	return &OpenAIClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: 45 * time.Second},
	}
}

func (c *OpenAIClient) JudgeRelation(ctx context.Context, aTitle, aContent, bTitle, bContent string, similarity float32) (*JudgeResult, error) {
	prompt := buildJudgePrompt(aTitle, aContent, bTitle, bContent, similarity)

	payload, err := json.Marshal(map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"response_format": map[string]string{"type": "json_object"},
		"temperature":     0.1,
		"max_tokens":      200,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai unavailable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("openai status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse openai response: %w", err)
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("openai returned empty choices")
	}

	return parseJudgeJSON(result.Choices[0].Message.Content)
}

// ── Anthropic client ──────────────────────────────────────────────────────────

// AnthropicClient implements Judger using the Anthropic Messages API.
type AnthropicClient struct {
	apiKey string
	model  string
	http   *http.Client
}

// NewAnthropicClient creates an Anthropic client.
func NewAnthropicClient(apiKey, model string) *AnthropicClient {
	return &AnthropicClient{
		apiKey: apiKey,
		model:  model,
		http:   &http.Client{Timeout: 45 * time.Second},
	}
}

func (c *AnthropicClient) JudgeRelation(ctx context.Context, aTitle, aContent, bTitle, bContent string, similarity float32) (*JudgeResult, error) {
	prompt := buildJudgePrompt(aTitle, aContent, bTitle, bContent, similarity)

	payload, err := json.Marshal(map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens": 300,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic unavailable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("anthropic status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse anthropic response: %w", err)
	}
	if len(result.Content) == 0 {
		return nil, fmt.Errorf("anthropic returned empty content")
	}

	return parseJudgeJSON(result.Content[0].Text)
}

// ── shared helpers ────────────────────────────────────────────────────────────

func parseJudgeJSON(raw string) (*JudgeResult, error) {
	// strip possible markdown fences
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "{"); i > 0 {
		raw = raw[i:]
	}
	if i := strings.LastIndex(raw, "}"); i >= 0 && i < len(raw)-1 {
		raw = raw[:i+1]
	}

	var jr JudgeResult
	if err := json.Unmarshal([]byte(raw), &jr); err != nil {
		return nil, fmt.Errorf("parse judge json: %w", err)
	}

	valid := map[string]bool{
		"conflicts_with": true, "supersedes": true, "related": true,
		"compatible": true, "scoped": true, "not_conflict": true,
	}
	if !valid[jr.Relation] {
		return nil, fmt.Errorf("invalid relation verb: %q", jr.Relation)
	}
	return &jr, nil
}
