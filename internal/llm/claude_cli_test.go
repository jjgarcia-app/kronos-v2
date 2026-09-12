package llm

import (
	"context"
	"os"
	"os/exec"
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

	out, err := b.generate(context.Background(), `{"hello":"world"}`, 100, 0)
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

	out, err := b.generate(context.Background(), "prompt", 100, 0)
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

	_, err := b.generate(context.Background(), "prompt", 100, 0)
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
	_, err := b.generate(context.Background(), "prompt", 100, 0)
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

// TestClaudeCLIBackend_Generate_TimeoutParamOverridesShorter confirma que el
// parámetro timeout de generate() pisa el timeout con el que se construyó el
// backend cuando es MÁS CORTO — necesario para que un caller pueda acotar
// una llamada puntual sin tocar el timeout general del cliente.
func TestClaudeCLIBackend_Generate_TimeoutParamOverridesShorter(t *testing.T) {
	cli := writeFakeCLI(t, "#!/bin/sh\nsleep 5\necho deberia-no-verse\n")
	// b.timeout=5s (generoso) pero se pide timeout=100ms para esta llamada.
	b := &claudeCLIBackend{cliPath: cli, model: "haiku", configDir: t.TempDir(), timeout: 5 * time.Second}

	start := time.Now()
	_, err := b.generate(context.Background(), "prompt", 100, 100*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("esperaba timeout — el parámetro debería haber acotado la llamada a 100ms, no a los 5s del backend")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("el error debería mencionar el timeout, got: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("debería haberse cortado cerca de 100ms (override), tardó %s", elapsed)
	}
}

// TestClaudeCLIBackend_Generate_TimeoutParamOverridesLonger confirma el caso
// que motiva digest.timeout_ms: un caller puede pedir MÁS tiempo del que
// tiene configurado el backend (llm.timeout_ms) para una llamada puntual —
// acá el backend tiene 100ms (moriría con el timeout general) pero la
// llamada pide 3s, tiempo de sobra para que el script de 300ms termine bien.
func TestClaudeCLIBackend_Generate_TimeoutParamOverridesLonger(t *testing.T) {
	cli := writeFakeCLI(t, "#!/bin/sh\nsleep 0.3\necho listo\n")
	b := &claudeCLIBackend{cliPath: cli, model: "haiku", configDir: t.TempDir(), timeout: 100 * time.Millisecond}

	out, err := b.generate(context.Background(), "prompt", 100, 3*time.Second)
	if err != nil {
		t.Fatalf("con el timeout pisado a 3s no debería fallar por el b.timeout de 100ms: %v", err)
	}
	if out != "listo" {
		t.Errorf("out = %q", out)
	}
}

func TestClassifyClaudeCLIFailure(t *testing.T) {
	cases := []struct {
		name     string
		runErr   error
		timedOut bool
		stderr   string
		want     string
	}{
		{"no logueado", nil, false, "Not logged in · Please run /login", ClaudeCLIFailureNotLoggedIn},
		{"flag desconocida", nil, false, "unknown flag --model", ClaudeCLIFailureIncompatible},
		{"modelo inexistente", nil, false, "Error: invalid model 'no-existe'", ClaudeCLIFailureIncompatible},
		{"timeout por contexto", nil, true, "", ClaudeCLIFailureTimeout},
		{"timeout por stderr", nil, false, "request timed out", ClaudeCLIFailureTimeout},
		{"permiso denegado por stderr", nil, false, "bash: permission denied", ClaudeCLIFailurePermission},
		{"binario inexistente", &exec.Error{Name: "claude", Err: exec.ErrNotFound}, false, "", ClaudeCLIFailureBinaryMissing},
		{"stderr no reconocido", nil, false, "algo raro que nunca vimos antes", ClaudeCLIFailureUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, advice := classifyClaudeCLIFailure(tc.runErr, tc.timedOut, tc.stderr)
			if kind != tc.want {
				t.Errorf("kind = %q, want %q", kind, tc.want)
			}
			if advice == "" {
				t.Error("advice no debería ser vacío")
			}
		})
	}
}

func TestClaudeCLIBackend_Generate_RecordsLastFailureOnError(t *testing.T) {
	cli := writeFakeCLI(t, "#!/bin/sh\necho 'Not logged in, please run /login' >&2\nexit 1\n")
	failurePath := filepath.Join(t.TempDir(), "llm-last-failure.json")
	b := &claudeCLIBackend{cliPath: cli, model: "haiku", configDir: t.TempDir(), timeout: 5 * time.Second, lastFailurePath: failurePath}

	if _, err := b.generate(context.Background(), "prompt", 100, 0); err == nil {
		t.Fatal("esperaba error")
	}

	lf, ok := ReadLastFailure(failurePath)
	if !ok {
		t.Fatal("esperaba que se persistiera la última falla")
	}
	if lf.Kind != ClaudeCLIFailureNotLoggedIn {
		t.Errorf("kind = %q, want %q", lf.Kind, ClaudeCLIFailureNotLoggedIn)
	}
	if lf.Provider != claudeCLIProvider {
		t.Errorf("provider = %q, want %q", lf.Provider, claudeCLIProvider)
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

// dataDirFor calcula el mismo data dir que platform.DataDir() vería con el
// HOME/XDG_DATA_HOME que dejó withFakeHome — evita que el test dependa de
// platform.DataDir directamente para no acoplarse a su firma.
func dataDirFor(t *testing.T, home string) string {
	t.Helper()
	return filepath.Join(home, ".local", "share", "kronos")
}

func TestNewClaudeCLIFromConfig_NoCredentials_RegistraAbstencionSinAbrirCortacircuitos(t *testing.T) {
	home := withFakeHome(t)
	cfg := config.Default()
	cfg.LLM.Provider = "claude-cli"

	if c := NewClaudeCLIFromConfig(context.Background(), cfg); c != nil {
		t.Fatal("esperaba nil sin credenciales de Claude Code disponibles")
	}

	dataDir := dataDirFor(t, home)

	st := NewUsage(DefaultUsagePath(dataDir)).State()
	total := 0
	for _, b := range st.Buckets {
		if b.Provider == claudeCLIProvider && b.Result == UsageResultSkippedNoCreds {
			total += b.Count
		}
	}
	if total != 1 {
		t.Fatalf("esperaba exactamente 1 abstención skipped_no_creds contada, got %d (buckets=%+v)", total, st.Buckets)
	}
	if st.LastResult != UsageResultSkippedNoCreds {
		t.Errorf("LastResult = %q, want %q", st.LastResult, UsageResultSkippedNoCreds)
	}

	lf, ok := ReadLastFailure(DefaultLastFailurePath(dataDir))
	if !ok {
		t.Fatal("esperaba que se persistiera la clasificación de la abstención")
	}
	if lf.Kind != ClaudeCLIFailureNoCreds {
		t.Errorf("kind = %q, want %q", lf.Kind, ClaudeCLIFailureNoCreds)
	}

	b := NewBreaker(DefaultBreakerPath(dataDir), 3, time.Minute)
	if bst := b.State(); bst.ConsecutiveFailures != 0 || !bst.OpenUntil.IsZero() {
		t.Errorf("la abstención sin credenciales no debería tocar el cortacircuitos, got %+v", bst)
	}
}
