package store

import (
	"testing"
	"time"
)

// unreachablePgDSN falla rápido (connection refused en localhost:1) en vez
// de colgarse en un timeout de red — mismo DSN que usa
// TestNewDualFromDSN_PrimaryUnreachable_DegradesGracefully.
const unreachablePgDSN = "postgresql://user:pass@127.0.0.1:1/nope"

// testPostgresDSN es la misma base que usa store_postgres_test.go (package
// store_test) — duplicada acá porque un archivo de tests white-box
// (package store) no puede importar un const no exportado de un paquete de
// test externo (package store_test).
const testPostgresDSN = "postgresql://postgres:kronos@localhost:5432/kronos?sslmode=disable"

// TestDualStore_IsPrimaryDown_TTLNotExpired_DoesNotRetry cubre el guardrail
// del fix: dentro de primaryRetryTTL, isPrimaryDown() no debe intentar
// reconectar — solo leer el flag. Lo verificamos indirectamente: un intento
// de reconexión (exitoso o no) siempre reescribe downSince a time.Now();
// si downSince queda intacto, no hubo intento.
func TestDualStore_IsPrimaryDown_TTLNotExpired_DoesNotRetry(t *testing.T) {
	ds := newTestDualStore(t)
	ds.primaryDSN = unreachablePgDSN
	ds.down = true
	original := time.Now().Add(-1 * time.Second) // dentro del TTL de 5s
	ds.downSince = original

	if !ds.isPrimaryDown() {
		t.Fatal("debería seguir down dentro del TTL")
	}
	if !ds.downSince.Equal(original) {
		t.Error("downSince cambió — isPrimaryDown() intentó reconectar antes de que venza el TTL")
	}
}

// TestDualStore_IsPrimaryDown_TTLExpired_RetriesAndFailsGracefully es el
// caso que exponía el bug real: antes del fix, un down=true solo se
// limpiaba en el próximo tick de syncLoop (60s-60min de backoff). Ahora, al
// vencer un TTL corto, isPrimaryDown() reintenta por su cuenta — acá el
// intento falla (DSN inalcanzable) pero debe fallar con gracia: sigue
// reportando down y re-arma downSince en vez de dejarlo estancado.
func TestDualStore_IsPrimaryDown_TTLExpired_RetriesAndFailsGracefully(t *testing.T) {
	ds := newTestDualStore(t)
	ds.primaryDSN = unreachablePgDSN
	ds.down = true
	ds.downSince = time.Now().Add(-10 * time.Second) // vencido (TTL = 5s)

	if !ds.isPrimaryDown() {
		t.Fatal("con el DSN inalcanzable debería seguir down tras el retry")
	}
	if time.Since(ds.downSince) > time.Second {
		t.Error("downSince no se re-armó — el retry no se intentó pese a que el TTL venció")
	}
}

// TestDualStore_IsPrimaryDown_TTLExpired_ReconnectSucceeds_MarksUp es la
// regresión central del bug: una observación guardada en primary mientras
// estaba sano se volvía invisible (mem_get_observation/mem_context/etc.)
// durante la ventana de down si algo marcaba down=true en el medio. Con
// Postgres real disponible, el retry del read path debe restablecer
// primary sin esperar al syncLoop.
func TestDualStore_IsPrimaryDown_TTLExpired_ReconnectSucceeds_MarksUp(t *testing.T) {
	pg, err := NewPostgres(testPostgresDSN)
	if err != nil {
		t.Skipf("Postgres no disponible en %s, se salta el test de integración: %v", testPostgresDSN, err)
	}
	pg.Close()

	ds := newTestDualStore(t)
	ds.primaryDSN = testPostgresDSN
	ds.down = true
	ds.downSince = time.Now().Add(-10 * time.Second) // vencido

	if ds.isPrimaryDown() {
		t.Fatal("con Postgres real disponible, el retry debería restablecer primary y devolver down=false")
	}
	if ds.primary == nil {
		t.Error("primary debería haberse reemplazado por la conexión reconectada")
	}
	t.Cleanup(func() { ds.primary.Close() })
}
