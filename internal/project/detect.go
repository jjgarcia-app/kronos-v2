package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Method string

const (
	MethodConfig    Method = "config"     // kronos.toml [project] name
	MethodGitRemote Method = "git_remote" // nombre del repo desde git remote
	MethodGitRoot   Method = "git_root"   // basename del directorio git root
	MethodSingle    Method = "git_child"  // único sub-repo en el directorio
	MethodDirname   Method = "dir_basename"
)

type Result struct {
	Name   string
	Method Method
}

// Detect determina el nombre del proyecto para un directorio de trabajo.
// Aplica el algoritmo de 6 casos en orden de prioridad.
func Detect(cwd string) Result {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return Result{Name: "unknown", Method: MethodDirname}
		}
	}

	// 1. kronos.toml explícito
	if name := fromConfig(cwd); name != "" {
		return Result{Name: normalize(name), Method: MethodConfig}
	}

	// 2. git remote origin → nombre del repo
	if name := fromGitRemote(cwd); name != "" {
		return Result{Name: normalize(name), Method: MethodGitRemote}
	}

	// 3. basename del git root
	if name := fromGitRoot(cwd); name != "" {
		return Result{Name: normalize(name), Method: MethodGitRoot}
	}

	// 4. único sub-repo dentro del directorio
	if name := fromSingleChild(cwd); name != "" {
		return Result{Name: normalize(name), Method: MethodSingle}
	}

	// 5 y 6. fallback: basename del cwd
	return Result{Name: normalize(filepath.Base(cwd)), Method: MethodDirname}
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

func fromSingleChild(cwd string) string {
	entries, err := os.ReadDir(cwd)
	if err != nil {
		return ""
	}

	var gitDirs []string
	deadline := time.Now().Add(200 * time.Millisecond)

	for _, e := range entries {
		if time.Now().After(deadline) {
			break
		}
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(cwd, e.Name(), ".git")); err == nil {
			gitDirs = append(gitDirs, e.Name())
		}
		if len(gitDirs) > 1 {
			// ambiguous: más de un sub-repo, no aplica este caso
			return ""
		}
	}

	if len(gitDirs) == 1 {
		return gitDirs[0]
	}
	return ""
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
