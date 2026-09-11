package hooks

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jjgarcia-app/kronos-v2/internal/checkpoint"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// Medición real que justifica este archivo: 60 mem_search sobre 9.470
// prompts registrados (0,63%) — pedirle al agente que consulte memoria a
// mano no funciona. El bloque core es la respuesta: inyección automática
// (nadie decide si aparece), presupuesto duro (nunca revienta el prompt),
// y curaduría con regla de desborde (si no entra todo, entra lo prioritario).
//
// Reparto de presupuesto (benchmark 2026-09-11, 8 sesiones reales): en las
// 7 sesiones donde se inyectó, el bloque siempre terminó en ~1904/2000 chars
// y las 9 items eran TODAS scope=global — preferencias/feedback de OTRO
// proyecto (ATISA). El conocimiento propio del proyecto activo nunca entró:
// salió por injectContinuity (preview de 80 chars), y en la única sesión
// donde eso "funcionó" fue de casualidad (el título de la observación traía
// la respuesta). Un bloque siempre-inyectado que solo trae contexto ajeno es
// decorado, no memoria útil. De ahí max_global_chars (tope duro a lo global,
// comprimido) y project_min_chars (reserva para lo propio) más abajo.

// corePoolLimit: cuántas observaciones recientes se traen del store antes de
// clasificarlas por prioridad. Más grande que MaxItems a propósito — una
// observación global o una decisión que importa puede estar enterrada bajo
// varias de tipo passive/session más recientes, y ListObservations solo
// ordena por created_at DESC (no filtra por tipo/scope).
const corePoolLimit = 300

// headerFooterReserve: caracteres reservados para la cabecera y, si aplica,
// el footer de recorte y la línea de advertencia sobre items [intent]. La
// cabecera reporta cuánto presupuesto se usó, un número que depende del
// tamaño final del bloque — reservar espacio de antemano evita tener que
// recalcular la selección de items en función de su propia cabecera.
const headerFooterReserve = 250

// Defaults de CoreBlockOptions cuando el caller no especifica (o pasa cero).
const (
	defaultCoreBlockCharsLimit      = 2000
	defaultCoreBlockMaxItems        = 12
	defaultCoreBlockMaxGlobalChars  = 800
	defaultCoreBlockProjectMinChars = 600
	// defaultCoreBlockMaxPerType: ver comentario de curaduría más abajo —
	// caso real medido (proyecto kronos-v2, 2026-09-11): 6 de 7 items de
	// proyecto en el bloque eran [architecture], varios del mismo hilo de
	// trabajo del día.
	defaultCoreBlockMaxPerType   = 3
	defaultCoreBlockMaxItemChars = 110
	defaultCoreBlockStaleDays    = 90
)

// titleOverlapThreshold: fracción mínima de tokens significativos
// compartidos (respecto del título más chico) para considerar dos
// observaciones el mismo tema. Ver dedupeObservations.
const titleOverlapThreshold = 0.70

// maxGlobalItemChars: tope por item comprimido (tipo + título, sin "Qué:
// ..."). Medido: una observación global sin comprimir ocupa ~190 chars —
// con 9 de esas ya no queda lugar para nada del proyecto. Comprimida, cada
// una entra en una fracción de eso.
const maxGlobalItemChars = 120

// CoreBlockOptions configura el armado del bloque siempre-presente.
type CoreBlockOptions struct {
	CharsLimit        int
	MaxItems          int
	IncludeCheckpoint bool
	// MaxGlobalChars: presupuesto máximo, en chars, para la sección de
	// observaciones scope=global (renderizadas comprimidas). 0 usa el
	// default (ver defaultCoreBlockMaxGlobalChars).
	MaxGlobalChars int
	// ProjectMinChars: reserva mínima, en chars, para contenido del
	// proyecto actual (preferencias/feedback, decisiones/arquitectura,
	// checkpoint y relleno reciente). No es un piso garantizado si el
	// proyecto no tiene tanto contenido — es un tope a cuánto puede
	// consumir la sección global antes de dejarle lugar al relleno del
	// proyecto (paso 5). 0 usa el default (ver defaultCoreBlockProjectMinChars).
	ProjectMinChars int
	// MaxPerType: tope de items por tipo en TODO el bloque (no por sección).
	// Sin esto, un tipo con muchas observaciones recientes (ej.
	// [architecture]) puede monopolizar el bloque entero — caso real
	// medido: 6 de 7 items de proyecto eran [architecture] del mismo hilo
	// de trabajo. 0 usa el default (ver defaultCoreBlockMaxPerType).
	MaxPerType int
	// MaxItemChars: tope de caracteres por línea de item ("[tipo] título —
	// resumen"), aparte del tope de 90 chars del resumen en sí. 0 usa el
	// default (ver defaultCoreBlockMaxItemChars).
	MaxItemChars int
	// StaleDays: umbral de días sin actualización a partir del cual una
	// decisión/arquitectura se marca "(antiguo)" en su línea — el bloque no
	// distingue hoy entre una decisión vigente y una de hace meses que
	// puede haber cambiado. 0 usa el default (ver defaultCoreBlockStaleDays).
	StaleDays int
}

// coreItem es una línea candidata a entrar en el bloque. id es el ID de la
// observación (para deduplicar); 0 para el checkpoint, que no es una
// observación y no compite por deduplicación. isIntent marca items de tipo
// store.TypeIntent — planes/afirmaciones sin verificar contra el repo (ver
// comentario en store.TypeIntent) — para que assembleCoreBlock sepa cuándo
// agregar la línea de advertencia. raw, si es true, imprime text tal cual
// (sin el prefijo "- "), usado por el checkpoint.
type coreItem struct {
	id       int64
	text     string
	isIntent bool
	raw      bool
	// obsType: tipo de la observación de origen, vacío para el checkpoint
	// (raw=true). Usado solo para aplicar el tope por tipo (MaxPerType) —
	// ver filterMaxPerType.
	obsType store.ObservationType
}

// BuildCoreBlock arma el bloque siempre-presente de contexto para
// SessionStart: a diferencia de injectContinuity (que imprime lo último sin
// criterio de relevancia), este bloque prioriza qué vale la pena que el
// agente vea SIEMPRE, acotado a un presupuesto de caracteres fijo.
//
// Orden de llenado (reemplaza al anterior "global primero" — ver comentario
// de mediciones arriba: eso dejaba el bloque 100% ocupado por otros
// proyectos):
//
//  1. preferencias/feedback del proyecto actual
//  2. decisiones/arquitectura del proyecto actual
//  3. el checkpoint activo del proyecto — lo más accionable que hay, antes
//     quedaba al final y nunca entraba
//  4. observaciones scope=global, comprimidas, hasta MaxGlobalChars
//  5. relleno: las observaciones más recientes del proyecto, si sobra espacio
//
// ProjectMinChars limita cuánto puede gastar el paso 4 cuando los pasos 1-3
// no llenaron todavía esa reserva, dejando lugar para el paso 5.
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
	if opts.MaxGlobalChars <= 0 {
		opts.MaxGlobalChars = defaultCoreBlockMaxGlobalChars
	}
	if opts.ProjectMinChars <= 0 {
		opts.ProjectMinChars = defaultCoreBlockProjectMinChars
	}
	if opts.MaxPerType <= 0 {
		opts.MaxPerType = defaultCoreBlockMaxPerType
	}
	if opts.MaxItemChars <= 0 {
		opts.MaxItemChars = defaultCoreBlockMaxItemChars
	}
	if opts.StaleDays <= 0 {
		opts.StaleDays = defaultCoreBlockStaleDays
	}

	now := time.Now()

	pool, _ := st.ListObservations(ctx, project, corePoolLimit, 0)
	// Dedupe por solapamiento ANTES de clasificar por prioridad — caso real
	// que motiva esto (ver comentario de curaduría más abajo): sin esto, N
	// observaciones del mismo hilo de trabajo (mismo topic_key, o títulos
	// que en la práctica dicen lo mismo con otras palabras) compiten cada
	// una por un lugar en el bloque como si fueran temas distintos.
	pool = dedupeObservations(pool)

	seen := make(map[int64]bool, len(pool))

	// 1) preferencias/feedback del proyecto actual. "feedback" no es un
	// store.ObservationType declarado hoy en el codebase (solo
	// TypePreference existe) — se compara el string igual por si algún día
	// se agrega, sin que este bloque tenga que cambiar.
	var projectPriority []coreItem
	for _, o := range pool {
		if o.Scope != store.ScopeGlobal && !seen[o.ID] && (o.Type == store.TypePreference || o.Type == "feedback") {
			projectPriority = append(projectPriority, coreItemFromObs(o, now, opts.StaleDays, opts.MaxItemChars))
			seen[o.ID] = true
		}
	}

	// 2) decisiones/arquitectura del proyecto actual.
	for _, o := range pool {
		if seen[o.ID] {
			continue
		}
		if o.Scope != store.ScopeGlobal && (o.Type == store.TypeDecision || o.Type == store.TypeArchitecture) {
			projectPriority = append(projectPriority, coreItemFromObs(o, now, opts.StaleDays, opts.MaxItemChars))
			seen[o.ID] = true
		}
	}

	// 3) checkpoint activo — línea propia, formato "> tarea | siguiente: ...".
	var checkpointItem *coreItem
	if opts.IncludeCheckpoint {
		if dataDir, err := platform.DataDir(); err == nil {
			if cp, err := checkpoint.Load(dataDir, project); err == nil && cp != nil {
				checkpointItem = &coreItem{
					text: fmt.Sprintf("> %s | siguiente: %s", cp.Task, cp.NextStep),
					raw:  true,
				}
			}
		}
	}

	// 4) scope=global, comprimidas.
	var globalItems []coreItem
	for _, o := range pool {
		if seen[o.ID] {
			continue
		}
		if o.Scope == store.ScopeGlobal {
			globalItems = append(globalItems, coreItem{
				id:       o.ID,
				text:     formatObsLineCompressed(o),
				isIntent: o.Type == store.TypeIntent,
				obsType:  o.Type,
			})
			seen[o.ID] = true
		}
	}

	// 5) relleno: lo más reciente del proyecto que quedó afuera de 1 y 2.
	// scope=global explícitamente excluido — si una global no entró en el
	// paso 4 (por MaxGlobalChars), no debe colarse acá sin comprimir.
	var projectFill []coreItem
	for _, o := range pool {
		if seen[o.ID] {
			continue
		}
		if o.Scope == store.ScopeGlobal {
			continue
		}
		projectFill = append(projectFill, coreItemFromObs(o, now, opts.StaleDays, opts.MaxItemChars))
		seen[o.ID] = true
	}

	// Tope por tipo: se aplica en orden de prioridad (proyecto antes que
	// global, relleno al final) sobre TODO el bloque, no por sección — un
	// contador compartido evita que, por ejemplo, 3 [architecture] de
	// proyecto más 3 [architecture] globales sumen 6 items del mismo tipo.
	typeCounts := make(map[store.ObservationType]int, 4)
	projectPriority = filterMaxPerType(projectPriority, opts.MaxPerType, typeCounts)
	globalItems = filterMaxPerType(globalItems, opts.MaxPerType, typeCounts)
	projectFill = filterMaxPerType(projectFill, opts.MaxPerType, typeCounts)

	itemBudget := opts.CharsLimit - headerFooterReserve
	if itemBudget < 0 {
		itemBudget = 0
	}

	var included []coreItem
	used := 0
	truncated := false

	addItem := func(it coreItem) bool {
		if len(included) >= opts.MaxItems {
			truncated = true
			return false
		}
		lineLen := len(it.text) + len("\n")
		if !it.raw {
			lineLen += len("- ")
		}
		if used+lineLen > itemBudget {
			truncated = true
			return false
		}
		included = append(included, it)
		used += lineLen
		return true
	}

	// Pasos 1-2: preferencias/feedback y decisiones/arquitectura del proyecto.
	for _, it := range projectPriority {
		if !addItem(it) {
			break
		}
	}

	// Paso 3: checkpoint — lo más accionable que hay, no puede quedar afuera
	// solo porque hubo muchas preferencias/decisiones antes. addItem ya
	// marca truncated si no entra por MaxItems o presupuesto.
	if checkpointItem != nil {
		addItem(*checkpointItem)
	}

	// Paso 4: globales, comprimidas, con tope propio (MaxGlobalChars) y
	// reserva para el proyecto (ProjectMinChars) si los pasos 1-3 no la
	// llenaron todavía.
	projectReserve := opts.ProjectMinChars - used
	if projectReserve < 0 {
		projectReserve = 0
	}
	globalBudget := itemBudget - used - projectReserve
	if globalBudget > opts.MaxGlobalChars {
		globalBudget = opts.MaxGlobalChars
	}
	if globalBudget < 0 {
		globalBudget = 0
	}
	globalUsed := 0
	globalIncluded := 0
	for _, it := range globalItems {
		if len(included) >= opts.MaxItems {
			truncated = true
			break
		}
		lineLen := len(it.text) + len("- ") + len("\n")
		if globalUsed+lineLen > globalBudget {
			truncated = true
			break
		}
		included = append(included, it)
		used += lineLen
		globalUsed += lineLen
		globalIncluded++
	}
	if globalIncluded < len(globalItems) {
		truncated = true
	}

	// Paso 5: relleno del proyecto con lo que sobre de presupuesto.
	for _, it := range projectFill {
		if !addItem(it) {
			break
		}
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

// coreItemFromObs arma un coreItem en formato "normal" (tipo + título +
// resumen) a partir de una observación — usado en todos los pasos salvo el
// de globales comprimidas y el checkpoint.
func coreItemFromObs(o *store.Observation, now time.Time, staleDays, maxItemChars int) coreItem {
	return coreItem{
		id:       o.ID,
		text:     formatObsLine(o, now, staleDays, maxItemChars),
		isIntent: o.Type == store.TypeIntent,
		obsType:  o.Type,
	}
}

// filterMaxPerType recorta items una vez alcanzado el tope por tipo,
// preservando el orden de entrada (que ya viene de más a menos prioritario
// dentro de cada sección). counts es compartido entre llamadas sucesivas
// (proyecto → global → relleno) para que el tope aplique al BLOQUE entero,
// no a cada sección por separado. Los items sin tipo (el checkpoint, vía
// obsType vacío) nunca se filtran acá.
//
// Caso real que motiva esto (medido 2026-09-11, proyecto kronos-v2): 6 de 7
// items de proyecto en el bloque eran [architecture] — varias del mismo
// hilo de trabajo del día. Sin este tope, un tipo con mucha actividad
// reciente desplaza a preferencias, decisiones u otros tipos que aportan
// más variedad al perfil del proyecto.
func filterMaxPerType(items []coreItem, maxPerType int, counts map[store.ObservationType]int) []coreItem {
	if maxPerType <= 0 || len(items) == 0 {
		return items
	}
	kept := make([]coreItem, 0, len(items))
	for _, it := range items {
		if it.obsType == "" {
			kept = append(kept, it)
			continue
		}
		if counts[it.obsType] >= maxPerType {
			continue
		}
		counts[it.obsType]++
		kept = append(kept, it)
	}
	return kept
}

// titleStopwords: palabras de ≥4 letras sin peso temático para el cálculo
// de solapamiento de títulos (dedupeObservations) — sin esto, dos títulos
// distintos que comparten "para", "como" o "desde" contarían como si
// hablaran del mismo tema.
var titleStopwords = map[string]bool{
	"para": true, "como": true, "pero": true, "esta": true, "esto": true,
	"este": true, "estas": true, "estos": true, "desde": true, "hasta": true,
	"sobre": true, "entre": true, "cuando": true, "donde": true, "cada": true,
	"toda": true, "todo": true, "todos": true, "todas": true, "más": true,
	"menos": true, "solo": true, "sólo": true, "tiene": true, "tienen": true,
	"hace": true, "hizo": true, "fue": true, "están": true, "otro": true,
	"otra": true, "otros": true, "otras": true, "también": true, "porque": true,
	"aunque": true, "mientras": true, "antes": true, "luego": true, "tras": true,
	"sido": true,
}

// significantTitleTokens normaliza un título a sus tokens con peso
// temático real: minúsculas, separado por cualquier caracter no-letra, ≥4
// letras (en runas, no bytes — "más"/"según" no deben cortarse a mitad de
// una tilde), sin stopwords. Usado por dedupeObservations para decidir si
// dos títulos "dicen lo mismo con otras palabras".
func significantTitleTokens(title string) []string {
	fields := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r)
	})
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		if utf8.RuneCountInString(f) < 4 {
			continue
		}
		if titleStopwords[f] {
			continue
		}
		tokens = append(tokens, f)
	}
	return tokens
}

// titleOverlap calcula qué fracción de los tokens del título más chico
// también aparece en el más grande (coeficiente de solapamiento, no
// Jaccard — dos títulos donde uno es un superconjunto casi exacto del otro
// deben marcarse como el mismo tema aunque el más largo tenga tokens
// extra). 0 si algún título no aportó tokens significativos.
func titleOverlap(a, b []string) float64 {
	setA, setB := toTokenSet(a), toTokenSet(b)
	if len(setA) == 0 || len(setB) == 0 {
		return 0
	}
	shared := 0
	for t := range setA {
		if setB[t] {
			shared++
		}
	}
	smaller := len(setA)
	if len(setB) < smaller {
		smaller = len(setB)
	}
	return float64(shared) / float64(smaller)
}

func toTokenSet(tokens []string) map[string]bool {
	set := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		set[t] = true
	}
	return set
}

// isNewerObservation decide, entre dos observaciones consideradas el mismo
// tema, cuál queda: la de UpdatedAt más reciente y, en empate exacto, la de
// mayor RevisionCount (más veces confirmada/actualizada).
func isNewerObservation(a, b *store.Observation) bool {
	if !a.UpdatedAt.Equal(b.UpdatedAt) {
		return a.UpdatedAt.After(b.UpdatedAt)
	}
	return a.RevisionCount > b.RevisionCount
}

// dedupeObservations colapsa observaciones que comparten topic_key (no
// vacío) o cuyos títulos solapan ≥titleOverlapThreshold en tokens
// significativos — mismo tema contado dos veces no es curaduría, es un log.
// Caso real (proyecto kronos-v2, 2026-09-11): "Reparto de presupuesto core
// (proyecto antes que global) y tipo intent sin verificar" y "Bloque core:
// presupuesto repartido proyecto/global, checkpoint siempre incluido y tipo
// intent" son la MISMA decisión contada dos veces con otras palabras — sin
// este dedupe, competían por dos lugares distintos en el bloque.
//
// Corre sobre el pool completo (antes de clasificar por prioridad/scope)
// porque el tema puede repetirse cruzando proyecto/global — ej. una
// observación global de otro proyecto y una del proyecto actual con el
// mismo topic_key (topic_key es único por proyecto en el store, no
// globalmente). Preserva el resto del orden de pool (created_at DESC); el
// desempate entre duplicados usa UpdatedAt/RevisionCount, no la posición.
func dedupeObservations(pool []*store.Observation) []*store.Observation {
	kept := make([]*store.Observation, 0, len(pool))
	keptTokens := make([][]string, 0, len(pool))

	for _, o := range pool {
		oTokens := significantTitleTokens(o.Title)
		dupIdx := -1
		for i, k := range kept {
			if o.TopicKey != "" && o.TopicKey == k.TopicKey {
				dupIdx = i
				break
			}
			if titleOverlap(oTokens, keptTokens[i]) >= titleOverlapThreshold {
				dupIdx = i
				break
			}
		}
		if dupIdx == -1 {
			kept = append(kept, o)
			keptTokens = append(keptTokens, oTokens)
			continue
		}
		if isNewerObservation(o, kept[dupIdx]) {
			kept[dupIdx] = o
			keptTokens[dupIdx] = oTokens
		}
	}
	return kept
}

// quePrefixRe: encabezado "Qué:"/"What:" que mem_save usa como primera
// línea del contenido estructurado (What/Why/Where/Learned). El bloque core
// muestra un resumen de una línea, no el contenido completo — mostrar el
// literal "Qué:" ahí es ruido, no información.
var quePrefixRe = regexp.MustCompile(`(?i)^\s*(qué|que|what)\s*:\s*`)

func stripQuePrefix(content string) string {
	return quePrefixRe.ReplaceAllString(content, "")
}

// isStaleDecision marca decisiones/arquitectura sin actualizar hace más de
// staleDays — el bloque no distingue hoy entre una decisión vigente y una
// tomada hace meses que puede haber cambiado; el sufijo "(antiguo)" se lo
// dice al agente sin necesidad de verificación automática contra el repo.
func isStaleDecision(o *store.Observation, now time.Time, staleDays int) bool {
	if o.Type != store.TypeDecision && o.Type != store.TypeArchitecture {
		return false
	}
	if staleDays <= 0 {
		return false
	}
	return now.Sub(o.UpdatedAt) > time.Duration(staleDays)*24*time.Hour
}

// intentWarning: cabecera que se agrega UNA sola vez cuando el bloque
// incluye al menos un item [intent]. Motivada por un caso real del
// benchmark 2026-09-11: en una sesión el agente encontró una observación
// guardada, la citó, y — correctamente — se negó a darla por buena porque
// contradecía el filesystem ("no lo voy a dar por bueno solo porque está en
// memoria"). Kronos hoy no distingue intención/plan de hecho verificado; sin
// esta marca, un plan dicho una vez ("el comando de release es X") se ve
// idéntico a un bugfix confirmado contra el código. No hay verificación
// automática — alcanza con que quien lee el bloque sepa qué es qué.
const intentWarning = "[kronos:core] los items [intent] son planes o afirmaciones sin verificar — confirmalos contra el repo antes de darlos por ciertos"

// assembleCoreBlock arma el texto final: cabecera + aviso de [intent] (si
// aplica) + items + footer opcional de recorte. La cabecera reporta cuántos
// caracteres se usaron en total — eso incluye a la cabecera misma, así que
// se resuelve por punto fijo (converge en 1-2 vueltas: el único motivo por
// el que cambiaría es que crezca la cantidad de dígitos del propio número).
func assembleCoreBlock(items []coreItem, limit int, project string, truncated bool) string {
	lines := make([]string, 0, len(items))
	hasIntent := false
	for _, it := range items {
		if it.isIntent {
			hasIntent = true
		}
		if it.raw {
			lines = append(lines, it.text)
		} else {
			lines = append(lines, "- "+it.text)
		}
	}
	body := strings.Join(lines, "\n")

	warning := ""
	if hasIntent {
		warning = intentWarning + "\n"
	}

	footer := ""
	if truncated {
		footer = "\n[kronos:core] recortado por presupuesto"
	}

	used := len(warning) + len(body) + len(footer)
	var header string
	for i := 0; i < 4; i++ {
		header = fmt.Sprintf("[kronos:core] %d items | presupuesto usado %d/%d chars | project %s", len(items), used, limit, project)
		total := len(header) + 1 + len(warning) + len(body) + len(footer)
		if total == used {
			break
		}
		used = total
	}

	return header + "\n" + warning + body + footer
}

// formatObsLine renderiza una observación como UNA línea densa del bloque:
// "[tipo] título — resumen", sin el literal "Qué: ..." que trae el
// contenido estructurado de mem_save (ver stripQuePrefix), y topeada a
// maxItemChars en total. El sufijo "(antiguo)" — si aplica (ver
// isStaleDecision) — se reserva ANTES de truncar el resto de la línea, para
// que nunca quede cortado a la mitad.
func formatObsLine(o *store.Observation, now time.Time, staleDays, maxItemChars int) string {
	summary := oneLineSummary(stripQuePrefix(o.Content), 90)
	line := fmt.Sprintf("[%s] %s — %s", o.Type, o.Title, summary)

	suffix := ""
	if isStaleDecision(o, now, staleDays) {
		suffix = " (antiguo)"
	}

	budget := maxItemChars - len(suffix)
	if budget < 0 {
		budget = 0
	}
	if len(line) > budget {
		line = truncateChars(line, budget)
	}
	return line + suffix
}

// truncateChars recorta s a n chars agregando "..." cuando hace falta —
// mismo criterio que ya usaba formatObsLineCompressed, factorizado porque
// ahora formatObsLine también lo necesita.
func truncateChars(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

// formatObsLineCompressed renderiza una observación scope=global en formato
// comprimido: solo tipo + título, sin "Qué: ...", topeado a
// maxGlobalItemChars. Medido: una línea sin comprimir ocupa ~190 chars — con
// 9 globales eso es el bloque entero (ver comentario de mediciones arriba).
func formatObsLineCompressed(o *store.Observation) string {
	return truncateChars(fmt.Sprintf("[%s] %s", o.Type, o.Title), maxGlobalItemChars)
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
