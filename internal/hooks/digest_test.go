package hooks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
// digestUpdateInterval (20min) en el test.
func backdateObservationUpdatedAt(t *testing.T, st *store.Store, id int64, ts time.Time) {
	t.Helper()
	if _, err := st.DB().Exec(`UPDATE observations SET updated_at = ? WHERE id = ?`,
		ts.UTC().Format(time.RFC3339), id); err != nil {
		t.Fatal(err)
	}
}

func TestIsDigestDue_NoExistingDigest_True(t *testing.T) {
	st := newTestStore(t)
	if !hooks.IsDigestDue(context.Background(), st, "s1", "/tmp/kronos-v2") {
		t.Error("sin digest previo debería estar due")
	}
}

func TestIsDigestDue_RecentDigest_False(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeSession, Title: "Resumen en curso de la sesión", Content: "c",
		Project: "kronos-v2", TopicKey: "session/s1",
	}); err != nil {
		t.Fatal(err)
	}

	if hooks.IsDigestDue(ctx, st, "s1", "/tmp/kronos-v2") {
		t.Error("un digest recién guardado no debería estar due")
	}
}

func TestIsDigestDue_EmptySessionID_False(t *testing.T) {
	st := newTestStore(t)
	if hooks.IsDigestDue(context.Background(), st, "", "/tmp/kronos-v2") {
		t.Error("sin session_id nunca está due")
	}
}

func TestMaybeUpdateDigest_NilLLMClient_NoOp(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"investigando un bug real de verdad, con bastante texto para pasar el umbral mínimo del excerpt"}}`,
	})

	if err := hooks.MaybeUpdateDigest(ctx, st, nil, "s1", path, "/tmp/kronos-v2"); err != nil {
		t.Fatalf("no debería fallar con llmClient nil: %v", err)
	}
	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil {
		t.Error("sin LLM no debería haberse creado ningún digest")
	}
}

// TestMaybeUpdateDigest_FirstUpdate_CreatesDigest cubre el camino feliz de
// la primera actualización: sin digest previo, guarda uno nuevo con
// type=session y topic_key session/<id>.
func TestMaybeUpdateDigest_FirstUpdate_CreatesDigest(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"por qué falla el build, llevo media hora viendo este error y no encuentro qué lo está causando"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"la causa era un import circular entre internal/foo e internal/bar, lo saqué a un paquete nuevo internal/shared"}}`,
	})
	srv := ollamaDigestStub(t, `{"content":"- Investigado y arreglado import circular entre internal/foo y internal/bar"}`)
	defer srv.Close()

	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")
	if err := hooks.MaybeUpdateDigest(ctx, st, llmClient, "s1", path, "/tmp/kronos-v2"); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}

	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil {
		t.Fatal("esperaba un digest guardado")
	}
	if obs.Type != store.TypeSession {
		t.Errorf("Type = %q, want %q", obs.Type, store.TypeSession)
	}
	if !strings.Contains(obs.Content, "import circular") {
		t.Errorf("Content no tiene el resumen: %q", obs.Content)
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
		Type: store.TypeSession, Title: "Resumen en curso de la sesión",
		Content: "- Arreglado bug de import circular", Project: "kronos-v2", TopicKey: "session/s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// vencer el digest a mano — si no, MaybeUpdateDigest lo salta por
	// "todavía no toca" y nunca llega a llamar al LLM.
	backdateObservationUpdatedAt(t, st, first.ID, time.Now().Add(-30*time.Minute))

	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"ahora encontré otro bug distinto, en el módulo de autenticación, tampoco es obvio"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"era un token expirado que no se refrescaba — el fix fue agregar el refresh automático antes de cada request"}}`,
	})
	var gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt string `json:"prompt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotPrompt = body.Prompt
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"response": `{"content":"- Arreglado bug de import circular\n- Arreglado token expirado en autenticación"}`,
		})
	}))
	defer srv.Close()

	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")
	if err := hooks.MaybeUpdateDigest(ctx, st, llmClient, "s1", path, "/tmp/kronos-v2"); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}

	if !strings.Contains(gotPrompt, "Arreglado bug de import circular") {
		t.Error("el prompt al LLM debería incluir el resumen previo para extenderlo")
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
	if !strings.Contains(obs.Content, "token expirado") {
		t.Errorf("Content no tiene la extensión: %q", obs.Content)
	}
}

// TestMaybeUpdateDigest_TooRecent_SkipsLLMCall confirma que MaybeUpdateDigest
// respeta digestUpdateInterval — no alcanza con que IsDigestDue exista, el
// propio MaybeUpdateDigest tiene que volver a chequear (es seguro llamarlo
// directo sin depender de un chequeo previo del caller).
func TestMaybeUpdateDigest_TooRecent_SkipsLLMCall(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeSession, Title: "Resumen en curso de la sesión", Content: "c",
		Project: "kronos-v2", TopicKey: "session/s1",
	}); err != nil {
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
		`{"type":"user","message":{"role":"user","content":"texto de sobra para pasar el umbral mínimo del excerpt sin problema"}}`,
	})
	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")
	if err := hooks.MaybeUpdateDigest(ctx, st, llmClient, "s1", path, "/tmp/kronos-v2"); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}
	if called {
		t.Error("digest actualizado hace poco no debería haber llamado al LLM")
	}
}

// TestMaybeUpdateDigest_LLMReturnsUnchanged_NoWrite confirma que si el LLM
// dice explícitamente "nada nuevo" (devuelve el resumen previo tal cual),
// no se pisa la fila con una revisión idéntica.
func TestMaybeUpdateDigest_LLMReturnsUnchanged_NoWrite(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	previous, err := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeSession, Title: "Resumen en curso de la sesión",
		Content: "- Ya arreglado esto", Project: "kronos-v2", TopicKey: "session/s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	backdateObservationUpdatedAt(t, st, previous.ID, time.Now().Add(-30*time.Minute))

	srv := ollamaDigestStub(t, `{"content":"- Ya arreglado esto"}`)
	defer srv.Close()

	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"charla intrascendente sin ningún avance real, solo para pasar el umbral mínimo"}}`,
	})
	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")
	if err := hooks.MaybeUpdateDigest(ctx, st, llmClient, "s1", path, "/tmp/kronos-v2"); err != nil {
		t.Fatalf("MaybeUpdateDigest: %v", err)
	}

	obs, err := st.GetByTopicKey(ctx, "kronos-v2", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if obs.RevisionCount != 1 {
		t.Errorf("RevisionCount = %d, want 1 (sin cambios reales, no debió reescribirse)", obs.RevisionCount)
	}
}
