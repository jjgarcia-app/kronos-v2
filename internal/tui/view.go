package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/jjgarcia-app/kronos-v2/internal/doctor"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func (m Model) View() string {
	if m.width == 0 {
		return "Cargando..."
	}
	switch m.screen {
	case ScreenDashboard:
		return m.viewDashboard()
	case ScreenSearch:
		return m.viewSearch()
	case ScreenSearchResults:
		return m.viewSearchResults()
	case ScreenRecent:
		return m.viewRecent()
	case ScreenObservationDetail:
		return m.viewObservationDetail()
	case ScreenTimeline:
		return m.viewTimeline()
	case ScreenSessions:
		return m.viewSessions()
	case ScreenSessionDetail:
		return m.viewSessionDetail()
	case ScreenDoctor:
		return m.viewDoctor()
	case ScreenDoctorFix:
		return m.viewDoctorFix()
	case ScreenConfig:
		return m.viewConfig()
	case ScreenOllama:
		return m.viewOllama()
	case ScreenExport:
		return m.viewExport()
	case ScreenSetup:
		return m.viewSetup()
	}
	return ""
}

// --- dashboard ---

func (m Model) viewDashboard() string {
	var b strings.Builder

	// Status bar (top)
	ollamaStatus := styleOK.Render("Ollama OK")
	if !m.ollamaOK {
		ollamaStatus = styleFail.Render("Ollama offline")
	}
	statusBar := styleStatusBar.Width(m.width).Render(
		"Kronos v2  |  " + ollamaStatus,
	)
	b.WriteString(statusBar + "\n")

	// Logo
	logo := lipgloss.NewStyle().
		Foreground(colorIris).
		Render(logoASCII)
	b.WriteString(lipgloss.NewStyle().Width(m.width).Align(lipgloss.Center).Render(logo))
	b.WriteString("\n")

	// Stats card
	if m.stats != nil {
		projects := strings.Join(m.stats.Projects, ", ")
		if projects == "" {
			projects = "(ninguno)"
		}
		card := styleCard.Render(
			styleTitle.Render("Estado") + "\n" +
				fmt.Sprintf("  Sesiones:      %s\n", styleOK.Render(fmt.Sprintf("%d", m.stats.TotalSessions))) +
				fmt.Sprintf("  Observaciones: %s\n", styleOK.Render(fmt.Sprintf("%d", m.stats.TotalObservations))) +
				fmt.Sprintf("  Prompts:       %s\n", styleOK.Render(fmt.Sprintf("%d", m.stats.TotalPrompts))) +
				fmt.Sprintf("  Proyectos:     %s", styleSubtext.Render(truncate(projects, 40))),
		)
		b.WriteString(lipgloss.NewStyle().Width(m.width).Align(lipgloss.Center).Render(card))
		b.WriteString("\n\n")
	}

	// Menu
	b.WriteString(styleTitle.Render("  Menu") + "\n")
	for i, item := range dashboardMenu {
		line := fmt.Sprintf("  [%s] %s", item.key, item.label)
		if i == m.cursor {
			b.WriteString(styleHighlight.Width(m.width).Render(styleCursor.Render("▶ ") + item.label) + "\n")
		} else {
			b.WriteString(styleMuted.Render(line) + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(styleHelp.Render("  j/k navegar · enter seleccionar · q salir"))

	return b.String()
}

// --- search ---

func (m Model) viewSearch() string {
	var b strings.Builder
	b.WriteString(m.headerLine("Buscar observaciones"))
	b.WriteString("\n\n")
	b.WriteString("  " + styleInput.Render(m.searchInput.View()) + "\n\n")
	b.WriteString(styleHelp.Render("  enter buscar · esc volver"))
	return b.String()
}

func (m Model) viewSearchResults() string {
	var b strings.Builder
	b.WriteString(m.headerLine(fmt.Sprintf("Resultados: %q — %d encontrado(s)", m.searchQuery, len(m.searchResults))))
	b.WriteString("\n")

	if len(m.searchResults) == 0 {
		b.WriteString("\n  " + styleMuted.Render("Sin resultados.") + "\n")
	} else {
		visible := m.visibleLines()
		start, end := m.scrollWindow(len(m.searchResults), visible)
		for i := start; i < end; i++ {
			r := m.searchResults[i]
			line := fmt.Sprintf("  [%s] %s  %s",
				styleTag.Render(string(r.Type)),
				truncate(r.Title, 50),
				styleSubtext.Render(r.Project),
			)
			if i == m.cursor {
				b.WriteString(styleHighlight.Width(m.width).Render(styleCursor.Render("▶ ")+strings.TrimPrefix(line, "  ")) + "\n")
			} else {
				b.WriteString(line + "\n")
			}
		}
	}

	b.WriteString("\n")
	b.WriteString(styleHelp.Render("  j/k navegar · enter detalle · t timeline · esc volver"))
	return b.String()
}

// --- recent ---

func (m Model) viewRecent() string {
	var b strings.Builder
	b.WriteString(m.headerLine(fmt.Sprintf("Recientes (%d)", len(m.recentObs))))
	b.WriteString("\n")

	if len(m.recentObs) == 0 {
		b.WriteString("\n  " + styleMuted.Render("Sin observaciones.") + "\n")
	} else {
		visible := m.visibleLines()
		start, end := m.scrollWindow(len(m.recentObs), visible)
		for i := start; i < end; i++ {
			obs := m.recentObs[i]
			age := formatAge(obs.UpdatedAt)
			line := fmt.Sprintf("  [%s] %s  %s  %s",
				styleTag.Render(string(obs.Type)),
				truncate(obs.Title, 45),
				styleSubtext.Render(obs.Project),
				styleMuted.Render(age),
			)
			if i == m.cursor {
				b.WriteString(styleHighlight.Width(m.width).Render(styleCursor.Render("▶ ")+strings.TrimPrefix(line, "  ")) + "\n")
			} else {
				b.WriteString(line + "\n")
			}
		}
	}

	b.WriteString("\n")
	b.WriteString(styleHelp.Render("  j/k navegar · enter detalle · t timeline · esc volver"))
	return b.String()
}

// --- observation detail ---

func (m Model) viewObservationDetail() string {
	if m.selectedObs == nil {
		return m.headerLine("Detalle") + "\n\n  Sin selección."
	}
	obs := m.selectedObs
	var b strings.Builder
	b.WriteString(m.headerLine("Observacion #" + fmt.Sprintf("%d", obs.ID)))
	b.WriteString("\n")

	meta := fmt.Sprintf("  %s  |  %s  |  %s  |  rev:%d  |  %s",
		styleTag.Render(string(obs.Type)),
		styleOK.Render(obs.Project),
		styleMuted.Render(string(obs.Scope)),
		obs.RevisionCount,
		styleMuted.Render(obs.UpdatedAt.Format("2006-01-02 15:04")),
	)
	b.WriteString(meta + "\n")
	b.WriteString(styleMuted.Render(strings.Repeat("─", min(m.width-2, 80))) + "\n")

	b.WriteString(styleTitle.Render("  "+obs.Title) + "\n\n")

	// scroll through content
	lines := strings.Split(obs.Content, "\n")
	start := m.scroll
	if start >= len(lines) {
		start = len(lines) - 1
	}
	if start < 0 {
		start = 0
	}
	visible := m.height - 10
	if visible < 5 {
		visible = 5
	}
	end := start + visible
	if end > len(lines) {
		end = len(lines)
	}
	for _, line := range lines[start:end] {
		b.WriteString("  " + line + "\n")
	}

	b.WriteString("\n")
	b.WriteString(styleHelp.Render("  j/k scroll · t timeline · esc volver"))
	return b.String()
}

// --- timeline ---

func (m Model) viewTimeline() string {
	var b strings.Builder
	b.WriteString(m.headerLine("Timeline de sesion"))
	b.WriteString("\n")

	if len(m.timelineObs) == 0 {
		b.WriteString("\n  " + styleMuted.Render("Sin observaciones en esta sesion.") + "\n")
	} else {
		for i, obs := range m.timelineObs {
			marker := "  "
			if m.selectedObs != nil && obs.ID == m.selectedObs.ID {
				marker = styleOK.Render("→ ")
			}
			ts := obs.CreatedAt.Format("15:04:05")
			line := fmt.Sprintf("%s[%s] %s  %s",
				marker,
				styleTag.Render(string(obs.Type)),
				truncate(obs.Title, 50),
				styleMuted.Render(ts),
			)
			if i == m.cursor {
				b.WriteString(styleHighlight.Width(m.width).Render(styleCursor.Render("▶ ")+strings.TrimPrefix(line, "  ")) + "\n")
			} else {
				b.WriteString(line + "\n")
			}
		}
	}

	b.WriteString("\n")
	b.WriteString(styleHelp.Render("  j/k navegar · enter detalle · esc volver"))
	return b.String()
}

// --- sessions ---

func (m Model) viewSessions() string {
	var b strings.Builder
	b.WriteString(m.headerLine(fmt.Sprintf("Sesiones (%d)", len(m.sessions))))
	b.WriteString("\n")

	if len(m.sessions) == 0 {
		b.WriteString("\n  " + styleMuted.Render("Sin sesiones.") + "\n")
	} else {
		visible := m.visibleLines()
		start, end := m.scrollWindow(len(m.sessions), visible)
		for i := start; i < end; i++ {
			sess := m.sessions[i]
			status := styleOK.Render("activa")
			if sess.EndedAt != nil {
				status = styleMuted.Render("cerrada")
			}
			line := fmt.Sprintf("  %s  %s  %s  %s",
				styleSubtext.Render(sess.ID[:min(8, len(sess.ID))]),
				styleOK.Render(sess.Project),
				status,
				styleMuted.Render(sess.StartedAt.Format("2006-01-02")),
			)
			if i == m.cursor {
				b.WriteString(styleHighlight.Width(m.width).Render(styleCursor.Render("▶ ")+strings.TrimPrefix(line, "  ")) + "\n")
			} else {
				b.WriteString(line + "\n")
			}
		}
	}

	b.WriteString("\n")
	b.WriteString(styleHelp.Render("  j/k navegar · enter detalle · esc volver"))
	return b.String()
}

func (m Model) viewSessionDetail() string {
	if m.selectedSess == nil {
		return m.headerLine("Sesion") + "\n\n  Sin sesion seleccionada."
	}
	var b strings.Builder
	sess := m.selectedSess
	b.WriteString(m.headerLine("Sesion " + sess.ID[:min(8, len(sess.ID))]))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("  Proyecto: %s  |  Dir: %s\n",
		styleOK.Render(sess.Project),
		styleMuted.Render(truncate(sess.Directory, 40)),
	))
	if sess.Summary != "" {
		b.WriteString("  " + styleSubtext.Render(truncate(sess.Summary, 80)) + "\n")
	}
	b.WriteString(styleMuted.Render(strings.Repeat("─", min(m.width-2, 80))) + "\n\n")

	if len(m.sessionDetail) == 0 {
		b.WriteString("  " + styleMuted.Render("Cargando...") + "\n")
	} else {
		visible := m.visibleLines()
		start, end := m.scrollWindow(len(m.sessionDetail), visible)
		for i := start; i < end; i++ {
			obs := m.sessionDetail[i]
			line := fmt.Sprintf("  [%s] %s",
				styleTag.Render(string(obs.Type)),
				truncate(obs.Title, 60),
			)
			if i == m.cursor {
				b.WriteString(styleHighlight.Width(m.width).Render(styleCursor.Render("▶ ")+strings.TrimPrefix(line, "  ")) + "\n")
			} else {
				b.WriteString(line + "\n")
			}
		}
	}

	b.WriteString("\n")
	b.WriteString(styleHelp.Render("  j/k navegar · enter detalle · esc volver"))
	return b.String()
}

// --- doctor ---

func (m Model) viewDoctor() string {
	var b strings.Builder
	b.WriteString(m.headerLine("Doctor"))
	b.WriteString("\n")

	if m.doctorRunning {
		b.WriteString("  " + m.spinner.View() + " Ejecutando checks...\n")
		return b.String()
	}

	if len(m.doctorReport.Checks) == 0 {
		b.WriteString("\n  " + styleMuted.Render("Sin resultados aun. Presiona r para ejecutar.") + "\n")
	} else {
		for i, check := range m.doctorReport.Checks {
			icon := statusIcon(check.Status)
			fix := ""
			if check.FixAvailable {
				fix = "  " + styleSubtext.Render("[enter=fix]")
			}
			line := fmt.Sprintf("  %s %s  %s%s",
				icon,
				check.Name,
				styleMuted.Render(truncate(check.Detail, 40)),
				fix,
			)
			if i == m.doctorCursor {
				b.WriteString(styleHighlight.Width(m.width).Render(styleCursor.Render("▶ ")+strings.TrimPrefix(line, "  ")) + "\n")
			} else {
				b.WriteString(line + "\n")
			}
		}
	}

	b.WriteString("\n")
	b.WriteString(styleHelp.Render("  j/k navegar · r refresh · f fix-todo · enter fix-este · esc volver"))
	return b.String()
}

func (m Model) viewDoctorFix() string {
	var b strings.Builder
	b.WriteString(m.headerLine("Doctor Fix"))
	b.WriteString("\n\n")

	for _, line := range m.doctorProgress {
		b.WriteString("  " + line + "\n")
	}

	if m.doctorRunning {
		b.WriteString("\n  " + m.spinner.View() + " Ejecutando...\n")
	} else {
		b.WriteString("\n  " + styleOK.Render("Listo.") + "\n")
		b.WriteString("\n" + styleHelp.Render("  esc volver"))
	}

	return b.String()
}

// --- config ---

func (m Model) viewConfig() string {
	var b strings.Builder
	b.WriteString(m.headerLine("Configuracion"))
	b.WriteString("\n")

	if m.errMsg != "" {
		b.WriteString("  " + styleOK.Render(m.errMsg) + "\n\n")
	}

	for i, f := range m.configFields {
		val := f.value
		if i == m.cursor && m.configEditing {
			val = m.configInput.View()
		}
		line := fmt.Sprintf("  %-28s %s", f.label+":", styleSubtext.Render(val))
		if i == m.cursor {
			b.WriteString(styleHighlight.Width(m.width).Render(styleCursor.Render("▶ ")+strings.TrimPrefix(line, "  ")) + "\n")
		} else {
			b.WriteString(line + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(styleHelp.Render("  j/k navegar · enter editar · s guardar · esc volver"))
	return b.String()
}

// --- ollama ---

func (m Model) viewOllama() string {
	var b strings.Builder
	b.WriteString(m.headerLine("Ollama"))
	b.WriteString("\n")

	ollamaURL := m.cfg.Embeddings.OllamaURL
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}

	if m.ollamaOK {
		b.WriteString("  " + styleOK.Render("Conectado a "+ollamaURL) + "\n\n")
	} else {
		b.WriteString("  " + styleFail.Render("Sin conexion con "+ollamaURL) + "\n\n")
	}

	if len(m.ollamaModels) == 0 {
		b.WriteString("  " + styleMuted.Render("Sin modelos instalados.") + "\n")
	} else {
		b.WriteString(styleTitle.Render("  Modelos instalados:") + "\n")
		for i, model := range m.ollamaModels {
			current := ""
			if model == m.cfg.Embeddings.OllamaModel || model == m.cfg.Embeddings.OllamaModel+":latest" {
				current = "  " + styleOK.Render("(embed)")
			}
			if model == m.cfg.Embeddings.OllamaLLMModel || model == m.cfg.Embeddings.OllamaLLMModel+":latest" {
				current = current + "  " + styleIris.Render("(llm)")
			}
			line := fmt.Sprintf("  %s%s", model, current)
			if i == m.cursor {
				b.WriteString(styleHighlight.Width(m.width).Render(styleCursor.Render("▶ ")+strings.TrimPrefix(line, "  ")) + "\n")
			} else {
				b.WriteString(line + "\n")
			}
		}
	}

	b.WriteString("\n")
	b.WriteString(styleHelp.Render("  j/k navegar · r refresh · esc volver"))
	return b.String()
}

// --- export ---

func (m Model) viewExport() string {
	var b strings.Builder
	b.WriteString(m.headerLine("Exportar a Obsidian"))
	b.WriteString("\n\n")

	b.WriteString(fmt.Sprintf("  Directorio: %s\n\n",
		styleSubtext.Render(m.exportOutput)))

	if m.exportDone != "" {
		b.WriteString("  " + styleOK.Render(m.exportDone) + "\n\n")
	}

	b.WriteString(styleHelp.Render("  enter exportar · esc volver"))
	return b.String()
}

// --- setup ---

func (m Model) viewSetup() string {
	var b strings.Builder
	b.WriteString(m.headerLine("Setup de agentes"))
	b.WriteString("\n\n")

	if m.errMsg != "" {
		b.WriteString("  " + styleOK.Render(m.errMsg) + "\n\n")
	}

	for i, agent := range m.setupAgents {
		line := "  " + agent
		if i == m.cursor {
			b.WriteString(styleHighlight.Width(m.width).Render(styleCursor.Render("▶ ")+agent) + "\n")
		} else {
			b.WriteString(line + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(styleHelp.Render("  j/k navegar · enter instalar · esc volver"))
	return b.String()
}

// --- helpers ---

func (m Model) headerLine(title string) string {
	left := styleTitle.Render(" KRONOS  |  " + title)
	right := styleMuted.Render(time.Now().Format("15:04:05") + " ")
	padding := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if padding < 0 {
		padding = 0
	}
	return styleStatusBar.Width(m.width).Render(left + strings.Repeat(" ", padding) + right)
}

func (m Model) visibleLines() int {
	lines := m.height - 6
	if lines < 5 {
		lines = 5
	}
	return lines
}

func (m Model) scrollWindow(total, visible int) (start, end int) {
	start = m.cursor - visible/2
	if start < 0 {
		start = 0
	}
	end = start + visible
	if end > total {
		end = total
		start = end - visible
		if start < 0 {
			start = 0
		}
	}
	return start, end
}

func statusIcon(s doctor.Status) string {
	switch s {
	case doctor.StatusOK:
		return styleOK.Render("✓")
	case doctor.StatusWarn:
		return styleWarn.Render("⚠")
	case doctor.StatusFail:
		return styleFail.Render("✗")
	}
	return " "
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "ahora"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// obsTypeColor returns a color per observation type
func obsTypeColor(t store.ObservationType) lipgloss.Color {
	switch t {
	case store.TypeBugfix:
		return colorLove
	case store.TypeDecision:
		return colorGold
	case store.TypeArchitecture:
		return colorPine
	case store.TypeDiscovery:
		return colorFoam
	case store.TypePattern:
		return colorIris
	case store.TypeConfig:
		return colorRose
	case store.TypePreference:
		return colorMauve
	case store.TypePassive:
		return colorMuted
	case store.TypeSession:
		return colorSubtext
	}
	return colorText
}
