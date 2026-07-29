package store

import (
	"sync"
	"testing"
	"time"
)

func TestNewID_AlwaysPositive(t *testing.T) {
	for i := 0; i < 10000; i++ {
		id := NewID()
		if id <= 0 {
			t.Fatalf("NewID() = %d, want > 0 (iteración %d)", id, i)
		}
	}
}

func TestNewID_NoCollisionsAcrossManyCalls(t *testing.T) {
	seen := make(map[int64]bool, 50000)
	for i := 0; i < 50000; i++ {
		id := NewID()
		if seen[id] {
			t.Fatalf("colisión detectada: ID %d repetido en la iteración %d", id, i)
		}
		seen[id] = true
	}
}

// TestNewID_RoughlyIncreasesOverTime confirma que IDs generados en
// milisegundos distintos quedan ordenados por tiempo — el componente de
// timestamp domina los bits altos, así que un ID posterior (con margen de
// más de 1ms) siempre debería ser numéricamente mayor. Dentro del MISMO
// milisegundo el orden no está garantizado (el componente aleatorio puede
// dar cualquier valor), por eso el test fuerza un salto real de tiempo.
func TestNewID_RoughlyIncreasesOverTime(t *testing.T) {
	a := NewID()
	time.Sleep(2 * time.Millisecond)
	b := NewID()
	if b <= a {
		t.Errorf("ID generado 2ms después (%d) debería ser mayor que el anterior (%d)", b, a)
	}
}

// TestNewID_LegacyIDsNeverCollide confirma que cualquier ID Snowflake nuevo
// es muchísimo más grande que un autoincrement histórico realista (kronos
// tiene ~800 observaciones hoy) — los dos esquemas de ID conviven sin
// necesidad de renumerar datos viejos.
func TestNewID_LegacyIDsNeverCollide(t *testing.T) {
	const maxRealisticLegacyID = 100_000 // muy por encima del volumen real actual
	id := NewID()
	if id <= maxRealisticLegacyID {
		t.Errorf("NewID() = %d, esperaba un valor muchísimo mayor que cualquier autoincrement legacy (%d)", id, maxRealisticLegacyID)
	}
}

// TestNewID_ConcurrentCallsNeverCollide reproduce el escenario real que
// causó la primera versión del diseño (solo timestamp + aleatorio, sin
// contador) a colisionar: muchas llamadas rápidas dentro del mismo
// milisegundo, ahora desde goroutines concurrentes de verdad en vez de un
// loop secuencial. El mutex + contador monotónico deben garantizar cero
// colisiones sin importar cuántas goroutines llamen a la vez.
func TestNewID_ConcurrentCallsNeverCollide(t *testing.T) {
	const goroutines = 50
	const perGoroutine = 1000

	var mu sync.Mutex
	seen := make(map[int64]bool, goroutines*perGoroutine)
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				id := NewID()
				mu.Lock()
				if seen[id] {
					t.Errorf("colisión detectada bajo concurrencia real: ID %d repetido", id)
				}
				seen[id] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) != goroutines*perGoroutine {
		t.Errorf("esperaba %d IDs únicos, hay %d", goroutines*perGoroutine, len(seen))
	}
}
