package doctor

import (
	"strings"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/llm"
)

func TestFormatUsageDetail_BreaksDownByProviderAndResult(t *testing.T) {
	now := time.Date(2026, 9, 12, 15, 30, 0, 0, time.UTC)
	currentHour := now.Truncate(time.Hour)

	state := llm.UsageState{
		Buckets: []llm.UsageBucket{
			{HourStart: currentHour, Provider: "claude-cli", Result: llm.UsageResultOK, Count: 4},
			{HourStart: currentHour.Add(-1 * time.Hour), Provider: "claude-cli", Result: llm.UsageResultOK, Count: 7},
			{HourStart: currentHour.Add(-1 * time.Hour), Provider: "claude-cli", Result: llm.UsageResultError, Count: 1},
		},
		LastAt:       now.Add(-3 * time.Minute),
		LastProvider: "claude-cli",
		LastResult:   llm.UsageResultOK,
	}

	got := formatUsageDetail(state, now)

	if !strings.Contains(got, "generación: 4 llamadas en la última hora (claude-cli 4: 4 ok)") {
		t.Errorf("falta el desglose de la última hora, got: %q", got)
	}
	if !strings.Contains(got, "hoy: 12 (11 ok, 1 error)") {
		t.Errorf("falta el desglose de hoy, got: %q", got)
	}
	if !strings.Contains(got, "última: hace 3 min") {
		t.Errorf("falta la última llamada, got: %q", got)
	}
}

func TestFormatUsageDetail_NoCallsInCurrentHour(t *testing.T) {
	now := time.Date(2026, 9, 12, 15, 30, 0, 0, time.UTC)
	state := llm.UsageState{
		Buckets: []llm.UsageBucket{
			{HourStart: now.Truncate(time.Hour).Add(-2 * time.Hour), Provider: "ollama", Result: llm.UsageResultOK, Count: 1},
		},
		LastAt:       now.Add(-2 * time.Hour),
		LastProvider: "ollama",
		LastResult:   llm.UsageResultOK,
	}

	got := formatUsageDetail(state, now)
	if !strings.Contains(got, "generación: 0 llamadas en la última hora | hoy:") {
		t.Errorf("una hora sin llamadas no debería inventar un desglose, got: %q", got)
	}
}

func TestFormatUsageDetail_ShowsSkippedNoCredsAbstention(t *testing.T) {
	now := time.Date(2026, 9, 12, 15, 30, 0, 0, time.UTC)
	currentHour := now.Truncate(time.Hour)

	state := llm.UsageState{
		Buckets: []llm.UsageBucket{
			{HourStart: currentHour, Provider: "claude-cli", Result: llm.UsageResultSkippedNoCreds, Count: 2},
		},
		LastAt:       now.Add(-1 * time.Minute),
		LastProvider: "claude-cli",
		LastResult:   llm.UsageResultSkippedNoCreds,
	}

	got := formatUsageDetail(state, now)

	if !strings.Contains(got, "claude-cli 2: 2 saltada sin credenciales") {
		t.Errorf("esperaba ver la abstención sin credenciales en el desglose, got: %q", got)
	}
}

func TestCheckLLMUsage_NoData_ReportsNoInventedZeros(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	check := checkLLMUsage()
	if check.Status != StatusOK {
		t.Errorf("status = %v, want StatusOK", check.Status)
	}
	if !strings.Contains(check.Detail, "sin datos") {
		t.Errorf("detail = %q, esperaba que dijera explícitamente que no hay datos", check.Detail)
	}
}
