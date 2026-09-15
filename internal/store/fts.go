package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Search performs full-text search over observations.
//
// Dos intentos: primero la consulta tal como llegó (estricta) y, solo si no
// devuelve NADA y tiene más de un término, un segundo intento con los
// términos unidos por OR (ver widenFTSQuery).
//
// Medido en producción el 2026-09-15: la misma búsqueda que el recall usa
// con las palabras de un prompt real devolvía 0 filas aunque el hecho
// existiera en la base — `"laptop enviar archivos scp tailscale"` → 0
// resultados, `"laptop"` → 3. La memoria estaba guardada y el hook la
// inyectaba; lo que fallaba era la consulta, demasiado estricta para texto
// natural (los términos se ANDean). El síntoma que ve el usuario es "el
// agente no consultó la memoria que tenía disponible".
//
// El segundo intento NO reemplaza al primero: si la consulta estricta trae
// algo, ese resultado es el que vale (precisión); el OR es solo la red que
// evita el vacío (recall). Correr con OR siempre degradaría búsquedas
// puntuales, así que no se hace.
func (s *Store) Search(ctx context.Context, p SearchParams) ([]*SearchResult, error) {
	if p.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if p.Limit <= 0 {
		p.Limit = 20
	}

	res, err := s.searchOnce(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(res) > 0 {
		return res, nil
	}

	amplio, ok := widenFTSQuery(p.Query)
	if !ok {
		return res, nil
	}
	p2 := p
	p2.Query = amplio
	res2, err2 := s.searchOnce(ctx, p2)
	if err2 != nil {
		// Si el fallback falla, el resultado válido es el vacío del intento
		// estricto: no convertir un "no hay nada" en un error del buscador.
		return res, nil
	}
	return res2, nil
}

// searchOnce es una corrida de búsqueda contra el backend activo.
func (s *Store) searchOnce(ctx context.Context, p SearchParams) ([]*SearchResult, error) {
	if s.driver == "postgres" {
		return s.searchPostgres(ctx, p)
	}
	return s.searchSQLite(ctx, p)
}

// widenFTSQuery arma la versión ancha de una consulta: cada término entre
// comillas, unidos por OR — la misma forma que ya usa runRecall y que
// entienden los dos backends (FTS5 de SQLite y websearch_to_tsquery de
// Postgres, ver sanitizeFTSQuery y searchPostgres).
//
// Devuelve false (no hay nada que ensanchar) cuando:
//   - la consulta ya trae sintaxis explícita (comillas, paréntesis,
//     wildcard): quien la escribió así sabe lo que quiere;
//   - ya trae operadores booleanos en mayúscula (OR/AND/NOT): ya está
//     ensanchada a mano;
//   - tiene un solo término: no hay nada que unir.
func widenFTSQuery(q string) (string, bool) {
	t := strings.TrimSpace(q)
	if t == "" {
		return "", false
	}
	if strings.ContainsAny(t, `"()*`) {
		return "", false
	}
	for _, op := range []string{" OR ", " AND ", " NOT "} {
		if strings.Contains(t, op) {
			return "", false
		}
	}
	fields := strings.Fields(t)
	if len(fields) < 2 {
		return "", false
	}
	for i, f := range fields {
		fields[i] = `"` + f + `"`
	}
	return strings.Join(fields, " OR "), true
}

func (s *Store) searchSQLite(ctx context.Context, p SearchParams) ([]*SearchResult, error) {
	query := sanitizeFTSQuery(p.Query)

	var sqlRows *sql.Rows
	var err error

	if p.Project != "" {
		sqlRows, err = s.query(ctx, `
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
		sqlRows, err = s.query(ctx, `
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

// websearch_to_tsquery (no plainto_tsquery) a propósito: plainto_tsquery
// ANDea todos los términos sin excepción — medido en la ronda 2 del
// benchmark, el mismo problema que motivó sanitizeFTSQuery del lado SQLite
// ("alfresco aspect remove" con AND implícito exige los tres términos en la
// misma observación, 0 filas). websearch_to_tsquery entiende "OR" (mayúscula,
// igual que FTS5 del lado SQLite — ver sanitizeFTSQuery) como operador y
// "frases entre comillas" como frase exacta, así que la misma query armada
// por runRecall (términos entre comillas unidos por " OR ") funciona igual en
// los dos backends sin ramas especiales por driver. Para texto plano sin OR
// ni comillas (el resto de los callers de Search) el comportamiento es
// idéntico al de plainto_tsquery: todos los términos AND.
func (s *Store) searchPostgres(ctx context.Context, p SearchParams) ([]*SearchResult, error) {
	var sqlRows *sql.Rows
	var err error

	if p.Project != "" {
		sqlRows, err = s.db.QueryContext(ctx, `
			SELECT id, COALESCE(sync_id,''), COALESCE(session_id,''), type, title, content,
			       COALESCE(tool_name,''), project, scope, topic_key, normalized_hash,
			       revision_count, duplicate_count, created_at, updated_at, deleted_at,
			       ts_rank(to_tsvector('simple', title || ' ' || content),
			               websearch_to_tsquery('simple', $1)) as rank
			FROM observations
			WHERE to_tsvector('simple', title || ' ' || content) @@ websearch_to_tsquery('simple', $1)
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
			               websearch_to_tsquery('simple', $1)) as rank
			FROM observations
			WHERE to_tsvector('simple', title || ' ' || content) @@ websearch_to_tsquery('simple', $1)
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
