package hooks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
	"github.com/jjgarcia-app/kronos-v2/internal/llm"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// isolatedDataDir aísla platform.DataDir() (de donde cuelga el archivo de
// reintentos pendientes de enriquecimiento, ver internal/llm.DigestPending)
// con un HOME/XDG_DATA_HOME temporales — sin esto, estos tests leerían y
// escribirían el estado real del usuario que corre la suite.
func isolatedDataDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	dataDir, err := platform.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	return dataDir
}

func ollamaDigestStub(t *testing.T, inner string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"response": inner})
	}))
}

// ollamaDigestStubSequence devuelve una respuesta distinta en cada llamada
// sucesiva (la última se repite si hay más llamadas que respuestas) — usado
// para probar el ciclo completo de un reintento: la primera llamada simula
// el tick original (prosa) y la segunda el reintento acotado de "solo
// hechos" en el siguiente tick, contra el mismo servidor. También guarda
// cada prompt recibido, para verificar qué se le pidió al modelo en cada
// llamada (ej. que el reintento no vuelve a mandar el prompt completo).
func ollamaDigestStubSequence(t *testing.T, responses []string) (srv *httptest.Server, prompts *[]string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt string `json:"prompt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		mu.Lock()
		idx := len(got)
		got = append(got, body.Prompt)
		mu.Unlock()

		inner := responses[len(responses)-1]
		if idx < len(responses) {
			inner = responses[idx]
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"response": inner})
	}))
	return srv, &got
}

// backdateObservationUpdatedAt fuerza updated_at al pasado — necesario para
// probar el camino "el digest existe pero ya venció" sin esperar de verdad
// digestInterval (20min por default) en el test.
func backdateObservationUpdatedAt(t *testing.T, st *store.Store, id int64, ts time.Time) {
	t.Helper()
	if _, err := st.DB().Exec(`UPDATE observations SET updated_at = ? WHERE id = ?`,
		ts.UTC().Format(time.RFC3339), id); err != nil {
		t.Fatal(err)
	}
}

func TestIsDigestDue_NoExistingDigest_True(t *testing.T) {
	st := newTestStore(t)
	if !hooks.IsDigestDue(context.Background(), st, config.Default(), "s1", "/tmp/kronos-v2") {
		t.Error("sin digest previo debería estar due")
	}
}

func TestIsDigestDue_RecentDigest_False(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeSession, Title: "Resumen de sesión s1 (automático)", Content: "c",
		Project: "kronos-v2", TopicKey: "session/s1",
	}); err != nil {
		t.Fatal(err)
	}

	if hooks.IsDigestDue(ctx, st, config.Default(), "s1", "/tmp/kronos-v2") {
		t.Error("un digest recién guardado no debería estar due")
	}
}

func TestIsDigestDue_EmptySessionID_False(t *testing.T) {
	if hooks.IsDigestDue(context.Background(), newTestStore(t), config.Default(), "", "/tmp/kronos-v2") {
		t.Error("sin session_id nunca está due")
	}
}

func TestIsDigestDue_DigestDisabled_False(t *testing.T) {
	cfg := config.Default()
	cfg.Digest.Enabled = false
	if hooks.IsDigestDue(context.Background(), newTestStore(t), cfg, "s1", "/tmp/kronos-v2") {
		t.Error("digest.enabled=false nunca debería estar due")
	}
}

// TestMaybeUpdateDigest_NilLLMClient_StillSavesDeterministic es la
// propiedad clave de este arreglo: antes, sin LLM disponible, el digest
// jamás se guardaba (ver git blame de este test). Ahora el determinístico
// no depende de Ollama para nada — se arma leyendo el transcript.
func TestMaybeUpdateDigest_NilLLMClient_StillSavesDeterministic(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"investigando un bug real de verdad, con bastante texto"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/repo/internal/foo.go"}}]}}`,
	})

	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), nil, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("no debería fallar con llmClient nil: %v", err)
	}
	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil {
		t.Fatal("sin LLM el digest determinístico debería haberse guardado igual")
	}
	if !strings.Contains(obs.Content, "investigando un bug real") {
		t.Errorf("Content no tiene el prompt: %q", obs.Content)
	}
	if !strings.Contains(obs.Content, "/repo/internal/foo.go") {
		t.Errorf("Content no tiene el archivo tocado: %q", obs.Content)
	}
	if obs.Type != store.TypeSession {
		t.Errorf("Type = %q, want %q", obs.Type, store.TypeSession)
	}
}

// TestMaybeUpdateDigest_EmptyTranscript_NoOp: transcript sin prompts ni
// archivos (por ejemplo, una cola con solo tool_result) no debe guardar
// basura.
func TestMaybeUpdateDigest_EmptyTranscript_NoOp(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"salida"}]}}`,
	})

	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), nil, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("no debería fallar: %v", err)
	}
	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil {
		t.Error("sin prompts ni archivos no debería haberse guardado nada")
	}
}

func TestMaybeUpdateDigest_DigestDisabled_NoOp(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cfg := config.Default()
	cfg.Digest.Enabled = false
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"prompt real con contenido de sobra"}}`,
	})

	if err := hooks.MaybeUpdateDigest(ctx, st, cfg, nil, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("no debería fallar: %v", err)
	}
	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil {
		t.Error("digest.enabled=false no debería guardar nada")
	}
}

// TestMaybeUpdateDigest_LLMEnriches_KeepsDeterministicBlock confirma la
// composición: cuando el LLM responde bien, el contenido guardado es la
// prosa del LLM MÁS el bloque determinístico al final — nunca se pierden
// los hechos concretos aunque el LLM resuma de más.
func TestMaybeUpdateDigest_LLMEnriches_KeepsDeterministicBlock(t *testing.T) {
	isolatedDataDir(t) // el stub no trae "facts" — sin aislar, marcaría un pendiente de hechos en el data dir real
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"por qué falla el build, llevo media hora viendo este error y no encuentro qué lo está causando"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"la causa era un import circular entre internal/foo e internal/bar, lo saqué a un paquete nuevo internal/shared"}}`,
	})
	srv := ollamaDigestStub(t, `{"content":"Investigado y arreglado import circular entre internal/foo y internal/bar"}`)
	defer srv.Close()

	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")
	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}

	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil {
		t.Fatal("esperaba un digest guardado")
	}
	if !strings.Contains(obs.Content, "import circular") {
		t.Errorf("Content no tiene la prosa del LLM: %q", obs.Content)
	}
	if !strings.Contains(obs.Content, "por qué falla el build") {
		t.Errorf("Content no tiene el bloque determinístico (prompt real): %q", obs.Content)
	}
	if !strings.Contains(obs.Content, "En qué se viene trabajando (automático, sin LLM)") {
		t.Errorf("Content no tiene el encabezado del bloque determinístico: %q", obs.Content)
	}
}

// TestMaybeUpdateDigest_LLMFails_KeepsDeterministicOnly confirma la
// propiedad principal del arreglo: si el LLM falla (acá, devuelve JSON
// inválido), el digest determinístico se guarda igual, sin el aporte del
// LLM.
func TestMaybeUpdateDigest_LLMFails_KeepsDeterministicOnly(t *testing.T) {
	isolatedDataDir(t) // la falla marca un pendiente — sin aislar, escribiría en el data dir real
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"prompt real con contenido de sobra para pasar el umbral del excerpt tambien"}}`,
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")
	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("no debería propagar el error del LLM: %v", err)
	}

	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil {
		t.Fatal("el determinístico debería haberse guardado pese a la falla del LLM")
	}
	if !strings.Contains(obs.Content, "prompt real con contenido de sobra") {
		t.Errorf("Content no tiene el prompt: %q", obs.Content)
	}
}

// TestMaybeUpdateDigest_LLMEnrichmentDisabled_DoesNotCallLLM confirma
// digest.llm=false: el LLM nunca se llama, aunque haya un cliente sano
// disponible.
func TestMaybeUpdateDigest_LLMEnrichmentDisabled_DoesNotCallLLM(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"response": `{"content":"x"}`})
	}))
	defer srv.Close()

	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"prompt real con contenido de sobra para pasar el umbral"}}`,
	})
	cfg := config.Default()
	cfg.Digest.LLMEnrichment = false
	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")
	if err := hooks.MaybeUpdateDigest(ctx, st, cfg, llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}
	if called {
		t.Error("digest.llm=false no debería haber llamado al LLM")
	}
	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || !strings.Contains(obs.Content, "prompt real") {
		t.Errorf("el determinístico debería haberse guardado igual, got: %+v", obs)
	}
}

// TestMaybeUpdateDigest_ExtendsPreviousDigest_UpsertsSameRow confirma el
// caso real que motiva topic_key: una segunda actualización sobre una
// sesión larga extiende el mismo resumen (mismo ID, revision_count sube),
// no crea una fila nueva por cada actualización periódica.
func TestMaybeUpdateDigest_ExtendsPreviousDigest_UpsertsSameRow(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}

	first, err := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeSession, Title: "Resumen de sesión s1 (automático)",
		Content: "- Arreglado bug de import circular", Project: "kronos-v2", TopicKey: "session/s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// vencer el digest a mano — si no, MaybeUpdateDigest lo salta por
	// "todavía no toca" y nunca llega a recomponerlo.
	backdateObservationUpdatedAt(t, st, first.ID, time.Now().Add(-30*time.Minute))

	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"ahora encontré otro bug distinto, en el módulo de autenticación"}}`,
	})

	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), nil, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}

	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs.ID != first.ID {
		t.Errorf("ID cambió de %d a %d — debería ser upsert (misma fila), no una nueva", first.ID, obs.ID)
	}
	if obs.RevisionCount != 2 {
		t.Errorf("RevisionCount = %d, want 2", obs.RevisionCount)
	}
}

// TestMaybeUpdateDigest_TooRecent_SkipsUpdate confirma que MaybeUpdateDigest
// respeta digestInterval — no alcanza con que IsDigestDue exista, el propio
// MaybeUpdateDigest tiene que volver a chequear.
func TestMaybeUpdateDigest_TooRecent_SkipsUpdate(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	saved, err := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeSession, Title: "Resumen de sesión s1 (automático)", Content: "c",
		Project: "kronos-v2", TopicKey: "session/s1",
	})
	if err != nil {
		t.Fatal(err)
	}

	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"texto de sobra para pasar el umbral mínimo"}}`,
	})
	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), nil, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}
	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs.RevisionCount != 1 || obs.ID != saved.ID {
		t.Errorf("un digest actualizado hace poco no debería haberse tocado, got RevisionCount=%d", obs.RevisionCount)
	}
}

// TestMaybeUpdateDigest_Force_IgnoresInterval confirma el caso real que
// motiva el parámetro force: PreCompact necesita que se actualice el
// digest aunque hayan pasado menos de digestInterval desde la última vez.
func TestMaybeUpdateDigest_Force_IgnoresInterval(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeSession, Title: "Resumen de sesión s1 (automático)", Content: "- Ya arreglado esto",
		Project: "kronos-v2", TopicKey: "session/s1",
	}); err != nil {
		t.Fatal(err)
	}

	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"encontré otro bug justo antes de compactar, en el módulo de exportación"}}`,
	})
	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), nil, "s1", path, "/tmp/kronos-v2", true); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}

	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs.RevisionCount != 2 {
		t.Errorf("RevisionCount = %d, want 2 — force debería haber actualizado pese al digest reciente", obs.RevisionCount)
	}
	if !strings.Contains(obs.Content, "justo antes de compactar") {
		t.Errorf("Content no tiene la actualización forzada: %q", obs.Content)
	}
}

func TestMaybeUpdateDigest_EmptySessionOrTranscriptPath_NoOp(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), nil, "", "/tmp/x.jsonl", "/tmp/kronos-v2", false); err != nil {
		t.Errorf("session_id vacío no debería fallar: %v", err)
	}
	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), nil, "s1", "", "/tmp/kronos-v2", false); err != nil {
		t.Errorf("transcript_path vacío no debería fallar: %v", err)
	}
}

// TestMaybeUpdateDigest_SessionRowMissing_StillSaves cubre el bug real medido
// en producción: la observación del digest se guarda con session_id y la tabla
// observations tiene FK a sessions. Si la fila de la sesión no existe todavía
// (SessionStart no corrió para esa sesión: sesión resumida, o el digest
// disparado desde el camino local del hook), el INSERT fallaba con
// "FOREIGN KEY constraint failed" y el caller descartaba el error: el digest
// se perdía en silencio, siempre.
//
// A diferencia del test anterior, acá NO se crea la sesión a propósito.
func TestMaybeUpdateDigest_SessionRowMissing_StillSaves(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"sesión resumida sin fila en la base, con texto suficiente"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Write","input":{"file_path":"/repo/internal/bar.go"}}]}}`,
	})

	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), nil, "sin-fila", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("con la sesión sin fila debería guardarse igual: %v", err)
	}
	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/sin-fila")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil {
		t.Fatal("el digest no se guardó: la observación con session_id necesita que la fila de sesión exista")
	}
	if !strings.Contains(obs.Content, "sesión resumida sin fila") {
		t.Errorf("Content inesperado: %q", obs.Content)
	}
	if sess, err := st.GetSession(ctx, "sin-fila"); err != nil || sess == nil {
		t.Errorf("la fila de la sesión debería haberse creado (err=%v)", err)
	}
}

// --- Tarea B: hechos estructurados promovidos desde la misma llamada del digest ---

// TestMaybeUpdateDigest_PromotesFacts_WithType confirma el comportamiento
// central de la Tarea B: un hecho que viene en la MISMA respuesta del LLM
// que la prosa del digest (ver llm.Client.UpdateDigest) se guarda como
// observación PROPIA con su tipo — no enterrado en el digest tipo "session".
func TestMaybeUpdateDigest_PromotesFacts_WithType(t *testing.T) {
	isolatedDataDir(t) // pending.Clear() escribe al data dir tras un enriquecimiento exitoso — aislar por higiene
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"por qué falla el build, llevo media hora viendo este error y no encuentro qué lo está causando"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"la causa era un import circular entre internal/foo e internal/bar, lo saqué a un paquete nuevo internal/shared"}}`,
	})
	srv := ollamaDigestStub(t, `{"content":"Investigado y arreglado import circular",
		"facts": [{"type":"bugfix","title":"Fix import circular entre foo y bar","content":"Qué: import circular. Por qué: paquetes acoplados. Cómo aplicar: separar en internal/shared."}]}`)
	defer srv.Close()

	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")
	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}

	obs, err := st.ListAll(ctx, "kronos-v2")
	if err != nil {
		t.Fatal(err)
	}
	var fact *store.Observation
	for _, o := range obs {
		if o.Type == store.TypeBugfix {
			fact = o
		}
	}
	if fact == nil {
		t.Fatalf("esperaba una observación tipo bugfix promovida desde el digest, got: %+v", obs)
	}
	if fact.Title != "Fix import circular entre foo y bar" {
		t.Errorf("Title = %q", fact.Title)
	}
	if fact.SessionID != "s1" {
		t.Errorf("SessionID = %q, want s1", fact.SessionID)
	}
}

// TestMaybeUpdateDigest_SingleLLMCall_NoExtraUsage confirma que promover
// hechos no agrega una llamada nueva al LLM: es la MISMA respuesta que ya
// generaba la prosa, así que el contador de uso sube en 1, no en 2.
func TestMaybeUpdateDigest_SingleLLMCall_NoExtraUsage(t *testing.T) {
	isolatedDataDir(t) // pending.Clear() escribe al data dir tras un enriquecimiento exitoso — aislar por higiene
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"investigando por qué el build falla desde ayer a la tarde, revisé el import circular entre dos paquetes internos, reproduje el error paso a paso y encontré la causa raíz después de bastante rato de revisar logs y stack traces largos"}}`,
	})
	srv := ollamaDigestStub(t, `{"content":"resumen","facts":[{"type":"decision","title":"Decisión de prueba con largo suficiente","content":"contenido de prueba con largo suficiente para no descartarse"}]}`)
	defer srv.Close()

	usage := llm.NewUsage(t.TempDir() + "/usage.json")
	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")
	llmClient.SetUsage(usage)

	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}

	total := 0
	for _, b := range usage.State().Buckets {
		total += b.Count
	}
	if total != 1 {
		t.Errorf("contador de uso = %d, want 1 (una sola llamada LLM por actualización de digest, con o sin hechos)", total)
	}
}

// TestMaybeUpdateDigest_RerunSameFacts_DoesNotDuplicate confirma el dedupe:
// re-correr el digest con el mismo hecho (mismo título+contenido) no crea
// una fila nueva — SaveObservation ya dedupea por hash, esto verifica que
// promoteDigestFacts lo aprovecha en vez de esquivarlo con un topic_key.
func TestMaybeUpdateDigest_RerunSameFacts_DoesNotDuplicate(t *testing.T) {
	isolatedDataDir(t) // pending.Clear() escribe al data dir tras un enriquecimiento exitoso — aislar por higiene
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"investigando por qué el build falla desde ayer a la tarde, revisé el import circular entre dos paquetes internos, reproduje el error paso a paso y encontré la causa raíz después de bastante rato de revisar logs y stack traces largos (segunda corrida, mismo excerpt largo para pasar el umbral otra vez)"}}`,
	})
	factJSON := `{"type":"config","title":"Cambio de config repetido en dos corridas","content":"contenido idéntico entre la primera y la segunda corrida del digest"}`
	srv := ollamaDigestStub(t, `{"content":"resumen","facts":[`+factJSON+`]}`)
	defer srv.Close()
	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")

	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), llmClient, "s1", path, "/tmp/kronos-v2", true); err != nil {
		t.Fatalf("MaybeUpdateDigest (1): %v", err)
	}
	countType := func() (n int, dup int) {
		obs, err := st.ListAll(ctx, "kronos-v2")
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range obs {
			if o.Type == store.TypeConfig {
				n++
				dup = o.DuplicateCount
			}
		}
		return
	}
	n1, _ := countType()
	if n1 != 1 {
		t.Fatalf("setup inválido: esperaba 1 hecho config tras la primera corrida, got %d", n1)
	}

	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), llmClient, "s1", path, "/tmp/kronos-v2", true); err != nil {
		t.Fatalf("MaybeUpdateDigest (2): %v", err)
	}
	n2, dup2 := countType()
	if n2 != 1 {
		t.Errorf("re-correr el digest con el mismo hecho creó una fila nueva — filas=%d, want 1", n2)
	}
	if dup2 < 2 {
		t.Errorf("duplicate_count = %d, want >= 2 (bumpeado por el dedupe de SaveObservation)", dup2)
	}
}

// TestMaybeUpdateDigest_DiscardsFactsWithInvalidType confirma que un tipo
// fuera del whitelist (acá "session", justo el tipo que este cambio busca
// dejar de sobrecargar) se descarta en vez de colarse como observación.
func TestMaybeUpdateDigest_DiscardsFactsWithInvalidType(t *testing.T) {
	isolatedDataDir(t) // pending.Clear() escribe al data dir tras un enriquecimiento exitoso — aislar por higiene
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"investigando por qué el build falla desde ayer a la tarde, revisé el import circular entre dos paquetes internos, reproduje el error paso a paso y encontré la causa raíz después de bastante rato de revisar logs y stack traces largos"}}`,
	})
	srv := ollamaDigestStub(t, `{"content":"resumen","facts":[{"type":"session","title":"Hecho con tipo invalido de sobra","content":"contenido de sobra para no descartarse por longitud"}]}`)
	defer srv.Close()
	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")

	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}
	obs, err := st.ListAll(ctx, "kronos-v2")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range obs {
		if o.Title == "Hecho con tipo invalido de sobra" {
			t.Errorf("un hecho con type=session no debería promoverse — se coló: %+v", o)
		}
	}
}

// TestMaybeUpdateDigest_DiscardsTooShortFacts confirma el piso de longitud:
// un hecho genérico/truncado ("se corrieron tests", o peor, casi vacío) no
// vale la pena guardarlo como observación propia.
func TestMaybeUpdateDigest_DiscardsTooShortFacts(t *testing.T) {
	isolatedDataDir(t) // pending.Clear() escribe al data dir tras un enriquecimiento exitoso — aislar por higiene
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"investigando por qué el build falla desde ayer a la tarde, revisé el import circular entre dos paquetes internos, reproduje el error paso a paso y encontré la causa raíz después de bastante rato de revisar logs y stack traces largos"}}`,
	})
	srv := ollamaDigestStub(t, `{"content":"resumen","facts":[{"type":"discovery","title":"corto","content":"corto"}]}`)
	defer srv.Close()
	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")

	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}
	obs, err := st.ListAll(ctx, "kronos-v2")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range obs {
		if o.Type == store.TypeDiscovery {
			t.Errorf("un hecho demasiado corto no debería promoverse — se coló: %+v", o)
		}
	}
}

// TestMaybeUpdateDigest_MaxFactsCap confirma digest.max_facts: con más
// hechos propuestos que el tope, solo se guardan los primeros N.
func TestMaybeUpdateDigest_MaxFactsCap(t *testing.T) {
	isolatedDataDir(t) // pending.Clear() escribe al data dir tras un enriquecimiento exitoso — aislar por higiene
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"investigando por qué el build falla desde ayer a la tarde, revisé el import circular entre dos paquetes internos, reproduje el error paso a paso y encontré la causa raíz después de bastante rato de revisar logs y stack traces largos"}}`,
	})
	facts := ""
	for i := 0; i < 5; i++ {
		if i > 0 {
			facts += ","
		}
		facts += fmt.Sprintf(`{"type":"pattern","title":"Patrón número %d con largo suficiente","content":"contenido de prueba con largo suficiente para no descartarse jamás"}`, i)
	}
	srv := ollamaDigestStub(t, `{"content":"resumen","facts":[`+facts+`]}`)
	defer srv.Close()
	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")

	cfg := config.Default()
	cfg.Digest.MaxFacts = 2
	if err := hooks.MaybeUpdateDigest(ctx, st, cfg, llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}
	obsList, err := st.ListAll(ctx, "kronos-v2")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, o := range obsList {
		if o.Type == store.TypePattern {
			n++
		}
	}
	if n != 2 {
		t.Errorf("con max_facts=2 y 5 hechos propuestos, se guardaron %d — want 2", n)
	}
}

// TestMaybeUpdateDigest_PromoteFactsDisabled_NoFactsSaved confirma
// digest.promote_facts=false: aunque el LLM proponga hechos válidos, no se
// guarda ninguno como observación propia (el digest tipo session sí, igual
// que siempre).
func TestMaybeUpdateDigest_PromoteFactsDisabled_NoFactsSaved(t *testing.T) {
	isolatedDataDir(t) // pending.Clear() escribe al data dir tras un enriquecimiento exitoso — aislar por higiene
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"investigando por qué el build falla desde ayer a la tarde, revisé el import circular entre dos paquetes internos, reproduje el error paso a paso y encontré la causa raíz después de bastante rato de revisar logs y stack traces largos"}}`,
	})
	srv := ollamaDigestStub(t, `{"content":"resumen","facts":[{"type":"preference","title":"Preferencia con largo suficiente","content":"contenido de prueba con largo suficiente para no descartarse"}]}`)
	defer srv.Close()
	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")

	cfg := config.Default()
	cfg.Digest.PromoteFacts = false
	if err := hooks.MaybeUpdateDigest(ctx, st, cfg, llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}
	obs, err := st.ListAll(ctx, "kronos-v2")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range obs {
		if o.Type == store.TypePreference {
			t.Errorf("promote_facts=false no debería haber guardado ningún hecho, se coló: %+v", o)
		}
	}
}

// TestMaybeUpdateDigest_MalformedFactsJSON_KeepsContent confirma la
// tolerancia al parseo: "facts" con una forma inesperada (acá, un string en
// vez de un array de objetos) no debe tirar abajo el digest en prosa, que es
// lo que ya funcionaba antes de que existiera esta sección.
func TestMaybeUpdateDigest_MalformedFactsJSON_KeepsContent(t *testing.T) {
	isolatedDataDir(t) // facts malformado marca pendiente de hechos — sin aislar, escribiría en el data dir real
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"investigando por qué el build falla desde ayer a la tarde, revisé el import circular entre dos paquetes internos, reproduje el error paso a paso y encontré la causa raíz después de bastante rato de revisar logs y stack traces largos"}}`,
	})
	srv := ollamaDigestStub(t, `{"content":"resumen en prosa pese a facts malformado","facts":"esto no es un array"}`)
	defer srv.Close()
	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")

	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("facts malformado no debería hacer fallar el digest: %v", err)
	}
	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || !strings.Contains(obs.Content, "resumen en prosa pese a facts malformado") {
		t.Errorf("el contenido en prosa debería haberse guardado igual, got: %+v", obs)
	}
}

// --- Tema 1: reintento del enriquecimiento tras timeout/pico de carga ---

// slowOllamaStub simula un LLM que tarda más de lo que digest.timeout_ms le
// permite — para forzar el mismo error de contexto vencido que un pico de
// carga real produce contra claude-cli, sin depender de un CLI real.
func slowOllamaStub(t *testing.T, delay time.Duration, inner string) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"response": inner})
	}))
	return srv, &calls
}

// TestMaybeUpdateDigest_EnrichmentTimesOut_SavesDeterministicAndMarksPending
// confirma la propiedad central del Tema 1: un enriquecimiento que se pasa
// del presupuesto de digest.timeout_ms no pierde nada (el determinístico se
// guarda igual, fail-open de siempre) y además deja la sesión anotada para
// reintentar en la próxima oportunidad, sin esperar a que venza
// digest.interval_minutes de nuevo.
func TestMaybeUpdateDigest_EnrichmentTimesOut_SavesDeterministicAndMarksPending(t *testing.T) {
	dataDir := isolatedDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"investigando un pico de carga que hizo fallar el enriquecimiento del digest, con texto de sobra para pasar el umbral mínimo del excerpt que exige TailExcerpt antes de siquiera intentar la llamada al LLM"}}`,
	})
	srv, _ := slowOllamaStub(t, 500*time.Millisecond, `{"content":"no debería llegar a usarse"}`)
	defer srv.Close()

	cfg := config.Default()
	cfg.Digest.TimeoutMs = 5 // imposible de cumplir con el stub de 500ms — fuerza el timeout con margen amplio
	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")

	if err := hooks.MaybeUpdateDigest(ctx, st, cfg, llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("un timeout del LLM no debería propagarse como error: %v", err)
	}

	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || !strings.Contains(obs.Content, "pico de carga que hizo fallar") {
		t.Fatalf("el determinístico debería haberse guardado igual pese al timeout, got: %+v", obs)
	}

	pending := llm.NewDigestPending(llm.DefaultDigestPendingPath(dataDir))
	if !pending.Due("s1") {
		t.Error("tras un timeout del enriquecimiento, la sesión debería quedar anotada como pendiente de reintento")
	}
	if !hooks.IsDigestDue(ctx, st, cfg, "s1", "/tmp/kronos-v2") {
		t.Error("con un reintento pendiente, IsDigestDue debería dar true aunque el digest se acaba de actualizar")
	}
}

// TestMaybeUpdateDigest_RetrySucceeds_PromotesFactAndClearsPending confirma
// el segundo tramo del Tema 1: en el siguiente MaybeUpdateDigest de una
// sesión con enriquecimiento pendiente, se reintenta AUNQUE todavía no toque
// digest.interval_minutes — y si esta vez el LLM responde bien, se promueve
// el hecho tipado (mismo promoteDigestFacts de siempre) y se limpia el
// pendiente, sin duplicar la observación de digest (mismo topic_key).
func TestMaybeUpdateDigest_RetrySucceeds_PromotesFactAndClearsPending(t *testing.T) {
	dataDir := isolatedDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}

	// Simula el estado que deja un ciclo anterior fallido: el determinístico
	// ya guardado (recién, no vencido) y la sesión marcada como pendiente.
	first, err := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeSession, Title: "Resumen de sesión s1 (automático)",
		Content: "## En qué se viene trabajando (automático, sin LLM)\n- prompt viejo", Project: "kronos-v2", TopicKey: "session/s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	pending := llm.NewDigestPending(llm.DefaultDigestPendingPath(dataDir))
	pending.MarkFailed("s1")

	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"por qué falla el build, llevo media hora viendo este error y no encuentro qué lo está causando"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"la causa era un import circular entre internal/foo e internal/bar, lo saqué a un paquete nuevo internal/shared"}}`,
	})
	srv := ollamaDigestStub(t, `{"content":"Investigado y arreglado import circular (reintento exitoso)",
		"facts": [{"type":"bugfix","title":"Fix import circular entre foo y bar","content":"Qué: import circular. Por qué: paquetes acoplados. Cómo aplicar: separar en internal/shared."}]}`)
	defer srv.Close()
	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")

	// force=false y el digest recién se guardó (no venció digestInterval) —
	// sin el reintento pendiente, MaybeUpdateDigest lo saltaría por "todavía
	// no toca".
	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}

	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs.ID != first.ID {
		t.Errorf("ID cambió de %d a %d — el reintento debería actualizar la misma observación (upsert), no crear una nueva", first.ID, obs.ID)
	}
	if !strings.Contains(obs.Content, "reintento exitoso") {
		t.Errorf("Content no tiene la prosa del reintento: %q", obs.Content)
	}

	allObs, err := st.ListAll(ctx, "kronos-v2")
	if err != nil {
		t.Fatal(err)
	}
	var fact *store.Observation
	for _, o := range allObs {
		if o.Type == store.TypeBugfix {
			fact = o
		}
	}
	if fact == nil {
		t.Fatalf("esperaba una observación tipo bugfix promovida por el reintento, got: %+v", allObs)
	}

	if pending.Due("s1") {
		t.Error("tras un reintento exitoso, el pendiente debería haberse limpiado")
	}
}

// erroringOllamaStub responde 500 SIN demora — a diferencia de
// slowOllamaStub (pensado para el timeout), esta falla es determinística:
// no depende de que un timeout gane una carrera contra la máquina bajo
// carga, así que sirve para contar llamadas con un tope exacto sin
// aserciones de reloj.
func erroringOllamaStub(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	return srv, &calls
}

// TestMaybeUpdateDigest_RetriesExhausted_StopsAttemptingLLM confirma el
// tope: tras digestPendingMaxAttempts fallos consecutivos del enriquecimiento
// para la misma sesión, se deja de reintentar antes de que toque el
// intervalo normal de nuevo — no tiene sentido seguir golpeando un LLM que
// ya falló repetidas veces para esta sesión puntual. La falla es
// determinística (500 instantáneo, ver erroringOllamaStub), no un timeout:
// contar llamadas exactas no debe depender de ganarle una carrera al reloj
// en una máquina bajo carga.
func TestMaybeUpdateDigest_RetriesExhausted_StopsAttemptingLLM(t *testing.T) {
	isolatedDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	srv, calls := erroringOllamaStub(t)
	defer srv.Close()

	cfg := config.Default()
	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")

	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"texto de sobra para pasar el umbral mínimo del excerpt en cada intento del reintento de enriquecimiento, repetido varias veces para asegurar con margen de sobra que supere ampliamente los 200 caracteres que exige TailExcerpt antes de intentar la llamada"}}`,
	})

	// 4 llamadas seguidas, sin que pase tiempo real entre ellas: la primera
	// es due porque no hay digest previo; las siguientes 2 son due solo
	// porque el pendiente sigue vigente (no por el intervalo, que no venció);
	// la 4ta ya no debería ni intentar el LLM.
	for i := 0; i < 4; i++ {
		if err := hooks.MaybeUpdateDigest(ctx, st, cfg, llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
			t.Fatalf("llamada %d: %v", i+1, err)
		}
	}

	if *calls != 3 {
		t.Errorf("llamadas al LLM = %d, want 3 (tope de reintentos alcanzado, la 4ta no debería haber llamado)", *calls)
	}
}

// --- Tema 1: los hechos del digest dejan de perderse en silencio ---
//
// El hueco medido en producción: la llamada de enriquecimiento sale bien
// (guarda prosa), pero la respuesta no trae una sección de hechos explícita
// — hoy eso no queda pendiente, así que el aprendizaje se pierde en silencio.
// Los tres tests que siguen cubren los tres desenlaces posibles: (a) hechos
// en la primera respuesta (ya cubierto arriba por
// TestMaybeUpdateDigest_PromotesFacts_WithType), (b) prosa sin sección de
// hechos → pendiente de "solo hechos", y (c) el modelo dice explícitamente
// que no hay nada → sin pendiente.

// TestMaybeUpdateDigest_ProseWithoutFactsField_MarksFactsPending confirma el
// caso (b): la prosa se guarda igual, pero al no venir "facts" en la
// respuesta (ni lista ni ausencia explícita) la sesión queda anotada como
// pendiente de SOLO hechos — no de enriquecimiento completo, que ya se logró.
func TestMaybeUpdateDigest_ProseWithoutFactsField_MarksFactsPending(t *testing.T) {
	dataDir := isolatedDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"por qué falla el build, llevo media hora viendo este error y no encuentro qué lo está causando"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"la causa era un import circular entre internal/foo e internal/bar, lo saqué a un paquete nuevo internal/shared"}}`,
	})
	srv := ollamaDigestStub(t, `{"content":"prosa sin sección de hechos"}`)
	defer srv.Close()

	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")
	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}

	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || !strings.Contains(obs.Content, "prosa sin sección de hechos") {
		t.Fatalf("la prosa debería haberse guardado igual, got: %+v", obs)
	}

	pending := llm.NewDigestPending(llm.DefaultDigestPendingPath(dataDir))
	if kind := pending.PendingKind("s1"); kind != llm.DigestPendingKindFacts {
		t.Errorf("PendingKind = %q, want %q (éxito en la prosa, hechos sin respuesta explícita)", kind, llm.DigestPendingKindFacts)
	}
}

// TestMaybeUpdateDigest_ExplicitEmptyFacts_NoPending confirma el caso (c):
// el modelo respeta el formato pedido y devuelve "facts": [] — eso es una
// respuesta explícita de "no hay nada que promover", así que no debe quedar
// ningún pendiente (reintentar sería trabajo al pedo).
func TestMaybeUpdateDigest_ExplicitEmptyFacts_NoPending(t *testing.T) {
	dataDir := isolatedDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"por qué falla el build, llevo media hora viendo este error y no encuentro qué lo está causando"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"la causa era un import circular entre internal/foo e internal/bar, lo saqué a un paquete nuevo internal/shared"}}`,
	})
	srv := ollamaDigestStub(t, `{"content":"prosa sin novedades","facts":[]}`)
	defer srv.Close()

	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")
	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}

	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || !strings.Contains(obs.Content, "prosa sin novedades") {
		t.Fatalf("la prosa del LLM debería haberse guardado (confirma que la llamada sí se hizo): %+v", obs)
	}

	pending := llm.NewDigestPending(llm.DefaultDigestPendingPath(dataDir))
	if kind := pending.PendingKind("s1"); kind != "" {
		t.Errorf("PendingKind = %q, want \"\" (facts:[] es una respuesta explícita, no hay nada que reintentar)", kind)
	}
}

// TestMaybeUpdateDigest_FactsOnlyRetry_PromotesFactWithoutResendingProse
// confirma el ciclo completo del caso (b): en el tick siguiente a una prosa
// sin hechos, MaybeUpdateDigest reintenta con el pedido ACOTADO (sin volver a
// pedir la prosa, que ya está guardada), y si esta vez el modelo responde con
// hechos, se promueven y el pendiente se limpia — preservando el contenido ya
// guardado en el tick anterior en vez de pisarlo con el determinístico
// recién calculado.
func TestMaybeUpdateDigest_FactsOnlyRetry_PromotesFactWithoutResendingProse(t *testing.T) {
	isolatedDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"por qué falla el build, llevo media hora viendo este error y no encuentro qué lo está causando"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"la causa era un import circular entre internal/foo e internal/bar, lo saqué a un paquete nuevo internal/shared"}}`,
	})
	srv, prompts := ollamaDigestStubSequence(t, []string{
		`{"content":"prosa original guardada en el primer tick"}`,
		`[{"type":"bugfix","title":"Fix vía reintento acotado de hechos","content":"Qué: import circular. Por qué: paquetes acoplados. Cómo aplicar: separar en internal/shared."}]`,
	})
	defer srv.Close()
	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")

	// Tick 1: prosa sin hechos — deja el pendiente de "solo hechos" (caso b).
	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("MaybeUpdateDigest (tick 1): %v", err)
	}

	// Tick 2: force=false y el digest recién se guardó (no venció
	// digestInterval) — sin el pendiente de hechos, MaybeUpdateDigest lo
	// saltearía por "todavía no toca".
	if err := hooks.MaybeUpdateDigest(ctx, st, config.Default(), llmClient, "s1", path, "/tmp/kronos-v2", false); err != nil {
		t.Fatalf("MaybeUpdateDigest (tick 2): %v", err)
	}

	if len(*prompts) != 2 {
		t.Fatalf("llamadas al LLM = %d, want 2", len(*prompts))
	}
	if strings.Contains((*prompts)[1], "You maintain a running summary") {
		t.Error("el reintento de hechos no debería reenviar el prompt completo de prosa")
	}
	if !strings.Contains((*prompts)[1], "extracting standalone facts") {
		t.Errorf("el reintento debería usar el prompt acotado de solo hechos, prompt = %q", (*prompts)[1])
	}

	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(obs.Content, "prosa original guardada en el primer tick") {
		t.Errorf("el reintento de hechos no debería pisar la prosa ya guardada, Content = %q", obs.Content)
	}

	allObs, err := st.ListAll(ctx, "kronos-v2")
	if err != nil {
		t.Fatal(err)
	}
	var fact *store.Observation
	for _, o := range allObs {
		if o.Type == store.TypeBugfix {
			fact = o
		}
	}
	if fact == nil {
		t.Fatalf("esperaba una observación tipo bugfix promovida por el reintento acotado, got: %+v", allObs)
	}

	dataDir, err := platform.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	pending := llm.NewDigestPending(llm.DefaultDigestPendingPath(dataDir))
	if kind := pending.PendingKind("s1"); kind != "" {
		t.Errorf("PendingKind = %q, want \"\" tras el reintento exitoso", kind)
	}
}
