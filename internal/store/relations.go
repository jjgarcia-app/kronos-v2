package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Tipos de relación (verbo cerrado).
const (
	RelationPending       = "pending"
	RelationRelated       = "related"
	RelationCompatible    = "compatible"
	RelationScoped        = "scoped"
	RelationConflictsWith = "conflicts_with"
	RelationSupersedes    = "supersedes"
	RelationNotConflict   = "not_conflict"
)

// Estado del juicio.
const (
	JudgmentPending  = "pending"
	JudgmentJudged   = "judged"
	JudgmentOrphaned = "orphaned"
	JudgmentIgnored  = "ignored"
)

var validRelationVerbs = map[string]bool{
	RelationRelated:       true,
	RelationCompatible:    true,
	RelationScoped:        true,
	RelationConflictsWith: true,
	RelationSupersedes:    true,
	RelationNotConflict:   true,
}

var ErrInvalidRelation = errors.New("relation verb inválido")
var ErrCrossProjectRelation = errors.New("relaciones entre proyectos distintos no están permitidas")

// Relation representa un vínculo entre dos observaciones.
type Relation struct {
	ID                     int64
	SyncID                 string
	SourceID               string // sync_id de la observación origen
	TargetID               string // sync_id de la observación destino
	Relation               string
	Reason                 string
	Evidence               string
	Confidence             float64
	JudgmentStatus         string
	MarkedByActor          string
	MarkedByKind           string
	MarkedByModel          string
	SessionID              string
	SupersededAt           string
	SupersededByRelationID *int64
	CreatedAt              string
	UpdatedAt              string
	DeletedAt              *string

	// anotaciones enriquecidas (no en DB)
	SourceIntID    int64
	SourceTitle    string
	SourceProject  string
	TargetIntID    int64
	TargetTitle    string
	TargetProject  string
}

// Candidate es un resultado de FindCandidates: observación potencialmente conflictiva.
type Candidate struct {
	ID           int64
	SyncID       string
	Title        string
	Type         ObservationType
	TopicKey     string
	Score        float64 // BM25 (negativo en SQLite — más cercano a 0 = mejor)
	JudgmentID   int64   // ID en memory_relations si ya se insertó
}

// CandidateOptions controla la búsqueda de candidatos.
type CandidateOptions struct {
	Project    string
	Scope      Scope
	Limit      int     // default 3
	BM25Floor  float64 // default -2.0 (filtro de calidad)
	SkipInsert bool    // si true, no inserta filas en memory_relations
}

// JudgeRelationParams parámetros para mem_judge.
type JudgeRelationParams struct {
	JudgmentID     int64
	Relation       string
	Reason         string
	Evidence       string
	Confidence     float64
	MarkedByActor  string
	MarkedByKind   string
	MarkedByModel  string
	SessionID      string
}

// FindCandidates busca observaciones potencialmente conflictivas con savedID
// usando FTS5 BM25. Inserta filas pending en memory_relations por cada candidato
// (a menos que SkipInsert=true o la relación ya exista).
// Solo disponible en SQLite (se ignora en postgres).
func (s *Store) FindCandidates(ctx context.Context, savedObs *Observation, opts CandidateOptions) ([]Candidate, error) {
	if s.driver == "postgres" {
		return nil, nil
	}
	if opts.Limit <= 0 {
		opts.Limit = 3
	}
	if opts.BM25Floor == 0 {
		opts.BM25Floor = -2.0
	}
	if opts.Project == "" {
		opts.Project = savedObs.Project
	}

	ftsQuery := sanitizeFTSCandidates(savedObs.Title)
	if ftsQuery == "" {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT o.id, o.sync_id, o.title, o.type, o.topic_key, bm25(observations_fts) as rank
		FROM observations_fts
		JOIN observations o ON observations_fts.rowid = o.id
		WHERE observations_fts MATCH ?
		  AND o.id != ?
		  AND (o.project = ? OR o.scope = 'global')
		  AND o.deleted_at IS NULL
		ORDER BY rank
		LIMIT ?`,
		ftsQuery, savedObs.ID, opts.Project, opts.Limit*3,
	)
	if err != nil {
		return nil, fmt.Errorf("find candidates: %w", err)
	}
	defer rows.Close()

	var candidates []Candidate
	for rows.Next() {
		var c Candidate
		var topicKey sql.NullString
		var score float64
		if err := rows.Scan(&c.ID, &c.SyncID, &c.Title, &c.Type, &topicKey, &score); err != nil {
			return nil, err
		}
		c.TopicKey = topicKey.String
		c.Score = score

		// filtrar por calidad BM25
		if score < opts.BM25Floor {
			continue
		}

		// verificar si ya existe relación entre estos dos
		exists, err := s.relationExists(ctx, savedObs.SyncID, c.SyncID)
		if err != nil {
			return nil, err
		}
		if exists {
			continue
		}

		if !opts.SkipInsert {
			rid, err := s.insertRelationPending(ctx, savedObs.SyncID, c.SyncID)
			if err != nil {
				// ignorar error de duplicado (UNIQUE constraint)
				continue
			}
			c.JudgmentID = rid
		}

		candidates = append(candidates, c)
		if len(candidates) >= opts.Limit {
			break
		}
	}
	return candidates, rows.Err()
}

// JudgeRelation registra el veredicto de un agente sobre una relación pendiente.
func (s *Store) JudgeRelation(ctx context.Context, p JudgeRelationParams) (*Relation, error) {
	if !validRelationVerbs[p.Relation] {
		return nil, fmt.Errorf("%w: %q — válidos: %s",
			ErrInvalidRelation, p.Relation, strings.Join(validRelationVerbList(), ", "))
	}

	rel, err := s.getRelationByID(ctx, p.JudgmentID)
	if err != nil {
		return nil, fmt.Errorf("relation %d: %w", p.JudgmentID, err)
	}
	if rel == nil {
		return nil, fmt.Errorf("relation %d no encontrada", p.JudgmentID)
	}

	// cross-project guard
	if err := s.checkCrossProject(ctx, rel.SourceID, rel.TargetID); err != nil {
		return nil, err
	}

	ts := now()
	_, err = s.db.ExecContext(ctx, `
		UPDATE memory_relations
		SET relation = ?, reason = ?, evidence = ?, confidence = ?,
		    judgment_status = 'judged',
		    marked_by_actor = ?, marked_by_kind = ?, marked_by_model = ?,
		    session_id = ?, updated_at = ?
		WHERE id = ?`,
		p.Relation, p.Reason, p.Evidence, p.Confidence,
		p.MarkedByActor, p.MarkedByKind, p.MarkedByModel,
		p.SessionID, ts, p.JudgmentID,
	)
	if err != nil {
		return nil, fmt.Errorf("judge relation: %w", err)
	}

	return s.getRelationByID(ctx, p.JudgmentID)
}

// JudgeBySemantic persiste un veredicto semántico (generado por LLM).
// Si relation == "not_conflict" → no inserta nada, retorna "".
// UPSERT: si ya existe relación en cualquier dirección, actualiza.
func (s *Store) JudgeBySemantic(ctx context.Context, sourceID, targetID, relation string, confidence float64, reason, model string) (string, error) {
	if relation == RelationNotConflict {
		return "", nil
	}
	if !validRelationVerbs[relation] {
		return "", fmt.Errorf("%w: %q", ErrInvalidRelation, relation)
	}

	ts := now()

	// buscar relación existente en cualquier dirección
	existing, err := s.findRelationBetween(ctx, sourceID, targetID)
	if err != nil {
		return "", err
	}

	if existing != nil {
		_, err = s.db.ExecContext(ctx, `
			UPDATE memory_relations
			SET relation = ?, confidence = ?, reason = ?,
			    judgment_status = 'judged',
			    marked_by_kind = 'system', marked_by_actor = 'kronos', marked_by_model = ?,
			    updated_at = ?
			WHERE id = ?`,
			relation, confidence, reason, model, ts, existing.ID,
		)
		if err != nil {
			return "", fmt.Errorf("update semantic relation: %w", err)
		}
		return existing.SyncID, nil
	}

	// insert nuevo
	syncID := newSyncID()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO memory_relations
			(sync_id, source_id, target_id, relation, confidence, reason,
			 judgment_status, marked_by_kind, marked_by_actor, marked_by_model,
			 created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'judged', 'system', 'kronos', ?, ?, ?)`,
		syncID, sourceID, targetID, relation, confidence, reason, model, ts, ts,
	)
	if err != nil {
		return "", fmt.Errorf("insert semantic relation: %w", err)
	}
	return syncID, nil
}

// ListRelations lista relaciones con filtros opcionales.
func (s *Store) ListRelations(ctx context.Context, project, status string, limit, offset int) ([]Relation, error) {
	if limit <= 0 {
		limit = 50
	}

	var args []any
	var where []string
	where = append(where, "r.deleted_at IS NULL")

	if status != "" {
		where = append(where, "r.judgment_status = ?")
		args = append(args, status)
	}
	if project != "" {
		where = append(where, "(src.project = ? OR tgt.project = ?)")
		args = append(args, project, project)
	}

	whereClause := "WHERE " + strings.Join(where, " AND ")

	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT r.id, r.sync_id, r.source_id, r.target_id, r.relation, r.judgment_status,
		       r.reason, r.confidence, r.marked_by_actor, r.marked_by_kind, r.created_at, r.updated_at,
		       COALESCE(src.id, 0), COALESCE(src.title, ''), COALESCE(src.project, ''),
		       COALESCE(tgt.id, 0), COALESCE(tgt.title, ''), COALESCE(tgt.project, '')
		FROM memory_relations r
		LEFT JOIN observations src ON src.sync_id = r.source_id
		LEFT JOIN observations tgt ON tgt.sync_id = r.target_id
		%s
		ORDER BY r.created_at DESC
		LIMIT ? OFFSET ?`, whereClause), args...)
	if err != nil {
		return nil, fmt.Errorf("list relations: %w", err)
	}
	defer rows.Close()

	var result []Relation
	for rows.Next() {
		var r Relation
		if err := rows.Scan(
			&r.ID, &r.SyncID, &r.SourceID, &r.TargetID, &r.Relation, &r.JudgmentStatus,
			&r.Reason, &r.Confidence, &r.MarkedByActor, &r.MarkedByKind, &r.CreatedAt, &r.UpdatedAt,
			&r.SourceIntID, &r.SourceTitle, &r.SourceProject,
			&r.TargetIntID, &r.TargetTitle, &r.TargetProject,
		); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// RelationStats estadísticas de conflictos por proyecto.
type RelationStats struct {
	Project         string
	Total           int
	Pending         int
	Judged          int
	Orphaned        int
	ByRelation      map[string]int
}

// GetRelationStats retorna estadísticas de relaciones para un proyecto.
func (s *Store) GetRelationStats(ctx context.Context, project string) (*RelationStats, error) {
	stats := &RelationStats{
		Project:    project,
		ByRelation: make(map[string]int),
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT r.judgment_status, r.relation, COUNT(*) as n
		FROM memory_relations r
		LEFT JOIN observations src ON src.sync_id = r.source_id
		LEFT JOIN observations tgt ON tgt.sync_id = r.target_id
		WHERE r.deleted_at IS NULL AND (src.project = ? OR tgt.project = ?)
		GROUP BY r.judgment_status, r.relation`, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var status, rel string
		var n int
		if err := rows.Scan(&status, &rel, &n); err != nil {
			return nil, err
		}
		stats.Total += n
		switch status {
		case JudgmentPending:
			stats.Pending += n
		case JudgmentJudged:
			stats.Judged += n
		case JudgmentOrphaned:
			stats.Orphaned += n
		}
		if rel != RelationPending {
			stats.ByRelation[rel] += n
		}
	}
	return stats, rows.Err()
}

// internos

func (s *Store) insertRelationPending(ctx context.Context, sourceID, targetID string) (int64, error) {
	syncID := newSyncID()
	ts := now()
	return s.insertReturning(ctx, `
		INSERT INTO memory_relations (sync_id, source_id, target_id, relation, judgment_status, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', 'pending', ?, ?)`,
		syncID, sourceID, targetID, ts, ts,
	)
}

func (s *Store) relationExists(ctx context.Context, sourceID, targetID string) (bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM memory_relations
		WHERE deleted_at IS NULL
		  AND ((source_id = ? AND target_id = ?) OR (source_id = ? AND target_id = ?))`,
		sourceID, targetID, targetID, sourceID,
	)
	var n int
	err := row.Scan(&n)
	return n > 0, err
}

func (s *Store) findRelationBetween(ctx context.Context, sourceID, targetID string) (*Relation, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, sync_id FROM memory_relations
		WHERE deleted_at IS NULL
		  AND ((source_id = ? AND target_id = ?) OR (source_id = ? AND target_id = ?))
		LIMIT 1`,
		sourceID, targetID, targetID, sourceID,
	)
	var r Relation
	err := row.Scan(&r.ID, &r.SyncID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) getRelationByID(ctx context.Context, id int64) (*Relation, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, sync_id, source_id, target_id, relation, judgment_status,
		       COALESCE(reason,''), COALESCE(evidence,''), COALESCE(confidence,0),
		       COALESCE(marked_by_actor,''), COALESCE(marked_by_kind,''), COALESCE(marked_by_model,''),
		       COALESCE(session_id,''), created_at, updated_at
		FROM memory_relations WHERE id = ? AND deleted_at IS NULL`, id)
	var r Relation
	err := row.Scan(
		&r.ID, &r.SyncID, &r.SourceID, &r.TargetID, &r.Relation, &r.JudgmentStatus,
		&r.Reason, &r.Evidence, &r.Confidence,
		&r.MarkedByActor, &r.MarkedByKind, &r.MarkedByModel,
		&r.SessionID, &r.CreatedAt, &r.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

func (s *Store) checkCrossProject(ctx context.Context, sourceID, targetID string) error {
	src, err := s.GetObservationBySyncID(ctx, sourceID)
	if err != nil || src == nil {
		return nil // si no existe no bloqueamos
	}
	tgt, err := s.GetObservationBySyncID(ctx, targetID)
	if err != nil || tgt == nil {
		return nil
	}
	if src.Project != tgt.Project {
		return fmt.Errorf("%w: %q vs %q", ErrCrossProjectRelation, src.Project, tgt.Project)
	}
	return nil
}

// GCRelations marca como eliminadas (soft-delete) las relaciones basura en dos casos:
//  1. Dangling: la observación fuente o destino fue soft-deleted (ya no existe activa).
//  2. Stale pending: llevan más de staleDays días sin ser resueltas por el LLM judge.
//
// Retorna el total de filas marcadas.
func (s *Store) GCRelations(ctx context.Context, staleDays int) (int64, error) {
	if staleDays <= 0 {
		staleDays = 30
	}
	ts := now()

	// 1. dangling: source o target ya no existe como observación activa
	res1, err := s.exec(ctx, `
		UPDATE memory_relations SET deleted_at = ?, updated_at = ?
		WHERE deleted_at IS NULL AND (
			NOT EXISTS (
				SELECT 1 FROM observations WHERE sync_id = source_id AND deleted_at IS NULL
			) OR NOT EXISTS (
				SELECT 1 FROM observations WHERE sync_id = target_id AND deleted_at IS NULL
			)
		)`, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("gc dangling relations: %w", err)
	}
	dangling, _ := res1.RowsAffected()

	// 2. stale pending: nunca fueron juzgadas después de staleDays días
	cutoff := time.Now().UTC().AddDate(0, 0, -staleDays).Format(time.RFC3339)
	res2, err := s.exec(ctx,
		`UPDATE memory_relations SET deleted_at = ?, updated_at = ?
		 WHERE deleted_at IS NULL AND judgment_status = 'pending' AND created_at < ?`,
		ts, ts, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("gc stale relations: %w", err)
	}
	stale, _ := res2.RowsAffected()

	return dangling + stale, nil
}

// sanitizeFTSCandidates convierte un título en una query FTS5 OR-based para búsqueda amplia.
// Ej: "fix auth bug" → `"fix" OR "auth" OR "bug"`
func sanitizeFTSCandidates(title string) string {
	words := strings.Fields(strings.ToLower(title))
	if len(words) == 0 {
		return ""
	}
	// filtrar stopwords cortas
	var parts []string
	for _, w := range words {
		if len(w) > 2 {
			parts = append(parts, fmt.Sprintf("%q", w))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " OR ")
}

func validRelationVerbList() []string {
	return []string{
		RelationRelated, RelationCompatible, RelationScoped,
		RelationConflictsWith, RelationSupersedes, RelationNotConflict,
	}
}
