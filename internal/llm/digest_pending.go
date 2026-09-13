package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const digestPendingFileName = "digest-pending.json"

// digestPendingMaxAttempts: tope de reintentos de enriquecimiento por
// sesión — a partir de acá se asume que el LLM no va a servir para esta
// sesión en este ciclo (CLI roto, credenciales vencidas, etc.) y se deja de
// insistir; la sesión sigue con el digest determinístico nada más.
const digestPendingMaxAttempts = 3

// digestPendingMaxAge: un pendiente más viejo que esto se descarta sin
// reintentar — evita que una sesión abandonada hace días dispare una llamada
// al LLM cuando alguien por fin la retoma, mucho después de que el pico de
// carga que causó la falla original ya pasó.
const digestPendingMaxAge = 12 * time.Hour

// Los dos tipos de pendiente que puede dejar un ciclo de enriquecimiento del
// digest (ver internal/hooks.MaybeUpdateDigest):
//   - DigestPendingKindEnrichment: la llamada completa (prosa + hechos) falló
//     o se pasó de tiempo — el próximo tick reintenta todo, igual que antes de
//     que existiera el tipo "facts".
//   - DigestPendingKindFacts: la prosa se guardó bien, pero la respuesta no
//     trajo una sección de hechos explícita (ni lista ni "FACTS: ninguno") —
//     el próximo tick reintenta SOLO los hechos con un pedido acotado, sin
//     volver a pedir la prosa que ya está guardada.
//
// Una entrada persistida sin "kind" (archivo escrito por un binario viejo, o
// zero-value en un test) se trata como DigestPendingKindEnrichment — mismo
// comportamiento que tenía todo pendiente antes de que existiera esta
// distinción.
const (
	DigestPendingKindEnrichment = "enrichment"
	DigestPendingKindFacts      = "facts"
)

// DigestPendingEntry marca que el enriquecimiento por LLM del digest de una
// sesión falló, se pasó de tiempo, o tuvo éxito en la prosa sin traer una
// respuesta explícita sobre hechos — y debe reintentarse (todo, o solo los
// hechos, según Kind) en la próxima oportunidad SIN esperar a que venza
// digest.interval_minutes de nuevo — ver internal/hooks.MaybeUpdateDigest.
type DigestPendingEntry struct {
	SessionID string    `json:"session_id"`
	Kind      string    `json:"kind,omitempty"`
	Attempts  int       `json:"attempts"`
	FirstAt   time.Time `json:"first_at"`
}

// normalizedKind devuelve el Kind de la entrada, con DigestPendingKindEnrichment
// como default para datos viejos sin el campo (ver comentario de los consts).
func (e DigestPendingEntry) normalizedKind() string {
	if e.Kind == "" {
		return DigestPendingKindEnrichment
	}
	return e.Kind
}

type digestPendingState struct {
	Entries []DigestPendingEntry `json:"entries"`
}

// DigestPending persiste, en un archivo del data dir, qué sesiones tienen un
// enriquecimiento de digest pendiente de reintento — mismo mecanismo de
// archivo compartido entre procesos que Usage/LastFailure (ver usage.go/
// diagnostics.go): lectura tolerante a corrupción (arranca de cero) y
// escritura atómica (tmp+rename, ver atomicWriteJSON).
type DigestPending struct {
	path string
}

// NewDigestPending crea un DigestPending con estado persistido en path.
func NewDigestPending(path string) *DigestPending {
	return &DigestPending{path: path}
}

// DefaultDigestPendingPath arma la ruta estándar del archivo de pendientes
// dentro de dataDir, igual que DefaultUsagePath/DefaultBreakerPath.
func DefaultDigestPendingPath(dataDir string) string {
	return filepath.Join(dataDir, digestPendingFileName)
}

func (p *DigestPending) load() digestPendingState {
	data, err := os.ReadFile(p.path)
	if err != nil {
		return digestPendingState{}
	}
	var st digestPendingState
	if err := json.Unmarshal(data, &st); err != nil {
		return digestPendingState{} // corrupto — arrancar de cero, igual que Usage/Breaker
	}
	return st
}

func (p *DigestPending) save(st digestPendingState) {
	_ = atomicWriteJSON(p.path, st)
}

// pruneExpired descarta entradas que ya agotaron digestPendingMaxAttempts o
// que superan digestPendingMaxAge — se aplica en cada lectura/escritura para
// que un pendiente abandonado no quede vivo para siempre en el archivo.
func pruneExpired(entries []DigestPendingEntry, now time.Time) []DigestPendingEntry {
	kept := entries[:0]
	for _, e := range entries {
		if e.Attempts >= digestPendingMaxAttempts {
			continue
		}
		if now.Sub(e.FirstAt) > digestPendingMaxAge {
			continue
		}
		kept = append(kept, e)
	}
	return kept
}

// Due indica si sessionID tiene un reintento de enriquecimiento pendiente
// vigente (no agotó los intentos, no expiró) — MaybeUpdateDigest lo consulta
// para saltear el chequeo normal de digest.interval_minutes y reintentar ya.
// nil-safe: un DigestPending nil (no se pudo resolver el data dir) se
// comporta como "sin pendientes", igual que un archivo vacío.
func (p *DigestPending) Due(sessionID string) bool {
	if p == nil {
		return false
	}
	for _, e := range pruneExpired(p.load().Entries, time.Now()) {
		if e.SessionID == sessionID {
			return true
		}
	}
	return false
}

// MarkFailed registra que el enriquecimiento completo (prosa + hechos) de
// sessionID falló o se pasó de tiempo — crea la entrada la primera vez
// (FirstAt=ahora, Attempts=1, Kind=DigestPendingKindEnrichment) o incrementa
// Attempts si ya existía, sobrescribiendo el Kind a "enrichment" (una falla
// real siempre exige reintentar todo, aunque la entrada previa fuera
// "facts"). nil-safe (ver Due): no-op si no hay data dir resuelto.
func (p *DigestPending) MarkFailed(sessionID string) {
	p.markPending(sessionID, DigestPendingKindEnrichment)
}

// MarkFactsPending registra que la prosa del digest de sessionID se guardó
// bien pero la respuesta no trajo una sección de hechos explícita (ni lista
// ni "FACTS: ninguno") — el próximo tick reintenta SOLO los hechos (ver
// DigestPendingKindFacts). Mismo mecanismo de upsert/expiración/tope que
// MarkFailed. nil-safe (ver Due): no-op si no hay data dir resuelto.
func (p *DigestPending) MarkFactsPending(sessionID string) {
	p.markPending(sessionID, DigestPendingKindFacts)
}

// markPending es el upsert compartido por MarkFailed/MarkFactsPending — crea
// la entrada la primera vez (FirstAt=ahora, Attempts=1) o incrementa Attempts
// si ya existía, y siempre fija Kind al valor pedido (una entrada "facts" que
// vuelve a fallar del todo pasa a "enrichment", y viceversa si el próximo
// intento arregla la prosa pero no los hechos). De paso descarta entradas (de
// CUALQUIER sesión) que ya expiraron o agotaron los intentos, así el archivo
// no crece indefinidamente con pendientes muertos.
func (p *DigestPending) markPending(sessionID, kind string) {
	if p == nil {
		return
	}
	now := time.Now()
	entries := pruneExpired(p.load().Entries, now)
	found := false
	for i := range entries {
		if entries[i].SessionID == sessionID {
			entries[i].Attempts++
			entries[i].Kind = kind
			found = true
			break
		}
	}
	if !found {
		entries = append(entries, DigestPendingEntry{SessionID: sessionID, Kind: kind, Attempts: 1, FirstAt: now})
	}
	p.save(digestPendingState{Entries: entries})
}

// Clear quita el pendiente de sessionID — se llama cuando el enriquecimiento
// finalmente tuvo éxito, para que no se siga reintentando de más. nil-safe
// (ver Due): no-op si no hay data dir resuelto.
func (p *DigestPending) Clear(sessionID string) {
	if p == nil {
		return
	}
	now := time.Now()
	entries := pruneExpired(p.load().Entries, now)
	kept := entries[:0]
	for _, e := range entries {
		if e.SessionID != sessionID {
			kept = append(kept, e)
		}
	}
	p.save(digestPendingState{Entries: kept})
}

// Count devuelve cuántas sesiones tienen un enriquecimiento pendiente
// vigente (de cualquier Kind) — nil-safe (ver Due): 0 si no hay data dir
// resuelto.
func (p *DigestPending) Count() int {
	if p == nil {
		return 0
	}
	return len(pruneExpired(p.load().Entries, time.Now()))
}

// CountByKind desglosa Count() por tipo de pendiente — usado por
// `kronos doctor` para que la línea del digest distinga "pendientes de
// enriquecimiento completo" de "pendientes de solo hechos" en vez de un
// número único que no dice qué falta. nil-safe (ver Due): (0, 0) si no hay
// data dir resuelto.
func (p *DigestPending) CountByKind() (enrichment, facts int) {
	if p == nil {
		return 0, 0
	}
	for _, e := range pruneExpired(p.load().Entries, time.Now()) {
		if e.normalizedKind() == DigestPendingKindFacts {
			facts++
		} else {
			enrichment++
		}
	}
	return enrichment, facts
}

// PendingKind devuelve el Kind del pendiente vigente de sessionID
// (DigestPendingKindEnrichment/DigestPendingKindFacts) o "" si no hay
// ninguno due — MaybeUpdateDigest lo consulta para decidir si el próximo
// intento pide todo de nuevo o solo los hechos. nil-safe (ver Due): "" si no
// hay data dir resuelto.
func (p *DigestPending) PendingKind(sessionID string) string {
	if p == nil {
		return ""
	}
	for _, e := range pruneExpired(p.load().Entries, time.Now()) {
		if e.SessionID == sessionID {
			return e.normalizedKind()
		}
	}
	return ""
}
