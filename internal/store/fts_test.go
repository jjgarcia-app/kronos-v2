package store

import (
	"context"
	"testing"
)

func TestSanitizeFTSQuery(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"simple", "modal confirm", `"modal" "confirm"`},
		{"empty", "", ""},
		{"explicit quotes preserved as-is", `"exact phrase"`, `"exact phrase"`},
		{"wildcard preserved as-is", "foo*", "foo*"},
		{"grouping preserved as-is", "(a OR b) AND c", "(a OR b) AND c"},
		{
			// Bug real: un ticket con guion mezclado con OR rompía FTS5
			// ("no such column: 441") porque antes esto se pasaba crudo sin
			// sanear apenas detectaba " OR " en el texto.
			"hyphenated term with OR operator",
			"AT-441 OR AT-442 agrupar subcontratos",
			`"AT-441" OR "AT-442" "agrupar" "subcontratos"`,
		},
		{"lowercase or is a literal term, not an operator", "css or html", `"css" "or" "html"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeFTSQuery(c.query); got != c.want {
				t.Errorf("sanitizeFTSQuery(%q) = %q, want %q", c.query, got, c.want)
			}
		})
	}
}

// TestSearch_HyphenatedTicketIDWithOR reproduce el bug real end-to-end:
// buscar dos IDs de ticket con guion combinados con OR tiraba
// "sqlite3: SQL logic error: no such column: 441" en vez de buscar.
func TestSearch_HyphenatedTicketIDWithOR(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveObservation(ctx, SaveParams{
		Type: TypeDiscovery, Title: "AT-441 encontrado", Content: "detalle del ticket", Project: "p",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Search(ctx, SearchParams{Query: "AT-441 OR AT-442 agrupar subcontratos", Project: "p"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
}

func TestWidenFTSQuery(t *testing.T) {
	cases := []struct {
		name   string
		query  string
		want   string
		wantOK bool
	}{
		{
			// El caso real medido el 2026-09-15: frase natural que antes
			// devolvía 0 filas con el hecho ya guardado en la base.
			"frase natural con varios terminos",
			"laptop enviar archivos scp tailscale",
			`"laptop" OR "enviar" OR "archivos" OR "scp" OR "tailscale"`,
			true,
		},
		{"un solo termino no se ensancha", "laptop", "", false},
		{"comillas explicitas no se tocan", `"frase exacta"`, "", false},
		{"parentesis explicitos no se tocan", "(a OR b) AND c", "", false},
		{"wildcard no se toca", "lapto*", "", false},
		{"OR ya escrito a mano no se reescribe", "laptop OR omarchy", "", false},
		{"guion sobrevive entre comillas", "AT-441 agrupar", `"AT-441" OR "agrupar"`, true},
		{"vacio no se ensancha", "   ", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := widenFTSQuery(c.query)
			if ok != c.wantOK || got != c.want {
				t.Errorf("widenFTSQuery(%q) = (%q, %v), want (%q, %v)", c.query, got, ok, c.want, c.wantOK)
			}
		})
	}
}

// TestSearch_FraseLargaConTerminosSueltosEncuentraResultado es el bug real
// del 2026-09-15 end-to-end: el hecho está guardado, la frase que escribe
// un usuario (o las palabras de un prompt) no matchea todos los términos,
// y antes eso devolvía 0 filas — la memoria existía y no llegaba.
func TestSearch_FraseLargaConTerminosSueltosEncuentraResultado(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveObservation(ctx, SaveParams{
		Type:    TypePreference,
		Title:   "Para entregar archivos a la laptop usar scp/rsync por Tailscale",
		Content: "Desde el VPS se llega por Tailscale SSH a 100.115.41.105 y se copia con scp o rsync.",
		Project: "infra", Scope: ScopeGlobal,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Search(ctx, SearchParams{Query: "laptop enviar archivos scp tailscale", Project: "infra"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("la búsqueda ancha devolvió 0 resultados con el hecho guardado")
	}

	// Y la consulta estricta sigue siendo la que manda cuando da resultados:
	// buscar los términos que sí están todos debe seguir funcionando.
	estricta, err := s.Search(ctx, SearchParams{Query: "laptop Tailscale", Project: "infra"})
	if err != nil {
		t.Fatalf("Search estricta: %v", err)
	}
	if len(estricta) == 0 {
		t.Fatal("la búsqueda estricta con términos presentes devolvió 0 resultados")
	}
}
