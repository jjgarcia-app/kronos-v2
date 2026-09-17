package hooks

import "testing"

// TestPassesPrecisionGuard_MatchedAboveNeeded_SiempreDejaPasar cubre el
// camino sin cambios: matched >= needed pasa sin importar el largo de los
// términos.
func TestPassesPrecisionGuard_MatchedAboveNeeded_SiempreDejaPasar(t *testing.T) {
	if !passesPrecisionGuard([]string{"a", "b"}, 2) {
		t.Fatal("matched == needed debería pasar")
	}
	if !passesPrecisionGuard([]string{"a", "b", "c"}, 2) {
		t.Fatal("matched > needed debería pasar")
	}
}

// TestPassesPrecisionGuard_UnTerminoCorto_NoPasa cubre el caso que sigue
// descartándose: un único término por debajo de needed y por debajo de
// guardaLargoMinimo no es lo bastante específico como para no ser ruido.
func TestPassesPrecisionGuard_UnTerminoCorto_NoPasa(t *testing.T) {
	if passesPrecisionGuard([]string{"ahora"}, 2) {
		t.Fatal("un único término corto (5 runas) no debería pasar la guarda")
	}
}

// TestPassesPrecisionGuard_UnTerminoLargo_Pasa cubre la excepción medida
// contra el fixture kronos-bench: un único término >= guardaLargoMinimo
// runas pasa aunque matched < needed — "librerías" (10 runas) y
// "convenciones" (12 runas) son ejemplos reales que motivaron esto.
func TestPassesPrecisionGuard_UnTerminoLargo_Pasa(t *testing.T) {
	if !passesPrecisionGuard([]string{"librerías"}, 2) {
		t.Fatal("un único término largo (>= guardaLargoMinimo runas) debería pasar la guarda")
	}
	if !passesPrecisionGuard([]string{"convenciones"}, 2) {
		t.Fatal("un único término largo (>= guardaLargoMinimo runas) debería pasar la guarda")
	}
}

// TestPassesPrecisionGuard_LargoExactoEnElLimite cubre el borde exacto:
// guardaLargoMinimo runas pasa, una runa menos no.
func TestPassesPrecisionGuard_LargoExactoEnElLimite(t *testing.T) {
	if !passesPrecisionGuard([]string{"acerca"}, 2) { // 6 runas
		t.Fatal("6 runas (== guardaLargoMinimo) debería pasar")
	}
	if passesPrecisionGuard([]string{"cerca"}, 2) { // 5 runas
		t.Fatal("5 runas (< guardaLargoMinimo) no debería pasar")
	}
}

// TestPassesPrecisionGuard_DosTerminosCortosPorDebajoDeNeeded_NoPasa cubre
// que la excepción es SOLO para matched == 1 — dos términos cortos, aunque
// juntos aporten más letras, siguen sin alcanzar needed y no hay término
// único que evaluar por largo.
func TestPassesPrecisionGuard_DosTerminosCortosPorDebajoDeNeeded_NoPasa(t *testing.T) {
	if passesPrecisionGuard([]string{"aquí", "acá"}, 3) {
		t.Fatal("matched == 2 < needed == 3, sin término único, no debería pasar")
	}
}

// TestMatchingTerms_DevuelveSoloLosQueAparecen cubre que matchingTerms es
// case-insensitive y filtra a los términos realmente presentes en
// título+contenido, sin alterar su orden de entrada.
func TestMatchingTerms_DevuelveSoloLosQueAparecen(t *testing.T) {
	got := matchingTerms([]string{"alfresco", "aspect", "remove"}, "Alfresco Upgrade", "notas de alfresco")
	if len(got) != 1 || got[0] != "alfresco" {
		t.Fatalf("esperaba [\"alfresco\"], got %+v", got)
	}
}
