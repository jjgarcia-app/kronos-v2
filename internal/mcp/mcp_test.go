package mcp_test

import (
	"context"
	"os"
	"testing"

	kronosmcp "github.com/jjgarcia-app/kronos-v2/internal/mcp"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// newTestServer crea un Server con DB en memoria para tests.
func newTestServer(t *testing.T) *kronosmcp.Server {
	t.Helper()
	f, err := os.CreateTemp("", "kronos-mcp-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	st, err := store.New(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	return kronosmcp.New(st, 10, 20)
}

// call invoca un tool del servidor y retorna el texto de la respuesta.
func call(t *testing.T, srv *kronosmcp.Server, tool string, args map[string]any) string {
	t.Helper()
	result, err := srv.Call(context.Background(), tool, args)
	if err != nil {
		t.Fatalf("Call(%s): %v", tool, err)
	}
	if result == nil {
		t.Fatalf("Call(%s): nil result", tool)
	}
	if result.IsError {
		t.Fatalf("Call(%s) returned error: %v", tool, extractText(result))
	}
	return extractText(result)
}

func callExpectError(t *testing.T, srv *kronosmcp.Server, tool string, args map[string]any) string {
	t.Helper()
	result, err := srv.Call(context.Background(), tool, args)
	if err != nil {
		return err.Error()
	}
	if result == nil || !result.IsError {
		t.Fatalf("Call(%s): expected error but got success", tool)
	}
	return extractText(result)
}

func extractText(r *mcpgo.CallToolResult) string {
	if r == nil {
		return ""
	}
	for _, c := range r.Content {
		if tc, ok := mcpgo.AsTextContent(c); ok {
			return tc.Text
		}
	}
	return ""
}

// --- mem_session_start / mem_session_end ---

func TestMemSessionStart(t *testing.T) {
	srv := newTestServer(t)

	text := call(t, srv, "mem_session_start", map[string]any{
		"project":    "kronos-v2",
		"directory":  "/home/jerry/kronos-v2",
		"session_id": "sess-test-001",
	})

	if text == "" {
		t.Error("expected non-empty response")
	}
	// debe confirmar el ID y el proyecto
	for _, want := range []string{"sess-test-001", "kronos-v2"} {
		if !contains(text, want) {
			t.Errorf("response missing %q:\n%s", want, text)
		}
	}
}

func TestMemSessionEnd(t *testing.T) {
	srv := newTestServer(t)

	call(t, srv, "mem_session_start", map[string]any{
		"project": "p", "session_id": "s1",
	})

	text := call(t, srv, "mem_session_end", map[string]any{
		"session_id": "s1",
	})
	if !contains(text, "s1") {
		t.Errorf("response missing session id: %s", text)
	}
}

func TestMemSessionEnd_NotFound(t *testing.T) {
	srv := newTestServer(t)
	callExpectError(t, srv, "mem_session_end", map[string]any{
		"session_id": "no-existe",
	})
}

// --- mem_save ---

func TestMemSave_Basic(t *testing.T) {
	srv := newTestServer(t)

	text := call(t, srv, "mem_save", map[string]any{
		"title":     "Elegimos Go para Kronos v2",
		"content":   "Qué: Adoptamos Go puro sin CGO como lenguaje del proyecto.\nPor qué: Binario estático multiplataforma con ncruces/go-sqlite3 WASM.\nArchivos: go.mod\nCómo aplicar: Nunca agregar dependencias con CGO.",
		"type":      "decision",
		"project":   "kronos-v2",
		"topic_key": "stack/language",
	})

	if !contains(text, "guardado") {
		t.Errorf("expected 'guardado' in response: %s", text)
	}
}

func TestMemSave_Upsert(t *testing.T) {
	srv := newTestServer(t)

	call(t, srv, "mem_save", map[string]any{
		"title":     "v1",
		"content":   "Qué: Elegimos PostgreSQL como base de datos primaria.\nPor qué: Necesitamos consultas complejas y replicación.\nArchivos: internal/store/store.go\nCómo aplicar: Usar $N placeholders, no ?.",
		"type":      "decision",
		"project":   "p",
		"topic_key": "arch/db",
	})

	text := call(t, srv, "mem_save", map[string]any{
		"title":     "v2",
		"content":   "Qué: Actualizamos a DualStore con SQLite como buffer de PostgreSQL.\nPor qué: Tolerancia a fallos cuando PG está caído.\nArchivos: internal/store/dual_store.go\nCómo aplicar: DualStore es siempre el backend en producción.",
		"type":      "decision",
		"project":   "p",
		"topic_key": "arch/db",
	})

	if !contains(text, "actualizado") {
		t.Errorf("expected 'actualizado' in upsert response: %s", text)
	}
}

func TestMemSave_MissingTitle(t *testing.T) {
	srv := newTestServer(t)
	callExpectError(t, srv, "mem_save", map[string]any{
		"content": "contenido sin título", "type": "decision", "project": "p",
	})
}

func TestMemSave_MissingProject(t *testing.T) {
	srv := newTestServer(t)
	callExpectError(t, srv, "mem_save", map[string]any{
		"title": "título", "content": "contenido sin proyecto", "type": "decision",
	})
}

// --- mem_search ---

func TestMemSearch_FindsSaved(t *testing.T) {
	srv := newTestServer(t)

	call(t, srv, "mem_save", map[string]any{
		"title":   "SQLite FTS5 soporta búsqueda full-text",
		"content": "Qué: FTS5 con unicode61 maneja español sin configuración extra.\nPor qué: No necesita extensiones adicionales en ncruces/go-sqlite3.\nArchivos: internal/store/fts.go\nCómo aplicar: Usar FTS5 directamente sin flags adicionales.",
		"type":    "discovery",
		"project": "p",
	})

	text := call(t, srv, "mem_search", map[string]any{
		"query": "sqlite", "project": "p",
	})

	if !contains(text, "SQLite") {
		t.Errorf("search 'sqlite' should find saved observation:\n%s", text)
	}
}

func TestMemSearch_NoResults(t *testing.T) {
	srv := newTestServer(t)

	text := call(t, srv, "mem_search", map[string]any{
		"query": "zzznonexistent", "project": "p",
	})

	if !contains(text, "No se encontraron") {
		t.Errorf("expected 'No se encontraron' for empty results: %s", text)
	}
}

func TestMemSearch_EmptyQuery(t *testing.T) {
	srv := newTestServer(t)
	callExpectError(t, srv, "mem_search", map[string]any{
		"query": "", "project": "p",
	})
}

// --- mem_context ---

func TestMemContext_Empty(t *testing.T) {
	srv := newTestServer(t)

	text := call(t, srv, "mem_context", map[string]any{
		"project": "proyecto-vacio",
	})

	if !contains(text, "No hay") {
		t.Errorf("expected 'No hay' for empty context: %s", text)
	}
}

func TestMemContext_WithObservations(t *testing.T) {
	srv := newTestServer(t)

	call(t, srv, "mem_save", map[string]any{
		"title":     "Decisión importante del proyecto",
		"content":   "Qué: Adoptamos arquitectura hexagonal para kronos-v2.\nPor qué: Separar dominio de infraestructura facilita tests y cambio de backend.\nArchivos: internal/store/storer.go\nCómo aplicar: Store siempre detrás de interfaz Storer.",
		"type":      "architecture",
		"project":   "p",
		"topic_key": "arch/hexagonal",
	})

	text := call(t, srv, "mem_context", map[string]any{
		"project": "p",
	})

	if !contains(text, "Decisión importante") {
		t.Errorf("expected saved observation in context: %s", text)
	}
}

// --- mem_get_observation ---

func TestMemGetObservation(t *testing.T) {
	srv := newTestServer(t)

	// guardar y obtener el ID del texto de respuesta
	saveText := call(t, srv, "mem_save", map[string]any{
		"title":   "Observación para recuperar completa",
		"content": "Qué: Guardamos observación para probar mem_get_observation.\nPor qué: Verificar que el contenido completo se retorna sin truncar.\nArchivos: N/A\nCómo aplicar: mem_get_observation retorna el contenido íntegro.",
		"type":    "discovery",
		"project": "p",
	})

	// extraer ID del texto "ID: N"
	id := extractID(saveText)
	if id == "" {
		t.Fatalf("could not extract ID from: %s", saveText)
	}

	text := call(t, srv, "mem_get_observation", map[string]any{"id": id})

	if !contains(text, "Observación para recuperar") {
		t.Errorf("get_observation missing title: %s", text)
	}
	if !contains(text, "mem_get_observation retorna") {
		t.Errorf("get_observation missing content: %s", text)
	}
}

func TestMemGetObservation_InvalidID(t *testing.T) {
	srv := newTestServer(t)
	callExpectError(t, srv, "mem_get_observation", map[string]any{"id": "no-es-numero"})
}

func TestMemGetObservation_NotFound(t *testing.T) {
	srv := newTestServer(t)
	callExpectError(t, srv, "mem_get_observation", map[string]any{"id": "99999"})
}

// --- mem_update ---

func TestMemUpdate(t *testing.T) {
	srv := newTestServer(t)

	saveText := call(t, srv, "mem_save", map[string]any{
		"title":     "Título original antes de actualizar",
		"content":   "Qué: Guardamos observación para probar mem_update.\nPor qué: Verificar que actualización con formato correcto funciona.\nArchivos: N/A\nCómo aplicar: Siempre usar formato al actualizar content.",
		"type":      "decision",
		"project":   "p",
		"topic_key": "test/update",
	})

	id := extractID(saveText)
	if id == "" {
		t.Fatalf("no ID in: %s", saveText)
	}

	text := call(t, srv, "mem_update", map[string]any{
		"id":      id,
		"title":   "Título actualizado correctamente",
		"content": "Qué: Actualizamos la observación con mem_update.\nPor qué: El contenido previo tenía información incompleta.\nArchivos: N/A\nCómo aplicar: mem_update requiere el mismo formato que mem_save.",
	})

	if !contains(text, "actualizada") {
		t.Errorf("expected 'actualizada': %s", text)
	}

	// verificar que el cambio persiste
	got := call(t, srv, "mem_get_observation", map[string]any{"id": id})
	if !contains(got, "Título actualizado") {
		t.Errorf("update not persisted: %s", got)
	}
}

// --- mem_session_summary ---

func TestMemSessionSummary(t *testing.T) {
	srv := newTestServer(t)

	call(t, srv, "mem_session_start", map[string]any{
		"project": "p", "session_id": "s-sum",
	})

	text := call(t, srv, "mem_session_summary", map[string]any{
		"session_id": "s-sum",
		"project":    "p",
		"summary":    "## Objetivo\nImplementar store con FTS5\n\n## Completado\n- Store SQLite con FTS5\n- 22 tests pasando\n\n## Descubrimientos clave\nFTS5 unicode61 maneja español sin config extra\n\n## Próximos pasos\nImplementar DualStore\n\n## Archivos relevantes\ninternal/store/store.go",
	})

	if !contains(text, "guardado") && !contains(text, "Resumen") {
		t.Errorf("unexpected response: %s", text)
	}
}

// --- mem_save_prompt ---

func TestMemSavePrompt(t *testing.T) {
	srv := newTestServer(t)
	text := call(t, srv, "mem_save_prompt", map[string]any{
		"content": "¿Cómo implementamos el store?",
		"project": "p",
	})
	if !contains(text, "guardado") || !contains(text, "Prompt") {
		t.Errorf("unexpected response: %s", text)
	}
}

// newTestServerWithStore crea un Server y retorna también el store subyacente para inspección.
func newTestServerWithStore(t *testing.T) (*kronosmcp.Server, *store.Store) {
	t.Helper()
	f, err := os.CreateTemp("", "kronos-mcp-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	st, err := store.New(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	return kronosmcp.New(st, 10, 20), st
}

// --- mem_search search_count instrumentation ---

func TestMemSearch_IncrementsSearchCount(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	ctx := context.Background()

	st.CreateSession(ctx, "sess-sc-mcp", "proj", "/tmp")

	call(t, srv, "mem_search", map[string]any{
		"query":      "anything",
		"project":    "proj",
		"session_id": "sess-sc-mcp",
	})

	sess, err := st.GetSession(ctx, "sess-sc-mcp")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess == nil {
		t.Fatal("GetSession returned nil")
	}
	if sess.SearchCount != 1 {
		t.Errorf("SearchCount = %d, want 1", sess.SearchCount)
	}
}

func TestMemSearch_IncrementsOnEmptyResults(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	ctx := context.Background()

	st.CreateSession(ctx, "sess-sc-empty", "proj", "/tmp")

	// Query that returns 0 results — should still increment.
	call(t, srv, "mem_search", map[string]any{
		"query":      "zzznomatch",
		"project":    "proj",
		"session_id": "sess-sc-empty",
	})

	sess, err := st.GetSession(ctx, "sess-sc-empty")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess == nil {
		t.Fatal("GetSession returned nil")
	}
	if sess.SearchCount != 1 {
		t.Errorf("SearchCount = %d, want 1 (even for empty results)", sess.SearchCount)
	}
}

// --- helpers ---

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

func extractID(s string) string {
	// busca "ID: N" en el texto de respuesta
	const prefix = "ID: "
	idx := 0
	for i := 0; i <= len(s)-len(prefix); i++ {
		if s[i:i+len(prefix)] == prefix {
			idx = i + len(prefix)
			end := idx
			for end < len(s) && s[end] >= '0' && s[end] <= '9' {
				end++
			}
			if end > idx {
				return s[idx:end]
			}
		}
	}
	return ""
}
