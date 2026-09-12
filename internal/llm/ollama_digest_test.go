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

// TestUpdateDigest_ParsesFacts_SameCall confirma que "facts" se extrae de la
// MISMA respuesta que "content" — ningún round-trip extra al backend (ver
// Tarea B: la promoción de hechos no agrega una llamada nueva).
func TestUpdateDigest_ParsesFacts_SameCall(t *testing.T) {
	srv := ollamaGenerateStub(t, `{"content":"resumen","facts":[{"type":"bugfix","title":"t","content":"c"}]}`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	d, err := c.UpdateDigest(context.Background(), "", "excerpt")
	if err != nil {
		t.Fatalf("UpdateDigest: %v", err)
	}
	if len(d.Facts) != 1 || d.Facts[0].Type != "bugfix" || d.Facts[0].Title != "t" || d.Facts[0].Content != "c" {
		t.Errorf("Facts = %+v", d.Facts)
	}
}

// TestUpdateDigest_MalformedFacts_ContentSurvives confirma la tolerancia de
// parseo: "facts" con una forma inesperada (acá, un string en vez de un
// array de objetos) se descarta en silencio, pero Content sigue parseando
// bien — el parseo de facts no debe poder tirar abajo lo que ya funcionaba.
func TestUpdateDigest_MalformedFacts_ContentSurvives(t *testing.T) {
	srv := ollamaGenerateStub(t, `{"content":"resumen valido","facts":"esto no es un array"}`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	d, err := c.UpdateDigest(context.Background(), "", "excerpt")
	if err != nil {
		t.Fatalf("UpdateDigest no debería fallar por facts malformado: %v", err)
	}
	if d.Content != "resumen valido" {
		t.Errorf("Content = %q, want %q", d.Content, "resumen valido")
	}
	if d.Facts != nil {
		t.Errorf("Facts = %+v, want nil (descartado por parseo inválido)", d.Facts)
	}
}

// TestUpdateDigest_NoFactsField_ContentParsesFine confirma compatibilidad
// hacia atrás: una respuesta sin "facts" (un modelo que no lo soporta, o un
// stub de test viejo) sigue parseando Content normalmente, con Facts nil.
func TestUpdateDigest_NoFactsField_ContentParsesFine(t *testing.T) {
	srv := ollamaGenerateStub(t, `{"content":"resumen sin facts"}`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	d, err := c.UpdateDigest(context.Background(), "", "excerpt")
	if err != nil {
		t.Fatalf("UpdateDigest: %v", err)
	}
	if d.Content != "resumen sin facts" || d.Facts != nil {
		t.Errorf("d = %+v", d)
	}
}
