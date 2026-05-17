package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/checkpoint"
	"github.com/jjgarcia-app/kronos-v2/internal/secrets"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func (s *Server) handleMemSave(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	title := str(req, "title")
	content := secrets.Redact(str(req, "content"))
	typ := store.ObservationType(strOr(req, "type", "discovery"))
	project := str(req, "project")
	sessionID := str(req, "session_id")
	topicKey := str(req, "topic_key")
	scope := store.Scope(strOr(req, "scope", "project"))

	obs, err := s.store.SaveObservation(ctx, store.SaveParams{
		SessionID: sessionID,
		Type:      typ,
		Title:     title,
		Content:   content,
		Project:   project,
		Scope:     scope,
		TopicKey:  topicKey,
	})
	if err != nil {
		return fail(err), nil
	}

	s.activity.RecordSave(sessionID)

	action := "guardado"
	if obs.RevisionCount > 1 {
		action = fmt.Sprintf("actualizado (rev %d)", obs.RevisionCount)
	} else if obs.DuplicateCount > 1 {
		action = "ya existía (duplicado)"
	}

	msg := fmt.Sprintf("Observación %s. ID: %d | Topic: %s", action, obs.ID, obs.TopicKey)

	// Index embedding and check for related observations (best-effort).
	if s.rel != nil {
		indexText := title + " " + content
		_ = s.rel.Index(ctx, obs.ID, indexText)

		related, _ := s.rel.Check(ctx, obs.ID, indexText)
		if len(related) > 0 {
			msg += "\n\nObservaciones relacionadas:"
			for _, r := range related {
				msg += fmt.Sprintf("\n  - ID %d (similitud %.2f): %s", r.ObsID, r.Similarity, r.Snippet)
			}
		}
	}

	return ok(msg), nil
}

func (s *Server) handleMemSearch(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	query := str(req, "query")
	project := str(req, "project")
	limit := intOr(req, "limit", 10)

	results, err := s.store.Search(ctx, store.SearchParams{
		Query:   query,
		Project: project,
		Limit:   limit,
	})
	if err != nil {
		return fail(err), nil
	}
	if len(results) == 0 {
		return ok("No se encontraron resultados para: " + query), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Resultados para \"%s\" (%d)\n\n", query, len(results))
	for _, r := range results {
		fmt.Fprintf(&sb, "**[%d] %s** (%s)\n", r.ID, r.Title, r.Type)
		fmt.Fprintf(&sb, "Proyecto: %s | %s\n", r.Project, r.CreatedAt.Format("2006-01-02"))
		// preview de 200 chars
		preview := r.Content
		if len(preview) > 200 {
			preview = preview[:197] + "..."
		}
		fmt.Fprintf(&sb, "%s\n\n", preview)
	}
	return ok(sb.String()), nil
}

func (s *Server) handleMemContext(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	project := str(req, "project")
	sessionID := str(req, "session_id")
	limit := intOr(req, "limit", 10)

	var observations []*store.Observation
	var err error

	if sessionID != "" {
		observations, err = s.store.ListSessionObservations(ctx, sessionID)
	} else {
		observations, err = s.store.ListObservations(ctx, project, limit)
	}
	if err != nil {
		return fail(err), nil
	}
	if len(observations) == 0 {
		msg := "No hay observaciones previas"
		if project != "" {
			msg += " para el proyecto " + project
		}
		return ok(msg), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Contexto de memoria (%d observaciones)\n\n", len(observations))
	for _, o := range observations {
		fmt.Fprintf(&sb, "**[%d] %s** (%s) — %s\n", o.ID, o.Title, o.Type, o.CreatedAt.Format("2006-01-02"))
		preview := o.Content
		if len(preview) > 150 {
			preview = preview[:147] + "..."
		}
		fmt.Fprintf(&sb, "%s\n\n", preview)
	}

	// nudge si corresponde
	if nudge := s.activity.NudgeMessage(sessionID); nudge != "" {
		fmt.Fprintf(&sb, "\n---\n%s\n", nudge)
	}

	return ok(sb.String()), nil
}

func (s *Server) handleMemGetObservation(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	idStr := str(req, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return fail(fmt.Errorf("id inválido: %s", idStr)), nil
	}

	obs, err := s.store.GetObservation(ctx, id)
	if err != nil {
		return fail(err), nil
	}
	if obs == nil {
		return fail(fmt.Errorf("observación %d no encontrada", id)), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", obs.Title)
	fmt.Fprintf(&sb, "**ID**: %d | **Tipo**: %s | **Proyecto**: %s\n", obs.ID, obs.Type, obs.Project)
	fmt.Fprintf(&sb, "**Scope**: %s | **Topic key**: %s\n", obs.Scope, obs.TopicKey)
	fmt.Fprintf(&sb, "**Creado**: %s | **Rev**: %d\n\n", obs.CreatedAt.Format(time.RFC3339), obs.RevisionCount)
	fmt.Fprintf(&sb, "%s\n", obs.Content)

	return ok(sb.String()), nil
}

func (s *Server) handleMemUpdate(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	idStr := str(req, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return fail(fmt.Errorf("id inválido: %s", idStr)), nil
	}

	p := store.UpdateParams{ID: id}
	if v := str(req, "title"); v != "" {
		p.Title = &v
	}
	if v := str(req, "content"); v != "" {
		p.Content = &v
	}
	if v := str(req, "type"); v != "" {
		t := store.ObservationType(v)
		p.Type = &t
	}

	obs, err := s.store.UpdateObservation(ctx, p)
	if err != nil {
		return fail(err), nil
	}

	return ok(fmt.Sprintf("Observación %d actualizada (rev %d)", obs.ID, obs.RevisionCount)), nil
}

func (s *Server) handleMemSessionStart(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	project := str(req, "project")
	directory := strOr(req, "directory", "")
	sessionID := str(req, "session_id")
	if sessionID == "" {
		sessionID = fmt.Sprintf("s-%d", time.Now().UnixNano())
	}

	sess, err := s.store.CreateSession(ctx, sessionID, project, directory)
	if err != nil {
		return fail(err), nil
	}

	s.activity.SessionStarted(sess.ID)

	return ok(fmt.Sprintf("Sesión iniciada. ID: %s | Proyecto: %s", sess.ID, sess.Project)), nil
}

func (s *Server) handleMemSessionEnd(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	sessionID := str(req, "session_id")
	if err := s.store.EndSession(ctx, sessionID, ""); err != nil {
		return fail(err), nil
	}
	s.activity.Remove(sessionID)
	return ok("Sesión " + sessionID + " cerrada"), nil
}

func (s *Server) handleMemSessionSummary(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	sessionID := str(req, "session_id")
	summary := str(req, "summary")
	project := str(req, "project")

	if err := s.store.EndSession(ctx, sessionID, summary); err != nil {
		return fail(err), nil
	}

	// también guardar el resumen como observación para que sea buscable
	if project != "" && summary != "" {
		s.store.SaveObservation(ctx, store.SaveParams{
			SessionID: sessionID,
			Type:      store.TypeSession,
			Title:     "Resumen de sesión " + sessionID[:min(8, len(sessionID))],
			Content:   summary,
			Project:   project,
			TopicKey:  "session/summary/" + sessionID[:min(8, len(sessionID))],
		})
	}

	s.activity.Remove(sessionID)
	return ok("Resumen guardado. Sesión " + sessionID + " cerrada."), nil
}

func (s *Server) handleMemDelete(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	idStr := str(req, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return fail(fmt.Errorf("id inválido: %s", idStr)), nil
	}
	if err := s.store.DeleteObservation(ctx, id); err != nil {
		return fail(err), nil
	}
	return ok(fmt.Sprintf("Observación %d eliminada.", id)), nil
}

func (s *Server) handleMemCheckpoint(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	if s.dataDir == "" {
		return fail(fmt.Errorf("dataDir no configurado en el servidor")), nil
	}

	project := str(req, "project")
	status := strOr(req, "status", "active")

	if status == "completed" {
		if err := checkpoint.Clear(s.dataDir, project); err != nil {
			return fail(fmt.Errorf("limpiar checkpoint: %w", err)), nil
		}
		return ok("Checkpoint cerrado. La próxima sesión comenzará sin tarea en progreso."), nil
	}

	task := str(req, "task")
	nextStep := str(req, "next_step")
	if task == "" || nextStep == "" {
		return fail(fmt.Errorf("task y next_step son obligatorios")), nil
	}

	cp := checkpoint.State{
		Task:     task,
		Progress: str(req, "progress"),
		NextStep: nextStep,
		Files:    str(req, "files"),
		Notes:    str(req, "notes"),
		Project:  project,
	}

	if err := checkpoint.Save(s.dataDir, project, cp); err != nil {
		return fail(fmt.Errorf("guardar checkpoint: %w", err)), nil
	}

	return ok(fmt.Sprintf("Checkpoint guardado.\nTarea: %s\nPróximo paso: %s", task, nextStep)), nil
}

func (s *Server) handleMemSavePrompt(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	content := secrets.Redact(str(req, "content"))
	project := str(req, "project")
	sessionID := str(req, "session_id")

	if err := s.store.SavePrompt(ctx, sessionID, project, content); err != nil {
		return fail(err), nil
	}
	return ok("Prompt guardado"), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
