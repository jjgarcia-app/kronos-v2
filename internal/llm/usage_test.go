package llm_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/llm"
)

func TestUsage_Record_AccumulatesCountsByProviderAndResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	u := llm.NewUsage(path)

	u.Record("claude-cli", llm.UsageResultOK)
	u.Record("claude-cli", llm.UsageResultOK)
	u.Record("claude-cli", llm.UsageResultError)
	u.Record("ollama", llm.UsageResultOK)

	st := u.State()
	total := 0
	for _, b := range st.Buckets {
		total += b.Count
	}
	if total != 4 {
		t.Fatalf("total = %d, want 4", total)
	}
	if st.LastProvider != "ollama" || st.LastResult != llm.UsageResultOK {
		t.Errorf("last call = (%s, %s), want (ollama, ok)", st.LastProvider, st.LastResult)
	}
	if st.LastAt.IsZero() {
		t.Error("LastAt no debería quedar en cero tras Record")
	}
}

func TestUsage_Record_PersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	writer := llm.NewUsage(path)
	writer.Record("claude-cli", llm.UsageResultOK)
	writer.Record("claude-cli", llm.UsageResultOK)

	reader := llm.NewUsage(path)
	st := reader.State()
	if len(st.Buckets) != 1 || st.Buckets[0].Count != 2 {
		t.Fatalf("esperaba un bucket con count=2, got %+v", st.Buckets)
	}
}

func TestUsage_CorruptStateFile_StartsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, []byte("{ no es json"), 0o644); err != nil {
		t.Fatal(err)
	}
	u := llm.NewUsage(path)
	if st := u.State(); len(st.Buckets) != 0 {
		t.Fatalf("JSON corrupto debería tratarse como estado vacío, got %+v", st.Buckets)
	}
	u.Record("ollama", llm.UsageResultOK)
	if st := u.State(); len(st.Buckets) != 1 {
		t.Fatalf("Record después de corrupción debería arrancar de cero y contar 1, got %+v", st.Buckets)
	}
}

func TestUsage_MissingStateFile_StartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-existe", "usage.json")
	u := llm.NewUsage(path)
	if st := u.State(); len(st.Buckets) != 0 {
		t.Fatalf("sin archivo debería devolver estado vacío, got %+v", st.Buckets)
	}
}

func TestUsage_Record_PrunesBucketsOlderThanRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	old := llm.UsageState{
		Buckets: []llm.UsageBucket{
			{HourStart: time.Now().Add(-30 * 24 * time.Hour), Provider: "ollama", Result: llm.UsageResultOK, Count: 5},
		},
	}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	u := llm.NewUsage(path)
	u.Record("claude-cli", llm.UsageResultOK)

	st := u.State()
	for _, b := range st.Buckets {
		if b.Provider == "ollama" {
			t.Fatalf("el bucket viejo (30 días) debería haberse descartado, got %+v", st.Buckets)
		}
	}
}
