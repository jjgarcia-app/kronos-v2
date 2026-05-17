package main

import (
	"fmt"

	"github.com/jjgarcia-app/kronos-v2/internal/setup"
)

func runSetup(args []string) error {
	target := "claude-code"
	if len(args) > 0 {
		target = args[0]
	}

	switch target {
	case "claude-code":
		return setup.InstallClaudeCode()
	case "cursor":
		return setup.InstallCursor()
	case "windsurf":
		return setup.InstallWindsurf()
	case "--all":
		for _, fn := range []func() error{
			setup.InstallClaudeCode,
			setup.InstallCursor,
			setup.InstallWindsurf,
		} {
			if err := fn(); err != nil {
				fmt.Printf("advertencia: %v\n", err)
			}
		}
		return nil
	case "uninstall":
		_ = setup.Uninstall()
		_ = setup.UninstallCursor()
		_ = setup.UninstallWindsurf()
		return nil
	case "--list":
		fmt.Println("Agentes soportados:")
		fmt.Println("  claude-code  — hooks en ~/.claude/settings.json")
		fmt.Println("  cursor       — MCP server en ~/.cursor/mcp.json")
		fmt.Println("  windsurf     — MCP server en ~/.codeium/windsurf/mcp_config.json")
		fmt.Println("  --all        — instalar en todos")
		fmt.Println("  uninstall    — desinstalar de todos")
		return nil
	default:
		return fmt.Errorf("agente desconocido: %s\nUsa: kronos setup --list", target)
	}
}
