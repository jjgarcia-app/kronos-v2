package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// resolveFunctionalTestDSN busca el DSN de Postgres en KRONOS_TEST_DSN
// primero (para CI o corridas contra una instancia descartable) y si no
// está seteada cae al config real del usuario (~/.config/kronos/config.json,
// clave db.postgres_dsn) — la misma DB que usa kronos en esta máquina.
// Vacío si ninguna de las dos fuentes tiene un DSN.
func resolveFunctionalTestDSN(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("KRONOS_TEST_DSN"); dsn != "" {
		return dsn
	}
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	return cfg.DB.PostgresDSN
}

// TestPostgresBackend_AllReadPathsSucceed es el test funcional pedido por la
// auditoría de placeholders: abre el *Store Postgres real y llama a cada
// método que corre en el camino de lectura de una sesión (los mismos que la
// sonda cmd/sqlprobe detectó rotos por "?" sin rebind — ver
// CountObservations y ListRelations en observation.go/relations.go). Si
// cualquiera de estos vuelve a romper con "syntax error at or near AND/OR"
// (regresión de la clase de bug documentada en docs/architecture.md), este
// test falla contra Postgres real — no alcanza con que pase contra SQLite.
func TestPostgresBackend_AllReadPathsSucceed(t *testing.T) {
	dsn := resolveFunctionalTestDSN(t)
	if dsn == "" {
		t.Skip("sin DSN de Postgres (KRONOS_TEST_DSN o db.postgres_dsn en config) — se salta el test funcional")
	}

	s, err := store.NewPostgres(dsn)
	if err != nil {
		t.Skipf("Postgres no disponible en %s, se salta el test funcional: %v", dsn, err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()

	if _, err := s.Stats(ctx); err != nil {
		t.Errorf("Stats: %v", err)
	}

	if _, err := s.CountObservations(ctx, ""); err != nil {
		t.Errorf("CountObservations: %v (syntax error at or near AND es el síntoma exacto del bug de placeholders)", err)
	}

	if _, err := s.ListObservations(ctx, "", 5, 0); err != nil {
		t.Errorf("ListObservations: %v", err)
	}

	if _, err := s.ListAll(ctx, ""); err != nil {
		t.Errorf("ListAll: %v", err)
	}

	if _, err := s.ListRelations(ctx, "", store.JudgmentPending, 50, 0); err != nil {
		t.Errorf("ListRelations(pending): %v (syntax error at or near AND es el síntoma exacto del bug de placeholders)", err)
	}
	if _, err := s.ListRelations(ctx, "", "", 50, 0); err != nil {
		t.Errorf("ListRelations(todos): %v (syntax error at or near OR es el síntoma exacto del bug de placeholders)", err)
	}

	if _, err := s.Search(ctx, store.SearchParams{Query: "postgres", Limit: 5}); err != nil {
		t.Errorf("Search: %v", err)
	}

	to := time.Now().UTC()
	from := to.AddDate(0, 0, -7)
	if _, err := s.Timesheet(ctx, from, to, ""); err != nil {
		t.Errorf("Timesheet: %v", err)
	}

	if _, err := s.GetActiveSession(ctx, ""); err != nil {
		t.Errorf("GetActiveSession: %v", err)
	}

	if _, err := s.ListSessions(ctx, "", 5); err != nil {
		t.Errorf("ListSessions: %v", err)
	}

	if _, err := s.AllSessions(ctx, 5); err != nil {
		t.Errorf("AllSessions: %v", err)
	}

	sessionID := "test-placeholder-audit-" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		_, _ = s.DB().ExecContext(context.Background(), `DELETE FROM sessions WHERE id = $1`, sessionID)
	})
	if _, err := s.CreateSession(ctx, sessionID, "kronos-placeholder-audit", "/tmp"); err != nil {
		t.Errorf("CreateSession: %v", err)
	}
}
