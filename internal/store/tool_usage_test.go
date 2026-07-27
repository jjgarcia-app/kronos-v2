package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRecordToolUse_And_ToolUsageStats(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	if err := s.RecordToolUse(ctx, "s1", "kronos-v2", "Edit"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordToolUse(ctx, "s1", "kronos-v2", "Edit"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordToolUse(ctx, "s1", "kronos-v2", "Bash"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordToolUse(ctx, "s2", "otro-proyecto", "Write"); err != nil {
		t.Fatal(err)
	}

	stats, err := s.ToolUsageStats(ctx, "kronos-v2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("esperaba 2 tools distintos para kronos-v2, obtuve %d: %+v", len(stats), stats)
	}
	if stats[0].ToolName != "Edit" || stats[0].Count != 2 {
		t.Errorf("top tool = %+v, want Edit:2", stats[0])
	}

	all, err := s.ToolUsageStats(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("esperaba 3 tools distintos across projects, obtuve %d: %+v", len(all), all)
	}
}
