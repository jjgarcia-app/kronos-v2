package store

import "context"

// Storer is the interface satisfied by both *Store (single backend) and
// *DualStore (local-first with PostgreSQL async replication).
// The MCP server accepts a Storer so the backend is interchangeable.
type Storer interface {
	SaveObservation(ctx context.Context, p SaveParams) (*Observation, error)
	GetObservation(ctx context.Context, id int64) (*Observation, error)
	UpdateObservation(ctx context.Context, p UpdateParams) (*Observation, error)
	DeleteObservation(ctx context.Context, id int64) error
	ListObservations(ctx context.Context, project string, limit int) ([]*Observation, error)
	ListAll(ctx context.Context, project string) ([]*Observation, error)
	ListSessionObservations(ctx context.Context, sessionID string) ([]*Observation, error)
	SavePassive(ctx context.Context, sessionID, project, content string) (*Observation, error)

	CreateSession(ctx context.Context, id, project, directory string) (*Session, error)
	EndSession(ctx context.Context, id, summary string) error
	GetSession(ctx context.Context, id string) (*Session, error)
	GetActiveSession(ctx context.Context, project string) (*Session, error)
	ListSessions(ctx context.Context, project string, limit int) ([]*Session, error)

	// Phase 1, Change 1: injected-IDs dedup support
	PersistInjectedIDs(ctx context.Context, sessionID string, ids []string) error
	LoadInjectedIDs(ctx context.Context, sessionID string) ([]string, error)

	// Phase 1, Change 1: observation count for bootstrapping signal
	CountObservations(ctx context.Context, project string) (int, error)

	SavePrompt(ctx context.Context, sessionID, project, content string) error
	Search(ctx context.Context, p SearchParams) ([]*SearchResult, error)

	Close() error
}
