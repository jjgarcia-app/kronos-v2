package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// Server expone la memoria de Kronos via HTTP REST local.
type Server struct {
	st   store.Storer
	port int
	mux  *http.ServeMux
}

// New crea un Server listo para arrancar.
func New(st store.Storer, port int) *Server {
	if port <= 0 {
		port = 4317
	}
	srv := &Server{
		st:   st,
		port: port,
		mux:  http.NewServeMux(),
	}
	srv.routes()
	return srv
}

// Start arranca el servidor HTTP en background (no bloqueante).
func (srv *Server) Start() error {
	httpSrv := &http.Server{
		Addr:    srv.Addr(),
		Handler: srv.mux,
	}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("kronos http server error: %v\n", err)
		}
	}()
	return nil
}

// Addr retorna la dirección de escucha, ej: ":4317".
func (srv *Server) Addr() string {
	return fmt.Sprintf(":%d", srv.port)
}

// routes registra todos los endpoints.
func (srv *Server) routes() {
	srv.mux.HandleFunc("/health", srv.handleHealth)
	srv.mux.HandleFunc("/stats", srv.handleStats)

	// Sessions
	srv.mux.HandleFunc("/sessions", srv.handleSessions)
	srv.mux.HandleFunc("/sessions/", srv.handleSessionsPath)

	// Observations
	srv.mux.HandleFunc("/observations", srv.handleObservations)
	srv.mux.HandleFunc("/observations/", srv.handleObservationsPath)

	// Search & Context
	srv.mux.HandleFunc("/search", srv.handleSearch)
	srv.mux.HandleFunc("/context", srv.handleContext)

	// Conflicts
	srv.mux.HandleFunc("/conflicts", srv.handleConflicts)
	srv.mux.HandleFunc("/conflicts/", srv.handleConflictsPath)

	// Export/Import
	srv.mux.HandleFunc("/export", srv.handleExport)
	srv.mux.HandleFunc("/import", srv.handleImport)

	// Project
	srv.mux.HandleFunc("/project/current", srv.handleProjectCurrent)
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// lister es una interfaz interna para exportar todas las observaciones.
type lister interface {
	ListAll(ctx context.Context, project string) ([]*store.Observation, error)
}

// sqliteStore retorna el *store.Store subyacente para operaciones que no están
// en la interfaz Storer (Stats, ListRelations, etc.).
// Para DualStore devuelve el LocalStore (SQLite buffer).
func (srv *Server) sqliteStore() *store.Store {
	if s, ok := srv.st.(*store.Store); ok {
		return s
	}
	if d, ok := srv.st.(*store.DualStore); ok {
		return d.LocalStore()
	}
	return nil
}

// ---------- /health ----------

func (srv *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok","service":"kronos"}`))
}

// ---------- /stats ----------

func (srv *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := context.Background()

	s := srv.sqliteStore()
	if s == nil {
		writeError(w, http.StatusNotImplemented, "stats solo disponible en backend SQLite directo")
		return
	}

	st, err := s.Stats(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions":     st.TotalSessions,
		"observations": st.TotalObservations,
		"prompts":      st.TotalPrompts,
		"projects":     st.Projects,
	})
}

// ---------- /sessions ----------

func (srv *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	switch r.Method {
	case http.MethodPost:
		var body struct {
			ID        string `json:"id"`
			Project   string `json:"project"`
			Directory string `json:"directory"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if body.ID == "" || body.Project == "" {
			writeError(w, http.StatusBadRequest, "id and project are required")
			return
		}
		sess, err := srv.st.CreateSession(ctx, body.ID, body.Project, body.Directory)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, sess)

	case http.MethodGet:
		proj := r.URL.Query().Get("project")
		limit := queryInt(r, "limit", 20)
		sessions, err := srv.st.ListSessions(ctx, proj, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, sessions)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleSessionsPath despacha /sessions/{id} y /sessions/{id}/end
func (srv *Server) handleSessionsPath(w http.ResponseWriter, r *http.Request) {
	// strip /sessions/
	path := strings.TrimPrefix(r.URL.Path, "/sessions/")
	if path == "" {
		srv.handleSessions(w, r)
		return
	}

	// /sessions/recent?project=X&limit=N
	if path == "recent" {
		srv.handleSessionsRecent(w, r)
		return
	}

	// /sessions/{id}/end
	if strings.HasSuffix(path, "/end") {
		id := strings.TrimSuffix(path, "/end")
		srv.handleSessionEnd(w, r, id)
		return
	}

	// /sessions/{id}
	srv.handleSessionGet(w, r, path)
}

func (srv *Server) handleSessionsRecent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := context.Background()
	proj := r.URL.Query().Get("project")
	limit := queryInt(r, "limit", 20)
	sessions, err := srv.st.ListSessions(ctx, proj, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (srv *Server) handleSessionGet(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := context.Background()
	sess, err := srv.st.GetSession(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (srv *Server) handleSessionEnd(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := context.Background()
	var body struct {
		Summary string `json:"summary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := srv.st.EndSession(ctx, id, body.Summary); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sess, _ := srv.st.GetSession(ctx, id)
	writeJSON(w, http.StatusOK, sess)
}

// ---------- /observations ----------

func (srv *Server) handleObservations(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	switch r.Method {
	case http.MethodPost:
		var p store.SaveParams
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		obs, err := srv.st.SaveObservation(ctx, p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, obs)

	case http.MethodGet:
		proj := r.URL.Query().Get("project")
		limit := queryInt(r, "limit", 50)
		obss, err := srv.st.ListObservations(ctx, proj, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, obss)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleObservationsPath despacha /observations/{id}, PATCH, DELETE
func (srv *Server) handleObservationsPath(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	path := strings.TrimPrefix(r.URL.Path, "/observations/")
	if path == "" {
		srv.handleObservations(w, r)
		return
	}

	id, err := strconv.ParseInt(path, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid observation id")
		return
	}

	switch r.Method {
	case http.MethodGet:
		obs, err := srv.st.GetObservation(ctx, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if obs == nil {
			writeError(w, http.StatusNotFound, "observation not found")
			return
		}
		writeJSON(w, http.StatusOK, obs)

	case http.MethodPatch:
		var body struct {
			Title   *string              `json:"title"`
			Content *string              `json:"content"`
			Type    *store.ObservationType `json:"type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		updated, err := srv.st.UpdateObservation(ctx, store.UpdateParams{
			ID:      id,
			Title:   body.Title,
			Content: body.Content,
			Type:    body.Type,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, updated)

	case http.MethodDelete:
		hard := r.URL.Query().Get("hard") == "true"
		if hard {
			// hard delete: usa el método estándar (soft) — no hay hard delete en la interfaz
			// enviamos soft igual para no romper la interfaz
		}
		if err := srv.st.DeleteObservation(ctx, id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ---------- /search ----------

func (srv *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := context.Background()
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	proj := r.URL.Query().Get("project")
	limit := queryInt(r, "limit", 20)

	results, err := srv.st.Search(ctx, store.SearchParams{
		Query:   q,
		Project: proj,
		Limit:   limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, results)
}

// ---------- /context ----------

func (srv *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := context.Background()
	proj := r.URL.Query().Get("project")
	limit := queryInt(r, "limit", 20)

	obss, err := srv.st.ListObservations(ctx, proj, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, obss)
}

// ---------- /conflicts ----------

func (srv *Server) handleConflicts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s := srv.sqliteStore()
	if s == nil {
		writeError(w, http.StatusNotImplemented, "conflicts solo disponible en backend SQLite")
		return
	}
	ctx := context.Background()
	proj := r.URL.Query().Get("project")
	status := r.URL.Query().Get("status")
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)

	rels, err := s.ListRelations(ctx, proj, status, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rels)
}

// handleConflictsPath despacha /conflicts/stats y /conflicts/judge
func (srv *Server) handleConflictsPath(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/conflicts/")
	switch path {
	case "stats":
		srv.handleConflictsStats(w, r)
	case "judge":
		srv.handleConflictsJudge(w, r)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (srv *Server) handleConflictsStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s := srv.sqliteStore()
	if s == nil {
		writeError(w, http.StatusNotImplemented, "conflicts solo disponible en backend SQLite")
		return
	}
	ctx := context.Background()
	proj := r.URL.Query().Get("project")
	stats, err := s.GetRelationStats(ctx, proj)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (srv *Server) handleConflictsJudge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s := srv.sqliteStore()
	if s == nil {
		writeError(w, http.StatusNotImplemented, "conflicts solo disponible en backend SQLite")
		return
	}
	ctx := context.Background()
	var body struct {
		JudgmentID int64   `json:"judgment_id"`
		Relation   string  `json:"relation"`
		Reason     string  `json:"reason"`
		Evidence   string  `json:"evidence"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	rel, err := s.JudgeRelation(ctx, store.JudgeRelationParams{
		JudgmentID: body.JudgmentID,
		Relation:   body.Relation,
		Reason:     body.Reason,
		Evidence:   body.Evidence,
		Confidence: body.Confidence,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rel)
}

// ---------- /export ----------

func (srv *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := context.Background()
	proj := r.URL.Query().Get("project")

	ls, ok := srv.st.(lister)
	if !ok {
		writeError(w, http.StatusNotImplemented, "export no disponible en este backend")
		return
	}
	obss, err := ls.ListAll(ctx, proj)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, obss)
}

// ---------- /import ----------

func (srv *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := context.Background()
	var items []store.SaveParams
	if err := json.NewDecoder(r.Body).Decode(&items); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON array: "+err.Error())
		return
	}
	var imported int
	var errs []string
	for _, p := range items {
		if _, err := srv.st.SaveObservation(ctx, p); err != nil {
			errs = append(errs, err.Error())
		} else {
			imported++
		}
	}
	result := map[string]any{"imported": imported}
	if len(errs) > 0 {
		result["errors"] = errs
	}
	writeJSON(w, http.StatusOK, result)
}

// ---------- /project/current ----------

func (srv *Server) handleProjectCurrent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cwd := r.URL.Query().Get("cwd")
	dr := project.DetectFull(cwd)

	result := map[string]any{
		"project": dr.Project,
		"source":  dr.Source,
		"path":    dr.Path,
	}
	if dr.Warning != "" {
		result["warning"] = dr.Warning
	}
	if dr.Error != nil {
		result["error"] = dr.Error.Error()
		if len(dr.AvailableProjects) > 0 {
			result["available_projects"] = dr.AvailableProjects
		}
	}
	writeJSON(w, http.StatusOK, result)
}
