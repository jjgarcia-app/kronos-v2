package store

import "time"

type ObservationType string

const (
	TypeBugfix       ObservationType = "bugfix"
	TypeDecision     ObservationType = "decision"
	TypeArchitecture ObservationType = "architecture"
	TypeDiscovery    ObservationType = "discovery"
	TypePattern      ObservationType = "pattern"
	TypeConfig       ObservationType = "config"
	TypePreference   ObservationType = "preference"
	TypePassive      ObservationType = "passive"
	TypeSession      ObservationType = "session"
)

type Scope string

const (
	ScopeProject Scope = "project"
	ScopeGlobal  Scope = "global"
)

type Session struct {
	ID        string
	Project   string
	Directory string
	StartedAt time.Time
	EndedAt   *time.Time
	Summary   string
}

type Observation struct {
	ID             int64
	SyncID         string
	SessionID      string
	Type           ObservationType
	Title          string
	Content        string
	ToolName       string
	Project        string
	Scope          Scope
	TopicKey       string
	NormalizedHash string
	RevisionCount  int
	DuplicateCount int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      *time.Time
}

type SaveParams struct {
	SyncID    string // si se provee, usar este en lugar de generar uno nuevo
	SessionID string
	Type      ObservationType
	Title     string
	Content   string
	ToolName  string
	Project   string
	Scope     Scope
	TopicKey  string // si se provee, hace upsert por topic_key+project
}

type UpdateParams struct {
	ID      int64
	Title   *string
	Content *string
	Type    *ObservationType
}

type SearchParams struct {
	Query   string
	Project string
	Scope   Scope  // si vacío, busca en project + global
	Limit   int
}

type SearchResult struct {
	Observation
	Rank float64
}

type UserPrompt struct {
	ID        int64
	SessionID string
	Content   string
	Project   string
	CreatedAt time.Time
}
