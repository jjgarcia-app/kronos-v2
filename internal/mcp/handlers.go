package mcp

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jjgarcia-app/kronos-v2/internal/checkpoint"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
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

	if err := validateSaveParams(content, typ, topicKey); err != nil {
		return fail(err), nil
	}

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

	// Index embedding fire-and-forget: Ollama can take 30s+ even warm; blocking
	// the handler is not acceptable. Related observations are skipped from the
	// save response — use mem_search for similarity lookups instead.
	if s.rel != nil {
		indexText := title + " " + content
		obsID := obs.ID
		rel := s.rel
		go func() {
			embCtx, embCancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer embCancel()
			_ = rel.Index(embCtx, obsID, indexText)
		}()
	}

	// Conflict surfacing via FTS5 BM25 (best-effort, always local).
	if ls := s.localStore(); ls != nil && obs.RevisionCount == 1 {
		candidates, _ := ls.FindCandidates(ctx, obs, store.CandidateOptions{Project: project})
		if len(candidates) > 0 {
			msg += "\n\n**Conflictos potenciales detectados** — usar mem_judge para resolverlos:"
			for _, c := range candidates {
				msg += fmt.Sprintf("\n  - judgment_id=%d | ID %d: %s (%s)", c.JudgmentID, c.ID, c.Title, c.Type)
			}
		}
	}

	return ok(msg), nil
}

func (s *Server) handleMemSearch(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	query := str(req, "query")
	project := str(req, "project")
	sessionID := str(req, "session_id")
	limit := intOr(req, "limit", 10)

	if sessionID != "" {
		_ = s.store.IncrementSearchCount(ctx, sessionID)
	}

	// BM25 — recuperación léxica
	bm25Results, err := s.store.Search(ctx, store.SearchParams{
		Query:   query,
		Project: project,
		Limit:   limit * 2, // traer más para fusión RRF
	})
	if err != nil {
		return fail(err), nil
	}

	// RRF: acumular scores por observación (BM25 + vector)
	const rrfK = 60.0
	type rrfEntry struct {
		result *store.SearchResult
		score  float64
	}
	scored := make(map[int64]*rrfEntry, len(bm25Results))
	for i, r := range bm25Results {
		r := r
		scored[r.ID] = &rrfEntry{result: r, score: 1.0 / (rrfK + float64(i+1))}
	}

	// búsqueda vectorial complementaria (bge-m3)
	if s.rel != nil && s.rel.Enabled() {
		hits, _ := s.rel.Similar(ctx, query, limit*2, 0, 0.55)
		if ls := s.localStore(); ls != nil {
			for i, h := range hits {
				vScore := 1.0 / (rrfK + float64(i+1))
				if entry, exists := scored[h.ObsID]; exists {
					entry.score += vScore
				} else {
					obs, err := ls.GetObservation(ctx, h.ObsID)
					if err == nil && obs != nil {
						scored[h.ObsID] = &rrfEntry{
							result: &store.SearchResult{Observation: *obs, Rank: float64(h.Similarity)},
							score:  vScore,
						}
					}
				}
			}
		}
	}

	// ordenar por score RRF descendente
	entries := make([]*rrfEntry, 0, len(scored))
	for _, e := range scored {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].score > entries[j].score
	})

	// truncar al límite solicitado
	if len(entries) > limit {
		entries = entries[:limit]
	}

	if len(entries) == 0 {
		return ok("No se encontraron resultados para: " + query), nil
	}

	// actualizar last_seen_at para que GCStale no elimine observaciones activas
	if ls := s.localStore(); ls != nil {
		ids := make([]int64, len(entries))
		for i, e := range entries {
			ids[i] = e.result.ID
		}
		_ = ls.TouchLastSeen(ctx, ids)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Resultados para \"%s\" (%d)\n\n", query, len(entries))
	for _, e := range entries {
		r := e.result
		fmt.Fprintf(&sb, "**[%d] %s** (%s)\n", r.ID, r.Title, r.Type)
		fmt.Fprintf(&sb, "Proyecto: %s | %s\n", r.Project, r.CreatedAt.Format("2006-01-02"))
		preview := secrets.Redact(r.Content)
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
		staleMarker := ""
		if !o.UpdatedAt.IsZero() && time.Since(o.UpdatedAt) > 90*24*time.Hour {
			staleMarker = " ⚠ obsoleta"
		}
		fmt.Fprintf(&sb, "**[%d] %s** (%s%s) — %s\n", o.ID, o.Title, o.Type, staleMarker, o.CreatedAt.Format("2006-01-02"))
		preview := secrets.Redact(o.Content)
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
	fmt.Fprintf(&sb, "%s\n", secrets.Redact(obs.Content))

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
		v = secrets.Redact(v)
		newType := store.ObservationType(strOr(req, "type", "discovery"))
		// topic_key not re-validated on update — it was set on creation
		if err := validateSaveParams(v, newType, ""); err != nil {
			return fail(err), nil
		}
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

	if err := validateSummaryFormat(summary); err != nil {
		return fail(err), nil
	}

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

// ── Fase 3: nuevos tools ────────────────────────────────────────────────────

func (s *Server) handleMemJudge(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	idStr := str(req, "judgment_id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return fail(fmt.Errorf("judgment_id inválido: %s", idStr)), nil
	}
	relation := str(req, "relation")
	reason := str(req, "reason")
	evidence := str(req, "evidence")
	confidence, _ := strconv.ParseFloat(strOr(req, "confidence", "0.8"), 64)
	sessionID := str(req, "session_id")

	ls := s.localStore()
	if ls == nil {
		return fail(fmt.Errorf("mem_judge no disponible: store incompatible")), nil
	}

	rel, err := ls.JudgeRelation(ctx, store.JudgeRelationParams{
		JudgmentID:    id,
		Relation:      relation,
		Reason:        reason,
		Evidence:      evidence,
		Confidence:    confidence,
		MarkedByActor: "agent",
		MarkedByKind:  "agent",
		SessionID:     sessionID,
	})
	if err != nil {
		return fail(err), nil
	}

	return ok(fmt.Sprintf("Relación %d juzgada: %s (confianza %.2f)\nRazón: %s",
		rel.ID, rel.Relation, rel.Confidence, rel.Reason)), nil
}

func (s *Server) handleMemCompare(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	sourceStr := str(req, "source_id")
	targetStr := str(req, "target_id")
	relation := str(req, "relation")
	reason := str(req, "reason")
	confidence, _ := strconv.ParseFloat(strOr(req, "confidence", "0.7"), 64)

	ls := s.localStore()
	if ls == nil {
		return fail(fmt.Errorf("mem_compare no disponible: store incompatible")), nil
	}

	sourceID, err := resolveObsSyncID(ctx, ls, sourceStr)
	if err != nil {
		return fail(fmt.Errorf("source_id: %w", err)), nil
	}
	targetID, err := resolveObsSyncID(ctx, ls, targetStr)
	if err != nil {
		return fail(fmt.Errorf("target_id: %w", err)), nil
	}

	syncID, err := ls.JudgeBySemantic(ctx, sourceID, targetID, relation, confidence, reason, "agent")
	if err != nil {
		return fail(err), nil
	}
	if syncID == "" {
		return ok(fmt.Sprintf("Par %s ↔ %s marcado como no conflictivo.", sourceStr, targetStr)), nil
	}
	return ok(fmt.Sprintf("Relación semántica registrada.\nSync ID: %s\nRelación: %s | Confianza: %.2f",
		syncID, relation, confidence)), nil
}

func (s *Server) handleMemSuggestTopicKey(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	title := str(req, "title")
	typ := strOr(req, "type", "misc")
	suggestion := buildTopicKey(title, typ)
	return ok(fmt.Sprintf("topic_key sugerido: \"%s\"", suggestion)), nil
}

func (s *Server) handleMemTimeline(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	idStr := str(req, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return fail(fmt.Errorf("id inválido: %s", idStr)), nil
	}
	window := intOr(req, "window", 3)

	ls := s.localStore()
	if ls == nil {
		return fail(fmt.Errorf("mem_timeline no disponible")), nil
	}

	observations, err := ls.TimelineObservations(ctx, id, window)
	if err != nil {
		return fail(err), nil
	}
	if len(observations) == 0 {
		return ok(fmt.Sprintf("Sin contexto de timeline para ID %d", id)), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Timeline (±%d alrededor de ID %d)\n\n", window, id)
	for _, o := range observations {
		marker := "  "
		if o.ID == id {
			marker = "▶"
		}
		fmt.Fprintf(&sb, "%s [%d] %s (%s) — %s\n",
			marker, o.ID, o.Title, o.Type, o.CreatedAt.Format("2006-01-02 15:04"))
	}
	return ok(sb.String()), nil
}

func (s *Server) handleMemStats(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	proj := str(req, "project")

	ls := s.localStore()
	if ls == nil {
		return fail(fmt.Errorf("mem_stats no disponible")), nil
	}

	st, err := ls.Stats(ctx)
	if err != nil {
		return fail(err), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Estado de Kronos\n\n")
	fmt.Fprintf(&sb, "**Observaciones**: %d\n", st.TotalObservations)
	fmt.Fprintf(&sb, "**Sesiones**: %d\n", st.TotalSessions)
	fmt.Fprintf(&sb, "**Prompts**: %d\n", st.TotalPrompts)
	if len(st.Projects) > 0 {
		fmt.Fprintf(&sb, "**Proyectos**: %s\n", strings.Join(st.Projects, ", "))
	}

	if proj != "" {
		relStats, err := ls.GetRelationStats(ctx, proj)
		if err == nil && relStats.Total > 0 {
			fmt.Fprintf(&sb, "\n**Relaciones [%s]**: %d total | %d pendientes | %d juzgadas\n",
				proj, relStats.Total, relStats.Pending, relStats.Judged)
		}
	}

	if d, ok := s.store.(*store.DualStore); ok {
		if pending := d.PendingCount(); pending > 0 {
			fmt.Fprintf(&sb, "\n**Sync pendiente**: %d operaciones sin replicar\n", pending)
		}
	}

	return ok(sb.String()), nil
}

func (s *Server) handleMemCurrentProject(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	dir := str(req, "directory")
	dr := project.DetectFull(dir)

	var sb strings.Builder
	if dr.Error != nil {
		fmt.Fprintf(&sb, "**Error de detección**: %s\n", dr.Error)
		if len(dr.AvailableProjects) > 0 {
			fmt.Fprintf(&sb, "**Proyectos disponibles**: %s\n", strings.Join(dr.AvailableProjects, ", "))
		}
		return ok(sb.String()), nil
	}

	fmt.Fprintf(&sb, "**Proyecto**: %s\n", dr.Project)
	fmt.Fprintf(&sb, "**Fuente**: %s\n", dr.Source)
	fmt.Fprintf(&sb, "**Directorio**: %s\n", dr.Path)
	if dr.Warning != "" {
		fmt.Fprintf(&sb, "**Advertencia**: %s\n", dr.Warning)
	}
	return ok(sb.String()), nil
}

func (s *Server) handleMemCapturePassive(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	content := secrets.Redact(str(req, "content"))
	proj := str(req, "project")
	sessionID := str(req, "session_id")

	obs, err := s.store.SavePassive(ctx, sessionID, proj, content)
	if err != nil {
		return fail(err), nil
	}
	return ok(fmt.Sprintf("Captura pasiva guardada. ID: %d | %s", obs.ID, obs.Title)), nil
}

func (s *Server) handleMemMergeProjects(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	from := str(req, "from")
	to := str(req, "to")
	if from == "" || to == "" {
		return fail(fmt.Errorf("from y to son obligatorios")), nil
	}
	if from == to {
		return fail(fmt.Errorf("from y to deben ser proyectos distintos")), nil
	}

	ls := s.localStore()
	if ls == nil {
		return fail(fmt.Errorf("mem_merge_projects no disponible")), nil
	}

	affected, err := ls.RenameProject(ctx, from, to)
	if err != nil {
		return fail(err), nil
	}
	return ok(fmt.Sprintf("Proyecto \"%s\" fusionado en \"%s\". %d observaciones actualizadas.",
		from, to, affected)), nil
}

func (s *Server) handleMemDoctor(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	ls := s.localStore()

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Diagnóstico Kronos\n\n")

	if ls != nil {
		st, err := ls.Stats(ctx)
		if err != nil {
			fmt.Fprintf(&sb, "**Store**: ERROR — %s\n", err)
		} else {
			fmt.Fprintf(&sb, "**Store**: OK — %d obs, %d sesiones, %d proyectos\n",
				st.TotalObservations, st.TotalSessions, len(st.Projects))
		}

		rels, _ := ls.ListRelations(ctx, "", store.JudgmentPending, 100, 0)
		if len(rels) > 0 {
			fmt.Fprintf(&sb, "**Relaciones pendientes**: %d (usar mem_judge para resolverlas)\n", len(rels))
		} else {
			fmt.Fprintf(&sb, "**Relaciones pendientes**: ninguna\n")
		}
	} else {
		fmt.Fprintf(&sb, "**Store**: no disponible\n")
	}

	if d, ok := s.store.(*store.DualStore); ok {
		if pending := d.PendingCount(); pending > 0 {
			fmt.Fprintf(&sb, "**Sync pendiente**: %d operaciones sin replicar a PostgreSQL\n", pending)
		} else {
			fmt.Fprintf(&sb, "**Sync**: OK\n")
		}
	} else {
		fmt.Fprintf(&sb, "**Backend**: SQLite (sin sync configurado)\n")
	}

	if s.dataDir != "" {
		fmt.Fprintf(&sb, "**Data dir**: %s\n", s.dataDir)
	}

	return ok(sb.String()), nil
}

// ── helpers internos ─────────────────────────────────────────────────────────

// resolveObsSyncID accepts either a numeric ID (string) or a sync_id hex string.
func resolveObsSyncID(ctx context.Context, st *store.Store, idOrSync string) (string, error) {
	if intID, err := strconv.ParseInt(idOrSync, 10, 64); err == nil {
		obs, err := st.GetObservation(ctx, intID)
		if err != nil || obs == nil {
			return "", fmt.Errorf("observación %d no encontrada", intID)
		}
		return obs.SyncID, nil
	}
	return idOrSync, nil
}

// buildTopicKey generates a stable path-like topic key from a title and type.
func buildTopicKey(title, typ string) string {
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true,
		"el": true, "la": true, "los": true, "las": true,
		"de": true, "en": true, "con": true, "por": true,
		"para": true, "que": true, "y": true, "o": true,
	}
	words := strings.Fields(strings.ToLower(title))
	var parts []string
	for _, w := range words {
		w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if w != "" && !stopWords[w] {
			parts = append(parts, w)
		}
		if len(parts) >= 3 {
			break
		}
	}
	slug := strings.Join(parts, "-")
	if slug == "" {
		slug = "general"
	}
	if typ == "" {
		typ = "misc"
	}
	return typ + "/" + slug
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
