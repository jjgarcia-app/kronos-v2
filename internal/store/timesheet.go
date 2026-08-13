package store

import (
	"context"
	"time"

	kproject "github.com/jjgarcia-app/kronos-v2/internal/project"
)

// SessionTimesheet is one session's real activity: gap-discounted active
// minutes (computed from tool_usage ∪ user_prompts timestamps, never
// estimated) plus the observations saved during it.
type SessionTimesheet struct {
	Session       *Session
	ActiveMinutes int
	Observations  []*Observation
}

// timesheetGapThreshold: a gap between two consecutive activity events
// longer than this doesn't count as active time — same threshold the TUI
// uses to decide a session went "inactiva" (ver internal/tui/view.go
// sessionStaleAfter). A gap this long means the session was left idle, not
// worked on.
const timesheetGapThreshold = 30 * time.Minute

// Timesheet reports real active time and saved observations, per session
// that started within [from, to), optionally filtered by project.
func (s *Store) Timesheet(ctx context.Context, from, to time.Time, project string) ([]*SessionTimesheet, error) {
	sessions, err := s.sessionsInRange(ctx, from, to, project)
	if err != nil {
		return nil, err
	}

	out := make([]*SessionTimesheet, 0, len(sessions))
	for _, sess := range sessions {
		events, err := s.activityTimestamps(ctx, sess.ID)
		if err != nil {
			return nil, err
		}
		obs, err := s.ListSessionObservations(ctx, sess.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, &SessionTimesheet{
			Session:       sess,
			ActiveMinutes: activeMinutes(events),
			Observations:  obs,
		})
	}
	return out, nil
}

func (s *Store) sessionsInRange(ctx context.Context, from, to time.Time, project string) ([]*Session, error) {
	query := `SELECT id, project, directory, started_at, ended_at, summary, injected_observation_ids, search_count, last_activity_at
		 FROM sessions WHERE deleted_at IS NULL AND started_at >= ? AND started_at < ?`
	args := []any{from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339)}
	if project != "" {
		query += " AND project = ?"
		args = append(args, kproject.Normalize(project))
	}
	query += " ORDER BY started_at ASC"

	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// activityTimestamps returns every tool_usage/user_prompt timestamp for a
// session, sorted ascending — the raw event stream used to compute real
// active time.
func (s *Store) activityTimestamps(ctx context.Context, sessionID string) ([]time.Time, error) {
	rows, err := s.query(ctx,
		`SELECT created_at FROM tool_usage WHERE session_id = ?
		 UNION ALL
		 SELECT created_at FROM user_prompts WHERE session_id = ? AND deleted_at IS NULL
		 ORDER BY created_at ASC`,
		sessionID, sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []time.Time
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			continue
		}
		events = append(events, t)
	}
	return events, rows.Err()
}

// activeMinutes sums the gaps between consecutive events, discarding any gap
// longer than timesheetGapThreshold (treated as idle, not active work).
func activeMinutes(events []time.Time) int {
	if len(events) < 2 {
		return 0
	}
	var total time.Duration
	for i := 1; i < len(events); i++ {
		gap := events[i].Sub(events[i-1])
		if gap > 0 && gap <= timesheetGapThreshold {
			total += gap
		}
	}
	return int(total.Minutes())
}
