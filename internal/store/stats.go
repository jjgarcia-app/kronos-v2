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
	// TopOrganicProject y TopOrganicShare miden concentración EXCLUYENDO
	// importaciones masivas de una sola vez (>5 observaciones del mismo
	// proyecto creadas en el mismo minuto — un import real deja un pico
	// instantáneo, un flujo de trabajo orgánico no). Medido en una base real:
	// un proyecto con 67% de concentración bruta (778/1152) caía a 44%
	// (150/343) al excluir un import de 628 filas creadas en un solo minuto
	// — la concentración orgánica es la que importa para el equilibrio del
	// corpus (F7 en el modelo de auditoría de memoria de agentes), no el
	// volumen bruto, que puede ser legítimo.
	TopOrganicProject string
	TopOrganicShare   float64 // 0 si no hay observaciones orgánicas
	OrganicTotal      int
}

// Stats queries aggregate statistics from the database.
func (s *Store) Stats(ctx context.Context) (*Stats, error) {
	var st Stats

	row := s.queryRow(ctx,
		`SELECT COUNT(*) FROM observations WHERE deleted_at IS NULL`)
	if err := row.Scan(&st.TotalObservations); err != nil {
		return nil, err
	}

	row = s.queryRow(ctx, `SELECT COUNT(*) FROM sessions WHERE deleted_at IS NULL`)
	if err := row.Scan(&st.TotalSessions); err != nil {
		return nil, err
	}

	row = s.queryRow(ctx, `SELECT COUNT(*) FROM user_prompts WHERE deleted_at IS NULL`)
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

	if err := s.fillOrganicConcentration(ctx, &st); err != nil {
		return nil, err
	}

	return &st, nil
}

// bulkImportMinuteThreshold: más de esta cantidad de observaciones del MISMO
// proyecto creadas en el MISMO minuto (created_at truncado a 'YYYY-MM-DDTHH:MM')
// se trata como un import masivo de una sola vez, no como trabajo orgánico —
// ver el comentario de Stats.TopOrganicShare para la medición que justifica
// el criterio y el umbral.
const bulkImportMinuteThreshold = 5

// fillOrganicConcentration calcula TopOrganicProject/TopOrganicShare/
// OrganicTotal sobre Stats: dos consultas (una para encontrar los minutos con
// >bulkImportMinuteThreshold observaciones de un proyecto, otra para contar el
// resto por proyecto) — portable entre SQLite y Postgres porque created_at es
// TEXT en ambos backends y substr() funciona igual en los dos.
func (s *Store) fillOrganicConcentration(ctx context.Context, st *Stats) error {
	bulkRows, err := s.query(ctx, `
		SELECT project, substr(created_at, 1, 16) AS minuto
		FROM observations
		WHERE deleted_at IS NULL
		GROUP BY project, substr(created_at, 1, 16)
		HAVING COUNT(*) > ?`, bulkImportMinuteThreshold)
	if err != nil {
		return err
	}
	type bulkKey struct{ project, minute string }
	bulk := make(map[bulkKey]bool)
	for bulkRows.Next() {
		var k bulkKey
		if err := bulkRows.Scan(&k.project, &k.minute); err != nil {
			bulkRows.Close()
			return err
		}
		bulk[k] = true
	}
	if err := bulkRows.Err(); err != nil {
		bulkRows.Close()
		return err
	}
	bulkRows.Close()

	rows, err := s.query(ctx, `
		SELECT project, substr(created_at, 1, 16) AS minuto, COUNT(*)
		FROM observations
		WHERE deleted_at IS NULL AND project != ''
		GROUP BY project, substr(created_at, 1, 16)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	perProject := make(map[string]int)
	total := 0
	for rows.Next() {
		var project, minute string
		var n int
		if err := rows.Scan(&project, &minute, &n); err != nil {
			return err
		}
		if bulk[bulkKey{project, minute}] {
			continue
		}
		perProject[project] += n
		total += n
	}
	if err := rows.Err(); err != nil {
		return err
	}

	st.OrganicTotal = total
	if total == 0 {
		return nil
	}
	var topProject string
	var topN int
	for p, n := range perProject {
		if n > topN {
			topProject, topN = p, n
		}
	}
	st.TopOrganicProject = topProject
	st.TopOrganicShare = float64(topN) / float64(total)
	return nil
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
		`SELECT id, project, directory, started_at, ended_at, summary, injected_observation_ids, search_count, last_activity_at
		 FROM sessions WHERE deleted_at IS NULL ORDER BY started_at DESC LIMIT ?`, limit)
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
		`SELECT id, sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
		        normalized_hash, revision_count, duplicate_count, created_at, updated_at, deleted_at
		 FROM observations
		 WHERE session_id = ? AND deleted_at IS NULL
		 ORDER BY created_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all, err := s.scanObservations(rows)
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
