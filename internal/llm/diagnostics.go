package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const lastFailureFileName = "llm-last-failure.json"

// Clasificaciones de falla de claude-cli — ver classifyClaudeCLIFailure en
// claude_cli.go. El texto de cada una es el que se muestra tal cual en el
// log y en `kronos doctor`.
const (
	ClaudeCLIFailureNotLoggedIn   = "no logueado"
	ClaudeCLIFailureIncompatible  = "cli o modelo incompatibles"
	ClaudeCLIFailureTimeout       = "timeout"
	ClaudeCLIFailureBinaryMissing = "binario no encontrado"
	ClaudeCLIFailurePermission    = "permiso denegado"
	ClaudeCLIFailureUnknown       = "desconocido"
	// ClaudeCLIFailureNoCreds: no es una falla del CLI en sí — el chequeo
	// previo de NewClaudeCLIFromConfig no encontró credenciales de Claude
	// Code para armar el config dir aislado, así que ni se llegó a invocar
	// `claude -p`. Se distingue del resto de las clasificaciones (que sí
	// vienen de una invocación real) para que `kronos doctor` señale
	// configuración del entorno en vez de "el CLI falló".
	ClaudeCLIFailureNoCreds = "sin credenciales"
)

// LastFailure es la última clasificación de falla de generación registrada.
// Se persiste aparte del cortacircuitos (ver breaker.go) porque
// Breaker.RecordSuccess borra su propio LastError al primer éxito — acá
// queremos que `kronos doctor` pueda seguir mostrando "último fallo: hace X"
// también después de que el proveedor se recuperó.
type LastFailure struct {
	Provider string    `json:"provider"`
	Kind     string    `json:"kind"`
	Advice   string    `json:"advice"`
	At       time.Time `json:"at"`
}

// DefaultLastFailurePath arma la ruta estándar del archivo de última falla
// dentro de dataDir, igual que DefaultBreakerPath/DefaultUsagePath.
func DefaultLastFailurePath(dataDir string) string {
	return filepath.Join(dataDir, lastFailureFileName)
}

// recordLastFailure persiste la clasificación de la falla más reciente —
// sobrescribe cualquier registro previo, no acumula histórico (a diferencia
// de Usage): a `kronos doctor` solo le interesa la última. No-op si path
// está vacío (Client armado sin data dir resuelto).
func recordLastFailure(path, provider, kind, advice string) {
	if path == "" {
		return
	}
	lf := LastFailure{Provider: provider, Kind: kind, Advice: advice, At: time.Now()}
	_ = atomicWriteJSON(path, lf)
}

// ReadLastFailure lee la última falla clasificada persistida — ok=false si
// nunca hubo ninguna o el archivo está corrupto (se trata como "sin dato",
// igual que el resto de los estados de este paquete tolera JSON corrupto).
func ReadLastFailure(path string) (LastFailure, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LastFailure{}, false
	}
	var lf LastFailure
	if err := json.Unmarshal(data, &lf); err != nil {
		return LastFailure{}, false
	}
	if lf.At.IsZero() {
		return LastFailure{}, false
	}
	return lf, true
}
