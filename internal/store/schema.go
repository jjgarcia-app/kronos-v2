package store

// migrations se ejecutan en orden. Nunca modificar una existente, solo agregar nuevas.
var migrations = []string{
	// v1: schema inicial
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
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
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

	`CREATE INDEX IF NOT EXISTS idx_observations_project    ON observations(project)`,
	`CREATE INDEX IF NOT EXISTS idx_observations_topic_key  ON observations(project, topic_key) WHERE topic_key != ''`,
	`CREATE INDEX IF NOT EXISTS idx_observations_hash       ON observations(normalized_hash) WHERE normalized_hash != ''`,
	`CREATE INDEX IF NOT EXISTS idx_observations_session    ON observations(session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_observations_created    ON observations(created_at DESC)`,

	`CREATE VIRTUAL TABLE IF NOT EXISTS observations_fts USING fts5(
		title,
		content,
		type,
		project,
		topic_key,
		content='observations',
		content_rowid='id',
		tokenize='unicode61'
	)`,

	// triggers para mantener FTS5 sincronizado con observations
	`CREATE TRIGGER IF NOT EXISTS obs_fts_insert AFTER INSERT ON observations BEGIN
		INSERT INTO observations_fts(rowid, title, content, type, project, topic_key)
		VALUES (new.id, new.title, new.content, new.type, new.project, new.topic_key);
	END`,

	`CREATE TRIGGER IF NOT EXISTS obs_fts_delete AFTER DELETE ON observations BEGIN
		INSERT INTO observations_fts(observations_fts, rowid, title, content, type, project, topic_key)
		VALUES ('delete', old.id, old.title, old.content, old.type, old.project, old.topic_key);
	END`,

	`CREATE TRIGGER IF NOT EXISTS obs_fts_update AFTER UPDATE ON observations BEGIN
		INSERT INTO observations_fts(observations_fts, rowid, title, content, type, project, topic_key)
		VALUES ('delete', old.id, old.title, old.content, old.type, old.project, old.topic_key);
		INSERT INTO observations_fts(rowid, title, content, type, project, topic_key)
		VALUES (new.id, new.title, new.content, new.type, new.project, new.topic_key);
	END`,

	`CREATE TABLE IF NOT EXISTS user_prompts (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT REFERENCES sessions(id),
		content    TEXT NOT NULL,
		project    TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,

	`CREATE INDEX IF NOT EXISTS idx_prompts_project ON user_prompts(project)`,
	`CREATE INDEX IF NOT EXISTS idx_prompts_session ON user_prompts(session_id)`,
}
