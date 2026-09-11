package hooks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
	"github.com/jjgarcia-app/kronos-v2/internal/llm"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func ollamaDigestStub(t *testing.T, inner string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"response": inner})
	}))
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
