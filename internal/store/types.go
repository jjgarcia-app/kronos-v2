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
	// TypeIntent: plan o afirmación dicha por el usuario o el agente que
	// TODAVÍA no está confirmada contra el código real — a diferencia de
	// bugfix/decision/discovery/etc, que describen algo ya verificado.
	// Motivado por dos hallazgos del benchmark 2026-09-11 (8 sesiones
	// reales): (1) el usuario dijo "el comando de release es X" cuando el
	// script todavía no existía en el repo, y esa afirmación quedó guardada
	// sin distinguirse de un hecho comprobado; (2) en otra sesión el agente
	// encontró una observación guardada, la citó, y — correctamente — se
	// negó a darla por buena porque contradecía el filesystem ("no lo voy a
	// dar por bueno solo porque está en memoria"). Kronos no verifica
	// automáticamente: TypeIntent solo marca la diferencia para que el
	// bloque core y la inyección por continuidad avisen "esto es un plan,
	// confirmalo antes de asumirlo" (ver internal/hooks/core_block.go).
	TypeIntent ObservationType = "intent"
	// TypeSkill: procedimiento reutilizable — "cómo hacer X paso a paso" —
	// a diferencia de bugfix/decision/discovery/etc, que describen un HECHO
	// puntual, una skill describe un PROCESO con pasos ordenados que se va a
	// repetir. Motivado por la auditoría comparativa 2026-09-18: kronos tenía
	// bloque core (always-on) y recall FTS+vector, pero ningún lugar donde
	// guardar procedimiento sin que compitiera por el mismo espacio que los
	// hechos puntuales — "cómo cerrar una ronda de release" o "cómo
	// diagnosticar un bug de buffer-vs-primario" quedaban solo en el
	// historial de chat/commits, no en memoria reutilizable.
	//
	// El título (Title) es el nombre corto — lo único que se inyecta SIEMPRE
	// en el bloque core, comprimido a una línea (ver formatSkillLine en
	// internal/hooks/core_block.go). El Content es el cuerpo completo del
	// procedimiento — nunca se manda al bloque core, se carga solo bajo
	// demanda vía el tool MCP mem_skill_load (ver internal/mcp/handlers.go).
	// Mismo patrón de "progressive disclosure" que las skills de Claude Code:
	// nombre siempre visible, cuerpo cargado solo cuando hace falta.
	TypeSkill ObservationType = "skill"
)

type Scope string

const (
	ScopeProject Scope = "project"
	ScopeGlobal  Scope = "global"
)

type Session struct {
	ID                     string
	Project                string
	Directory              string
	StartedAt              time.Time
	EndedAt                *time.Time
	Summary                string
	InjectedObservationIDs []string // decoded from JSON column; nil if never set
	SearchCount            int
	// LastActivityAt: heartbeat actualizado en cada UserPromptSubmit (ver
	// TouchSessionActivity). Distinto de StartedAt/EndedAt — permite a la
	// TUI distinguir una sesión con actividad reciente de una que nunca se
	// cerró pero lleva días sin uso.
	LastActivityAt time.Time
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
	Scope   Scope // si vacío, busca en project + global
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
