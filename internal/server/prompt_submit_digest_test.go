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

// TestHandlePromptSubmit_TriggersDigestUpdate cubre el wiring de punta a
// punta: un POST a /hooks/prompt-submit con session_id + transcript_path,
// con un LLM local disponible y sin digest previo (siempre "due" la primera
// vez), dispara MaybeUpdateDigest en background y termina guardando el
// resumen — sin que la request en sí tenga que esperarlo (responde 200 de
// inmediato, el digest se ve un instante después).
func TestHandlePromptSubmit_TriggersDigestUpdate(t *testing.T) {
	srv, ts := newTestServer(t, "")

	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"response": `{"content":"- Arreglado bug real durante esta sesión"}`,
		})
	}))
	defer llmSrv.Close()
	srv.SetCaptureLLM(llm.NewClient(llmSrv.URL, "llama3.2:1b"), config.Config{})

	cwd := t.TempDir()
	st, ok := srv.st.(*store.Store)
	if !ok {
		t.Fatal("srv.st no es *store.Store")
	}
	projName := project.Detect(cwd).Name
	if _, err := st.CreateSession(context.Background(), "sess-digest", projName, cwd); err != nil {
		t.Fatal(err)
	}

	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	line := `{"type":"assistant","message":{"role":"assistant","content":"encontré y arreglé la causa raíz de un bug real, con suficiente texto para pasar el umbral mínimo del excerpt"}}` + "\n"
	if err := os.WriteFile(transcriptPath, []byte(strings.Repeat(line, 5)), 0o644); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{
		"session_id":      "sess-digest",
		"cwd":             cwd,
		"prompt":          "test prompt",
		"transcript_path": transcriptPath,
	})
	resp, err := http.Post(ts.URL+"/hooks/prompt-submit", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	deadline := time.Now().Add(2 * time.Second)
	var found *store.Observation
	for time.Now().Before(deadline) {
		found, _ = st.GetByTopicKey(context.Background(), projName, "session/sess-digest")
		if found != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if found == nil {
		t.Fatal("esperaba que el digest se guardara en background tras el POST")
	}
	if !strings.Contains(found.Content, "bug real") {
		t.Errorf("Content = %q", found.Content)
	}
}
