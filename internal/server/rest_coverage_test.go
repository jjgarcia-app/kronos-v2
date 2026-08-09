package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// Este archivo cubre la superficie REST del daemon que server_test.go no
// ejercitaba — /sessions, /observations/{id}, /context, /conflicts,
// /export, /import estaban en 0% pese a ser la API que expone el daemon
// compartido (ver docs/architecture.md).

func TestHandleSessions_PostAndGet(t *testing.T) {
	_, ts := newTestServer(t, "")

	body, _ := json.Marshal(map[string]string{"id": "s1", "project": "p", "directory": "/tmp"})
	resp, err := http.Post(ts.URL+"/sessions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /sessions status = %d, want 201", resp.StatusCode)
	}

	resp2, err := http.Get(ts.URL + "/sessions?project=p")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET /sessions status = %d, want 200", resp2.StatusCode)
	}
}

func TestHandleSessions_PostMissingFields_400(t *testing.T) {
	_, ts := newTestServer(t, "")
	body, _ := json.Marshal(map[string]string{"id": "s1"}) // sin project
	resp, err := http.Post(ts.URL+"/sessions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleSessionsPath_GetByID(t *testing.T) {
	srv, ts := newTestServer(t, "")
	ctx := context.Background()
	if _, err := srv.st.CreateSession(ctx, "s-get", "p", "/tmp"); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(ts.URL + "/sessions/s-get")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHandleSessionsPath_GetByID_NotFound(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, err := http.Get(ts.URL + "/sessions/no-existe")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandleSessionsPath_Recent(t *testing.T) {
	srv, ts := newTestServer(t, "")
	ctx := context.Background()
	if _, err := srv.st.CreateSession(ctx, "s-recent", "p", "/tmp"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(ts.URL + "/sessions/recent?project=p&limit=5")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHandleSessionEnd(t *testing.T) {
	srv, ts := newTestServer(t, "")
	ctx := context.Background()
	if _, err := srv.st.CreateSession(ctx, "s-end", "p", "/tmp"); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{"summary": "listo"})
	resp, err := http.Post(ts.URL+"/sessions/s-end/end", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	sess, err := srv.st.GetSession(ctx, "s-end")
	if err != nil || sess == nil || sess.EndedAt == nil {
		t.Errorf("la sesión debería tener EndedAt seteado: sess=%v err=%v", sess, err)
	}
}

func TestHandleObservationsPath_GetPatchDelete(t *testing.T) {
	srv, ts := newTestServer(t, "")
	ctx := context.Background()
	obs, err := srv.st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDiscovery, Title: "t", Content: "c", Project: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	idPath := ts.URL + "/observations/" + strconv.FormatInt(obs.ID, 10)

	resp, err := http.Get(idPath)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", resp.StatusCode)
	}

	patchBody, _ := json.Marshal(map[string]string{"title": "actualizado"})
	req, _ := http.NewRequest(http.MethodPatch, idPath, bytes.NewReader(patchBody))
	req.Header.Set("Content-Type", "application/json")
	patchResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200", patchResp.StatusCode)
	}
	updated, err := srv.st.GetObservation(ctx, obs.ID)
	if err != nil || updated.Title != "actualizado" {
		t.Errorf("Title = %q, want %q (err=%v)", updated.Title, "actualizado", err)
	}

	delReq, _ := http.NewRequest(http.MethodDelete, idPath, nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200", delResp.StatusCode)
	}
}

func TestHandleObservationsPath_InvalidID(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, err := http.Get(ts.URL + "/observations/no-es-un-id")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleContext(t *testing.T) {
	srv, ts := newTestServer(t, "")
	ctx := context.Background()
	if _, err := srv.st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDiscovery, Title: "t", Content: "c", Project: "p-ctx",
	}); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(ts.URL + "/context?project=p-ctx&limit=10")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHandleConflicts_ListStatsJudge(t *testing.T) {
	_, ts := newTestServer(t, "")

	resp, err := http.Get(ts.URL + "/conflicts?project=p")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /conflicts status = %d, want 200", resp.StatusCode)
	}

	statsResp, err := http.Get(ts.URL + "/conflicts/stats?project=p")
	if err != nil {
		t.Fatal(err)
	}
	statsResp.Body.Close()
	if statsResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /conflicts/stats status = %d, want 200", statsResp.StatusCode)
	}

	notFoundResp, err := http.Get(ts.URL + "/conflicts/no-existe")
	if err != nil {
		t.Fatal(err)
	}
	notFoundResp.Body.Close()
	if notFoundResp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /conflicts/no-existe status = %d, want 404", notFoundResp.StatusCode)
	}
}

func TestHandleExportImport(t *testing.T) {
	srv, ts := newTestServer(t, "")
	ctx := context.Background()
	if _, err := srv.st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDiscovery, Title: "exportme", Content: "c", Project: "p-exp",
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(ts.URL + "/export?project=p-exp")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /export status = %d, want 200", resp.StatusCode)
	}

	items := []store.SaveParams{
		{Type: store.TypeDiscovery, Title: "importado", Content: "c2", Project: "p-imp"},
	}
	body, _ := json.Marshal(items)
	importResp, err := http.Post(ts.URL+"/import", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer importResp.Body.Close()
	if importResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /import status = %d, want 200", importResp.StatusCode)
	}

	n, err := srv.st.CountObservations(ctx, "p-imp")
	if err != nil || n != 1 {
		t.Errorf("CountObservations(p-imp) = (%d, %v), want (1, nil)", n, err)
	}
}

func newTestStoreForServer(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestServer_StartStopAddr(t *testing.T) {
	st := newTestStoreForServer(t)
	// New(st, 0, "") NO significa "que el SO elija puerto" — port<=0 cae al
	// default 4317, que puede estar realmente ocupado por un daemon de
	// kronos corriendo en esta misma máquina. Usar un puerto alto y
	// específico para el test evita ese choque real.
	srv := New(st, 47318, "")
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if srv.Addr() == "" {
		t.Error("Addr() no debería ser vacío tras Start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

func TestServer_SetVectorStoreAndHandle(t *testing.T) {
	st := newTestStoreForServer(t)
	srv := New(st, 47319, "")
	srv.SetVectorStore(&embeddings.VectorStore{})
	if srv.vs == nil {
		t.Error("SetVectorStore no dejó vs seteado")
	}

	called := false
	srv.Handle("/custom", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/custom", nil)
	req.RemoteAddr = "127.0.0.1:12345" // authMiddleware exige loopback sin token
	rw := httptest.NewRecorder()
	srv.authMiddleware(srv.mux).ServeHTTP(rw, req)
	if !called {
		t.Error("el handler montado con Handle() no se llamó")
	}
	if rw.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rw.Code)
	}
}
