package hooks_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/checkpoint"
	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// setTempConfigDir redirige config.ConfigPath a un directorio temporal —
// mismo patrón que internal/config/config_test.go, duplicado acá porque
// vive en otro paquete de test.
func setTempConfigDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", dir)
	} else {
		t.Setenv("XDG_CONFIG_HOME", dir)
		t.Setenv("HOME", dir)
	}
	_ = os.MkdirAll(filepath.Join(dir, "kronos"), 0755)
}

func saveObs(t *testing.T, st store.Storer, typ store.ObservationType, scope store.Scope, project, title, content string) *store.Observation {
	t.Helper()
	obs, err := st.SaveObservation(context.Background(), store.SaveParams{
		Type:    typ,
		Title:   title,
		Content: content,
		Project: project,
		Scope:   scope,
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
	return obs
}

// distinctFillerWords: vocabulario para generar títulos de relleno con
// solapamiento de tokens bajo (ver internal/hooks/core_block.go,
// titleOverlapThreshold) — usar solo un número como diferenciador ("item
// %d") no sirve: los dígitos no son letras, así que significantTitleTokens
// los descarta y 20 títulos "Hallazgo N" quedan con el MISMO set de tokens,
// lo que el dedupe nuevo colapsaría a uno solo sin querer.
var distinctFillerWords = []string{
	"alfa", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta",
	"iota", "kappa", "lambda", "sigma", "omega", "pluma", "roble", "nube",
	"fuego", "arena", "hielo", "piedra", "rio", "monte", "valle", "bosque",
}

func fillerWord(i int) string {
	return distinctFillerWords[i%len(distinctFillerWords)]
}

// (a) el presupuesto se respeta: con CharsLimit chico, el bloque resultante
// no lo supera y avisa que fue recortado.
func TestBuildCoreBlock_RespectsCharsBudget(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		title := fmt.Sprintf("Hallazgo largo sobre %s", fillerWord(i))
		saveObs(t, st, store.TypeDiscovery, store.ScopeProject, "proyecto-x",
			title, strings.Repeat(fmt.Sprintf("contenido de relleno %d ", i), 20))
	}

	limit := 400
	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{CharsLimit: limit, MaxItems: 20})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if len(block) > limit {
		t.Errorf("bloque de %d chars supera el límite de %d", len(block), limit)
	}
	if !strings.Contains(block, "omitidos:") {
		t.Errorf("esperaba el reporte honesto de omitidos, bloque:\n%s", block)
	}
}

// (b) la prioridad se respeta: preferencia del proyecto y observación global
// entran, y se cae lo menos prioritario (relleno reciente del proyecto, que
// desde el reparto de presupuesto de core.globals_max_chars/project_min_chars
// pasó a ser el ÚLTIMO paso de llenado, no el primero). MaxItems: 2 fuerza el
// corte independientemente de la aritmética de chars — así el test no
// depende de los defaults de GlobalsMaxChars/ProjectMinChars.
func TestBuildCoreBlock_PrioritizesGlobalAndPreference(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	saveObs(t, st, store.TypePattern, store.ScopeGlobal, "proyecto-x", "Patron global reutilizable", "este patron aplica a cualquier proyecto")
	saveObs(t, st, store.TypePreference, store.ScopeProject, "proyecto-x", "Preferencia de Jerry", "nunca usar voseo ni jerga rioplatense")

	// Muchísimo relleno reciente (cada uno con contenido distinto, para que
	// el dedup por contenido normalizado no los colapse en uno solo) para
	// forzar que el corte por MaxItems deje afuera todo lo que no sea
	// preferencia + global.
	for i := 0; i < 30; i++ {
		saveObs(t, st, store.TypeDiscovery, store.ScopeProject, "proyecto-x",
			fmt.Sprintf("Relleno reciente %d", i), strings.Repeat(fmt.Sprintf("y%d", i), 80))
	}

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{CharsLimit: 2000, MaxItems: 2})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if !strings.Contains(block, "Patron global reutilizable") {
		t.Errorf("esperaba que la observación global entrara, bloque:\n%s", block)
	}
	if !strings.Contains(block, "Preferencia de Jerry") {
		t.Errorf("esperaba que la preferencia entrara, bloque:\n%s", block)
	}
	if strings.Contains(block, "Relleno reciente") {
		t.Errorf("esperaba que el relleno de menor prioridad quedara afuera, bloque:\n%s", block)
	}
	if !strings.Contains(block, "omitidos:") || !strings.Contains(block, "por presupuesto") {
		t.Errorf("esperaba el reporte honesto de omitidos por presupuesto, bloque:\n%s", block)
	}
}

// Tarea 1 — reparto de presupuesto entre lo global y lo propio del
// proyecto. Antes de esto, el bloque siempre-inyectado dejaba entrar
// globales primero y el proyecto salía por injectContinuity, que solo da un
// preview de 80 chars (ver benchmark citado en core_block.go). Estos tests
// cubren el nuevo orden: preferencias/decisiones del proyecto, checkpoint,
// globales comprimidas hasta globals_max_chars, y por último relleno.

// (a) con muchas globales largas + varias decisiones del proyecto, el
// bloque incluye al menos un item del proyecto y no deja que las globales
// se coman el presupuesto reservado por ProjectMinChars.
func TestBuildCoreBlock_ProjectMinChars_GetsProjectContentIn(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		saveObs(t, st, store.TypePattern, store.ScopeGlobal, "proyecto-x",
			fmt.Sprintf("Patron global largo %d", i),
			strings.Repeat(fmt.Sprintf("contenido global de otro proyecto %d ", i), 15))
	}
	for i := 0; i < 3; i++ {
		saveObs(t, st, store.TypeDecision, store.ScopeProject, "proyecto-x",
			fmt.Sprintf("Decision del proyecto %d", i),
			fmt.Sprintf("se eligió el enfoque %d por motivos internos del proyecto", i))
	}

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if !strings.Contains(block, "Decision del proyecto") {
		t.Errorf("esperaba al menos un item del proyecto en el bloque:\n%s", block)
	}
	if strings.Contains(block, "contenido global de otro proyecto") {
		t.Errorf("las globales deben ir comprimidas (sin 'Qué: ...'), bloque:\n%s", block)
	}
}

// (b) el checkpoint activo entra SIEMPRE si existe y el presupuesto lo
// permite — antes quedaba al final del orden de prioridad y nunca entraba.
func TestBuildCoreBlock_CheckpointAlwaysIncluded(t *testing.T) {
	dataDir := setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	if err := checkpoint.Save(dataDir, "proyecto-x", checkpoint.State{
		Task:     "implementar reparto de presupuesto",
		NextStep: "correr los tests",
		Project:  "proyecto-x",
	}); err != nil {
		t.Fatalf("checkpoint.Save: %v", err)
	}

	for i := 0; i < 10; i++ {
		saveObs(t, st, store.TypePattern, store.ScopeGlobal, "proyecto-x",
			fmt.Sprintf("Patron global %d", i),
			strings.Repeat(fmt.Sprintf("relleno global %d ", i), 15))
	}

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{IncludeCheckpoint: true})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if !strings.Contains(block, "> implementar reparto de presupuesto | siguiente: correr los tests") {
		t.Errorf("esperaba la línea del checkpoint en el bloque:\n%s", block)
	}
}

// (c) las globales no superan GlobalsMaxChars, sin importar cuántas haya.
func TestBuildCoreBlock_GlobalsRespectGlobalsMaxChars(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		saveObs(t, st, store.TypePattern, store.ScopeGlobal, "proyecto-x",
			fmt.Sprintf("Patron global reutilizable numero %d con titulo largo", i),
			"contenido irrelevante para el cálculo, va comprimido")
	}

	maxGlobal := 300
	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{GlobalsMaxChars: maxGlobal, GlobalsMaxItems: 50, MaxItems: 50, MaxPerType: 50})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}

	globalChars := 0
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "- [pattern]") {
			globalChars += len(line) + 1 // +1 por el "\n" que separaba la línea
		}
	}
	if globalChars > maxGlobal {
		t.Errorf("las globales usaron %d chars, supera GlobalsMaxChars=%d, bloque:\n%s", globalChars, maxGlobal, block)
	}
}

// (c.2) el tope de CANTIDAD (GlobalsMaxItems) rige independiente de cuántos
// chars ocupen las globales — muchas globales cortas no deben monopolizar
// la lista de items solo porque entran cómodas en GlobalsMaxChars.
func TestBuildCoreBlock_GlobalsMaxItems_CapsCount(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		saveObs(t, st, store.TypePattern, store.ScopeGlobal, "proyecto-x",
			fmt.Sprintf("Global corta sobre %s", fillerWord(i)), "contenido corto")
	}

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{
		GlobalsMaxChars: 5000, GlobalsMaxItems: 3, MaxItems: 50, MaxPerType: 50,
	})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}

	entered := strings.Count(block, "- [pattern]")
	if entered != 3 {
		t.Errorf("esperaba exactamente 3 globales con GlobalsMaxItems=3, entraron %d, bloque:\n%s", entered, block)
	}
	if !strings.Contains(block, "omitidos:") || !strings.Contains(block, "globales (presupuesto)") {
		t.Errorf("esperaba el reporte de omitidos por presupuesto de globales, bloque:\n%s", block)
	}
}

// (d) el total nunca supera CharsLimit, incluso con GlobalsMaxChars y
// ProjectMinChars generosos combinados.
func TestBuildCoreBlock_TotalNeverExceedsCharsLimit(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 15; i++ {
		saveObs(t, st, store.TypePattern, store.ScopeGlobal, "proyecto-x",
			fmt.Sprintf("Global %d", i), strings.Repeat("x", 300))
	}
	for i := 0; i < 15; i++ {
		saveObs(t, st, store.TypeDecision, store.ScopeProject, "proyecto-x",
			fmt.Sprintf("Decision %d", i), strings.Repeat("y", 300))
	}

	limit := 900
	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{
		CharsLimit:      limit,
		MaxItems:        50,
		GlobalsMaxChars: 800,
		ProjectMinChars: 800,
	})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if len(block) > limit {
		t.Errorf("bloque de %d chars supera CharsLimit=%d", len(block), limit)
	}
}

// (e) proyecto nuevo, sin observaciones propias: el bloque no se rompe y
// sigue mostrando las globales disponibles.
func TestBuildCoreBlock_NoProjectObservations_StillWorks(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	saveObs(t, st, store.TypePattern, store.ScopeGlobal, "proyecto-nuevo", "Patron global util", "aplica a cualquier proyecto")

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-nuevo", hooks.CoreBlockOptions{})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if !strings.Contains(block, "Patron global util") {
		t.Errorf("esperaba la global en un proyecto sin observaciones propias, bloque:\n%s", block)
	}
}

// Tarea 2 — un item [intent] se marca como tal y el bloque avisa que no
// está verificado. Ver store.TypeIntent para el caso real del benchmark.
func TestBuildCoreBlock_IntentMarkedWithWarning(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	saveObs(t, st, store.TypeIntent, store.ScopeProject, "proyecto-x",
		"El comando de release es make release-v2", "dicho por Jerry, todavía no confirmado contra el repo")

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if !strings.Contains(block, "[intent] El comando de release es make release-v2") {
		t.Errorf("esperaba el item marcado [intent], bloque:\n%s", block)
	}
	if !strings.Contains(block, "son planes o afirmaciones sin verificar") {
		t.Errorf("esperaba la cabecera de advertencia sobre [intent], bloque:\n%s", block)
	}
}

// Tarea 3 — curaduría real del bloque: dedupe por topic_key/solapamiento de
// título, tope por tipo y frescura visible. Caso real medido (proyecto
// kronos-v2, 2026-09-11): 6 de 7 items de proyecto en el bloque eran
// [architecture], varias del mismo hilo de trabajo del día — un log, no un
// perfil curado.

// (a) topic_key compartido: dos observaciones de títulos y contenido bien
// distintos, pero el MISMO topic_key (vía UPDATE directo — SaveObservation
// ya hace upsert por topic_key dentro de un mismo proyecto, así que forzar
// el choque a mano es la única forma de reproducir el caso real: dos filas
// que terminan compartiendo topic_key por venir de scopes/momentos
// distintos). Solo una debe entrar al bloque.
func TestBuildCoreBlock_DedupeByTopicKey_OnlyOneEnters(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	obsA := saveObs(t, st, store.TypeArchitecture, store.ScopeProject, "proyecto-x",
		"Configuración del backup nocturno", "se decidió correr el backup a las 3am")
	obsB := saveObs(t, st, store.TypeArchitecture, store.ScopeProject, "proyecto-x",
		"Ajuste del respaldo automático", "se cambió el horario del respaldo a las 4am")

	if _, err := st.DB().Exec(`UPDATE observations SET topic_key = ? WHERE id IN (?, ?)`,
		"core-perf-topic", obsA.ID, obsB.ID); err != nil {
		t.Fatalf("forzar topic_key compartido: %v", err)
	}

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	hasA := strings.Contains(block, "Configuración del backup nocturno")
	hasB := strings.Contains(block, "Ajuste del respaldo automático")
	if hasA == hasB {
		t.Errorf("esperaba que solo UNA de las dos observaciones con topic_key compartido entrara, bloque:\n%s", block)
	}
}

// (b) títulos que solapan ≥70% de sus tokens significativos (sin topic_key)
// — mismo caso real citado en core_block.go: "Reparto de presupuesto core
// ..." y "Bloque core: presupuesto repartido ..." son la misma decisión
// contada dos veces con otras palabras.
func TestBuildCoreBlock_DedupeByTitleOverlap_OnlyOneEnters(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	saveObs(t, st, store.TypeArchitecture, store.ScopeProject, "proyecto-x",
		"Bloque core presupuesto proyecto global checkpoint",
		"primera versión de la decisión")
	saveObs(t, st, store.TypeArchitecture, store.ScopeProject, "proyecto-x",
		"Bloque core presupuesto proyecto global sesión",
		"segunda versión, redactada distinto, mismo tema")

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	hasFirst := strings.Contains(block, "Bloque core presupuesto proyecto global checkpoint")
	hasSecond := strings.Contains(block, "Bloque core presupuesto proyecto global sesión")
	if hasFirst == hasSecond {
		t.Errorf("esperaba que solo UNO de los dos títulos solapados (≥70%%) entrara, bloque:\n%s", block)
	}
}

// (c) tope por tipo: 6 candidatas [architecture] con títulos de bajo
// solapamiento (no deben dedupearse entre sí) + core.max_per_type=3 ⇒
// entran exactamente 3. Caso real: 6/7 items de proyecto eran
// [architecture] del mismo hilo de trabajo — un tipo no puede monopolizar
// el bloque.
func TestBuildCoreBlock_MaxPerType_CapsArchitectureItems(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	modulos := []string{"autenticación", "facturación", "notificaciones", "exportación", "sincronización", "respaldo"}
	for _, m := range modulos {
		saveObs(t, st, store.TypeArchitecture, store.ScopeProject, "proyecto-x",
			fmt.Sprintf("Arquitectura módulo %s", m),
			fmt.Sprintf("se definió el diseño del módulo de %s", m))
	}

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{MaxPerType: 3, MaxItems: 20})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	entered := 0
	for _, m := range modulos {
		if strings.Contains(block, "módulo "+m) {
			entered++
		}
	}
	if entered != 3 {
		t.Errorf("esperaba exactamente 3 items [architecture] con max_per_type=3, entraron %d, bloque:\n%s", entered, block)
	}
}

// (d) frescura visible: una decisión sin actualizar hace más de
// core.stale_days lleva el sufijo "(antiguo)" — el bloque no distingue hoy
// entre una decisión vigente y una desactualizada.
func TestBuildCoreBlock_StaleDecision_MarkedAntiguo(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	obs := saveObs(t, st, store.TypeDecision, store.ScopeProject, "proyecto-x",
		"Decision vieja sobre backups", "se eligió el enfoque de backups incrementales")
	backdateObservationUpdatedAt(t, st, obs.ID, time.Now().Add(-20*24*time.Hour))

	fresh := saveObs(t, st, store.TypeDecision, store.ScopeProject, "proyecto-x",
		"Decision fresca sobre despliegues", "se eligió el enfoque de despliegues canary")
	_ = fresh

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{StaleDays: 10})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if !strings.Contains(block, "Decision vieja sobre backups") || !strings.Contains(block, "(antiguo)") {
		t.Errorf("esperaba la decisión vieja marcada (antiguo), bloque:\n%s", block)
	}
	for _, line := range strings.Split(block, "\n") {
		if strings.Contains(line, "Decision fresca sobre despliegues") && strings.Contains(line, "(antiguo)") {
			t.Errorf("la decisión fresca no debería llevar el sufijo (antiguo), línea:\n%s", line)
		}
	}
}

// (e) formato denso: una línea por item, separador "—", sin "Qué: ...", y
// topeado a core.max_item_chars.
func TestBuildCoreBlock_DenseFormat_RespectsMaxItemChars(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	saveObs(t, st, store.TypeDecision, store.ScopeProject, "proyecto-x",
		"Decision sobre backups",
		"Qué: se decidió un enfoque muy largo con muchísimos detalles de más de noventa caracteres para probar el recorte")

	maxItemChars := 60
	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{MaxItemChars: maxItemChars})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if strings.Contains(block, "Qué:") {
		t.Errorf("no debería quedar el literal 'Qué:' en el bloque:\n%s", block)
	}
	found := false
	for _, line := range strings.Split(block, "\n") {
		if !strings.HasPrefix(line, "- [decision]") {
			continue
		}
		found = true
		item := strings.TrimPrefix(line, "- ")
		if len(item) > maxItemChars {
			t.Errorf("línea de %d chars supera max_item_chars=%d: %q", len(item), maxItemChars, item)
		}
		if !strings.Contains(line, " — ") {
			t.Errorf("esperaba el separador ' — ' en la línea: %q", line)
		}
	}
	if !found {
		t.Fatalf("esperaba una línea [decision] en el bloque:\n%s", block)
	}
}

// Tarea 4 — filtro de pertinencia de globales (core.relevance_filter). Caso
// real medido (proyecto kronos-v2, 2026-09-11): 6-7 de 12 items inyectados
// eran globales de OTRO proyecto (ATISA) sin relación con lo que se estaba
// trabajando — ver comentario de mediciones en core_block.go.

// (a) una global cuyo proyecto de ORIGEN (o.Project — no el proyecto que se
// está consultando) es distinto del actual, sin pertinencia positiva, queda
// afuera cuando RelevanceFilter está activo.
func TestBuildCoreBlock_RelevanceFilter_OtherOriginExcluded(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	saveObs(t, st, store.TypePreference, store.ScopeGlobal, "otro-proyecto",
		"Usar siempre pnpm nunca npm", "seguridad supply chain en despliegues de otro-proyecto")

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{RelevanceFilter: true})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if strings.Contains(block, "Usar siempre pnpm") {
		t.Errorf("esperaba que la global de otro proyecto de origen quedara afuera, bloque:\n%s", block)
	}
	if !strings.Contains(block, "omitidos:") || !strings.Contains(block, "poco pertinentes") {
		t.Errorf("esperaba el reporte de omitidos por pertinencia, bloque:\n%s", block)
	}
}

// (b) una global cuyo proyecto de origen ES el actual entra siempre, aun con
// el filtro activo.
func TestBuildCoreBlock_RelevanceFilter_SameOriginIncluded(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	saveObs(t, st, store.TypePattern, store.ScopeGlobal, "proyecto-x",
		"Patron propio del proyecto", "este patron nació en proyecto-x")

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{RelevanceFilter: true})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if !strings.Contains(block, "Patron propio del proyecto") {
		t.Errorf("esperaba que la global del mismo proyecto de origen entrara, bloque:\n%s", block)
	}
}

// (c) rescate por pertinencia positiva: una global de OTRO proyecto de
// origen entra igual si su título comparte >=2 tokens significativos con
// los títulos de observaciones del proyecto actual (fuera del propio
// nombre del proyecto — ver comentario de mediciones en core_block.go sobre
// por qué el nombre del proyecto se excluye del cómputo).
func TestBuildCoreBlock_RelevanceFilter_PositiveOverlapRescues(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	saveObs(t, st, store.TypeDecision, store.ScopeProject, "proyecto-x",
		"Migracion completa de autenticacion biometrica", "se completó la migración del módulo biométrico")
	saveObs(t, st, store.TypePattern, store.ScopeGlobal, "otro-proyecto",
		"Guia de migracion biometrica para nuevos clientes", "aplica el mismo patrón de migración biométrica")

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{RelevanceFilter: true})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if !strings.Contains(block, "Guia de migracion biometrica") {
		t.Errorf("esperaba que la pertinencia positiva (2+ tokens compartidos) rescatara la global, bloque:\n%s", block)
	}
}

// (d) el nombre del propio proyecto NO cuenta para el rescate de pertinencia
// positiva — caso real medido: kronos-v2 es el proyecto del propio
// asistente de memoria, así que casi cualquier observación (propia o
// ajena) menciona "kronos"; sin excluir ese token, una global de OTRO
// proyecto que solo comparte el nombre del proyecto + UN término técnico
// común se rescataría igual, anulando el filtro.
func TestBuildCoreBlock_RelevanceFilter_ProjectNameTokenExcludedFromRescue(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	saveObs(t, st, store.TypeArchitecture, store.ScopeProject, "kronos-v2",
		"Busqueda contra Postgres optimizada", "se agregó índice GIN para acelerar la búsqueda")
	saveObs(t, st, store.TypeConfig, store.ScopeGlobal, "otro-proyecto",
		"Migrado kronos de SQLite a Postgres en Docker", "instalación propia de otro-proyecto, nada que ver con este")

	block, err := hooks.BuildCoreBlock(ctx, st, "kronos-v2", hooks.CoreBlockOptions{RelevanceFilter: true})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if strings.Contains(block, "Migrado kronos de SQLite") {
		t.Errorf("compartir solo el nombre del proyecto (\"kronos\") + un término técnico no debería alcanzar para rescatar, bloque:\n%s", block)
	}
}

// backdateObservationCreatedAt fuerza created_at al pasado — usado para que
// una observación quede FUERA de relevanceRecentObsLimit sin depender de
// timestamps reales en un test rápido.
func backdateObservationCreatedAt(t *testing.T, st store.Storer, id int64, ts time.Time) {
	t.Helper()
	s, ok := st.(*store.Store)
	if !ok {
		t.Fatalf("backdateObservationCreatedAt necesita *store.Store")
	}
	if _, err := s.DB().Exec(`UPDATE observations SET created_at = ? WHERE id = ?`,
		ts.UTC().Format(time.RFC3339), id); err != nil {
		t.Fatal(err)
	}
}

// (d.2) caso real medido (proyecto kronos-v2, 2026-09-11): con TODO el
// historial del proyecto como vocabulario, una nota real de ATISA
// ("SIEMPRE probar en pruebas antes de producción...") compartía 5 tokens
// contra títulos de kronos-v2 acumulados en meses — vocabulario técnico
// genérico, no pertinencia real. Acotar projectVocab a lo más reciente
// (relevanceRecentObsLimit) es lo que evita el rescate: vocabulario que solo
// aparece en observaciones VIEJAS del proyecto no debe rescatar una global
// ajena.
func TestBuildCoreBlock_RelevanceFilter_OldVocabDoesNotRescue(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Una observación vieja (fuera de relevanceRecentObsLimit) que comparte
	// vocabulario con la global ajena.
	old := saveObs(t, st, store.TypeDiscovery, store.ScopeProject, "proyecto-x",
		"Resultado de pruebas contra produccion", "hallazgo antiguo, ya no relevante")
	backdateObservationCreatedAt(t, st, old.ID, time.Now().Add(-90*24*time.Hour))

	// Suficientes observaciones RECIENTES (sin ese vocabulario) para que la
	// vieja quede fuera de relevanceRecentObsLimit.
	for i := 0; i < 10; i++ {
		saveObs(t, st, store.TypeDiscovery, store.ScopeProject, "proyecto-x",
			fmt.Sprintf("Hallazgo reciente %s", fillerWord(i)), "contenido sin relación")
	}

	saveObs(t, st, store.TypePreference, store.ScopeGlobal, "otro-proyecto",
		"Siempre probar en pruebas antes de produccion", "sin excepción, para cualquier proyecto")

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{
		RelevanceFilter: true, MaxItems: 50, MaxPerType: 50,
	})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if strings.Contains(block, "Siempre probar en pruebas") {
		t.Errorf("vocabulario compartido solo con una observación VIEJA no debería rescatar la global, bloque:\n%s", block)
	}
}

// (e) RelevanceFilter: false dispensa el filtro por completo — mismo
// comportamiento que antes de esta curaduría, para callers que arman
// CoreBlockOptions{} sin pensar en pertinencia (ej. tests existentes,
// internal/obsidian/export.go).
func TestBuildCoreBlock_RelevanceFilter_Disabled_IncludesEverything(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	saveObs(t, st, store.TypePreference, store.ScopeGlobal, "otro-proyecto",
		"Preferencia sin ninguna relacion", "contenido totalmente ajeno a proyecto-x")

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{RelevanceFilter: false})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if !strings.Contains(block, "Preferencia sin ninguna relacion") {
		t.Errorf("con RelevanceFilter=false la global debería entrar igual, bloque:\n%s", block)
	}
}

// El checkpoint entra SIEMPRE, incluso compitiendo con MaxItems=1 y varios
// items de proyecto de alta prioridad — "siempre entra" no cede ante la
// competencia por presupuesto/cantidad.
func TestBuildCoreBlock_CheckpointEntersDespiteMaxItemsCompetition(t *testing.T) {
	dataDir := setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	if err := checkpoint.Save(dataDir, "proyecto-x", checkpoint.State{
		Task: "tarea en curso", NextStep: "siguiente paso", Project: "proyecto-x",
	}); err != nil {
		t.Fatalf("checkpoint.Save: %v", err)
	}
	for i := 0; i < 5; i++ {
		saveObs(t, st, store.TypePreference, store.ScopeProject, "proyecto-x",
			fmt.Sprintf("Preferencia %s", fillerWord(i)), "contenido cualquiera")
	}

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{IncludeCheckpoint: true, MaxItems: 1})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if !strings.Contains(block, "> tarea en curso | siguiente: siguiente paso") {
		t.Errorf("el checkpoint debería entrar SIEMPRE, incluso con MaxItems=1 y preferencias compitiendo, bloque:\n%s", block)
	}
}

// El recorte por MaxItemChars nunca corta una palabra a la mitad — retrocede
// hasta el último espacio antes de agregar "...". Se compara la línea
// recortada contra la línea SIN recortar (MaxItemChars grande): la recortada
// debe ser un prefijo exacto de la completa, y el carácter que sigue a ese
// prefijo en la línea completa debe ser un espacio (o no existir) — nunca
// una letra a mitad de palabra.
func TestBuildCoreBlock_Truncation_NeverCutsWordMidway(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	words := []string{"palabraunodiez", "palabradosdiez", "palabratresdiez", "palabracuatrodiez", "palabracincodiez"}
	saveObs(t, st, store.TypeDiscovery, store.ScopeProject, "proyecto-x", "T", strings.Join(words, " "))

	findLine := func(block string) string {
		for _, l := range strings.Split(block, "\n") {
			if strings.HasPrefix(l, "- [discovery]") {
				return l
			}
		}
		return ""
	}

	fullBlock, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{MaxItemChars: 500})
	if err != nil {
		t.Fatalf("BuildCoreBlock (sin recorte): %v", err)
	}
	fullLine := findLine(fullBlock)
	if fullLine == "" || strings.HasSuffix(fullLine, "...") {
		t.Fatalf("esperaba la línea completa sin recortar, bloque:\n%s", fullBlock)
	}

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{MaxItemChars: 45})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	line := findLine(block)
	if line == "" {
		t.Fatalf("esperaba una línea [discovery] en el bloque:\n%s", block)
	}
	if !strings.HasSuffix(line, "...") {
		t.Fatalf("esperaba que la línea recortada terminara en '...': %q", line)
	}
	trimmed := strings.TrimSuffix(line, "...")
	if !strings.HasPrefix(fullLine, trimmed) {
		t.Fatalf("la línea recortada %q no es un prefijo de la línea completa %q", trimmed, fullLine)
	}
	if len(fullLine) > len(trimmed) && fullLine[len(trimmed)] != ' ' {
		t.Errorf("se cortó a mitad de palabra: después del prefijo recortado %q sigue %q (no un espacio)", trimmed, string(fullLine[len(trimmed)]))
	}
}

// Reporte honesto combinando varias clases de omisión en el mismo bloque:
// globales poco pertinentes, globales por presupuesto y proyecto por
// presupuesto conviven en la misma cláusula "omitidos: ...".
func TestBuildCoreBlock_HonestOmissionReport_MultipleClasses(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	saveObs(t, st, store.TypePreference, store.ScopeGlobal, "otro-proyecto",
		"Preferencia irrelevante de otro proyecto", "sin relación con proyecto-x")
	for i := 0; i < 6; i++ {
		saveObs(t, st, store.TypePattern, store.ScopeGlobal, "proyecto-x",
			fmt.Sprintf("Patron propio %s", fillerWord(i)), "contenido")
	}
	for i := 0; i < 10; i++ {
		saveObs(t, st, store.TypeDiscovery, store.ScopeProject, "proyecto-x",
			fmt.Sprintf("Hallazgo %s", fillerWord(i+10)), "contenido de relleno")
	}

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{
		RelevanceFilter: true, GlobalsMaxItems: 2, MaxItems: 4,
	})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if strings.Contains(block, "recortado por presupuesto") {
		t.Errorf("el footer genérico anterior no debería aparecer más, bloque:\n%s", block)
	}
	if !strings.Contains(block, "omitidos:") {
		t.Fatalf("esperaba la cláusula de omitidos, bloque:\n%s", block)
	}
	if !strings.Contains(block, "poco pertinentes") {
		t.Errorf("esperaba omitidos por pertinencia, bloque:\n%s", block)
	}
	if !strings.Contains(block, "por presupuesto") {
		t.Errorf("esperaba omitidos por presupuesto, bloque:\n%s", block)
	}
}

// (c) enabled: false en config ⇒ el hook no imprime nada de core.
func TestRunSessionStart_CoreDisabled_NoCoreBlock(t *testing.T) {
	setupTempDataDir(t)
	setTempConfigDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	cfg := config.Default()
	cfg.Core.Enabled = false
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save config: %v", err)
	}

	saveObs(t, st, store.TypePattern, store.ScopeGlobal, "proyecto-y", "Patron global", "contenido")

	in := hooks.Input{SessionID: "test-core-disabled", CWD: "/home/x/proyecto-y"}
	out := captureStdout(t, func() {
		if err := hooks.RunSessionStart(ctx, in, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	if strings.Contains(out, "[kronos:core]") {
		t.Errorf("con core.enabled=false no debería aparecer el bloque core, output:\n%s", out)
	}
}

// injectContinuity también marca [intent] (no solo el bloque core) y avisa
// una vez — con core.enabled=false para aislar la señal a lo que imprime
// injectContinuity fuera del bloque core.
func TestRunSessionStart_Continuity_MarksIntentWithWarning(t *testing.T) {
	setupTempDataDir(t)
	setTempConfigDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	cfg := config.Default()
	cfg.Core.Enabled = false
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save config: %v", err)
	}

	saveObs(t, st, store.TypeIntent, store.ScopeProject, "proyecto-intent",
		"El comando de release es make release-v2", "dicho por Jerry, todavía no confirmado contra el repo")

	in := hooks.Input{SessionID: "test-continuity-intent", CWD: "/home/x/proyecto-intent"}
	out := captureStdout(t, func() {
		if err := hooks.RunSessionStart(ctx, in, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	if !strings.Contains(out, "[intent] El comando de release es make release-v2") {
		t.Errorf("esperaba el item de continuidad marcado [intent], output:\n%s", out)
	}
	if !strings.Contains(out, "son planes o afirmaciones sin verificar") {
		t.Errorf("esperaba la advertencia sobre [intent], output:\n%s", out)
	}
}

// enabled: true (default) sí debe imprimir el bloque cuando hay contenido.
func TestRunSessionStart_CoreEnabled_PrintsCoreBlock(t *testing.T) {
	setupTempDataDir(t)
	setTempConfigDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	cfg := config.Default()
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save config: %v", err)
	}

	saveObs(t, st, store.TypePattern, store.ScopeGlobal, "proyecto-z", "Patron global visible", "contenido")

	in := hooks.Input{SessionID: "test-core-enabled", CWD: "/home/x/proyecto-z"}
	out := captureStdout(t, func() {
		if err := hooks.RunSessionStart(ctx, in, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	if !strings.Contains(out, "[kronos:core]") {
		t.Errorf("esperaba bloque core en el output:\n%s", out)
	}
	if !strings.Contains(out, "Patron global visible") {
		t.Errorf("esperaba la observación global en el bloque, output:\n%s", out)
	}
}
