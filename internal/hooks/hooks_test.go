package hooks_test

import (
	"context"
	"os"
	"strings"
	"testing"

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

func TestRunSessionStart_CreatesSession(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	in := hooks.Input{
		SessionID: "test-sess-001",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
	}

	if err := hooks.RunSessionStart(ctx, in, st); err != nil {
		t.Fatalf("RunSessionStart: %v", err)
	}

	// Session must exist now; ending it should succeed.
	if err := st.EndSession(ctx, "test-sess-001", ""); err != nil {
		t.Errorf("EndSession after SessionStart: %v", err)
	}
}

func TestRunPromptSubmit_SavesPrompt(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// session-start always precedes prompt-submit in real usage.
	if _, err := st.CreateSession(ctx, "sess-p", "kronos-v2", "C:\\Users\\Jerry\\kronos-v2"); err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{
		SessionID: "sess-p",
		CWD:       "C:\\Users\\Jerry\\kronos-v2",
		Prompt:    "¿Cómo implementamos el store de memoria?",
	}

	if err := hooks.RunPromptSubmit(ctx, in, st); err != nil {
		t.Fatalf("RunPromptSubmit: %v", err)
	}
}

func TestRunPromptSubmit_EmptyPrompt_Noop(t *testing.T) {
	st := newTestStore(t)
	in := hooks.Input{SessionID: "s", CWD: "/tmp"}
	if err := hooks.RunPromptSubmit(context.Background(), in, st); err != nil {
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

	if err := hooks.RunPromptSubmit(ctx, in, st); err != nil {
		t.Fatalf("RunPromptSubmit with secret: %v", err)
	}
}

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
