package store

import (
	"context"
	"sort"
	"time"

	kproject "github.com/jjgarcia-app/kronos-v2/internal/project"
)

// SessionTimesheet is one session's real activity: gap-discounted active
// minutes (computed from tool_usage ∪ user_prompts timestamps, never
// estimated) plus the observations saved during it.
//
// ActiveMinutes is scoped to THIS session alone — when sessions overlap in
// wall-clock time (a fork or a background subagent runs while the parent
// session keeps going; the harness gives each its own session_id), the same
// real minute is counted once per session here. Never sum ActiveMinutes
// across sessions for a grand total — use TimesheetReport.TotalMinutes /
// DailyMinutes instead, which are computed from the globally merged event
// stream and dedup overlapping time automatically.
type SessionTimesheet struct {
	Session       *Session
	ActiveMinutes int
	Observations  []*Observation
}

// TimesheetReport is the full result of a Timesheet query: the per-session
// breakdown (for listing what happened and where) plus deduplicated totals
// computed from every session's events merged into one timeline, so
// concurrent sessions (forks, background subagents) never double-count.
type TimesheetReport struct {
	Sessions     []*SessionTimesheet
	DailyMinutes map[string]int // "2026-08-05" -> active minutes that day, deduplicated
	TotalMinutes int
}

// timesheetGapThreshold: a gap between two consecutive activity events
// longer than this doesn't count as active time — same threshold the TUI
// uses to decide a session went "inactiva" (ver internal/tui/view.go
// sessionStaleAfter). A gap this long means the session was left idle, not
// worked on.
const timesheetGapThreshold = 30 * time.Minute

// Timesheet reports real active time and saved observations, per session
// that started within [from, to), optionally filtered by project.
//
// Per-session ActiveMinutes and the report's dedup'd totals are computed
// separately on purpose: a fork or background subagent gets its own
// session_id from the harness, so two sessions can have real, overlapping
// wall-clock activity at the same time. Merging every session's events into
// one sorted timeline before computing gaps (instead of summing each
// session's independently-computed minutes) is what prevents that overlap
// from being counted twice in the total.
func (s *Store) Timesheet(ctx context.Context, from, to time.Time, project string) (*TimesheetReport, error) {
	sessions, err := s.sessionsInRange(ctx, from, to, project)
	if err != nil {
		return nil, err
	}

	sessionEntries := make([]*SessionTimesheet, 0, len(sessions))
	var allEvents []time.Time
	for _, sess := range sessions {
		events, err := s.activityTimestamps(ctx, sess.ID)
		if err != nil {
			return nil, err
		}
		obs, err := s.ListSessionObservations(ctx, sess.ID)
		if err != nil {
			return nil, err
		}
		sessionEntries = append(sessionEntries, &SessionTimesheet{
			Session:       sess,
			ActiveMinutes: activeMinutes(events),
			Observations:  obs,
		})
		allEvents = append(allEvents, events...)
	}

	sort.Slice(allEvents, func(i, j int) bool { return allEvents[i].Before(allEvents[j]) })

	return &TimesheetReport{
		Sessions:     sessionEntries,
		DailyMinutes: dailyActiveMinutes(allEvents),
		TotalMinutes: activeMinutes(allEvents),
	}, nil
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

// dailyActiveMinutes buckets the same gap-discounted active time as
// activeMinutes, but per UTC calendar day — a session that keeps going past
// midnight has its minutes split between the two days they actually
// happened in, instead of all landing on whichever day the session started.
// events must already be sorted ascending.
//
// Accumulates as time.Duration per day and only truncates to whole minutes
// once, at the very end — same as activeMinutes. Truncating each individual
// gap to int minutes before summing loses every gap shorter than a minute
// (routine between consecutive tool calls), which silently drops real
// active time on a session with many short gaps.
//
// A gap can cross at most one midnight: gaps longer than timesheetGapThreshold
// (30min) are discarded before they'd ever need to, since 30min < 24h.
func dailyActiveMinutes(events []time.Time) map[string]int {
	totals := make(map[string]time.Duration)
	for i := 1; i < len(events); i++ {
		start, end := events[i-1], events[i]
		gap := end.Sub(start)
		if gap <= 0 || gap > timesheetGapThreshold {
			continue
		}

		startDay := start.Format("2006-01-02")
		endDay := end.Format("2006-01-02")
		if startDay == endDay {
			totals[startDay] += gap
			continue
		}

		midnight := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
		totals[startDay] += midnight.Sub(start)
		totals[endDay] += end.Sub(midnight)
	}

	out := make(map[string]int, len(totals))
	for day, d := range totals {
		out[day] = int(d.Minutes())
	}
	return out
}
