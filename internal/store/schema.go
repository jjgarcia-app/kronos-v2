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

	// v16–v19: sync_id + tool_name columns en observations
	`ALTER TABLE observations ADD COLUMN sync_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE observations ADD COLUMN tool_name TEXT NOT NULL DEFAULT ''`,
	`UPDATE observations SET sync_id = lower(hex(randomblob(16))) WHERE sync_id = ''`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_observations_sync_id ON observations(sync_id) WHERE sync_id != ''`,

	// v20–v28: rebuild FTS5 con tool_name
	`DROP TRIGGER IF EXISTS obs_fts_insert`,
	`DROP TRIGGER IF EXISTS obs_fts_delete`,
	`DROP TRIGGER IF EXISTS obs_fts_update`,
	`DROP TABLE IF EXISTS observations_fts`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS observations_fts USING fts5(
		title,
		content,
		tool_name,
		type,
		project,
		topic_key,
		content='observations',
		content_rowid='id',
		tokenize='unicode61'
	)`,
	`CREATE TRIGGER IF NOT EXISTS obs_fts_insert AFTER INSERT ON observations BEGIN
		INSERT INTO observations_fts(rowid, title, content, tool_name, type, project, topic_key)
		VALUES (new.id, new.title, new.content, new.tool_name, new.type, new.project, new.topic_key);
	END`,
	`CREATE TRIGGER IF NOT EXISTS obs_fts_delete AFTER DELETE ON observations BEGIN
		INSERT INTO observations_fts(observations_fts, rowid, title, content, tool_name, type, project, topic_key)
		VALUES ('delete', old.id, old.title, old.content, old.tool_name, old.type, old.project, old.topic_key);
	END`,
	`CREATE TRIGGER IF NOT EXISTS obs_fts_update AFTER UPDATE ON observations BEGIN
		INSERT INTO observations_fts(observations_fts, rowid, title, content, tool_name, type, project, topic_key)
		VALUES ('delete', old.id, old.title, old.content, old.tool_name, old.type, old.project, old.topic_key);
		INSERT INTO observations_fts(rowid, title, content, tool_name, type, project, topic_key)
		VALUES (new.id, new.title, new.content, new.tool_name, new.type, new.project, new.topic_key);
	END`,
	`INSERT INTO observations_fts(observations_fts) VALUES('rebuild')`,

	// v29–v32: tabla memory_relations para conflict surfacing
	`CREATE TABLE IF NOT EXISTS memory_relations (
		id                        INTEGER PRIMARY KEY AUTOINCREMENT,
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
		superseded_by_relation_id INTEGER REFERENCES memory_relations(id),
		created_at                TEXT NOT NULL,
		updated_at                TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_memrel_source ON memory_relations(source_id, judgment_status)`,
	`CREATE INDEX IF NOT EXISTS idx_memrel_target ON memory_relations(target_id, judgment_status)`,
	`CREATE INDEX IF NOT EXISTS idx_memrel_status ON memory_relations(judgment_status)`,

	// v33–v34: sync_chunks para dedup de import
	`CREATE TABLE IF NOT EXISTS sync_chunks (
		target_key  TEXT NOT NULL,
		chunk_id    TEXT NOT NULL,
		imported_at TEXT NOT NULL,
		PRIMARY KEY (target_key, chunk_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_sync_chunks_target ON sync_chunks(target_key)`,

	// v35–v36: soft-delete para memory_relations
	`ALTER TABLE memory_relations ADD COLUMN deleted_at TEXT`,
	`CREATE INDEX IF NOT EXISTS idx_memrel_deleted ON memory_relations(deleted_at)`,

	// v37–v40: soft-delete para sessions y user_prompts
	`ALTER TABLE sessions ADD COLUMN deleted_at TEXT`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_deleted ON sessions(deleted_at)`,
	`ALTER TABLE user_prompts ADD COLUMN deleted_at TEXT`,
	`CREATE INDEX IF NOT EXISTS idx_prompts_deleted ON user_prompts(deleted_at)`,

	// v41: dedup support — injected observation IDs per session
	`ALTER TABLE sessions ADD COLUMN injected_observation_ids TEXT NULL`,

	// v42: pre-tool-use gate — track mem_search calls per session
	`ALTER TABLE sessions ADD COLUMN search_count INTEGER NOT NULL DEFAULT 0`,
}
