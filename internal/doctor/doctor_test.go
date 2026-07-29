package doctor_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/doctor"
)

func kronosBinName() string {
	if runtime.GOOS == "windows" {
		return "kronos.exe"
	}
	return "kronos"
}

func TestRun_ReturnsAllChecks(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	report := doctor.Run(ctx, cfg)

	wantNames := []string{
		"Config file",
		"Base de datos",
		"Ollama",
		"Modelo embeddings",
		"Hooks Claude Code",
		"Binario en PATH",
	}
	if len(report.Checks) != len(wantNames) {
		t.Fatalf("expected %d checks, got %d", len(wantNames), len(report.Checks))
	}
	for i, check := range report.Checks {
		if check.Name != wantNames[i] {
			t.Errorf("check[%d] name = %q, want %q", i, check.Name, wantNames[i])
		}
		if check.Detail == "" {
			t.Errorf("check[%d] %q has empty detail", i, check.Name)
		}
	}
}

func TestRun_StatusValues_AreValid(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	report := doctor.Run(ctx, cfg)
	for _, check := range report.Checks {
		switch check.Status {
		case doctor.StatusOK, doctor.StatusWarn, doctor.StatusFail:
			// valid
		default:
			t.Errorf("check %q has invalid status %d", check.Name, check.Status)
		}
	}
}

func TestFix_UnknownCheck_ReturnsError(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	ch := make(chan string, 8)
	err := doctor.Fix(ctx, cfg, "NonexistentCheck", ch)
	if err == nil {
		t.Error("Fix with unknown check name should return error")
	}
}

func TestFix_ClosesChannel(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	ch := make(chan string, 8)
	_ = doctor.Fix(ctx, cfg, "NonexistentCheck", ch)
	// channel must be closed by Fix
	for range ch {
	}
	// if we got here without blocking, channel is closed — test passes
}

// TestCheckBinaryInPath_WarnsOnDuplicates reproduce el bug real de esta
// sesión: dos kronos.exe en PATH que se desincronizaron sin ningún aviso —
// los hooks resolvían al viejo (por PATH lookup), el MCP server al nuevo
// (por ruta absoluta), y nada avisaba de la divergencia hasta que rompió en
// producción. checkBinaryInPath ahora debe detectar esto.
func TestCheckBinaryInPath_WarnsOnDuplicates(t *testing.T) {
	binName := kronosBinName()

	dir1 := t.TempDir()
	dir2 := t.TempDir()
	writeFakeBinary(t, filepath.Join(dir1, binName))
	writeFakeBinary(t, filepath.Join(dir2, binName))

	t.Setenv("PATH", strings.Join([]string{dir1, dir2}, string(os.PathListSeparator)))

	ctx := context.Background()
	report := doctor.Run(ctx, config.Default())

	check := findCheck(t, report, "Binario en PATH")
	if check.Status != doctor.StatusWarn {
		t.Errorf("Status = %v, want StatusWarn con 2 copias en PATH", check.Status)
	}
	if !strings.Contains(check.Detail, dir1) || !strings.Contains(check.Detail, dir2) {
		t.Errorf("Detail no menciona ambas rutas: %q", check.Detail)
	}
}

// TestCheckBinaryInPath_OKWithSingleCopy confirma que una sola copia en
// PATH sigue reportando OK, sin falsos positivos.
func TestCheckBinaryInPath_OKWithSingleCopy(t *testing.T) {
	binName := kronosBinName()

	dir := t.TempDir()
	writeFakeBinary(t, filepath.Join(dir, binName))
	t.Setenv("PATH", dir)

	ctx := context.Background()
	report := doctor.Run(ctx, config.Default())

	check := findCheck(t, report, "Binario en PATH")
	if check.Status != doctor.StatusOK {
		t.Errorf("Status = %v, want StatusOK con una sola copia en PATH (detail: %q)", check.Status, check.Detail)
	}
}

func writeFakeBinary(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func findCheck(t *testing.T, report doctor.Report, name string) doctor.Check {
	t.Helper()
	for _, c := range report.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q no encontrado en el report", name)
	return doctor.Check{}
}

// TestCheckClaudeHooks_DetectsAbsolutePathCommand reproduce una regresión
// real: el check buscaba el literal "kronos hook" en settings.json, pero
// los comandos de hook pasaron a usar ruta absoluta ("kronos.exe hook ...",
// con .exe en el medio) — dejó de matchear y reportaba "hooks no
// encontrados" con hooks realmente bien instalados.
func TestCheckClaudeHooks_DetectsAbsolutePathCommand(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"C:/Users/Jerry/go/bin/kronos.exe hook pre-tool-use"}]}]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	report := doctor.Run(context.Background(), config.Default())
	check := findCheck(t, report, "Hooks Claude Code")
	if check.Status != doctor.StatusOK {
		t.Errorf("Status = %v, want StatusOK con hooks de ruta absoluta instalados (detail: %q)", check.Status, check.Detail)
	}
}
