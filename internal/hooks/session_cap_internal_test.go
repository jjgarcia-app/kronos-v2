package hooks

import "testing"

// TestRankAndDedupeRecallItemsOpts_SessionItemsLast cubre el tope de resúmenes
// de sesión en el recall: un resumen no puede ganarle a conocimiento real
// aunque matchee más términos, y solo entra uno por prompt. Medido contra la
// base real: type=session era el tipo MÁS inyectado de todos (76 items, ~19%).
//
// Corre con el factor de densidad DEFAULT (0.60, no -1): el fixture expone de
// paso el corte de rellenos (ver TestRankAndDedupeRecallItemsOpts_Densidad*
// para su cobertura dedicada) — "k2" (1 término matcheado sobre 3 tokens
// significativos del título, densidad 0.33) queda por debajo de 0.60 * la
// densidad de "k1" (2/2 = 1.0), así que ahora el bloque entrega 2 items en vez
// de los 3 de antes de este cambio. Comportamiento esperado, no una
// regresión: el corte de rellenos es justamente esto.
func TestRankAndDedupeRecallItemsOpts_SessionItemsLast(t *testing.T) {
	items := []recallItem{
		{id: "s1", title: "Resumen de sesión a", typ: "session", matchedTerms: 5, similarity: 0.99},
		{id: "k1", title: "Fix del índice del tsvector", typ: "bugfix", matchedTerms: 2, similarity: 0.70},
		{id: "s2", title: "Resumen de sesión b", typ: "session", matchedTerms: 4, similarity: 0.98},
		{id: "k2", title: "Decisión sobre el driver de Postgres", typ: "decision", matchedTerms: 1, similarity: 0.60},
	}
	got := rankAndDedupeRecallItemsOpts(items, 10, 1, rellenoDensidadFallback)
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
	if len(got) != 2 {
		t.Errorf("esperaba 2 items (1 de conocimiento + 1 sesión; 'k2' se cae por densidad floja), got %d: %+v", len(got), got)
	}
}

// TestRankAndDedupeRecallItemsOpts_TopeConfigurable cubre el tope de sesión
// configurable (0 explícito sigue usando el default; un valor negativo
// también — nunca "sin tope" por accidente). Corre con el corte de densidad
// DESACTIVADO (factor -1): el fixture usa resúmenes de sesión con títulos
// largos a propósito para aislar el tope de sesión, y con el factor default
// esos mismos títulos quedan por debajo del umbral de densidad — ver
// TestRankAndDedupeRecallItemsOpts_Densidad* para la cobertura del corte de
// densidad en sí.
func TestRankAndDedupeRecallItemsOpts_TopeConfigurable(t *testing.T) {
	items := []recallItem{
		{id: "k1", title: "Fix del índice del tsvector", typ: "bugfix", matchedTerms: 2},
		{id: "s1", title: "Resumen del trabajo de índices de Postgres", typ: "session", matchedTerms: 1},
		{id: "s2", title: "Resumen del deploy de Caddy en producción", typ: "session", matchedTerms: 1},
		{id: "s3", title: "Resumen de la reunión con ATISA", typ: "session", matchedTerms: 1},
	}
	got := rankAndDedupeRecallItemsOpts(items, 10, 2, -1)
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
