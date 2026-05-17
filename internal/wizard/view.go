package wizard

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/jjgarcia-app/kronos-v2/internal/doctor"
)

func (m Model) View() string {
	var b strings.Builder

	// ── Header ────────────────────────────────────────────────────────────────
	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colIris).
		PaddingLeft(2).Render("K R O N O S   v2"))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("  Memoria persistente para agentes de IA  —  Asistente de configuración"))
	b.WriteString("\n\n")

	// ── Completed phase summaries ─────────────────────────────────────────────
	for _, line := range m.done {
		b.WriteString(line + "\n")
	}
	if len(m.done) > 0 {
		b.WriteString("\n")
	}

	// ── Active phase ──────────────────────────────────────────────────────────
	switch m.phase {

	case phaseWelcome:
		content := lines(
			stylePhase.Render("Bienvenido"),
			"",
			styleText.Render("Este asistente configurará Kronos en tu sistema:"),
			styleMuted.Render("  •  Verificar que el binario esté en PATH"),
			styleMuted.Render("  •  Crear la base de datos"),
			styleMuted.Render("  •  Detectar o instalar Ollama (opcional)"),
			styleMuted.Render("  •  Registrar Kronos en tus agentes de IA"),
			"",
			styleMuted.Render("Sistema: ")+styleText.Render(osLabel()),
		)
		b.WriteString(renderBox(m.width, content))
		b.WriteString(styleHelp.Render("\n  Enter para comenzar  ·  q para salir"))

	case phaseBinary:
		var bodyLines []string
		bodyLines = append(bodyLines, stylePhase.Render("1/4  Binario en PATH"), "")
		if m.binaryPath == "" {
			bodyLines = append(bodyLines, m.sp.View()+"  Verificando...")
		} else if m.binaryOK {
			bodyLines = append(bodyLines,
				styleOK.Render("✓  kronos encontrado en:"),
				styleText.Render("   "+m.binaryPath),
			)
		} else {
			bodyLines = append(bodyLines,
				styleWarn.Render("!!  kronos no está en PATH"),
				"",
				styleText.Render("Ubicación actual del binario:"),
				styleMuted.Render("   "+m.binaryPath),
				"",
				styleText.Render("Para añadirlo al PATH:"),
			)
			for _, l := range strings.Split(pathHint(), "\n") {
				bodyLines = append(bodyLines, styleMuted.Render("   "+l))
			}
		}
		b.WriteString(renderBox(m.width, lines(bodyLines...)))
		if m.binaryPath != "" {
			b.WriteString(styleHelp.Render("\n  Enter para continuar"))
		}

	case phaseConfig:
		content := lines(
			stylePhase.Render("2/4  Base de datos"),
			"",
			styleText.Render("Ruta donde Kronos guardará tus memorias:"),
			"  "+m.dbInput.View(),
			"",
			styleMuted.Render("Presiona Enter para confirmar o edita la ruta."),
		)
		b.WriteString(renderBox(m.width, content))
		b.WriteString(styleHelp.Render("\n  Enter para confirmar"))

	case phaseOllama:
		var bodyLines []string
		bodyLines = append(bodyLines, stylePhase.Render("3/4  Embeddings semánticos"), "")
		if m.ollamaURL == "" {
			bodyLines = append(bodyLines, m.sp.View()+"  Verificando Ollama...")
		} else if m.ollamaOK {
			bodyLines = append(bodyLines,
				styleOK.Render("✓  Ollama disponible en "+m.ollamaURL),
				"",
				styleMuted.Render("La búsqueda semántica está habilitada."),
			)
		} else {
			bodyLines = append(bodyLines,
				styleFail.Render("✗  Ollama no responde en "+m.ollamaURL),
				"",
				styleMuted.Render("Ollama habilita búsqueda semántica y contexto inteligente."),
				styleMuted.Render("Sin él, Kronos usa búsqueda de texto completo (FTS5)."),
			)
		}
		b.WriteString(renderBox(m.width, lines(bodyLines...)))
		if m.ollamaURL != "" {
			if m.ollamaOK {
				b.WriteString(styleHelp.Render("\n  Enter para continuar"))
			} else {
				b.WriteString(styleHelp.Render("\n  Enter para ver opciones de instalación"))
			}
		}

	case phaseOllamaOpts:
		opts := []struct{ label, hint string }{
			{"Instalar localmente", ollamaLocalHint()},
			{"Instalar via Docker  (automático)", ""},
			{"Omitir por ahora", ""},
		}
		var bodyLines []string
		bodyLines = append(bodyLines, stylePhase.Render("3/4  Opciones de Ollama"), "")
		for i, opt := range opts {
			if i == m.ollamaCursor {
				bodyLines = append(bodyLines, styleCursor.Render("> ")+styleHighlight.Render(opt.label))
			} else {
				bodyLines = append(bodyLines, "  "+styleMuted.Render(opt.label))
			}
		}
		if m.ollamaCursor == 0 {
			bodyLines = append(bodyLines, "")
			for _, l := range strings.Split(ollamaLocalHint(), "\n") {
				bodyLines = append(bodyLines, styleMuted.Render("  "+l))
			}
		}
		b.WriteString(renderBox(m.width, lines(bodyLines...)))
		b.WriteString(styleHelp.Render("\n  j/k mover  ·  Enter seleccionar"))

	case phaseAgents:
		var bodyLines []string
		bodyLines = append(bodyLines,
			stylePhase.Render("4/4  Agentes de IA"),
			"",
			styleText.Render("Selecciona dónde instalar Kronos:"),
			"",
		)
		for i, a := range m.agents {
			cursor := "  "
			check := styleMuted.Render("[ ]")
			nameStyle := styleMuted
			if i == m.agentCursor {
				cursor = styleCursor.Render("> ")
				nameStyle = styleHighlight
			}
			if a.checked {
				check = styleOK.Render("[✓]")
			}
			bodyLines = append(bodyLines,
				cursor+check+"  "+nameStyle.Render(a.label)+"   "+styleMuted.Render(a.desc),
			)
		}
		b.WriteString(renderBox(m.width, lines(bodyLines...)))
		b.WriteString(styleHelp.Render("\n  j/k mover  ·  Espacio marcar/desmarcar  ·  Enter instalar"))

	case phaseSetup:
		var bodyLines []string
		bodyLines = append(bodyLines, stylePhase.Render("Instalando..."), "")
		if len(m.setupLog) == 0 {
			bodyLines = append(bodyLines, m.sp.View()+"  Iniciando...")
		} else {
			start := 0
			if len(m.setupLog) > 12 {
				start = len(m.setupLog) - 12
			}
			for _, l := range m.setupLog[start:] {
				bodyLines = append(bodyLines, styleMuted.Render(l))
			}
			if !m.setupDone {
				bodyLines = append(bodyLines, "", m.sp.View()+"  Trabajando...")
			}
		}
		b.WriteString(renderBox(m.width, lines(bodyLines...)))

	case phaseDone:
		b.WriteString(renderDoctorReport(m.report))
		b.WriteString("\n")
		b.WriteString(styleOK.Render("  ¡Kronos está listo!"))
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("  Reinicia tu agente de IA para activar el MCP server."))
		b.WriteString("\n")
		b.WriteString(styleMuted.Render("  Explora con: kronos tui"))
		b.WriteString(styleHelp.Render("\n\n  Enter para salir"))
	}

	return b.String()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// lines joins strings with \n — used to build box content without embedding
// newlines inside lipgloss.Render() calls (which causes misalignment).
func lines(ss ...string) string {
	return strings.Join(ss, "\n")
}

func renderBox(width int, content string) string {
	boxWidth := width - 6
	if boxWidth < 44 {
		boxWidth = 44
	}
	return styleBox.Width(boxWidth).Render(content)
}

func renderDoctorReport(r doctor.Report) string {
	if len(r.Checks) == 0 {
		return styleMuted.Render("  Cargando verificación...")
	}
	var sb strings.Builder
	sb.WriteString(stylePhase.Render("  Verificación final") + "\n\n")
	for _, c := range r.Checks {
		icon := styleOK.Render("✓")
		switch c.Status {
		case doctor.StatusWarn:
			icon = styleWarn.Render("!")
		case doctor.StatusFail:
			icon = styleFail.Render("✗")
		}
		name := lipgloss.NewStyle().Width(24).Render(c.Name + ":")
		sb.WriteString(fmt.Sprintf("  %s %s%s\n", icon, name, styleMuted.Render(c.Detail)))
	}
	return sb.String()
}

func osLabel() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "darwin":
		return "macOS"
	default:
		return "Linux"
	}
}
