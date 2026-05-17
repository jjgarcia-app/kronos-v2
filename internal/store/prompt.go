package store

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

func (s *Store) SavePrompt(ctx context.Context, sessionID, project, content string) error {
	if content == "" || project == "" {
		return nil
	}
	_, err := s.exec(ctx,
		`INSERT INTO user_prompts(session_id, content, project, created_at) VALUES (?, ?, ?, ?)`,
		nullStr(sessionID), content, project, now(),
	)
	return err
}

func (s *Store) DeletePrompt(ctx context.Context, id int64) error {
	_, err := s.exec(ctx,
		`UPDATE user_prompts SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, now(), id)
	return err
}

func (s *Store) ListPrompts(ctx context.Context, project string, limit int) ([]*UserPrompt, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.query(ctx,
		`SELECT id, session_id, content, project, created_at
		 FROM user_prompts WHERE project = ? AND deleted_at IS NULL
		 ORDER BY created_at DESC LIMIT ?`, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var prompts []*UserPrompt
	for rows.Next() {
		var p UserPrompt
		var sessionID string
		if err := rows.Scan(&p.ID, &sessionID, &p.Content, &p.Project, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.SessionID = sessionID
		prompts = append(prompts, &p)
	}
	return prompts, rows.Err()
}

// ExtractLearnings extrae items de una sección "## Key Learnings:" del output de un sub-agente.
// Mínimo 20 chars y 4 palabras por ítem para evitar ruido.
func ExtractLearnings(text string) []string {
	header := learningHeaderRe.FindStringIndex(text)
	if header == nil {
		return nil
	}
	body := text[header[1]:]

	// leer hasta el próximo ## header o fin del texto
	if next := nextHeaderRe.FindStringIndex(body); next != nil {
		body = body[:next[0]]
	}

	var results []string
	seen := make(map[string]bool)

	for _, line := range strings.Split(body, "\n") {
		item := itemRe.FindStringSubmatch(line)
		if item == nil {
			continue
		}
		content := strings.TrimSpace(item[1])
		if len(content) < 20 {
			continue
		}
		if len(strings.Fields(content)) < 4 {
			continue
		}
		key := strings.ToLower(content)
		if seen[key] {
			continue
		}
		seen[key] = true
		results = append(results, content)
	}
	return results
}

var (
	learningHeaderRe = regexp.MustCompile(`(?im)^#{2,3}\s+(?:Key\s+Learnings?|Learnings?|Aprendizajes(?:\s+Clave)?):?\s*$`)
	nextHeaderRe     = regexp.MustCompile(`(?m)^#{1,3}\s+`)
	itemRe           = regexp.MustCompile(`(?m)^\s*(?:\d+[.)]\s+|[-*]\s+)(.+)$`)
)

func validateLearning(s string) bool {
	return len(s) >= 20 && len(strings.Fields(s)) >= 4
}

var _ = fmt.Sprintf // evitar unused import
var _ = validateLearning
