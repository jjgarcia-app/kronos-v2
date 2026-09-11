package llm

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// breakerDefaultFailures y breakerDefaultOpenMinutes son los defaults de
// config.LLMConfig (llm.breaker_failures / llm.breaker_minutes) — repetidos
// acá para que un Breaker construido con threshold/openFor <= 0 (config
// vacía, tests) tenga un comportamiento razonable en vez de abrirse en el
// primer fallo o no cerrarse nunca.
const (
	breakerDefaultFailures = 3
	breakerDefaultOpenFor  = 30 * time.Minute
	breakerStateFileName   = "llm-breaker.json"
)

// BreakerState es el estado en disco del cortacircuitos — compartido entre
// el daemon y cada proceso corto de hook (`kronos hook ...`) vía un archivo
// JSON en el data dir, porque las llamadas que este cortacircuitos protege
// (digest, captura pasiva, judge) corren en procesos de SO distintos: un
// cortacircuitos solo en memoria nunca vería los fallos de los demás.
type BreakerState struct {
	ConsecutiveFailures int       `json:"consecutive_failures"`
	OpenUntil           time.Time `json:"open_until,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// Breaker corta las llamadas al LLM local tras N fallos consecutivos —
// motivado por un bug real medido en producción: en una máquina cargada,
// las llamadas de generación de Ollama (no el ping de /api/tags, que
// responde rápido) colgaban muy por encima del presupuesto de 15-20s del
// hook, y cada intento fallido se repetía en cada prompt, en silencio,
// mientras el proceso llama-server colgado seguía consumiendo CPU que le
// hacía falta al resto de las sesiones.
type Breaker struct {
	path             string
	failureThreshold int
	openFor          time.Duration
}

// NewBreaker crea un Breaker con estado persistido en path. failureThreshold
// <= 0 y openFor <= 0 caen a los defaults (3 fallos, 30 minutos).
func NewBreaker(path string, failureThreshold int, openFor time.Duration) *Breaker {
	if failureThreshold <= 0 {
		failureThreshold = breakerDefaultFailures
	}
	if openFor <= 0 {
		openFor = breakerDefaultOpenFor
	}
	return &Breaker{path: path, failureThreshold: failureThreshold, openFor: openFor}
}

// DefaultBreakerPath arma la ruta estándar del archivo de estado dentro de
// dataDir — separado de NewBreaker para que quien construye el Breaker (ver
// NewOllamaFromConfig) no tenga que conocer el nombre de archivo.
func DefaultBreakerPath(dataDir string) string {
	return filepath.Join(dataDir, breakerStateFileName)
}

func (b *Breaker) load() BreakerState {
	data, err := os.ReadFile(b.path)
	if err != nil {
		return BreakerState{}
	}
	var st BreakerState
	if err := json.Unmarshal(data, &st); err != nil {
		return BreakerState{} // corrupto — arrancar de cero en vez de fallar
	}
	return st
}

// save escribe el estado con tmp+rename (atómico) — el sufijo del temporal
// incluye el PID para que dos procesos escribiendo al mismo tiempo no pisen
// el archivo temporal del otro (sí pueden pisarse el rename final, pero eso
// deja el archivo en un estado válido de uno de los dos, nunca corrupto).
func (b *Breaker) save(st BreakerState) {
	st.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0755); err != nil {
		return
	}
	tmp := b.path + ".tmp." + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	if err := os.Rename(tmp, b.path); err != nil {
		_ = os.Remove(tmp)
	}
}

func isOpen(st BreakerState, now time.Time) bool {
	return !st.OpenUntil.IsZero() && now.Before(st.OpenUntil)
}

// Allow reporta si corresponde intentar una llamada al LLM ahora mismo.
func (b *Breaker) Allow() bool {
	return !isOpen(b.load(), time.Now())
}

// State expone el estado en disco para introspección (ver `kronos doctor`)
// — no participa de la decisión de Allow, que además interpreta OpenUntil
// contra time.Now().
func (b *Breaker) State() BreakerState {
	return b.load()
}

// RecordSuccess cierra el cortacircuitos y resetea el contador de fallos —
// solo escribe y loguea si había algo que cerrar, para no pagar una
// escritura a disco en cada llamada exitosa de un cortacircuitos que nunca
// estuvo en problemas.
func (b *Breaker) RecordSuccess() {
	st := b.load()
	if st.ConsecutiveFailures == 0 && st.OpenUntil.IsZero() {
		return
	}
	wasOpen := isOpen(st, time.Now())
	b.save(BreakerState{})
	if wasOpen {
		slog.Info("llm breaker cerrado tras una llamada exitosa", "path", b.path)
	}
}

// RecordFailure cuenta un fallo y abre el cortacircuitos al llegar a
// failureThreshold fallos consecutivos — loguea la apertura una sola vez por
// transición (cerrado→abierto), no en cada fallo mientras ya está abierto.
func (b *Breaker) RecordFailure(callErr error) {
	if callErr == nil {
		return
	}
	now := time.Now()
	st := b.load()
	wasOpen := isOpen(st, now)

	st.ConsecutiveFailures++
	st.LastError = callErr.Error()

	if st.ConsecutiveFailures >= b.failureThreshold {
		st.OpenUntil = now.Add(b.openFor)
		b.save(st)
		if !wasOpen {
			slog.Warn("llm breaker abierto tras fallos consecutivos",
				"failures", st.ConsecutiveFailures,
				"reintenta_en", st.OpenUntil.Format(time.RFC3339),
				"ultimo_error", st.LastError)
		}
		return
	}
	b.save(st)
}
