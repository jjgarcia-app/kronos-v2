package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// testStatsPostgresDSN — misma DB local que usa kronos en esta máquina, ver
// internal/store/store_postgres_test.go.
const testStatsPostgresDSN = "postgresql://postgres:kronos@localhost:5432/kronos?sslmode=disable"

// TestHandleStats_ReadsFromPrimaryNotBuffer_RealPostgres reproduce el mismo
// bug ya corregido en el tool MCP mem_stats (ver internal/mcp/handlers.go):
// handleStats usaba sqliteStore().Stats() — SIEMPRE el buffer local, sin
// importar el estado del primary. Una sesión creada solo en Postgres
// (primary sano → CreateSession nunca toca el buffer) no aparecía en
// /stats hasta que, por casualidad, también existiera en el buffer.
func TestHandleStats_ReadsFromPrimaryNotBuffer_RealPostgres(t *testing.T) {
	f, err := os.CreateTemp("", "kronos-server-stats-test-*.db")
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

	// NewDualFromDSN nunca devuelve error si el primary no está disponible
	// (degrada con gracia a propósito) — el chequeo real de disponibilidad
	// va después, viendo si CreateSession terminó en el buffer.
	dual, err := store.NewDualFromDSN(buffer, testStatsPostgresDSN)
	if err != nil {
		t.Skipf("Postgres no disponible en %s, se salta el test de integración: %v", testStatsPostgresDSN, err)
	}
	t.Cleanup(func() { dual.Close() })

	ctx := context.Background()
	sessionID := fmt.Sprintf("sess-server-stats-primary-only-%d", time.Now().UnixNano())
	sess, err := dual.CreateSession(ctx, sessionID, "kronos-v2", "/tmp")
	if err != nil {
		t.Skipf("Postgres no disponible para escribir, se salta: %v", err)
	}
	t.Cleanup(func() {
		_ = dual.EndSession(context.Background(), sess.ID, "cleanup de test")
	})

	if got, _ := buffer.GetSession(ctx, sess.ID); got != nil {
		t.Skip("CreateSession cayó al buffer — Postgres no está disponible en este entorno (ej. CI), se salta el test de integración")
	}

	bufStats, err := buffer.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bufStats.TotalSessions != 0 {
		t.Fatalf("buffer.TotalSessions = %d, want 0 — el buffer temporal no debería tener nada todavía", bufStats.TotalSessions)
	}

	srv := New(dual, 0, "")
	ts := httptest.NewServer(srv.authMiddleware(srv.mux))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body struct {
		Data struct {
			Sessions int `json:"sessions"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Sessions == 0 {
		t.Error("/stats devolvió 0 sesiones — está leyendo el buffer vacío en vez de Postgres primary")
	}
}
