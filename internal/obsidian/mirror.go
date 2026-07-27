package obsidian

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// MirrorStore envuelve un store.Storer y mantiene un vault de Obsidian
// actualizado en cada escritura (save/update/delete), en vez de depender de
// una exportación manual (`kronos export`) que queda desactualizada apenas
// se corre. El vault nunca es la fuente de verdad — si una escritura al
// vault falla, se loguea y se sigue: el store subyacente ya confirmó el
// dato real.
type MirrorStore struct {
	store.Storer
	outDir string
	mu     sync.Mutex
}

// NewMirrorStore crea un MirrorStore que escribe en outDir.
func NewMirrorStore(inner store.Storer, outDir string) *MirrorStore {
	return &MirrorStore{Storer: inner, outDir: outDir}
}

func (m *MirrorStore) SaveObservation(ctx context.Context, p store.SaveParams) (*store.Observation, error) {
	obs, err := m.Storer.SaveObservation(ctx, p)
	if err == nil {
		m.mirror(ctx, obs, nil)
	}
	return obs, err
}

func (m *MirrorStore) SavePassive(ctx context.Context, sessionID, project, content string) (*store.Observation, error) {
	obs, err := m.Storer.SavePassive(ctx, sessionID, project, content)
	if err == nil {
		m.mirror(ctx, obs, nil)
	}
	return obs, err
}

func (m *MirrorStore) UpdateObservation(ctx context.Context, p store.UpdateParams) (*store.Observation, error) {
	before, _ := m.Storer.GetObservation(ctx, p.ID)
	obs, err := m.Storer.UpdateObservation(ctx, p)
	if err == nil {
		m.mirror(ctx, obs, before)
	}
	return obs, err
}

// LocalStore y PendingCount reenvían al store envuelto cuando este las
// implementa (típicamente *store.DualStore). Sin este passthrough explícito,
// envolver el store rompe todos los type-assertions concretos que el resto
// del código usa para detectar backend/sync (handlers.go, server.go) — un
// campo embebido de tipo interfaz (store.Storer) solo promueve los métodos
// declarados en esa interfaz, no los del tipo dinámico real.
func (m *MirrorStore) LocalStore() *store.Store {
	if ls, ok := m.Storer.(interface{ LocalStore() *store.Store }); ok {
		return ls.LocalStore()
	}
	if s, ok := m.Storer.(*store.Store); ok {
		return s
	}
	return nil
}

func (m *MirrorStore) PendingCount() int {
	if pc, ok := m.Storer.(interface{ PendingCount() int }); ok {
		return pc.PendingCount()
	}
	return 0
}

// CountSessionPrompts y CountSessionObservations reenvían al store envuelto
// — el nudge de guardado (hooks/prompt_submit.go) los necesita para saber si
// la sesión lleva N prompts sin ningún mem_save.
func (m *MirrorStore) CountSessionPrompts(ctx context.Context, sessionID string) int {
	if c, ok := m.Storer.(interface {
		CountSessionPrompts(ctx context.Context, sessionID string) int
	}); ok {
		return c.CountSessionPrompts(ctx, sessionID)
	}
	return 0
}

func (m *MirrorStore) CountSessionObservations(ctx context.Context, sessionID string) int {
	if c, ok := m.Storer.(interface {
		CountSessionObservations(ctx context.Context, sessionID string) int
	}); ok {
		return c.CountSessionObservations(ctx, sessionID)
	}
	return 0
}

func (m *MirrorStore) DeleteObservation(ctx context.Context, id int64) error {
	before, _ := m.Storer.GetObservation(ctx, id)
	err := m.Storer.DeleteObservation(ctx, id)
	if err == nil && before != nil {
		m.mu.Lock()
		_ = os.Remove(obsPath(m.outDir, before))
		m.mu.Unlock()
		m.refreshIndex(ctx)
	}
	return err
}

// mirror escribe/reescribe el archivo de obs y refresca el índice. Si before
// no es nil y su path cambió (por un rename de título o tipo), borra el
// archivo viejo para no dejar duplicados huérfanos.
func (m *MirrorStore) mirror(ctx context.Context, obs *store.Observation, before *store.Observation) {
	if obs == nil {
		return
	}
	m.mu.Lock()
	if before != nil {
		if oldPath, newPath := obsPath(m.outDir, before), obsPath(m.outDir, obs); oldPath != newPath {
			_ = os.Remove(oldPath)
		}
	}
	err := writeObservation(m.outDir, obs)
	m.mu.Unlock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[obsidian-mirror] no se pudo escribir observación %d: %v\n", obs.ID, err)
		return
	}
	m.refreshIndex(ctx)
}

func (m *MirrorStore) refreshIndex(ctx context.Context) {
	all, err := m.Storer.ListAll(ctx, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[obsidian-mirror] no se pudo refrescar índice: %v\n", err)
		return
	}
	m.mu.Lock()
	err = writeIndex(m.outDir, all)
	m.mu.Unlock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[obsidian-mirror] no se pudo escribir índice: %v\n", err)
	}
}
