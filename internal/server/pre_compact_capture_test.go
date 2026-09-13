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
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
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
	llmDone := make(chan struct{})
	slowOllama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		close(llmDone)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"response": `{"found":false}`})
	}))
	defer slowOllama.Close()

	srv, ts := newTestServer(t, "")
	srv.SetCaptureLLM(llm.NewClient(slowOllama.URL, "llama3.2:1b"), config.Default())

	transcriptPath := filepath.Join(t.TempDir(), "t.jsonl")
	_ = os.WriteFile(transcriptPath, []byte(`{"type":"user","message":{"role":"user","content":"algo"}}`+"\n"), 0o644)

	body, _ := json.Marshal(map[string]string{
		"session_id":      "s1",
		"transcript_path": transcriptPath,
		"cwd":             "/tmp",
	})

	resp, err := http.Post(ts.URL+"/hooks/pre-compact-capture", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202", resp.StatusCode)
	}
	// La prueba de que la respuesta no esperó al LLM no es una cota de tiempo:
	// es que el LLM todavía está dormido (2 s) cuando la respuesta ya llegó. Con
	// una cota de reloj, la máquina cargada (load 7-10) hacía fallar al handler
	// correcto — el POST entero puede tardar segundos por la carga, no por
	// esperar al LLM. Si el handler esperara, el POST volvería recién con el
	// canal cerrado y este select caería en el primer caso.
	select {
	case <-llmDone:
		t.Errorf("la respuesta volvió después de que el LLM terminó: el handler esperó al LLM en vez de responder ya")
	default:
		// el LLM sigue dormido: la respuesta no lo esperó
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
	isolatedDigestPendingDir(t)
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"response": `{"found":true,"title":"Hallazgo async de prueba","content":"Qué: se guardó async.\nPor qué: test end-to-end."}`,
		})
	}))
	defer ollama.Close()

	srv, ts := newTestServer(t, "")
	srv.SetCaptureLLM(llm.NewClient(ollama.URL, "llama3.2:1b"), config.Default())

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
		// La goroutine ahora también fuerza una actualización del digest
		// (ver handlePreCompactCapture) — el stub de Ollama, sin discriminar
		// por prompt, contesta el mismo JSON a ambas llamadas, así que un
		// segundo observation (type=session, topic_key=session/s1) aparece
		// además del hallazgo async. Se busca el hallazgo específico en vez
		// de asumir len(obs)==1.
		for _, o := range obs {
			if o.Title == "Hallazgo async de prueba" {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no se guardó la observación dentro del deadline — la goroutine async no terminó o falló en silencio")
}

// TestHandlePreCompactCapture_ForcesDigestUpdate_EvenIfNotDue reproduce el
// bug real: antes, PreCompact solo disparaba el juicio one-shot
// (RunPreCompactCapture) — si compactabas antes de que pasaran los 20min de
// digestUpdateInterval, el digest corriente de la sesión quedaba
// desactualizado justo cuando el transcript completo iba a desaparecer.
// Ahora handlePreCompactCapture también llama a MaybeUpdateDigest con
// force=true, así que debe actualizarse aunque un digest recién guardado
// diga que "todavía no toca".
func TestHandlePreCompactCapture_ForcesDigestUpdate_EvenIfNotDue(t *testing.T) {
	isolatedDigestPendingDir(t)
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body struct {
			Prompt string `json:"prompt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.Contains(body.Prompt, "content") && strings.Contains(body.Prompt, "found") {
			// prompt de RunPreCompactCapture (juicio one-shot)
			_ = json.NewEncoder(w).Encode(map[string]string{"response": `{"found":false}`})
			return
		}
		// prompt de UpdateDigest
		_ = json.NewEncoder(w).Encode(map[string]string{
			"response": `{"content":"- Justo antes de compactar, forzado"}`,
		})
	}))
	defer ollama.Close()

	srv, ts := newTestServer(t, "")
	srv.SetCaptureLLM(llm.NewClient(ollama.URL, "llama3.2:1b"), config.Default())

	// project.Detect(cwd) es lo que MaybeUpdateDigest usa de verdad para
	// resolver el nombre del proyecto (ver internal/hooks/digest.go) — no
	// necesariamente coincide con un nombre literal arbitrario, así que acá
	// se resuelve una sola vez y se reusa para CreateSession/SaveObservation
	// y para la búsqueda final, igual que TestHandlePromptSubmit_TriggersDigestUpdate.
	cwd := t.TempDir()
	projName := project.Detect(cwd).Name

	ctx := context.Background()
	if _, err := srv.st.CreateSession(ctx, "s1", projName, cwd); err != nil {
		t.Fatal(err)
	}
	// Digest recién guardado — sin el forzado, MaybeUpdateDigest lo saltaría
	// por "todavía no toca" (digestUpdateInterval = 20min).
	if _, err := srv.st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeSession, Title: "Resumen en curso de la sesión", Content: "- Ya arreglado esto",
		Project: projName, SessionID: "s1", TopicKey: "session/s1",
	}); err != nil {
		t.Fatal(err)
	}

	transcriptPath := filepath.Join(t.TempDir(), "t.jsonl")
	prompt := `{"type":"user","message":{"role":"user","content":"encontré otro bug justo antes de compactar"}}` + "\n"
	longText := strings.Repeat("contenido de conversación real que supera el mínimo de caracteres para que se llame al LLM. ", 4)
	_ = os.WriteFile(transcriptPath,
		[]byte(prompt+`{"type":"assistant","message":{"role":"assistant","content":"`+longText+`"}}`+"\n"),
		0o644)

	body, _ := json.Marshal(map[string]string{
		"session_id":      "s1",
		"transcript_path": transcriptPath,
		"cwd":             cwd,
	})
	resp, err := http.Post(ts.URL+"/hooks/pre-compact-capture", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		obs, err := srv.st.GetByTopicKey(ctx, projName, "session/s1")
		if err != nil {
			t.Fatal(err)
		}
		if obs != nil && strings.Contains(obs.Content, "forzado") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("el digest no se actualizó forzado dentro del deadline")
}
