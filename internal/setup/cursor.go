package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// InstallCursor registers Kronos as an MCP server in Cursor (~/.cursor/mcp.json).
func InstallCursor() error {
	dir, err := cursorConfigDir()
	if err != nil {
		return err
	}
	return installMCPServer(filepath.Join(dir, "mcp.json"), "Cursor")
}

// UninstallCursor removes Kronos from Cursor's MCP config.
func UninstallCursor() error {
	dir, err := cursorConfigDir()
	if err != nil {
		return err
	}
	return removeMCPServer(filepath.Join(dir, "mcp.json"), "Cursor")
}

func cursorConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cursor"), nil
}

func installMCPServer(configPath, agentName string) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return err
	}
	cfg, err := loadMCPConfig(configPath)
	if err != nil {
		return fmt.Errorf("load %s MCP config: %w", agentName, err)
	}

	servers := mcpServers(cfg)
	if _, exists := servers["kronos"]; exists {
		fmt.Printf("Kronos ya está registrado en %s — sin cambios.\n", agentName)
		return nil
	}

	servers["kronos"] = map[string]any{
		"command": kronosBinary(),
		"args":    []string{"serve"},
	}
	cfg["mcpServers"] = servers

	if err := saveMCPConfig(configPath, cfg); err != nil {
		return fmt.Errorf("save %s MCP config: %w", agentName, err)
	}
	fmt.Printf("Kronos registrado como MCP server en %s (%s)\n", agentName, configPath)
	return nil
}

func removeMCPServer(configPath, agentName string) error {
	cfg, err := loadMCPConfig(configPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	servers := mcpServers(cfg)
	delete(servers, "kronos")
	cfg["mcpServers"] = servers
	if err := saveMCPConfig(configPath, cfg); err != nil {
		return fmt.Errorf("save %s MCP config: %w", agentName, err)
	}
	fmt.Printf("Kronos eliminado de %s MCP config.\n", agentName)
	return nil
}

func mcpServers(cfg map[string]any) map[string]any {
	if s, ok := cfg["mcpServers"].(map[string]any); ok {
		return s
	}
	s := map[string]any{}
	cfg["mcpServers"] = s
	return s
}

func loadMCPConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

func saveMCPConfig(path string, cfg map[string]any) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func kronosBinary() string {
	if runtime.GOOS == "windows" {
		return "kronos.exe"
	}
	return "kronos"
}
