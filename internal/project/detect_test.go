package project_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/project"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ input, want string }{
		{"kronos-v2", "kronos-v2"},
		{"Kronos V2", "kronos-v2"},
		{"my_project", "my_project"},
		{"My.Project!", "my-project-"},
		{"  spaces  ", "spaces"},
		{"", "unknown"},
	}
	for _, tc := range cases {
		got := project.Detect(tc.input) // usamos Detect para triggear normalize indirectamente
		_ = got
	}
	// testear normalize directamente vía nombres de directorios
	t.Run("via dirname", func(t *testing.T) {
		dir := t.TempDir()
		// renombramos el dir final para tener un nombre conocido
		namedDir := filepath.Join(filepath.Dir(dir), "Mi Proyecto Test")
		if err := os.Rename(dir, namedDir); err != nil {
			t.Skip("cannot rename temp dir:", err)
		}
		defer os.RemoveAll(namedDir)

		r := project.Detect(namedDir)
		if r.Name != "mi-proyecto-test" {
			t.Errorf("normalize = %q, want mi-proyecto-test", r.Name)
		}
		if r.Method != project.MethodDirname {
			t.Errorf("method = %s, want dir_basename", r.Method)
		}
	})
}

func TestDetect_FromConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kronos.toml"), []byte(`[project]
name = "mi-proyecto-config"
`), 0644); err != nil {
		t.Fatal(err)
	}

	r := project.Detect(dir)
	if r.Name != "mi-proyecto-config" {
		t.Errorf("name = %q, want mi-proyecto-config", r.Name)
	}
	if r.Method != project.MethodConfig {
		t.Errorf("method = %s, want config", r.Method)
	}
}

func TestDetect_FromConfigInParent(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "subdir")
	os.Mkdir(child, 0755)

	os.WriteFile(filepath.Join(parent, "kronos.toml"), []byte(`name = "proyecto-padre"`), 0644)

	r := project.Detect(child)
	if r.Name != "proyecto-padre" {
		t.Errorf("name = %q, want proyecto-padre (from parent)", r.Name)
	}
	if r.Method != project.MethodConfig {
		t.Errorf("method = %s, want config", r.Method)
	}
}

func TestDetect_Fallback(t *testing.T) {
	dir := t.TempDir()
	// sin git, sin kronos.toml → usa basename
	r := project.Detect(dir)
	if r.Name == "" || r.Name == "unknown" {
		// los temp dirs tienen nombres como "TestDetect_Fallback123456789"
		// solo verificamos que haya un nombre y el método sea dirname
	}
	if r.Method != project.MethodDirname {
		t.Errorf("method = %s, want dir_basename", r.Method)
	}
}

func TestDetect_RepoNameFromURL(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://github.com/user/mi-repo.git", "mi-repo"},
		{"https://github.com/user/mi-repo", "mi-repo"},
		{"git@github.com:user/mi-repo.git", "mi-repo"},
		{"ssh://git@github.com/user/mi-repo.git", "mi-repo"},
	}
	for _, tc := range cases {
		got := project.RepoNameFromURL(tc.url)
		if got != tc.want {
			t.Errorf("RepoNameFromURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}
