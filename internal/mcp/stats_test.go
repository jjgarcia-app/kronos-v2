package mcp_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	kronosmcp "github.com/jjgarcia-app/kronos-v2/internal/mcp"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func TestMemStats_UsesPlainStore(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	ctx := context.Background()

	if _, err := st.CreateSession(ctx, "sess-stats-1", "kronos-v2", "/tmp"); err != nil {
		t.Fatal(err)
	}

	out := call(t, srv, "mem_stats", map[string]any{})
	if !strings.Contains(out, "**Sesiones**: 1") {
		t.Errorf("output no refleja la sesión creada: %s", out)
	}
}

// testStatsPostgresDSN — misma DB local que usa kronos en esta máquina, ver
// internal/store/store_postgres_test.go.
const testStatsPostgresDSN = "postgresql://postgres:kronos@localhost:5432/kronos?sslmode=disable"

// TestMemStats_ReadsFromPrimaryNotBuffer_RealPostgres reproduce el bug real:
// handleMemStats usaba s.localStore().Stats() — SIEMPRE el buffer SQLite,
// sin importar el estado del primary. Una sesión creada solo en Postgres
// (primary sano → CreateSession nunca toca el buffer, ver DualStore.CreateSession)
// no aparecía en mem_stats hasta que, por casualidad, también existiera en
// el buffer. Este test crea la sesión SOLO en primary y confirma que
// mem_stats la ve — solo pasa si el handler lee de s.store (primary-aware),
// no del buffer.
func TestMemStats_ReadsFromPrimaryNotBuffer_RealPostgres(t *testing.T) {
	f, err := os.CreateTemp("", "kronos-mcp-stats-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	buffer, err := store.New(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { buffer.Close() })

	dual, err := store.NewDualFromDSN(buffer, testStatsPostgresDSN)
	if err != nil {
		t.Skipf("Postgres no disponible en %s, se salta el test de integración: %v", testStatsPostgresDSN, err)
	}
	t.Cleanup(func() { dual.Close() })

	srv := kronosmcp.New(dual, 10, 20)
	ctx := context.Background()

	// ID único por corrida — esto escribe contra la Postgres real y
	// compartida de la máquina; un ID fijo colisiona con la fila que dejó
	// una corrida anterior (EndSession no borra, solo marca ended_at) y
	// hace fallar el INSERT en primary, tirando el test justo al camino de
	// fallback a buffer que se supone debe probar que NO pasa.
	sessionID := fmt.Sprintf("sess-stats-primary-only-%d", time.Now().UnixNano())
	sess, err := dual.CreateSession(ctx, sessionID, "kronos-v2", "/tmp")
	if err != nil {
		t.Skipf("Postgres no disponible para escribir, se salta: %v", err)
	}
	t.Cleanup(func() {
		_ = dual.EndSession(context.Background(), sess.ID, "cleanup de test")
	})

	if got, _ := buffer.GetSession(ctx, sess.ID); got != nil {
		t.Fatal("la sesión ya existía en el buffer — el test no prueba lo que dice probar")
	}

	// El buffer temporal es fresco y CreateSession con primary sano nunca lo
	// toca — así que si mem_stats leyera del buffer (el bug original),
	// devolvería exactamente "0" acá, sin importar cuántas sesiones reales
	// haya en Postgres. No comparamos contra un conteo exacto de Postgres
	// porque hay procesos kronos reales escribiendo en paralelo contra la
	// misma DB — el número puede cambiar entre el setup y la aserción.
	bufStats, err := buffer.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bufStats.TotalSessions != 0 {
		t.Fatalf("buffer.TotalSessions = %d, want 0 — el buffer temporal no debería tener nada todavía", bufStats.TotalSessions)
	}

	out := call(t, srv, "mem_stats", map[string]any{})
	if strings.Contains(out, "**Sesiones**: 0") {
		t.Errorf("mem_stats devolvió 0 sesiones — está leyendo el buffer vacío en vez de Postgres primary: %s", out)
	}
}
