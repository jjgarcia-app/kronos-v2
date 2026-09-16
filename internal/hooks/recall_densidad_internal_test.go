package hooks

import "testing"

// TestRankAndDedupeRecallItemsOpts_DensidadCortaRellenosFlojos cubre (a): el
// primer item (mayor densidad) siempre entra; los rellenos con densidad floja
// respecto del primero (matchedTerms diluido en un título+contenido largo) se
// descartan. "filler2" y "filler3" matchean 1 término cada uno pero lo hacen
// sobre 11 y 10 tokens significativos respectivamente (densidad ~0.09-0.10),
// muy por debajo de 0.60 * 1.0 (densidad del primero, que matchea sus 3
// tokens completos) — solo el primero sobrevive.
func TestRankAndDedupeRecallItemsOpts_DensidadCortaRellenosFlojos(t *testing.T) {
	items := []recallItem{
		{
			id: "baseline", typ: "discovery", matchedTerms: 3,
			title: "arquitectura consolidada recall",
		},
		{
			id: "filler2", typ: "discovery", matchedTerms: 1,
			title:   "resumen extendido variado",
			content: "contenido adicional disperso sobre trabajo variado ajeno reciente extenso",
		},
		{
			id: "filler3", typ: "discovery", matchedTerms: 1,
			title:   "notas dispersas variadas",
			content: "explicacion adicional sin relacion directa con la consulta actual reciente",
		},
	}
	got := rankAndDedupeRecallItemsOpts(items, 10, 3, rellenoDensidadFallback)
	if len(got) != 1 {
		t.Fatalf("esperaba que solo sobreviva el primero (densidad alta), got %d: %+v", len(got), got)
	}
	if got[0].id != "baseline" {
		t.Errorf("esperaba que el sobreviviente sea 'baseline', got %q", got[0].id)
	}
}

// TestRankAndDedupeRecallItemsOpts_DensidadSimilarMantieneTodos cubre (b): con
// tres candidatos de densidad parecida (0.67-1.0, todos >= 0.60 * la del
// primero) ninguno se descarta por el corte de rellenos.
func TestRankAndDedupeRecallItemsOpts_DensidadSimilarMantieneTodos(t *testing.T) {
	items := []recallItem{
		{id: "baseline", typ: "discovery", matchedTerms: 3, title: "arquitectura consolidada recall"},
		{id: "item2", typ: "discovery", matchedTerms: 2, title: "indice tsvector"},
		{id: "item3", typ: "discovery", matchedTerms: 2, title: "driver postgres pool"},
	}
	got := rankAndDedupeRecallItemsOpts(items, 10, 3, rellenoDensidadFallback)
	if len(got) != 3 {
		t.Fatalf("densidad pareja entre los 3 candidatos, esperaba que ninguno se corte, got %d: %+v", len(got), got)
	}
}

// TestRankAndDedupeRecallItemsOpts_DensidadNegativaDesactivaElCorte cubre (c):
// mismo fixture que corta a 1 con el factor default, pero con
// relleno_densidad negativo el corte queda desactivado — comportamiento
// anterior a este cambio, quedan los 3.
func TestRankAndDedupeRecallItemsOpts_DensidadNegativaDesactivaElCorte(t *testing.T) {
	items := []recallItem{
		{id: "baseline", typ: "discovery", matchedTerms: 3, title: "arquitectura consolidada recall"},
		{
			id: "filler2", typ: "discovery", matchedTerms: 1,
			title:   "resumen extendido variado",
			content: "contenido adicional disperso sobre trabajo variado ajeno reciente extenso",
		},
		{
			id: "filler3", typ: "discovery", matchedTerms: 1,
			title:   "notas dispersas variadas",
			content: "explicacion adicional sin relacion directa con la consulta actual reciente",
		},
	}
	got := rankAndDedupeRecallItemsOpts(items, 10, 3, -1)
	if len(got) != 3 {
		t.Fatalf("relleno_densidad negativo debería desactivar el corte, esperaba 3, got %d: %+v", len(got), got)
	}
}

// TestRankAndDedupeRecallItemsOpts_DensidadPrimeraCeroNoCorta cubre el caso
// límite: si el primer item (tras ordenar) no matcheó ningún término
// (matchedTerms = 0, su densidad es 0), el umbral resultante (factor * 0 = 0)
// no debe cortar nada — no hay candidato de referencia contra el cual ser
// "flojo". similarity decreciente fuerza el orden (todos empatan en
// matchedTerms = 0) para que "baseline" quede primero de forma determinística.
func TestRankAndDedupeRecallItemsOpts_DensidadPrimeraCeroNoCorta(t *testing.T) {
	items := []recallItem{
		{id: "baseline", typ: "discovery", matchedTerms: 0, similarity: 0.9, title: "tema generico sin match"},
		{id: "filler_a", typ: "discovery", matchedTerms: 0, similarity: 0.5, title: "cosa breve"},
		{id: "filler_b", typ: "discovery", matchedTerms: 0, similarity: 0.4, title: "otro tema distinto detalle"},
	}
	got := rankAndDedupeRecallItemsOpts(items, 10, 3, rellenoDensidadFallback)
	if len(got) != 3 {
		t.Fatalf("primer item con densidad 0 no debería cortar rellenos, esperaba 3, got %d: %+v", len(got), got)
	}
	if got[0].id != "baseline" {
		t.Fatalf("setup inválido: esperaba 'baseline' primero tras ordenar, got %q", got[0].id)
	}
}

// TestRankAndDedupeRecallItemsOpts_AnclaEsLaMaximaNoElPrimero cubre el caso
// que motivó anclar el corte a la densidad MÁXIMA entre candidatos en vez de
// a la del primero del orden: "primero" (por matchedTerms/similitud) no
// siempre es el más denso. Acá los tres candidatos empatan en matchedTerms,
// así que el orden lo decide similarity — "primero" queda con densidad floja
// (1/11 ≈ 0.09) mientras "denso" (mismo matchedTerms, título de una sola
// palabra) tiene densidad 1.0. Con la densidad del primero como ancla, el
// umbral (0.6 * 0.09) es tan bajo que hasta un relleno de verdad ("filler",
// densidad 1/3 ≈ 0.33) sobrevive; anclado a la máxima (0.6 * 1.0 = 0.6),
// "filler" queda por debajo y se corta — solo "primero" y "denso" quedan.
func TestRankAndDedupeRecallItemsOpts_AnclaEsLaMaximaNoElPrimero(t *testing.T) {
	items := []recallItem{
		{
			id: "primero", typ: "discovery", matchedTerms: 1, similarity: 0.9,
			title:   "resumen extendido variado",
			content: "contenido adicional disperso sobre trabajo variado ajeno reciente extenso",
		},
		{id: "denso", typ: "discovery", matchedTerms: 1, similarity: 0.1, title: "recall"},
		{id: "filler", typ: "discovery", matchedTerms: 1, similarity: 0.05, title: "detalle disperso", content: "variado"},
	}
	got := rankAndDedupeRecallItemsOpts(items, 10, 3, rellenoDensidadFallback)
	if len(got) != 2 {
		t.Fatalf("esperaba que 'filler' se corte contra la densidad máxima ('denso'), got %d: %+v", len(got), got)
	}
	ids := map[string]bool{got[0].id: true, got[1].id: true}
	if !ids["primero"] || !ids["denso"] {
		t.Errorf("esperaba que sobrevivan 'primero' y 'denso', got %+v", got)
	}
}
