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
