package wizard

import (
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.dbInput.Width = minI(m.width-10, 52)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd

	case binaryCheckedMsg:
		m.binaryPath = msg.path
		m.binaryOK = msg.ok
		return m, nil

	case binaryInstalledMsg:
		if msg.err != nil {
			m.binaryInstallErr = msg.err.Error()
		} else {
			m.binaryPath = msg.path
			m.binaryOK = true
		}
		m.binaryInstalling = false
		return m, nil

	case ollamaCheckedMsg:
		m.ollamaOK = msg.ok
		m.ollamaURL = msg.url
		return m, nil

	case setupLineMsg:
		m.setupLog = append(m.setupLog, msg.line)
		return m, drainSetup(msg.ch) // keep draining

	case setupFinishedMsg:
		m.setupDone = true
		m.phase = phaseDone
		return m, cmdRunDoctor(m.cfg)

	case doctorDoneMsg:
		m.report = msg.report
		return m, nil
	}

	// forward to textinput when editing config
	if m.phase == phaseConfig {
		var cmd tea.Cmd
		m.dbInput, cmd = m.dbInput.Update(msg)
		return m, cmd
	}
	if m.phase == phaseConfigPG {
		var cmd tea.Cmd
		m.pgInput, cmd = m.pgInput.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.phase {

	// ── Welcome ──────────────────────────────────────────────────────────────
	case phaseWelcome:
		switch msg.String() {
		case "enter", " ":
			m.phase = phaseBinary
			return m, cmdCheckBinary()
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	// ── Binary in PATH ───────────────────────────────────────────────────────
	case phaseBinary:
		switch msg.String() {
		case "i":
			if !m.binaryOK && !m.binaryInstalling && m.binaryPath != "" {
				m.binaryInstalling = true
				m.binaryInstallErr = ""
				return m, cmdInstallBinary(m.binaryPath)
			}
		case "enter", " ":
			if m.binaryInstalling {
				return m, nil // wait for install to finish
			}
			if m.binaryOK {
				m.done = append(m.done, styleOK.Render("  ✓ Binario: ")+m.binaryPath)
			} else {
				m.done = append(m.done, styleWarn.Render("  !! Binario: no está en PATH (instalar manualmente)"))
			}
			m.phase = phaseDBChoice
			return m, nil
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	// ── DB backend choice ────────────────────────────────────────────────────
	case phaseDBChoice:
		switch msg.String() {
		case "j", "down":
			if m.dbCursor < 1 {
				m.dbCursor++
			}
		case "k", "up":
			if m.dbCursor > 0 {
				m.dbCursor--
			}
		case "enter":
			if m.dbCursor == 0 {
				m.cfg.DB.Backend = "sqlite"
				m.phase = phaseConfig
				m.dbInput.Focus()
			} else {
				m.cfg.DB.Backend = "postgres"
				m.phase = phaseConfigPG
				m.pgInput.Focus()
			}
			return m, nil
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	// ── Config SQLite (DB path) ──────────────────────────────────────────────
	case phaseConfig:
		switch msg.String() {
		case "enter":
			dbPath := m.dbInput.Value()
			if dbPath == "" {
				dbPath = m.dbInput.Placeholder
			}
			m.cfg.DB.SQLitePath = dbPath
			m.done = append(m.done, styleOK.Render("  ✓ Base de datos: ")+"SQLite — "+dbPath)
			m.phase = phaseOllama
			m.dbInput.Blur()
			return m, tea.Batch(
				cmdSaveConfig(m.cfg),
				cmdCheckOllama(m.ollamaURL),
			)
		case "ctrl+c":
			return m, tea.Quit
		default:
			var cmd tea.Cmd
			m.dbInput, cmd = m.dbInput.Update(msg)
			return m, cmd
		}

	// ── Config PostgreSQL (DSN) ──────────────────────────────────────────────
	case phaseConfigPG:
		switch msg.String() {
		case "enter":
			dsn := m.pgInput.Value()
			if dsn == "" || !strings.HasPrefix(dsn, "postgresql://") {
				dsn = m.pgInput.Placeholder
			}
			m.cfg.DB.PostgresDSN = dsn
			m.wantsPostgresDocker = strings.Contains(dsn, "localhost") || strings.Contains(dsn, "127.0.0.1")
			label := "PostgreSQL"
			if m.wantsPostgresDocker {
				label += " (Docker automático)"
			}
			m.done = append(m.done, styleOK.Render("  ✓ Base de datos: ")+label)
			m.phase = phaseOllama
			m.pgInput.Blur()
			return m, tea.Batch(
				cmdSaveConfig(m.cfg),
				cmdCheckOllama(m.ollamaURL),
			)
		case "ctrl+c":
			return m, tea.Quit
		default:
			var cmd tea.Cmd
			m.pgInput, cmd = m.pgInput.Update(msg)
			return m, cmd
		}

	// ── Ollama check ─────────────────────────────────────────────────────────
	case phaseOllama:
		switch msg.String() {
		case "enter", " ":
			if m.ollamaOK {
				m.done = append(m.done, styleOK.Render("  ✓ Ollama: ")+m.ollamaURL)
				m.phase = phaseAgents
			} else {
				m.phase = phaseOllamaOpts
				m.ollamaCursor = 0
			}
			return m, nil
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	// ── Ollama options (not found) ────────────────────────────────────────────
	case phaseOllamaOpts:
		switch msg.String() {
		case "j", "down":
			if m.ollamaCursor < 2 {
				m.ollamaCursor++
			}
		case "k", "up":
			if m.ollamaCursor > 0 {
				m.ollamaCursor--
			}
		case "enter":
			switch m.ollamaCursor {
			case 0: // instalar localmente — solo mostrar hint, usuario lo hace
				m.done = append(m.done, styleWarn.Render("  !! Ollama: instalar manualmente (ver instrucciones)"))
				m.phase = phaseAgents
			case 1: // Docker — incluir en la fase de setup
				m.wantsDocker = true
				m.done = append(m.done, styleOK.Render("  ✓ Ollama: se instalará via Docker en el siguiente paso"))
				m.phase = phaseAgents
			case 2: // omitir
				m.done = append(m.done, styleMuted.Render("  -- Ollama: omitido"))
				m.phase = phaseAgents
			}
			return m, nil
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	// ── Agent selection ───────────────────────────────────────────────────────
	case phaseAgents:
		switch msg.String() {
		case "j", "down":
			if m.agentCursor < len(m.agents)-1 {
				m.agentCursor++
			}
		case "k", "up":
			if m.agentCursor > 0 {
				m.agentCursor--
			}
		case " ":
			m.agents[m.agentCursor].checked = !m.agents[m.agentCursor].checked
		case "enter":
			m.phase = phaseSetup
			m.setupLog = nil
			m.setupDone = false
			ollamaModel := m.cfg.Embeddings.OllamaModel
			if ollamaModel == "" {
				ollamaModel = "bge-m3"
			}
			var setupCmd tea.Cmd
			setupCmd, m.cancelSetup = cmdRunSetup(m.agents, m.wantsDocker, m.wantsPostgresDocker, m.cfg.DB.PostgresDSN, ollamaModel)
			return m, tea.Batch(m.sp.Tick, setupCmd)
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	// ── Setup running ─────────────────────────────────────────────────────────
	case phaseSetup:
		switch msg.String() {
		case "esc", "q":
			if m.cancelSetup != nil {
				m.cancelSetup()
				m.cancelSetup = nil
			}
			m.setupLog = append(m.setupLog, "Instalación cancelada por el usuario")
			m.setupDone = true
			m.phase = phaseDone
			return m, cmdRunDoctor(m.cfg)
		case "ctrl+c":
			if m.cancelSetup != nil {
				m.cancelSetup()
			}
			return m, tea.Quit
		}

	// ── Done ──────────────────────────────────────────────────────────────────
	case phaseDone:
		switch msg.String() {
		case "q", "enter", "ctrl+c":
			return m, tea.Quit
		}
	}

	return m, nil
}
