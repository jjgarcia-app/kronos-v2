package wizard

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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

	sp        spinner.Model
	setupLog  []string
	setupDone bool

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
// Docker) and returns the first drain command.
func cmdRunSetup(agents []agentItem, wantsDocker bool, ollamaModel string) tea.Cmd {
	ch := make(chan string, 64)
	go func() {
		defer close(ch)

		if wantsDocker {
			ch <- "Iniciando Ollama en Docker..."
			cmd := exec.Command("docker", "run", "-d", "--name", "kronos-ollama",
				"-p", "11434:11434", "ollama/ollama")
			if out, err := cmd.CombinedOutput(); err != nil {
				ch <- fmt.Sprintf("  ! docker: %s", strings.TrimSpace(string(out)))
			} else {
				ch <- "  ✓ Contenedor kronos-ollama iniciado"
				time.Sleep(3 * time.Second)
				ch <- fmt.Sprintf("Descargando %s...", ollamaModel)
				pull := exec.Command("ollama", "pull", ollamaModel)
				if out, err := pull.CombinedOutput(); err != nil {
					ch <- fmt.Sprintf("  ! pull: %s", strings.TrimSpace(string(out)))
				} else {
					ch <- fmt.Sprintf("  ✓ Modelo %s listo", ollamaModel)
				}
			}
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
	return drainSetup(ch)
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
