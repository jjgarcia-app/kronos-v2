package hooks

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jjgarcia-app/kronos-v2/internal/checkpoint"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	kproject "github.com/jjgarcia-app/kronos-v2/internal/project"
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
// decorado, no memoria útil.
//
// Segunda medición (2026-09-11, proyecto kronos-v2): con el reparto de
// presupuesto ya en su lugar, el bloque seguía trayendo 6-7 items globales
// de ATISA (docs/qa-reports, git-branch-guard.sh, Infisical, antd v5,
// migración de Postgres) que no tienen relación con el proyecto donde se
// está trabajando — se comían la mitad del presupuesto sin aportar nada.
// Confirmado contra la base real: esos items tienen o.Project="atisa-..."
// (ver kproject.Normalize en SaveObservation — un scope=global retiene su
// proyecto de origen, ListObservations solo relaja el filtro WHERE, no lo
// borra). De ahí el filtro de pertinencia (ver classifyGlobalRelevance):
// un global entra si su proyecto de origen es el actual, o si comparte
// pertinencia real con lo que se está trabajando ahora.

// corePoolLimit: cuántas observaciones recientes se traen del store antes de
// clasificarlas por prioridad. Más grande que MaxItems a propósito — una
// observación global o una decisión que importa puede estar enterrada bajo
// varias de tipo passive/session más recientes, y ListObservations solo
// ordena por created_at DESC (no filtra por tipo/scope).
const corePoolLimit = 300

// headerFooterReserve: caracteres reservados para la cabecera (que ahora
// también puede incluir la cláusula "omitidos: ..." — ver assembleCoreBlock)
// y la línea de advertencia sobre items [intent]. La cabecera reporta cuánto
// presupuesto se usó y qué quedó fuera, números que dependen del tamaño
// final del bloque — reservar espacio de antemano evita tener que
// recalcular la selección de items en función de su propia cabecera.
const headerFooterReserve = 250

// Defaults de CoreBlockOptions cuando el caller no especifica (o pasa cero).
const (
	defaultCoreBlockCharsLimit      = 2000
	defaultCoreBlockMaxItems        = 12
	defaultCoreBlockProjectMinChars = 600
	// defaultCoreBlockGlobalsMaxChars: ~30% del presupuesto total por
	// default — medido en producción (proyecto kronos-v2, 2026-09-11): sin
	// este tope, 12 items inyectados / 1483 chars usados terminaban con 6-7
	// globales de OTRO proyecto (ATISA) comiéndose la mitad del bloque.
	defaultCoreBlockGlobalsMaxChars = 600
	// defaultCoreBlockGlobalsMaxItems: tope duro de CANTIDAD de globales,
	// independiente de cuántos chars ocupen — sin esto, muchas globales
	// cortas podían seguir monopolizando la lista de items (MaxItems) aunque
	// entraran cómodas en GlobalsMaxChars.
	defaultCoreBlockGlobalsMaxItems = 4
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

// relevancePositiveTokens: cantidad mínima de tokens significativos
// compartidos (fuera del propio nombre del proyecto — ver classifyGlobalRelevance)
// para que un global de OTRO proyecto de origen se rescate como pertinente.
const relevancePositiveTokens = 2

// relevanceRecentObsLimit: cuántas observaciones de proyecto MÁS RECIENTES
// (pool ya viene ordenado created_at DESC) alimentan projectVocab, usado
// para el rescate de pertinencia positiva. Ver comentario de mediciones en
// buildCoreBlock — acotar a lo reciente (no todo el historial) es lo que
// evita que vocabulario técnico genérico ("pruebas", "postgres", "config")
// rescate globales de otro proyecto por pura coincidencia temática.
const relevanceRecentObsLimit = 8

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
	// GlobalsMaxChars: presupuesto máximo, en chars, para la sección de
	// observaciones scope=global (renderizadas comprimidas). 0 usa el
	// default (ver defaultCoreBlockGlobalsMaxChars).
	GlobalsMaxChars int
	// GlobalsMaxItems: tope máximo de CANTIDAD de observaciones globales,
	// independiente de GlobalsMaxChars. 0 usa el default (ver
	// defaultCoreBlockGlobalsMaxItems).
	GlobalsMaxItems int
	// RelevanceFilter: si true, una observación global solo entra cuando su
	// proyecto de origen es el actual o comparte pertinencia real con él
	// (ver classifyGlobalRelevance) — sin esto, cualquier global pasa el
	// filtro de pertinencia automáticamente (compatibilidad/aislamiento en
	// tests). Igual que IncludeCheckpoint, no tiene "default a true" a nivel
	// de función: el default vive en config.CoreConfig.RelevanceFilter, que
	// printCoreBlock pasa explícito.
	RelevanceFilter bool
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

// CoreBlockMeta trae metadata del armado que no forma parte del texto
// inyectado — hoy solo IDs de items de PROYECTO incluidos (ni checkpoint ni
// globales), consumido por RunSessionStart para que el gate de
// pre-tool-use (ver pre_tool_use.go, gate.satisfied_by_injection) sepa que
// esta sesión ya recibió memoria del proyecto sin depender de que el agente
// llame mem_search.
type CoreBlockMeta struct {
	ProjectItemIDs []string
}

// coreItem es una línea candidata a entrar en el bloque. id es el ID de la
// observación (para deduplicar y para CoreBlockMeta.ProjectItemIDs); 0 para
// el checkpoint, que no es una observación y no compite por deduplicación.
// section clasifica de dónde salió el item ("project"/"global"/"checkpoint")
// — usado para el tope global y para CoreBlockMeta, evitando tener que
// re-derivarlo del obsType (que no alcanza: un item de proyecto y uno
// global pueden compartir tipo). isIntent marca items de tipo
// store.TypeIntent — planes/afirmaciones sin verificar contra el repo (ver
// comentario en store.TypeIntent) — para que assembleCoreBlock sepa cuándo
// agregar la línea de advertencia. raw, si es true, imprime text tal cual
// (sin el prefijo "- "), usado por el checkpoint.
type coreItem struct {
	id       int64
	text     string
	isIntent bool
	raw      bool
	section  string
	// obsType: tipo de la observación de origen, vacío para el checkpoint
	// (raw=true). Usado solo para aplicar el tope por tipo (MaxPerType) —
	// ver filterMaxPerType.
	obsType store.ObservationType
}

// coreOmissions cuenta, por clase, cuántos items candidatos quedaron fuera
// del bloque final — reemplaza el footer genérico "recortado por
// presupuesto" (que no decía NADA sobre qué se perdió) por un reporte
// honesto: cuántos y de qué clase. Ver describe().
type coreOmissions struct {
	// globalsIrrelevant: globales descartadas por el filtro de pertinencia
	// (classifyGlobalRelevance) — nunca llegaron a competir por presupuesto.
	globalsIrrelevant int
	// globalsBudget: globales pertinentes que no entraron por
	// GlobalsMaxChars, GlobalsMaxItems o el MaxItems/presupuesto general.
	globalsBudget int
	// maxPerType: items (de cualquier sección) descartados por el tope de
	// cantidad por tipo (MaxPerType).
	maxPerType int
	// budget: items de proyecto (prioridad o relleno) o de la red de
	// seguridad final que no entraron por presupuesto/MaxItems general.
	budget int
}

func (o coreOmissions) total() int {
	return o.globalsIrrelevant + o.globalsBudget + o.maxPerType + o.budget
}

// describe arma la cláusula "N clase, M clase2" del footer — solo incluye
// clases con al menos un item, en orden fijo (mismo orden en que se aplican
// los filtros: pertinencia, presupuesto de globales, tope por tipo,
// presupuesto general).
func (o coreOmissions) describe() string {
	var parts []string
	if o.globalsIrrelevant > 0 {
		parts = append(parts, fmt.Sprintf("%d globales (poco pertinentes)", o.globalsIrrelevant))
	}
	if o.globalsBudget > 0 {
		parts = append(parts, fmt.Sprintf("%d globales (presupuesto)", o.globalsBudget))
	}
	if o.maxPerType > 0 {
		parts = append(parts, fmt.Sprintf("%d por límite de tipo", o.maxPerType))
	}
	if o.budget > 0 {
		parts = append(parts, fmt.Sprintf("%d por presupuesto", o.budget))
	}
	return strings.Join(parts, ", ")
}

// BuildCoreBlock arma el bloque siempre-presente de contexto para
// SessionStart: a diferencia de injectContinuity (que imprime lo último sin
// criterio de relevancia), este bloque prioriza qué vale la pena que el
// agente vea SIEMPRE, acotado a un presupuesto de caracteres fijo. Envoltorio
// fino sobre buildCoreBlock que descarta el CoreBlockMeta — usar
// BuildCoreBlockWithMeta cuando el caller necesita saber qué IDs de proyecto
// entraron (ver RunSessionStart).
func BuildCoreBlock(ctx context.Context, st store.Storer, project string, opts CoreBlockOptions) (string, error) {
	text, _, err := buildCoreBlock(ctx, st, project, opts)
	return text, err
}

// BuildCoreBlockWithMeta es BuildCoreBlock más CoreBlockMeta (IDs de items
// de proyecto incluidos) — ver CoreBlockMeta.
func BuildCoreBlockWithMeta(ctx context.Context, st store.Storer, project string, opts CoreBlockOptions) (string, CoreBlockMeta, error) {
	return buildCoreBlock(ctx, st, project, opts)
}

// buildCoreBlock hace el trabajo real. Orden de llenado:
//
//  1. checkpoint activo del proyecto — SIEMPRE entra si existe (es "dónde
//     estábamos"), antes que compita nada más por MaxItems/presupuesto.
//  2. preferencias/feedback del proyecto actual
//  3. decisiones/arquitectura del proyecto actual
//  4. observaciones scope=global relevantes (ver classifyGlobalRelevance),
//     comprimidas, hasta GlobalsMaxChars/GlobalsMaxItems
//  5. relleno: las observaciones más recientes del proyecto, si sobra espacio
//
// Los items de proyecto (2-3) entran antes que las globales (4) y nunca se
// sacrifican por ellas — el presupuesto de globales es un tope propio
// (GlobalsMaxChars/GlobalsMaxItems), no algo que le saque lugar al proyecto.
//
// Best-effort en cada paso: un error del store o la ausencia de checkpoint
// no aborta el armado, simplemente esa fuente queda vacía. Nunca devuelve
// error real — el hook que la llama no debe fallar por esto.
func buildCoreBlock(ctx context.Context, st store.Storer, project string, opts CoreBlockOptions) (string, CoreBlockMeta, error) {
	if opts.CharsLimit <= 0 {
		opts.CharsLimit = defaultCoreBlockCharsLimit
	}
	if opts.MaxItems <= 0 {
		opts.MaxItems = defaultCoreBlockMaxItems
	}
	if opts.GlobalsMaxChars <= 0 {
		opts.GlobalsMaxChars = defaultCoreBlockGlobalsMaxChars
	}
	if opts.GlobalsMaxItems <= 0 {
		opts.GlobalsMaxItems = defaultCoreBlockGlobalsMaxItems
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
	normalizedProject := kproject.Normalize(project)

	pool, _ := st.ListObservations(ctx, project, corePoolLimit, 0)
	// Dedupe por solapamiento ANTES de clasificar por prioridad — caso real
	// que motiva esto (ver comentario de curaduría más abajo): sin esto, N
	// observaciones del mismo hilo de trabajo (mismo topic_key, o títulos
	// que en la práctica dicen lo mismo con otras palabras) compiten cada
	// una por un lugar en el bloque como si fueran temas distintos.
	pool = dedupeObservations(pool)

	seen := make(map[int64]bool, len(pool))
	var omissions coreOmissions

	// projectVocab: tokens significativos de los TÍTULOS de las
	// relevanceRecentObsLimit observaciones de proyecto MÁS RECIENTES en pool
	// (que ya viene ordenado created_at DESC — ver ListObservations), usado
	// como "observaciones recientes" para el rescate de pertinencia positiva
	// de globales (ver classifyGlobalRelevance). Dos recortes deliberados,
	// ambos medidos contra la base real (proyecto kronos-v2, 2026-09-11):
	//
	//  1. Solo títulos, no contenido completo — el contenido completo de
	//     kronos-v2 menciona vocabulario común con notas de infraestructura
	//     de OTROS proyectos ("postgres", "sqlite", "docker"), lo que
	//     rescataba de vuelta exactamente los globales que el filtro debía
	//     sacar.
	//  2. Solo lo MÁS RECIENTE, no todo el historial del proyecto — con las
	//     52 observaciones de kronos-v2 completas, una nota real de ATISA
	//     ("SIEMPRE probar en pruebas antes de producción...") compartía 5
	//     tokens ("pruebas", "producción", "código", "datos", "siempre")
	//     contra títulos de kronos-v2 de meses de historial — pura
	//     coincidencia de vocabulario técnico genérico en un proyecto que
	//     además ES la herramienta de memoria (mucho ruido temático propio).
	//     Acotado a lo reciente, esa coincidencia desaparece: es lo más
	//     cercano a "en qué se está trabajando AHORA", que es la señal real
	//     que pide la pertinencia positiva.
	projectVocab := make(map[string]bool)
	recentProjectObs := 0
	for _, o := range pool {
		if o.Scope == store.ScopeGlobal {
			continue
		}
		if recentProjectObs >= relevanceRecentObsLimit {
			break
		}
		recentProjectObs++
		for _, t := range significantTitleTokens(o.Title) {
			projectVocab[t] = true
		}
	}
	// projectNameTokens: tokens del propio nombre del proyecto — EXCLUIDOS
	// del cómputo de pertinencia positiva (ver classifyGlobalRelevance). Caso
	// real medido: kronos-v2 es el proyecto del propio asistente de memoria,
	// así que casi cualquier observación de kronos-v2 menciona literalmente
	// "kronos" — dejar que ese token cuente habría rescatado la nota global
	// real de ATISA "Migrado kronos de SQLite a Postgres..." (comparte
	// "kronos" + "postgres" con los títulos de kronos-v2) a pesar de ser
	// sobre la instalación de OTRO proyecto, no sobre este.
	projectNameTokens := make(map[string]bool)
	for _, t := range significantTitleTokens(project) {
		projectNameTokens[t] = true
	}

	// 0) checkpoint activo — línea propia, formato "> tarea | siguiente:
	// ...". Se resuelve primero y se agrega a `included` sin pasar por
	// addItem: "el checkpoint de la sesión siempre entra" no es negociable
	// contra MaxItems ni contra el presupuesto de los pasos siguientes (ver
	// comentario de buildCoreBlock).
	var included []coreItem
	used := 0
	if opts.IncludeCheckpoint {
		if dataDir, err := platform.DataDir(); err == nil {
			if cp, err := checkpoint.Load(dataDir, project); err == nil && cp != nil {
				text := fmt.Sprintf("> %s | siguiente: %s", cp.Task, cp.NextStep)
				included = append(included, coreItem{text: text, raw: true, section: "checkpoint"})
				used += len(text) + len("\n")
			}
		}
	}

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

	// 3) scope=global, filtradas por pertinencia y comprimidas.
	var globalCandidates []coreItem
	for _, o := range pool {
		if seen[o.ID] {
			continue
		}
		if o.Scope != store.ScopeGlobal {
			continue
		}
		seen[o.ID] = true
		relevant, reason := classifyGlobalRelevance(o, normalizedProject, projectNameTokens, projectVocab, opts.RelevanceFilter)
		if !relevant {
			omissions.globalsIrrelevant++
			slog.Debug("core_block: global omitido por pertinencia", "id", o.ID, "origin_project", o.Project, "project", normalizedProject, "reason", reason)
			continue
		}
		slog.Debug("core_block: global candidato", "id", o.ID, "origin_project", o.Project, "project", normalizedProject, "reason", reason)
		globalCandidates = append(globalCandidates, coreItem{
			id:       o.ID,
			text:     formatObsLineCompressed(o),
			isIntent: o.Type == store.TypeIntent,
			obsType:  o.Type,
			section:  "global",
		})
	}

	// 4) relleno: lo más reciente del proyecto que quedó afuera de 1 y 2.
	// scope=global explícitamente excluido — si una global no entró en el
	// paso 3, no debe colarse acá sin comprimir ni sin pasar el filtro de
	// pertinencia.
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
	var dropped int
	projectPriority, dropped = filterMaxPerType(projectPriority, opts.MaxPerType, typeCounts)
	omissions.maxPerType += dropped
	globalCandidates, dropped = filterMaxPerType(globalCandidates, opts.MaxPerType, typeCounts)
	omissions.maxPerType += dropped
	projectFill, dropped = filterMaxPerType(projectFill, opts.MaxPerType, typeCounts)
	omissions.maxPerType += dropped

	itemBudget := opts.CharsLimit - headerFooterReserve
	if itemBudget < 0 {
		itemBudget = 0
	}

	addItem := func(it coreItem) bool {
		if len(included) >= opts.MaxItems {
			return false
		}
		lineLen := len(it.text) + len("\n")
		if !it.raw {
			lineLen += len("- ")
		}
		if used+lineLen > itemBudget {
			return false
		}
		included = append(included, it)
		used += lineLen
		return true
	}

	// Paso 1-2: preferencias/feedback y decisiones/arquitectura del
	// proyecto — el primer item que no entra corta el resto de la sección
	// (vienen en orden de prioridad; lo que sigue es igual o menos
	// prioritario), y todo lo que quedó sin procesar se cuenta como omitido
	// por presupuesto.
	for i, it := range projectPriority {
		if !addItem(it) {
			omissions.budget += len(projectPriority) - i
			break
		}
	}

	// Paso 3: globales relevantes, con tope propio (GlobalsMaxChars/
	// GlobalsMaxItems) y reserva para el proyecto (ProjectMinChars) si los
	// pasos anteriores no la llenaron todavía.
	projectReserve := opts.ProjectMinChars - used
	if projectReserve < 0 {
		projectReserve = 0
	}
	globalBudget := itemBudget - used - projectReserve
	if globalBudget > opts.GlobalsMaxChars {
		globalBudget = opts.GlobalsMaxChars
	}
	if globalBudget < 0 {
		globalBudget = 0
	}
	globalUsed := 0
	globalIncluded := 0
	for _, it := range globalCandidates {
		if len(included) >= opts.MaxItems || globalIncluded >= opts.GlobalsMaxItems {
			break
		}
		lineLen := len(it.text) + len("- ") + len("\n")
		if globalUsed+lineLen > globalBudget {
			break
		}
		included = append(included, it)
		used += lineLen
		globalUsed += lineLen
		globalIncluded++
	}
	omissions.globalsBudget += len(globalCandidates) - globalIncluded

	// Paso 4: relleno del proyecto con lo que sobre de presupuesto.
	for i, it := range projectFill {
		if !addItem(it) {
			omissions.budget += len(projectFill) - i
			break
		}
	}

	if len(included) == 0 && omissions.total() == 0 {
		return "", CoreBlockMeta{}, nil
	}

	// Red de seguridad: si aun así el bloque final (con cabecera real, que
	// varía con la cuenta de omitidos) supera CharsLimit — proyecto con
	// nombre muy largo, por ejemplo — se sigue recortando desde el final
	// hasta que entre. included[0] es el checkpoint si existe (raw=true,
	// agregado antes que nada más) — se protege de este recorte: "siempre
	// entra" no debe ceder ante un caso límite de presupuesto.
	for {
		block := assembleCoreBlock(included, opts.CharsLimit, project, omissions)
		protectedFloor := 0
		if len(included) > 0 && included[0].raw {
			protectedFloor = 1
		}
		if len(block) <= opts.CharsLimit || len(included) <= protectedFloor {
			return block, coreBlockMetaFrom(included), nil
		}
		included = included[:len(included)-1]
		omissions.budget++
	}
}

// coreBlockMetaFrom extrae CoreBlockMeta de los items finalmente incluidos.
func coreBlockMetaFrom(included []coreItem) CoreBlockMeta {
	var meta CoreBlockMeta
	for _, it := range included {
		if it.section == "project" {
			meta.ProjectItemIDs = append(meta.ProjectItemIDs, fmt.Sprintf("%d", it.id))
		}
	}
	return meta
}

// classifyGlobalRelevance decide si una observación scope=global entra al
// bloque core del proyecto actual. enabled=false (RelevanceFilter apagado)
// deja pasar todo, igual que el comportamiento anterior a esta curaduría —
// existe para no romper callers/tests que arman CoreBlockOptions{} a mano
// sin pensar en pertinencia.
//
// Regla primaria: el proyecto de ORIGEN del item (o.Project — ver comentario
// de mediciones en buildCoreBlock: un scope=global retiene el proyecto que
// lo guardó, ListObservations solo relaja el WHERE, no lo borra) es el
// proyecto actual. Confirmado contra la base real (2026-09-11): las
// observaciones globales que contaminaban kronos-v2 tenían todas
// o.Project="atisa-provider-management-all-in-one" (u otro proyecto),
// nunca "kronos-v2" — es una señal estructural 100% confiable, no hace
// falta parsear texto.
//
// Rescate de pertinencia positiva: si el item (por sus tokens de TÍTULO)
// comparte >= relevancePositiveTokens tokens significativos con el
// vocabulario reciente del proyecto actual (projectVocab, títulos de sus
// observaciones no-globales), entra igual aunque el proyecto de origen sea
// otro — cubre el caso real de un patrón/config genuinamente aplicable
// (spec: "comparte tokens... con el nombre del proyecto actual o con sus
// observaciones recientes"). Los tokens del propio NOMBRE del proyecto se
// excluyen del cómputo (projectNameTokens) — ver comentario de mediciones en
// buildCoreBlock: sin esto, cualquier global que mencione "kronos" (el
// nombre del propio proyecto que se está memorizando) se auto-rescataría.
func classifyGlobalRelevance(o *store.Observation, normalizedProject string, projectNameTokens, projectVocab map[string]bool, enabled bool) (relevant bool, reason string) {
	if !enabled {
		return true, "filtro de pertinencia desactivado"
	}
	if kproject.Normalize(o.Project) == normalizedProject {
		return true, "proyecto de origen coincide"
	}

	itemTokens := significantTitleTokens(o.Title)
	shared := 0
	seenTok := make(map[string]bool, len(itemTokens))
	for _, t := range itemTokens {
		if seenTok[t] || projectNameTokens[t] {
			continue
		}
		seenTok[t] = true
		if projectVocab[t] {
			shared++
		}
	}
	if shared >= relevancePositiveTokens {
		return true, fmt.Sprintf("pertinencia positiva (%d tokens compartidos con el proyecto actual)", shared)
	}
	return false, fmt.Sprintf("proyecto de origen distinto (%s), sin pertinencia positiva", o.Project)
}

// coreItemFromObs arma un coreItem en formato "normal" (tipo + título +
// resumen) a partir de una observación — usado en los pasos de proyecto
// (preferencias/decisiones/relleno).
func coreItemFromObs(o *store.Observation, now time.Time, staleDays, maxItemChars int) coreItem {
	return coreItem{
		id:       o.ID,
		text:     formatObsLine(o, now, staleDays, maxItemChars),
		isIntent: o.Type == store.TypeIntent,
		obsType:  o.Type,
		section:  "project",
	}
}

// filterMaxPerType recorta items una vez alcanzado el tope por tipo,
// preservando el orden de entrada (que ya viene de más a menos prioritario
// dentro de cada sección). counts es compartido entre llamadas sucesivas
// (proyecto → global → relleno) para que el tope aplique al BLOQUE entero,
// no a cada sección por separado. Los items sin tipo (el checkpoint, vía
// obsType vacío) nunca se filtran acá. Devuelve también cuántos items se
// descartaron, para el reporte honesto de omitidos (ver coreOmissions).
//
// Caso real que motiva esto (medido 2026-09-11, proyecto kronos-v2): 6 de 7
// items de proyecto en el bloque eran [architecture] — varias del mismo
// hilo de trabajo del día. Sin este tope, un tipo con mucha actividad
// reciente desplaza a preferencias, decisiones u otros tipos que aportan
// más variedad al perfil del proyecto.
func filterMaxPerType(items []coreItem, maxPerType int, counts map[store.ObservationType]int) ([]coreItem, int) {
	if maxPerType <= 0 || len(items) == 0 {
		return items, 0
	}
	kept := make([]coreItem, 0, len(items))
	dropped := 0
	for _, it := range items {
		if it.obsType == "" {
			kept = append(kept, it)
			continue
		}
		if counts[it.obsType] >= maxPerType {
			dropped++
			continue
		}
		counts[it.obsType]++
		kept = append(kept, it)
	}
	return kept, dropped
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
// dos títulos "dicen lo mismo con otras palabras", y por
// classifyGlobalRelevance para el rescate de pertinencia positiva.
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

// assembleCoreBlock arma el texto final: cabecera (con reporte honesto de
// omitidos, si los hay — ver coreOmissions) + aviso de [intent] (si aplica)
// + items. La cabecera reporta cuántos caracteres se usaron en total — eso
// incluye a la cabecera misma, así que se resuelve por punto fijo (converge
// en 1-2 vueltas: el único motivo por el que cambiaría es que crezca la
// cantidad de dígitos del propio número, o el texto de omitidos).
func assembleCoreBlock(items []coreItem, limit int, project string, omissions coreOmissions) string {
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

	omittedClause := ""
	if omissions.total() > 0 {
		omittedClause = " | omitidos: " + omissions.describe()
	}

	used := len(warning) + len(body)
	var header string
	for i := 0; i < 4; i++ {
		header = fmt.Sprintf("[kronos:core] %d items | %d/%d chars | project %s%s", len(items), used, limit, project, omittedClause)
		total := len(header) + 1 + len(warning) + len(body)
		if total == used {
			break
		}
		used = total
	}

	return header + "\n" + warning + body
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
		line = truncateAtWordBoundary(line, budget)
	}
	return line + suffix
}

// truncateAtWordBoundary recorta s a n chars agregando "..." cuando hace
// falta, retrocediendo hasta el último espacio para no cortar una palabra a
// la mitad — si no hay espacio disponible (una sola palabra larga), corta
// tal cual, que es lo mejor que se puede hacer.
func truncateAtWordBoundary(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	cut := n - 3
	for cut > 0 && s[cut] != ' ' {
		cut--
	}
	if cut == 0 {
		cut = n - 3
	}
	return strings.TrimRight(s[:cut], " ") + "..."
}

// formatObsLineCompressed renderiza una observación scope=global en formato
// comprimido: solo tipo + título, sin "Qué: ...", topeado a
// maxGlobalItemChars. Medido: una línea sin comprimir ocupa ~190 chars — con
// 9 globales eso es el bloque entero (ver comentario de mediciones arriba).
func formatObsLineCompressed(o *store.Observation) string {
	return truncateAtWordBoundary(fmt.Sprintf("[%s] %s", o.Type, o.Title), maxGlobalItemChars)
}

// oneLineSummary reduce content a su primera línea, recortada a maxLen sin
// cortar una palabra a la mitad (ver truncateAtWordBoundary).
func oneLineSummary(content string, maxLen int) string {
	content = strings.TrimSpace(content)
	if nl := strings.IndexAny(content, "\r\n"); nl >= 0 {
		content = strings.TrimSpace(content[:nl])
	}
	if len(content) <= maxLen {
		return content
	}
	return truncateAtWordBoundary(content, maxLen)
}
