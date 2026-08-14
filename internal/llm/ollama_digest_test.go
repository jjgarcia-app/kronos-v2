package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/llm"
)

// ollamaGenerateStubCapture es como ollamaGenerateStub pero además guarda el
// prompt real que le llegó — para verificar qué se le manda al modelo, no
// solo qué se parsea de la respuesta.
func ollamaGenerateStubCapture(t *testing.T, inner string, gotPrompt *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt string `json:"prompt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		*gotPrompt = body.Prompt
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"response": inner})
	}))
}

func TestUpdateDigest_FirstUpdate_ParsesContent(t *testing.T) {
	srv := ollamaGenerateStub(t, `{"content":"- Arreglado bug de import circular"}`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	d, err := c.UpdateDigest(context.Background(), "", "assistant: encontré la causa raíz del bug")
	if err != nil {
		t.Fatalf("UpdateDigest: %v", err)
	}
	if d == nil {
		t.Fatal("esperaba un DigestUpdate no nil")
	}
	if !strings.Contains(d.Content, "import circular") {
		t.Errorf("Content = %q", d.Content)
	}
}

func TestUpdateDigest_IncludesPreviousDigestInPrompt(t *testing.T) {
	var gotPrompt string
	srv := ollamaGenerateStubCapture(t, `{"content":"resumen extendido"}`, &gotPrompt)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	if _, err := c.UpdateDigest(context.Background(), "resumen anterior real", "nuevo excerpt"); err != nil {
		t.Fatalf("UpdateDigest: %v", err)
	}
	if !strings.Contains(gotPrompt, "resumen anterior real") {
		t.Error("el prompt debería incluir el digest previo para que el LLM lo extienda")
	}
}

func TestUpdateDigest_EmptyContent_ReturnsNil(t *testing.T) {
	srv := ollamaGenerateStub(t, `{"content":""}`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	d, err := c.UpdateDigest(context.Background(), "previo", "excerpt")
	if err != nil {
		t.Fatalf("UpdateDigest: %v", err)
	}
	if d != nil {
		t.Errorf("content vacío debería devolver nil, got: %+v", d)
	}
}

func TestUpdateDigest_MalformedInnerJSON_ReturnsError(t *testing.T) {
	srv := ollamaGenerateStub(t, `esto no es json`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	_, err := c.UpdateDigest(context.Background(), "previo", "excerpt")
	if err == nil {
		t.Fatal("esperaba error por JSON interno malformado")
	}
}

func TestUpdateDigest_OllamaUnreachable_ReturnsError(t *testing.T) {
	c := llm.NewClient("http://127.0.0.1:1", "llama3.2:1b")
	_, err := c.UpdateDigest(context.Background(), "previo", "excerpt")
	if err == nil {
		t.Fatal("esperaba error cuando Ollama es inalcanzable")
	}
}
