package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

type Store struct {
	db     *sql.DB
	driver string
}

func New(dbPath string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)", dbPath)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// SQLite funciona mejor con una sola conexión escritora
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db, driver: "sqlite3"}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) migrate() error {
	ctx := context.Background()

	// determinar versión actual
	var current int
	row := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`)

	// si la tabla no existe aún, current queda en 0
	_ = row.Scan(&current)

	for i, sql := range migrations {
		version := i + 1
		if version <= current {
			continue
		}

		if _, err := s.db.ExecContext(ctx, sql); err != nil {
			return fmt.Errorf("migration v%d: %w", version, err)
		}

		// registrar migración aplicada (schema_migrations puede no existir en v1)
		if version > 1 {
			_, _ = s.db.ExecContext(ctx,
				`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
				version, time.Now().UTC().Format(time.RFC3339),
			)
		}
	}

	// asegurar que v1 quede registrada
	_, _ = s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (1, ?)`,
		time.Now().UTC().Format(time.RFC3339),
	)

	return nil
}

// rebind converts ? placeholders to $N for postgres.
func (s *Store) rebind(query string) string {
	if s.driver != "postgres" {
		return query
	}
	var n int
	var sb strings.Builder
	for _, r := range query {
		if r == '?' {
			n++
			fmt.Fprintf(&sb, "$%d", n)
		} else {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func nullableTime(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s.String)
	if err != nil {
		return nil
	}
	return &t
}
