package project

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrAmbiguousProject is returned when multiple child git repositories are found
// and no config specifies which one to use.
var ErrAmbiguousProject = errors.New("multiple child git repositories — specify project via .kronos/config.json")

type Method string

const (
	MethodConfig    Method = "config"     // kronos.toml or .kronos/config.json [project] name
	MethodGitRemote Method = "git_remote" // nombre del repo desde git remote
	MethodGitRoot   Method = "git_root"   // basename del directorio git root
	MethodSingle    Method = "git_child"  // único sub-repo en el directorio
	MethodDirname   Method = "dir_basename"
	MethodAmbiguous Method = "ambiguous"
)

// noiseDir are directories that should not be considered as child git repos.
var noiseDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	".venv":        true,
	"target":       true,
	"dist":         true,
	"__pycache__":  true,
	".git":         true,
}

// Result is the legacy detection result (backward compatible).
type Result struct {
	Name   string
	Method Method
}

// DetectionResult is the full detection result with additional context.
type DetectionResult struct {
	Project           string   // vacío si ambiguous
	Source            string   // git_remote | git_root | git_child | ambiguous | dir_basename | config
	Path              string   // directorio canónico del repo
	Warning           string   // "auto-promoted child repository" si git_child
	Error             error    // ErrAmbiguousProject si múltiples children
	AvailableProjects []string // si ambiguous
}

// Detect determina el nombre del proyecto para un directorio de trabajo.
// Aplica el algoritmo de 6 casos en orden de prioridad.
// Backward compatible: delegates to DetectFull internally.
func Detect(cwd string) Result {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return Result{Name: "unknown", Method: MethodDirname}
		}
	}

	dr := DetectFull(cwd)

	if dr.Error == ErrAmbiguousProject {
		return Result{Name: "unknown", Method: MethodAmbiguous}
	}

	name := dr.Project
	if name == "" {
		name = "unknown"
	}
	return Result{Name: name, Method: Method(dr.Source)}
}

// DetectFull determina el proyecto con información completa incluyendo advertencias y errores.
func DetectFull(cwd string) DetectionResult {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return DetectionResult{Project: "unknown", Source: string(MethodDirname), Path: cwd}
		}
	}

	absPath, err := filepath.Abs(cwd)
	if err != nil {
		absPath = cwd
	}

	// 1. .kronos/config.json explícito
	if name := fromKronosConfig(absPath); name != "" {
		return DetectionResult{
			Project: normalize(name),
			Source:  string(MethodConfig),
			Path:    absPath,
		}
	}

	// 2. kronos.toml explícito
	if name := fromConfig(absPath); name != "" {
		return DetectionResult{
			Project: normalize(name),
			Source:  string(MethodConfig),
			Path:    absPath,
		}
	}

	// 3. git remote origin → nombre del repo
	if name := fromGitRemote(absPath); name != "" {
		return DetectionResult{
			Project: normalize(name),
			Source:  string(MethodGitRemote),
			Path:    absPath,
		}
	}

	// 4. basename del git root
	if name := fromGitRoot(absPath); name != "" {
		return DetectionResult{
			Project: normalize(name),
			Source:  string(MethodGitRoot),
			Path:    absPath,
		}
	}

	// 5. único sub-repo (o ambiguous) dentro del directorio
	children, gitPaths := findChildRepos(absPath)
	if len(children) == 1 {
		return DetectionResult{
			Project: normalize(children[0]),
			Source:  string(MethodSingle),
			Path:    gitPaths[0],
			Warning: "auto-promoted child repository",
		}
	}
	if len(children) > 1 {
		return DetectionResult{
			Project:           "",
			Source:            string(MethodAmbiguous),
			Path:              absPath,
			Error:             ErrAmbiguousProject,
			AvailableProjects: children,
		}
	}

	// 6. fallback: basename del cwd
	return DetectionResult{
		Project: normalize(filepath.Base(absPath)),
		Source:  string(MethodDirname),
		Path:    absPath,
	}
}

// fromKronosConfig reads .kronos/config.json and returns project_name if valid.
func fromKronosConfig(cwd string) string {
	dir := cwd
	for i := 0; i < 4; i++ {
		path := filepath.Join(dir, ".kronos", "config.json")
		data, err := os.ReadFile(path)
		if err == nil {
			var cfg struct {
				ProjectName string `json:"project_name"`
			}
			if jsonErr := json.Unmarshal(data, &cfg); jsonErr == nil {
				name := cfg.ProjectName
				if isValidProjectName(name) {
					return name
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// isValidProjectName checks that a project name is non-empty, has no control chars
// and no path separators.
func isValidProjectName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r < 32 || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func fromConfig(cwd string) string {
	// busca kronos.toml en el cwd y directorios padre (hasta 4 niveles)
	dir := cwd
	for i := 0; i < 4; i++ {
		path := filepath.Join(dir, "kronos.toml")
		data, err := os.ReadFile(path)
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "name") {
					parts := strings.SplitN(line, "=", 2)
					if len(parts) == 2 {
						return strings.Trim(strings.TrimSpace(parts[1]), `"'`)
					}
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func fromGitRemote(cwd string) string {
	out := gitCmd(cwd, "remote", "get-url", "origin")
	if out == "" {
		return ""
	}
	return RepoNameFromURL(out)
}

func fromGitRoot(cwd string) string {
	out := gitCmd(cwd, "rev-parse", "--show-toplevel")
	if out == "" {
		return ""
	}
	return filepath.Base(out)
}

// findChildRepos returns the basenames and absolute paths of child git repos,
// filtering out noise directories.
func findChildRepos(cwd string) ([]string, []string) {
	entries, err := os.ReadDir(cwd)
	if err != nil {
		return nil, nil
	}

	var names []string
	var paths []string
	deadline := time.Now().Add(200 * time.Millisecond)

	for _, e := range entries {
		if time.Now().After(deadline) {
			break
		}
		if !e.IsDir() {
			continue
		}
		// Filter noise directories
		if noiseDirs[e.Name()] {
			continue
		}
		childPath := filepath.Join(cwd, e.Name())
		if _, err := os.Stat(filepath.Join(childPath, ".git")); err == nil {
			names = append(names, e.Name())
			paths = append(paths, childPath)
		}
	}

	return names, paths
}

// gitCmd ejecuta un comando git con timeout de 200ms.
func gitCmd(cwd string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd

	done := make(chan []byte, 1)
	go func() {
		out, _ := cmd.Output()
		done <- out
	}()

	select {
	case out := <-done:
		return strings.TrimSpace(string(out))
	case <-time.After(200 * time.Millisecond):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return ""
	}
}

// RepoNameFromURL extrae el nombre del repo de una URL git.
// Soporta HTTPS, SSH y git:// formats.
func RepoNameFromURL(url string) string {
	// quitar .git del final
	url = strings.TrimSuffix(url, ".git")

	// extraer el último segmento del path
	// https://github.com/user/repo  → repo
	// git@github.com:user/repo      → repo
	// ssh://git@github.com/user/repo → repo
	if idx := strings.LastIndex(url, "/"); idx >= 0 {
		return url[idx+1:]
	}
	if idx := strings.LastIndex(url, ":"); idx >= 0 {
		return url[idx+1:]
	}
	return url
}

// normalize convierte a minúsculas y reemplaza caracteres no alfanuméricos con guión.
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "unknown"
	}
	return result
}
