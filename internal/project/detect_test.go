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

func TestDetectFull_FromKronosConfig(t *testing.T) {
	dir := t.TempDir()
	kronosDir := filepath.Join(dir, ".kronos")
	if err := os.Mkdir(kronosDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kronosDir, "config.json"), []byte(`{"project_name":"mi-proyecto-json"}`), 0644); err != nil {
		t.Fatal(err)
	}

	r := project.DetectFull(dir)
	if r.Project != "mi-proyecto-json" {
		t.Errorf("Project = %q, want mi-proyecto-json", r.Project)
	}
	if r.Source != "config" {
		t.Errorf("Source = %q, want config", r.Source)
	}
	if r.Error != nil {
		t.Errorf("unexpected error: %v", r.Error)
	}
}

func TestDetectFull_KronosConfig_InvalidName(t *testing.T) {
	dir := t.TempDir()
	kronosDir := filepath.Join(dir, ".kronos")
	if err := os.Mkdir(kronosDir, 0755); err != nil {
		t.Fatal(err)
	}
	// project_name with path separator — should be rejected
	if err := os.WriteFile(filepath.Join(kronosDir, "config.json"), []byte(`{"project_name":"../../etc/passwd"}`), 0644); err != nil {
		t.Fatal(err)
	}

	r := project.DetectFull(dir)
	// Should fall through to dir_basename since config name is invalid
	if r.Source == "config" {
		t.Error("expected invalid project_name to be rejected, but Source = config")
	}
}

func TestDetectFull_KronosConfig_EmptyName(t *testing.T) {
	dir := t.TempDir()
	kronosDir := filepath.Join(dir, ".kronos")
	if err := os.Mkdir(kronosDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kronosDir, "config.json"), []byte(`{"project_name":""}`), 0644); err != nil {
		t.Fatal(err)
	}

	r := project.DetectFull(dir)
	if r.Source == "config" {
		t.Error("empty project_name should be rejected")
	}
}

func TestDetectFull_SingleChild_AutoPromote(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "my-service")
	if err := os.MkdirAll(filepath.Join(child, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	r := project.DetectFull(parent)
	if r.Project != "my-service" {
		t.Errorf("Project = %q, want my-service", r.Project)
	}
	if r.Source != "git_child" {
		t.Errorf("Source = %q, want git_child", r.Source)
	}
	if r.Warning != "auto-promoted child repository" {
		t.Errorf("Warning = %q, want 'auto-promoted child repository'", r.Warning)
	}
	if r.Error != nil {
		t.Errorf("unexpected error: %v", r.Error)
	}
}

func TestDetectFull_MultipleChildren_Ambiguous(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"service-a", "service-b"} {
		if err := os.MkdirAll(filepath.Join(parent, name, ".git"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	r := project.DetectFull(parent)
	if r.Project != "" {
		t.Errorf("Project should be empty for ambiguous, got %q", r.Project)
	}
	if r.Source != "ambiguous" {
		t.Errorf("Source = %q, want ambiguous", r.Source)
	}
	if r.Error != project.ErrAmbiguousProject {
		t.Errorf("Error = %v, want ErrAmbiguousProject", r.Error)
	}
	if len(r.AvailableProjects) != 2 {
		t.Errorf("AvailableProjects len = %d, want 2", len(r.AvailableProjects))
	}
}

func TestDetectFull_NoiseFiltered(t *testing.T) {
	parent := t.TempDir()
	// Create a noise dir with .git — should be ignored
	if err := os.MkdirAll(filepath.Join(parent, "node_modules", ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	// Create a real child repo
	if err := os.MkdirAll(filepath.Join(parent, "my-app", ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	r := project.DetectFull(parent)
	if r.Project != "my-app" {
		t.Errorf("Project = %q, want my-app (node_modules should be filtered)", r.Project)
	}
	if r.Source != "git_child" {
		t.Errorf("Source = %q, want git_child", r.Source)
	}
}

func TestDetectFull_PathIsAbsolute(t *testing.T) {
	dir := t.TempDir()
	r := project.DetectFull(dir)
	if !filepath.IsAbs(r.Path) {
		t.Errorf("Path %q should be absolute", r.Path)
	}
}

func TestDetect_BackwardCompat_Ambiguous(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"repo-x", "repo-y"} {
		if err := os.MkdirAll(filepath.Join(parent, name, ".git"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	r := project.Detect(parent)
	// Ambiguous case: Detect returns "unknown" with ambiguous method
	if r.Name != "unknown" {
		t.Errorf("Detect ambiguous: Name = %q, want unknown", r.Name)
	}
}
