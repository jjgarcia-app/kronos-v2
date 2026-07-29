package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func newTestServer(t *testing.T, token string) (*Server, *httptest.Server) {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	srv := New(st, 0, token)
	// httptest.NewServer bindea en 127.0.0.1 — cumple el chequeo de loopback
	// de authMiddleware sin necesitar simular headers de proxy.
	ts := httptest.NewServer(srv.authMiddleware(srv.mux))
	t.Cleanup(ts.Close)
	return srv, ts
}

func TestHandleHealth_AlwaysOK(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestAuthMiddleware_NoToken_AllowsLoopback(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, err := http.Get(ts.URL + "/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (loopback sin token debería pasar)", resp.StatusCode)
	}
}

func TestAuthMiddleware_WithToken_RejectsMissingHeader(t *testing.T) {
	_, ts := newTestServer(t, "secret-token")
	resp, err := http.Get(ts.URL + "/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 sin Authorization header", resp.StatusCode)
	}
}

func TestAuthMiddleware_WithToken_AcceptsCorrectBearer(t *testing.T) {
	_, ts := newTestServer(t, "secret-token")
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/stats", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 con Bearer token correcto", resp.StatusCode)
	}
}

func TestAuthMiddleware_WithToken_RejectsWrongToken(t *testing.T) {
	_, ts := newTestServer(t, "secret-token")
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/stats", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 con token incorrecto", resp.StatusCode)
	}
}

func TestObservations_CreateAndGet(t *testing.T) {
	_, ts := newTestServer(t, "")

	body, _ := json.Marshal(map[string]any{
		"type":    "discovery",
		"title":   "Test via REST",
		"content": "contenido de prueba",
		"project": "p",
	})
	resp, err := http.Post(ts.URL+"/observations", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /observations status = %d, want 201", resp.StatusCode)
	}

	var created struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Data.ID == 0 {
		t.Fatal("expected non-zero ID in response")
	}

	getResp, err := http.Get(ts.URL + "/observations?project=p")
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /observations status = %d, want 200", getResp.StatusCode)
	}
}

func TestSearch_RequiresQuery(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, err := http.Get(ts.URL + "/search")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 sin 'q'", resp.StatusCode)
	}
}

func TestSearch_FindsSavedObservation(t *testing.T) {
	_, ts := newTestServer(t, "")

	body, _ := json.Marshal(map[string]any{
		"type": "discovery", "title": "Buscar esto por REST", "content": "contenido buscable", "project": "p",
	})
	http.Post(ts.URL+"/observations", "application/json", bytes.NewReader(body))

	resp, err := http.Get(ts.URL + "/search?q=buscar+esto&project=p")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		Data []any `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	if len(got.Data) == 0 {
		t.Error("esperaba al menos 1 resultado de búsqueda")
	}
}

func TestProjectCurrent_ReturnsDetection(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, err := http.Get(ts.URL + "/project/current?cwd=" + t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		Data map[string]any `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	if _, ok := got.Data["project"]; !ok {
		t.Errorf("respuesta no tiene campo 'project': %v", got.Data)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	_, ts := newTestServer(t, "")
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/search", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 para DELETE /search", resp.StatusCode)
	}
}

// --- /hooks/prompt-submit (punto 3 de mejoras: prompt-submit vía daemon) ---

func TestHandlePromptSubmit_SavesPromptAndReturns200(t *testing.T) {
	srv, ts := newTestServer(t, "")

	cwd := t.TempDir()
	st, ok := srv.st.(*store.Store)
	if !ok {
		t.Fatal("srv.st no es *store.Store")
	}
	// user_prompts.session_id tiene FK a sessions(id) — hay que crear la
	// sesión primero, si no SavePrompt falla en silencio (error descartado
	// a propósito en RunPromptSubmit, es fire-and-forget).
	if _, err := st.CreateSession(context.Background(), "sess-daemon-ps", "p", cwd); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{
		"session_id": "sess-daemon-ps",
		"cwd":        cwd,
		"prompt":     "test prompt via daemon",
	})
	resp, err := http.Post(ts.URL+"/hooks/prompt-submit", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	if n := st.CountSessionPrompts(context.Background(), "sess-daemon-ps"); n != 1 {
		t.Errorf("CountSessionPrompts = %d, want 1 — el endpoint debería haber guardado el prompt", n)
	}
}

func TestHandlePromptSubmit_MethodNotAllowed(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, err := http.Get(ts.URL + "/hooks/prompt-submit")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 para GET /hooks/prompt-submit", resp.StatusCode)
	}
}

func TestHandlePromptSubmit_InvalidBody(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, err := http.Post(ts.URL+"/hooks/prompt-submit", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 con body inválido", resp.StatusCode)
	}
}
