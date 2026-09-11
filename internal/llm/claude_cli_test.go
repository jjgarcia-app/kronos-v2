package llm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
)

// writeFakeCLI escribe un script ejecutable que hace de reemplazo del
// binario `claude` real para tests hermeticos — sin esto, testear
// claudeCLIBackend.generate necesitaría el CLI real, autenticado, con red.
func writeFakeCLI(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-claude.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClaudeCLIBackend_Generate_ReturnsTrimmedStdout(t *testing.T) {
	cli := writeFakeCLI(t, "#!/bin/sh\ncat\n")
	b := &claudeCLIBackend{cliPath: cli, model: "haiku", configDir: t.TempDir(), timeout: 5 * time.Second}

	out, err := b.generate(context.Background(), `{"hello":"world"}`, 100)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out != `{"hello":"world"}` {
		t.Errorf("out = %q", out)
	}
}

func TestClaudeCLIBackend_Generate_PassesConfigDirEnv(t *testing.T) {
	cli := writeFakeCLI(t, `#!/bin/sh
if [ -z "$CLAUDE_CONFIG_DIR" ]; then
  echo "CLAUDE_CONFIG_DIR no seteado" >&2
  exit 1
fi
echo "$CLAUDE_CONFIG_DIR"
`)
	configDir := t.TempDir()
	b := &claudeCLIBackend{cliPath: cli, model: "haiku", configDir: configDir, timeout: 5 * time.Second}

	out, err := b.generate(context.Background(), "prompt", 100)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out != configDir {
		t.Errorf("esperaba que el subproceso viera CLAUDE_CONFIG_DIR=%q, vio %q", configDir, out)
	}
}

func TestClaudeCLIBackend_Generate_NonZeroExit_ReturnsStderrInError(t *testing.T) {
	cli := writeFakeCLI(t, "#!/bin/sh\necho 'auth inválida' >&2\nexit 1\n")
	b := &claudeCLIBackend{cliPath: cli, model: "haiku", configDir: t.TempDir(), timeout: 5 * time.Second}

	_, err := b.generate(context.Background(), "prompt", 100)
	if err == nil {
		t.Fatal("esperaba error por exit code != 0")
	}
	if !strings.Contains(err.Error(), "auth inválida") {
		t.Errorf("el error debería incluir el stderr recortado, got: %v", err)
	}
}

func TestClaudeCLIBackend_Generate_TimeoutKillsProcess(t *testing.T) {
	cli := writeFakeCLI(t, "#!/bin/sh\nsleep 5\necho deberia-no-verse\n")
	b := &claudeCLIBackend{cliPath: cli, model: "haiku", configDir: t.TempDir(), timeout: 100 * time.Millisecond}

	start := time.Now()
	_, err := b.generate(context.Background(), "prompt", 100)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("esperaba error de timeout")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("el error debería mencionar el timeout, got: %v", err)
	}
	// 100ms de timeout + hasta 2s de WaitDelay (ver claude_cli.go) para el
	// cierre de pipes — muy por debajo de los 5s que tarda el script si no
	// lo matáramos.
	if elapsed > 3*time.Second {
		t.Errorf("el proceso debería haberse matado cerca del timeout (100ms + WaitDelay), tardó %s", elapsed)
	}
}

// withFakeHome apunta HOME (y limpia XDG_DATA_HOME/XDG_CONFIG_HOME) a un
// directorio temporal — así platform.ClaudeDir/ClaudeMCPFile/DataDir quedan
// bajo control del test sin tocar el ~/.claude real de la máquina.
func withFakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	return home
}

func TestPrepareClaudeCLIConfigDir_NoCredentials_ReturnsError(t *testing.T) {
	withFakeHome(t)
	dir := filepath.Join(t.TempDir(), "claude-cli")

	if err := prepareClaudeCLIConfigDir(dir); err == nil {
		t.Fatal("esperaba error sin ~/.claude/.credentials.json ni ~/.claude.json")
	}
}

func TestPrepareClaudeCLIConfigDir_LinksCredentialsAndWritesSettings(t *testing.T) {
	home := withFakeHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	credsContent := `{"token":"fake"}`
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(credsContent), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "claude-cli")
	if err := prepareClaudeCLIConfigDir(dir); err != nil {
		t.Fatalf("prepareClaudeCLIConfigDir: %v", err)
	}

	settings, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("leer settings.json: %v", err)
	}
	if string(settings) != `{"hooks":{}}` {
		t.Errorf("settings.json = %q, quería {\"hooks\":{}} (sin hooks, para que no se disparen dentro del subproceso)", settings)
	}

	got, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		t.Fatalf("leer .credentials.json linkeado: %v", err)
	}
	if string(got) != credsContent {
		t.Errorf(".credentials.json linkeado = %q, quería %q", got, credsContent)
	}
}

func TestPrepareClaudeCLIConfigDir_PreservesExistingSettingsOnSecondCall(t *testing.T) {
	home := withFakeHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "claude-cli")
	if err := prepareClaudeCLIConfigDir(dir); err != nil {
		t.Fatalf("primera llamada: %v", err)
	}

	custom := `{"hooks":{},"custom":true}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := prepareClaudeCLIConfigDir(dir); err != nil {
		t.Fatalf("segunda llamada: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != custom {
		t.Errorf("la segunda llamada pisó settings.json — got %q, quería preservar %q", got, custom)
	}
}

func TestNewClaudeCLIFromConfig_NoCredentials_ReturnsNil(t *testing.T) {
	withFakeHome(t)
	cfg := config.Default()
	cfg.LLM.Provider = "claude-cli"

	if c := NewClaudeCLIFromConfig(context.Background(), cfg); c != nil {
		t.Fatal("esperaba nil sin credenciales de Claude Code disponibles")
	}
}
