package store

import (
	"context"
	"database/sql"
)

// ToolUsageCount is one row of an aggregated tool-usage report.
type ToolUsageCount struct {
	ToolName string
	Count    int
}

// RecordToolUse appends one entry to the tool_usage log. Best-effort by
// design (called from the PostToolUse hook on every gated tool call) — a
// failure here must never block the tool call it's reporting on.
func (s *Store) RecordToolUse(ctx context.Context, sessionID, project, toolName string) error {
	_, err := s.exec(ctx,
		`INSERT INTO tool_usage (session_id, project, tool_name, created_at) VALUES (?, ?, ?, ?)`,
		sessionID, project, toolName, now())
	return err
}

// ToolUsageStats returns tool call counts for project, most-used first.
// Empty project aggregates across all projects.
func (s *Store) ToolUsageStats(ctx context.Context, project string, limit int) ([]ToolUsageCount, error) {
	if limit <= 0 {
		limit = 10
	}
	var rows *sql.Rows
	var err error
	if project != "" {
		rows, err = s.query(ctx,
			`SELECT tool_name, COUNT(*) as n FROM tool_usage WHERE project = ?
			 GROUP BY tool_name ORDER BY n DESC LIMIT ?`, project, limit)
	} else {
		rows, err = s.query(ctx,
			`SELECT tool_name, COUNT(*) as n FROM tool_usage
			 GROUP BY tool_name ORDER BY n DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ToolUsageCount
	for rows.Next() {
		var c ToolUsageCount
		if err := rows.Scan(&c.ToolName, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
