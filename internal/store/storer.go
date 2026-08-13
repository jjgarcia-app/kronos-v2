package store

import (
	"context"
	"time"
)

// Storer is the interface satisfied by both *Store (single backend) and
// *DualStore (local-first with PostgreSQL async replication).
// The MCP server accepts a Storer so the backend is interchangeable.
type Storer interface {
	SaveObservation(ctx context.Context, p SaveParams) (*Observation, error)
	GetObservation(ctx context.Context, id int64) (*Observation, error)
	UpdateObservation(ctx context.Context, p UpdateParams) (*Observation, error)
	DeleteObservation(ctx context.Context, id int64) error
	ListObservations(ctx context.Context, project string, limit, offset int) ([]*Observation, error)
	ListAll(ctx context.Context, project string) ([]*Observation, error)
	ListSessionObservations(ctx context.Context, sessionID string) ([]*Observation, error)
	SavePassive(ctx context.Context, sessionID, project, content string) (*Observation, error)

	CreateSession(ctx context.Context, id, project, directory string) (*Session, error)
	EndSession(ctx context.Context, id, summary string) error
	GetSession(ctx context.Context, id string) (*Session, error)
	GetActiveSession(ctx context.Context, project string) (*Session, error)
	ListSessions(ctx context.Context, project string, limit int) ([]*Session, error)
	// TouchSessionActivity actualiza el heartbeat de actividad de una sesión.
	TouchSessionActivity(ctx context.Context, id, project string) error

	// Phase 1, Change 1: injected-IDs dedup support
	PersistInjectedIDs(ctx context.Context, sessionID string, ids []string) error
	LoadInjectedIDs(ctx context.Context, sessionID string) ([]string, error)

	// Phase 1, Change 1: observation count for bootstrapping signal
	CountObservations(ctx context.Context, project string) (int, error)

	// Phase 2: pre-tool-use gate — track mem_search calls per session
	IncrementSearchCount(ctx context.Context, sessionID string) error

	SavePrompt(ctx context.Context, sessionID, project, content string) error
	Search(ctx context.Context, p SearchParams) ([]*SearchResult, error)

	// RecordToolUse registra una llamada a tool (PostToolUse) en el log de
	// uso — base para stats/analytics, ver mem_stats.
	RecordToolUse(ctx context.Context, sessionID, project, toolName string) error

	// Stats, AllSessions, TimelineObservations, GetObservationSync: usados
	// por la TUI (internal/tui) para dashboard/sesiones/timeline.
	Stats(ctx context.Context) (*Stats, error)
	AllSessions(ctx context.Context, limit int) ([]*Session, error)
	TimelineObservations(ctx context.Context, obsID int64, n int) ([]*Observation, error)
	GetObservationSync(id int64) (*Observation, error)

	// Timesheet reporta tiempo activo real (gap-discounted) y observaciones
	// guardadas por sesión — ver mem_timesheet. Los totales del reporte
	// (TotalMinutes/DailyMinutes) vienen de fusionar los eventos de todas
	// las sesiones antes de calcular huecos, para no contar dos veces el
	// tiempo de sesiones solapadas (forks, subagentes en background).
	Timesheet(ctx context.Context, from, to time.Time, project string) (*TimesheetReport, error)

	Close() error
}
