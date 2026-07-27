package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DataDir returns the OS-specific data directory for Kronos.
//   - Windows: %LOCALAPPDATA%\kronos
//   - macOS:   ~/Library/Application Support/kronos
//   - Linux:   $XDG_DATA_HOME/kronos  or  ~/.local/share/kronos
func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "windows":
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, "kronos"), nil
		}
		return filepath.Join(home, "AppData", "Local", "kronos"), nil
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "kronos"), nil
	default:
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return filepath.Join(xdg, "kronos"), nil
		}
		return filepath.Join(home, ".local", "share", "kronos"), nil
	}
}

// DBPath returns the full path to the Kronos SQLite database.
func DBPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kronos.db"), nil
}

// ConfigDir returns the OS-specific config directory for Kronos.
//   - Windows: %APPDATA%\kronos
//   - macOS:   ~/Library/Application Support/kronos
//   - Linux:   $XDG_CONFIG_HOME/kronos  or  ~/.config/kronos
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "kronos"), nil
		}
		return filepath.Join(home, "AppData", "Roaming", "kronos"), nil
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "kronos"), nil
	default:
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "kronos"), nil
		}
		return filepath.Join(home, ".config", "kronos"), nil
	}
}

// ClaudeDir returns the directory where Claude Code stores its configuration.
// Used for hooks installation (hooks.json lives here).
func ClaudeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// ClaudeMCPFile returns the path to ~/.claude.json where Claude Code stores
// user-level MCP server definitions (flat map, not nested under "mcpServers").
func ClaudeMCPFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude.json"), nil
}

// CurrentSessionPath returns the path to the file that stores the active
// session ID for a given project. Written by session-start, cleared by
// session-stop, read by the MCP server as a session_id fallback.
//
// Namespaced por proyecto (no un único archivo global): con varias sesiones
// de Claude Code concurrentes en distintos proyectos, un archivo global hacía
// que la última sesión en arrancar pisara el puntero de todas las demás —
// mem_search terminaba contando búsquedas para la sesión equivocada. No es
// una solución perfecta (dos sesiones en el MISMO proyecto todavía compiten),
// pero cubre el caso común y no requiere rediseñar el transporte MCP.
//
// project == "" cae al path histórico sin namespacing, para callers que no
// tienen contexto de proyecto disponible (ej. mem_search sin "project").
func CurrentSessionPath(project string) (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	if project == "" {
		return filepath.Join(dir, "current_session.txt"), nil
	}
	safe := sanitizeForFilename(project)
	return filepath.Join(dir, "sessions", "current_session_"+safe+".txt"), nil
}

// sanitizeForFilename reemplaza caracteres no seguros para nombre de archivo
// por "-". No pretende ser una normalización canónica (eso es project.normalize,
// en otro paquete) — solo evitar separadores de path u otros caracteres inválidos.
func sanitizeForFilename(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unknown"
	}
	return out
}

// OS returns the current operating system identifier.
// Values: "windows", "darwin", "linux", or the GOOS value for other systems.
func OS() string {
	return runtime.GOOS
}
