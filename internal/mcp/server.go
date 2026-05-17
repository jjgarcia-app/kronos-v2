package mcp

import (
	"context"
	"fmt"

	"github.com/jjgarcia-app/kronos-v2/internal/relations"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Server es el MCP server de Kronos. Expone los tools de memoria a Claude Code.
type Server struct {
	store      store.Storer
	activity   *Activity
	mcp        *server.MCPServer
	rel        *relations.Detector // nil when embeddings are disabled
	dataDir    string              // directorio de datos para checkpoints
	toolFilter map[string]bool     // nil = registrar todos; non-nil = solo los listados
}

// New crea un Server listo para ser servido via stdio o HTTP.
func New(st store.Storer, nudgeActions, nudgeFallbackMins int) *Server {
	return NewWithRelations(st, nudgeActions, nudgeFallbackMins, nil)
}

// NewWithRelations crea un Server con soporte opcional de relations/embeddings.
func NewWithRelations(st store.Storer, nudgeActions, nudgeFallbackMins int, rel *relations.Detector) *Server {
	return NewWithOptions(st, nudgeActions, nudgeFallbackMins, rel, nil)
}

// NewWithOptions crea un Server con todas las opciones configurables.
// toolFilter nil = registrar todos los tools; non-nil = solo los listados.
func NewWithOptions(st store.Storer, nudgeActions, nudgeFallbackMins int, rel *relations.Detector, toolFilter map[string]bool) *Server {
	s := &Server{
		store:      st,
		activity:   NewActivity(nudgeActions, nudgeFallbackMins),
		rel:        rel,
		toolFilter: toolFilter,
	}

	s.mcp = server.NewMCPServer("kronos", "2.0.0",
		server.WithToolCapabilities(true),
	)

	s.registerTools()
	return s
}

// SetDataDir configura el directorio de datos para persistir checkpoints.
// Llamar antes de ServeStdio.
func (s *Server) SetDataDir(dir string) *Server {
	s.dataDir = dir
	return s
}


// ServeStdio arranca el servidor MCP sobre stdin/stdout (modo Claude Code).
func (s *Server) ServeStdio() error {
	return server.ServeStdio(s.mcp)
}

// localStorer is a private interface to access the underlying SQLite store
// for operations that are always local (relations, stats, rename).
type localStorer interface {
	LocalStore() *store.Store
}

// localStore returns the underlying *store.Store for local-only operations.
// Works for both *store.Store and *store.DualStore.
func (s *Server) localStore() *store.Store {
	if ls, ok := s.store.(localStorer); ok {
		return ls.LocalStore()
	}
	if st, ok := s.store.(*store.Store); ok {
		return st
	}
	return nil
}

// Call invoca un handler de tool directamente — usado en tests.
func (s *Server) Call(ctx context.Context, tool string, arguments map[string]any) (*mcpgo.CallToolResult, error) {
	req := mcpgo.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = arguments

	var handler func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error)
	switch tool {
	case "mem_save":
		handler = s.handleMemSave
	case "mem_search":
		handler = s.handleMemSearch
	case "mem_context":
		handler = s.handleMemContext
	case "mem_get_observation":
		handler = s.handleMemGetObservation
	case "mem_update":
		handler = s.handleMemUpdate
	case "mem_session_start":
		handler = s.handleMemSessionStart
	case "mem_session_end":
		handler = s.handleMemSessionEnd
	case "mem_session_summary":
		handler = s.handleMemSessionSummary
	case "mem_save_prompt":
		handler = s.handleMemSavePrompt
	case "mem_delete":
		handler = s.handleMemDelete
	case "mem_checkpoint":
		handler = s.handleMemCheckpoint
	case "mem_judge":
		handler = s.handleMemJudge
	case "mem_compare":
		handler = s.handleMemCompare
	case "mem_suggest_topic_key":
		handler = s.handleMemSuggestTopicKey
	case "mem_timeline":
		handler = s.handleMemTimeline
	case "mem_stats":
		handler = s.handleMemStats
	case "mem_current_project":
		handler = s.handleMemCurrentProject
	case "mem_capture_passive":
		handler = s.handleMemCapturePassive
	case "mem_merge_projects":
		handler = s.handleMemMergeProjects
	case "mem_doctor":
		handler = s.handleMemDoctor
	default:
		return nil, fmt.Errorf("tool desconocido: %s", tool)
	}
	return handler(ctx, req)
}

func (s *Server) registerTools() {
	type entry struct {
		tool    func() mcpgo.Tool
		handler func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error)
	}
	all := []entry{
		{toolMemSave, s.handleMemSave},
		{toolMemSearch, s.handleMemSearch},
		{toolMemContext, s.handleMemContext},
		{toolMemGetObservation, s.handleMemGetObservation},
		{toolMemUpdate, s.handleMemUpdate},
		{toolMemSessionStart, s.handleMemSessionStart},
		{toolMemSessionEnd, s.handleMemSessionEnd},
		{toolMemSessionSummary, s.handleMemSessionSummary},
		{toolMemSavePrompt, s.handleMemSavePrompt},
		{toolMemDelete, s.handleMemDelete},
		{toolMemCheckpoint, s.handleMemCheckpoint},
		{toolMemJudge, s.handleMemJudge},
		{toolMemCompare, s.handleMemCompare},
		{toolMemSuggestTopicKey, s.handleMemSuggestTopicKey},
		{toolMemTimeline, s.handleMemTimeline},
		{toolMemStats, s.handleMemStats},
		{toolMemCurrentProject, s.handleMemCurrentProject},
		{toolMemCapturePassive, s.handleMemCapturePassive},
		{toolMemMergeProjects, s.handleMemMergeProjects},
		{toolMemDoctor, s.handleMemDoctor},
	}
	for _, e := range all {
		t := e.tool()
		if s.toolFilter == nil || s.toolFilter[t.Name] {
			s.mcp.AddTool(t, e.handler)
		}
	}
}

// helpers

func ok(text string) *mcpgo.CallToolResult {
	return mcpgo.NewToolResultText(text)
}

func fail(err error) *mcpgo.CallToolResult {
	return mcpgo.NewToolResultError(err.Error())
}

func args(req mcpgo.CallToolRequest) map[string]any {
	m, _ := req.Params.Arguments.(map[string]any)
	return m
}

func str(req mcpgo.CallToolRequest, key string) string {
	v, _ := args(req)[key].(string)
	return v
}

func strOr(req mcpgo.CallToolRequest, key, def string) string {
	if v := str(req, key); v != "" {
		return v
	}
	return def
}

func intOr(req mcpgo.CallToolRequest, key string, def int) int {
	switch v := args(req)[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

var _ = fmt.Sprintf // evitar unused
var _ = context.Background
