package main

// Proxy stdio→daemon para `kronos mcp`. Fase 3 del plan de daemon compartido
// (C:\Users\Jerry\.claude\plans\toasty-hugging-snail.md): en vez de que cada
// sesión de Claude Code levante el stack completo (SQLite, vector store,
// reindex, AutoJudge), `kronos mcp` se conecta al daemon compartido — y lo
// arranca si todavía no existe — y solo reenvía tool calls por stdio.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/mcp"
	mcpgoclient "github.com/mark3labs/mcp-go/client"
	mcptypes "github.com/mark3labs/mcp-go/mcp"
	mcpgoserver "github.com/mark3labs/mcp-go/server"
)

var spawnBackoff = []time.Duration{
	200 * time.Millisecond, 500 * time.Millisecond,
	1 * time.Second, 1 * time.Second, 2 * time.Second, 2 * time.Second,
}

// runMCP es el punto de entrada que Claude Code invoca por sesión.
func runMCP(args ...string) error {
	port := 4317
	toolsFlag := ""
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--port="):
			if n, err := strconv.Atoi(strings.TrimPrefix(a, "--port=")); err == nil && n > 0 {
				port = n
			}
		case strings.HasPrefix(a, "--tools="):
			toolsFlag = strings.TrimPrefix(a, "--tools=")
		}
	}

	bridge := &daemonBridge{
		url:  fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
		port: port,
	}

	ctx := context.Background()
	client, err := bridge.ensureConnected(ctx)
	if err != nil {
		return fmt.Errorf("conectar al daemon de kronos: %w", err)
	}

	listRes, err := client.ListTools(ctx, mcptypes.ListToolsRequest{})
	if err != nil {
		return fmt.Errorf("listar tools del daemon: %w", err)
	}
	// Cachea el schema inicial — bug real encontrado en producción: Claude
	// Code fetchea la lista de tools de ESTE proxy una sola vez, al conectar.
	// Si el daemon se reinicia después con un schema distinto (ej. un
	// parámetro nuevo agregado a un tool), este proxy sigue exponiendo el
	// schema viejo indefinidamente hasta que alguien corre /mcp a mano —
	// sin ningún aviso de que eso hace falta. bridge.toolsHash guarda una
	// huella del schema actual para poder detectar ese drift en cada
	// reconexión (ver checkToolDrift).
	bridge.toolsHash = hashTools(listRes.Tools)

	// toolFilter nil = todos los tools (perfil "all" o sin --tools=).
	toolFilter := mcp.ResolveTools(toolsFlag)
	local := mcpgoserver.NewMCPServer("kronos", version, mcpgoserver.WithToolCapabilities(true))
	for _, t := range listRes.Tools {
		if toolFilter != nil && !toolFilter[t.Name] {
			continue
		}
		local.AddTool(t, bridge.passthroughHandler(t.Name))
	}

	return mcpgoserver.ServeStdio(local)
}

// daemonBridge mantiene la conexión activa hacia el daemon compartido y
// reconecta (relanzando el daemon si hace falta) cuando una llamada falla.
type daemonBridge struct {
	url  string
	port int

	mu        sync.Mutex
	client    *mcpgoclient.Client
	toolsHash string // schema cacheado al conectar — ver checkToolDrift
	warned    bool   // avisar drift una sola vez, no en cada reconexión posterior
}

// ensureConnected devuelve un cliente conectado, reusando el existente si
// sigue vivo (Ping barato) o reconectando/relanzando el daemon si no.
func (b *daemonBridge) ensureConnected(ctx context.Context) (*mcpgoclient.Client, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.client != nil {
		pingCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
		err := b.client.Ping(pingCtx)
		cancel()
		if err == nil {
			return b.client, nil
		}
		_ = b.client.Close()
		b.client = nil
	}

	if c, err := tryConnect(ctx, b.url); err == nil {
		b.client = c
		b.checkToolDrift(ctx, c)
		return c, nil
	}

	if err := spawnDaemonDetached(b.port); err != nil {
		return nil, fmt.Errorf("lanzar daemon: %w", err)
	}

	var lastErr error
	for _, d := range spawnBackoff {
		time.Sleep(d)
		if c, err := tryConnect(ctx, b.url); err == nil {
			b.client = c
			b.checkToolDrift(ctx, c)
			return c, nil
		} else {
			lastErr = err
		}
	}
	return nil, fmt.Errorf("el daemon no respondió tras arrancarlo: %w", lastErr)
}

// checkToolDrift compara el schema de tools del daemon recién (re)conectado
// contra el que este proxy cacheó al arrancar y expuso a Claude Code. Si
// difieren, Claude Code sigue viendo el schema viejo hasta que la sesión
// reconecte — avisamos por stderr en vez de fallar en silencio. Llamar con
// b.mu ya tomado (invocado solo desde ensureConnected).
func (b *daemonBridge) checkToolDrift(ctx context.Context, c *mcpgoclient.Client) {
	if b.toolsHash == "" || b.warned {
		return // sin baseline todavía (primera conexión de runMCP) o ya avisado
	}
	listCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	listRes, err := c.ListTools(listCtx, mcptypes.ListToolsRequest{})
	if err != nil {
		return // best-effort — no bloquear la reconexión por esto
	}
	if newHash := hashTools(listRes.Tools); newHash != b.toolsHash {
		fmt.Fprintln(os.Stderr, "[kronos] el daemon compartido se reinició con un schema de tools distinto al que esta sesión tiene cargado — algún parámetro nuevo puede no estar disponible. Corré /mcp en esta sesión para refrescar.")
		b.warned = true
	}
}

// hashTools arma una huella determinística del schema completo (nombre +
// descripción + parámetros de cada tool) — cualquier cambio real en algún
// tool (ej. agregar el parámetro "directory" a mem_search) cambia el hash,
// sin depender de que alguien se acuerde de bumpear un número de versión.
func hashTools(tools []mcptypes.Tool) string {
	sorted := make([]mcptypes.Tool, len(tools))
	copy(sorted, tools)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	data, err := json.Marshal(sorted)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// callTool reenvía una llamada al daemon, con un reintento automático si la
// conexión falló (ej. el daemon murió a mitad de sesión).
func (b *daemonBridge) callTool(ctx context.Context, req mcptypes.CallToolRequest) (*mcptypes.CallToolResult, error) {
	client, err := b.ensureConnected(ctx)
	if err != nil {
		return nil, err
	}
	res, err := client.CallTool(ctx, req)
	if err == nil {
		return res, nil
	}

	// posible caída del daemon a mitad de sesión: forzar reconexión y
	// reintentar una sola vez antes de devolver el error final.
	b.mu.Lock()
	if b.client != nil {
		_ = b.client.Close()
		b.client = nil
	}
	b.mu.Unlock()

	client, connErr := b.ensureConnected(ctx)
	if connErr != nil {
		return nil, fmt.Errorf("daemon no disponible tras reconectar: %w (error original: %v)", connErr, err)
	}
	return client.CallTool(ctx, req)
}

// passthroughHandler arma un ToolHandlerFunc genérico: no sabe nada del tool
// en sí, solo reenvía nombre + argumentos tal cual al daemon. Agregar un
// tool nuevo en el daemon nunca requiere tocar el proxy.
func (b *daemonBridge) passthroughHandler(name string) mcpgoserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcptypes.CallToolRequest) (*mcptypes.CallToolResult, error) {
		req.Params.Name = name
		return b.callTool(ctx, req)
	}
}

func tryConnect(ctx context.Context, daemonURL string) (*mcpgoclient.Client, error) {
	c, err := mcpgoclient.NewStreamableHttpClient(daemonURL)
	if err != nil {
		return nil, err
	}
	if err := c.Start(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}

	initCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	initReq := mcptypes.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcptypes.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcptypes.Implementation{Name: "kronos-mcp-proxy", Version: version}
	if _, err := c.Initialize(initCtx, initReq); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// spawnDaemonDetached vive en cmd/kronos/detach_windows.go y detach_other.go
// (build tags) — el mecanismo de detach de proceso es específico de cada SO.
