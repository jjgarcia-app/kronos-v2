package llm

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDigestPending_Due_NoEntry_False(t *testing.T) {
	p := NewDigestPending(filepath.Join(t.TempDir(), "digest-pending.json"))
	if p.Due("s1") {
		t.Error("sin ningún MarkFailed previo, s1 no debería estar due")
	}
	if p.Count() != 0 {
		t.Errorf("Count = %d, want 0", p.Count())
	}
}

func TestDigestPending_MarkFailed_ThenDue_True(t *testing.T) {
	p := NewDigestPending(filepath.Join(t.TempDir(), "digest-pending.json"))
	p.MarkFailed("s1")
	if !p.Due("s1") {
		t.Error("tras MarkFailed, s1 debería estar due")
	}
	if p.Due("otra-sesion") {
		t.Error("MarkFailed de s1 no debería afectar a otra sesión")
	}
	if p.Count() != 1 {
		t.Errorf("Count = %d, want 1", p.Count())
	}
}

func TestDigestPending_MarkFailed_IncrementsAttempts(t *testing.T) {
	p := NewDigestPending(filepath.Join(t.TempDir(), "digest-pending.json"))
	p.MarkFailed("s1")
	p.MarkFailed("s1")
	entries := p.load().Entries
	if len(entries) != 1 {
		t.Fatalf("esperaba una sola entrada (upsert), got %d", len(entries))
	}
	if entries[0].Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", entries[0].Attempts)
	}
}

// TestDigestPending_MaxAttempts_NoLongerDue confirma el tope de
// digestPendingMaxAttempts (3): los primeros 2 fallos siguen dejando la
// sesión due (vale la pena reintentar), pero al llegar al 3er fallo
// consecutivo se deja de insistir — no tiene sentido seguir intentando con
// un LLM que ya falló 3 veces seguidas para esta sesión.
func TestDigestPending_MaxAttempts_NoLongerDue(t *testing.T) {
	p := NewDigestPending(filepath.Join(t.TempDir(), "digest-pending.json"))
	for i := 1; i < digestPendingMaxAttempts; i++ {
		p.MarkFailed("s1")
		if !p.Due("s1") {
			t.Fatalf("tras %d fallo(s) (< tope de %d) debería seguir due", i, digestPendingMaxAttempts)
		}
	}
	p.MarkFailed("s1") // llega al tope
	if p.Due("s1") {
		t.Errorf("tras alcanzar el tope de %d fallos no debería seguir due", digestPendingMaxAttempts)
	}
	if p.Count() != 0 {
		t.Errorf("Count = %d, want 0 (la entrada en el tope ya no cuenta como pendiente)", p.Count())
	}
}

// TestDigestPending_MaxAge_Expires confirma que un pendiente de más de 12h
// se descarta sin reintentar — simulado escribiendo FirstAt en el pasado
// directamente (MarkFailed siempre usa time.Now(), no hay forma de simular
// el paso del tiempo real en un test rápido).
func TestDigestPending_MaxAge_Expires(t *testing.T) {
	p := NewDigestPending(filepath.Join(t.TempDir(), "digest-pending.json"))
	old := time.Now().Add(-digestPendingMaxAge - time.Minute)
	p.save(digestPendingState{Entries: []DigestPendingEntry{
		{SessionID: "s1", Attempts: 1, FirstAt: old},
	}})
	if p.Due("s1") {
		t.Error("un pendiente de más de 12h no debería seguir due")
	}
	if p.Count() != 0 {
		t.Errorf("Count = %d, want 0 (el pendiente vencido no cuenta)", p.Count())
	}
}

func TestDigestPending_Clear_RemovesEntry(t *testing.T) {
	p := NewDigestPending(filepath.Join(t.TempDir(), "digest-pending.json"))
	p.MarkFailed("s1")
	p.MarkFailed("s2")
	p.Clear("s1")
	if p.Due("s1") {
		t.Error("s1 debería haberse limpiado")
	}
	if !p.Due("s2") {
		t.Error("Clear de s1 no debería afectar a s2")
	}
}

func TestDigestPending_CorruptFile_TreatedAsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "digest-pending.json")
	p := NewDigestPending(path)
	writeCorruptFile(t, path)
	if p.Due("s1") {
		t.Error("un archivo corrupto debería tratarse como sin pendientes")
	}
	// Sigue pudiendo escribir después de leer algo corrupto.
	p.MarkFailed("s1")
	if !p.Due("s1") {
		t.Error("después de un archivo corrupto, MarkFailed debería seguir funcionando")
	}
}

func TestDigestPending_NilReceiver_SafeNoop(t *testing.T) {
	var p *DigestPending
	if p.Due("s1") {
		t.Error("un DigestPending nil nunca debería reportar pendientes")
	}
	if p.Count() != 0 {
		t.Errorf("Count sobre nil = %d, want 0", p.Count())
	}
	// No deben paniquear.
	p.MarkFailed("s1")
	p.Clear("s1")
}

// --- Tema 1: distinguir pendiente de enriquecimiento completo vs solo hechos ---

func TestDigestPending_MarkFactsPending_KindIsFacts(t *testing.T) {
	p := NewDigestPending(filepath.Join(t.TempDir(), "digest-pending.json"))
	p.MarkFactsPending("s1")
	if kind := p.PendingKind("s1"); kind != DigestPendingKindFacts {
		t.Errorf("PendingKind = %q, want %q", kind, DigestPendingKindFacts)
	}
}

func TestDigestPending_MarkFailed_KindIsEnrichment(t *testing.T) {
	p := NewDigestPending(filepath.Join(t.TempDir(), "digest-pending.json"))
	p.MarkFailed("s1")
	if kind := p.PendingKind("s1"); kind != DigestPendingKindEnrichment {
		t.Errorf("PendingKind = %q, want %q", kind, DigestPendingKindEnrichment)
	}
}

func TestDigestPending_PendingKind_NoEntry_Empty(t *testing.T) {
	p := NewDigestPending(filepath.Join(t.TempDir(), "digest-pending.json"))
	if kind := p.PendingKind("s1"); kind != "" {
		t.Errorf("PendingKind sin entrada = %q, want \"\"", kind)
	}
}

// TestDigestPending_MarkFailed_OverwritesFactsKind confirma que una falla
// real del enriquecimiento completo siempre exige reintentar todo, aunque la
// sesión ya tuviera un pendiente de "solo hechos" de un ciclo anterior.
func TestDigestPending_MarkFailed_OverwritesFactsKind(t *testing.T) {
	p := NewDigestPending(filepath.Join(t.TempDir(), "digest-pending.json"))
	p.MarkFactsPending("s1")
	p.MarkFailed("s1")
	if kind := p.PendingKind("s1"); kind != DigestPendingKindEnrichment {
		t.Errorf("PendingKind = %q, want %q tras MarkFailed", kind, DigestPendingKindEnrichment)
	}
}

func TestDigestPending_CountByKind_SplitsByType(t *testing.T) {
	p := NewDigestPending(filepath.Join(t.TempDir(), "digest-pending.json"))
	p.MarkFailed("s1")
	p.MarkFactsPending("s2")
	p.MarkFactsPending("s3")
	enrichment, facts := p.CountByKind()
	if enrichment != 1 || facts != 2 {
		t.Errorf("CountByKind = (%d, %d), want (1, 2)", enrichment, facts)
	}
}

func TestDigestPending_CountByKind_NilReceiver_Zero(t *testing.T) {
	var p *DigestPending
	enrichment, facts := p.CountByKind()
	if enrichment != 0 || facts != 0 {
		t.Errorf("CountByKind sobre nil = (%d, %d), want (0, 0)", enrichment, facts)
	}
}

func writeCorruptFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("{esto no es json"), 0o644); err != nil {
		t.Fatal(err)
	}
}
