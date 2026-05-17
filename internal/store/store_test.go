package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	f, err := os.CreateTemp("", "kronos-test-*.db")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	s, err := store.New(f.Name())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// --- Session ---

func TestCreateSession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sess, err := s.CreateSession(ctx, "s1", "mi-proyecto", "/home/jerry/mi-proyecto")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.ID != "s1" || sess.Project != "mi-proyecto" {
		t.Errorf("unexpected session: %+v", sess)
	}
	if sess.StartedAt.IsZero() {
		t.Error("StartedAt is zero")
	}
}

func TestGetActiveSession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// sin sesión activa
	sess, err := s.GetActiveSession(ctx, "proyecto-x")
	if err != nil {
		t.Fatalf("GetActiveSession (empty): %v", err)
	}
	if sess != nil {
		t.Error("expected nil session, got one")
	}

	// con sesión activa
	s.CreateSession(ctx, "s1", "proyecto-x", "/tmp")
	sess, err = s.GetActiveSession(ctx, "proyecto-x")
	if err != nil {
		t.Fatalf("GetActiveSession: %v", err)
	}
	if sess == nil || sess.ID != "s1" {
		t.Errorf("expected s1, got %v", sess)
	}

	// después de cerrarla ya no aparece
	s.EndSession(ctx, "s1", "resumen")
	sess, _ = s.GetActiveSession(ctx, "proyecto-x")
	if sess != nil {
		t.Error("expected nil after EndSession")
	}
}

func TestEndSessionNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.EndSession(context.Background(), "no-existe", "")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

// --- SaveObservation: insert, upsert, dedup ---

func TestSaveObservation_Insert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	obs, err := s.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypeDecision,
		Title:   "Elegimos Go",
		Content: "Go compila a binario único sin dependencias externas.",
		Project: "kronos-v2",
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
	if obs.ID == 0 {
		t.Error("ID should be non-zero")
	}
	if obs.RevisionCount != 1 || obs.DuplicateCount != 1 {
		t.Errorf("counts = revision:%d duplicate:%d, want 1/1", obs.RevisionCount, obs.DuplicateCount)
	}
}

func TestSaveObservation_UpsertByTopicKey(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, _ := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "v1", Content: "contenido versión uno de esta decisión",
		Project: "p", TopicKey: "arch/db",
	})

	second, err := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "v2", Content: "contenido actualizado para versión dos",
		Project: "p", TopicKey: "arch/db",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("upsert creó nuevo registro id=%d, esperaba actualizar id=%d", second.ID, first.ID)
	}
	if second.RevisionCount != 2 {
		t.Errorf("revision_count = %d, want 2", second.RevisionCount)
	}
	if second.Title != "v2" {
		t.Errorf("title = %q, want v2", second.Title)
	}
}

func TestSaveObservation_DedupByHash(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p := store.SaveParams{
		Type: store.TypeDiscovery, Title: "Mismo título exacto",
		Content: "Mismo contenido exacto para probar deduplicación por hash SHA256.",
		Project: "p",
	}

	first, _ := s.SaveObservation(ctx, p)
	second, err := s.SaveObservation(ctx, p) // mismo título+contenido
	if err != nil {
		t.Fatalf("dedup save: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("dedup creó nuevo id=%d, esperaba id=%d", second.ID, first.ID)
	}
	if second.DuplicateCount != 2 {
		t.Errorf("duplicate_count = %d, want 2", second.DuplicateCount)
	}
}

func TestSaveObservation_TopicKeyIsolatedByProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// mismo topic_key pero distinto proyecto → dos registros separados
	o1, _ := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "decisión en proyecto A",
		Content: "contenido para el primer proyecto con topic key compartido",
		Project: "proyecto-a", TopicKey: "config/db",
	})
	o2, _ := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "decisión en proyecto B",
		Content: "contenido para el segundo proyecto con topic key compartido",
		Project: "proyecto-b", TopicKey: "config/db",
	})

	if o1.ID == o2.ID {
		t.Error("topic_key de proyectos distintos no deben colisionar")
	}
}

func TestSaveObservation_ValidationErrors(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name string
		p    store.SaveParams
	}{
		{"empty title", store.SaveParams{Type: store.TypeDecision, Content: "contenido", Project: "p"}},
		{"empty content", store.SaveParams{Type: store.TypeDecision, Title: "título", Project: "p"}},
		{"empty project", store.SaveParams{Type: store.TypeDecision, Title: "título", Content: "contenido"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.SaveObservation(ctx, tc.p)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestDeleteObservation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	obs, _ := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "para borrar",
		Content: "este registro será eliminado con soft delete",
		Project: "p",
	})

	if err := s.DeleteObservation(ctx, obs.ID); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}

	// soft delete: no aparece en ListObservations
	list, _ := s.ListObservations(ctx, "p", 50)
	for _, o := range list {
		if o.ID == obs.ID {
			t.Error("deleted observation still appears in list")
		}
	}

	// pero GetObservation lo retorna con deleted_at seteado
	got, _ := s.GetObservation(ctx, obs.ID)
	if got == nil || got.DeletedAt == nil {
		t.Error("GetObservation should return soft-deleted record with deleted_at set")
	}
}

// --- Search FTS5 ---

func TestSearch_Basic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "Elegimos SQLite",
		Content: "SQLite es la base de datos embebida más usada del mundo.",
		Project: "p",
	})
	s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDiscovery, Title: "FTS5 soporta español",
		Content: "El tokenizador unicode61 maneja acentos correctamente sin config extra.",
		Project: "p",
	})

	results, err := s.Search(ctx, store.SearchParams{Query: "sqlite", Project: "p", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected results for 'sqlite'")
	}
	if results[0].Title != "Elegimos SQLite" {
		t.Errorf("top result title = %q", results[0].Title)
	}
}

func TestSearch_Spanish(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDiscovery, Title: "Acentos en español",
		Content: "El sistema maneja correctamente tildes y caracteres especiales en búsquedas.",
		Project: "p",
	})

	results, _ := s.Search(ctx, store.SearchParams{Query: "español", Project: "p", Limit: 5})
	if len(results) == 0 {
		t.Error("FTS5 no encontró resultado en español")
	}
}

func TestSearch_NoResults(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	results, err := s.Search(ctx, store.SearchParams{Query: "zzznomatch", Project: "p", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestSearch_EmptyQuery(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Search(context.Background(), store.SearchParams{Query: "", Project: "p"})
	if err == nil {
		t.Error("expected error for empty query")
	}
}

func TestSearch_GlobalScope(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// observación global visible desde cualquier proyecto
	s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypePattern, Title: "Patrón global reutilizable",
		Content: "Este patrón aplica a todos los proyectos de la organización.",
		Project: "global", Scope: store.ScopeGlobal,
	})

	results, _ := s.Search(ctx, store.SearchParams{Query: "patrón", Project: "otro-proyecto", Limit: 5})
	if len(results) == 0 {
		t.Error("observación global no apareció en búsqueda de otro proyecto")
	}
}

func TestSearch_DeletedNotReturned(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	obs, _ := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "registro que se borrará",
		Content: "este contenido no debe aparecer en búsquedas después de borrado.",
		Project: "p",
	})
	s.DeleteObservation(ctx, obs.ID)

	results, _ := s.Search(ctx, store.SearchParams{Query: "borrará", Project: "p", Limit: 5})
	for _, r := range results {
		if r.ID == obs.ID {
			t.Error("deleted observation appeared in search results")
		}
	}
}

// --- ExtractLearnings ---

func TestExtractLearnings(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int
	}{
		{
			name: "numbered items",
			input: `## Key Learnings:
1. ncruces/go-sqlite3 no necesita CGO para compilar en Windows
2. FTS5 con content= requiere triggers manuales de sincronización
3. bm25() retorna negativos, ORDER BY ASC da mejor match primero`,
			want: 3,
		},
		{
			name: "bullet items",
			input: `## Key Learnings:
- El tokenizador unicode61 soporta español sin configuración adicional
- Los índices en project+topic_key mejoran mucho el rendimiento de upsert`,
			want: 2,
		},
		{
			name: "no header",
			input: `Hice cosas.
1. item uno sin header de learnings
2. item dos sin header`,
			want: 0,
		},
		{
			name:  "empty text",
			input: "",
			want:  0,
		},
		{
			name: "items too short filtered",
			input: `## Key Learnings:
1. Corto
2. Este item tiene suficiente longitud y palabras para pasar el filtro mínimo`,
			want: 1,
		},
		{
			name: "too few words filtered",
			input: `## Key Learnings:
1. Solo tres palabras
2. Este item tiene suficiente contenido con más de cuatro palabras en total`,
			want: 1,
		},
		{
			name: "dedup same content",
			input: `## Key Learnings:
1. Este aprendizaje aparece dos veces en el mismo bloque de learnings
2. Este aprendizaje aparece dos veces en el mismo bloque de learnings`,
			want: 1,
		},
		{
			name: "stops at next header",
			input: `## Key Learnings:
1. Este aprendizaje debe incluirse en la extracción de learnings

## Otra sección
- Este item NO debe incluirse porque está bajo otro header`,
			want: 1,
		},
		{
			name: "spanish header",
			input: `### Aprendizajes Clave:
1. El header en español también funciona para la extracción automática
2. Kronos soporta múltiples idiomas en los headers de learnings`,
			want: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := store.ExtractLearnings(tc.input)
			if len(got) != tc.want {
				t.Errorf("ExtractLearnings = %d items, want %d\nitems: %v", len(got), tc.want, got)
			}
		})
	}
}
