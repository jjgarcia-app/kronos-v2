package hooks

import (
	"context"
	"fmt"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/checkpoint"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// Medición real que justifica este archivo: 60 mem_search sobre 9.470
// prompts registrados (0,63%) — pedirle al agente que consulte memoria a
// mano no funciona. El bloque core es la respuesta: inyección automática
// (nadie decide si aparece), presupuesto duro (nunca revienta el prompt),
// y curaduría con regla de desborde (si no entra todo, entra lo prioritario).

// corePoolLimit: cuántas observaciones recientes se traen del store antes de
// clasificarlas por prioridad. Más grande que MaxItems a propósito — una
// observación global o una decisión que importa puede estar enterrada bajo
// varias de tipo passive/session más recientes, y ListObservations solo
// ordena por created_at DESC (no filtra por tipo/scope).
const corePoolLimit = 300

// headerFooterReserve: caracteres reservados para la cabecera y, si aplica,
// el footer de recorte. La cabecera reporta cuánto presupuesto se usó, un
// número que depende del tamaño final del bloque — reservar espacio de
// antemano evita tener que recalcular la selección de items en función de
// su propia cabecera.
const headerFooterReserve = 150

// Defaults de CoreBlockOptions cuando el caller no especifica (o pasa cero).
const (
	defaultCoreBlockCharsLimit = 2000
	defaultCoreBlockMaxItems   = 12
)

// CoreBlockOptions configura el armado del bloque siempre-presente.
type CoreBlockOptions struct {
	CharsLimit        int
	MaxItems          int
	IncludeCheckpoint bool
}

// coreItem es una línea candidata a entrar en el bloque. id es el ID de la
// observación (para deduplicar); 0 para el checkpoint, que no es una
// observación y no compite por deduplicación.
type coreItem struct {
	id   int64
	text string
}

// BuildCoreBlock arma el bloque siempre-presente de contexto para
// SessionStart: a diferencia de injectContinuity (que imprime lo último sin
// criterio de relevancia), este bloque prioriza qué vale la pena que el
// agente vea SIEMPRE, acotado a un presupuesto de caracteres fijo. Orden de
// prioridad, cada uno recorta al que sigue si no alcanza el presupuesto:
//
//  1. observaciones scope=global (patrones reutilizables entre proyectos)
//  2. preference/feedback del proyecto actual (las globales ya entraron en 1)
//  3. decisiones vivas: decision/architecture más recientes del proyecto
//  4. el checkpoint activo del proyecto, en una línea (task + next_step)
//  5. relleno: las observaciones más recientes del proyecto, si sobra espacio
//
// Best-effort en cada paso: un error del store o la ausencia de checkpoint
// no aborta el armado, simplemente esa fuente queda vacía. Nunca devuelve
// error real — el hook que la llama no debe fallar por esto.
func BuildCoreBlock(ctx context.Context, st store.Storer, project string, opts CoreBlockOptions) (string, error) {
	if opts.CharsLimit <= 0 {
		opts.CharsLimit = defaultCoreBlockCharsLimit
	}
	if opts.MaxItems <= 0 {
		opts.MaxItems = defaultCoreBlockMaxItems
	}

	pool, _ := st.ListObservations(ctx, project, corePoolLimit, 0)

	seen := make(map[int64]bool, len(pool))
	var candidates []coreItem

	// 1) scope=global.
	for _, o := range pool {
		if o.Scope == store.ScopeGlobal && !seen[o.ID] {
			candidates = append(candidates, coreItem{id: o.ID, text: formatObsLine(o)})
			seen[o.ID] = true
		}
	}

	// 2) preference/feedback del proyecto (las globales ya quedaron arriba).
	// "feedback" no es un store.ObservationType declarado hoy en el
	// codebase (solo TypePreference existe) — se compara el string igual
	// por si algún día se agrega, sin que este bloque tenga que cambiar.
	for _, o := range pool {
		if seen[o.ID] {
			continue
		}
		if o.Type == store.TypePreference || o.Type == "feedback" {
			candidates = append(candidates, coreItem{id: o.ID, text: formatObsLine(o)})
			seen[o.ID] = true
		}
	}

	// 3) decisiones vivas.
	for _, o := range pool {
		if seen[o.ID] {
			continue
		}
		if o.Type == store.TypeDecision || o.Type == store.TypeArchitecture {
			candidates = append(candidates, coreItem{id: o.ID, text: formatObsLine(o)})
			seen[o.ID] = true
		}
	}

	// 4) checkpoint activo.
	if opts.IncludeCheckpoint {
		if dataDir, err := platform.DataDir(); err == nil {
			if cp, err := checkpoint.Load(dataDir, project); err == nil && cp != nil {
				candidates = append(candidates, coreItem{
					text: fmt.Sprintf("[checkpoint] %s | next: %s", cp.Task, cp.NextStep),
				})
			}
		}
	}

	// 5) relleno con lo más reciente del proyecto.
	for _, o := range pool {
		if seen[o.ID] {
			continue
		}
		candidates = append(candidates, coreItem{id: o.ID, text: formatObsLine(o)})
		seen[o.ID] = true
	}

	if len(candidates) == 0 {
		return "", nil
	}

	// Curaduría por cantidad: nunca más de MaxItems, se pierden los de
	// menor prioridad (van al final de candidates).
	countCapped := candidates
	if len(countCapped) > opts.MaxItems {
		countCapped = countCapped[:opts.MaxItems]
	}
	truncated := len(countCapped) < len(candidates)

	// Curaduría por presupuesto de caracteres: se agregan items completos
	// (nunca se trunca a mitad de uno) hasta agotar el espacio disponible.
	itemBudget := opts.CharsLimit - headerFooterReserve
	if itemBudget < 0 {
		itemBudget = 0
	}
	var included []coreItem
	used := 0
	for _, it := range countCapped {
		lineLen := len(it.text) + len("- ") + len("\n")
		if used+lineLen > itemBudget {
			truncated = true
			break
		}
		included = append(included, it)
		used += lineLen
	}

	if len(included) == 0 {
		return "", nil
	}

	// Red de seguridad: si aun así el bloque final (con cabecera y footer
	// reales) supera CharsLimit — proyecto con nombre muy largo, por
	// ejemplo — se sigue recortando desde el final hasta que entre.
	for {
		block := assembleCoreBlock(included, opts.CharsLimit, project, truncated)
		if len(block) <= opts.CharsLimit || len(included) == 0 {
			return block, nil
		}
		included = included[:len(included)-1]
		truncated = true
	}
}

// assembleCoreBlock arma el texto final: cabecera + items + footer opcional
// de recorte. La cabecera reporta cuántos caracteres se usaron en total —
// eso incluye a la cabecera misma, así que se resuelve por punto fijo (converge
// en 1-2 vueltas: el único motivo por el que cambiaría es que crezca la
// cantidad de dígitos del propio número).
func assembleCoreBlock(items []coreItem, limit int, project string, truncated bool) string {
	lines := make([]string, 0, len(items))
	for _, it := range items {
		lines = append(lines, "- "+it.text)
	}
	body := strings.Join(lines, "\n")

	footer := ""
	if truncated {
		footer = "\n[kronos:core] recortado por presupuesto"
	}

	used := len(body) + len(footer)
	var header string
	for i := 0; i < 4; i++ {
		header = fmt.Sprintf("[kronos:core] %d items | presupuesto usado %d/%d chars | project %s", len(items), used, limit, project)
		total := len(header) + 1 + len(body) + len(footer)
		if total == used {
			break
		}
		used = total
	}

	return header + "\n" + body + footer
}

// formatObsLine renderiza una observación como una línea del bloque: tipo +
// título + resumen de una sola línea (nunca el contenido completo).
func formatObsLine(o *store.Observation) string {
	return fmt.Sprintf("[%s] %s: %s", o.Type, o.Title, oneLineSummary(o.Content, 90))
}

// oneLineSummary reduce content a su primera línea, recortada a maxLen.
func oneLineSummary(content string, maxLen int) string {
	content = strings.TrimSpace(content)
	if nl := strings.IndexAny(content, "\r\n"); nl >= 0 {
		content = strings.TrimSpace(content[:nl])
	}
	if len(content) <= maxLen {
		return content
	}
	return content[:maxLen-3] + "..."
}
