package store

import (
	"math/rand"
	"sync"
	"time"
)

// idEpoch es el punto de referencia para el componente de timestamp de
// NewID — 2025-01-01T00:00:00Z en milisegundos. Reduce el rango que
// necesita el timestamp (no hace falta representar 1970), dejando más
// margen antes de acercarse al límite de 41 bits (~69 años desde acá).
const idEpoch = 1735689600000

const (
	idSaltBits    = 10                 // "quién" generó — aleatorio, fijo por proceso
	idCounterBits = 12                 // "cuál" dentro del mismo milisegundo — monotónico
	idStride      = 16                 // hueco entre ids consecutivos del mismo milisegundo
	idMaxSeq      = 1 << idCounterBits // techo del contador (se espera al ms siguiente)
)

// idGen es el generador de ids del store. Es una variable para que los tests
// puedan forzar una colisión de id (ver observation_internal_test.go).
var idGen = NewID

// idSalt identifica a este proceso frente a otros que puedan estar
// generando IDs al mismo tiempo (el daemon compartido + hooks de vida
// corta que escriben directo cuando el daemon no responde a tiempo).
// Generado una sola vez al cargar el paquete — NO es un contador, es un
// valor aleatorio fijo para toda la vida del proceso.
var idSalt = int64(rand.Intn(1 << idSaltBits))

var (
	idMu     sync.Mutex
	idLastMs int64
	idSeq    int64
)

// NewID genera un ID de 64 bits estilo Snowflake (timestamp + salt de
// proceso + contador), generado por la app en vez de por el backend
// (AUTOINCREMENT/BIGSERIAL).
//
// Por qué: SQLite y Postgres tienen secuencias de autoincrement
// independientes — el mismo ID numérico podía referirse a filas distintas
// en cada backend (o no existir en uno de los dos), un problema real
// documentado varias veces en memoria de kronos. Generar el ID acá, antes
// del insert, y pasarlo explícito a ambos backends elimina la divergencia
// sin necesitar coordinación entre ellos: ninguno de los dos rechaza un PK
// explícito — AUTOINCREMENT/BIGSERIAL solo generan un valor cuando se
// omite, así que esto no requiere ninguna migración de schema.
//
// Primer diseño (descartado): solo timestamp + bits aleatorios, sin
// contador. Fallaba con miles de colisiones reales en un loop rápido
// dentro del mismo proceso (ej. una importación masiva) — con suficientes
// llamadas en el mismo milisegundo, el cumpleaños paradoja garantiza
// colisiones aunque el espacio aleatorio sea grande. Un contador
// monotónico por proceso (idSeq) elimina esa clase de colisión por
// completo — DENTRO de un mismo proceso, dos llamadas nunca pueden dar el
// mismo ID. El salt (idSalt, aleatorio pero fijo por proceso) es lo que
// evita colisiones ENTRE procesos distintos generando en el mismo
// milisegundo con el mismo valor de contador — con 1024 valores posibles
// de salt y kronos nunca teniendo más que un puñado de procesos
// escribiendo a la vez, la probabilidad real es insignificante.
//
// Los IDs viejos (chicos, generados por el autoincrement de cada backend
// antes de este cambio) nunca colisionan con los nuevos: un timestamp de
// 2025+ desplazado 22 bits siempre da un número muchísimo más grande que
// cualquier autoincrement histórico realista. No hace falta renumerar
// nada — ambos esquemas conviven sin conflicto.
func NewID() int64 {
	idMu.Lock()
	defer idMu.Unlock()

	ms := time.Now().UnixMilli() - idEpoch
	if ms == idLastMs {
		// El contador avanza de a idStride y no de a 1: así los ids generados
		// dejan huecos. Importa porque SQLite numera las filas insertadas sin
		// id explícito con max(id)+1, y sin huecos ese valor cae exactamente en
		// el siguiente id de este proceso dentro del mismo milisegundo →
		// "UNIQUE constraint failed: observations.id" y, con INSERT OR IGNORE,
		// la fila se perdía en silencio (bug real, visto en la suite completa).
		// Con el hueco, los ids que asigna SQLite nunca son un id de snowflake.
		idSeq += idStride
		if idSeq >= idMaxSeq {
			// agotamos los ids de este milisegundo — esperar al siguiente
			// en vez de arriesgar overflow hacia los bits del salt.
			for ms <= idLastMs {
				time.Sleep(50 * time.Microsecond)
				ms = time.Now().UnixMilli() - idEpoch
			}
			idSeq = 0
		}
	} else {
		idSeq = 0
	}
	idLastMs = ms

	return (ms << (idSaltBits + idCounterBits)) | (idSalt << idCounterBits) | idSeq
}
