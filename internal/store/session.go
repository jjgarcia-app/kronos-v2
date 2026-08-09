package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	kproject "github.com/jjgarcia-app/kronos-v2/internal/project"
)

func (s *Store) CreateSession(ctx context.Context, id, project, directory string) (*Session, error) {
	project = kproject.Normalize(project)
	startedAt := now()
	_, err := s.exec(ctx,
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
	// summary solo se sobreescribe si viene no-vacío — un EndSession
	// posterior con summary="" (ej. mem_session_end llamado después de
	// mem_session_summary por error) no debe borrar un resumen ya guardado.
	res, err := s.exec(ctx,
		`UPDATE sessions SET ended_at = ?, summary = CASE WHEN ? = '' THEN summary ELSE ? END WHERE id = ?`,
		now(), summary, summary, id,
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
	row := s.queryRow(ctx,
		`SELECT id, project, directory, started_at, ended_at, summary, injected_observation_ids, search_count FROM sessions
		 WHERE id = ? AND deleted_at IS NULL`, id)
	return scanSession(row)
}

func (s *Store) GetActiveSession(ctx context.Context, project string) (*Session, error) {
	row := s.queryRow(ctx,
		`SELECT id, project, directory, started_at, ended_at, summary, injected_observation_ids, search_count
		 FROM sessions
		 WHERE project = ? AND ended_at IS NULL AND deleted_at IS NULL
		 ORDER BY started_at DESC LIMIT 1`, project)
	return scanSession(row)
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.exec(ctx,
		`UPDATE sessions SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, now(), id)
	return err
}

func (s *Store) ListSessions(ctx context.Context, project string, limit int) ([]*Session, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.query(ctx,
		`SELECT id, project, directory, started_at, ended_at, summary, injected_observation_ids, search_count
		 FROM sessions WHERE project = ? AND deleted_at IS NULL
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

// PersistInjectedIDs stores the given observation IDs as a JSON array on the session row.
// Pass nil or empty slice to persist an empty set (used as dedup baseline).
func (s *Store) PersistInjectedIDs(ctx context.Context, sessionID string, ids []string) error {
	if ids == nil {
		ids = []string{}
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("marshal injected ids: %w", err)
	}
	_, err = s.exec(ctx,
		`UPDATE sessions SET injected_observation_ids = ? WHERE id = ?`,
		string(data), sessionID,
	)
	return err
}

// LoadInjectedIDs reads the injected observation IDs for the given session.
// Returns an empty slice (not nil) if the column is NULL or empty.
func (s *Store) LoadInjectedIDs(ctx context.Context, sessionID string) ([]string, error) {
	row := s.queryRow(ctx,
		`SELECT injected_observation_ids FROM sessions WHERE id = ?`, sessionID)
	var raw sql.NullString
	if err := row.Scan(&raw); err == sql.ErrNoRows {
		return []string{}, nil
	} else if err != nil {
		return nil, err
	}
	if !raw.Valid || raw.String == "" {
		return []string{}, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw.String), &ids); err != nil {
		return []string{}, nil
	}
	return ids, nil
}

type sessionScanner interface {
	Scan(dest ...any) error
}

func scanSession(sc sessionScanner) (*Session, error) {
	var sess Session
	var startedAt string
	var endedAt sql.NullString
	var injectedIDs sql.NullString

	err := sc.Scan(&sess.ID, &sess.Project, &sess.Directory, &startedAt, &endedAt, &sess.Summary, &injectedIDs, &sess.SearchCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sess.StartedAt, _ = time.Parse(time.RFC3339, startedAt)
	sess.EndedAt = nullableTime(endedAt)
	if injectedIDs.Valid && injectedIDs.String != "" {
		_ = json.Unmarshal([]byte(injectedIDs.String), &sess.InjectedObservationIDs)
	}
	return &sess, nil
}

// IncrementSearchCount atomically increments the search_count for a session.
// Silently succeeds (no error) if the session does not exist — fail-open contract.
func (s *Store) IncrementSearchCount(ctx context.Context, sessionID string) error {
	_, err := s.incrementSearchCountAffected(ctx, sessionID)
	return err
}

// incrementSearchCountAffected is like IncrementSearchCount but also reports
// rows affected — used internally by DualStore to detect "primary is up but
// doesn't have this session row" (err == nil, 0 rows) and fall back to the
// buffer instead of silently treating it as done. Not exported: the public
// contract (fail-open, no error either way) stays unchanged for direct
// single-backend callers.
func (s *Store) incrementSearchCountAffected(ctx context.Context, sessionID string) (int64, error) {
	res, err := s.exec(ctx,
		`UPDATE sessions SET search_count = search_count + 1 WHERE id = ?`,
		sessionID,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
