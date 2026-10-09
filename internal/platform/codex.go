package platform

import (
	"os"
	"path/filepath"
)

// CodexDir returns the directory Codex CLI uses for its config/hooks/MCP
// registration (hooks.json, config.toml). Codex (and Orca's per-account
// Codex wrapper) honors $CODEX_HOME when set — en máquinas con múltiples
// cuentas de Codex gestionadas por Orca, cada una tiene su propio
// CODEX_HOME (ver ~/.config/orca/codex-accounts/<id>/home) y NO es
// ~/.codex. Sin este chequeo, kronos setup codex escribiría en el home
// equivocado cuando corre dentro de una sesión de Orca.
func CodexDir() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}
