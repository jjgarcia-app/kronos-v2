package doctor_test

import (
	"context"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/doctor"
)

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
