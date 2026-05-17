package wizard

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/jjgarcia-app/kronos-v2/internal/doctor"
)

const banner = `
  ██╗  ██╗██████╗  ██████╗ ███╗   ██╗ ██████╗ ███████╗
  ██║ ██╔╝██╔══██╗██╔═══██╗████╗  ██║██╔═══██╗██╔════╝
  █████╔╝ ██████╔╝██║   ██║██╔██╗ ██║██║   ██║███████╗
  ██╔═██╗ ██╔══██╗██║   ██║██║╚██╗██║██║   ██║╚════██║
  ██║  ██╗██║  ██║╚██████╔╝██║ ╚████║╚██████╔╝███████║
  ╚═╝  ╚═╝╚═╝  ╚═╝ ╚═════╝ ╚═╝  ╚═══╝ ╚═════╝ ╚══════╝`

func (m Model) View() string {
	var b strings.Builder

	// Header
	b.WriteString(styleTitle.Render(banner))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render("  Memoria persistente para agentes de IA  —  Asistente de configuración"))
	b.WriteString("\n\n")

	// Completed phases (summary)
	for _, line := range m.done {
		b.WriteString(line + "\n")
	}
	if len(m.done) > 0 {
		b.WriteString("\n")
	}

	// Active phase
	switch m.phase {
	case phaseWelcome:
		b.WriteString(renderBox(m.width,
			stylePhase.Render("Bienvenido")+"\n\n"+
				styleText.Render("Este asistente configurará Kronos en tu sistema:\n")+
				styleMuted.Render("  • Verificar que el binario esté en PATH\n")+
				styleMuted.Render("  • Crear la base de datos\n")+
				styleMuted.Render("  • Detectar o instalar Ollama (opcional)\n")+
				styleMuted.Render("  • Registrar Kronos en tus agentes de IA\n")+
				"\n"+
				styleMuted.Render("Sistema: ")+styleText.Render(osLabel()),
		))
		b.WriteString(styleHelp.Render("\n  Enter para comenzar  ·  q para salir"))

	case phaseBinary:
		content := stylePhase.Render("1/4  Binario en PATH") + "\n\n"
		if m.binaryPath == "" {
			content += m.sp.View() + " Verificando..."
		} else if m.binaryOK {
			content += styleOK.Render("✓ kronos encontrado en:\n") +
				styleText.Render("  "+m.binaryPath)
		} else {
			content += styleWarn.Render("!! kronos no está en PATH\n\n") +
				styleText.Render("Ubicación actual del binario:\n") +
				styleMuted.Render("  "+m.binaryPath+"\n\n") +
				styleText.Render("Para añadirlo al PATH:\n") +
				styleMuted.Render(indent(pathHint(), "  "))
		}
		b.WriteString(renderBox(m.width, content))
		if m.binaryPath != "" {
			b.WriteString(styleHelp.Render("\n  Enter para continuar"))
		}

	case phaseConfig:
		content := stylePhase.Render("2/4  Base de datos") + "\n\n" +
			styleText.Render("Ruta donde Kronos guardará tus memorias:\n") +
			"  " + m.dbInput.View()
		b.WriteString(renderBox(m.width, content))
		b.WriteString(styleHelp.Render("\n  Enter para confirmar"))

	case phaseOllama:
		content := stylePhase.Render("3/4  Embeddings semánticos") + "\n\n"
		if m.ollamaURL == "" {
			content += m.sp.View() + " Verificando Ollama..."
		} else if m.ollamaOK {
			content += styleOK.Render("✓ Ollama disponible en "+m.ollamaURL+"\n") +
				styleMuted.Render("\nLa búsqueda semántica está habilitada.")
		} else {
			content += styleFail.Render("✗ Ollama no responde en "+m.ollamaURL+"\n") +
				styleMuted.Render("\nOllama habilita búsqueda semántica y contexto inteligente.\nSin él, Kronos usa búsqueda de texto completo (FTS5).")
		}
		b.WriteString(renderBox(m.width, content))
		if m.ollamaURL != "" {
			if m.ollamaOK {
				b.WriteString(styleHelp.Render("\n  Enter para continuar"))
			} else {
				b.WriteString(styleHelp.Render("\n  Enter para ver opciones"))
			}
		}

	case phaseOllamaOpts:
		opts := []string{
			"Instalar localmente (ver instrucciones)",
			"Instalar via Docker   (automático)",
			"Omitir por ahora",
		}
		content := stylePhase.Render("3/4  Opciones de Ollama") + "\n\n"
		for i, opt := range opts {
			if i == m.ollamaCursor {
				content += styleCursor.Render("> ") + styleHighlight.Render(opt) + "\n"
			} else {
				content += "  " + styleMuted.Render(opt) + "\n"
			}
		}
		if m.ollamaCursor == 0 {
			content += "\n" + styleMuted.Render(indent(ollamaLocalHint(), "  "))
		}
		b.WriteString(renderBox(m.width, content))
		b.WriteString(styleHelp.Render("\n  j/k mover  ·  Enter seleccionar"))

	case phaseAgents:
		content := stylePhase.Render("4/4  Agentes de IA") + "\n\n" +
			styleText.Render("Selecciona dónde instalar Kronos:\n\n")
		for i, a := range m.agents {
			cursor := "  "
			check := "[ ]"
			nameStyle := styleMuted
			if i == m.agentCursor {
				cursor = styleCursor.Render("> ")
				nameStyle = styleHighlight
			}
			if a.checked {
				check = styleOK.Render("[✓]")
			}
			content += cursor + check + " " + nameStyle.Render(a.label) +
				"  " + styleMuted.Render(a.desc) + "\n"
		}
		b.WriteString(renderBox(m.width, content))
		b.WriteString(styleHelp.Render("\n  j/k mover  ·  Espacio marcar/desmarcar  ·  Enter instalar"))

	case phaseSetup:
		content := stylePhase.Render("Instalando...") + "\n\n"
		if len(m.setupLog) == 0 {
			content += m.sp.View() + " Iniciando..."
		} else {
			// show last 10 lines to keep within box
			start := 0
			if len(m.setupLog) > 10 {
				start = len(m.setupLog) - 10
			}
			for _, line := range m.setupLog[start:] {
				content += styleMuted.Render(line) + "\n"
			}
			if !m.setupDone {
				content += "\n" + m.sp.View() + " Trabajando..."
			}
		}
		b.WriteString(renderBox(m.width, content))

	case phaseDone:
		b.WriteString(renderDoctorReport(m.report, m.width))
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

func renderBox(width int, content string) string {
	boxWidth := width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}
	return styleBox.Width(boxWidth).Render(content)
}

func renderDoctorReport(r doctor.Report, width int) string {
	if len(r.Checks) == 0 {
		return styleMuted.Render("  Cargando verificación...")
	}
	var sb strings.Builder
	sb.WriteString(stylePhase.Render("  Verificación del sistema") + "\n\n")
	for _, c := range r.Checks {
		icon := styleOK.Render("✓")
		switch c.Status {
		case doctor.StatusWarn:
			icon = styleWarn.Render("!")
		case doctor.StatusFail:
			icon = styleFail.Render("✗")
		}
		name := lipgloss.NewStyle().Width(22).Render(c.Name + ":")
		sb.WriteString(fmt.Sprintf("  %s %s%s\n", icon, name, styleMuted.Render(c.Detail)))
	}
	return sb.String()
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
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
