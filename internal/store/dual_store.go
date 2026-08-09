package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	kproject "github.com/jjgarcia-app/kronos-v2/internal/project"
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

	// localOnly: proyectos (ya normalizados) que nunca deben escribirse al
	// primary ni encolarse para sync — se quedan solo en buffer. Ver
	// SetLocalOnlyProjects y config.DBConfig.LocalOnlyProjects.
	localOnly map[string]bool
}

// SetLocalOnlyProjects marca qué proyectos nunca deben salir de esta
// máquina — ni al primary si está sano, ni encolados para sync si primary
// está caído. Reemplaza el set completo (no acumula entre llamadas).
func (d *DualStore) SetLocalOnlyProjects(projects []string) {
	m := make(map[string]bool, len(projects))
	for _, p := range projects {
		m[kproject.Normalize(p)] = true
	}
	d.mu.Lock()
	d.localOnly = m
	d.mu.Unlock()
}

func (d *DualStore) isLocalOnly(project string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.localOnly[kproject.Normalize(project)]
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
	if d.isLocalOnly(p.Project) {
		return d.buffer.SaveObservation(ctx, p)
	}
	if !d.isPrimaryDown() {
		obs, err := d.primary.SaveObservation(ctx, p)
		if err == nil {
			return obs, nil
		}
		d.markDown()
	}
	obs, err := d.buffer.SaveObservation(ctx, p)
	if err != nil && isFKError(err) && p.SessionID != "" {
		// La sesión se creó mientras primary estaba sano (CreateSession
		// escribe solo ahí, no en buffer) y recién ahora, en medio de la
		// sesión, primary se cayó — el buffer nunca vio esa sesión, así que
		// el FK de session_id no tiene a qué apuntar. Mismo criterio que
		// replayEntry en la dirección inversa: preservar el contenido sin el
		// link de sesión antes que perder el hallazgo entero.
		p.SessionID = ""
		obs, err = d.buffer.SaveObservation(ctx, p)
	}
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
	if d.isLocalOnly(project) {
		return d.buffer.CreateSession(ctx, id, project, directory)
	}
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
	// Una sesión local-only nunca existió en primary (CreateSession la
	// esquivó) — sin este chequeo, intentar primary acá devolvería "no
	// encontrada" y eso se leería como primary caído, degradando TODO lo
	// demás por una sesión que nunca debía sincronizarse.
	if sess, _ := d.buffer.GetSession(ctx, id); sess != nil && d.isLocalOnly(sess.Project) {
		return d.buffer.EndSession(ctx, id, summary)
	}
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

func (d *DualStore) RecordToolUse(ctx context.Context, sessionID, project, toolName string) error {
	if d.isLocalOnly(project) {
		return d.buffer.RecordToolUse(ctx, sessionID, project, toolName)
	}
	if !d.isPrimaryDown() {
		if err := d.primary.RecordToolUse(ctx, sessionID, project, toolName); err == nil {
			return nil
		}
		d.markDown()
	}
	if err := d.buffer.RecordToolUse(ctx, sessionID, project, toolName); err != nil {
		return err
	}
	type toolUsePayload struct{ SessionID, Project, ToolName string }
	_ = d.queue.enqueue("record_tool_use", toolUsePayload{sessionID, project, toolName})
	return nil
}

func (d *DualStore) SavePrompt(ctx context.Context, sessionID, project, content string) error {
	if d.isLocalOnly(project) {
		return d.buffer.SavePrompt(ctx, sessionID, project, content)
	}
	if !d.isPrimaryDown() {
		if err := d.primary.SavePrompt(ctx, sessionID, project, content); err == nil {
			return nil
		}
		d.markDown()
	}
	err := d.buffer.SavePrompt(ctx, sessionID, project, content)
	if err != nil && isFKError(err) && sessionID != "" {
		// mismo caso que SaveObservation: la sesión se creó con primary sano
		// (nunca llegó al buffer) y recién ahora primary se cayó.
		sessionID = ""
		err = d.buffer.SavePrompt(ctx, sessionID, project, content)
	}
	if err != nil {
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
		if err == nil && obs != nil {
			return obs, nil
		}
		if err != nil {
			d.markDown()
		}
		// err == nil && obs == nil: primary está sano pero no tiene esta fila
		// (puede existir solo en buffer — ej. drift de IDs entre SQLite y
		// Postgres, cada uno con su propio autoincrement). No es motivo para
		// marcar primary down, pero sí para probar el buffer antes de
		// devolver "no encontrado".
	}
	return d.buffer.GetObservation(ctx, id)
}

func (d *DualStore) ListObservations(ctx context.Context, project string, limit, offset int) ([]*Observation, error) {
	if !d.isPrimaryDown() {
		obs, err := d.primary.ListObservations(ctx, project, limit, offset)
		if err == nil {
			return obs, nil
		}
		d.markDown()
	}
	return d.buffer.ListObservations(ctx, project, limit, offset)
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
		if err == nil && sess != nil {
			return sess, nil
		}
		if err != nil {
			d.markDown()
		}
	}
	return d.buffer.GetSession(ctx, id)
}

func (d *DualStore) GetActiveSession(ctx context.Context, project string) (*Session, error) {
	if !d.isPrimaryDown() {
		sess, err := d.primary.GetActiveSession(ctx, project)
		if err == nil && sess != nil {
			return sess, nil
		}
		if err != nil {
			d.markDown()
		}
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

func (d *DualStore) PersistInjectedIDs(ctx context.Context, sessionID string, ids []string) error {
	if !d.isPrimaryDown() {
		if err := d.primary.PersistInjectedIDs(ctx, sessionID, ids); err == nil {
			return nil
		}
		d.markDown()
	}
	return d.buffer.PersistInjectedIDs(ctx, sessionID, ids)
}

func (d *DualStore) LoadInjectedIDs(ctx context.Context, sessionID string) ([]string, error) {
	if !d.isPrimaryDown() {
		ids, err := d.primary.LoadInjectedIDs(ctx, sessionID)
		if err == nil {
			return ids, nil
		}
		d.markDown()
	}
	return d.buffer.LoadInjectedIDs(ctx, sessionID)
}

func (d *DualStore) CountObservations(ctx context.Context, project string) (int, error) {
	if !d.isPrimaryDown() {
		n, err := d.primary.CountObservations(ctx, project)
		if err == nil {
			return n, nil
		}
		d.markDown()
	}
	return d.buffer.CountObservations(ctx, project)
}

// CountSessionPrompts y CountSessionObservations respaldan el nudge de
// guardado (ver hooks/prompt_submit.go): cada N prompts sin ninguna
// observación guardada en la sesión, se recuerda al agente que use mem_save.
// *store.Store ya las tenía; DualStore nunca las implementó — con Postgres
// como backend (el caso real de producción) el type assertion duck-typed
// contra estos dos métodos fallaba en silencio y el nudge jamás disparaba.
func (d *DualStore) CountSessionPrompts(ctx context.Context, sessionID string) int {
	if !d.isPrimaryDown() {
		return d.primary.CountSessionPrompts(ctx, sessionID)
	}
	return d.buffer.CountSessionPrompts(ctx, sessionID)
}

func (d *DualStore) CountSessionObservations(ctx context.Context, sessionID string) int {
	if !d.isPrimaryDown() {
		return d.primary.CountSessionObservations(ctx, sessionID)
	}
	return d.buffer.CountSessionObservations(ctx, sessionID)
}

func (d *DualStore) CountSessionPromptsSinceLastSave(ctx context.Context, sessionID string) int {
	if !d.isPrimaryDown() {
		return d.primary.CountSessionPromptsSinceLastSave(ctx, sessionID)
	}
	return d.buffer.CountSessionPromptsSinceLastSave(ctx, sessionID)
}

// IncrementSearchCount — antes, si primary estaba sano pero no tenía esta
// fila de sesión (ver GetSession: divergencia de IDs entre SQLite y
// Postgres, o una sesión creada en buffer mientras primary estaba caído),
// el UPDATE de primary afectaba 0 filas SIN error — *store.Store tiene
// contrato fail-open a propósito — y DualStore lo trataba como "listo",
// sin probar nunca el buffer. Resultado real: search_count nunca se
// incrementaba para esas sesiones y el gate de pre-tool-use quedaba
// bloqueado para siempre pese a buscar de verdad.
func (d *DualStore) IncrementSearchCount(ctx context.Context, sessionID string) error {
	if !d.isPrimaryDown() {
		n, err := d.primary.incrementSearchCountAffected(ctx, sessionID)
		if err == nil && n > 0 {
			return nil
		}
		if err != nil {
			d.markDown()
		}
	}
	if err := d.buffer.IncrementSearchCount(ctx, sessionID); err != nil {
		return err
	}
	type searchCountPayload struct{ SessionID string }
	_ = d.queue.enqueue("increment_search_count", searchCountPayload{sessionID})
	return nil
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
	// attempt an immediate flush on startup so short-lived MCP sessions
	// (< 60s) don't leave the queue un-drained indefinitely.
	if !d.queue.isEmpty() {
		d.FlushPending(ctx)
	}

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

// flushBatchSize es cuántas entradas se leen de sync_queue por vuelta.
// maxFlushBatches acota el trabajo total de una sola llamada a Flush* (~200k
// entradas) — una cola real nunca debería acercarse a eso; es solo para que
// un flush no corra indefinidamente si algo deja la cola creciendo sin freno.
const (
	flushBatchSize  = 200
	maxFlushBatches = 1000
)

// FlushPendingVerbose is like FlushPending but returns the underlying error.
func (d *DualStore) FlushPendingVerbose(ctx context.Context) (bool, error) {
	primary, err := d.ensurePrimary()
	if err != nil {
		return false, fmt.Errorf("conectar a postgres: %w", err)
	}

	for i := 0; i < maxFlushBatches; i++ {
		entries, err := d.queue.pending(flushBatchSize)
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
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
	}
	return false, fmt.Errorf("sync_queue no drenó tras %d lotes — posible crecimiento sin freno", maxFlushBatches)
}

// FlushPending tries to reconnect to the primary and replay ALL queued
// operations in insertion order, en lotes de flushBatchSize hasta vaciar la
// cola por completo (no solo el primer lote). Returns true when the queue is
// fully drained. Exported so `kronos sync` can call it directly.
func (d *DualStore) FlushPending(ctx context.Context) bool {
	primary, err := d.ensurePrimary()
	if err != nil {
		return false
	}

	for i := 0; i < maxFlushBatches; i++ {
		entries, err := d.queue.pending(flushBatchSize)
		if err != nil {
			return false
		}
		if len(entries) == 0 {
			return true
		}
		for _, e := range entries {
			if err := d.replayEntry(ctx, primary, e); err != nil {
				d.markDown()
				return false
			}
			_ = d.queue.delete(e.ID)
		}
		if ctx.Err() != nil {
			return false
		}
	}
	return false
}

// ensurePrimary devuelve la conexión primary viva, reconectando si hace falta.
func (d *DualStore) ensurePrimary() (*Store, error) {
	d.mu.RLock()
	primary := d.primary
	isDown := d.down
	d.mu.RUnlock()

	if !isDown && primary != nil {
		return primary, nil
	}
	conn, err := NewPostgres(d.primaryDSN)
	if err != nil {
		return nil, err
	}
	d.markUp(conn)
	return conn, nil
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
		if err != nil && isFKError(err) && p.SessionID != "" {
			// parent session missing from primary (e.g. PG was wiped after
			// this entry was queued) — preserve content without session link.
			p.SessionID = ""
			_, err = primary.SaveObservation(ctx, p)
		}
		return err

	case "update_observation":
		var p UpdateParams
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil
		}
		_, err := primary.UpdateObservation(ctx, p)
		if err != nil && strings.Contains(err.Error(), "observation") && strings.Contains(err.Error(), "not found") {
			return nil // observation gone from primary — discard update
		}
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
		if err != nil && isDuplicateError(err) {
			return nil // session already exists in primary — idempotent
		}
		return err

	case "end_session":
		var p struct{ ID, Summary string }
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil
		}
		err := primary.EndSession(ctx, p.ID, p.Summary)
		// session may not exist in the primary if PG was wiped/rebuilt after
		// the entry was queued — treat as already resolved and discard.
		if err != nil && strings.Contains(err.Error(), "session not found") {
			return nil
		}
		return err

	case "save_prompt":
		var p struct{ SessionID, Project, Content string }
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil
		}
		err := primary.SavePrompt(ctx, p.SessionID, p.Project, p.Content)
		if err != nil && isFKError(err) {
			return nil // session missing from primary — discard orphaned prompt
		}
		return err

	case "record_tool_use":
		var p struct{ SessionID, Project, ToolName string }
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil
		}
		return primary.RecordToolUse(ctx, p.SessionID, p.Project, p.ToolName)

	case "increment_search_count":
		var p struct{ SessionID string }
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil
		}
		return primary.IncrementSearchCount(ctx, p.SessionID)
	}
	return nil
}

// isFKError returns true when err is a foreign key constraint violation
// from either the PostgreSQL or SQLite driver.
func isFKError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "foreign key constraint") ||
		strings.Contains(s, "FOREIGN KEY constraint")
}

// isDuplicateError returns true when err signals a unique/primary-key conflict.
func isDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "duplicate key") ||
		strings.Contains(s, "UNIQUE constraint") ||
		strings.Contains(s, "already exists")
}
