package hooks

import (
	"strings"
	"testing"
)

// El puntero no puede crecer sin que alguien lo note: si supera el tope, las
// líneas de más se caen (no se cortan por la mitad) y este test lo fija.
func TestHintsPreamble_TopePorLineasCompletas(t *testing.T) {
	texto := HintsPreamble(0) // default
	if texto == "" {
		t.Fatal("el puntero por defecto no puede estar vacío")
	}
	if len(texto) > defaultHintsChars {
		t.Errorf("el puntero mide %d chars y el tope es %d", len(texto), defaultHintsChars)
	}
	for _, l := range hints {
		if !strings.HasSuffix(l, "") || strings.Contains(l, "\n") {
			t.Errorf("una línea del puntero tiene salto interno: %q", l)
		}
	}
	if n := strings.Count(texto, "\n"); n != len(hints) {
		t.Errorf("esperaba %d líneas, got %d", len(hints), n)
	}
	if !strings.Contains(texto, "kronos env") {
		t.Error("el puntero tiene que nombrar `kronos env`")
	}
	if !strings.Contains(texto, "_infra") {
		t.Error("el puntero tiene que nombrar la ruta de la documentación")
	}
}

func TestHintsPreamble_TopeChicoTruncaSinCortarLineas(t *testing.T) {
	// Tope para una sola línea: la primera entra, las otras no.
	primera := hints[0]
	texto := HintsPreamble(len(primera))
	if texto != primera+"\n" {
		t.Errorf("con tope de una línea esperaba solo la primera, got %q", texto)
	}

	// Tope ínfimo: no entra nada y no se devuelve media línea cortada.
	if got := HintsPreamble(10); got != "" {
		t.Errorf("con tope 10 esperaba vacío, got %q", got)
	}
	if got := HintsPreamble(len(primera) - 1); got != "" {
		t.Errorf("con tope menor a la primera línea esperaba vacío, got %q", got)
	}
}

func TestHintsStats(t *testing.T) {
	lineas, chars := HintsStats(0)
	if lineas != len(hints) || chars == 0 {
		t.Errorf("HintsStats(0) = (%d, %d), esperaba (%d, >0)", lineas, chars, len(hints))
	}
	if l, c := HintsStats(5); l != 0 || c != 0 {
		t.Errorf("HintsStats(5) = (%d, %d), esperaba (0, 0)", l, c)
	}
}
