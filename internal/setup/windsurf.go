package setup

import (
	"os"
	"path/filepath"
)

// InstallWindsurf registers Kronos as an MCP server in Windsurf.
// Config: ~/.codeium/windsurf/mcp_config.json
func InstallWindsurf() error {
	dir, err := windsurfConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return installMCPServer(filepath.Join(dir, "mcp_config.json"), "Windsurf")
}

// UninstallWindsurf removes Kronos from Windsurf's MCP config.
func UninstallWindsurf() error {
	dir, err := windsurfConfigDir()
	if err != nil {
		return err
	}
	return removeMCPServer(filepath.Join(dir, "mcp_config.json"), "Windsurf")
}

func windsurfConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codeium", "windsurf"), nil
}
