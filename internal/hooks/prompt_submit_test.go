package hooks_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// submitPrompt runs RunPromptSubmit once and returns what it wrote to w.
func submitPrompt(t *testing.T, ctx context.Context, st *store.Store, sessionID, cwd, prompt string) string {
	t.Helper()
	var buf bytes.Buffer
	in := hooks.Input{SessionID: sessionID, CWD: cwd, Prompt: prompt}
	if err := hooks.RunPromptSubmit(ctx, in, st, nil, &buf); err != nil {
		t.Fatalf("RunPromptSubmit: %v", err)
	}
	return buf.String()
}

// TestRunPromptSubmit_NudgesAfterThreshold confirma que el aviso de guardado
// aparece en la salida del hook una vez que se cruza el umbral de prompts
// (nudgeEveryN=15) sin ningún mem_save, y no antes.
func TestRunPromptSubmit_NudgesAfterThreshold(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd := t.TempDir()
	sessionID := "s-nudge-1"

	if _, err := st.CreateSession(ctx, sessionID, "p", cwd); err != nil {
		t.Fatal(err)
	}

	var last string
	for i := 1; i <= 15; i++ {
		last = submitPrompt(t, ctx, st, sessionID, cwd, "prompt")
	}

	if !strings.Contains(last, "recordatorio de memoria") {
		t.Errorf("prompt #15 sin save debería incluir el nudge, salida: %q", last)
	}
}

// TestRunPromptSubmit_NudgesAgainAfterSave_LongUnsavedStretch es la
// regresión real: antes, CountSessionObservations == 0 era la condición de
// disparo — apenas había UN mem_save en la sesión, el nudge quedaba en
// silencio para siempre, sin importar cuánto trabajo sin guardar viniera
// después (el caso real: un barrido de 51 documentos que nunca se guardó
// porque una sesión anterior ya había guardado algo). Ahora el conteo es
// "prompts desde el último save", así que el nudge debe volver a disparar
// tras un tramo largo sin guardar, incluso habiendo guardado antes.
func TestRunPromptSubmit_NudgesAgainAfterSave_LongUnsavedStretch(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd := t.TempDir()
	sessionID := "s-nudge-2"

	if _, err := st.CreateSession(ctx, sessionID, "p", cwd); err != nil {
		t.Fatal(err)
	}

	// Un save temprano en la sesión.
	if _, err := st.SaveObservation(ctx, store.SaveParams{
		SessionID: sessionID, Type: store.TypeDiscovery, Title: "obs temprana", Content: "c", Project: "p",
	}); err != nil {
		t.Fatal(err)
	}
	// created_at tiene precisión de segundo — cruzar el límite a propósito
	// para que "prompts desde el último save" cuente solo lo posterior.
	time.Sleep(1100 * time.Millisecond)

	var last string
	for i := 1; i <= 15; i++ {
		last = submitPrompt(t, ctx, st, sessionID, cwd, "prompt tras la sesión larga sin guardar")
	}

	if !strings.Contains(last, "recordatorio de memoria") {
		t.Errorf("tras 15 prompts sin guardar DESPUÉS del save temprano, debería nudgear de nuevo; salida: %q", last)
	}
}

// TestRunPromptSubmit_NoNudgeBeforeThreshold confirma que no hay ruido antes
// de llegar al umbral.
func TestRunPromptSubmit_NoNudgeBeforeThreshold(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd := t.TempDir()
	sessionID := "s-nudge-3"

	if _, err := st.CreateSession(ctx, sessionID, "p", cwd); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 14; i++ {
		out := submitPrompt(t, ctx, st, sessionID, cwd, "prompt")
		if strings.Contains(out, "recordatorio de memoria") {
			t.Fatalf("nudge disparó antes de tiempo en el prompt #%d", i)
		}
	}
}

// --- Recalibración ronda 2: FTS por OR + guarda de precisión, presupuesto
// total, exclusión de IDs del arranque, cache por prompt, sonda de vector ---

// TestRunPromptSubmit_ORQuery_MinMatchedTerms_PartialMatchAboveThreshold_Injects
// cubre (a): un prompt de 4 términos significativos donde una observación
// solo matchea 2 de los 4 — con min_matched_terms default (2) y ≥3 términos
// en el prompt, eso alcanza para inyectar. Caso real que motiva esto: "AND
// implícito" exigía los 4 en la misma observación (0 filas, ver ronda 2 del
// benchmark); "OR con guarda de precisión" relaja a "al menos 2 de verdad
// presentes", ni tan estricto como el AND viejo ni tan laxo como un OR puro.
func TestRunPromptSubmit_ORQuery_MinMatchedTerms_PartialMatchAboveThreshold_Injects(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-or-partial", "kronos-v2", cwd)
	st.PersistInjectedIDs(ctx, "sess-or-partial", []string{})

	// Título+contenido traen "alfresco" y "aspect" (2 de los 4 términos del
	// prompt) pero no "remove" ni "ahora" — con AND implícito esto daba 0
	// filas.
	st.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypeDiscovery,
		Title:   "alfresco aspect config",
		Content: "cómo quedó configurado el aspect en alfresco para este content type",
		Project: "kronos-v2",
	})

	in := hooks.Input{SessionID: "sess-or-partial", CWD: cwd, Prompt: "alfresco aspect remove ahora"}

	out := submitPrompt(t, ctx, st, "sess-or-partial", cwd, in.Prompt)

	if !strings.Contains(out, "[kronos:relevante]") {
		t.Errorf("2/4 términos matcheados (>= min_matched_terms default 2) debería inyectar vía FTS-OR, salida: %q", out)
	}
}

// TestRunPromptSubmit_ORQuery_BelowMinMatchedTerms_FallsBackToVector cubre el
// reverso de (a): con solo 1 de 4 términos presentes (por debajo de
// min_matched_terms=2) y ese único término CORTO (< guardaLargoMinimo, no
// específico), el resultado FTS se descarta como ruido y NO se inyecta desde
// ahí — si tampoco hay vector disponible, no hay nada que inyectar. "ahora"
// (5 letras) es justo el caso que NO debe activar la excepción de término
// largo (ver TestRunPromptSubmit_ORQuery_SingleLongTerm_Injects para el caso
// que sí la activa).
func TestRunPromptSubmit_ORQuery_BelowMinMatchedTerms_NoInjection(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-or-weak", "kronos-v2", cwd)
	st.PersistInjectedIDs(ctx, "sess-or-weak", []string{})

	// Solo "ahora" de los 4 términos del prompt aparece acá — 1 match, corto,
	// por debajo del mínimo (2) para un prompt de 4 términos significativos.
	st.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypeDiscovery,
		Title:   "tareas pendientes ahora",
		Content: "hay que revisarlo en algún momento, nada urgente todavía",
		Project: "kronos-v2",
	})

	out := submitPrompt(t, ctx, st, "sess-or-weak", cwd, "alfresco aspect remove ahora")

	if strings.Contains(out, "[kronos:relevante]") {
		t.Errorf("1/4 términos matcheados, corto (< min_matched_terms y < guardaLargoMinimo) no debería inyectar, salida: %q", out)
	}
}

// TestRunPromptSubmit_ORQuery_SingleLongTerm_Injects cubre la excepción a la
// guarda de precisión: un único término matcheado (por debajo de
// min_matched_terms=2) SÍ inyecta si ese término tiene >= guardaLargoMinimo
// runas — palabras largas ("alfresco") son específicas del tema, a
// diferencia de coincidencias cortas como "ahora". Caso real medido contra
// el fixture kronos-bench (ronda feat/recall-ventana-candidatos): preguntas
// como "¿qué convenciones de estilo usa el proyecto?" solo matcheaban
// "convenciones" (1 de 3+ términos) y la guarda estricta las descartaba
// aunque el hecho correcto estuviera en los resultados de la FTS.
func TestRunPromptSubmit_ORQuery_SingleLongTerm_Injects(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-or-long", "kronos-v2", cwd)
	st.PersistInjectedIDs(ctx, "sess-or-long", []string{})

	// Solo "alfresco" (8 letras) de los 4 términos del prompt aparece acá.
	st.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypeDiscovery,
		Title:   "alfresco upgrade notes",
		Content: "notas de la migración de versión de alfresco",
		Project: "kronos-v2",
	})

	out := submitPrompt(t, ctx, st, "sess-or-long", cwd, "alfresco aspect remove ahora")

	if !strings.Contains(out, "[kronos:relevante]") {
		t.Errorf("1/4 términos matcheados pero largo (>= guardaLargoMinimo) debería inyectar, salida: %q", out)
	}
}

// TestRunPromptSubmit_TotalBudget_NoMatchAndSlowProvider_NothingInjected
// cubre (b): sin ningún término presente por FTS y con el proveedor vectorial
// colgado, no debe inyectarse nada y el presupuesto TOTAL (total_budget_ms,
// no timeout_ms) debe respetarse — antes, FTS+vector podían sumar más de lo
// que un hook debería tardar.
func TestRunPromptSubmit_TotalBudget_NoMatchAndSlowProvider_NothingInjected(t *testing.T) {
	setupTempConfigDir(t)
	cfg := config.Default()
	cfg.Recall.TotalBudgetMs = 200
	if err := cfg.Save(); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}

	st := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-budget-b", "kronos-v2", cwd)
	st.PersistInjectedIDs(ctx, "sess-budget-b", []string{})

	obs, _ := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDiscovery, Title: "obs cualquiera", Content: "contenido cualquiera para indexar", Project: "kronos-v2",
	})

	prompt := "zzznadamatcheaaquizzz totalmente distinto"
	vs, err := embeddings.NewInMemory(sleepingEmbedFnForQuery(prompt))
	if err != nil {
		t.Fatal(err)
	}
	if err := vs.Index(ctx, obs.ID, obs.Content); err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{SessionID: "sess-budget-b", CWD: cwd, Prompt: prompt}

	var buf bytes.Buffer
	start := time.Now()
	if err := hooks.RunPromptSubmit(ctx, in, st, vs, &buf); err != nil {
		t.Fatalf("RunPromptSubmit: %v", err)
	}
	elapsed := time.Since(start)

	if strings.Contains(buf.String(), "[kronos:relevante]") {
		t.Errorf("sin match FTS y proveedor colgado no debería inyectar nada: %q", buf.String())
	}
	if elapsed > 800*time.Millisecond {
		t.Errorf("elapsed=%v — total_budget_ms=200 debería haber cortado bastante antes (el provider duerme 5s)", elapsed)
	}
}

// TestRunPromptSubmit_ExcludesIDsInjectedBySessionStart cubre (c): una
// observación ya inyectada por el arranque de sesión (RunSessionStart /
// injectContinuity, que persiste sus IDs con PersistInjectedIDs) no vuelve a
// aparecer en el recall del primer prompt, aunque matchee de sobra por FTS.
func TestRunPromptSubmit_ExcludesIDsInjectedBySessionStart(t *testing.T) {
	setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()
	cwd := t.TempDir()
	projName := project.Detect(cwd).Name

	if _, err := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "decision core recall test", Content: "contenido core recall test compartido",
		Project: projName,
	}); err != nil {
		t.Fatal(err)
	}

	sessionID := "sess-core-then-recall"
	captureStdout(t, func() {
		if err := hooks.RunSessionStart(ctx, hooks.Input{SessionID: sessionID, CWD: cwd}, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	ids, err := st.LoadInjectedIDs(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("setup inválido: esperaba 1 id persistido por RunSessionStart, got %d (%v)", len(ids), ids)
	}

	in := hooks.Input{SessionID: sessionID, CWD: cwd, Prompt: "decision core recall"}
	var buf bytes.Buffer
	if err := hooks.RunPromptSubmit(ctx, in, st, nil, &buf); err != nil {
		t.Fatalf("RunPromptSubmit: %v", err)
	}

	if strings.Contains(buf.String(), "decision core recall test") {
		t.Errorf("runRecall repitió una observación ya inyectada por el arranque de sesión: %q", buf.String())
	}
}

// TestRunPromptSubmit_SamePromptTwice_SecondCallIsCached cubre (d): la misma
// consulta (mismo proyecto+prompt normalizado) repetida no vuelve a pagar
// Search — la segunda llamada reusa los candidatos del cache por prompt
// normalizado en vez de recalcularlos.
func TestRunPromptSubmit_SamePromptTwice_SecondCallIsCached(t *testing.T) {
	base := newTestStore(t)
	st := &searchCountingStore{Store: base}
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-cache", "kronos-v2", cwd)
	st.PersistInjectedIDs(ctx, "sess-cache", []string{})

	st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "cache test observation", Content: "contenido de prueba para el cache de recall",
		Project: "kronos-v2",
	})

	in := hooks.Input{SessionID: "sess-cache", CWD: cwd, Prompt: "cache test observation"}

	var buf1 bytes.Buffer
	if err := hooks.RunPromptSubmit(ctx, in, st, nil, &buf1); err != nil {
		t.Fatalf("RunPromptSubmit (1): %v", err)
	}
	if !strings.Contains(buf1.String(), "[kronos:relevante]") {
		t.Fatalf("primera llamada debería inyectar vía FTS, salida: %q", buf1.String())
	}
	searchCallsAfterFirst := st.count()
	if searchCallsAfterFirst == 0 {
		t.Fatalf("setup inválido: la primera llamada no llamó a Search")
	}

	var buf2 bytes.Buffer
	start := time.Now()
	if err := hooks.RunPromptSubmit(ctx, in, st, nil, &buf2); err != nil {
		t.Fatalf("RunPromptSubmit (2): %v", err)
	}
	elapsed := time.Since(start)

	if got := st.count(); got != searchCallsAfterFirst {
		t.Errorf("segunda llamada con el mismo prompt no debería volver a llamar Search — antes %d, ahora %d", searchCallsAfterFirst, got)
	}
	if strings.Contains(buf2.String(), "[kronos:relevante]") {
		t.Errorf("segunda llamada no debería repetir un ítem ya inyectado en la primera: %q", buf2.String())
	}
	if elapsed > time.Second {
		// Cota holgada a propósito: la prueba real de este caso es que NO hubo
		// llamada nueva a Search ni embedding (assertions de arriba, deterministas).
		// Esta cota solo detecta una regresión gruesa (pagar un round-trip de
		// segundos) sin fallar por carga de la máquina — medido: 450ms con el
		// suite completo corriendo en paralelo, que es ruido de scheduler, no bug.
		t.Errorf("segunda llamada (cache hit, sin Search ni vector nuevos) tardó %v — debería ser prácticamente instantánea", elapsed)
	}
}

// latencyControlledFn es un embeddings.EmbeddingFunc de prueba que se demora
// slowFor SOLO cuando el texto pedido es slowText — permite dejar en el
// *VectorStore un lastLatency "lento" indexando un documento (Index también
// pasa por el mismo embedFn) y después verificar si runRecall llegó a
// intentar (o no) una consulta vectorial nueva para un texto distinto.
type latencyControlledFn struct {
	mu       sync.Mutex
	calls    int
	slowText string
	slowFor  time.Duration
}

func (f *latencyControlledFn) fn(ctx context.Context, text string) ([]float32, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if text == f.slowText {
		select {
		case <-time.After(f.slowFor):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []float32{1, 0}, nil
}

func (f *latencyControlledFn) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestRunPromptSubmit_VectorProbe_HotAttemptsColdSkips cubre (e): una
// observación indexada SOLO en el vector store (sin match FTS posible, el
// prompt no comparte ningún término real) se inyecta si el proveedor viene
// caliente (sin historial de latencia, o última llamada rápida) y NO se
// intenta el round-trip si la sonda detecta que el proveedor viene lento —
// el caso real que motiva esto: Ollama compartido con el daemon puede tardar
// hasta 6s, y arriesgar todo el presupuesto en un intento que probablemente
// no vuelva a tiempo es peor que no intentarlo.
func TestRunPromptSubmit_VectorProbe_HotAttemptsColdSkips(t *testing.T) {
	setupTempConfigDir(t)
	cfg := config.Default()
	cfg.Recall.VectorProbeMs = 100
	if err := cfg.Save(); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}

	ctx := context.Background()
	cwd, _ := os.Getwd()
	// prompt y obsText no comparten NINGÚN token significativo real (a
	// propósito, para que la guarda de precisión de FTS-OR devuelva 0 filas
	// y el único camino posible sea el vectorial) — solo pueden matchear vía
	// similitud coseno, nunca por FTS.
	prompt := "zzzvectorprobezzz articulo especial"
	obsText := "documento archivado ajeno sin relacion evidente"

	setup := func(t *testing.T, slowFor time.Duration) (*store.Store, *embeddings.VectorStore, *latencyControlledFn) {
		t.Helper()
		st := newTestStore(t)
		obs, err := st.SaveObservation(ctx, store.SaveParams{
			Type: store.TypeDiscovery, Title: "obs solo vector probe", Content: obsText, Project: "kronos-v2",
		})
		if err != nil {
			t.Fatal(err)
		}
		f := &latencyControlledFn{slowText: obsText, slowFor: slowFor}
		vs, err := embeddings.NewInMemory(f.fn)
		if err != nil {
			t.Fatal(err)
		}
		// Index ya deja registrada en vs la latencia de esta llamada (rápida
		// o lenta según slowFor) — es la "última llamada real" que la sonda
		// va a mirar antes del intento para prompt.
		if err := vs.Index(ctx, obs.ID, obsText); err != nil {
			t.Fatal(err)
		}
		return st, vs, f
	}

	t.Run("caliente: intenta y con similitud alta inyecta", func(t *testing.T) {
		st, vs, f := setup(t, 5*time.Millisecond) // muy por debajo de VectorProbeMs=100
		st.CreateSession(ctx, "sess-probe-hot", "kronos-v2", cwd)
		st.PersistInjectedIDs(ctx, "sess-probe-hot", []string{})

		in := hooks.Input{SessionID: "sess-probe-hot", CWD: cwd, Prompt: prompt}
		var buf bytes.Buffer
		if err := hooks.RunPromptSubmit(ctx, in, st, vs, &buf); err != nil {
			t.Fatalf("RunPromptSubmit: %v", err)
		}
		if !strings.Contains(buf.String(), "[kronos:relevante]") {
			t.Errorf("proveedor caliente debería intentar el vector e inyectar: %q", buf.String())
		}
		if got := f.count(); got < 2 {
			t.Errorf("proveedor caliente debería haber llamado al embed para el prompt (además de indexar) — llamadas=%d", got)
		}
	})

	t.Run("frío: se saltea el intento, nada inyectado", func(t *testing.T) {
		st, vs, f := setup(t, 500*time.Millisecond) // muy por encima de VectorProbeMs=100
		st.CreateSession(ctx, "sess-probe-cold", "kronos-v2", cwd)
		st.PersistInjectedIDs(ctx, "sess-probe-cold", []string{})

		callsAfterIndex := f.count()

		in := hooks.Input{SessionID: "sess-probe-cold", CWD: cwd, Prompt: prompt}
		var buf bytes.Buffer
		start := time.Now()
		if err := hooks.RunPromptSubmit(ctx, in, st, vs, &buf); err != nil {
			t.Fatalf("RunPromptSubmit: %v", err)
		}
		elapsed := time.Since(start)

		if strings.Contains(buf.String(), "[kronos:relevante]") {
			t.Errorf("proveedor frío no debería inyectar nada: %q", buf.String())
		}
		if got := f.count(); got != callsAfterIndex {
			t.Errorf("proveedor frío no debería haber intentado un embed nuevo para el prompt — antes %d, ahora %d", callsAfterIndex, got)
		}
		if elapsed > 10*time.Second {
			// Igual que en el test de cache: la prueba determinista de que la
			// sonda evitó el round-trip lento es que f.count() no cambió (arriba).
			// Esta cota es un backstop para una regresión gruesa (colgarse), no un
			// presupuesto de latencia: la máquina cargada (load 9-10) hacía tardar
			// 1.83 s al handler correcto, así que 300 ms y 1 s fallaban sin bug.
			t.Errorf("elapsed=%v — la sonda debería evitar pagar el round-trip lento (500ms) por completo", elapsed)
		}
	})
}
