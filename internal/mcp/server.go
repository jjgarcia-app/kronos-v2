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
	store    *store.Store
	activity *Activity
	mcp      *server.MCPServer
	rel      *relations.Detector // nil when embeddings are disabled
}

// New crea un Server listo para ser servido via stdio o HTTP.
func New(st *store.Store, nudgeActions, nudgeFallbackMins int) *Server {
	return NewWithRelations(st, nudgeActions, nudgeFallbackMins, nil)
}

// NewWithRelations crea un Server con soporte opcional de relations/embeddings.
func NewWithRelations(st *store.Store, nudgeActions, nudgeFallbackMins int, rel *relations.Detector) *Server {
	s := &Server{
		store:    st,
		activity: NewActivity(nudgeActions, nudgeFallbackMins),
		rel:      rel,
	}

	s.mcp = server.NewMCPServer("kronos", "2.0.0",
		server.WithToolCapabilities(true),
	)

	s.registerTools()
	return s
}


// ServeStdio arranca el servidor MCP sobre stdin/stdout (modo Claude Code).
func (s *Server) ServeStdio() error {
	return server.ServeStdio(s.mcp)
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
	default:
		return nil, fmt.Errorf("tool desconocido: %s", tool)
	}
	return handler(ctx, req)
}

func (s *Server) registerTools() {
	s.mcp.AddTool(toolMemSave(), s.handleMemSave)
	s.mcp.AddTool(toolMemSearch(), s.handleMemSearch)
	s.mcp.AddTool(toolMemContext(), s.handleMemContext)
	s.mcp.AddTool(toolMemGetObservation(), s.handleMemGetObservation)
	s.mcp.AddTool(toolMemUpdate(), s.handleMemUpdate)
	s.mcp.AddTool(toolMemSessionStart(), s.handleMemSessionStart)
	s.mcp.AddTool(toolMemSessionEnd(), s.handleMemSessionEnd)
	s.mcp.AddTool(toolMemSessionSummary(), s.handleMemSessionSummary)
	s.mcp.AddTool(toolMemSavePrompt(), s.handleMemSavePrompt)
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
