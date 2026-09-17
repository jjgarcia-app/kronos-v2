package hooks

import "testing"

// TestRankAndDedupeRecallItemsOpts_VectorSinTerminosGanaConSimilitudAlta es la
// regresión del caso real medido en fixture_set (prompt "¿puedo usar
// librerías externas en este repo?", 2026-09-17): la fase vectorial (una vez
// arreglado el bug de indexación de arranque, ver fix del reindex en
// cmd/kronos/serve.go) encontraba la observación correcta con similarity=0.69
// pero matchedTerms=0 (ninguna palabra literal compartida con el prompt), y
// perdía SIEMPRE el desempate contra 3 candidatos de FTS con matchedTerms=2
// que solo compartían palabras sueltas sin relación real de sentido
// ("Fix: Sub() sumaba +1 de más" comparte "repo"/"este" con el prompt).
//
// El fix prioriza similarity por delante de matchedTerms únicamente cuando
// uno de los dos candidatos tiene matchedTerms=0 y similarity por encima de
// vectorZeroTermPriorityThreshold — nunca cuando ambos ya tienen señal léxica
// real (ver TestRankAndDedupeRecallItemsOpts_NoCambiaOrdenSiAmbosTienenTerminos
// para la regresión inversa, el caso que casi rompió set.json).
func TestRankAndDedupeRecallItemsOpts_VectorSinTerminosGanaConSimilitudAlta(t *testing.T) {
	items := []recallItem{
		{id: "fts_ruido_1", typ: "bugfix", matchedTerms: 2, similarity: 0, title: "ruido con match literal"},
		{id: "fts_ruido_2", typ: "bugfix", matchedTerms: 2, similarity: 0, title: "otro ruido con match literal"},
		{id: "vector_correcto", typ: "decision", matchedTerms: 0, similarity: 0.69, title: "respuesta correcta sin match literal"},
	}
	got := rankAndDedupeRecallItemsOpts(items, 3, 3, -1)
	found := false
	for _, it := range got {
		if it.id == "vector_correcto" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("el candidato del vector con similarity alta y matchedTerms=0 no sobrevivió: %+v", got)
	}
}

// TestRankAndDedupeRecallItemsOpts_NoCambiaOrdenSiAmbosTienenTerminos es la
// regresión inversa: si AMBOS candidatos ya tienen matchedTerms>0 (ninguno es
// puramente semántico), el desempate sigue siendo por matchedTerms como
// antes, aunque uno tenga mayor similarity. Caso real (set.json, prompt
// "¿esto es local o fue error en el PR?"): sin este resguardo, un candidato
// con similarity=0.69 y matchedTerms=2 le ganaba el desempate a uno con
// matchedTerms=3 pero similarity menor — bajando la cobertura de set.json de
// 43% a 41%.
func TestRankAndDedupeRecallItemsOpts_NoCambiaOrdenSiAmbosTienenTerminos(t *testing.T) {
	items := []recallItem{
		{id: "menos_terminos_mas_similar", typ: "preference", matchedTerms: 2, similarity: 0.69, title: "menos terminos"},
		{id: "mas_terminos_menos_similar", typ: "preference", matchedTerms: 3, similarity: 0.68, title: "mas terminos"},
	}
	got := rankAndDedupeRecallItemsOpts(items, 3, 3, -1)
	if len(got) < 1 || got[0].id != "mas_terminos_menos_similar" {
		t.Fatalf("esperaba que matchedTerms (no similarity) decida cuando ambos tienen señal léxica, got %+v", got)
	}
}
