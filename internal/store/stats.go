package store

import (
	"context"
	"database/sql"
	"time"
)

// Stats contains aggregate counts for the Kronos database.
type Stats struct {
	TotalObservations int
	TotalSessions     int
	TotalPrompts      int
	Projects          []string
}

// Stats queries aggregate statistics from the database.
func (s *Store) Stats(ctx context.Context) (*Stats, error) {
	var st Stats

	row := s.queryRow(ctx,
		`SELECT COUNT(*) FROM observations WHERE deleted_at IS NULL`)
	if err := row.Scan(&st.TotalObservations); err != nil {
		return nil, err
	}

	row = s.queryRow(ctx, `SELECT COUNT(*) FROM sessions`)
	if err := row.Scan(&st.TotalSessions); err != nil {
		return nil, err
	}

	row = s.queryRow(ctx, `SELECT COUNT(*) FROM user_prompts`)
	if err := row.Scan(&st.TotalPrompts); err != nil && err != sql.ErrNoRows {
		// user_prompts table may not exist in older DBs
		st.TotalPrompts = 0
	}

	rows, err := s.query(ctx,
		`SELECT DISTINCT project FROM observations WHERE deleted_at IS NULL ORDER BY project ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		if p != "" {
			st.Projects = append(st.Projects, p)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &st, nil
}

// GetObservationSync is a convenience wrapper that creates its own context.
func (s *Store) GetObservationSync(id int64) (*Observation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.GetObservation(ctx, id)
}

// AllSessions returns all sessions ordered by start time descending.
func (s *Store) AllSessions(ctx context.Context, limit int) ([]*Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.query(ctx,
		`SELECT id, project, directory, started_at, ended_at, summary
		 FROM sessions ORDER BY started_at DESC LIMIT ?`, limit)
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

// TimelineObservations returns up to n observations before and after the given
// observation ID within the same session.
func (s *Store) TimelineObservations(ctx context.Context, obsID int64, n int) ([]*Observation, error) {
	if n <= 0 {
		n = 5
	}
	// first get the session_id and created_at of the target observation
	var sessionID string
	var createdAt string
	row := s.queryRow(ctx,
		`SELECT COALESCE(session_id,''), created_at FROM observations WHERE id = ?`, obsID)
	if err := row.Scan(&sessionID, &createdAt); err != nil {
		return nil, err
	}

	if sessionID == "" {
		return nil, nil
	}

	rows, err := s.query(ctx,
		`SELECT id, session_id, type, title, content, project, scope, topic_key,
		        normalized_hash, revision_count, duplicate_count, created_at, updated_at, deleted_at
		 FROM observations
		 WHERE session_id = ? AND deleted_at IS NULL
		 ORDER BY created_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all, err := scanObservations(rows)
	if err != nil {
		return nil, err
	}

	// find target index
	targetIdx := -1
	for i, o := range all {
		if o.ID == obsID {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return all, nil
	}

	start := targetIdx - n
	if start < 0 {
		start = 0
	}
	end := targetIdx + n + 1
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], nil
}
