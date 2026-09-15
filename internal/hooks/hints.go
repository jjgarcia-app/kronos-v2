package hooks

import "strings"

// defaultHintsChars: tope del puntero de conducta.
const defaultHintsChars = 300

// hints es el texto FIJO que los hooks imprimen al arrancar cada sesión (y
// tras una compactación). Son PUNTEROS, no datos: no describen el entorno,
// dicen dónde está y cómo comprobarlo. Meter hechos "por si acaso" en el
// contexto es el presupuesto regalado que ya recortamos con
// core.max_session_items — el contenido del entorno se consulta a demanda
// con `kronos env`.
//
// Motivo medido el 2026-09-15: un agente afirmó "no tengo herramienta de
// navegador en este entorno" para justificar no sacar capturas de un fix
// (Playwright 1.63.0 y Chromium headless estaban instalados y cacheados), y
// después le preguntó al usuario el usuario de las credenciales de pruebas,
// que están escritas en el propio repo (.claude/commands/nemesis.md). Las dos
// cosas se resuelven comprobando antes de afirmar y leyendo la doc antes de
// preguntar.
var hints = []string{
	"[kronos] capacidades y entorno comprobables: `kronos env` (con hora de comprobacion)",
	"[kronos] documentacion: .claude/commands/ y docs/ del repo - /home/orca/projects/_infra/",
	"[kronos] comproba antes de afirmar algo tuyo o del entorno; si no lo encontras deci \"no lo encontre\"",
}

// HintsPreamble devuelve las líneas de conducta respetando un tope de chars
// (<=0 usa defaultHintsChars). Trunca por líneas completas: una línea cortada
// a la mitad es peor que una línea menos. Si ni la primera entra, no devuelve
// nada — el tope manda.
func HintsPreamble(maxChars int) string {
	if maxChars <= 0 {
		maxChars = defaultHintsChars
	}
	var out []string
	usado := 0
	for _, l := range hints {
		if usado+len(l) > maxChars {
			break
		}
		out = append(out, l)
		usado += len(l)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

// HintsStats reporta líneas y chars del puntero (lo usa el doctor).
func HintsStats(maxChars int) (lineas, chars int) {
	texto := HintsPreamble(maxChars)
	if texto == "" {
		return 0, 0
	}
	return strings.Count(texto, "\n"), len(texto)
}
