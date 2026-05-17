package wizard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/doctor"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/setup"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// ── Palette (Rosé Pine dark) ──────────────────────────────────────────────────

var (
	colPine  = lipgloss.Color("#31748f")
	colGold  = lipgloss.Color("#f6c177")
	colRose  = lipgloss.Color("#ebbcba")
	colText  = lipgloss.Color("#e0def4")
	colMuted = lipgloss.Color("#6e6a86")
	colFoam  = lipgloss.Color("#9ccfd8")
	colIris  = lipgloss.Color("#c4a7e7")
	colLove  = lipgloss.Color("#eb6f92")
)

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	styleTitle     = lipgloss.NewStyle().Bold(true).Foreground(colIris).MarginBottom(1)
	styleBox       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colPine).Padding(0, 2).MarginTop(1)
	stylePhase     = lipgloss.NewStyle().Bold(true).Foreground(colGold)
	styleOK        = lipgloss.NewStyle().Foreground(colFoam)
	styleWarn      = lipgloss.NewStyle().Foreground(colGold)
	styleFail      = lipgloss.NewStyle().Foreground(colLove)
	styleMuted     = lipgloss.NewStyle().Foreground(colMuted)
	styleText      = lipgloss.NewStyle().Foreground(colText)
	styleCursor    = lipgloss.NewStyle().Foreground(colRose).Bold(true)
	styleHelp      = lipgloss.NewStyle().Foreground(colMuted).MarginTop(1)
	styleHighlight = lipgloss.NewStyle().Foreground(colIris).Bold(true)
)

// ── Phase ─────────────────────────────────────────────────────────────────────

type phase int

const (
	phaseWelcome    phase = iota // Banner, Enter para continuar
	phaseBinary                  // Verificar binario en PATH
	phaseConfig                  // Ruta de base de datos
	phaseOllama                  // Detectar Ollama
	phaseOllamaOpts              // Elegir qué hacer con Ollama
	phaseAgents                  // Seleccionar agentes
	phaseSetup                   // Async: instalar todo
	phaseDone                    // Verificación final (doctor)
)

// ── Messages ──────────────────────────────────────────────────────────────────

type binaryCheckedMsg struct {
	path string
	ok   bool
}

type ollamaCheckedMsg struct {
	ok  bool
	url string
}

// setupLineMsg carries a progress line AND the channel, so Update can keep draining.
type setupLineMsg struct {
	line string
	ch   <-chan string
}

type setupFinishedMsg struct{}

type doctorDoneMsg struct {
	report doctor.Report
}

// ── Agents ────────────────────────────────────────────────────────────────────

type agentItem struct {
	id      string
	label   string
	desc    string
	checked bool
}

// ── Model ─────────────────────────────────────────────────────────────────────

type Model struct {
	phase  phase
	width  int
	height int

	binaryPath string
	binaryOK   bool

	dbInput textinput.Model
	cfg     config.Config

	ollamaOK     bool
	ollamaURL    string
	ollamaCursor int
	wantsDocker  bool // if user chose Docker for Ollama

	agents      []agentItem
	agentCursor int

	sp          spinner.Model
	setupLog    []string
	setupDone   bool
	cancelSetup func() // cancels the running setup goroutine

	report doctor.Report

	done []string // summaries of completed phases
	err  error
}

// New returns the initial wizard model.
func New() Model {
	dbPath, _ := platform.DBPath()

	ti := textinput.New()
	ti.Placeholder = dbPath
	ti.SetValue(dbPath)
	ti.CharLimit = 300
	ti.Width = 52

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colFoam)

	cfg, _ := config.Load()
	if cfg.Embeddings.OllamaURL == "" {
		cfg.Embeddings.OllamaURL = "http://localhost:11434"
	}

	return Model{
		phase:     phaseWelcome,
		dbInput:   ti,
		cfg:       cfg,
		sp:        sp,
		ollamaURL: cfg.Embeddings.OllamaURL,
		agents: []agentItem{
			{id: "claude-code", label: "Claude Code", desc: "hooks + MCP en ~/.claude/settings.json", checked: true},
			{id: "cursor", label: "Cursor", desc: "MCP en ~/.cursor/mcp.json"},
			{id: "windsurf", label: "Windsurf", desc: "MCP en ~/.codeium/windsurf/mcp_config.json"},
		},
	}
}

// Run creates the Bubble Tea program and starts the wizard.
func Run() error {
	p := tea.NewProgram(New(), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m Model) Init() tea.Cmd {
	return m.sp.Tick
}

// ── Commands ──────────────────────────────────────────────────────────────────

func cmdCheckBinary() tea.Cmd {
	return func() tea.Msg {
		for _, name := range []string{"kronos", "kronos.exe"} {
			if p, err := exec.LookPath(name); err == nil {
				return binaryCheckedMsg{path: p, ok: true}
			}
		}
		exe, _ := os.Executable()
		// Fallback: check if the binary's own directory is listed in PATH.
		// LookPath can miss it when the session PATH differs from the registry.
		exeDir := filepath.Clean(filepath.Dir(exe))
		for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
			if strings.EqualFold(filepath.Clean(dir), exeDir) {
				return binaryCheckedMsg{path: exe, ok: true}
			}
		}
		return binaryCheckedMsg{path: exe, ok: false}
	}
}

func cmdCheckOllama(url string) tea.Cmd {
	return func() tea.Msg {
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get(url + "/api/tags")
		if err != nil {
			return ollamaCheckedMsg{ok: false, url: url}
		}
		resp.Body.Close()
		return ollamaCheckedMsg{ok: resp.StatusCode < 300, url: url}
	}
}

// drainSetup reads the next item from ch and returns it as a tea.Msg,
// carrying ch so Update can keep draining on each received line.
func drainSetup(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return setupFinishedMsg{}
		}
		return setupLineMsg{line: line, ch: ch}
	}
}

// cmdRunSetup starts a goroutine that installs agents (and optionally Ollama via
// Docker) and returns the first drain command plus a cancel func.
func cmdRunSetup(agents []agentItem, wantsDocker bool, ollamaModel string) (tea.Cmd, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan string, 64)
	go func() {
		defer close(ch)

		ollamaURL := "http://localhost:11434"

		cancelled := func() bool {
			select {
			case <-ctx.Done():
				ch <- "  Instalación cancelada"
				return true
			default:
				return false
			}
		}

		if wantsDocker {
			if cancelled() {
				return
			}
			// Step 1: pull the image so we can stream layer progress.
			ch <- "Descargando imagen ollama/ollama..."
			pullImg := exec.CommandContext(ctx, "docker", "pull", "ollama/ollama")
			if err := streamCmd(pullImg, "  ", ch); err != nil {
				if ctx.Err() != nil {
					ch <- "  Cancelado"
					return
				}
				ch <- fmt.Sprintf("  ! docker pull: %v", err)
				goto agents
			}
			ch <- "  ✓ Imagen descargada"

			if cancelled() {
				return
			}
			// Step 2: start container (detached — fast, no streaming needed).
			ch <- "Iniciando contenedor kronos-ollama..."
			runOut, runErr := exec.Command("docker", "run", "-d",
				"--name", "kronos-ollama",
				"-p", "11434:11434",
				"ollama/ollama").CombinedOutput()
			if runErr != nil {
				msg := strings.TrimSpace(string(runOut))
				if strings.Contains(msg, "already in use") {
					ch <- "  ✓ Contenedor kronos-ollama ya existe"
				} else {
					ch <- fmt.Sprintf("  ! docker run: %s", msg)
					goto agents
				}
			} else {
				ch <- "  ✓ Contenedor iniciado"
			}

			if cancelled() {
				return
			}
			// Step 3: wait for Ollama API to be ready.
			ch <- "Esperando que Ollama inicie..."
			if !waitOllamaReady(ollamaURL, 45*time.Second) {
				ch <- "  ! Ollama no respondió a tiempo — continúa sin modelo"
				goto agents
			}
			ch <- "  ✓ Ollama disponible"
		}

		if cancelled() {
			return
		}
		// Always check and pull the model whenever Ollama is reachable.
		if waitOllamaReady(ollamaURL, 5*time.Second) {
			if isModelInstalled(ollamaURL, ollamaModel) {
				ch <- fmt.Sprintf("  ✓ Modelo %s ya instalado", ollamaModel)
			} else {
				ch <- fmt.Sprintf("Descargando modelo %s...", ollamaModel)
				if err := pullModelViaAPI(ctx, ollamaURL, ollamaModel, ch); err != nil {
					if ctx.Err() == nil {
						ch <- fmt.Sprintf("  ! pull %s: %v", ollamaModel, err)
					}
				} else {
					ch <- fmt.Sprintf("  ✓ Modelo %s listo", ollamaModel)
				}
			}
		}

	agents:
		if cancelled() {
			return
		}
		for _, a := range agents {
			if !a.checked {
				continue
			}
			ch <- fmt.Sprintf("Configurando %s...", a.label)
			var err error
			switch a.id {
			case "claude-code":
				err = setup.InstallClaudeCode()
			case "cursor":
				err = setup.InstallCursor()
			case "windsurf":
				err = setup.InstallWindsurf()
			}
			if err != nil {
				ch <- fmt.Sprintf("  ! %s: %v", a.label, err)
			} else {
				ch <- fmt.Sprintf("  ✓ %s listo", a.label)
			}
		}
	}()
	return drainSetup(ch), cancel
}

func cmdSaveConfig(cfg config.Config) tea.Cmd {
	return func() tea.Msg {
		_ = os.MkdirAll(cfg.DB.SQLitePath[:max(strings.LastIndexAny(cfg.DB.SQLitePath, "/\\"), 0)], 0755)
		_ = cfg.Save()
		st, err := store.New(cfg.DB.SQLitePath)
		if err == nil {
			st.Close()
		}
		return nil
	}
}

func cmdRunDoctor(cfg config.Config) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return doctorDoneMsg{report: doctor.Run(ctx, cfg)}
	}
}

// ── Streaming helpers ─────────────────────────────────────────────────────────

// scanCRLF splits on \n, \r\n, or bare \r — handles docker/ollama progress output.
func scanCRLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i, b := range data {
		if b == '\n' || b == '\r' {
			advance = i + 1
			if b == '\r' && i+1 < len(data) && data[i+1] == '\n' {
				advance = i + 2
			}
			return advance, data[:i], nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// streamCmd runs cmd and sends each non-empty output line (prefixed) to ch.
func streamCmd(cmd *exec.Cmd, prefix string, ch chan<- string) error {
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		sc := bufio.NewScanner(pr)
		sc.Split(scanCRLF)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line != "" {
				ch <- prefix + line
			}
		}
	}()

	runErr := cmd.Run()
	pw.Close()
	<-scanDone
	pr.Close()
	return runErr
}

// waitOllamaReady polls the Ollama API until it responds or timeout elapses.
func waitOllamaReady(url string, timeout time.Duration) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if resp, err := client.Get(url + "/api/tags"); err == nil {
			resp.Body.Close()
			if resp.StatusCode < 300 {
				return true
			}
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// pullModelViaAPI calls POST /api/pull and streams JSON progress to ch.
// Avoids TTY/ANSI issues from running "ollama pull" as a subprocess.
func pullModelViaAPI(ctx context.Context, baseURL, model string, ch chan<- string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/api/pull",
		strings.NewReader(`{"name":"`+model+`","stream":true}`))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	type pullEvent struct {
		Status    string `json:"status"`
		Digest    string `json:"digest"`
		Total     int64  `json:"total"`
		Completed int64  `json:"completed"`
		Error     string `json:"error"`
	}

	var lastPct int64 = -1
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var ev pullEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Error != "" {
			return fmt.Errorf("%s", ev.Error)
		}
		if ev.Total > 0 {
			pct := ev.Completed * 100 / ev.Total
			if pct != lastPct && pct%10 == 0 {
				ch <- fmt.Sprintf("  %d%%  (%d MB)", pct, ev.Total/(1024*1024))
				lastPct = pct
			}
		} else if ev.Status != "" && ev.Digest == "" {
			ch <- "  " + ev.Status
		}
	}
	return sc.Err()
}

// isModelInstalled checks /api/tags to see if the model name appears in Ollama.
func isModelInstalled(baseURL, model string) bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}
	return strings.Contains(string(body), model)
}

// ── Path helpers ──────────────────────────────────────────────────────────────

func pathHint() string {
	switch runtime.GOOS {
	case "windows":
		return "Agrega la carpeta de kronos.exe al PATH del sistema:\n" +
			"  Inicio → Variables de entorno → PATH → Nuevo\n" +
			"  O en PowerShell:\n" +
			`  $p=[Environment]::GetEnvironmentVariable("PATH","User")` + "\n" +
			`  [Environment]::SetEnvironmentVariable("PATH","$p;C:\tu\dir","User")`
	case "darwin":
		return "  sudo mv kronos /usr/local/bin/\n" +
			"  O agrega a ~/.zshrc: export PATH=\"$PATH:/ruta/kronos\""
	default:
		return "  sudo mv kronos /usr/local/bin/\n" +
			"  O agrega a ~/.bashrc: export PATH=\"$PATH:/ruta/kronos\""
	}
}

func ollamaLocalHint() string {
	switch runtime.GOOS {
	case "windows":
		return "Descarga e instala: https://ollama.com/download\nLuego ejecuta: ollama serve"
	case "darwin":
		return "brew install ollama && ollama serve\nO descarga de: https://ollama.com/download"
	default:
		return "curl -fsSL https://ollama.com/install.sh | sh\nLuego: ollama serve &"
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}
