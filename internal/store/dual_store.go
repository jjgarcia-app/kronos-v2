package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// retryStage defines one phase of the staged reconnect backoff.
type retryStage struct {
	interval    time.Duration
	maxAttempts int // 0 = unlimited
}

// retrySchedule: 3×60s → 3×5min → 3×10min → 3×20min → 3×30min → ∞×60min
var retrySchedule = []retryStage{
	{60 * time.Second, 3},
	{5 * time.Minute, 3},
	{10 * time.Minute, 3},
	{20 * time.Minute, 3},
	{30 * time.Minute, 3},
	{60 * time.Minute, 0},
}

type retryState struct {
	phase    int
	attempts int
}

func (s *retryState) nextInterval() time.Duration {
	if s.phase >= len(retrySchedule) {
		return 60 * time.Minute
	}
	stage := retrySchedule[s.phase]
	interval := stage.interval
	s.attempts++
	if stage.maxAttempts > 0 && s.attempts >= stage.maxAttempts {
		s.phase++
		s.attempts = 0
	}
	return interval
}

func (s *retryState) reset() {
	s.phase = 0
	s.attempts = 0
}

// DualStore applies only when the user configured PostgreSQL as their backend.
//
// Normal operation (PG up):
//   - All reads and writes go to the primary (PG).
//
// Degraded operation (PG down):
//   - Reads and writes fall back to the SQLite buffer.
//   - Every write to the buffer is also enqueued in sync_queue.
//
// Recovery:
//   - The sync goroutine follows the staged backoff to reconnect.
//   - On reconnect: queued operations are replayed to PG in order.
//   - Normal operation resumes automatically.
//   - Users can also trigger a manual sync via `kronos sync`.
type DualStore struct {
	primary    *Store
	buffer     *Store  // SQLite emergency fallback
	primaryDSN string  // used to reconnect when primary is nil/down
	down       bool    // true when primary is unreachable
	mu         sync.RWMutex
	queue      *syncQueue // lives in the buffer DB
	cancel     context.CancelFunc
}

// NewDualFromDSN creates a DualStore with the given SQLite buffer and
// PostgreSQL DSN. The primary connection is attempted eagerly; if it fails
// the sync loop will reconnect following the staged backoff schedule.
func NewDualFromDSN(buffer *Store, pgDSN string) (*DualStore, error) {
	q, err := newSyncQueue(buffer.DB())
	if err != nil {
		return nil, err
	}

	primary, _ := NewPostgres(pgDSN) // nil on failure — lazy connect via sync loop

	ctx, cancel := context.WithCancel(context.Background())
	d := &DualStore{
		primary:    primary,
		buffer:     buffer,
		primaryDSN: pgDSN,
		down:       primary == nil,
		queue:      q,
		cancel:     cancel,
	}
	go d.syncLoop(ctx)
	return d, nil
}

func (d *DualStore) isPrimaryDown() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.down
}

func (d *DualStore) markDown() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.down = true
}

func (d *DualStore) markUp(p *Store) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.primary = p
	d.down = false
}

// ── write methods ───────────────────────────────────────────────────────────
// Try primary first; on failure fall back to buffer and enqueue for sync.

func (d *DualStore) SaveObservation(ctx context.Context, p SaveParams) (*Observation, error) {
	if !d.isPrimaryDown() {
		obs, err := d.primary.SaveObservation(ctx, p)
		if err == nil {
			return obs, nil
		}
		d.markDown()
	}
	obs, err := d.buffer.SaveObservation(ctx, p)
	if err != nil {
		return nil, err
	}
	pWithSync := p
	pWithSync.SyncID = obs.SyncID
	_ = d.queue.enqueue("save_observation", pWithSync)
	return obs, nil
}

func (d *DualStore) UpdateObservation(ctx context.Context, p UpdateParams) (*Observation, error) {
	if !d.isPrimaryDown() {
		obs, err := d.primary.UpdateObservation(ctx, p)
		if err == nil {
			return obs, nil
		}
		d.markDown()
	}
	obs, err := d.buffer.UpdateObservation(ctx, p)
	if err != nil {
		return nil, err
	}
	_ = d.queue.enqueue("update_observation", p)
	return obs, nil
}

func (d *DualStore) DeleteObservation(ctx context.Context, id int64) error {
	if !d.isPrimaryDown() {
		if err := d.primary.DeleteObservation(ctx, id); err == nil {
			return nil
		}
		d.markDown()
	}
	if err := d.buffer.DeleteObservation(ctx, id); err != nil {
		return err
	}
	type deletePayload struct{ ID int64 }
	_ = d.queue.enqueue("delete_observation", deletePayload{id})
	return nil
}

func (d *DualStore) SavePassive(ctx context.Context, sessionID, project, content string) (*Observation, error) {
	return d.SaveObservation(ctx, SaveParams{
		SessionID: sessionID,
		Type:      TypePassive,
		Title:     passiveTitle(content),
		Content:   content,
		Project:   project,
		Scope:     ScopeProject,
	})
}

func (d *DualStore) CreateSession(ctx context.Context, id, project, directory string) (*Session, error) {
	if !d.isPrimaryDown() {
		sess, err := d.primary.CreateSession(ctx, id, project, directory)
		if err == nil {
			return sess, nil
		}
		d.markDown()
	}
	sess, err := d.buffer.CreateSession(ctx, id, project, directory)
	if err != nil {
		return nil, err
	}
	type sessionPayload struct{ ID, Project, Directory string }
	_ = d.queue.enqueue("create_session", sessionPayload{id, project, directory})
	return sess, nil
}

func (d *DualStore) EndSession(ctx context.Context, id, summary string) error {
	if !d.isPrimaryDown() {
		if err := d.primary.EndSession(ctx, id, summary); err == nil {
			return nil
		}
		d.markDown()
	}
	if err := d.buffer.EndSession(ctx, id, summary); err != nil {
		return err
	}
	type endPayload struct{ ID, Summary string }
	_ = d.queue.enqueue("end_session", endPayload{id, summary})
	return nil
}

func (d *DualStore) SavePrompt(ctx context.Context, sessionID, project, content string) error {
	if !d.isPrimaryDown() {
		if err := d.primary.SavePrompt(ctx, sessionID, project, content); err == nil {
			return nil
		}
		d.markDown()
	}
	if err := d.buffer.SavePrompt(ctx, sessionID, project, content); err != nil {
		return err
	}
	type promptPayload struct{ SessionID, Project, Content string }
	_ = d.queue.enqueue("save_prompt", promptPayload{sessionID, project, content})
	return nil
}

// ── read methods ────────────────────────────────────────────────────────────
// Try primary first; on failure fall back to buffer.

func (d *DualStore) GetObservation(ctx context.Context, id int64) (*Observation, error) {
	if !d.isPrimaryDown() {
		obs, err := d.primary.GetObservation(ctx, id)
		if err == nil {
			return obs, nil
		}
		d.markDown()
	}
	return d.buffer.GetObservation(ctx, id)
}

func (d *DualStore) ListObservations(ctx context.Context, project string, limit int) ([]*Observation, error) {
	if !d.isPrimaryDown() {
		obs, err := d.primary.ListObservations(ctx, project, limit)
		if err == nil {
			return obs, nil
		}
		d.markDown()
	}
	return d.buffer.ListObservations(ctx, project, limit)
}

func (d *DualStore) ListAll(ctx context.Context, project string) ([]*Observation, error) {
	if !d.isPrimaryDown() {
		obs, err := d.primary.ListAll(ctx, project)
		if err == nil {
			return obs, nil
		}
		d.markDown()
	}
	return d.buffer.ListAll(ctx, project)
}

func (d *DualStore) ListSessionObservations(ctx context.Context, sessionID string) ([]*Observation, error) {
	if !d.isPrimaryDown() {
		obs, err := d.primary.ListSessionObservations(ctx, sessionID)
		if err == nil {
			return obs, nil
		}
		d.markDown()
	}
	return d.buffer.ListSessionObservations(ctx, sessionID)
}

func (d *DualStore) GetSession(ctx context.Context, id string) (*Session, error) {
	if !d.isPrimaryDown() {
		sess, err := d.primary.GetSession(ctx, id)
		if err == nil {
			return sess, nil
		}
		d.markDown()
	}
	return d.buffer.GetSession(ctx, id)
}

func (d *DualStore) GetActiveSession(ctx context.Context, project string) (*Session, error) {
	if !d.isPrimaryDown() {
		sess, err := d.primary.GetActiveSession(ctx, project)
		if err == nil {
			return sess, nil
		}
		d.markDown()
	}
	return d.buffer.GetActiveSession(ctx, project)
}

func (d *DualStore) ListSessions(ctx context.Context, project string, limit int) ([]*Session, error) {
	if !d.isPrimaryDown() {
		sessions, err := d.primary.ListSessions(ctx, project, limit)
		if err == nil {
			return sessions, nil
		}
		d.markDown()
	}
	return d.buffer.ListSessions(ctx, project, limit)
}

func (d *DualStore) Search(ctx context.Context, p SearchParams) ([]*SearchResult, error) {
	if !d.isPrimaryDown() {
		results, err := d.primary.Search(ctx, p)
		if err == nil {
			return results, nil
		}
		d.markDown()
	}
	return d.buffer.Search(ctx, p)
}

// LocalStore retorna el Store SQLite local (buffer).
// Usar para operaciones que siempre deben ejecutarse en local: conflictos, sync, checkpoints.
func (d *DualStore) LocalStore() *Store {
	return d.buffer
}

func (d *DualStore) Close() error {
	d.cancel()
	d.mu.RLock()
	primary := d.primary
	d.mu.RUnlock()
	if primary != nil {
		_ = primary.Close()
	}
	return d.buffer.Close()
}

// ── sync loop ───────────────────────────────────────────────────────────────

func (d *DualStore) syncLoop(ctx context.Context) {
	state := &retryState{}
	for {
		interval := state.nextInterval()
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			if !d.isPrimaryDown() && d.queue.isEmpty() {
				state.reset()
				continue
			}
			if d.FlushPending(ctx) {
				state.reset()
			}
		}
	}
}

// FlushPendingVerbose is like FlushPending but returns the underlying error.
func (d *DualStore) FlushPendingVerbose(ctx context.Context) (bool, error) {
	d.mu.RLock()
	primary := d.primary
	isDown := d.down
	d.mu.RUnlock()

	if isDown || primary == nil {
		conn, err := NewPostgres(d.primaryDSN)
		if err != nil {
			return false, fmt.Errorf("conectar a postgres: %w", err)
		}
		d.markUp(conn)
		primary = conn
	}

	entries, err := d.queue.pending(200)
	if err != nil {
		return false, fmt.Errorf("leer sync_queue: %w", err)
	}
	if len(entries) == 0 {
		return true, nil
	}

	for _, e := range entries {
		if err := d.replayEntry(ctx, primary, e); err != nil {
			d.markDown()
			return false, fmt.Errorf("replay %s: %w", e.EntityType, err)
		}
		_ = d.queue.delete(e.ID)
	}
	return true, nil
}

// FlushPending tries to reconnect to the primary and replay all queued
// operations in insertion order. Returns true when the queue is fully drained.
// Exported so `kronos sync` can call it directly.
func (d *DualStore) FlushPending(ctx context.Context) bool {
	// ensure we have a live primary connection
	d.mu.RLock()
	primary := d.primary
	isDown := d.down
	d.mu.RUnlock()

	if isDown || primary == nil {
		conn, err := NewPostgres(d.primaryDSN)
		if err != nil {
			return false // still unreachable
		}
		d.markUp(conn)
		primary = conn
	}

	entries, err := d.queue.pending(200)
	if err != nil || len(entries) == 0 {
		return true
	}

	for _, e := range entries {
		if err := d.replayEntry(ctx, primary, e); err != nil {
			d.markDown()
			return false
		}
		_ = d.queue.delete(e.ID)
	}
	return true
}

// PendingCount returns the number of operations waiting to be synced.
func (d *DualStore) PendingCount() int {
	entries, _ := d.queue.pending(1000000)
	return len(entries)
}

func (d *DualStore) replayEntry(ctx context.Context, primary *Store, e syncEntry) error {
	switch e.EntityType {
	case "save_observation":
		var p SaveParams
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil // corrupt: discard
		}
		_, err := primary.SaveObservation(ctx, p)
		return err

	case "update_observation":
		var p UpdateParams
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil
		}
		_, err := primary.UpdateObservation(ctx, p)
		return err

	case "delete_observation":
		var p struct{ ID int64 }
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil
		}
		return primary.DeleteObservation(ctx, p.ID)

	case "create_session":
		var p struct{ ID, Project, Directory string }
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil
		}
		_, err := primary.CreateSession(ctx, p.ID, p.Project, p.Directory)
		return err

	case "end_session":
		var p struct{ ID, Summary string }
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil
		}
		return primary.EndSession(ctx, p.ID, p.Summary)

	case "save_prompt":
		var p struct{ SessionID, Project, Content string }
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil
		}
		return primary.SavePrompt(ctx, p.SessionID, p.Project, p.Content)
	}
	return nil
}
