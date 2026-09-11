package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// errBreakerOpen is returned by the three generate-calling methods
// (JudgeRelation, ExtractFinding, UpdateDigest) when the circuit breaker is
// open — callers already treat any non-nil error from these methods as
// "skip gracefully", so this doesn't need special handling beyond what
// fail-open call sites already do.
var errBreakerOpen = errors.New("llm breaker abierto — no se intenta la llamada")

// errLoadTooHigh es lo que generate() devuelve cuando el guardián de carga
// (ver loadguard.go) decide no intentar la llamada — mismo contrato
// fail-open que errBreakerOpen: los tres métodos de generación lo propagan
// como un error más, y los call sites ya tratan cualquier error como "no
// hay resultado, seguir sin esto".
var errLoadTooHigh = errors.New("llm: carga de la máquina por encima del umbral configurado — se saltea la llamada")

const (
	DefaultModel   = "llama3.2"
	DefaultBase    = "http://localhost:11434"
	defaultTimeout = 45 * time.Second

	// loadGuardLogThrottle limita el log de "salteando por carga" a una vez
	// cada tanto en vez de una vez por prompt — con la máquina saturada esto
	// se dispararía en casi cada llamada, y no aporta nada verlo repetido.
	loadGuardLogThrottle = 10 * time.Minute
)

// JudgeResult is the structured judgment returned by the LLM.
type JudgeResult struct {
	Relation   string  `json:"relation"`
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
}

// generateBackend hace la llamada de generación real — implementado por
// ollamaBackend (HTTP contra /api/generate) y claudeCLIBackend (subproceso
// `claude -p`, ver claude_cli.go). Client no sabe ni le importa cuál de los
// dos tiene: arma el prompt, respeta el cortacircuitos y el guardián de
// carga, y delega el "conseguime texto crudo" acá.
type generateBackend interface {
	generate(ctx context.Context, prompt string, numPredict int) (string, error)
}

// pinger es un backend que puede verificar su propia disponibilidad antes de
// comprometerse a usarlo (ollamaBackend). claudeCLIBackend no lo implementa
// — no hay un ping barato equivalente para `claude -p`, así que Client.Ping
// es un no-op en ese caso (se considera disponible hasta la primera llamada
// real, que el cortacircuitos protege igual que a Ollama).
type pinger interface {
	ping(ctx context.Context) error
}

// Client es un cliente de generación LLM — históricamente solo Ollama, ahora
// también claude-cli (ver NewClaudeCLIFromConfig) detrás del mismo backend
// intercambiable, para que digest.go / pre_compact_capture.go no tengan que
// conocer la diferencia.
type Client struct {
	model   string
	breaker *Breaker
	backend generateBackend

	// maxLoadPerCPU es el umbral del guardián de carga (llm.max_load_per_cpu,
	// ver loadguard.go) — 0 lo desactiva, que es el default para un Client
	// armado con NewClient/New directamente (tests, usos fuera de
	// NewOllamaFromConfig/NewClaudeCLIFromConfig).
	maxLoadPerCPU float64

	loadGuardMu       sync.Mutex
	loadGuardLoggedAt time.Time
}

// SetBreaker conecta un cortacircuitos al cliente — las tres llamadas de
// generación (JudgeRelation, ExtractFinding, UpdateDigest) lo consultan
// antes de pegarle al backend y le reportan el resultado. nil (default de
// NewClient) deja al cliente sin cortacircuitos, igual que antes.
func (c *Client) SetBreaker(b *Breaker) {
	c.breaker = b
}

// New creates a Client with default settings (localhost:11434, llama3.2).
func New() *Client {
	return NewClient(DefaultBase, DefaultModel)
}

// NewClient creates a Client with explicit base URL and model, using the
// Ollama HTTP backend — comportamiento sin cambios respecto de antes del
// soporte para claude-cli (ver NewClaudeCLIFromConfig para el otro backend).
func NewClient(base, model string) *Client {
	if base == "" {
		base = DefaultBase
	}
	if model == "" {
		model = DefaultModel
	}
	return &Client{
		model: model,
		backend: &ollamaBackend{
			base:  base,
			model: model,
			http:  &http.Client{Timeout: defaultTimeout},
		},
	}
}

// Ping verifica que el backend esté disponible — solo tiene efecto real para
// el backend de Ollama (chequea /api/tags); claude-cli no tiene un
// equivalente barato, así que se considera disponible sin chequeo previo.
func (c *Client) Ping(ctx context.Context) error {
	if p, ok := c.backend.(pinger); ok {
		return p.ping(ctx)
	}
	return nil
}

// localCPUBackend es un backend cuya generación consume CPU DE ESTA máquina
// (Ollama corre el modelo acá) — es el único al que le aplica el guardián de
// carga. claudeCLIBackend no lo implementa a propósito: `claude -p` genera en
// la nube y lo único local es un proceso esperando red, así que saltearlo por
// carga alta no protege ningún CPU y en cambio deja sin captura automática a
// una máquina ocupada (medido: con 8 agentes corriendo el load ronda 6-10, o
// sea que el guardián saltearía siempre y la feature quedaría muerta).
//
// El caso que motivó el guardián es concreto y solo del backend local: un
// intento fallido con Ollama saturado dejaba `llama-server` girando al 212% de
// CPU durante 13 minutos.
type localCPUBackend interface {
	consumesLocalCPU() bool
}

// checkLoadGuard es el guardián de carga (ver loadguard.go) que los métodos de
// generación consultan ANTES de comprometer el cortacircuitos — deliberadamente
// separado de él y chequeado antes de que se registre el defer que llama a
// RecordFailure/RecordSuccess: un salteo por carga alta no es un fallo del
// backend, así que no debe contar como uno ni acercar el cortacircuitos a
// abrirse. Aplica solo a backends que consumen CPU local (ver localCPUBackend).
func (c *Client) checkLoadGuard() error {
	if lb, ok := c.backend.(localCPUBackend); !ok || !lb.consumesLocalCPU() {
		return nil
	}
	if over, load1, cpus := loadOverThreshold(c.maxLoadPerCPU); over {
		c.logLoadSkipOnce(load1, cpus)
		return errLoadTooHigh
	}
	return nil
}

// logLoadSkipOnce loguea en debug que se salteó una llamada por carga alta —
// a lo sumo una vez cada loadGuardLogThrottle, para no llenar los logs de la
// misma línea en cada prompt mientras la máquina sigue saturada.
func (c *Client) logLoadSkipOnce(load1 float64, cpus int) {
	c.loadGuardMu.Lock()
	defer c.loadGuardMu.Unlock()
	now := time.Now()
	if now.Sub(c.loadGuardLoggedAt) < loadGuardLogThrottle {
		return
	}
	c.loadGuardLoggedAt = now
	slog.Debug("llm: carga de la máquina por encima del umbral, salteando llamada de generación",
		"load1", load1, "cpus", cpus, "max_load_per_cpu", c.maxLoadPerCPU)
}

// ollamaBackend implementa generateBackend contra la API HTTP de Ollama
// (/api/generate, format:"json" para forzar salida parseable).
//
// consumesLocalCPU() devuelve true: es el backend que corre el modelo en ESTA
// máquina, y el único al que le aplica el guardián de carga (ver
// localCPUBackend). claudeCLIBackend no implementa este método a propósito.
func (b *ollamaBackend) consumesLocalCPU() bool { return true }

type ollamaBackend struct {
	base  string
	model string
	http  *http.Client
}

func (b *ollamaBackend) ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("ollama unreachable: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama /api/tags returned %d", resp.StatusCode)
	}
	return nil
}

func (b *ollamaBackend) generate(ctx context.Context, prompt string, numPredict int) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"model":  b.model,
		"prompt": prompt,
		"stream": false,
		"format": "json",
		"options": map[string]any{
			"temperature": 0.1,
			"num_predict": numPredict,
		},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base+"/api/generate", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama unavailable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read ollama response: %w", err)
	}

	var outer struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return "", fmt.Errorf("parse ollama wrapper: %w", err)
	}
	return outer.Response, nil
}

// JudgeRelation asks the LLM to classify the semantic relationship between
// two observations that have already been screened by cosine similarity.
// Returns nil (no error) when Ollama is unavailable — callers should fall back gracefully.
func (c *Client) JudgeRelation(ctx context.Context, aTitle, aContent, bTitle, bContent string, similarity float32) (result *JudgeResult, err error) {
	if c.breaker != nil && !c.breaker.Allow() {
		return nil, errBreakerOpen
	}
	if err := c.checkLoadGuard(); err != nil {
		return nil, err
	}
	if c.breaker != nil {
		defer func() {
			if err != nil {
				c.breaker.RecordFailure(err)
			} else {
				c.breaker.RecordSuccess()
			}
		}()
	}

	prompt := buildJudgePrompt(aTitle, aContent, bTitle, bContent, similarity)

	raw, err := c.backend.generate(ctx, prompt, 200)
	if err != nil {
		return nil, err
	}

	var jr JudgeResult
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &jr); err != nil {
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
func (c *Client) ExtractFinding(ctx context.Context, excerpt string) (finding *Finding, err error) {
	if c.breaker != nil && !c.breaker.Allow() {
		return nil, errBreakerOpen
	}
	if err := c.checkLoadGuard(); err != nil {
		return nil, err
	}
	if c.breaker != nil {
		defer func() {
			if err != nil {
				c.breaker.RecordFailure(err)
			} else {
				c.breaker.RecordSuccess()
			}
		}()
	}

	prompt := buildExtractPrompt(excerpt)

	raw, err := c.backend.generate(ctx, prompt, 400)
	if err != nil {
		return nil, err
	}

	var f Finding
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &f); err != nil {
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
func (c *Client) UpdateDigest(ctx context.Context, previousDigest, excerpt string) (update *DigestUpdate, err error) {
	if c.breaker != nil && !c.breaker.Allow() {
		return nil, errBreakerOpen
	}
	if err := c.checkLoadGuard(); err != nil {
		return nil, err
	}
	if c.breaker != nil {
		defer func() {
			if err != nil {
				c.breaker.RecordFailure(err)
			} else {
				c.breaker.RecordSuccess()
			}
		}()
	}

	prompt := buildDigestPrompt(previousDigest, excerpt)

	raw, err := c.backend.generate(ctx, prompt, 600)
	if err != nil {
		return nil, err
	}

	var d DigestUpdate
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &d); err != nil {
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

// extractJSONObject recorta texto alrededor del primer '{' y el último '}' —
// Ollama con format:"json" ya devuelve JSON exacto, pero claude-cli con
// --output-format text puede envolver la respuesta en fences de markdown o
// alguna frase pese a que el prompt pide "solo JSON"; esto la deja parseable
// en ambos casos sin bifurcar el código de los tres métodos de generación.
func extractJSONObject(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "{"); i > 0 {
		raw = raw[i:]
	}
	if i := strings.LastIndex(raw, "}"); i >= 0 && i < len(raw)-1 {
		raw = raw[:i+1]
	}
	return raw
}
