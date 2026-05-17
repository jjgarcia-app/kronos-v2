package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// SaveObservation persiste una observación con dedup y upsert por topic_key.
//
// Lógica de resolución:
//  1. topic_key no vacío + existe en el mismo proyecto → UPDATE (upsert), incrementa revision_count
//  2. normalized_hash existe → incrementa duplicate_count, retorna el existente
//  3. En otro caso → INSERT nuevo
func (s *Store) SaveObservation(ctx context.Context, p SaveParams) (*Observation, error) {
	if p.Title == "" || p.Content == "" {
		return nil, fmt.Errorf("title and content are required")
	}
	if p.Project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if p.Scope == "" {
		p.Scope = ScopeProject
	}

	hash := normalizedHash(p.Title, p.Content)
	ts := now()

	// caso 1: upsert por topic_key
	if p.TopicKey != "" {
		existing, err := s.getByTopicKey(ctx, p.Project, p.TopicKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return s.updateObservation(ctx, existing.ID, p.Title, p.Content, string(p.Type), hash, ts)
		}
	}

	// caso 2: dedup por hash
	existing, err := s.getByHash(ctx, hash, p.Project)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return s.bumpDuplicate(ctx, existing.ID, ts)
	}

	// caso 3: insert nuevo
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO observations
			(session_id, type, title, content, project, scope, topic_key, normalized_hash,
			 revision_count, duplicate_count, last_seen_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?, ?)`,
		nullStr(p.SessionID), string(p.Type), p.Title, p.Content,
		p.Project, string(p.Scope), p.TopicKey, hash, ts, ts, ts,
	)
	if err != nil {
		return nil, fmt.Errorf("insert observation: %w", err)
	}
	id, _ := res.LastInsertId()
	return s.GetObservation(ctx, id)
}

func (s *Store) GetObservation(ctx context.Context, id int64) (*Observation, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, session_id, type, title, content, project, scope, topic_key,
		        normalized_hash, revision_count, duplicate_count, created_at, updated_at, deleted_at
		 FROM observations WHERE id = ?`, id)
	return scanObservation(row)
}

func (s *Store) UpdateObservation(ctx context.Context, p UpdateParams) (*Observation, error) {
	existing, err := s.GetObservation(ctx, p.ID)
	if err != nil || existing == nil {
		return nil, fmt.Errorf("observation %d not found", p.ID)
	}

	title := existing.Title
	content := existing.Content
	typ := string(existing.Type)

	if p.Title != nil {
		title = *p.Title
	}
	if p.Content != nil {
		content = *p.Content
	}
	if p.Type != nil {
		typ = string(*p.Type)
	}

	hash := normalizedHash(title, content)
	return s.updateObservation(ctx, p.ID, title, content, typ, hash, now())
}

func (s *Store) DeleteObservation(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE observations SET deleted_at = ? WHERE id = ?`, now(), id)
	return err
}

func (s *Store) ListObservations(ctx context.Context, project string, limit int) ([]*Observation, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, type, title, content, project, scope, topic_key,
		        normalized_hash, revision_count, duplicate_count, created_at, updated_at, deleted_at
		 FROM observations
		 WHERE (project = ? OR scope = 'global') AND deleted_at IS NULL
		 ORDER BY created_at DESC LIMIT ?`, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanObservations(rows)
}

// ListAll returns every non-deleted observation, optionally filtered by project.
// Pass project="" to include all projects. Results are ordered by project, then created_at.
func (s *Store) ListAll(ctx context.Context, project string) ([]*Observation, error) {
	var rows *sql.Rows
	var err error
	if project == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, session_id, type, title, content, project, scope, topic_key,
			        normalized_hash, revision_count, duplicate_count, created_at, updated_at, deleted_at
			 FROM observations
			 WHERE deleted_at IS NULL
			 ORDER BY project ASC, created_at ASC`)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, session_id, type, title, content, project, scope, topic_key,
			        normalized_hash, revision_count, duplicate_count, created_at, updated_at, deleted_at
			 FROM observations
			 WHERE (project = ? OR scope = 'global') AND deleted_at IS NULL
			 ORDER BY created_at ASC`, project)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanObservations(rows)
}

func (s *Store) ListSessionObservations(ctx context.Context, sessionID string) ([]*Observation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, type, title, content, project, scope, topic_key,
		        normalized_hash, revision_count, duplicate_count, created_at, updated_at, deleted_at
		 FROM observations
		 WHERE session_id = ? AND deleted_at IS NULL
		 ORDER BY created_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanObservations(rows)
}

// SavePassive guarda learnings extraídos del output de sub-agentes.
// Usa dedup por hash para evitar duplicados entre sesiones.
func (s *Store) SavePassive(ctx context.Context, sessionID, project, content string) (*Observation, error) {
	title := passiveTitle(content)
	return s.SaveObservation(ctx, SaveParams{
		SessionID: sessionID,
		Type:      TypePassive,
		Title:     title,
		Content:   content,
		Project:   project,
		Scope:     ScopeProject,
	})
}

// internos

func (s *Store) getByTopicKey(ctx context.Context, project, topicKey string) (*Observation, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, session_id, type, title, content, project, scope, topic_key,
		        normalized_hash, revision_count, duplicate_count, created_at, updated_at, deleted_at
		 FROM observations
		 WHERE project = ? AND topic_key = ? AND deleted_at IS NULL
		 ORDER BY created_at DESC LIMIT 1`, project, topicKey)
	obs, err := scanObservation(row)
	if err != nil || obs == nil {
		return nil, err
	}
	return obs, nil
}

func (s *Store) getByHash(ctx context.Context, hash, project string) (*Observation, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, session_id, type, title, content, project, scope, topic_key,
		        normalized_hash, revision_count, duplicate_count, created_at, updated_at, deleted_at
		 FROM observations
		 WHERE normalized_hash = ? AND project = ? AND deleted_at IS NULL
		 LIMIT 1`, hash, project)
	obs, err := scanObservation(row)
	if err != nil || obs == nil {
		return nil, err
	}
	return obs, nil
}

func (s *Store) updateObservation(ctx context.Context, id int64, title, content, typ, hash, ts string) (*Observation, error) {
	_, err := s.db.ExecContext(ctx,
		`UPDATE observations
		 SET title = ?, content = ?, type = ?, normalized_hash = ?,
		     revision_count = revision_count + 1, last_seen_at = ?, updated_at = ?
		 WHERE id = ?`,
		title, content, typ, hash, ts, ts, id,
	)
	if err != nil {
		return nil, fmt.Errorf("update observation: %w", err)
	}
	return s.GetObservation(ctx, id)
}

func (s *Store) bumpDuplicate(ctx context.Context, id int64, ts string) (*Observation, error) {
	_, err := s.db.ExecContext(ctx,
		`UPDATE observations
		 SET duplicate_count = duplicate_count + 1, last_seen_at = ?
		 WHERE id = ?`, ts, id)
	if err != nil {
		return nil, err
	}
	return s.GetObservation(ctx, id)
}

func normalizedHash(title, content string) string {
	key := strings.ToLower(strings.TrimSpace(title) + "|" + strings.TrimSpace(content))
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", sum)
}

func passiveTitle(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) == 0 {
		return "passive capture"
	}
	title := strings.TrimLeft(lines[0], "0123456789.-) \t")
	if len(title) > 80 {
		title = title[:77] + "..."
	}
	return title
}

func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

type observationScanner interface {
	Scan(dest ...any) error
}

func scanObservation(row observationScanner) (*Observation, error) {
	var o Observation
	var sessionID, deletedAt sql.NullString
	var topicKey, hash, createdAt, updatedAt string

	err := row.Scan(
		&o.ID, &sessionID, &o.Type, &o.Title, &o.Content,
		&o.Project, &o.Scope, &topicKey, &hash,
		&o.RevisionCount, &o.DuplicateCount,
		&createdAt, &updatedAt, &deletedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o.SessionID = sessionID.String
	o.TopicKey = topicKey
	o.NormalizedHash = hash
	o.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	o.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	o.DeletedAt = nullableTime(deletedAt)
	return &o, nil
}

func scanObservations(rows *sql.Rows) ([]*Observation, error) {
	var result []*Observation
	for rows.Next() {
		o, err := scanObservation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, o)
	}
	return result, rows.Err()
}
