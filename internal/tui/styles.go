package tui

import "github.com/charmbracelet/lipgloss"

// Rosé Pine dark palette
var (
	colorBase     = lipgloss.Color("#191724")
	colorSurface  = lipgloss.Color("#1f1d2e")
	colorOverlay  = lipgloss.Color("#26233a")
	colorMuted    = lipgloss.Color("#6e6a86")
	colorSubtext  = lipgloss.Color("#908caa")
	colorText     = lipgloss.Color("#e0def4")
	colorLove     = lipgloss.Color("#eb6f92")
	colorGold     = lipgloss.Color("#f6c177")
	colorRose     = lipgloss.Color("#ebbcba")
	colorPine     = lipgloss.Color("#31748f")
	colorFoam     = lipgloss.Color("#9ccfd8")
	colorIris     = lipgloss.Color("#c4a7e7")
	colorGreen    = lipgloss.Color("#9ccfd8")
	colorLavender = lipgloss.Color("#c4a7e7")
	colorBlue     = lipgloss.Color("#31748f")
	colorMauve    = lipgloss.Color("#c4a7e7")
	colorTeal     = lipgloss.Color("#9ccfd8")
)

var (
	styleBase = lipgloss.NewStyle().
			Background(colorBase).
			Foreground(colorText)

	styleSurface = lipgloss.NewStyle().
			Background(colorSurface).
			Foreground(colorText)

	styleOverlay = lipgloss.NewStyle().
			Background(colorOverlay).
			Foreground(colorText)

	styleMuted = lipgloss.NewStyle().
			Foreground(colorMuted)

	styleSubtext = lipgloss.NewStyle().
			Foreground(colorSubtext)

	styleTitle = lipgloss.NewStyle().
			Foreground(colorIris).
			Bold(true)

	styleHighlight = lipgloss.NewStyle().
			Background(colorOverlay).
			Foreground(colorText)

	styleCursor = lipgloss.NewStyle().
			Foreground(colorRose).
			Bold(true)

	styleOK = lipgloss.NewStyle().
		Foreground(colorFoam)

	styleWarn = lipgloss.NewStyle().
			Foreground(colorGold)

	styleFail = lipgloss.NewStyle().
			Foreground(colorLove)

	styleBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorOverlay).
			Padding(0, 1)

	styleCard = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPine).
			Padding(0, 1)

	styleStatusBar = lipgloss.NewStyle().
			Background(colorSurface).
			Foreground(colorSubtext).
			Padding(0, 1)

	styleHelp = lipgloss.NewStyle().
			Foreground(colorMuted)

	styleInput = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(colorPine).
			Padding(0, 1)

	styleTag = lipgloss.NewStyle().
			Background(colorOverlay).
			Foreground(colorIris).
			Padding(0, 1)

	styleIris = lipgloss.NewStyle().
			Foreground(colorIris)
)

const logoASCII = `
 ██╗  ██╗██████╗  ██████╗ ███╗   ██╗ ██████╗ ███████╗
 ██║ ██╔╝██╔══██╗██╔═══██╗████╗  ██║██╔═══██╗██╔════╝
 █████╔╝ ██████╔╝██║   ██║██╔██╗ ██║██║   ██║███████╗
 ██╔═██╗ ██╔══██╗██║   ██║██║╚██╗██║██║   ██║╚════██║
 ██║  ██╗██║  ██║╚██████╔╝██║ ╚████║╚██████╔╝███████║
 ╚═╝  ╚═╝╚═╝  ╚═╝ ╚═════╝ ╚═╝  ╚═══╝ ╚═════╝ ╚══════╝
`
