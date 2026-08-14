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

// Finding is the structured result of ExtractFinding — either "nothing here"
// (Found: false) or a save-worthy item ready to hand to SaveObservation.
type Finding struct {
	Found   bool   `json:"found"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// ExtractFinding asks the local LLM whether a transcript excerpt documents
// something worth persisting to memory (a bug fixed with its root cause, an
// architecture/design decision, a non-obvious discovery, a config change) —
// and if so, extracts a title + structured content. Returns (nil, nil) when
// the model finds nothing, same fail-open contract as JudgeRelation: callers
// treat both "Ollama unavailable" and "nothing found" as "skip, don't save".
func (c *Client) ExtractFinding(ctx context.Context, excerpt string) (*Finding, error) {
	prompt := buildExtractPrompt(excerpt)

	payload, err := json.Marshal(map[string]any{
		"model":  c.model,
		"prompt": prompt,
		"stream": false,
		"format": "json",
		"options": map[string]any{
			"temperature": 0.1,
			"num_predict": 400,
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

	var f Finding
	if err := json.Unmarshal([]byte(outer.Response), &f); err != nil {
		return nil, fmt.Errorf("parse llm finding: %w", err)
	}
	if !f.Found {
		return &Finding{Found: false}, nil
	}
	if strings.TrimSpace(f.Title) == "" || strings.TrimSpace(f.Content) == "" {
		return &Finding{Found: false}, nil // el modelo dijo found:true pero no dio contenido real — tratarlo como nada
	}
	return &f, nil
}

// DigestUpdate is the result of UpdateDigest — the running session summary
// after folding in the new excerpt (may be identical to the previous
// version if nothing substantive happened since).
type DigestUpdate struct {
	Content string `json:"content"`
}

// UpdateDigest asks the local LLM to extend a running per-session summary
// with whatever new, concrete work shows up in a fresh transcript excerpt —
// the periodic counterpart to ExtractFinding's one-shot "is this excerpt
// worth saving" judgment. Called on an interval while a session is still
// going (see internal/hooks.MaybeUpdateDigest), not just once at the end,
// so mem_search/mem_context have a running thread of what's been worked on
// without depending on the agent remembering to call mem_save.
//
// Returns (nil, nil) on any failure or empty response — same fail-open
// contract as ExtractFinding/JudgeRelation.
func (c *Client) UpdateDigest(ctx context.Context, previousDigest, excerpt string) (*DigestUpdate, error) {
	prompt := buildDigestPrompt(previousDigest, excerpt)

	payload, err := json.Marshal(map[string]any{
		"model":  c.model,
		"prompt": prompt,
		"stream": false,
		"format": "json",
		"options": map[string]any{
			"temperature": 0.1,
			"num_predict": 600,
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

	var d DigestUpdate
	if err := json.Unmarshal([]byte(outer.Response), &d); err != nil {
		return nil, fmt.Errorf("parse llm digest: %w", err)
	}
	if strings.TrimSpace(d.Content) == "" {
		return nil, nil
	}
	return &d, nil
}

func buildDigestPrompt(previousDigest, excerpt string) string {
	prior := "No hay resumen previo — esta es la primera actualización."
	if strings.TrimSpace(previousDigest) != "" {
		prior = fmt.Sprintf("Resumen previo:\n---\n%s\n---", truncate(previousDigest, 3000))
	}

	return fmt.Sprintf(`You maintain a running summary of an ongoing coding session for a persistent memory system — so that later, anyone (or any agent) querying memory gets a real answer to "what has been worked on here", without re-reading the full transcript.

%s

New transcript excerpt since the last update (oldest first):
---
%s
---

Extend the summary with anything new and concrete from this excerpt: what was investigated, decided, fixed, or built. Keep it dense — short bullet points, no filler, no restating obvious code. Preserve earlier content that's still relevant; drop anything superseded by newer information. If truly nothing new and substantive happened (small talk, routine back-and-forth with no real progress), return the previous summary completely unchanged.

Respond ONLY with valid JSON (no markdown, no explanation outside JSON):
{"content": "<updated running summary, plain text with line breaks, en español>"}`,
		prior, truncate(excerpt, 6000),
	)
}

func buildExtractPrompt(excerpt string) string {
	return fmt.Sprintf(`You are screening a coding-session transcript excerpt for a persistent memory system, right before the conversation context gets compacted (destroyed).

Decide if this excerpt documents something worth remembering permanently:
- a bug that got fixed, together with its root cause
- an architecture, design, or implementation decision that was made
- a non-obvious discovery: a gotcha, an unexpected behavior, a hard-won workaround
- a configuration change and the reason for it

Do NOT flag: small talk, routine unremarkable edits, restating what the code already makes obvious, work that's still in progress with no conclusion yet.

Transcript excerpt (most recent turns, oldest first):
---
%s
---

Respond ONLY with valid JSON (no markdown, no explanation outside JSON).
If nothing qualifies: {"found": false}
If something qualifies: {"found": true, "title": "<short searchable phrase, verb + what>", "content": "<Qué: ...\nPor qué: ...\nCómo aplicar: ...>"}`,
		truncate(excerpt, 6000),
	)
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
