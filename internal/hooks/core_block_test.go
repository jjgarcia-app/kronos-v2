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
	if !strings.Contains(block, "recortado por presupuesto") {
		t.Errorf("esperaba aviso de recorte, bloque:\n%s", block)
	}
}

// (b) la prioridad se respeta: preferencia del proyecto y observación global
// entran, y se cae lo menos prioritario (relleno reciente del proyecto, que
// desde el reparto de presupuesto de core.max_global_chars/project_min_chars
// pasó a ser el ÚLTIMO paso de llenado, no el primero). MaxItems: 2 fuerza el
// corte independientemente de la aritmética de chars — así el test no
// depende de los defaults de MaxGlobalChars/ProjectMinChars.
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
	if !strings.Contains(block, "recortado por presupuesto") {
		t.Errorf("esperaba aviso de recorte, bloque:\n%s", block)
	}
}

// Tarea 1 — reparto de presupuesto entre lo global y lo propio del
// proyecto. Antes de esto, el bloque siempre-inyectado dejaba entrar
// globales primero y el proyecto salía por injectContinuity, que solo da un
// preview de 80 chars (ver benchmark citado en core_block.go). Estos tests
// cubren el nuevo orden: preferencias/decisiones del proyecto, checkpoint,
// globales comprimidas hasta max_global_chars, y por último relleno.

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

// (c) las globales no superan MaxGlobalChars, sin importar cuántas haya.
func TestBuildCoreBlock_GlobalsRespectMaxGlobalChars(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		saveObs(t, st, store.TypePattern, store.ScopeGlobal, "proyecto-x",
			fmt.Sprintf("Patron global reutilizable numero %d con titulo largo", i),
			"contenido irrelevante para el cálculo, va comprimido")
	}

	maxGlobal := 300
	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{MaxGlobalChars: maxGlobal, MaxItems: 50})
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
		t.Errorf("las globales usaron %d chars, supera MaxGlobalChars=%d, bloque:\n%s", globalChars, maxGlobal, block)
	}
}

// (d) el total nunca supera CharsLimit, incluso con MaxGlobalChars y
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
		MaxGlobalChars:  800,
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
