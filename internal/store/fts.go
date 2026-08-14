package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Search performs full-text search over observations.
func (s *Store) Search(ctx context.Context, p SearchParams) ([]*SearchResult, error) {
	if p.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if p.Limit <= 0 {
		p.Limit = 20
	}
	if s.driver == "postgres" {
		return s.searchPostgres(ctx, p)
	}
	return s.searchSQLite(ctx, p)
}

func (s *Store) searchSQLite(ctx context.Context, p SearchParams) ([]*SearchResult, error) {
	query := sanitizeFTSQuery(p.Query)

	var sqlRows *sql.Rows
	var err error

	if p.Project != "" {
		sqlRows, err = s.db.QueryContext(ctx, `
			SELECT o.id, o.sync_id, o.session_id, o.type, o.title, o.content, o.tool_name,
			       o.project, o.scope, o.topic_key, o.normalized_hash,
			       o.revision_count, o.duplicate_count, o.created_at, o.updated_at, o.deleted_at,
			       bm25(observations_fts) as rank
			FROM observations_fts
			JOIN observations o ON observations_fts.rowid = o.id
			WHERE observations_fts MATCH ?
			  AND (o.project = ? OR o.scope = 'global')
			  AND o.deleted_at IS NULL
			ORDER BY rank
			LIMIT ?`,
			query, p.Project, p.Limit,
		)
	} else {
		sqlRows, err = s.db.QueryContext(ctx, `
			SELECT o.id, o.sync_id, o.session_id, o.type, o.title, o.content, o.tool_name,
			       o.project, o.scope, o.topic_key, o.normalized_hash,
			       o.revision_count, o.duplicate_count, o.created_at, o.updated_at, o.deleted_at,
			       bm25(observations_fts) as rank
			FROM observations_fts
			JOIN observations o ON observations_fts.rowid = o.id
			WHERE observations_fts MATCH ?
			  AND o.deleted_at IS NULL
			ORDER BY rank
			LIMIT ?`,
			query, p.Limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer sqlRows.Close()

	var results []*SearchResult
	for sqlRows.Next() {
		r, err := scanSearchResult(sqlRows)
		if err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, sqlRows.Err()
}

func (s *Store) searchPostgres(ctx context.Context, p SearchParams) ([]*SearchResult, error) {
	var sqlRows *sql.Rows
	var err error

	if p.Project != "" {
		sqlRows, err = s.db.QueryContext(ctx, `
			SELECT id, COALESCE(sync_id,''), COALESCE(session_id,''), type, title, content,
			       COALESCE(tool_name,''), project, scope, topic_key, normalized_hash,
			       revision_count, duplicate_count, created_at, updated_at, deleted_at,
			       ts_rank(to_tsvector('simple', title || ' ' || content),
			               plainto_tsquery('simple', $1)) as rank
			FROM observations
			WHERE to_tsvector('simple', title || ' ' || content) @@ plainto_tsquery('simple', $1)
			  AND (project = $2 OR scope = 'global')
			  AND deleted_at IS NULL
			ORDER BY rank DESC
			LIMIT $3`,
			p.Query, p.Project, p.Limit,
		)
	} else {
		sqlRows, err = s.db.QueryContext(ctx, `
			SELECT id, COALESCE(sync_id,''), COALESCE(session_id,''), type, title, content,
			       COALESCE(tool_name,''), project, scope, topic_key, normalized_hash,
			       revision_count, duplicate_count, created_at, updated_at, deleted_at,
			       ts_rank(to_tsvector('simple', title || ' ' || content),
			               plainto_tsquery('simple', $1)) as rank
			FROM observations
			WHERE to_tsvector('simple', title || ' ' || content) @@ plainto_tsquery('simple', $1)
			  AND deleted_at IS NULL
			ORDER BY rank DESC
			LIMIT $2`,
			p.Query, p.Limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres search: %w", err)
	}
	defer sqlRows.Close()

	var results []*SearchResult
	for sqlRows.Next() {
		r, err := scanSearchResult(sqlRows)
		if err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, sqlRows.Err()
}

func scanSearchResult(row interface{ Scan(dest ...any) error }) (*SearchResult, error) {
	var r SearchResult
	var syncID, sessionID, toolName, deletedAt sql.NullString
	var topicKey, hash, createdAt, updatedAt string

	err := row.Scan(
		&r.ID, &syncID, &sessionID, &r.Type, &r.Title, &r.Content,
		&toolName, &r.Project, &r.Scope, &topicKey, &hash,
		&r.RevisionCount, &r.DuplicateCount,
		&createdAt, &updatedAt, &deletedAt,
		&r.Rank,
	)
	if err != nil {
		return nil, err
	}
	r.SyncID = syncID.String
	r.SessionID = sessionID.String
	r.ToolName = toolName.String
	r.TopicKey = topicKey
	r.NormalizedHash = hash
	r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	r.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	r.DeletedAt = nullableTime(deletedAt)
	return &r, nil
}

// sanitizeFTSQuery limpia el query para evitar errores de sintaxis FTS5.
//
// Si el caller ya usa comillas, wildcards o agrupación explícitas, se
// respeta tal cual — son señales inequívocas de sintaxis FTS5 deliberada.
// En cualquier otro caso, cada término se encierra en comillas por
// separado — incluido un término como "AT-441": el parser de queries de
// FTS5 (distinto del tokenizer que indexa el contenido) corta un bareword
// sin comillas en el primer "-", así que "AT-441" sin comillas se lee como
// "AT" seguido del operador NOT aplicado a "441", y en ciertas
// combinaciones (ej. con OR) esto rompe con "no such column: 441" en vez
// de buscar. Encerrarlo en comillas lo vuelve una frase de 2 tokens ("at"
// "441", mismo corte que aplica el tokenizer al indexar) en vez de
// sintaxis de operadores.
//
// Antes esto se saltaba enteramente la sanitización si el query contenía
// " OR "/" AND "/" NOT " como texto — asumiendo que eso significaba
// "sintaxis FTS5 avanzada, no tocar". Bug real: un query normal como
// "AT-441 OR AT-442 agrupar subcontratos" (búsqueda con múltiples tickets,
// nada de FTS5 avanzado) calificaba igual y rompía. Ahora los operadores
// booleanos (OR/AND/NOT, mayúsculas exactas — así los reconoce FTS5) se
// preservan como operadores; todo lo demás se sanea término por término,
// sin importar si aparecen mezclados con operadores o no.
func sanitizeFTSQuery(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return q
	}
	if strings.ContainsAny(q, `"*^()`) {
		return q
	}

	terms := strings.Fields(q)
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		switch t {
		case "OR", "AND", "NOT":
			parts = append(parts, t)
		default:
			t = strings.ReplaceAll(t, `"`, "")
			if t == "" {
				continue
			}
			parts = append(parts, fmt.Sprintf(`"%s"`, t))
		}
	}
	if len(parts) == 0 {
		return q
	}
	// Espacio simple, no " AND " — entre dos frases entre comillas sin
	// operador de por medio, FTS5 ya aplica AND implícito por default;
	// insertar "AND" a mano acá rompería justo al lado de un OR/AND/NOT
	// explícito que ya vino en parts (ej. `"a" AND OR "b"`, inválido).
	return strings.Join(parts, " ")
}
