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
}
