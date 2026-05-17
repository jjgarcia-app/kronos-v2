// Package sync implementa exportación/importación de memoria mediante
// chunks inmutables + manifest append-only, compatible con sync git.
//
// Cada Export genera un archivo chunks/{id}.jsonl.gz y agrega una entrada
// al manifest.json. El manifest es append-only y puede mergearse en git
// sin conflictos cuando distintos usuarios exportan en paralelo.
//
// Import lee el manifest y aplica cada chunk no visto usando INSERT OR IGNORE,
// por lo que es idempotente.
package sync

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// SyncResult es el resultado de una operación Export.
type SyncResult struct {
	ChunkID  string
	Sessions int
	Memories int
	Prompts  int
	IsEmpty  bool // no hay datos nuevos para exportar
}

// ImportResult es el resultado de una operación Import.
type ImportResult struct {
	Chunks   int
	Sessions int
	Memories int
	Prompts  int
	Skipped  int // chunks ya importados anteriormente
}

// Syncer coordina exportación e importación de memoria.
type Syncer struct {
	store   *store.Store
	syncDir string // directorio raíz del proyecto; los archivos viven en syncDir/.kronos/
}

// New crea un Syncer que usa el store dado y el directorio raíz indicado.
func New(st *store.Store, syncDir string) *Syncer {
	return &Syncer{store: st, syncDir: syncDir}
}

// Export consulta datos nuevos desde el último chunk exportado y genera un
// nuevo chunk + entrada en manifest. Si no hay datos nuevos retorna IsEmpty=true.
func (s *Syncer) Export(createdBy, project string) (*SyncResult, error) {
	ctx := context.Background()

	manifest, err := loadManifest(s.syncDir)
	if err != nil {
		return nil, fmt.Errorf("cargar manifest: %w", err)
	}

	// sinceTime: si hay chunks previos, usar el created_at del último
	sinceTime := time.Unix(0, 0).UTC().Format(time.RFC3339)
	if len(manifest.Chunks) > 0 {
		sinceTime = manifest.Chunks[len(manifest.Chunks)-1].CreatedAt
	}

	cd, err := s.collectData(ctx, project, sinceTime)
	if err != nil {
		return nil, err
	}

	if len(cd.Sessions) == 0 && len(cd.Observations) == 0 && len(cd.Prompts) == 0 {
		return &SyncResult{IsEmpty: true}, nil
	}

	compressed, chunkID, err := marshalChunk(cd)
	if err != nil {
		return nil, err
	}

	// si el chunkID ya existe en el manifest, no hay datos nuevos efectivos
	if manifest.hasChunk(chunkID) {
		return &SyncResult{IsEmpty: true}, nil
	}

	if err := writeChunk(s.syncDir, chunkID, compressed); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	entry := ChunkEntry{
		ID:        chunkID,
		CreatedBy: createdBy,
		CreatedAt: now,
		Sessions:  len(cd.Sessions),
		Memories:  len(cd.Observations),
		Prompts:   len(cd.Prompts),
	}
	manifest.Chunks = append(manifest.Chunks, entry)

	if err := saveManifest(s.syncDir, manifest); err != nil {
		return nil, err
	}

	// registrar en sync_chunks para no reimportar este chunk
	if err := s.markChunk(chunkID, now); err != nil {
		return nil, err
	}

	return &SyncResult{
		ChunkID:  chunkID,
		Sessions: len(cd.Sessions),
		Memories: len(cd.Observations),
		Prompts:  len(cd.Prompts),
	}, nil
}

// Import lee el manifest y aplica todos los chunks no vistos.
func (s *Syncer) Import() (*ImportResult, error) {
	manifest, err := loadManifest(s.syncDir)
	if err != nil {
		return nil, fmt.Errorf("cargar manifest: %w", err)
	}
	if len(manifest.Chunks) == 0 {
		return &ImportResult{}, nil
	}

	// compactar manifest si crece demasiado (solo los últimos 500 chunks)
	const maxChunks = 500
	if len(manifest.Chunks) > maxChunks {
		manifest.Chunks = manifest.Chunks[len(manifest.Chunks)-maxChunks:]
		_ = saveManifest(s.syncDir, manifest)
	}

	result := &ImportResult{}

	for _, entry := range manifest.Chunks {
		imported, err := s.chunkImported(entry.ID)
		if err != nil {
			return nil, err
		}
		if imported {
			result.Skipped++
			continue
		}

		cd, err := readChunk(s.syncDir, entry.ID)
		if err != nil {
			return nil, err
		}

		counts, err := s.applyChunk(cd)
		if err != nil {
			return nil, fmt.Errorf("aplicar chunk %s: %w", entry.ID, err)
		}

		now := time.Now().UTC().Format(time.RFC3339)
		if err := s.markChunk(entry.ID, now); err != nil {
			return nil, err
		}

		result.Chunks++
		result.Sessions += counts[0]
		result.Memories += counts[1]
		result.Prompts += counts[2]
	}

	return result, nil
}

// collectData consulta sessions, observations y prompts creados después de sinceTime.
func (s *Syncer) collectData(ctx context.Context, project, sinceTime string) (*ChunkData, error) {
	db := s.store.DB()

	cd := &ChunkData{}

	// sessions
	var sessionQuery string
	var sessionArgs []any
	if project != "" {
		sessionQuery = `SELECT id, project, directory, started_at, ended_at, summary
		                FROM sessions WHERE started_at > ? AND project = ? AND deleted_at IS NULL ORDER BY started_at ASC`
		sessionArgs = []any{sinceTime, project}
	} else {
		sessionQuery = `SELECT id, project, directory, started_at, ended_at, summary
		                FROM sessions WHERE started_at > ? AND deleted_at IS NULL ORDER BY started_at ASC`
		sessionArgs = []any{sinceTime}
	}

	rows, err := db.QueryContext(ctx, sessionQuery, sessionArgs...)
	if err != nil {
		return nil, fmt.Errorf("consultar sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, proj, dir, startedAt string
		var endedAt, summary *string
		if err := rows.Scan(&id, &proj, &dir, &startedAt, &endedAt, &summary); err != nil {
			return nil, err
		}
		m := map[string]any{
			"id":         id,
			"project":    proj,
			"directory":  dir,
			"started_at": startedAt,
		}
		if endedAt != nil {
			m["ended_at"] = *endedAt
		}
		if summary != nil {
			m["summary"] = *summary
		}
		cd.Sessions = append(cd.Sessions, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// observations
	var obsQuery string
	var obsArgs []any
	if project != "" {
		obsQuery = `SELECT id, sync_id, session_id, type, title, content, project, scope, topic_key,
		                    normalized_hash, revision_count, duplicate_count,
		                    last_seen_at, created_at, updated_at
		             FROM observations
		             WHERE deleted_at IS NULL AND created_at > ? AND project = ?
		             ORDER BY created_at ASC`
		obsArgs = []any{sinceTime, project}
	} else {
		obsQuery = `SELECT id, sync_id, session_id, type, title, content, project, scope, topic_key,
		                    normalized_hash, revision_count, duplicate_count,
		                    last_seen_at, created_at, updated_at
		             FROM observations
		             WHERE deleted_at IS NULL AND created_at > ?
		             ORDER BY created_at ASC`
		obsArgs = []any{sinceTime}
	}

	obsRows, err := db.QueryContext(ctx, obsQuery, obsArgs...)
	if err != nil {
		return nil, fmt.Errorf("consultar observations: %w", err)
	}
	defer obsRows.Close()
	for obsRows.Next() {
		var id int64
		var syncID sql.NullString
		var sessionID *string
		var typ, title, content, proj, scope, topicKey, hash string
		var revCount, dupCount int
		var lastSeen, createdAt, updatedAt string
		if err := obsRows.Scan(&id, &syncID, &sessionID, &typ, &title, &content, &proj, &scope,
			&topicKey, &hash, &revCount, &dupCount, &lastSeen, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		m := map[string]any{
			"id":              id,
			"sync_id":         syncID.String,
			"type":            typ,
			"title":           title,
			"content":         content,
			"project":         proj,
			"scope":           scope,
			"topic_key":       topicKey,
			"normalized_hash": hash,
			"revision_count":  revCount,
			"duplicate_count": dupCount,
			"last_seen_at":    lastSeen,
			"created_at":      createdAt,
			"updated_at":      updatedAt,
		}
		if sessionID != nil {
			m["session_id"] = *sessionID
		}
		cd.Observations = append(cd.Observations, m)
	}
	if err := obsRows.Err(); err != nil {
		return nil, err
	}

	// prompts
	var promptQuery string
	var promptArgs []any
	if project != "" {
		promptQuery = `SELECT id, session_id, content, project, created_at
		               FROM user_prompts WHERE created_at > ? AND project = ? AND deleted_at IS NULL ORDER BY created_at ASC`
		promptArgs = []any{sinceTime, project}
	} else {
		promptQuery = `SELECT id, session_id, content, project, created_at
		               FROM user_prompts WHERE created_at > ? AND deleted_at IS NULL ORDER BY created_at ASC`
		promptArgs = []any{sinceTime}
	}

	pRows, err := db.QueryContext(ctx, promptQuery, promptArgs...)
	if err != nil {
		return nil, fmt.Errorf("consultar prompts: %w", err)
	}
	defer pRows.Close()
	for pRows.Next() {
		var id int64
		var sessionID *string
		var content, proj, createdAt string
		if err := pRows.Scan(&id, &sessionID, &content, &proj, &createdAt); err != nil {
			return nil, err
		}
		m := map[string]any{
			"id":         id,
			"content":    content,
			"project":    proj,
			"created_at": createdAt,
		}
		if sessionID != nil {
			m["session_id"] = *sessionID
		}
		cd.Prompts = append(cd.Prompts, m)
	}
	if err := pRows.Err(); err != nil {
		return nil, err
	}

	return cd, nil
}

// applyChunk inserta los datos del chunk en el store local usando INSERT OR IGNORE.
// Retorna [sessions, observations, prompts] insertados.
func (s *Syncer) applyChunk(cd *ChunkData) ([3]int, error) {
	ctx := context.Background()
	db := s.store.DB()
	var counts [3]int

	for _, sess := range cd.Sessions {
		id, _ := sess["id"].(string)
		proj, _ := sess["project"].(string)
		dir, _ := sess["directory"].(string)
		startedAt, _ := sess["started_at"].(string)
		endedAt, _ := sess["ended_at"].(string)
		summary, _ := sess["summary"].(string)
		if id == "" || proj == "" {
			continue
		}
		res, err := db.ExecContext(ctx,
			`INSERT OR IGNORE INTO sessions(id, project, directory, started_at, ended_at, summary)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			id, proj, dir, startedAt, nullableStr(endedAt), summary,
		)
		if err != nil {
			return counts, fmt.Errorf("insertar session %s: %w", id, err)
		}
		n, _ := res.RowsAffected()
		counts[0] += int(n)
	}

	for _, obs := range cd.Observations {
		sessionID, _ := obs["session_id"].(string)
		syncID, _ := obs["sync_id"].(string)
		typ, _ := obs["type"].(string)
		title, _ := obs["title"].(string)
		content, _ := obs["content"].(string)
		proj, _ := obs["project"].(string)
		scope, _ := obs["scope"].(string)
		topicKey, _ := obs["topic_key"].(string)

		if typ == "" || title == "" || proj == "" {
			continue
		}
		// solo insertar si session_id es vacío o la session existe
		if sessionID != "" {
			var exists int
			_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id = ?`, sessionID).Scan(&exists)
			if exists == 0 {
				continue
			}
		}

		_, err := s.store.SaveObservation(ctx, store.SaveParams{
			SyncID:    syncID,
			SessionID: sessionID,
			Type:      store.ObservationType(typ),
			Title:     title,
			Content:   content,
			Project:   proj,
			Scope:     store.Scope(scope),
			TopicKey:  topicKey,
		})
		if err != nil {
			return counts, fmt.Errorf("insertar observation: %w", err)
		}
		counts[1]++
	}

	for _, prompt := range cd.Prompts {
		sessionID, _ := prompt["session_id"].(string)
		content, _ := prompt["content"].(string)
		proj, _ := prompt["project"].(string)
		createdAt, _ := prompt["created_at"].(string)

		if content == "" || proj == "" {
			continue
		}
		res, err := db.ExecContext(ctx,
			`INSERT OR IGNORE INTO user_prompts(session_id, content, project, created_at)
			 VALUES (?, ?, ?, ?)`,
			nullableStr(sessionID), content, proj, createdAt,
		)
		if err != nil {
			return counts, fmt.Errorf("insertar prompt: %w", err)
		}
		n, _ := res.RowsAffected()
		counts[2] += int(n)
	}

	return counts, nil
}

// ensureSyncChunksTable crea la tabla sync_chunks si no existe.
func (s *Syncer) ensureSyncChunksTable() error {
	_, err := s.store.DB().Exec(`
		CREATE TABLE IF NOT EXISTS sync_chunks (
			target_key  TEXT NOT NULL,
			chunk_id    TEXT NOT NULL,
			imported_at TEXT NOT NULL,
			PRIMARY KEY(target_key, chunk_id)
		)`)
	return err
}

// markChunk registra un chunk como procesado en sync_chunks.
func (s *Syncer) markChunk(chunkID, importedAt string) error {
	if err := s.ensureSyncChunksTable(); err != nil {
		return err
	}
	_, err := s.store.DB().Exec(
		`INSERT OR IGNORE INTO sync_chunks(target_key, chunk_id, imported_at) VALUES (?, ?, ?)`,
		s.syncDir, chunkID, importedAt,
	)
	return err
}

// chunkImported retorna true si el chunk ya fue procesado.
func (s *Syncer) chunkImported(chunkID string) (bool, error) {
	if err := s.ensureSyncChunksTable(); err != nil {
		return false, err
	}
	var count int
	err := s.store.DB().QueryRow(
		`SELECT COUNT(*) FROM sync_chunks WHERE target_key = ? AND chunk_id = ?`,
		s.syncDir, chunkID,
	).Scan(&count)
	return count > 0, err
}

// nullableStr convierte cadena vacía a nil para columnas nullable.
func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// intVal extrae un int desde valores JSON (float64 o int).
func intVal(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 1
}
