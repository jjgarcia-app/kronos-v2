package hooks

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/secrets"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// nudgeEveryN prompts without a save triggers a format-reminder nudge.
const nudgeEveryN = 15

// totalBudgetFallback se usa solo si config.Recall.TotalBudgetMs viene en 0
// (config.Default() ya pone 400ms) — un config.json editado a mano no puede
// dejar la fase vectorial sin presupuesto.
const totalBudgetFallback = 400 * time.Millisecond

// ftsTimeoutFallback se usa solo si config.Recall.FTSTimeoutMs viene en 0
// (config.Default() ya pone 1000ms) — mismo caso que totalBudgetFallback
// pero para la fase FTS. 1000ms: la FTS local mide ~2ms en Postgres, así que
// esto es margen de sobra para una máquina saturada, nunca el límite real en
// la práctica.
const ftsTimeoutFallback = 1000 * time.Millisecond

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
// saber de cuál de las dos estrategias vino. matchedTerms/similarity son el
// criterio de orden final (ver rankAndDedupeRecallItems): se calculan igual
// sea cual sea el origen, así ninguno de los dos caminos gana solo por venir
// de ahí.
type recallItem struct {
	id           string
	title        string
	typ          string
	content      string
	matchedTerms int
	similarity   float64
}

// quotedPhraseRe extrae frases "entre comillas" del prompt — se preservan
// como término de frase exacta en la query OR en vez de partirse palabra por
// palabra (ver buildPromptQuery).
var quotedPhraseRe = regexp.MustCompile(`"([^"]+)"`)

// promptQuery es la consulta FTS armada a partir de un prompt para la
// estrategia "FTS por OR con guarda de precisión" (ver runRecall):
// orTerms ya vienen listos para unir con " OR " (cada uno entre comillas,
// frases completas preservadas); sigTerms es la misma lista en minúsculas,
// usada después para verificar cuántos términos aparecen de verdad en
// título+contenido de cada resultado — el motor FTS ya no lo garantiza una
// vez que la query relaja el AND implícito a OR.
type promptQuery struct {
	orTerms  []string
	sigTerms []string
}

func (q promptQuery) ftsQuery() string {
	return strings.Join(q.orTerms, " OR ")
}

// buildPromptQuery tokeniza el prompt para la query FTS por OR: frases entre
// comillas se preservan enteras; el resto se parte en palabras significativas
// reusando significantTitleTokens (internal/hooks/core_block.go) — mismo
// filtro (minúsculas, ≥4 letras, sin stopwords) que ya usa el dedupe del
// bloque core para títulos, aplicado acá al prompt en vez de a un título.
// Caso real medido (ronda 2 del benchmark): "alfresco aspect remove" (3
// términos) daba 0 filas con el AND implícito de ambos backends porque exige
// los tres en la misma observación; unidos por OR, cada uno entra como
// candidato y countMatchedTerms decide después cuáles matchean lo bastante
// como para no ser ruido.
func buildPromptQuery(prompt string) promptQuery {
	var q promptQuery
	seen := make(map[string]bool)

	rest := prompt
	for _, m := range quotedPhraseRe.FindAllStringSubmatch(prompt, -1) {
		phrase := strings.TrimSpace(m[1])
		if phrase == "" {
			continue
		}
		lower := strings.ToLower(phrase)
		if seen[lower] {
			continue
		}
		seen[lower] = true
		q.orTerms = append(q.orTerms, fmt.Sprintf(`"%s"`, phrase))
		q.sigTerms = append(q.sigTerms, lower)
		rest = strings.Replace(rest, m[0], " ", 1)
	}

	for _, tok := range significantTitleTokens(rest) {
		if seen[tok] {
			continue
		}
		seen[tok] = true
		q.orTerms = append(q.orTerms, fmt.Sprintf(`"%s"`, tok))
		q.sigTerms = append(q.sigTerms, tok)
	}

	return q
}

// minMatchedTermsFor decide cuántos términos deben aparecer de verdad en un
// resultado para contarlo como match real y no ruido de la relajación OR:
// con 3+ términos significativos alcanza que coincidan `configured` (default
// 2, ver config.RecallConfig.MinMatchedTerms); con 1-2 términos ("postgres
// driver") no hay margen para exigir 2, así que con 1 alcanza.
func minMatchedTermsFor(sigTermCount, configured int) int {
	if sigTermCount < 3 {
		return 1
	}
	if configured <= 0 {
		return 2
	}
	return configured
}

// countMatchedTerms cuenta cuántos de terms aparecen (substring, sin
// distinguir mayúsculas) en título+contenido — es la verificación real de
// precisión que reemplaza la garantía que el AND implícito daba gratis: con
// la query relajada a OR, "matcheó por FTS" ya no implica "todos los
// términos están ahí".
func countMatchedTerms(terms []string, title, content string) int {
	haystack := strings.ToLower(title + " " + content)
	n := 0
	for _, t := range terms {
		if t != "" && strings.Contains(haystack, t) {
			n++
		}
	}
	return n
}

// vectorProbeThreshold resuelve config.RecallConfig.VectorProbeMs con su
// default (300ms) — separado para no repetir el fallback en cada punto que
// lo necesita.
func vectorProbeThreshold(rc config.RecallConfig) time.Duration {
	ms := rc.VectorProbeMs
	if ms <= 0 {
		ms = 300
	}
	return time.Duration(ms) * time.Millisecond
}

// promptRecallCacheTTL: misma ventana que embeddings.recallCacheTTL — una
// consulta repetida (reformulación, doble Enter) dentro de este tramo no
// vuelve a pagar ni el round-trip de FTS ni el de embeddings.
const promptRecallCacheTTL = 10 * time.Minute

type promptCacheEntry struct {
	items   []recallItem
	expires time.Time
}

// promptCache guarda, por (proyecto, prompt normalizado), los candidatos que
// ya pasaron su guarda de precisión (FTS con min_matched_terms, vector con
// min_similarity) — SIN el filtro de ya-inyectado, que es por sesión y se
// aplica siempre fresco en runRecall aunque la lista venga de acá. Sin
// límite de tamaño ni desalojo activo: mismo razonamiento que
// embeddings.embedCache — el volumen de prompts distintos por ventana de 10
// minutos es chico, no vale la pena una LRU.
var (
	promptCacheMu sync.Mutex
	promptCache   = make(map[string]promptCacheEntry)
)

// promptCacheKey incluye la dirección del *Store subyacente además de
// proyecto+prompt: promptCache es un mapa global de paquete (mismo patrón que
// embeddings.embedCache), y sin esto dos stores DISTINTOS con el mismo
// proyecto y el mismo prompt (caso real: la suite de tests, que crea un
// store SQLite nuevo por test pero reusa nombres de proyecto y prompts entre
// tests) compartirían resultados que no corresponden — observaciones de un
// store no existen en el otro. En producción hay un solo *Store por proceso
// (corto) o por vida del daemon (compartido), así que esto no cambia nada
// del comportamiento real, solo aísla instancias distintas dentro del mismo
// binario.
func promptCacheKey(st store.Storer, project, prompt string) string {
	return fmt.Sprintf("%p\x00%s\x00%s", st, project, strings.ToLower(strings.TrimSpace(prompt)))
}

func loadPromptCache(key string) ([]recallItem, bool) {
	promptCacheMu.Lock()
	defer promptCacheMu.Unlock()
	e, ok := promptCache[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.items, true
}

func storePromptCache(key string, items []recallItem) {
	promptCacheMu.Lock()
	defer promptCacheMu.Unlock()
	promptCache[key] = promptCacheEntry{items: items, expires: time.Now().Add(promptRecallCacheTTL)}
}

// runRecall es el corazón de la inyección por relevancia: mide el prompt
// actual contra las observaciones ya guardadas y, si hay algo suficientemente
// parecido, lo escribe en w — sin esperar a que el agente decida llamar
// mem_search. Existe porque medido en producción, mem_search se llamó 60
// veces sobre 9.470 prompts (0,63%): la recuperación no puede depender de que
// el agente se acuerde de buscar.
//
// Recalibración (ronda 2 del benchmark): el AND implícito de ambos backends
// FTS hacía fallar justo los prompts técnicos cortos ("alfresco aspect
// remove", 0 filas) y todos los conversacionales, dejando casi todo el peso
// en el camino vectorial (800ms-6s medidos contra Ollama en esta máquina).
// Ahora: (1) la query FTS se arma por OR con una guarda de precisión
// (min_matched_terms verificado contra título+contenido, no lo que reporta
// el motor) en vez de exigir AND; (2) FTS y vector tienen presupuestos
// SEPARADOS (FTSTimeoutMs / TotalBudgetMs, ver gatherRecallCandidates) en vez
// de compartir un mismo deadline; (3) antes de pagar un embedding nuevo, una
// sonda barata (VectorProbeMs) mira cuánto tardó la ÚLTIMA llamada real del
// proveedor y se saltea el intento si viene lento, en vez de arriesgar todo
// el presupuesto en un round-trip que probablemente no vuelva a tiempo. Ver
// config.RecallConfig para el detalle de knobs y mediciones.
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

	// Presupuestos separados por fase (fix real: con la máquina cargada,
	// load 9-10, un deadline COMPARTIDO entre FTS y vector se agotaba
	// durante la FTS misma — barata, ~2ms en Postgres — y el recall volvía
	// vacío aunque la FTS ya tuviera el resultado en la mano). ftsTimeout
	// acota solo st.Search; vectorBudget acota solo el intento vectorial —
	// gatherRecallCandidates les da un context.WithTimeout propio a cada
	// una, así que lo que la FTS ya devolvió nunca se pierde por lo que
	// tarde (o falle) el camino vectorial después.
	ftsTimeout := ftsTimeoutFor(rc)
	vectorBudget := vectorBudgetFor(rc)

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

	pq := buildPromptQuery(in.Prompt)

	// Cache por prompt normalizado (punto 4): FTS y vector ya resueltos para
	// esta consulta+proyecto no se vuelven a pagar dentro de
	// promptRecallCacheTTL. El filtro de ya-inyectado (injectedSet, por
	// sesión — punto 3, persistido también por el arranque de sesión en
	// session_start.go) se aplica SIEMPRE fresco después, así que una misma
	// consulta repetida en la misma sesión no repite items ya mostrados
	// aunque la lista de candidatos venga del cache.
	cacheKey := promptCacheKey(st, projName, in.Prompt)
	candidates, cached := loadPromptCache(cacheKey)
	if !cached {
		candidates = gatherRecallCandidates(ctx, in.Prompt, st, vs, projName, pq, rc, k, minFTSResults, ftsTimeout, vectorBudget)
		storePromptCache(cacheKey, candidates)
	}

	// picked evita contar dos veces la misma observación si aparece tanto en
	// FTS como en vector (relevante sobre todo con min_fts_results > 1).
	picked := make(map[string]bool, len(candidates))
	items := make([]recallItem, 0, len(candidates))
	for _, it := range candidates {
		if injectedSet[it.id] || picked[it.id] {
			continue
		}
		picked[it.id] = true
		items = append(items, it)
	}

	items = rankAndDedupeRecallItemsOpts(items, k, rc.MaxSessionItems)

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

// ftsTimeoutFor resuelve config.RecallConfig.FTSTimeoutMs con su default
// (ftsTimeoutFallback) y el techo de compatibilidad de TimeoutMs (ver
// capByLegacyTimeout) — separado para que runRecall no repita el cálculo.
func ftsTimeoutFor(rc config.RecallConfig) time.Duration {
	t := time.Duration(rc.FTSTimeoutMs) * time.Millisecond
	if t <= 0 {
		t = ftsTimeoutFallback
	}
	return capByLegacyTimeout(t, rc)
}

// vectorBudgetFor resuelve config.RecallConfig.TotalBudgetMs (ahora exclusivo
// de la fase vectorial, ver comentario del campo) con su default
// (totalBudgetFallback) y el mismo techo de compatibilidad que ftsTimeoutFor.
func vectorBudgetFor(rc config.RecallConfig) time.Duration {
	t := time.Duration(rc.TotalBudgetMs) * time.Millisecond
	if t <= 0 {
		t = totalBudgetFallback
	}
	return capByLegacyTimeout(t, rc)
}

// capByLegacyTimeout aplica recall.timeout_ms — el límite compartido de antes
// de separar FTS y vector en presupuestos propios — como techo de
// compatibilidad: quien ya lo tenía configurado más chico que los nuevos
// defaults por fase sigue con ese límite más estricto en cada fase. Nunca
// relaja: si TimeoutMs no está seteado (<=0) o es más laxo que el default de
// la fase, no cambia nada.
func capByLegacyTimeout(phase time.Duration, rc config.RecallConfig) time.Duration {
	legacy := time.Duration(rc.TimeoutMs) * time.Millisecond
	if legacy > 0 && legacy < phase {
		return legacy
	}
	return phase
}

// gatherRecallCandidates ejecuta la estrategia FTS-first + vector oportunista
// y devuelve los candidatos que pasaron su guarda de precisión — SIN el
// filtro de ya-inyectado, que aplica el caller (ver runRecall) fresco en cada
// llamada, incluso cuando esta lista viene del cache por prompt normalizado.
//
// ftsTimeout y vectorBudget son presupuestos INDEPENDIENTES, cada uno con su
// propio context.WithTimeout derivado directamente de ctx (no encadenado uno
// del otro): la regla "lo que ya se tiene, se entrega" se sostiene porque una
// vez que la fase FTS terminó (con resultados o sin ellos), nada de lo que
// pase después en la fase vectorial puede tocar candidates ya agregados —
// están en la misma slice, pero la fase vectorial solo puede AGREGAR, nunca
// truncar lo que ya está.
func gatherRecallCandidates(ctx context.Context, prompt string, st store.Storer, vs *embeddings.VectorStore, projName string, pq promptQuery, rc config.RecallConfig, k, minFTSResults int, ftsTimeout, vectorBudget time.Duration) []recallItem {
	var candidates []recallItem

	// Estrategia 1 (siempre primero): FTS sobre las observaciones existentes
	// — sin red, sin LLM, responde en milisegundos (~2ms medidos en
	// Postgres). Search ya cubre proyecto + global cuando Scope viene vacío
	// (ver store.SearchParams). FallbackFTS gatea si este camino corre en
	// absoluto (default true; false solo para aislar el camino vectorial en
	// pruebas). Sin términos significativos (prompt de puras palabras
	// cortas/stopwords) no hay query que armar — directo al vector.
	if rc.FallbackFTS && len(pq.orTerms) > 0 {
		needed := minMatchedTermsFor(len(pq.sigTerms), rc.MinMatchedTerms)

		ftsCtx, cancel := context.WithTimeout(ctx, ftsTimeout)
		start := time.Now()
		ftsRes, err := st.Search(ftsCtx, store.SearchParams{
			Query:   pq.ftsQuery(),
			Project: projName,
			Limit:   k,
		})
		cancel()
		if err != nil {
			if ftsCtx.Err() != nil {
				slog.Debug("runRecall: fase FTS cortada por timeout",
					"elapsed_ms", time.Since(start).Milliseconds(), "fts_timeout_ms", ftsTimeout.Milliseconds())
			} else {
				slog.Debug("runRecall: FTS error", "err", err)
			}
		}
		for _, r := range ftsRes {
			matched := countMatchedTerms(pq.sigTerms, r.Title, r.Content)
			if matched < needed {
				continue // relajado por OR pero no matcheó lo suficiente — ruido, no resultado
			}
			candidates = append(candidates, recallItem{
				id: strconv.FormatInt(r.ID, 10), title: r.Title, typ: string(r.Type), content: r.Content,
				matchedTerms: matched,
			})
		}
	}

	// Estrategia 2 (solo si FTS no alcanzó el mínimo): búsqueda vectorial
	// oportunista, gateada por su propio presupuesto (independiente del de
	// FTS) y por la sonda de proveedor caliente/frío antes de pagar el
	// round-trip. Lo que candidates ya tiene de la fase FTS queda intacto sea
	// cual sea el resultado de acá — esta fase solo puede agregar.
	if len(candidates) < minFTSResults && rc.VectorOnFTSMiss && vs != nil {
		if last, ok := vs.LastLatency(); ok && last > vectorProbeThreshold(rc) {
			slog.Debug("runRecall: proveedor de embeddings viene lento (sonda), se saltea el intento vectorial",
				"last_latency_ms", last.Milliseconds(), "probe_ms", rc.VectorProbeMs)
			return candidates
		}

		vectorCtx, cancel := context.WithTimeout(ctx, vectorBudget)
		defer cancel()
		start := time.Now()
		sims, err := vs.Similar(vectorCtx, prompt, k, 0, float32(rc.MinSimilarity))
		if err != nil {
			if vectorCtx.Err() != nil {
				slog.Debug("runRecall: fase vectorial cortada por presupuesto",
					"elapsed_ms", time.Since(start).Milliseconds(), "total_budget_ms", vectorBudget.Milliseconds())
			} else {
				slog.Debug("runRecall: vector search error", "err", err)
			}
		}
		seen := make(map[string]bool, len(candidates))
		for _, c := range candidates {
			seen[c.id] = true
		}
		for _, s := range sims {
			id := strconv.FormatInt(s.ObsID, 10)
			if seen[id] {
				continue
			}
			obs, err := st.GetObservation(vectorCtx, s.ObsID)
			if err != nil || obs == nil {
				continue
			}
			candidates = append(candidates, recallItem{
				id: id, title: obs.Title, typ: string(obs.Type), content: obs.Content,
				matchedTerms: countMatchedTerms(pq.sigTerms, obs.Title, obs.Content),
				similarity:   float64(s.Similarity),
			})
		}
	}

	return candidates
}

// rankAndDedupeRecallItems ordena por (términos matcheados, similitud) —
// mismo criterio para candidatos de FTS y de vector, así ninguno le gana al
// otro solo por venir de un camino distinto — y colapsa títulos solapados
// ≥70% en tokens significativos (mismas función y umbral que usa el bloque
// core para deduplicar — ver internal/hooks/core_block.go, titleOverlap /
// titleOverlapThreshold / significantTitleTokens), antes de cortar en k.
func rankAndDedupeRecallItems(items []recallItem, k int) []recallItem {
	return rankAndDedupeRecallItemsOpts(items, k, defaultRecallMaxSessionItems)
}

// defaultRecallMaxSessionItems: cuántos resúmenes de sesión puede inyectar el
// recall por prompt. Uno: sirven para "¿qué veníamos haciendo?", no para
// desplazar el conocimiento real del proyecto.
const defaultRecallMaxSessionItems = 1

// rankAndDedupeRecallItemsOpts es la versión con tope de sesión configurable
// (ver config.RecallConfig.MaxSessionItems). Los items de tipo session van
// SIEMPRE al final, sin importar cuántos términos matcheen: medido contra la
// base real (2026-09-11), type=session era el tipo MÁS inyectado de todos (76
// items, ~19% del total inyectado), y un resumen de sesión que gana por
// matchedTerms tapa un bugfix o una decisión que es lo que el agente necesita.
// Se siguen pudiendo encontrar con mem_search: esto solo ordena/limita la
// inyección automática.
func rankAndDedupeRecallItemsOpts(items []recallItem, k, maxSession int) []recallItem {
	if maxSession <= 0 {
		maxSession = defaultRecallMaxSessionItems
	}
	isSession := func(typ string) bool { return typ == string(store.TypeSession) }

	sort.SliceStable(items, func(i, j int) bool {
		si, sj := isSession(items[i].typ), isSession(items[j].typ)
		if si != sj {
			return !si // el conocimiento real primero
		}
		if items[i].matchedTerms != items[j].matchedTerms {
			return items[i].matchedTerms > items[j].matchedTerms
		}
		return items[i].similarity > items[j].similarity
	})

	kept := make([]recallItem, 0, len(items))
	keptTokens := make([][]string, 0, len(items))
	sessions := 0
	for _, it := range items {
		if isSession(it.typ) {
			if sessions >= maxSession {
				continue
			}
			sessions++
		}
		tokens := significantTitleTokens(it.title)
		dup := false
		for _, kt := range keptTokens {
			if titleOverlap(tokens, kt) >= titleOverlapThreshold {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		kept = append(kept, it)
		keptTokens = append(keptTokens, tokens)
		if len(kept) >= k {
			break
		}
	}
	return kept
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
