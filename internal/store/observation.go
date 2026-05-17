package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
			return s.updateObservation(ctx, existing.ID, p.Title, p.Content, string(p.Type), p.ToolName, hash, ts)
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
	syncID := p.SyncID
	if syncID == "" {
		syncID = newSyncID()
	}

	if s.driver == "postgres" {
		var id int64
		err = s.db.QueryRowContext(ctx,
			s.rebind(`INSERT INTO observations
				(sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
				 normalized_hash, revision_count, duplicate_count, last_seen_at, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?, ?)
			 ON CONFLICT (sync_id) DO UPDATE SET updated_at = observations.updated_at
			 RETURNING id`),
			syncID, nullStr(p.SessionID), string(p.Type), p.Title, p.Content,
			p.ToolName, p.Project, string(p.Scope), p.TopicKey, hash, ts, ts, ts,
		).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("insert observation: %w", err)
		}
		return s.GetObservation(ctx, id)
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO observations
			(sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
			 normalized_hash, revision_count, duplicate_count, last_seen_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?, ?)`,
		syncID, nullStr(p.SessionID), string(p.Type), p.Title, p.Content,
		p.ToolName, p.Project, string(p.Scope), p.TopicKey, hash, ts, ts, ts,
	)
	if err != nil {
		return nil, fmt.Errorf("insert observation: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("insert observation lastid: %w", err)
	}
	if id == 0 {
		return s.GetObservationBySyncID(ctx, syncID)
	}
	return s.GetObservation(ctx, id)
}

func (s *Store) GetObservation(ctx context.Context, id int64) (*Observation, error) {
	row := s.queryRow(ctx,
		`SELECT id, sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
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
	toolName := existing.ToolName

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
	return s.updateObservation(ctx, p.ID, title, content, typ, toolName, hash, now())
}

func (s *Store) DeleteObservation(ctx context.Context, id int64) error {
	_, err := s.exec(ctx,
		`UPDATE observations SET deleted_at = ? WHERE id = ?`, now(), id)
	return err
}

func (s *Store) ListObservations(ctx context.Context, project string, limit int) ([]*Observation, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.query(ctx,
		`SELECT id, sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
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

// ListAll retorna todas las observaciones no borradas, opcionalmente filtradas por proyecto.
func (s *Store) ListAll(ctx context.Context, project string) ([]*Observation, error) {
	var rows *sql.Rows
	var err error
	if project == "" {
		rows, err = s.query(ctx,
			`SELECT id, sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
			        normalized_hash, revision_count, duplicate_count, created_at, updated_at, deleted_at
			 FROM observations
			 WHERE deleted_at IS NULL
			 ORDER BY project ASC, created_at ASC`)
	} else {
		rows, err = s.query(ctx,
			`SELECT id, sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
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
	rows, err := s.query(ctx,
		`SELECT id, sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
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

// GetObservationBySyncID busca una observación por su sync_id global.
func (s *Store) GetObservationBySyncID(ctx context.Context, syncID string) (*Observation, error) {
	row := s.queryRow(ctx,
		`SELECT id, sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
		        normalized_hash, revision_count, duplicate_count, created_at, updated_at, deleted_at
		 FROM observations WHERE sync_id = ?`, syncID)
	return scanObservation(row)
}

// SavePassive guarda learnings extraídos del output de sub-agentes.
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

// GCStale soft-deletes observations that have not been updated or re-seen
// within retentionDays. Only removes observations with revision_count=1 and
// duplicate_count=1 (never revised, never re-encountered).
// Returns the number of observations soft-deleted.
func (s *Store) GCStale(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		retentionDays = 90
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339)
	res, err := s.exec(ctx,
		`UPDATE observations SET deleted_at = ?
		 WHERE deleted_at IS NULL
		   AND revision_count = 1
		   AND duplicate_count = 1
		   AND last_seen_at < ?`,
		now(), cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("gc stale: %w", err)
	}
	affected, _ := res.RowsAffected()
	return affected, nil
}

// RenameProject mueve todas las observaciones de from a to. Retorna el número de filas afectadas.
func (s *Store) RenameProject(ctx context.Context, from, to string) (int64, error) {
	res, err := s.exec(ctx,
		`UPDATE observations SET project = ? WHERE project = ? AND deleted_at IS NULL`, to, from)
	if err != nil {
		return 0, fmt.Errorf("rename project: %w", err)
	}
	affected, _ := res.RowsAffected()
	return affected, nil
}

// CountObservations retorna el total de observaciones no borradas.
func (s *Store) CountObservations(ctx context.Context, project string) int {
	var row *sql.Row
	if project != "" {
		row = s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM observations WHERE project = ? AND deleted_at IS NULL`, project)
	} else {
		row = s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM observations WHERE deleted_at IS NULL`)
	}
	var n int
	_ = row.Scan(&n)
	return n
}

// internos

func (s *Store) getByTopicKey(ctx context.Context, project, topicKey string) (*Observation, error) {
	row := s.queryRow(ctx,
		`SELECT id, sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
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
	row := s.queryRow(ctx,
		`SELECT id, sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
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

func (s *Store) updateObservation(ctx context.Context, id int64, title, content, typ, toolName, hash, ts string) (*Observation, error) {
	_, err := s.exec(ctx,
		`UPDATE observations
		 SET title = ?, content = ?, type = ?, tool_name = ?, normalized_hash = ?,
		     revision_count = revision_count + 1, last_seen_at = ?, updated_at = ?
		 WHERE id = ?`,
		title, content, typ, toolName, hash, ts, ts, id,
	)
	if err != nil {
		return nil, fmt.Errorf("update observation: %w", err)
	}
	return s.GetObservation(ctx, id)
}

func (s *Store) bumpDuplicate(ctx context.Context, id int64, ts string) (*Observation, error) {
	_, err := s.exec(ctx,
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

func newSyncID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

type observationScanner interface {
	Scan(dest ...any) error
}

func scanObservation(row observationScanner) (*Observation, error) {
	var o Observation
	var syncID, sessionID, toolName, deletedAt sql.NullString
	var topicKey, hash, createdAt, updatedAt string

	err := row.Scan(
		&o.ID, &syncID, &sessionID, &o.Type, &o.Title, &o.Content,
		&toolName, &o.Project, &o.Scope, &topicKey, &hash,
		&o.RevisionCount, &o.DuplicateCount,
		&createdAt, &updatedAt, &deletedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o.SyncID = syncID.String
	o.SessionID = sessionID.String
	o.ToolName = toolName.String
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

// TouchLastSeen actualiza last_seen_at para las observaciones dadas.
// Se llama cuando el agente encuentra observaciones via búsqueda (no solo al guardarlas),
// para que GCStale no elimine observaciones activamente usadas.
func (s *Store) TouchLastSeen(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids)+1)
	args[0] = now()
	for i, id := range ids {
		placeholders[i] = "?"
		args[i+1] = id
	}
	_, err := s.exec(ctx,
		`UPDATE observations SET last_seen_at = ? WHERE id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	return err
}

// ListRecent retorna las N observaciones no borradas más recientemente actualizadas,
// de todos los proyectos. Usado para re-indexación de embeddings al iniciar el servidor.
func (s *Store) ListRecent(ctx context.Context, limit int) ([]*Observation, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.query(ctx,
		`SELECT id, sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
		        normalized_hash, revision_count, duplicate_count, created_at, updated_at, deleted_at
		 FROM observations
		 WHERE deleted_at IS NULL
		 ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanObservations(rows)
}
