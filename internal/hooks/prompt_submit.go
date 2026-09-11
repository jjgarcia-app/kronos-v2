package hooks

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/secrets"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// nudgeEveryN prompts without a save triggers a format-reminder nudge.
const nudgeEveryN = 15

// recallTimeoutFallback se usa solo si config.Recall.TimeoutMs viene en 0 —
// no debería pasar (config.Default() ya pone 1500ms), pero un config.json
// editado a mano no puede dejar el hook sin límite de tiempo.
const recallTimeoutFallback = 1500 * time.Millisecond

// trivialPromptPatterns son saludos/charla que no ameritan gastar ni FTS ni
// un embedding — caso real medido: "hola qué hora es" trajo ruido (una
// preferencia sobre "che") en vez de nada, porque tanto FTS como el camino
// vectorial encuentran SIEMPRE el match menos malo cuando no hay nada
// relevante que decir. La lista es corta y explícita a propósito: mejor
// dejar pasar algún saludo raro a FTS/vector que armar un clasificador para
// esto.
var trivialPromptPatterns = []string{
	"hola", "hi", "hey", "gracias", "thanks", "thank you",
	"ok", "okay", "dale", "listo", "buenas",
	"buenos días", "buenas tardes", "buenas noches",
	"qué hora es", "que hora es", "cómo estás", "como estas",
}

// minUsefulWords es el piso de palabras totales por debajo del cual un
// prompt no tiene señal suficiente como para justificar una búsqueda (ej. una
// sola palabra suelta como "dale" o "sqlite"). A propósito NO es 4: consultas
// técnicas cortas reales — "postgres driver" (2 palabras), "alfresco aspect
// remove" (3 palabras), ambas de las mediciones que motivan este cambio (ver
// RecallConfig) — son señal legítima y deben buscar, no filtrarse por
// longitud. El piso solo atrapa prompts de una sola palabra; todo lo demás se
// filtra por trivialPromptPatterns, no por conteo.
const minUsefulWords = 2

// isTrivialPrompt decide si el prompt es charla/saludo sin señal real de
// búsqueda. Dos chequeos independientes, cualquiera alcanza:
//  1. alguna palabra completa del prompt (no substring — "token" no debe
//     matchear el patrón "ok") coincide con un patrón de una sola palabra de
//     trivialPromptPatterns, o el prompt contiene literalmente una de las
//     frases completas (patrones con espacio, ej. "qué hora es").
//  2. tiene menos de minUsefulWords palabras en total.
func isTrivialPrompt(prompt string) bool {
	p := strings.ToLower(strings.TrimSpace(prompt))
	if p == "" {
		return true
	}

	words := strings.Fields(p)
	wordSet := make(map[string]bool, len(words))
	for _, w := range words {
		wordSet[strings.Trim(w, ".,!¡¿?")] = true
	}

	for _, pat := range trivialPromptPatterns {
		if strings.Contains(pat, " ") {
			if strings.Contains(p, pat) {
				return true
			}
		} else if wordSet[pat] {
			return true
		}
	}

	return len(words) < minUsefulWords
}

// RunPromptSubmit handles the UserPromptSubmit hook.
// Saves the prompt, then performs dual-strategy vector+FTS search and emits
// the top relevant (non-duplicate) results to w. w is a parameter (not
// os.Stdout directamente) para que el endpoint HTTP del daemon (ver
// internal/server/prompt_submit.go) pueda capturar la misma salida en un
// buffer y devolverla como respuesta — la lógica es idéntica sea invocada
// como proceso corto (fallback) o vía el daemon compartido.
func RunPromptSubmit(ctx context.Context, in Input, st store.Storer, vs *embeddings.VectorStore, w io.Writer) error {
	if strings.TrimSpace(in.Prompt) == "" {
		return nil
	}

	proj := project.Detect(in.CWD)
	content := secrets.Redact(in.Prompt)

	_ = st.SavePrompt(ctx, in.SessionID, proj.Name, content)
	_ = st.TouchSessionActivity(ctx, in.SessionID, proj.Name)

	runRecall(ctx, in, st, vs, proj.Name, w)

	// Nudge: every nudgeEveryN prompts since the last real save this
	// session, remind the agent to save using the standard format. Counts
	// from the last save, not from session start — a session that saves
	// early and then does a long unsaved stretch afterward still gets
	// nudged (CountSessionObservations == 0 would go silent forever after
	// the first save; see internal/store/store.go).
	// Uses a separate recover to ensure fail-open.
	func() {
		defer func() { _ = recover() }()
		if in.SessionID != "" {
			// Use the concrete *store.Store method if available; otherwise skip nudge.
			type promptCounter interface {
				CountSessionPromptsSinceLastSave(ctx context.Context, sessionID string) int
			}
			if counter, ok := st.(promptCounter); ok {
				n := counter.CountSessionPromptsSinceLastSave(ctx, in.SessionID)
				if n > 0 && n%nudgeEveryN == 0 {
					_, _ = fmt.Fprint(w, memoryNudge(n))
				}
			}
		}
	}()

	return nil
}

// recallItem es una observación candidata a inyectarse, ya resuelta desde
// vector search o FTS — el formateo final (formatRecallBlock) no necesita
// saber de cuál de las dos estrategias vino.
type recallItem struct {
	id      string
	title   string
	typ     string
	content string
}

// runRecall es el corazón de la inyección por relevancia: mide el prompt
// actual contra las observaciones ya guardadas y, si hay algo suficientemente
// parecido, lo escribe en w — sin esperar a que el agente decida llamar
// mem_search. Existe porque medido en producción, mem_search se llamó 60
// veces sobre 9.470 prompts (0,63%): la recuperación no puede depender de que
// el agente se acuerde de buscar.
//
// Estrategia FTS-first: FTS5 responde en milisegundos y es determinístico, así
// que corre siempre primero. El camino vectorial (embeddings vía Ollama, 800ms
// a 6s medidos en esta máquina) solo se intenta si FTS no encontró nada — y
// con el presupuesto de timeout_ms como techo duro, nunca más. Ver
// config.RecallConfig para el detalle de las mediciones que motivan esto.
//
// Fail-open total (recover propio): un panic acá nunca debe tirar abajo
// UserPromptSubmit — en el peor caso, el usuario se queda sin el bloque de
// relevancia para este prompt puntual.
func runRecall(ctx context.Context, in Input, st store.Storer, vs *embeddings.VectorStore, projName string, w io.Writer) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("runRecall: recovered panic", "panic", r)
		}
	}()

	cfg, _ := config.Load()
	rc := cfg.Recall
	if !rc.Enabled {
		return
	}

	// Charla/saludo sin señal real de búsqueda (caso real medido: "hola qué
	// hora es" trajo ruido) — ni FTS ni embeddings valen la pena acá.
	if isTrivialPrompt(in.Prompt) {
		return
	}

	timeout := time.Duration(rc.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = recallTimeoutFallback
	}
	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	k := rc.K
	if k <= 0 {
		k = 3
	}
	minFTSResults := rc.MinFTSResults
	if minFTSResults <= 0 {
		minFTSResults = 1
	}

	injectedIDs, _ := st.LoadInjectedIDs(ctx, in.SessionID)
	injectedSet := make(map[string]bool, len(injectedIDs))
	for _, id := range injectedIDs {
		injectedSet[id] = true
	}

	var items []recallItem
	// picked acumula IDs ya elegidos en ESTE prompt (además de injectedSet,
	// ya inyectados en prompts anteriores de la sesión) — evita que la misma
	// observación se cuente dos veces si matchea tanto por FTS como por
	// vector (solo relevante con min_fts_results > 1, el default 1 nunca
	// llega a correr ambas estrategias sobre la misma observación).
	picked := make(map[string]bool, k)

	// Estrategia 1 (siempre primero): FTS5 sobre las observaciones existentes
	// — sin red, sin LLM, responde en milisegundos. Search ya cubre proyecto +
	// global cuando Scope viene vacío (ver store.SearchParams). FallbackFTS
	// gatea si este camino corre en absoluto (default true; false solo para
	// aislar el camino vectorial en pruebas).
	if rc.FallbackFTS {
		ftsRes, err := st.Search(ctx2, store.SearchParams{
			Query:   in.Prompt,
			Project: projName,
			Limit:   k,
		})
		if err != nil {
			slog.Debug("runRecall: FTS error", "err", err)
		}
		for _, r := range ftsRes {
			id := strconv.FormatInt(r.ID, 10)
			if injectedSet[id] || picked[id] {
				continue
			}
			items = append(items, recallItem{id: id, title: r.Title, typ: string(r.Type), content: r.Content})
			picked[id] = true
			if len(items) >= k {
				break
			}
		}
	}

	// Estrategia 2 (solo si FTS no alcanzó el mínimo): búsqueda vectorial
	// oportunista (embeddings ya existentes en internal/embeddings — Ollama
	// con nomic-embed-text). vs.Similar es nil-safe: si el provider no está
	// disponible, vs viene nil desde el caller y esto simplemente no aporta
	// resultados. El presupuesto de ctx2 (timeout_ms) es el único límite: si
	// se agota acá, se sigue con lo que ya dio FTS (o nada) sin bloquear más.
	if len(items) < minFTSResults && rc.VectorOnFTSMiss && vs != nil {
		sims, err := vs.Similar(ctx2, in.Prompt, k, 0, float32(rc.MinSimilarity))
		if err != nil {
			if ctx2.Err() != nil {
				slog.Debug("runRecall: presupuesto de timeout agotado en el camino vectorial", "timeout_ms", rc.TimeoutMs)
			} else {
				slog.Debug("runRecall: vector search error", "err", err)
			}
		}
		for _, s := range sims {
			id := strconv.FormatInt(s.ObsID, 10)
			if injectedSet[id] || picked[id] {
				continue
			}
			obs, err := st.GetObservation(ctx2, s.ObsID)
			if err != nil || obs == nil {
				continue
			}
			items = append(items, recallItem{id: id, title: obs.Title, typ: string(obs.Type), content: obs.Content})
			picked[id] = true
			if len(items) >= k {
				break
			}
		}
	}

	if len(items) == 0 {
		return
	}

	block, usedIDs := formatRecallBlock(items, rc.CharsLimit)
	if block == "" {
		return
	}
	_, _ = fmt.Fprint(w, block)

	merged := make([]string, 0, len(injectedIDs)+len(usedIDs))
	merged = append(merged, injectedIDs...)
	merged = append(merged, usedIDs...)
	_ = st.PersistInjectedIDs(ctx, in.SessionID, merged)
}

// formatRecallBlock arma el bloque "[kronos:relevante] ..." respetando
// charsLimit — agrega ítems mientras entren en el presupuesto y corta ahí en
// vez de truncar contenido a la mitad, para que lo que se inyecta sea siempre
// legible. Devuelve también los IDs efectivamente incluidos, para que el
// caller los persista como ya-inyectados (y no los repita en el próximo
// prompt de la misma sesión).
func formatRecallBlock(items []recallItem, charsLimit int) (string, []string) {
	if len(items) == 0 {
		return "", nil
	}

	header := func(n int) string {
		return fmt.Sprintf("[kronos:relevante] %d memorias relacionadas con lo que pedís\n", n)
	}
	// Reserva el header más largo posible (todos los ítems) para decidir el
	// presupuesto de las líneas — el header real después achica como mucho
	// un dígito, nunca crece, así que reservar de más es seguro.
	reserved := len(header(len(items)))

	var body strings.Builder
	usedIDs := make([]string, 0, len(items))
	for _, it := range items {
		line := fmt.Sprintf("- %s: %s — %s\n", it.typ, it.title, preview80(it.content))
		if charsLimit > 0 && len(usedIDs) > 0 && reserved+body.Len()+len(line) > charsLimit {
			break // ya hay al menos un ítem; el resto no entra en el presupuesto
		}
		body.WriteString(line)
		usedIDs = append(usedIDs, it.id)
	}
	if len(usedIDs) == 0 {
		return "", nil
	}
	return header(len(usedIDs)) + body.String(), usedIDs
}

// memoryNudge returns the reminder injected into the agent's context.
// Includes the mandatory format so saves are consistent across sessions and agents.
func memoryNudge(turns int) string {
	return fmt.Sprintf(`
[Kronos — recordatorio de memoria, turno %d]
Llevas %d turnos sin guardar nada. Si descubriste algo importante
(decisión, bug resuelto, patrón, configuración), guarda AHORA con mem_save.

Formato obligatorio para el campo content:
  Qué: [qué ocurrió o se decidió]
  Por qué: [motivación, causa o restricción]
  Archivos: [path:línea si aplica, o "N/A"]
  Cómo aplicar: [regla práctica para sesiones futuras]

Reglas de campo:
  title   → "Verbo + qué" corto y buscable
  type    → bugfix | decision | architecture | discovery | pattern | config | preference
  topic_key → OBLIGATORIO si type es decision / architecture / pattern / config
              formato: "area/tema"  ej: "db/postgres-driver"

Si no hay nada relevante que guardar, ignora este mensaje.

`, turns, turns)
}
