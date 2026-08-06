package hooks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
	"github.com/jjgarcia-app/kronos-v2/internal/llm"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func writeTestTranscript(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func ollamaExtractStub(t *testing.T, inner string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"response": inner})
	}))
}

// TestRunPreCompactCapture_NilLLMClient_NoOp confirma el fail-open: sin
// Ollama disponible, no hay captura pasiva y no hay error — mismo contrato
// que el resto de las features que dependen de Ollama en este codebase.
func TestRunPreCompactCapture_NilLLMClient_NoOp(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	path := writeTestTranscript(t, []string{
		`{"type":"assistant","message":{"role":"assistant","content":"encontré y arreglé la causa raíz de un bug real"}}`,
	})

	if err := hooks.RunPreCompactCapture(ctx, st, nil, "s1", path, "/tmp/proj"); err != nil {
		t.Fatalf("no debería fallar con llmClient nil: %v", err)
	}
	obs, err := st.ListSessionObservations(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 0 {
		t.Errorf("sin LLM no debería guardarse nada, got %d observaciones", len(obs))
	}
}

// TestRunPreCompactCapture_FindingSaved cubre el camino feliz de punta a
// punta: transcript real → excerpt → LLM (mockeado) → SaveObservation con
// type=passive.
func TestRunPreCompactCapture_FindingSaved(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "s1", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"por qué falla el build, llevo media hora viendo este error y no encuentro qué lo está causando"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"la causa era un import circular entre internal/foo e internal/bar — go build no lo reporta como tal, solo dice symbol not found, lo saqué a un paquete nuevo internal/shared y quedó resuelto"}}`,
	})
	srv := ollamaExtractStub(t, `{"found":true,"title":"Fix import circular en internal/foo","content":"Qué: import circular.\nPor qué: paquete mal separado.\nCómo aplicar: extraer a paquete nuevo."}`)
	defer srv.Close()

	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")
	if err := hooks.RunPreCompactCapture(ctx, st, llmClient, "s1", path, "/tmp/kronos-v2"); err != nil {
		t.Fatalf("RunPreCompactCapture: %v", err)
	}

	obs, err := st.ListSessionObservations(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 {
		t.Fatalf("esperaba 1 observación guardada, got %d", len(obs))
	}
	if obs[0].Type != store.TypePassive {
		t.Errorf("Type = %q, want %q", obs[0].Type, store.TypePassive)
	}
	if obs[0].Title != "Fix import circular en internal/foo" {
		t.Errorf("Title = %q", obs[0].Title)
	}
	if !strings.Contains(obs[0].Content, "import circular") {
		t.Errorf("Content no tiene el hallazgo: %q", obs[0].Content)
	}
}

// TestRunPreCompactCapture_LLMSaysNotFound_NoSave confirma que un excerpt
// real pero sin nada save-worthy (según el LLM) no genera una observación.
func TestRunPreCompactCapture_LLMSaysNotFound_NoSave(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"qué hora es, y aparte quería preguntarte algo sobre el clima de hoy y si crees que va a llover más tarde"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"no tengo acceso al reloj del sistema ni a datos de clima en tiempo real, no puedo responder ninguna de las dos preguntas con certeza"}}`,
	})
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"response": `{"found":false}`})
	}))
	defer srv.Close()

	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")
	if err := hooks.RunPreCompactCapture(ctx, st, llmClient, "s1", path, "/tmp/proj"); err != nil {
		t.Fatalf("RunPreCompactCapture: %v", err)
	}
	if !called {
		t.Fatal("el excerpt debería haber superado el mínimo y llegado a llamar al LLM")
	}

	obs, err := st.ListSessionObservations(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 0 {
		t.Errorf("LLM dijo found:false, no debería haberse guardado nada, got %d", len(obs))
	}
}

// TestRunPreCompactCapture_ShortExcerpt_SkipsLLMCall_NoSave confirma que un
// transcript casi sin texto real (por debajo del umbral mínimo) ni siquiera
// llega a llamar al LLM.
func TestRunPreCompactCapture_ShortExcerpt_SkipsLLMCall_NoSave(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	path := writeTestTranscript(t, []string{
		`{"type":"user","message":{"role":"user","content":"ok"}}`,
	})

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"response": `{"found":true,"title":"x","content":"x"}`})
	}))
	defer srv.Close()

	llmClient := llm.NewClient(srv.URL, "llama3.2:1b")
	if err := hooks.RunPreCompactCapture(ctx, st, llmClient, "s1", path, "/tmp/proj"); err != nil {
		t.Fatalf("RunPreCompactCapture: %v", err)
	}
	if called {
		t.Error("excerpt por debajo del mínimo no debería llegar a llamar al LLM")
	}
}
