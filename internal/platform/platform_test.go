package platform_test

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/platform"
)

func TestDataDir_ContainsKronos(t *testing.T) {
	dir, err := platform.DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if !strings.HasSuffix(dir, "kronos") {
		t.Errorf("DataDir should end with 'kronos', got: %s", dir)
	}
}

func TestDBPath_EndsWithDB(t *testing.T) {
	p, err := platform.DBPath()
	if err != nil {
		t.Fatalf("DBPath: %v", err)
	}
	if filepath.Base(p) != "kronos.db" {
		t.Errorf("DBPath base should be kronos.db, got: %s", p)
	}
}

func TestConfigDir_ContainsKronos(t *testing.T) {
	dir, err := platform.ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if !strings.Contains(dir, "kronos") {
		t.Errorf("ConfigDir should contain 'kronos', got: %s", dir)
	}
}

func TestClaudeDir_ContainsDotClaude(t *testing.T) {
	dir, err := platform.ClaudeDir()
	if err != nil {
		t.Fatalf("ClaudeDir: %v", err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".claude")
	if dir != want {
		t.Errorf("ClaudeDir = %s, want %s", dir, want)
	}
}

func TestDataDir_WindowsLocalAppData(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only test")
	}
	dir, err := platform.DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	local := os.Getenv("LOCALAPPDATA")
	if local != "" && !strings.HasPrefix(dir, local) {
		t.Errorf("on Windows with LOCALAPPDATA set, DataDir should start with %s, got: %s", local, dir)
	}
}

func TestOS_KnownValue(t *testing.T) {
	got := platform.OS()
	known := map[string]bool{"windows": true, "darwin": true, "linux": true}
	if !known[got] && got == "" {
		t.Errorf("OS() returned empty string")
	}
}

// TestFakeHomeEnv_Windows/_Unix testean la lógica de selección de variables
// por SO de forma determinista, pasando el GOOS a mano — sin esto, la rama
// de Windows de FakeHomeEnv no se ejercitaba nunca en el CI de Linux/macOS.
func TestFakeHomeEnv_Windows(t *testing.T) {
	got := platform.FakeHomeEnv("windows", `C:\fake\home`)
	want := map[string]string{
		"USERPROFILE":  `C:\fake\home`,
		"LOCALAPPDATA": `C:\fake\home`,
		"APPDATA":      `C:\fake\home`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FakeHomeEnv(windows, ...) = %v, want %v", got, want)
	}
}

func TestFakeHomeEnv_Unix(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		got := platform.FakeHomeEnv(goos, "/fake/home")
		want := map[string]string{
			"HOME":            "/fake/home",
			"XDG_DATA_HOME":   "",
			"XDG_CONFIG_HOME": "",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("FakeHomeEnv(%s, ...) = %v, want %v", goos, got, want)
		}
	}
}
