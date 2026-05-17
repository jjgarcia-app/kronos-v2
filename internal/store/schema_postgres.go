package store

// postgresMigrations are postgres-compatible migrations.
// Key differences from SQLite:
//   - BIGSERIAL instead of INTEGER PRIMARY KEY AUTOINCREMENT
//   - No FTS5 virtual table: pg_trgm GIN index instead
//   - No triggers: search done inline
//   - ON CONFLICT DO NOTHING instead of INSERT OR IGNORE
var postgresMigrations = []string{
	`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS sessions (
		id          TEXT PRIMARY KEY,
		project     TEXT NOT NULL,
		directory   TEXT NOT NULL DEFAULT '',
		started_at  TEXT NOT NULL,
		ended_at    TEXT,
		summary     TEXT NOT NULL DEFAULT ''
	)`,

	`CREATE TABLE IF NOT EXISTS observations (
		id               BIGSERIAL PRIMARY KEY,
		session_id       TEXT REFERENCES sessions(id),
		type             TEXT NOT NULL,
		title            TEXT NOT NULL,
		content          TEXT NOT NULL,
		project          TEXT NOT NULL,
		scope            TEXT NOT NULL DEFAULT 'project',
		topic_key        TEXT NOT NULL DEFAULT '',
		normalized_hash  TEXT NOT NULL DEFAULT '',
		revision_count   INTEGER NOT NULL DEFAULT 1,
		duplicate_count  INTEGER NOT NULL DEFAULT 1,
		last_seen_at     TEXT NOT NULL,
		created_at       TEXT NOT NULL,
		updated_at       TEXT NOT NULL,
		deleted_at       TEXT
	)`,

	`CREATE INDEX IF NOT EXISTS idx_observations_project ON observations(project)`,
	`CREATE INDEX IF NOT EXISTS idx_observations_topic_key ON observations(project, topic_key) WHERE topic_key != ''`,
	`CREATE INDEX IF NOT EXISTS idx_observations_hash ON observations(normalized_hash) WHERE normalized_hash != ''`,
	`CREATE INDEX IF NOT EXISTS idx_observations_session ON observations(session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_observations_created ON observations(created_at DESC)`,

	`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
	`CREATE INDEX IF NOT EXISTS idx_observations_trgm ON observations USING GIN((title || ' ' || content) gin_trgm_ops)`,

	`CREATE TABLE IF NOT EXISTS user_prompts (
		id         BIGSERIAL PRIMARY KEY,
		session_id TEXT REFERENCES sessions(id),
		content    TEXT NOT NULL,
		project    TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,

	`CREATE INDEX IF NOT EXISTS idx_prompts_project ON user_prompts(project)`,
	`CREATE INDEX IF NOT EXISTS idx_prompts_session ON user_prompts(session_id)`,

	// v11–v14: sync_id + tool_name
	`ALTER TABLE observations ADD COLUMN IF NOT EXISTS sync_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE observations ADD COLUMN IF NOT EXISTS tool_name TEXT NOT NULL DEFAULT ''`,
	`UPDATE observations SET sync_id = md5(random()::text || id::text) WHERE sync_id = ''`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_observations_sync_id ON observations(sync_id) WHERE sync_id != ''`,

	// v15–v18: memory_relations
	`CREATE TABLE IF NOT EXISTS memory_relations (
		id                        BIGSERIAL PRIMARY KEY,
		sync_id                   TEXT UNIQUE NOT NULL,
		source_id                 TEXT,
		target_id                 TEXT,
		relation                  TEXT NOT NULL DEFAULT 'pending',
		reason                    TEXT,
		evidence                  TEXT,
		confidence                REAL,
		judgment_status           TEXT NOT NULL DEFAULT 'pending',
		marked_by_actor           TEXT,
		marked_by_kind            TEXT,
		marked_by_model           TEXT,
		session_id                TEXT,
		superseded_at             TEXT,
		superseded_by_relation_id BIGINT REFERENCES memory_relations(id),
		created_at                TEXT NOT NULL,
		updated_at                TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_memrel_source ON memory_relations(source_id, judgment_status)`,
	`CREATE INDEX IF NOT EXISTS idx_memrel_target ON memory_relations(target_id, judgment_status)`,
	`CREATE INDEX IF NOT EXISTS idx_memrel_status ON memory_relations(judgment_status)`,

	// v19–v20: sync_chunks
	`CREATE TABLE IF NOT EXISTS sync_chunks (
		target_key  TEXT NOT NULL,
		chunk_id    TEXT NOT NULL,
		imported_at TEXT NOT NULL,
		PRIMARY KEY (target_key, chunk_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_sync_chunks_target ON sync_chunks(target_key)`,

	// v21–v22: soft-delete para memory_relations
	`ALTER TABLE memory_relations ADD COLUMN IF NOT EXISTS deleted_at TEXT`,
	`CREATE INDEX IF NOT EXISTS idx_memrel_deleted ON memory_relations(deleted_at)`,

	// v23–v24: soft-delete para sessions
	`ALTER TABLE sessions ADD COLUMN IF NOT EXISTS deleted_at TEXT`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_deleted ON sessions(deleted_at)`,

	// v25–v26: soft-delete para user_prompts
	`ALTER TABLE user_prompts ADD COLUMN IF NOT EXISTS deleted_at TEXT`,
	`CREATE INDEX IF NOT EXISTS idx_prompts_deleted ON user_prompts(deleted_at)`,
}
