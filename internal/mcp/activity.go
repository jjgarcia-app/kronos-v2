package mcp

import (
	"sync"
	"time"
)

// Activity trackea el estado de actividad de una sesión para el nudge inteligente.
type Activity struct {
	mu                 sync.Mutex
	sessions           map[string]*sessionActivity
	nudgeActions       int           // nudge si significant actions >= este valor
	nudgeFallbackMins  time.Duration // nudge por tiempo si no hay actividad significativa
}

type sessionActivity struct {
	startedAt          time.Time
	lastSaveAt         time.Time
	significantActions int // Write/Edit/Bash tool calls
	saveCount          int
}

func NewActivity(nudgeActions int, nudgeFallbackMins int) *Activity {
	return &Activity{
		sessions:          make(map[string]*sessionActivity),
		nudgeActions:      nudgeActions,
		nudgeFallbackMins: time.Duration(nudgeFallbackMins) * time.Minute,
	}
}

func (a *Activity) SessionStarted(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions[sessionID] = &sessionActivity{
		startedAt:  time.Now(),
		lastSaveAt: time.Now(),
	}
}

func (a *Activity) RecordSave(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.sessions[sessionID]; ok {
		s.lastSaveAt = time.Now()
		s.saveCount++
	}
}

func (a *Activity) RecordSignificantAction(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.sessions[sessionID]; ok {
		s.significantActions++
	}
}

// NudgeMessage retorna un mensaje de recordatorio si corresponde, o vacío.
func (a *Activity) NudgeMessage(sessionID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()

	s, ok := a.sessions[sessionID]
	if !ok {
		return ""
	}

	elapsed := time.Since(s.lastSaveAt)
	sessionAge := time.Since(s.startedAt)

	// sesión muy nueva: no molestar
	if sessionAge < 2*time.Minute {
		return ""
	}

	// nudge por actividad significativa sin guardar
	if s.significantActions >= a.nudgeActions && elapsed > 5*time.Minute {
		s.significantActions = 0 // reset para no repetir hasta próxima ráfaga
		return "Hiciste cambios significativos sin guardar en memoria. Considera llamar mem_save con lo que aprendiste."
	}

	// nudge por tiempo fallback
	if elapsed > a.nudgeFallbackMins {
		return "Han pasado más de " + a.nudgeFallbackMins.String() + " sin guardar en memoria. Si completaste algo relevante, usa mem_save."
	}

	return ""
}

func (a *Activity) Remove(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, sessionID)
}
