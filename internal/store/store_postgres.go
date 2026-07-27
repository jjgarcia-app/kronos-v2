package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
)

// connectTimeout acota cuánto puede tardar el intento inicial de conexión +
// migración contra Postgres. Sin esto, si Postgres/Docker está caído, el
// arranque del server MCP puede colgarse mucho más de lo que el cliente MCP
// espera para el handshake — apareciendo como "disconnected" en vez de
// degradar rápido al buffer SQLite local como está pensado.
const connectTimeout = 3 * time.Second

// NewPostgres opens a PostgreSQL database and runs migrations.
func NewPostgres(dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	s := &Store{db: db, driver: "postgres"}
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := s.migratePostgres(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate postgres: %w", err)
	}
	return s, nil
}

// NewFromConfig creates a Store based on the configuration.
func NewFromConfig(cfg config.Config) (*Store, error) {
	if cfg.DB.Backend == "postgres" {
		if cfg.DB.PostgresDSN == "" {
			return nil, fmt.Errorf("postgres backend requires db.postgres_dsn")
		}
		return NewPostgres(cfg.DB.PostgresDSN)
	}
	if cfg.DB.SQLitePath != "" {
		return New(cfg.DB.SQLitePath)
	}
	return nil, fmt.Errorf("no db path configured; use platform.DBPath()")
}

func (s *Store) migratePostgres(ctx context.Context) error {
	var current int
	row := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`)
	_ = row.Scan(&current)

	for i, sql := range postgresMigrations {
		version := i + 1
		if version <= current {
			continue
		}
		if _, err := s.db.ExecContext(ctx, sql); err != nil {
			return fmt.Errorf("migration v%d: %w", version, err)
		}
		if version > 1 {
			_, _ = s.db.ExecContext(ctx,
				`INSERT INTO schema_migrations(version, applied_at) VALUES ($1, $2)`,
				version, time.Now().UTC().Format(time.RFC3339),
			)
		}
	}

	_, _ = s.db.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, applied_at) VALUES (1, $1) ON CONFLICT DO NOTHING`,
		time.Now().UTC().Format(time.RFC3339),
	)
	return nil
}
