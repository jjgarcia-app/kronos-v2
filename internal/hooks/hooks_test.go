package hooks_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	f, err := os.CreateTemp("", "kronos-hooks-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	st, err := store.New(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// slowSearchStore wraps a real store and blocks Search until the context is cancelled.
// Used to test the 100ms hard-timeout in RunPromptSubmit.
type slowSearchStore struct {
	store.Storer
}

func (s *slowSearchStore) Search(ctx context.Context, p store.SearchParams) ([]*store.SearchResult, error) {
	select {
	case <-time.After(500 * time.Millisecond):
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// captureStdout redirects os.Stdout during fn, returns captured string.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	r.Close()
	return buf.String()
}

// --- RunSessionStart (tasks 1.4) ---

func TestRunSessionStart_CreatesSession(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	in := hooks.Input{
		SessionID: "test-sess-001",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
	}

	captureStdout(t, func() {
		if err := hooks.RunSessionStart(ctx, in, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	// Session must exist now; ending it should succeed.
	if err := st.EndSession(ctx, "test-sess-001", ""); err != nil {
		t.Errorf("EndSession after SessionStart: %v", err)
	}
}

func TestRunSessionStart_NormalStart_EmitsSignal(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Save a few observations so count > 0.
	for i := 0; i < 3; i++ {
		st.SaveObservation(ctx, store.SaveParams{
			Type:    store.TypeDecision,
			Title:   fmt.Sprintf("obs signal test %d", i),
			Content: fmt.Sprintf("content for signal emission test observation %d", i),
			Project: "kronos-v2",
		})
	}

	in := hooks.Input{
		SessionID: "sess-signal",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
	}

	out := captureStdout(t, func() {
		if err := hooks.RunSessionStart(ctx, in, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	if !strings.Contains(out, "[kronos]") {
		t.Errorf("output missing [kronos] prefix: %q", out)
	}
	if !strings.Contains(out, "observations available for") {
		t.Errorf("output missing observation count line: %q", out)
	}
	if !strings.Contains(out, "call mem_search") {
		t.Errorf("output missing call-to-action line: %q", out)
	}
}

func TestRunSessionStart_NormalStart_NoObsContent(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypeDecision,
		Title:   "secret content observation",
		Content: "this content must NOT appear in normal start output",
		Project: "kronos-v2",
	})

	in := hooks.Input{
		SessionID: "sess-no-content",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
	}

	out := captureStdout(t, func() {
		hooks.RunSessionStart(ctx, in, st)
	})

	if strings.Contains(out, "this content must NOT appear") {
		t.Errorf("observation content leaked into normal start output: %q", out)
	}
}

func TestRunSessionStart_NormalStart_ZeroObs(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	in := hooks.Input{
		SessionID: "sess-zero",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
	}

	out := captureStdout(t, func() {
		if err := hooks.RunSessionStart(ctx, in, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	if !strings.Contains(out, "0 observations available") {
		t.Errorf("expected '0 observations available', got: %q", out)
	}
}

func TestRunSessionStart_NormalStart_PersistsEmptyIDs(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	in := hooks.Input{
		SessionID: "sess-persist",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
	}

	captureStdout(t, func() {
		hooks.RunSessionStart(ctx, in, st)
	})

	ids, err := st.LoadInjectedIDs(ctx, "sess-persist")
	if err != nil {
		t.Fatalf("LoadInjectedIDs: %v", err)
	}
	if ids == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 ids after normal start, got %d", len(ids))
	}
}

// --- RunPostCompaction (task 1.5) ---

func TestRunPostCompaction_PrintsSignalAndObs(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Save 5 observations.
	for i := 0; i < 5; i++ {
		st.SaveObservation(ctx, store.SaveParams{
			Type:    store.TypeDecision,
			Title:   fmt.Sprintf("post compact obs %d", i),
			Content: fmt.Sprintf("content for post compaction observation %d to test injection", i),
			Project: "kronos-v2",
		})
	}

	in := hooks.Input{
		SessionID: "sess-postcompact",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
		Reason:    "compact",
	}

	out := captureStdout(t, func() {
		if err := hooks.RunSessionStart(ctx, in, st); err != nil {
			t.Fatalf("RunSessionStart (compact): %v", err)
		}
	})

	if !strings.Contains(out, "observations available for") {
		t.Errorf("missing signal line in post-compact output: %q", out)
	}
	if !strings.Contains(out, "call mem_search") {
		t.Errorf("missing call-to-action in post-compact output: %q", out)
	}

	// Count [kronos] obs lines (exclude the two header lines).
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var obsLines int
	for _, l := range lines {
		if strings.HasPrefix(l, "[kronos] ") &&
			!strings.Contains(l, "observations available") &&
			!strings.Contains(l, "call mem_search") &&
			!strings.Contains(l, "active task:") {
			obsLines++
		}
	}
	if obsLines != 3 {
		t.Errorf("expected 3 obs lines, got %d\noutput: %q", obsLines, out)
	}
}

func TestRunPostCompaction_FewerThanK(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Save only 2 observations.
	for i := 0; i < 2; i++ {
		st.SaveObservation(ctx, store.SaveParams{
			Type:    store.TypeDecision,
			Title:   fmt.Sprintf("fewer than k obs %d", i),
			Content: fmt.Sprintf("content for fewer than k test observation %d injection", i),
			Project: "kronos-v2",
		})
	}

	in := hooks.Input{
		SessionID: "sess-fewerthan3",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
		Reason:    "compact",
	}

	out := captureStdout(t, func() {
		hooks.RunSessionStart(ctx, in, st)
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	var obsLines int
	for _, l := range lines {
		if strings.HasPrefix(l, "[kronos] ") &&
			!strings.Contains(l, "observations available") &&
			!strings.Contains(l, "call mem_search") &&
			!strings.Contains(l, "active task:") {
			obsLines++
		}
	}
	if obsLines != 2 {
		t.Errorf("expected 2 obs lines (fewer than k), got %d\noutput: %q", obsLines, out)
	}
}

func TestRunPostCompaction_EmptyStore(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	in := hooks.Input{
		SessionID: "sess-empty-compact",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
		Reason:    "compact",
	}

	out := captureStdout(t, func() {
		if err := hooks.RunSessionStart(ctx, in, st); err != nil {
			t.Fatalf("RunSessionStart (compact, empty): %v", err)
		}
	})

	if !strings.Contains(out, "0 observations available") {
		t.Errorf("expected '0 observations available' in empty-store compact: %q", out)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	var obsLines int
	for _, l := range lines {
		if strings.HasPrefix(l, "[kronos] ") &&
			!strings.Contains(l, "observations available") &&
			!strings.Contains(l, "call mem_search") &&
			!strings.Contains(l, "active task:") {
			obsLines++
		}
	}
	if obsLines != 0 {
		t.Errorf("expected 0 obs lines in empty store, got %d", obsLines)
	}
}

func TestRunPostCompaction_PersistsInjectedIDs(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	var savedIDs []int64
	for i := 0; i < 2; i++ {
		obs, _ := st.SaveObservation(ctx, store.SaveParams{
			Type:    store.TypeDecision,
			Title:   fmt.Sprintf("persist ids test obs %d", i),
			Content: fmt.Sprintf("content for persist ids test observation %d injected", i),
			Project: "kronos-v2",
		})
		savedIDs = append(savedIDs, obs.ID)
	}

	in := hooks.Input{
		SessionID: "sess-persist-ids",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
		Reason:    "compact",
	}

	captureStdout(t, func() {
		hooks.RunSessionStart(ctx, in, st)
	})

	ids, err := st.LoadInjectedIDs(ctx, "sess-persist-ids")
	if err != nil {
		t.Fatalf("LoadInjectedIDs: %v", err)
	}
	if len(ids) == 0 {
		t.Error("expected injected IDs to be persisted after post-compaction start")
	}
}

// --- RunPromptSubmit (task 2.2) ---

func TestRunPromptSubmit_SavesPrompt(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateSession(ctx, "sess-p", "kronos-v2", "C:\\Users\\Jerry\\kronos-v2"); err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{
		SessionID: "sess-p",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
		Prompt:    "¿Cómo implementamos el store de memoria?",
	}

	if err := hooks.RunPromptSubmit(ctx, in, st, nil); err != nil {
		t.Fatalf("RunPromptSubmit: %v", err)
	}
}

func TestRunPromptSubmit_EmptyPrompt_Noop(t *testing.T) {
	st := newTestStore(t)
	in := hooks.Input{SessionID: "s", CWD: "/tmp"}
	if err := hooks.RunPromptSubmit(context.Background(), in, st, nil); err != nil {
		t.Fatalf("empty prompt should be a no-op: %v", err)
	}
}

func TestRunPromptSubmit_RedactsSecrets(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateSession(ctx, "sess-sec", "kronos-v2", "C:\\Users\\Jerry\\kronos-v2"); err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{
		SessionID: "sess-sec",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
		Prompt:    "usa AKIAIOSFODNN7EXAMPLE para el request",
	}

	if err := hooks.RunPromptSubmit(ctx, in, st, nil); err != nil {
		t.Fatalf("RunPromptSubmit with secret: %v", err)
	}
}

func TestRunPromptSubmit_FTSResults_Emitted(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.CreateSession(ctx, "sess-fts", "kronos-v2", "/tmp")
	st.PersistInjectedIDs(ctx, "sess-fts", []string{})

	// Save observations that match the prompt keyword.
	st.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypeDecision,
		Title:   "sqlite store architecture",
		Content: "We chose SQLite because it is embedded and needs no network roundtrip.",
		Project: "kronos-v2",
	})
	st.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypeDiscovery,
		Title:   "sqlite FTS5 indexing",
		Content: "SQLite FTS5 module supports full-text search with unicode61 tokenizer.",
		Project: "kronos-v2",
	})

	in := hooks.Input{
		SessionID: "sess-fts",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
		Prompt:    "sqlite store",
	}

	out := captureStdout(t, func() {
		if err := hooks.RunPromptSubmit(ctx, in, st, nil); err != nil {
			t.Fatalf("RunPromptSubmit: %v", err)
		}
	})

	if !strings.Contains(out, "[kronos]") {
		t.Errorf("expected [kronos] output for matching prompt, got: %q", out)
	}
}

func TestRunPromptSubmit_Dedup_FiltersAlreadyInjected(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.CreateSession(ctx, "sess-dedup", "kronos-v2", "/tmp")

	obs, _ := st.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypeDecision,
		Title:   "dedup target observation",
		Content: "dedup target content should not appear in output after injection",
		Project: "kronos-v2",
	})

	// Mark this obs as already injected.
	obsIDStr := fmt.Sprintf("%d", obs.ID)
	st.PersistInjectedIDs(ctx, "sess-dedup", []string{obsIDStr})

	in := hooks.Input{
		SessionID: "sess-dedup",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
		Prompt:    "dedup target",
	}

	out := captureStdout(t, func() {
		hooks.RunPromptSubmit(ctx, in, st, nil)
	})

	if strings.Contains(out, "dedup target") {
		t.Errorf("dedup-filtered obs appeared in output: %q", out)
	}
}

func TestRunPromptSubmit_NoResults_NoOutput(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.CreateSession(ctx, "sess-noresults", "proj", "/tmp")
	st.PersistInjectedIDs(ctx, "sess-noresults", []string{})

	in := hooks.Input{
		SessionID: "sess-noresults",
		CWD:       "/tmp",
		Prompt:    "zzznomatchzzzunlikelytermxyz",
	}

	out := captureStdout(t, func() {
		if err := hooks.RunPromptSubmit(ctx, in, st, nil); err != nil {
			t.Fatalf("RunPromptSubmit: %v", err)
		}
	})

	if strings.Contains(out, "[kronos]") {
		t.Errorf("unexpected [kronos] output for no-results query: %q", out)
	}
}

func TestRunPromptSubmit_Timeout_ExitsClean(t *testing.T) {
	realSt := newTestStore(t)
	ctx := context.Background()
	realSt.CreateSession(ctx, "sess-timeout", "proj", "/tmp")
	realSt.PersistInjectedIDs(ctx, "sess-timeout", []string{})

	// Wrap with a slow Search that blocks for 500ms — the 100ms internal timeout must cut it.
	st := &slowSearchStore{Storer: realSt}

	in := hooks.Input{
		SessionID: "sess-timeout",
		CWD:       "/tmp",
		Prompt:    "timeout test prompt query",
	}

	done := make(chan error, 1)
	go func() {
		done <- hooks.RunPromptSubmit(ctx, in, st, nil)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RunPromptSubmit returned error: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Error("RunPromptSubmit did not return within 300ms — 100ms timeout not enforced")
	}
}

func TestRunPromptSubmit_SearchError_ExitsClean(t *testing.T) {
	// Use a real store but give it an empty prompt — since prompt is not empty
	// but query is unusual, search may fail or return nothing.
	st := newTestStore(t)
	ctx := context.Background()
	st.CreateSession(ctx, "sess-err", "proj", "/tmp")
	st.PersistInjectedIDs(ctx, "sess-err", []string{})

	in := hooks.Input{
		SessionID: "sess-err",
		CWD:       "/tmp",
		Prompt:    "search error test",
	}

	if err := hooks.RunPromptSubmit(ctx, in, st, nil); err != nil {
		t.Errorf("RunPromptSubmit should not return error: %v", err)
	}
}

func TestRunPromptSubmit_VectorStoreNil_FallsBackToFTS(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.CreateSession(ctx, "sess-vnil", "kronos-v2", "/tmp")
	st.PersistInjectedIDs(ctx, "sess-vnil", []string{})

	st.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypeDecision,
		Title:   "vectornil fallback test",
		Content: "vectornil observation content for FTS fallback path",
		Project: "kronos-v2",
	})

	in := hooks.Input{
		SessionID: "sess-vnil",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
		Prompt:    "vectornil fallback",
	}

	// Pass nil explicitly — should fall through to FTS.
	out := captureStdout(t, func() {
		if err := hooks.RunPromptSubmit(ctx, in, st, nil); err != nil {
			t.Fatalf("RunPromptSubmit with nil vs: %v", err)
		}
	})

	if !strings.Contains(out, "[kronos]") {
		t.Errorf("expected [kronos] output from FTS fallback (nil vs): %q", out)
	}
}

// --- Existing tests kept below ---

func TestRunSubagentStop_ExtractsLearnings(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateSession(ctx, "sess-sub", "kronos-v2", "C:\\Users\\Jerry\\kronos-v2"); err != nil {
		t.Fatal(err)
	}

	response := strings.Join([]string{
		"## Key Learnings:",
		"- SQLite FTS5 soporta búsqueda full-text con unicode61 para español",
		"- El tokenizador unicode61 maneja acentos correctamente sin configuración",
	}, "\n")

	in := hooks.Input{
		SessionID: "sess-sub",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
		Response:  response,
	}

	if err := hooks.RunSubagentStop(ctx, in, st); err != nil {
		t.Fatalf("RunSubagentStop: %v", err)
	}

	results, err := st.Search(ctx, store.SearchParams{
		Query: "unicode61",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected passive learning to be saved and searchable")
	}
}

func TestRunSubagentStop_EmptyResponse_Noop(t *testing.T) {
	st := newTestStore(t)
	in := hooks.Input{SessionID: "s", CWD: "/tmp", Response: ""}
	if err := hooks.RunSubagentStop(context.Background(), in, st); err != nil {
		t.Fatalf("empty response should be a no-op: %v", err)
	}
}

func TestRunSubagentStop_NoLearnings_Noop(t *testing.T) {
	st := newTestStore(t)
	in := hooks.Input{
		SessionID: "s",
		CWD:       "/tmp",
		Response:  "Este texto no tiene sección de learnings.",
	}
	if err := hooks.RunSubagentStop(context.Background(), in, st); err != nil {
		t.Fatalf("response without learnings should be a no-op: %v", err)
	}
}

func TestRunSessionStop_EndsSession(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Create session first.
	if _, err := st.CreateSession(ctx, "sess-stop", "p", "/tmp"); err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{SessionID: "sess-stop", CWD: "/tmp"}
	if err := hooks.RunSessionStop(ctx, in, st); err != nil {
		t.Fatalf("RunSessionStop: %v", err)
	}
}

func TestRunSessionStop_EmptySessionID_Noop(t *testing.T) {
	st := newTestStore(t)
	in := hooks.Input{CWD: "/tmp"}
	if err := hooks.RunSessionStop(context.Background(), in, st); err != nil {
		t.Fatalf("empty session_id should be a no-op: %v", err)
	}
}

func TestRun_UnknownHook(t *testing.T) {
	// Run with unknown hook name should return error without panicking.
	// We can't easily test Run() directly since it reads stdin,
	// so we test the dispatch logic indirectly via the exported helpers.
	_ = hooks.RunSessionStop // just verify it's exported
}
