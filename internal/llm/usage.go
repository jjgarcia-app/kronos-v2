package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	usageStateFileName = "llm-usage.json"

	// usageRetentionDays acota cuánto tiempo de buckets por hora se conserva
	// — con el volumen real (unas pocas llamadas por hora) unos días alcanzan
	// de sobra para "última hora"/"hoy" en `kronos doctor` sin dejar crecer el
	// archivo indefinidamente.
	usageRetentionDays = 7

	// Resultados posibles de un intento de generación — un valor por llamada,
	// se cuenten o no contra el cortacircuitos (los "skipped_*" son salteos
	// deliberados, no fallos del backend).
	UsageResultOK             = "ok"
	UsageResultError          = "error"
	UsageResultSkippedLoad    = "skipped_load"
	UsageResultSkippedBreaker = "skipped_breaker"
)

// UsageBucket cuenta cuántas llamadas de generación con un (proveedor,
// resultado) dado cayeron en una hora puntual — agregar por hora en vez de
// guardar cada llamada individual mantiene el archivo chico indefinidamente.
type UsageBucket struct {
	HourStart time.Time `json:"hour_start"`
	Provider  string    `json:"provider"`
	Result    string    `json:"result"`
	Count     int       `json:"count"`
}

// UsageState es el estado en disco del contador de uso. LastAt/LastProvider/
// LastResult se guardan aparte de los buckets (que solo tienen granularidad
// de hora) para que "última llamada: hace N min" en `kronos doctor` tenga
// precisión real en vez de quedar atada al borde de la hora.
type UsageState struct {
	Buckets      []UsageBucket `json:"buckets"`
	LastAt       time.Time     `json:"last_at,omitempty"`
	LastProvider string        `json:"last_provider,omitempty"`
	LastResult   string        `json:"last_result,omitempty"`
}

// Usage registra invocaciones de generación del LLM en un archivo del data
// dir — mismo mecanismo de varios-procesos-un-archivo que Breaker (ver
// breaker.go): el daemon y cada proceso corto de hook comparten el mismo
// contador.
type Usage struct {
	path string
}

// NewUsage crea un Usage con estado persistido en path.
func NewUsage(path string) *Usage {
	return &Usage{path: path}
}

// DefaultUsagePath arma la ruta estándar del archivo de uso dentro de
// dataDir, igual que DefaultBreakerPath.
func DefaultUsagePath(dataDir string) string {
	return filepath.Join(dataDir, usageStateFileName)
}

func (u *Usage) load() UsageState {
	data, err := os.ReadFile(u.path)
	if err != nil {
		return UsageState{}
	}
	var st UsageState
	if err := json.Unmarshal(data, &st); err != nil {
		return UsageState{} // corrupto — arrancar de cero, igual que Breaker
	}
	return st
}

func (u *Usage) save(st UsageState) {
	_ = atomicWriteJSON(u.path, st)
}

// Record cuenta una llamada de generación — provider ("ollama"/"claude-cli")
// y result (ok/error/skipped_load/skipped_breaker). Lee, actualiza y escribe
// el archivo entero en cada llamada: el volumen es bajísimo (unas pocas por
// hora) así que la contención de escribir siempre no importa, y evita tener
// que coordinar un flush entre procesos de vida corta (cada hook es un
// proceso de SO distinto que no sobrevive para hacerlo después).
func (u *Usage) Record(provider, result string) {
	now := time.Now()
	st := u.load()
	st.Buckets = pruneOldBuckets(st.Buckets, now)
	st.Buckets = incrementBucket(st.Buckets, provider, result, now)
	st.LastAt = now
	st.LastProvider = provider
	st.LastResult = result
	u.save(st)
}

// State expone el estado en disco para introspección (ver `kronos doctor`) —
// no participa de Record.
func (u *Usage) State() UsageState {
	return u.load()
}

func incrementBucket(buckets []UsageBucket, provider, result string, at time.Time) []UsageBucket {
	hour := at.Truncate(time.Hour)
	for i := range buckets {
		if buckets[i].HourStart.Equal(hour) && buckets[i].Provider == provider && buckets[i].Result == result {
			buckets[i].Count++
			return buckets
		}
	}
	return append(buckets, UsageBucket{HourStart: hour, Provider: provider, Result: result, Count: 1})
}

func pruneOldBuckets(buckets []UsageBucket, now time.Time) []UsageBucket {
	cutoff := now.Add(-usageRetentionDays * 24 * time.Hour)
	kept := buckets[:0]
	for _, b := range buckets {
		if b.HourStart.After(cutoff) {
			kept = append(kept, b)
		}
	}
	return kept
}
