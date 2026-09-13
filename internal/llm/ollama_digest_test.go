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
	d, err := c.UpdateDigest(context.Background(), "", "assistant: encontré la causa raíz del bug", 0)
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
	if _, err := c.UpdateDigest(context.Background(), "resumen anterior real", "nuevo excerpt", 0); err != nil {
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
	d, err := c.UpdateDigest(context.Background(), "previo", "excerpt", 0)
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
	_, err := c.UpdateDigest(context.Background(), "previo", "excerpt", 0)
	if err == nil {
		t.Fatal("esperaba error por JSON interno malformado")
	}
}

func TestUpdateDigest_OllamaUnreachable_ReturnsError(t *testing.T) {
	c := llm.NewClient("http://127.0.0.1:1", "llama3.2:1b")
	_, err := c.UpdateDigest(context.Background(), "previo", "excerpt", 0)
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
	d, err := c.UpdateDigest(context.Background(), "", "excerpt", 0)
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
	d, err := c.UpdateDigest(context.Background(), "", "excerpt", 0)
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
// Es justo el caso (b) del Tema 1: sin la clave "facts", FactsKnown debe
// quedar en false — no hay forma de distinguir "no hay hechos" de "el modelo
// no llegó a esa sección", y MaybeUpdateDigest necesita ese false para
// reintentar solo los hechos en vez de perderlos en silencio.
func TestUpdateDigest_NoFactsField_ContentParsesFine(t *testing.T) {
	srv := ollamaGenerateStub(t, `{"content":"resumen sin facts"}`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	d, err := c.UpdateDigest(context.Background(), "", "excerpt", 0)
	if err != nil {
		t.Fatalf("UpdateDigest: %v", err)
	}
	if d.Content != "resumen sin facts" || d.Facts != nil {
		t.Errorf("d = %+v", d)
	}
	if d.FactsKnown {
		t.Error("sin la clave facts, FactsKnown debería ser false (respuesta no explícita)")
	}
}

// TestUpdateDigest_EmptyFactsList_FactsKnownTrue confirma el caso (c) del
// Tema 1: cuando el modelo respeta el formato pedido y devuelve "facts": []
// (nada que proponer), eso SÍ es una respuesta explícita — FactsKnown debe
// ser true para que MaybeUpdateDigest no deje la sesión pendiente de
// reintento al pedo.
func TestUpdateDigest_EmptyFactsList_FactsKnownTrue(t *testing.T) {
	srv := ollamaGenerateStub(t, `{"content":"resumen","facts":[]}`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	d, err := c.UpdateDigest(context.Background(), "", "excerpt", 0)
	if err != nil {
		t.Fatalf("UpdateDigest: %v", err)
	}
	if !d.FactsKnown {
		t.Error("facts:[] es una respuesta explícita — FactsKnown debería ser true")
	}
	if len(d.Facts) != 0 {
		t.Errorf("Facts = %+v, want vacío", d.Facts)
	}
}

// TestUpdateDigest_PopulatedFacts_FactsKnownTrue confirma el caso (a): con
// hechos reales en la respuesta, FactsKnown también es true.
func TestUpdateDigest_PopulatedFacts_FactsKnownTrue(t *testing.T) {
	srv := ollamaGenerateStub(t, `{"content":"resumen","facts":[{"type":"bugfix","title":"t","content":"c"}]}`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	d, err := c.UpdateDigest(context.Background(), "", "excerpt", 0)
	if err != nil {
		t.Fatalf("UpdateDigest: %v", err)
	}
	if !d.FactsKnown {
		t.Error("con hechos poblados, FactsKnown debería ser true")
	}
}

// TestUpdateDigest_MalformedFacts_FactsKnownFalse confirma que una sección
// "facts" con una forma inesperada (no es un array) tampoco cuenta como
// respuesta explícita — sigue siendo ambigua, no un "no hay nada" real.
func TestUpdateDigest_MalformedFacts_FactsKnownFalse(t *testing.T) {
	srv := ollamaGenerateStub(t, `{"content":"resumen valido","facts":"esto no es un array"}`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	d, err := c.UpdateDigest(context.Background(), "", "excerpt", 0)
	if err != nil {
		t.Fatalf("UpdateDigest: %v", err)
	}
	if d.FactsKnown {
		t.Error("facts malformado no es una respuesta explícita — FactsKnown debería ser false")
	}
}

// --- ExtractDigestFacts: el pedido acotado del reintento de "solo hechos" ---

func TestExtractDigestFacts_ParsesArray(t *testing.T) {
	srv := ollamaGenerateStub(t, `[{"type":"bugfix","title":"t","content":"c"}]`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	facts, explicit, err := c.ExtractDigestFacts(context.Background(), "excerpt", 3, 0)
	if err != nil {
		t.Fatalf("ExtractDigestFacts: %v", err)
	}
	if !explicit {
		t.Error("una lista de hechos parseable es una respuesta explícita")
	}
	if len(facts) != 1 || facts[0].Title != "t" {
		t.Errorf("facts = %+v", facts)
	}
}

// TestExtractDigestFacts_ExplicitNoFacts confirma la válvula "FACTS: ninguno"
// (Tema 1, caso (c) del reintento acotado): sin ella, cualquier sesión sin
// hechos reintentaría hasta agotar los 3 intentos al pedo.
func TestExtractDigestFacts_ExplicitNoFacts(t *testing.T) {
	srv := ollamaGenerateStub(t, `FACTS: ninguno`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	facts, explicit, err := c.ExtractDigestFacts(context.Background(), "excerpt", 3, 0)
	if err != nil {
		t.Fatalf("ExtractDigestFacts: %v", err)
	}
	if !explicit {
		t.Error("FACTS: ninguno debería ser una respuesta explícita")
	}
	if len(facts) != 0 {
		t.Errorf("facts = %+v, want vacío", facts)
	}
}

// TestExtractDigestFacts_ExplicitNoFacts_CaseInsensitive confirma tolerancia
// de formato: mayúsculas/minúsculas y un punto final no deberían importar.
func TestExtractDigestFacts_ExplicitNoFacts_CaseInsensitive(t *testing.T) {
	srv := ollamaGenerateStub(t, `facts: Ninguno.`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	_, explicit, err := c.ExtractDigestFacts(context.Background(), "excerpt", 3, 0)
	if err != nil {
		t.Fatalf("ExtractDigestFacts: %v", err)
	}
	if !explicit {
		t.Error("la variante en minúsculas con punto final también debería reconocerse")
	}
}

// TestExtractDigestFacts_UnparseableResponse_NotExplicit confirma que una
// respuesta que no es ni un array JSON ni el literal de "no hay nada" deja
// explicit=false — el caller debe seguir reintentando, no asumir que no hay
// hechos.
func TestExtractDigestFacts_UnparseableResponse_NotExplicit(t *testing.T) {
	srv := ollamaGenerateStub(t, `esto no es ni JSON ni el literal esperado`)
	defer srv.Close()

	c := llm.NewClient(srv.URL, "llama3.2:1b")
	facts, explicit, err := c.ExtractDigestFacts(context.Background(), "excerpt", 3, 0)
	if err != nil {
		t.Fatalf("una respuesta no parseable no es un error de la llamada: %v", err)
	}
	if explicit {
		t.Error("una respuesta no interpretable no debería contar como explícita")
	}
	if facts != nil {
		t.Errorf("facts = %+v, want nil", facts)
	}
}

// TestExtractDigestFacts_BackendError_ReturnsError confirma el contrato
// fail-open: una falla real de la llamada (acá, backend inalcanzable) sí
// devuelve error, para que el caller la distinga de "respuesta ambigua" y
// pueda loguearla como tal.
func TestExtractDigestFacts_BackendError_ReturnsError(t *testing.T) {
	c := llm.NewClient("http://127.0.0.1:1", "llama3.2:1b")
	_, explicit, err := c.ExtractDigestFacts(context.Background(), "excerpt", 3, 0)
	if err == nil {
		t.Fatal("esperaba error cuando el backend es inalcanzable")
	}
	if explicit {
		t.Error("una falla real no debería reportarse como explícita")
	}
}
