package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case statsMsg:
		if msg.err == nil {
			m.stats = msg.stats
		}
		return m, nil

	case recentMsg:
		if msg.err == nil {
			m.recentObs = msg.obs
		}
		return m, nil

	case searchMsg:
		m.searchResults = msg.results
		m.searchQuery = msg.query
		m.screen = ScreenSearchResults
		m.cursor = 0
		m.scroll = 0
		return m, nil

	case sessionsMsg:
		if msg.err == nil {
			m.sessions = msg.sessions
		}
		return m, nil

	case sessionDetailMsg:
		if msg.err == nil {
			m.sessionDetail = msg.obs
		}
		return m, nil

	case doctorMsg:
		m.doctorReport = msg.report
		m.doctorRunning = false
		return m, nil

	case doctorFixMsg:
		if msg.done {
			m.doctorRunning = false
			m.doctorProgress = append(m.doctorProgress, "Completado.")
			return m, m.runDoctor()
		}
		m.doctorProgress = append(m.doctorProgress, msg.line)
		return m, nil

	case ollamaModelsMsg:
		if msg.err == nil {
			m.ollamaModels = msg.models
			m.ollamaOK = true
		} else {
			m.ollamaOK = false
		}
		return m, nil

	case exportDoneMsg:
		if msg.err != nil {
			m.exportDone = "Error: " + msg.err.Error()
		} else {
			m.exportDone = "Exportado a " + msg.path
		}
		return m, nil

	case setupDoneMsg:
		if msg.err != nil {
			m.errMsg = "Error: " + msg.err.Error()
		} else {
			m.errMsg = msg.agent + " configurado."
		}
		return m, nil

	case timelineMsg:
		if msg.err == nil {
			m.timelineObs = msg.obs
		}
		m.screen = ScreenTimeline
		m.cursor = 0
		m.scroll = 0
		return m, nil
	}

	// Spinner tick
	var spinCmd tea.Cmd
	m.spinner, spinCmd = m.spinner.Update(msg)

	// Route to per-screen handler
	var cmd tea.Cmd
	switch m.screen {
	case ScreenDashboard:
		m, cmd = m.updateDashboard(msg)
	case ScreenSearch:
		m, cmd = m.updateSearch(msg)
	case ScreenSearchResults:
		m, cmd = m.updateSearchResults(msg)
	case ScreenRecent:
		m, cmd = m.updateRecent(msg)
	case ScreenObservationDetail:
		m, cmd = m.updateObservationDetail(msg)
	case ScreenTimeline:
		m, cmd = m.updateTimeline(msg)
	case ScreenSessions:
		m, cmd = m.updateSessions(msg)
	case ScreenSessionDetail:
		m, cmd = m.updateSessionDetail(msg)
	case ScreenDoctor:
		m, cmd = m.updateDoctor(msg)
	case ScreenDoctorFix:
		m, cmd = m.updateDoctorFix(msg)
	case ScreenConfig:
		m, cmd = m.updateConfig(msg)
	case ScreenOllama:
		m, cmd = m.updateOllama(msg)
	case ScreenExport:
		m, cmd = m.updateExport(msg)
	case ScreenSetup:
		m, cmd = m.updateSetup(msg)
	}

	return m, tea.Batch(cmd, spinCmd)
}

// --- menu items for dashboard ---

var dashboardMenu = []struct {
	label  string
	screen Screen
	key    string
}{
	{"Buscar observaciones", ScreenSearch, "s"},
	{"Recientes", ScreenRecent, "r"},
	{"Sesiones", ScreenSessions, "S"},
	{"Doctor", ScreenDoctor, "d"},
	{"Configuracion", ScreenConfig, "c"},
	{"Ollama", ScreenOllama, "o"},
	{"Exportar", ScreenExport, "e"},
	{"Setup agentes", ScreenSetup, "a"},
}

// --- per-screen update handlers ---

func (m Model) updateDashboard(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			if m.cursor < len(dashboardMenu)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "s":
			m.screen = ScreenSearch
			m.searchInput.Focus()
			return m, nil
		case "enter", " ":
			if m.cursor < len(dashboardMenu) {
				item := dashboardMenu[m.cursor]
				return m.navigateTo(item.screen)
			}
		}
	}
	return m, nil
}

func (m Model) updateSearch(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			m.screen = ScreenDashboard
			m.cursor = 0
			return m, nil
		case "enter":
			q := m.searchInput.Value()
			if q != "" {
				return m, m.doSearch(q)
			}
		}
	}
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	return m, cmd
}

func (m Model) updateSearchResults(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			m.screen = ScreenSearch
			return m, nil
		case "j", "down":
			if m.cursor < len(m.searchResults)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "enter":
			if m.cursor < len(m.searchResults) {
				r := m.searchResults[m.cursor]
				obs, err := m.store.GetObservationSync(r.ID)
				if err == nil && obs != nil {
					m.selectedObs = obs
					m.prevScreen = ScreenSearchResults
					m.screen = ScreenObservationDetail
					m.scroll = 0
				}
			}
		case "t":
			if m.cursor < len(m.searchResults) {
				obsID := m.searchResults[m.cursor].ID
				m.prevScreen = ScreenSearchResults
				return m, m.loadTimeline(obsID)
			}
		}
	}
	return m, nil
}

func (m Model) updateRecent(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			m.screen = ScreenDashboard
			m.cursor = 0
			return m, nil
		case "j", "down":
			if m.cursor < len(m.recentObs)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "enter":
			if m.cursor < len(m.recentObs) {
				m.selectedObs = m.recentObs[m.cursor]
				m.prevScreen = ScreenRecent
				m.screen = ScreenObservationDetail
				m.scroll = 0
			}
		case "t":
			if m.cursor < len(m.recentObs) {
				obsID := m.recentObs[m.cursor].ID
				m.prevScreen = ScreenRecent
				return m, m.loadTimeline(obsID)
			}
		}
	}
	return m, nil
}

func (m Model) updateObservationDetail(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			m.screen = m.prevScreen
			return m, nil
		case "j", "down":
			m.scroll++
		case "k", "up":
			if m.scroll > 0 {
				m.scroll--
			}
		case "t":
			if m.selectedObs != nil {
				m.prevScreen = ScreenObservationDetail
				return m, m.loadTimeline(m.selectedObs.ID)
			}
		}
	}
	return m, nil
}

func (m Model) updateTimeline(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			m.screen = m.prevScreen
			return m, nil
		case "j", "down":
			if m.cursor < len(m.timelineObs)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "enter":
			if m.cursor < len(m.timelineObs) {
				m.selectedObs = m.timelineObs[m.cursor]
				m.prevScreen = ScreenTimeline
				m.screen = ScreenObservationDetail
				m.scroll = 0
			}
		}
	}
	return m, nil
}

func (m Model) updateSessions(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			m.screen = ScreenDashboard
			m.cursor = 0
			return m, nil
		case "j", "down":
			if m.cursor < len(m.sessions)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "enter":
			if m.cursor < len(m.sessions) {
				m.selectedSess = m.sessions[m.cursor]
				m.prevScreen = ScreenSessions
				m.screen = ScreenSessionDetail
				m.scroll = 0
				return m, m.loadSessionDetail(m.selectedSess.ID)
			}
		}
	}
	return m, nil
}

func (m Model) updateSessionDetail(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			m.screen = ScreenSessions
			return m, nil
		case "j", "down":
			if m.cursor < len(m.sessionDetail)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "enter":
			if m.cursor < len(m.sessionDetail) {
				m.selectedObs = m.sessionDetail[m.cursor]
				m.prevScreen = ScreenSessionDetail
				m.screen = ScreenObservationDetail
				m.scroll = 0
			}
		}
	}
	return m, nil
}

func (m Model) updateDoctor(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			m.screen = ScreenDashboard
			m.cursor = 0
			return m, nil
		case "j", "down":
			if m.doctorCursor < len(m.doctorReport.Checks)-1 {
				m.doctorCursor++
			}
		case "k", "up":
			if m.doctorCursor > 0 {
				m.doctorCursor--
			}
		case "r":
			m.doctorRunning = true
			return m, m.runDoctor()
		case "f":
			// fix all failing checks
			m.doctorProgress = nil
			m.doctorRunning = true
			m.screen = ScreenDoctorFix
			if len(m.doctorReport.Checks) > 0 {
				return m, m.runDoctorFix(m.doctorReport.Checks[0].Name)
			}
		case "enter":
			if m.doctorCursor < len(m.doctorReport.Checks) {
				check := m.doctorReport.Checks[m.doctorCursor]
				if check.FixAvailable {
					m.doctorProgress = nil
					m.doctorRunning = true
					m.screen = ScreenDoctorFix
					return m, m.runDoctorFix(check.Name)
				}
			}
		}
	}
	return m, nil
}

func (m Model) updateDoctorFix(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			if !m.doctorRunning {
				m.screen = ScreenDoctor
				return m, nil
			}
		}
	case doctorFixMsg:
		if msg.done {
			m.doctorRunning = false
			m.doctorProgress = append(m.doctorProgress, "Completado.")
			return m, m.runDoctor()
		}
		m.doctorProgress = append(m.doctorProgress, msg.line)
		// Continue draining (handled by the channel approach in model.go)
		return m, nil
	}
	return m, nil
}

func (m Model) updateConfig(msg tea.Msg) (Model, tea.Cmd) {
	if m.configEditing {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "enter", "esc":
				// Save value
				if m.cursor < len(m.configFields) {
					m.configFields[m.cursor].value = m.configInput.Value()
					_ = m.cfg.Set(
						m.configFields[m.cursor].section+"."+m.configFields[m.cursor].key,
						m.configInput.Value(),
					)
				}
				m.configEditing = false
				m.configInput.Blur()
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.configInput, cmd = m.configInput.Update(msg)
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			m.screen = ScreenDashboard
			m.cursor = 0
			return m, nil
		case "j", "down":
			if m.cursor < len(m.configFields)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "s":
			_ = m.cfg.Save()
			m.errMsg = "Configuracion guardada."
		case "enter":
			if m.cursor < len(m.configFields) {
				m.configEditing = true
				m.configInput.SetValue(m.configFields[m.cursor].value)
				m.configInput.Focus()
			}
		}
	}
	return m, nil
}

func (m Model) updateOllama(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			m.screen = ScreenDashboard
			m.cursor = 0
			return m, nil
		case "j", "down":
			if m.cursor < len(m.ollamaModels)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "r":
			return m, m.checkOllama()
		case "enter":
			// pull selected model (placeholder)
			m.errMsg = "Pull no implementado aun — usa: ollama pull <model>"
		}
	}
	return m, nil
}

func (m Model) updateExport(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			m.screen = ScreenDashboard
			m.cursor = 0
			return m, nil
		case "enter":
			m.exportDone = ""
			return m, m.doExport()
		}
	}
	return m, nil
}

func (m Model) updateSetup(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			m.screen = ScreenDashboard
			m.cursor = 0
			return m, nil
		case "j", "down":
			if m.cursor < len(m.setupAgents)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "enter":
			if m.cursor < len(m.setupAgents) {
				agent := m.setupAgents[m.cursor]
				return m, m.doSetupAgent(agent)
			}
		}
	}
	return m, nil
}

// navigateTo transitions to a screen and loads its initial data.
func (m Model) navigateTo(screen Screen) (Model, tea.Cmd) {
	m.prevScreen = m.screen
	m.screen = screen
	m.cursor = 0
	m.scroll = 0
	m.errMsg = ""

	var cmd tea.Cmd
	switch screen {
	case ScreenSearch:
		m.searchInput.SetValue("")
		m.searchInput.Focus()
	case ScreenRecent:
		cmd = m.loadRecent()
	case ScreenSessions:
		cmd = m.loadSessions()
	case ScreenDoctor:
		m.doctorRunning = true
		cmd = m.runDoctor()
	case ScreenOllama:
		cmd = m.checkOllama()
	}
	return m, cmd
}
