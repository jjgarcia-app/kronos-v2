package main

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	mcptypes "github.com/mark3labs/mcp-go/mcp"
	mcpgoserver "github.com/mark3labs/mcp-go/server"
)

// --- hashTools: unit tests puros, sin red ---

func TestHashTools_DifferentSchemas_ProduceDifferentHashes(t *testing.T) {
	a := []mcptypes.Tool{mcptypes.NewTool("foo", mcptypes.WithString("x"))}
	b := []mcptypes.Tool{mcptypes.NewTool("foo", mcptypes.WithString("x"), mcptypes.WithString("y"))}

	ha, hb := hashTools(a), hashTools(b)
	if ha == "" || hb == "" {
		t.Fatal("hashTools no debería devolver string vacío para tools válidos")
	}
	if ha == hb {
		t.Error("dos schemas distintos (parámetro nuevo agregado) deberían producir hashes distintos")
	}
}

func TestHashTools_SameSchema_OrderIndependent(t *testing.T) {
	x := mcptypes.NewTool("x")
	y := mcptypes.NewTool("y")

	h1 := hashTools([]mcptypes.Tool{x, y})
	h2 := hashTools([]mcptypes.Tool{y, x})
	if h1 != h2 {
		t.Error("hashTools debería ser independiente del orden de la lista (mismo set de tools)")
	}
}

// --- checkToolDrift: integración real contra dos servers MCP con schemas
// distintos, simulando el bug real: el daemon se reinicia con un tool
// cambiado y el proxy ya conectado no se entera hasta reconectar ---

func newFakeMCPServer(t *testing.T, tools ...mcptypes.Tool) *httptest.Server {
	t.Helper()
	srv := mcpgoserver.NewMCPServer("fake", "1.0", mcpgoserver.WithToolCapabilities(true))
	noop := func(ctx context.Context, req mcptypes.CallToolRequest) (*mcptypes.CallToolResult, error) {
		return mcptypes.NewToolResultText("ok"), nil
	}
	for _, tl := range tools {
		srv.AddTool(tl, noop)
	}
	streamable := mcpgoserver.NewStreamableHTTPServer(srv)
	ts := httptest.NewServer(streamable)
	t.Cleanup(ts.Close)
	return ts
}

func TestCheckToolDrift_WarnsWhenSchemaChangedOnReconnect(t *testing.T) {
	oldSchema := newFakeMCPServer(t, mcptypes.NewTool("mem_search", mcptypes.WithString("query")))
	newSchema := newFakeMCPServer(t, mcptypes.NewTool("mem_search", mcptypes.WithString("query"), mcptypes.WithString("directory")))

	bridge := &daemonBridge{url: oldSchema.URL}
	ctx := context.Background()

	client, err := bridge.ensureConnected(ctx)
	if err != nil {
		t.Fatalf("primera conexión: %v", err)
	}
	listRes, err := client.ListTools(ctx, mcptypes.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools inicial: %v", err)
	}
	bridge.toolsHash = hashTools(listRes.Tools)

	// simula: el daemon murió y un daemon NUEVO (con schema distinto)
	// arrancó en su lugar — mismo mecanismo real (proceso reemplazado,
	// mismo puerto lógico).
	_ = bridge.client.Close()
	bridge.client = nil
	bridge.url = newSchema.URL

	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	_, err = bridge.ensureConnected(ctx)
	w.Close()
	os.Stderr = origStderr
	if err != nil {
		t.Fatalf("reconexión: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "/mcp") {
		t.Errorf("esperaba un aviso sugiriendo /mcp en stderr tras detectar drift, got: %q", buf.String())
	}
	if !bridge.warned {
		t.Error("bridge.warned debería quedar en true tras detectar el drift")
	}
}

func TestCheckToolDrift_NoWarningWhenSchemaUnchanged(t *testing.T) {
	schema := newFakeMCPServer(t, mcptypes.NewTool("mem_search", mcptypes.WithString("query")))

	bridge := &daemonBridge{url: schema.URL}
	ctx := context.Background()

	client, err := bridge.ensureConnected(ctx)
	if err != nil {
		t.Fatalf("primera conexión: %v", err)
	}
	listRes, err := client.ListTools(ctx, mcptypes.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools inicial: %v", err)
	}
	bridge.toolsHash = hashTools(listRes.Tools)

	_ = bridge.client.Close()
	bridge.client = nil

	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	_, err = bridge.ensureConnected(ctx)
	w.Close()
	os.Stderr = origStderr
	if err != nil {
		t.Fatalf("reconexión: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)
	if buf.Len() != 0 {
		t.Errorf("no debería haber ningún aviso cuando el schema no cambió, got: %q", buf.String())
	}
	if bridge.warned {
		t.Error("bridge.warned no debería activarse sin drift real")
	}
}
