package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/doctor"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// Screen identifies which view is currently active.
type Screen int

const (
	ScreenDashboard Screen = iota
	ScreenSearch
	ScreenSearchResults
	ScreenRecent
	ScreenObservationDetail
	ScreenTimeline
	ScreenSessions
	ScreenSessionDetail
	ScreenDoctor
	ScreenDoctorFix
	ScreenConfig
	ScreenOllama
	ScreenLLM        // nueva
	ScreenExport
	ScreenSetup
)

// --- async messages ---

type statsMsg struct {
	stats *store.Stats
	err   error
}

type searchMsg struct {
	results []*store.SearchResult
	query   string
	err     error
}

type recentMsg struct {
	obs []*store.Observation
	err error
}

type sessionsMsg struct {
	sessions []*store.Session
	err      error
}

type sessionDetailMsg struct {
	obs []*store.Observation
	err error
}

type doctorMsg struct {
	report doctor.Report
}

type doctorFixMsg struct {
	line string
	done bool
	err  error
}

type ollamaModelsMsg struct {
	models []string
	err    error
}

type llmTestMsg struct {
	ok     bool
	detail string
}

type exportDoneMsg struct {
	path  string
	count int
	err   error
}

type setupDoneMsg struct {
	agent string
	err   error
}

type timelineMsg struct {
	obs []*store.Observation
	err error
}

// configField represents an editable config field in the TUI.
type configField struct {
	section string
	key     string
	label   string
	value   string
}

// Model is the root Bubble Tea model.
type Model struct {
	store  *store.Store
	cfg    config.Config
	width  int
	height int

	screen     Screen
	prevScreen Screen
	cursor     int
	scroll     int

	// dashboard
	stats *store.Stats

	// search
	searchInput   textinput.Model
	searchQuery   string
	searchResults []*store.SearchResult

	// recent observations
	recentObs []*store.Observation

	// selected observation (detail + timeline)
	selectedObs *store.Observation
	timelineObs []*store.Observation

	// sessions
	sessions      []*store.Session
	selectedSess  *store.Session
	sessionDetail []*store.Observation

	// doctor
	doctorReport   doctor.Report
	doctorCursor   int
	doctorProgress []string
	doctorRunning  bool

	// config editing
	configFields  []configField
	configEditing bool
	configInput   textinput.Model

	// ollama
	ollamaModels []string

	// llm config editing
	llmFields   []configField
	llmEditing  bool
	llmStatus   string // "" | "testing" | "ok" | "fail: ..."

	// export
	exportOutput string
	exportDone   string

	// setup
	setupAgents []string

	// spinner for async ops
	spinner spinner.Model

	// error message
	errMsg string

	// ollama status (for dashboard status bar)
	ollamaOK bool
}

// New creates a new TUI Model.
func New(st *store.Store, cfg config.Config) Model {
	ti := textinput.New()
	ti.Placeholder = "Buscar..."
	ti.CharLimit = 200
	ti.Width = 40

	ci := textinput.New()
	ci.CharLimit = 500
	ci.Width = 40

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = styleCursor

	m := Model{
		store:        st,
		cfg:          cfg,
		screen:       ScreenDashboard,
		searchInput:  ti,
		configInput:  ci,
		spinner:      sp,
		exportOutput: cfg.Export.DefaultOutput,
		setupAgents:  []string{"Claude Code", "Cursor", "Windsurf"},
	}

	m.configFields = buildConfigFields(cfg)
	m.llmFields = buildLLMFields(cfg)
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.loadStats(),
		m.checkOllama(),
		m.spinner.Tick,
	)
}

// --- commands ---

func (m Model) loadStats() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		st, err := m.store.Stats(ctx)
		return statsMsg{stats: st, err: err}
	}
}

func (m Model) loadRecent() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		obs, err := m.store.ListObservations(ctx, "", 50)
		return recentMsg{obs: obs, err: err}
	}
}

func (m Model) doSearch(query string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		results, err := m.store.Search(ctx, store.SearchParams{
			Query: query,
			Limit: 30,
		})
		return searchMsg{results: results, query: query, err: err}
	}
}

func (m Model) loadSessions() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		sessions, err := m.store.AllSessions(ctx, 50)
		return sessionsMsg{sessions: sessions, err: err}
	}
}

func (m Model) loadSessionDetail(sessionID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		obs, err := m.store.ListSessionObservations(ctx, sessionID)
		return sessionDetailMsg{obs: obs, err: err}
	}
}

func (m Model) runDoctor() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		report := doctor.Run(ctx, m.cfg)
		return doctorMsg{report: report}
	}
}

func (m Model) runDoctorFix(checkName string) tea.Cmd {
	progress := make(chan string, 32)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		err := doctor.Fix(ctx, m.cfg, checkName, progress)
		if err != nil {
			progress <- fmt.Sprintf("Error: %v", err)
		}
	}()

	return waitForFixLine(progress)
}

func waitForFixLine(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return doctorFixMsg{done: true}
		}
		return doctorFixMsg{line: line, done: false, err: nil}
	}
}

func (m Model) checkOllama() tea.Cmd {
	url := m.cfg.Embeddings.OllamaURL
	if url == "" {
		url = "http://localhost:11434"
	}
	return func() tea.Msg {
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get(url + "/api/tags")
		if err != nil {
			return ollamaModelsMsg{err: err}
		}
		defer resp.Body.Close()

		var result struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return ollamaModelsMsg{err: err}
		}
		names := make([]string, 0, len(result.Models))
		for _, m := range result.Models {
			names = append(names, m.Name)
		}
		return ollamaModelsMsg{models: names}
	}
}

func (m Model) testLLM() tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		switch cfg.LLM.Provider {
		case "ollama", "":
			url := cfg.LLM.BaseURL
			if url == "" {
				url = cfg.Embeddings.OllamaURL
			}
			if url == "" {
				url = "http://localhost:11434"
			}
			client := &http.Client{Timeout: 2 * time.Second}
			resp, err := client.Get(url + "/api/tags")
			if err != nil {
				return llmTestMsg{ok: false, detail: err.Error()}
			}
			resp.Body.Close()
			return llmTestMsg{ok: resp.StatusCode == 200, detail: url}
		case "openai", "openai-compatible":
			if cfg.LLM.APIKey == "" {
				return llmTestMsg{ok: false, detail: "API Key no configurada"}
			}
			return llmTestMsg{ok: true, detail: "API Key configurada"}
		case "anthropic":
			if cfg.LLM.APIKey == "" {
				return llmTestMsg{ok: false, detail: "API Key no configurada"}
			}
			return llmTestMsg{ok: true, detail: "API Key configurada"}
		case "disabled":
			return llmTestMsg{ok: false, detail: "LLM deshabilitado"}
		}
		_ = ctx
		return llmTestMsg{ok: false, detail: "provider desconocido"}
	}
}

func (m Model) loadTimeline(obsID int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		obs, err := m.store.TimelineObservations(ctx, obsID, 5)
		return timelineMsg{obs: obs, err: err}
	}
}

func (m Model) doExport() tea.Cmd {
	outDir := m.exportOutput
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		obs, err := m.store.ListAll(ctx, "")
		if err != nil {
			return exportDoneMsg{err: err}
		}
		return exportDoneMsg{path: outDir, count: len(obs)}
	}
}

func (m Model) doSetupAgent(agent string) tea.Cmd {
	return func() tea.Msg {
		switch agent {
		case "Claude Code":
			from_setup := setupInstallClaudeCode()
			return setupDoneMsg{agent: agent, err: from_setup}
		default:
			return setupDoneMsg{agent: agent, err: fmt.Errorf("setup para %s no implementado aún", agent)}
		}
	}
}

// --- helpers ---

func setupInstallClaudeCode() error {
	// import setup package at call site to avoid circular deps
	// We call it via the doctor fix mechanism
	progress := make(chan string, 8)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = doctor.Fix(ctx, config.Config{}, "Hooks Claude Code", progress)
	}()
	// drain
	for range progress {
	}
	return nil
}

func buildConfigFields(cfg config.Config) []configField {
	return []configField{
		{section: "db", key: "backend", label: "DB Backend", value: cfg.DB.Backend},
		{section: "db", key: "sqlite_path", label: "SQLite Path", value: cfg.DB.SQLitePath},
		{section: "embeddings", key: "provider", label: "Embeddings Provider", value: cfg.Embeddings.Provider},
		{section: "embeddings", key: "ollama_url", label: "Ollama URL", value: cfg.Embeddings.OllamaURL},
		{section: "embeddings", key: "ollama_model", label: "Ollama Embed Model", value: cfg.Embeddings.OllamaModel},
		{section: "embeddings", key: "ollama_llm_model", label: "Ollama LLM Model", value: cfg.Embeddings.OllamaLLMModel},
		{section: "memory", key: "max_observation_length", label: "Max Obs Length", value: fmt.Sprintf("%d", cfg.Memory.MaxObservationLength)},
		{section: "memory", key: "max_search_results", label: "Max Search Results", value: fmt.Sprintf("%d", cfg.Memory.MaxSearchResults)},
		{section: "memory", key: "max_context_results", label: "Max Context Results", value: fmt.Sprintf("%d", cfg.Memory.MaxContextResults)},
		{section: "memory", key: "dedupe_window_minutes", label: "Dedupe Window (min)", value: fmt.Sprintf("%d", cfg.Memory.DedupeWindowMinutes)},
		{section: "nudge", key: "actions_threshold", label: "Nudge Actions Threshold", value: fmt.Sprintf("%d", cfg.Nudge.ActionsThreshold)},
		{section: "nudge", key: "fallback_minutes", label: "Nudge Fallback (min)", value: fmt.Sprintf("%d", cfg.Nudge.FallbackMinutes)},
		{section: "secrets", key: "enabled", label: "Secrets Detection", value: fmt.Sprintf("%v", cfg.Secrets.Enabled)},
		{section: "export", key: "default_output", label: "Export Output Dir", value: cfg.Export.DefaultOutput},
	}
}

func buildLLMFields(cfg config.Config) []configField {
	return []configField{
		{section: "llm", key: "provider", label: "Provider", value: cfg.LLM.Provider},
		{section: "llm", key: "model", label: "Model", value: cfg.LLM.Model},
		{section: "llm", key: "api_key", label: "API Key", value: cfg.LLM.APIKey},
		{section: "llm", key: "base_url", label: "Base URL", value: cfg.LLM.BaseURL},
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
