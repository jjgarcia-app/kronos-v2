package hooks

import "testing"

// TestRankAndDedupeRecallItemsOpts_SessionItemsLast cubre el tope de resúmenes
// de sesión en el recall: un resumen no puede ganarle a conocimiento real
// aunque matchee más términos, y solo entra uno por prompt. Medido contra la
// base real: type=session era el tipo MÁS inyectado de todos (76 items, ~19%).
func TestRankAndDedupeRecallItemsOpts_SessionItemsLast(t *testing.T) {
	items := []recallItem{
		{id: "s1", title: "Resumen de sesión a", typ: "session", matchedTerms: 5, similarity: 0.99},
		{id: "k1", title: "Fix del índice del tsvector", typ: "bugfix", matchedTerms: 2, similarity: 0.70},
		{id: "s2", title: "Resumen de sesión b", typ: "session", matchedTerms: 4, similarity: 0.98},
		{id: "k2", title: "Decisión sobre el driver de Postgres", typ: "decision", matchedTerms: 1, similarity: 0.60},
	}
	got := rankAndDedupeRecallItemsOpts(items, 10, 1)
	if len(got) == 0 {
		t.Fatal("no devolvió nada")
	}
	if got[0].typ == "session" {
		t.Errorf("el primero debería ser conocimiento real, no un resumen de sesión: %+v", got[0])
	}
	sesiones := 0
	for _, it := range got {
		if it.typ == "session" {
			sesiones++
		}
	}
	if sesiones > 1 {
		t.Errorf("entraron %d resúmenes de sesión, el tope es 1", sesiones)
	}
	if len(got) != 3 {
		t.Errorf("esperaba 3 items (2 de conocimiento + 1 sesión), got %d: %+v", len(got), got)
	}
}

// TestRankAndDedupeRecallItemsOpts_SinTopeDeSesion permite apagarlo (0 explícito
// sigue usando el default; un valor negativo también — nunca "sin tope" por
// accidente).
func TestRankAndDedupeRecallItemsOpts_TopeConfigurable(t *testing.T) {
	items := []recallItem{
		{id: "k1", title: "Fix del índice del tsvector", typ: "bugfix", matchedTerms: 2},
		{id: "s1", title: "Resumen del trabajo de índices de Postgres", typ: "session", matchedTerms: 1},
		{id: "s2", title: "Resumen del deploy de Caddy en producción", typ: "session", matchedTerms: 1},
		{id: "s3", title: "Resumen de la reunión con ATISA", typ: "session", matchedTerms: 1},
	}
	got := rankAndDedupeRecallItemsOpts(items, 10, 2)
	sesiones := 0
	for _, it := range got {
		if it.typ == "session" {
			sesiones++
		}
	}
	if sesiones != 2 {
		t.Errorf("con maxSession=2 esperaba 2 resúmenes, got %d", sesiones)
	}
}
