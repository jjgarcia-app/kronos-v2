package doctor_test

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/doctor"
	"github.com/jjgarcia-app/kronos-v2/internal/llm"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// digestTestConfig aísla la config con un SQLite propio y un HOME temporal
// (de donde platform.DataDir() resuelve el archivo del cortacircuitos), para
// no leer/escribir el estado real del usuario que corre la suite — a
// diferencia de TestRun_ReturnsAllChecks, que sí usa config.Default() tal
// cual porque solo valida nombres/estados, no contenido.
func digestTestConfig(t *testing.T) config.Config {
	t.Helper()
	home := t.TempDir()
	for k, v := range platform.FakeHomeEnv(runtime.GOOS, home) {
		t.Setenv(k, v)
	}

	cfg := config.Default()
	cfg.DB.SQLitePath = filepath.Join(t.TempDir(), "kronos.db")
	return cfg
}

func TestCheckAutoDigest_NoDigestYet_ReportsNoData(t *testing.T) {
	cfg := digestTestConfig(t)
	report := doctor.Run(context.Background(), cfg)
	check := findCheck(t, report, "Digest automático")
	if !strings.Contains(check.Detail, "sin datos") {
		t.Errorf("Detail = %q, want mención de 'sin datos'", check.Detail)
	}
	if check.Status != doctor.StatusOK {
		t.Errorf("Status = %v, want StatusOK (sin digest todavía no es un fallo)", check.Status)
	}
}

func TestCheckAutoDigest_DigestDisabled_Warns(t *testing.T) {
	cfg := digestTestConfig(t)
	cfg.Digest.Enabled = false
	report := doctor.Run(context.Background(), cfg)
	check := findCheck(t, report, "Digest automático")
	if check.Status != doctor.StatusWarn {
		t.Errorf("Status = %v, want StatusWarn con digest.enabled=false", check.Status)
	}
	if !strings.Contains(check.Detail, "enabled=false") {
		t.Errorf("Detail = %q, esperaba mención de enabled=false", check.Detail)
	}
}

func TestCheckAutoDigest_ReportsLatestSessionDigest(t *testing.T) {
	cfg := digestTestConfig(t)
	st, err := store.New(cfg.DB.SQLitePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.CreateSession(ctx, "sess-abc12345", "kronos-v2", "/tmp/kronos-v2"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveObservation(ctx, store.SaveParams{
		SessionID: "sess-abc12345", Type: store.TypeSession,
		Title: "Resumen de sesión sess-abc1 (automático)", Content: "algo",
		Project: "kronos-v2", TopicKey: "session/sess-abc12345",
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	report := doctor.Run(ctx, cfg)
	check := findCheck(t, report, "Digest automático")
	if strings.Contains(check.Detail, "sin datos") {
		t.Errorf("Detail = %q, no debería decir 'sin datos' con un digest guardado", check.Detail)
	}
	if !strings.Contains(check.Detail, "sess-abc") {
		t.Errorf("Detail = %q, esperaba la sesión sess-abc", check.Detail)
	}
}

func TestCheckAutoDigest_BreakerOpen_WarnsWithDetail(t *testing.T) {
	cfg := digestTestConfig(t)
	dataDir, err := platform.DataDir()
	if err != nil {
		t.Fatal(err)
	}

	b := llm.NewBreaker(llm.DefaultBreakerPath(dataDir), 1, time.Hour)
	b.RecordFailure(errTestBreaker{})

	report := doctor.Run(context.Background(), cfg)
	check := findCheck(t, report, "Digest automático")
	if check.Status != doctor.StatusWarn {
		t.Errorf("Status = %v, want StatusWarn con el cortacircuitos abierto", check.Status)
	}
	if !strings.Contains(check.Detail, "ABIERTO") {
		t.Errorf("Detail = %q, esperaba mención de cortacircuitos ABIERTO", check.Detail)
	}
}

type errTestBreaker struct{}

func (errTestBreaker) Error() string { return "fallo simulado de prueba" }

// TestCheckAutoDigest_PendingEnrichment_ReportsCount confirma que un
// enriquecimiento por LLM pendiente de reintento (ver internal/llm.
// DigestPending, tema del reintento tras timeout) se ve en la línea del
// digest de `kronos doctor` — sin esto, un pico de carga que hizo fallar el
// enriquecimiento queda invisible hasta que alguien nota que faltan hechos
// tipados.
func TestCheckAutoDigest_PendingEnrichment_ReportsCount(t *testing.T) {
	cfg := digestTestConfig(t)
	dataDir, err := platform.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	pending := llm.NewDigestPending(llm.DefaultDigestPendingPath(dataDir))
	pending.MarkFailed("s1")
	pending.MarkFailed("s2")

	report := doctor.Run(context.Background(), cfg)
	check := findCheck(t, report, "Digest automático")
	if !strings.Contains(check.Detail, "enriquecimiento pendiente: 2 sesiones") {
		t.Errorf("Detail = %q, esperaba mención de 2 sesiones con enriquecimiento pendiente", check.Detail)
	}
}

// TestCheckAutoDigest_NoPending_OmitsDetail confirma que sin pendientes no
// se agrega ruido a la línea del digest.
func TestCheckAutoDigest_NoPending_OmitsDetail(t *testing.T) {
	cfg := digestTestConfig(t)
	report := doctor.Run(context.Background(), cfg)
	check := findCheck(t, report, "Digest automático")
	if strings.Contains(check.Detail, "enriquecimiento pendiente") {
		t.Errorf("Detail = %q, no debería mencionar pendientes sin ninguno", check.Detail)
	}
}
