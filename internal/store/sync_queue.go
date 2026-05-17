package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const createSyncQueueSQL = `
CREATE TABLE IF NOT EXISTS sync_queue (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	entity_type TEXT    NOT NULL,
	payload     TEXT    NOT NULL,
	created_at  TEXT    NOT NULL
)`

type syncEntry struct {
	ID         int64
	EntityType string
	Payload    string
	CreatedAt  time.Time
}

type syncQueue struct {
	db *sql.DB
}

func newSyncQueue(db *sql.DB) (*syncQueue, error) {
	if _, err := db.Exec(createSyncQueueSQL); err != nil {
		return nil, fmt.Errorf("create sync_queue: %w", err)
	}
	return &syncQueue{db: db}, nil
}

func (q *syncQueue) enqueue(entityType string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = q.db.Exec(
		`INSERT INTO sync_queue(entity_type, payload, created_at) VALUES (?, ?, ?)`,
		entityType, string(data), time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (q *syncQueue) pending(limit int) ([]syncEntry, error) {
	rows, err := q.db.Query(
		`SELECT id, entity_type, payload, created_at FROM sync_queue ORDER BY id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []syncEntry
	for rows.Next() {
		var e syncEntry
		var createdAt string
		if err := rows.Scan(&e.ID, &e.EntityType, &e.Payload, &createdAt); err != nil {
			return nil, err
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (q *syncQueue) delete(id int64) error {
	_, err := q.db.Exec(`DELETE FROM sync_queue WHERE id = ?`, id)
	return err
}

func (q *syncQueue) isEmpty() bool {
	var count int
	_ = q.db.QueryRow(`SELECT COUNT(*) FROM sync_queue`).Scan(&count)
	return count == 0
}
