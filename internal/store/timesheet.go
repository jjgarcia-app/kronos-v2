package store

import (
	"context"
	"sort"
	"time"

	kproject "github.com/jjgarcia-app/kronos-v2/internal/project"
)

// SessionDayEntry is one session's contribution to one calendar day: its own
// gap-discounted active minutes that landed on that specific day (a
// long-lived session — Claude Code conversations routinely span days or
// weeks — can contribute to several different DayTimesheet entries, one per
// day it actually touched). Observations are attached only on the day of
// the session's earliest in-range activity, so a long session's saved
// narrative isn't repeated under every day it spans.
type SessionDayEntry struct {
	Session      *Session
	Minutes      int
	Observations []*Observation
}

// DayTimesheet is one calendar day's real activity: Minutes is
// deduplicated — computed from every session's events for this project
// merged into one timeline before counting gaps, so two sessions with real,
// overlapping wall-clock activity (a fork or background subagent running
// alongside the session that spawned it) never double-count the same
// minute. Sessions lists which sessions contributed and how much each did
// on this day specifically — those per-session figures are NOT deduplicated
// against each other, so don't sum them expecting to match Minutes.
type DayTimesheet struct {
	Day      string // "2026-08-05"
	Minutes  int
	Sessions []*SessionDayEntry
}

// TimesheetReport is the full result of a Timesheet query, organized by the
// calendar day activity actually happened on — never by which day a session
// started, since a long-lived session's start date can be weeks before any
// of the activity a given query range cares about.
type TimesheetReport struct {
	Days         []*DayTimesheet // sorted ascending by day
	TotalMinutes int
}

// timesheetGapThreshold: a gap between two consecutive activity events
// longer than this doesn't count as active time — same threshold the TUI
// uses to decide a session went "inactiva" (ver internal/tui/view.go
// sessionStaleAfter). A gap this long means the session was left idle, not
// worked on.
const timesheetGapThreshold = 30 * time.Minute

// Timesheet reports real active time and saved observations, organized by
// the calendar day activity actually happened on, for every session with
// activity in [from, to), optionally filtered by project.
//
// Per-day totals and the report's grand total are computed from every
// session's events merged into one timeline BEFORE counting gaps (instead
// of summing each session's independently-computed minutes) — a fork or
// background subagent gets its own session_id from the harness, so two
// sessions can have real, overlapping wall-clock activity at the same time,
// and summing their minutes separately would double-count that overlap.
//
// Grouping by day is likewise driven by where each event actually falls,
// never by Session.StartedAt — a session can live for days or weeks (this
// package itself was built inside one that started 2026-07-27 and was still
// active over two weeks later), so its activity routinely spans many days
// within a single query range.
func (s *Store) Timesheet(ctx context.Context, from, to time.Time, project string) (*TimesheetReport, error) {
	sessions, err := s.sessionsInRange(ctx, from, to, project)
	if err != nil {
		return nil, err
	}

	type sessionActivity struct {
		session      *Session
		events       []time.Time
		observations []*Observation
	}

	activities := make([]sessionActivity, 0, len(sessions))
	var allEvents []time.Time
	for _, sess := range sessions {
		events, err := s.activityTimestamps(ctx, sess.ID, from, to)
		if err != nil {
			return nil, err
		}
		obs, err := s.ListSessionObservations(ctx, sess.ID)
		if err != nil {
			return nil, err
		}
		activities = append(activities, sessionActivity{session: sess, events: events, observations: obs})
		allEvents = append(allEvents, events...)
	}

	sort.Slice(allEvents, func(i, j int) bool { return allEvents[i].Before(allEvents[j]) })
	globalDaily := dailyActiveMinutes(allEvents)

	dayMap := make(map[string]*DayTimesheet, len(globalDaily))
	for day, minutes := range globalDaily {
		dayMap[day] = &DayTimesheet{Day: day, Minutes: minutes}
	}

	for _, a := range activities {
		if len(a.events) == 0 {
			// sessionsInRange only returns sessions with an activity row in
			// range, so this shouldn't happen — but skip defensively rather
			// than attribute a session to a day it has no evidence for.
			continue
		}

		earliestDay := ""
		for _, e := range a.events {
			d := e.Format("2006-01-02")
			if earliestDay == "" || d < earliestDay {
				earliestDay = d
			}
		}

		sessDaily := dailyActiveMinutes(a.events)
		if len(sessDaily) == 0 {
			// Fewer than 2 events (or a single lone event) means no gap to
			// measure, not "no activity" — a session with exactly one real
			// tool call, or one carrying a saved observation, must still
			// surface with 0 minutes on its one real day rather than vanish
			// from the report entirely.
			sessDaily = map[string]int{earliestDay: 0}
		}

		for day, minutes := range sessDaily {
			dt, ok := dayMap[day]
			if !ok {
				// Only reachable if a session's own gaps produced a day the
				// merged global timeline didn't (shouldn't happen since
				// allEvents is a superset), but fail safe instead of losing
				// the entry.
				dt = &DayTimesheet{Day: day}
				dayMap[day] = dt
			}
			entry := &SessionDayEntry{Session: a.session, Minutes: minutes}
			if day == earliestDay {
				entry.Observations = a.observations
			}
			dt.Sessions = append(dt.Sessions, entry)
		}
	}

	days := make([]*DayTimesheet, 0, len(dayMap))
	for _, dt := range dayMap {
		sort.Slice(dt.Sessions, func(i, j int) bool {
			return dt.Sessions[i].Session.StartedAt.Before(dt.Sessions[j].Session.StartedAt)
		})
		days = append(days, dt)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Day < days[j].Day })

	return &TimesheetReport{
		Days:         days,
		TotalMinutes: activeMinutes(allEvents),
	}, nil
}

// sessionsInRange returns every session with real activity (a tool_usage or
// user_prompts row) inside [from, to) — NOT sessions whose started_at falls
// in that window.
//
// Claude Code sessions routinely live for days or weeks (this very
// conversation started 2026-07-27 and is still active today): filtering by
// started_at would make a long-lived session's entire activity invisible to
// any date range query that doesn't happen to include its original start
// date, even though most of its real work happened well within the
// requested range. Filtering by "has an activity row in range" instead
// finds the session regardless of how old it is.
func (s *Store) sessionsInRange(ctx context.Context, from, to time.Time, project string) ([]*Session, error) {
	fromStr := from.UTC().Format(time.RFC3339)
	toStr := to.UTC().Format(time.RFC3339)

	query := `SELECT s.id, s.project, s.directory, s.started_at, s.ended_at, s.summary,
			s.injected_observation_ids, s.search_count, s.last_activity_at
		 FROM sessions s
		 WHERE s.deleted_at IS NULL
		 AND s.id IN (
			SELECT session_id FROM tool_usage WHERE created_at >= ? AND created_at < ?
			UNION
			SELECT session_id FROM user_prompts WHERE created_at >= ? AND created_at < ? AND deleted_at IS NULL
		 )`
	args := []any{fromStr, toStr, fromStr, toStr}
	if project != "" {
		query += " AND s.project = ?"
		args = append(args, kproject.Normalize(project))
	}
	query += " ORDER BY s.started_at ASC"

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
// session within [from, to), sorted ascending — the raw event stream used to
// compute real active time. Bounded to the requested range so a long-lived
// session (see sessionsInRange) contributes only the activity that actually
// happened in this window, not its entire multi-day history.
func (s *Store) activityTimestamps(ctx context.Context, sessionID string, from, to time.Time) ([]time.Time, error) {
	fromStr := from.UTC().Format(time.RFC3339)
	toStr := to.UTC().Format(time.RFC3339)
	rows, err := s.query(ctx,
		`SELECT created_at FROM tool_usage WHERE session_id = ? AND created_at >= ? AND created_at < ?
		 UNION ALL
		 SELECT created_at FROM user_prompts WHERE session_id = ? AND deleted_at IS NULL AND created_at >= ? AND created_at < ?
		 ORDER BY created_at ASC`,
		sessionID, fromStr, toStr, sessionID, fromStr, toStr,
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
