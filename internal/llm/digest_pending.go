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

// DigestPendingEntry marca que el enriquecimiento por LLM del digest de una
// sesión falló o se pasó de tiempo, y debe reintentarse en la próxima
// oportunidad SIN esperar a que venza digest.interval_minutes de nuevo — ver
// internal/hooks.MaybeUpdateDigest.
type DigestPendingEntry struct {
	SessionID string    `json:"session_id"`
	Attempts  int       `json:"attempts"`
	FirstAt   time.Time `json:"first_at"`
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

// MarkFailed registra que el enriquecimiento de sessionID falló o se pasó de
// tiempo — crea la entrada la primera vez (FirstAt=ahora, Attempts=1) o
// incrementa Attempts si ya existía. De paso descarta entradas (de CUALQUIER
// sesión) que ya expiraron o agotaron los intentos, así el archivo no crece
// indefinidamente con pendientes muertos. nil-safe (ver Due): no-op si no
// hay data dir resuelto.
func (p *DigestPending) MarkFailed(sessionID string) {
	if p == nil {
		return
	}
	now := time.Now()
	entries := pruneExpired(p.load().Entries, now)
	found := false
	for i := range entries {
		if entries[i].SessionID == sessionID {
			entries[i].Attempts++
			found = true
			break
		}
	}
	if !found {
		entries = append(entries, DigestPendingEntry{SessionID: sessionID, Attempts: 1, FirstAt: now})
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
// vigente — usado por `kronos doctor` para reportarlo en la línea del
// digest. nil-safe (ver Due): 0 si no hay data dir resuelto.
func (p *DigestPending) Count() int {
	if p == nil {
		return 0
	}
	return len(pruneExpired(p.load().Entries, time.Now()))
}
