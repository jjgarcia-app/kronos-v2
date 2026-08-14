package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/llm"
)

func TestHandlePreCompactCapture_WrongMethod_405(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, err := http.Get(ts.URL + "/hooks/pre-compact-capture")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

// TestHandlePreCompactCapture_RespondsImmediately_WithoutWaitingForLLM es la
// garantía de "no afecta el flujo": la respuesta HTTP no debe demorarse ni
// un poco por más lento que sea Ollama — el handler responde 202 antes de
// hacer ningún trabajo real.
func TestHandlePreCompactCapture_RespondsImmediately_WithoutWaitingForLLM(t *testing.T) {
	slowOllama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"response": `{"found":false}`})
	}))
	defer slowOllama.Close()

	srv, ts := newTestServer(t, "")
	srv.SetCaptureLLM(llm.NewClient(slowOllama.URL, "llama3.2:1b"), config.Config{})

	transcriptPath := filepath.Join(t.TempDir(), "t.jsonl")
	_ = os.WriteFile(transcriptPath, []byte(`{"type":"user","message":{"role":"user","content":"algo"}}`+"\n"), 0o644)

	body, _ := json.Marshal(map[string]string{
		"session_id":      "s1",
		"transcript_path": transcriptPath,
		"cwd":             "/tmp",
	})

	start := time.Now()
	resp, err := http.Post(ts.URL+"/hooks/pre-compact-capture", "application/json", bytes.NewReader(body))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202", resp.StatusCode)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("la respuesta tardó %v — debería responder antes de esperar al LLM (que tarda 2s)", elapsed)
	}
}

func TestHandlePreCompactCapture_MissingFields_StillAccepted_NoOp(t *testing.T) {
	_, ts := newTestServer(t, "")
	body, _ := json.Marshal(map[string]string{})
	resp, err := http.Post(ts.URL+"/hooks/pre-compact-capture", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202 incluso sin session_id/transcript_path", resp.StatusCode)
	}
}

// TestHandlePreCompactCapture_EndToEnd_SavesObservationAsync confirma que,
// pese a responder 202 de inmediato, el trabajo real sigue corriendo del
// lado del daemon y termina guardando la observación.
func TestHandlePreCompactCapture_EndToEnd_SavesObservationAsync(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"response": `{"found":true,"title":"Hallazgo async de prueba","content":"Qué: se guardó async.\nPor qué: test end-to-end."}`,
		})
	}))
	defer ollama.Close()

	srv, ts := newTestServer(t, "")
	srv.SetCaptureLLM(llm.NewClient(ollama.URL, "llama3.2:1b"), config.Config{})

	ctx := context.Background()
	if _, err := srv.st.CreateSession(ctx, "s1", "proj", "/tmp"); err != nil {
		t.Fatal(err)
	}

	transcriptPath := filepath.Join(t.TempDir(), "t.jsonl")
	longText := strings.Repeat("contenido de conversación real que supera el mínimo de caracteres para que se llame al LLM. ", 4)
	_ = os.WriteFile(transcriptPath,
		[]byte(`{"type":"assistant","message":{"role":"assistant","content":"`+longText+`"}}`+"\n"),
		0o644)

	body, _ := json.Marshal(map[string]string{
		"session_id":      "s1",
		"transcript_path": transcriptPath,
		"cwd":             "/tmp",
	})
	resp, err := http.Post(ts.URL+"/hooks/pre-compact-capture", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		obs, err := srv.st.ListSessionObservations(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		if len(obs) == 1 {
			if obs[0].Title != "Hallazgo async de prueba" {
				t.Errorf("Title = %q", obs[0].Title)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no se guardó la observación dentro del deadline — la goroutine async no terminó o falló en silencio")
}
