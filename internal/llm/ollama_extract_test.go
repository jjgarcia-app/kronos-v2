package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/llm"
)

// ollamaGenerateStub sirve /api/generate devolviendo el wrapper
// {"response": "<innerJSON>"} que Ollama realmente usa — mismo formato que
// ExtractFinding/JudgeRelation parsean.
func ollamaGenerateStub(t *testing.T, inner string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"response": inner})
	}))
}

func TestExtractFinding_Found_ParsesTitleAndContent(t *testing.T) {
	srv := ollamaGenerateStub(t, `{"found":true,"title":"Fix gate nunca desbloqueaba","content":"Qué: ...\nPor qué: ..."}`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	f, err := c.ExtractFinding(context.Background(), "assistant: encontré la causa raíz del bug")
	if err != nil {
		t.Fatalf("ExtractFinding: %v", err)
	}
	if f == nil || !f.Found {
		t.Fatalf("esperaba Found=true, got: %+v", f)
	}
	if f.Title != "Fix gate nunca desbloqueaba" {
		t.Errorf("Title = %q", f.Title)
	}
	if f.Content == "" {
		t.Errorf("Content vacío")
	}
}

func TestExtractFinding_NotFound_ReturnsFoundFalse(t *testing.T) {
	srv := ollamaGenerateStub(t, `{"found":false}`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	f, err := c.ExtractFinding(context.Background(), "user: qué hora es")
	if err != nil {
		t.Fatalf("ExtractFinding: %v", err)
	}
	if f == nil || f.Found {
		t.Fatalf("esperaba Found=false, got: %+v", f)
	}
}

// TestExtractFinding_FoundTrueButEmptyContent_TreatedAsNotFound cubre el
// caso donde el modelo (1B params, no siempre confiable) dice found:true
// pero no llena title/content — no vale la pena guardar un observation vacío.
func TestExtractFinding_FoundTrueButEmptyContent_TreatedAsNotFound(t *testing.T) {
	srv := ollamaGenerateStub(t, `{"found":true,"title":"","content":""}`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	f, err := c.ExtractFinding(context.Background(), "algo")
	if err != nil {
		t.Fatalf("ExtractFinding: %v", err)
	}
	if f == nil || f.Found {
		t.Fatalf("found:true sin contenido debería tratarse como not-found, got: %+v", f)
	}
}

func TestExtractFinding_MalformedInnerJSON_ReturnsError(t *testing.T) {
	srv := ollamaGenerateStub(t, `esto no es json`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	_, err := c.ExtractFinding(context.Background(), "algo")
	if err == nil {
		t.Fatal("esperaba error por JSON interno malformado")
	}
}

func TestExtractFinding_OllamaUnreachable_ReturnsError(t *testing.T) {
	c := llm.NewClient("http://127.0.0.1:1", "llama3.2:1b") // puerto reservado, rechaza conexión al instante
	_, err := c.ExtractFinding(context.Background(), "algo")
	if err == nil {
		t.Fatal("esperaba error cuando Ollama es inalcanzable")
	}
}

func TestExtractFinding_NonOKStatus_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	_, err := c.ExtractFinding(context.Background(), "algo")
	if err == nil {
		t.Fatal("esperaba error por status 500")
	}
}
