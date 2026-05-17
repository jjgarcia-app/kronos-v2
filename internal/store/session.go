package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func (s *Store) CreateSession(ctx context.Context, id, project, directory string) (*Session, error) {
	startedAt := now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(id, project, directory, started_at) VALUES (?, ?, ?, ?)`,
		id, project, directory, startedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	t, _ := time.Parse(time.RFC3339, startedAt)
	return &Session{
		ID:        id,
		Project:   project,
		Directory: directory,
		StartedAt: t,
	}, nil
}

func (s *Store) EndSession(ctx context.Context, id, summary string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET ended_at = ?, summary = ? WHERE id = ?`,
		now(), summary, id,
	)
	if err != nil {
		return fmt.Errorf("end session: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("session not found: %s", id)
	}
	return nil
}

func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project, directory, started_at, ended_at, summary FROM sessions WHERE id = ?`, id)
	return scanSession(row)
}

func (s *Store) GetActiveSession(ctx context.Context, project string) (*Session, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project, directory, started_at, ended_at, summary
		 FROM sessions
		 WHERE project = ? AND ended_at IS NULL
		 ORDER BY started_at DESC LIMIT 1`, project)
	return scanSession(row)
}

func (s *Store) ListSessions(ctx context.Context, project string, limit int) ([]*Session, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project, directory, started_at, ended_at, summary
		 FROM sessions WHERE project = ?
		 ORDER BY started_at DESC LIMIT ?`, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

type sessionScanner interface {
	Scan(dest ...any) error
}

func scanSession(sc sessionScanner) (*Session, error) {
	var sess Session
	var startedAt string
	var endedAt sql.NullString

	err := sc.Scan(&sess.ID, &sess.Project, &sess.Directory, &startedAt, &endedAt, &sess.Summary)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sess.StartedAt, _ = time.Parse(time.RFC3339, startedAt)
	sess.EndedAt = nullableTime(endedAt)
	return &sess, nil
}
