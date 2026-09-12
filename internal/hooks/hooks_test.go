package hooks_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/checkpoint"
	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// fixedVectorEmbedFn devuelve el vector fijo de la tabla si el texto coincide
// exacto, o un vector neutro por default — mismo patrón que
// internal/judge/judge_test.go, reescrito acá porque ese helper es privado a
// su paquete. Alcanza para controlar la similitud coseno de forma
// determinística sin depender de Ollama.
func fixedVectorEmbedFn(vectors map[string][]float32) embeddings.EmbeddingFunc {
	return func(_ context.Context, text string) ([]float32, error) {
		if v, ok := vectors[text]; ok {
			return v, nil
		}
		return []float32{0.5, 0.5}, nil
	}
}

// setupTempDataDir redirects platform.DataDir() to a fresh temp directory for
// the duration of the test. Returns the kronos sub-directory path.
// Skips on macOS because DataDir there uses a fixed ~/Library path with no env override.
func setupTempDataDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("macOS DataDir uses ~/Library/Application Support — not overridable via env")
	}
	base := t.TempDir()
	kronosDir := filepath.Join(base, "kronos")
	if err := os.MkdirAll(kronosDir, 0o755); err != nil {
		t.Fatalf("setupTempDataDir mkdir: %v", err)
	}
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LOCALAPPDATA", base)
	default:
		t.Setenv("XDG_DATA_HOME", base)
	}
	return kronosDir
}

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

// slowSearchStore wraps a real store and blocks Search until the context is
// cancelled. Used to test that runRecall respeta el timeout de
// config.Recall.TimeoutMs (default 800ms) en vez de esperar a que el store
// responda.
type slowSearchStore struct {
	store.Storer
}

func (s *slowSearchStore) Search(ctx context.Context, p store.SearchParams) ([]*store.SearchResult, error) {
	select {
	case <-time.After(3 * time.Second):
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// setupTempConfigDir redirige config.ConfigPath() a un directorio temporal
// para la duración del test — mismo patrón que setupTempDataDir, pero para
// XDG_CONFIG_HOME/APPDATA en vez de XDG_DATA_HOME/LOCALAPPDATA. Necesario
// para poder escribir un config.json de prueba con Recall custom sin tocar
// el config real de la máquina.
func setupTempConfigDir(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("macOS ConfigDir usa ~/Library/Application Support fijo — no overrideable por env")
	}
	base := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("APPDATA", base)
	default:
		t.Setenv("XDG_CONFIG_HOME", base)
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
	if !strings.Contains(out, `your session_id is "sess-signal"`) {
		t.Errorf("output missing explicit session_id echo — Claude Code doesn't pass session_id to MCP servers (anthropics/claude-code#41836), so this line is the only reliable source; got: %q", out)
	}
}

// TestRunSessionStart_EmptySessionID_NoSessionIDLine confirma que sin
// session_id no se imprime una línea vacía/rota ('your session_id is ""').
func TestRunSessionStart_EmptySessionID_NoSessionIDLine(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	in := hooks.Input{SessionID: "", CWD: "C:\\Users\\Jerry\\kronos-v2"}
	out := captureStdout(t, func() {
		if err := hooks.RunSessionStart(ctx, in, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	if strings.Contains(out, "your session_id is") {
		t.Errorf("no debería imprimir la línea de session_id sin un session_id real: %q", out)
	}
}

// TestRunSessionStart_NormalStart_InjectsRecentObs confirma el fix del bug
// real encontrado en vivo el 2026-09-03: antes, un arranque normal (resume/
// startup/clear — cualquier reason distinto de "compact") no inyectaba
// ningún contenido real, solo el aviso genérico de "llamá mem_search" — la
// propia sesión de trabajo de kronos-v2 llevaba 20 días sin que su digest se
// mostrara en ningún resume por esta razón exacta. Ahora un arranque normal
// también inyecta contenido real, igual que post-compactación.
func TestRunSessionStart_NormalStart_InjectsRecentObs(t *testing.T) {
	setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	// project.Detect(cwd) es lo que RunSessionStart usa de verdad para
	// resolver el nombre del proyecto — no coincide con un literal
	// "kronos-v2" en toda plataforma (en CI Linux/macOS, un path estilo
	// Windows resuelve distinto), así que se resuelve una vez y se reusa acá
	// y en el input.
	cwd := t.TempDir()
	projName := project.Detect(cwd).Name

	st.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypeDecision,
		Title:   "observación real de arranque normal",
		Content: "esto SÍ debe aparecer en un arranque normal, no solo post-compactación",
		Project: projName,
	})

	in := hooks.Input{
		SessionID: "sess-with-content",
		CWD:       cwd,
	}

	out := captureStdout(t, func() {
		hooks.RunSessionStart(ctx, in, st)
	})

	if !strings.Contains(out, "esto SÍ debe aparecer") {
		t.Errorf("un arranque normal debería inyectar observaciones recientes, igual que post-compactación: %q", out)
	}
}

// TestRunSessionStart_NormalStart_PrioritizesOwnSessionDigest confirma que,
// si esta sesión ya tiene un digest corriente (ver internal/hooks/digest.go),
// se muestra ese digest — es el hilo de continuidad real de ESTA
// conversación, más útil que la observación más reciente de cualquier otra
// sesión del proyecto.
func TestRunSessionStart_NormalStart_PrioritizesOwnSessionDigest(t *testing.T) {
	setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	cwd := t.TempDir()
	projName := project.Detect(cwd).Name

	if _, err := st.CreateSession(ctx, "sess-own-digest", projName, cwd); err != nil {
		t.Fatal(err)
	}

	// Observación reciente de OTRA sesión — no debería ganarle al digest propio.
	st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "obs de otra sesión", Content: "contenido de otra sesión",
		Project: projName,
	})
	if _, err := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeSession, Title: "Resumen en curso de la sesión",
		Content: "digest propio de esta sesión, debe salir primero", Project: projName,
		SessionID: "sess-own-digest", TopicKey: "session/sess-own-digest",
	}); err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{
		SessionID: "sess-own-digest",
		CWD:       cwd,
	}

	out := captureStdout(t, func() {
		hooks.RunSessionStart(ctx, in, st)
	})

	if !strings.Contains(out, "digest propio de esta sesión") {
		t.Errorf("debería inyectar el digest de la sesión propia: %q", out)
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

// TestRunSessionStart_NormalStart_PersistsEmptyIDs_WhenNothingToInject
// confirma que, sin observaciones ni checkpoint, el arranque normal sigue
// persistiendo el baseline vacío de siempre (sin esto, RunPromptSubmit no
// tendría con qué comparar para deduplicar la primera búsqueda real).
func TestRunSessionStart_NormalStart_PersistsEmptyIDs_WhenNothingToInject(t *testing.T) {
	setupTempDataDir(t)
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
		t.Errorf("expected 0 ids when there's nothing to inject, got %d", len(ids))
	}
}

// TestRunSessionStart_NormalStart_PersistsInjectedIDs confirma que, cuando SÍ
// hay contenido real para inyectar, los IDs quedan persistidos — mismo
// contrato que ya tenía post-compactación (RunPromptSubmit los usa para no
// repetir lo que ya se mostró al arrancar).
func TestRunSessionStart_NormalStart_PersistsInjectedIDs(t *testing.T) {
	setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	cwd := t.TempDir()
	projName := project.Detect(cwd).Name

	if _, err := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "obs a persistir", Content: "contenido cualquiera",
		Project: projName,
	}); err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{
		SessionID: "sess-persist-real",
		CWD:       cwd,
	}

	captureStdout(t, func() {
		hooks.RunSessionStart(ctx, in, st)
	})

	ids, err := st.LoadInjectedIDs(ctx, "sess-persist-real")
	if err != nil {
		t.Fatalf("LoadInjectedIDs: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("expected 1 injected id, got %d", len(ids))
	}
}

// --- RunPostCompaction (task 1.5) ---

func TestRunPostCompaction_PrintsSignalAndObs(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()

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
		CWD:       cwd,
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
	cwd, _ := os.Getwd()

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
		CWD:       cwd,
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
	cwd, _ := os.Getwd()

	for i := 0; i < 2; i++ {
		if _, err := st.SaveObservation(ctx, store.SaveParams{
			Type:    store.TypeDecision,
			Title:   fmt.Sprintf("persist ids test obs %d", i),
			Content: fmt.Sprintf("content for persist ids test observation %d injected", i),
			Project: "kronos-v2",
		}); err != nil {
			t.Fatal(err)
		}
	}

	in := hooks.Input{
		SessionID: "sess-persist-ids",
		CWD:       cwd,
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

	if err := hooks.RunPromptSubmit(ctx, in, st, nil, os.Stdout); err != nil {
		t.Fatalf("RunPromptSubmit: %v", err)
	}
}

// TestRunPromptSubmit_TouchesSessionActivity cubre el heartbeat que permite
// a la TUI distinguir una sesión con actividad reciente de una abandonada
// que nunca disparó SessionEnd (ver internal/tui sessionStatus).
func TestRunPromptSubmit_TouchesSessionActivity(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	sess, err := st.CreateSession(ctx, "sess-heartbeat", "kronos-v2", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	before := sess.LastActivityAt

	time.Sleep(1100 * time.Millisecond) // now() trunca a segundo — dar margen real

	in := hooks.Input{SessionID: "sess-heartbeat", CWD: "/tmp", Prompt: "otro prompt"}
	if err := hooks.RunPromptSubmit(ctx, in, st, nil, os.Stdout); err != nil {
		t.Fatalf("RunPromptSubmit: %v", err)
	}

	got, err := st.GetSession(ctx, "sess-heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastActivityAt.After(before) {
		t.Errorf("LastActivityAt no avanzó: before=%v after=%v", before, got.LastActivityAt)
	}
}

func TestRunPromptSubmit_EmptyPrompt_Noop(t *testing.T) {
	st := newTestStore(t)
	in := hooks.Input{SessionID: "s", CWD: "/tmp"}
	if err := hooks.RunPromptSubmit(context.Background(), in, st, nil, os.Stdout); err != nil {
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

	if err := hooks.RunPromptSubmit(ctx, in, st, nil, os.Stdout); err != nil {
		t.Fatalf("RunPromptSubmit with secret: %v", err)
	}
}

func TestRunPromptSubmit_FTSResults_Emitted(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-fts", "kronos-v2", cwd)
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
		CWD:       cwd,
		Prompt:    "sqlite store",
	}

	out := captureStdout(t, func() {
		if err := hooks.RunPromptSubmit(ctx, in, st, nil, os.Stdout); err != nil {
			t.Fatalf("RunPromptSubmit: %v", err)
		}
	})

	if !strings.Contains(out, "[kronos:relevante]") {
		t.Errorf("expected [kronos:relevante] output for matching prompt, got: %q", out)
	}
}

func TestRunPromptSubmit_Dedup_FiltersAlreadyInjected(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-dedup", "kronos-v2", cwd)

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
		CWD:       cwd,
		Prompt:    "dedup target",
	}

	out := captureStdout(t, func() {
		hooks.RunPromptSubmit(ctx, in, st, nil, os.Stdout)
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
		if err := hooks.RunPromptSubmit(ctx, in, st, nil, os.Stdout); err != nil {
			t.Fatalf("RunPromptSubmit: %v", err)
		}
	})

	if strings.Contains(out, "[kronos:relevante]") {
		t.Errorf("unexpected [kronos:relevante] output for no-results query: %q", out)
	}
}

func TestRunPromptSubmit_Timeout_ExitsClean(t *testing.T) {
	realSt := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()
	realSt.CreateSession(ctx, "sess-timeout", "kronos-v2", cwd)
	realSt.PersistInjectedIDs(ctx, "sess-timeout", []string{})

	// Wrap con un Search que bloquea 3s — el timeout de la fase FTS
	// (que este test configura en 1500ms; el default de producción es 5000ms)
	// tiene que cortarlo bastante antes. Usar os.Getwd() como CWD asegura que
	// project.Detect resuelva rápido vía git remote, así que la única demora
	// real es el corte por el deadline de la fase FTS en gatherRecallCandidates.
	st := &slowSearchStore{Storer: realSt}

	in := hooks.Input{
		SessionID: "sess-timeout",
		CWD:       cwd,
		Prompt:    "timeout test prompt query",
	}

	done := make(chan error, 1)
	go func() {
		done <- hooks.RunPromptSubmit(ctx, in, st, nil, os.Stdout)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RunPromptSubmit returned error: %v", err)
		}
	case <-time.After(2500 * time.Millisecond):
		t.Error("RunPromptSubmit did not return within 2500ms — el timeout de config.Recall (1500ms) no se aplicó")
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

	if err := hooks.RunPromptSubmit(ctx, in, st, nil, os.Stdout); err != nil {
		t.Errorf("RunPromptSubmit should not return error: %v", err)
	}
}

func TestRunPromptSubmit_VectorStoreNil_FallsBackToFTS(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-vnil", "kronos-v2", cwd)
	st.PersistInjectedIDs(ctx, "sess-vnil", []string{})

	st.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypeDecision,
		Title:   "vectornil fallback test",
		Content: "vectornil observation content for FTS fallback path",
		Project: "kronos-v2",
	})

	in := hooks.Input{
		SessionID: "sess-vnil",
		CWD:       cwd,
		Prompt:    "vectornil fallback",
	}

	// Pass nil explicitly — should fall through to FTS. vs=nil es exactamente
	// lo que devuelve embeddings.New cuando Ollama no responde (ver
	// embeddings.AutoFunc), así que esto también cubre el caso "provider caído".
	out := captureStdout(t, func() {
		if err := hooks.RunPromptSubmit(ctx, in, st, nil, os.Stdout); err != nil {
			t.Fatalf("RunPromptSubmit with nil vs: %v", err)
		}
	})

	if !strings.Contains(out, "[kronos:relevante]") {
		t.Errorf("expected [kronos:relevante] output from FTS fallback (nil vs): %q", out)
	}
}

// TestRunPromptSubmit_HighSimilarity_InjectsRespectingCharsLimit cubre el
// camino vectorial feliz: con un embedding fake que devuelve similitud
// coseno 1.0 (>= min_similarity), el bloque se inyecta con formato
// "[kronos:relevante]" y, con un chars_limit chico a propósito, no mete los
// 3 ítems candidatos — el presupuesto corta antes.
func TestRunPromptSubmit_HighSimilarity_InjectsRespectingCharsLimit(t *testing.T) {
	setupTempConfigDir(t)
	cfg := config.Default()
	cfg.Recall.CharsLimit = 120
	cfg.Recall.K = 3
	if err := cfg.Save(); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}

	st := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-highsim", "kronos-v2", cwd)
	st.PersistInjectedIDs(ctx, "sess-highsim", []string{})

	prompt := "cómo se implementó el store de sqlite"
	textA := "sqlite embebido observación uno"
	textB := "sqlite embebido observación dos"
	textC := "sqlite embebido observación tres"

	obsA, _ := st.SaveObservation(ctx, store.SaveParams{Type: store.TypeDecision, Title: "obs uno", Content: textA, Project: "kronos-v2"})
	obsB, _ := st.SaveObservation(ctx, store.SaveParams{Type: store.TypeDecision, Title: "obs dos", Content: textB, Project: "kronos-v2"})
	obsC, _ := st.SaveObservation(ctx, store.SaveParams{Type: store.TypeDecision, Title: "obs tres", Content: textC, Project: "kronos-v2"})

	vs, err := embeddings.NewInMemory(fixedVectorEmbedFn(map[string][]float32{
		prompt: {1, 0},
		textA:  {1, 0},
		textB:  {1, 0},
		textC:  {1, 0},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range []*store.Observation{obsA, obsB, obsC} {
		if err := vs.Index(ctx, o.ID, o.Content); err != nil {
			t.Fatal(err)
		}
	}

	in := hooks.Input{SessionID: "sess-highsim", CWD: cwd, Prompt: prompt}

	out := captureStdout(t, func() {
		if err := hooks.RunPromptSubmit(ctx, in, st, vs, os.Stdout); err != nil {
			t.Fatalf("RunPromptSubmit: %v", err)
		}
	})

	if !strings.Contains(out, "[kronos:relevante]") {
		t.Fatalf("esperaba bloque de relevancia, salida: %q", out)
	}
	if len(out) > 200 {
		t.Errorf("chars_limit=120 debería acotar el bloque, salió %d chars: %q", len(out), out)
	}
	if strings.Count(out, "- decision:") >= 3 {
		t.Errorf("chars_limit=120 debería haber cortado antes de meter los 3 ítems: %q", out)
	}
}

// TestRunPromptSubmit_LowSimilarity_NoInjection cubre el caso donde la
// similitud vectorial queda por debajo de min_similarity (0.62 default) —
// con fallback_fts desactivado para aislar el camino vectorial, no debe
// inyectarse nada.
func TestRunPromptSubmit_LowSimilarity_NoInjection(t *testing.T) {
	setupTempConfigDir(t)
	cfg := config.Default()
	cfg.Recall.FallbackFTS = false
	if err := cfg.Save(); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}

	st := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-lowsim", "kronos-v2", cwd)
	st.PersistInjectedIDs(ctx, "sess-lowsim", []string{})

	prompt := "consulta totalmente no relacionada"
	obsText := "contenido de una observación distinta"
	obs, _ := st.SaveObservation(ctx, store.SaveParams{Type: store.TypeDecision, Title: "obs no relacionada", Content: obsText, Project: "kronos-v2"})

	vs, err := embeddings.NewInMemory(fixedVectorEmbedFn(map[string][]float32{
		prompt:  {0, 1},
		obsText: {1, 0}, // ortogonal → similitud coseno 0.0, por debajo del umbral
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := vs.Index(ctx, obs.ID, obs.Content); err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{SessionID: "sess-lowsim", CWD: cwd, Prompt: prompt}

	out := captureStdout(t, func() {
		if err := hooks.RunPromptSubmit(ctx, in, st, vs, os.Stdout); err != nil {
			t.Fatalf("RunPromptSubmit: %v", err)
		}
	})

	if strings.Contains(out, "[kronos:relevante]") {
		t.Errorf("similitud por debajo del umbral no debería inyectar nada: %q", out)
	}
}

// TestRunPromptSubmit_RecallDisabled_NoInjection cubre recall.enabled=false:
// el hook debe comportarse exactamente como sin esta feature (nada de bloque
// de relevancia), aunque exista una observación que matchearía por FTS.
func TestRunPromptSubmit_RecallDisabled_NoInjection(t *testing.T) {
	setupTempConfigDir(t)
	cfg := config.Default()
	cfg.Recall.Enabled = false
	if err := cfg.Save(); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}

	st := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-recalloff", "kronos-v2", cwd)
	st.PersistInjectedIDs(ctx, "sess-recalloff", []string{})

	st.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypeDecision,
		Title:   "sqlite store architecture",
		Content: "We chose SQLite because it is embedded and needs no network roundtrip.",
		Project: "kronos-v2",
	})

	in := hooks.Input{SessionID: "sess-recalloff", CWD: cwd, Prompt: "sqlite store"}

	out := captureStdout(t, func() {
		if err := hooks.RunPromptSubmit(ctx, in, st, nil, os.Stdout); err != nil {
			t.Fatalf("RunPromptSubmit: %v", err)
		}
	})

	if out != "" {
		t.Errorf("recall.enabled=false debería dejar la salida sin cambios (vacía acá): %q", out)
	}
}

// --- Estrategia FTS-first (recalibración de recall) ---

// countingEmbedFn envuelve un embeddings.EmbeddingFunc y cuenta cuántas veces
// se invocó — permite verificar, sin tocar Ollama, si el camino vectorial
// llegó a dispararse o no.
type countingEmbedFn struct {
	mu    sync.Mutex
	calls int
	inner embeddings.EmbeddingFunc
}

func (c *countingEmbedFn) fn(ctx context.Context, text string) ([]float32, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.inner(ctx, text)
}

func (c *countingEmbedFn) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// searchCountingStore envuelve un *store.Store real y cuenta cuántas veces se
// llamó Search — permite verificar que un prompt trivial no gasta ni FTS.
type searchCountingStore struct {
	*store.Store
	mu    sync.Mutex
	calls int
}

func (s *searchCountingStore) Search(ctx context.Context, p store.SearchParams) ([]*store.SearchResult, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.Store.Search(ctx, p)
}

func (s *searchCountingStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestRunPromptSubmit_FTSHit_NeverCallsEmbeddings cubre (a) de la
// recalibración: con FTS devolviendo al menos min_fts_results, el camino
// vectorial NUNCA se invoca — la razón de ser de FTS-first es no pagar el
// round-trip de Ollama (800ms-6s medidos en esta máquina) cuando FTS ya
// resolvió el prompt en milisegundos.
func TestRunPromptSubmit_FTSHit_NeverCallsEmbeddings(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-fts-hit", "kronos-v2", cwd)
	st.PersistInjectedIDs(ctx, "sess-fts-hit", []string{})

	st.SaveObservation(ctx, store.SaveParams{
		Type:    store.TypeDecision,
		Title:   "sqlite store architecture",
		Content: "We chose SQLite because it is embedded and needs no network roundtrip.",
		Project: "kronos-v2",
	})

	counting := &countingEmbedFn{inner: fixedVectorEmbedFn(nil)}
	vs, err := embeddings.NewInMemory(counting.fn)
	if err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{SessionID: "sess-fts-hit", CWD: cwd, Prompt: "sqlite store"}

	out := captureStdout(t, func() {
		if err := hooks.RunPromptSubmit(ctx, in, st, vs, os.Stdout); err != nil {
			t.Fatalf("RunPromptSubmit: %v", err)
		}
	})

	if !strings.Contains(out, "[kronos:relevante]") {
		t.Fatalf("esperaba bloque de relevancia vía FTS, salida: %q", out)
	}
	if got := counting.count(); got != 0 {
		t.Errorf("FTS con resultados no debería llamar al embedding — se llamó %d veces", got)
	}
}

// TestRunPromptSubmit_FTSMiss_VectorAboveThreshold_Injects cubre (b): sin
// nada que matchee por FTS, el camino vectorial oportunista se intenta y, con
// similitud por encima de min_similarity, inyecta.
func TestRunPromptSubmit_FTSMiss_VectorAboveThreshold_Injects(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-vector-hit", "kronos-v2", cwd)
	st.PersistInjectedIDs(ctx, "sess-vector-hit", []string{})

	prompt := "zzzunmatchedqueryzzz por FTS"
	obsText := "contenido totalmente distinto en las palabras, matchea solo por vector"
	obs, _ := st.SaveObservation(ctx, store.SaveParams{Type: store.TypeDiscovery, Title: "obs solo vector", Content: obsText, Project: "kronos-v2"})

	vs, err := embeddings.NewInMemory(fixedVectorEmbedFn(map[string][]float32{
		prompt:  {1, 0},
		obsText: {1, 0}, // mismo vector → similitud coseno 1.0
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := vs.Index(ctx, obs.ID, obs.Content); err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{SessionID: "sess-vector-hit", CWD: cwd, Prompt: prompt}

	out := captureStdout(t, func() {
		if err := hooks.RunPromptSubmit(ctx, in, st, vs, os.Stdout); err != nil {
			t.Fatalf("RunPromptSubmit: %v", err)
		}
	})

	if !strings.Contains(out, "[kronos:relevante]") {
		t.Errorf("FTS vacío + similitud alta debería inyectar vía vector, salida: %q", out)
	}
}

// TestRunPromptSubmit_FTSMiss_VectorBelowThreshold_NoInjection cubre (c): sin
// FTS y con similitud vectorial por debajo de min_similarity (0.62 default),
// no debe inyectarse nada.
func TestRunPromptSubmit_FTSMiss_VectorBelowThreshold_NoInjection(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-vector-low", "kronos-v2", cwd)
	st.PersistInjectedIDs(ctx, "sess-vector-low", []string{})

	prompt := "zzzunmatchedqueryzzz por FTS"
	obsText := "contenido ortogonal, no matchea ni por FTS ni por vector"
	obs, _ := st.SaveObservation(ctx, store.SaveParams{Type: store.TypeDiscovery, Title: "obs no relacionada", Content: obsText, Project: "kronos-v2"})

	vs, err := embeddings.NewInMemory(fixedVectorEmbedFn(map[string][]float32{
		prompt:  {0, 1},
		obsText: {1, 0}, // ortogonal → similitud coseno 0.0
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := vs.Index(ctx, obs.ID, obs.Content); err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{SessionID: "sess-vector-low", CWD: cwd, Prompt: prompt}

	out := captureStdout(t, func() {
		if err := hooks.RunPromptSubmit(ctx, in, st, vs, os.Stdout); err != nil {
			t.Fatalf("RunPromptSubmit: %v", err)
		}
	})

	if strings.Contains(out, "[kronos:relevante]") {
		t.Errorf("similitud por debajo del umbral no debería inyectar nada: %q", out)
	}
}

// TestRunPromptSubmit_TrivialPrompt_NoSearchAtAll cubre (d): un prompt
// trivial (charla/saludo) no debe gastar ni FTS ni embeddings — caso real
// medido: "hola qué hora es" trajo ruido en vez de nada.
func TestRunPromptSubmit_TrivialPrompt_NoSearchAtAll(t *testing.T) {
	base := newTestStore(t)
	st := &searchCountingStore{Store: base}
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-trivial", "kronos-v2", cwd)
	st.PersistInjectedIDs(ctx, "sess-trivial", []string{})

	counting := &countingEmbedFn{inner: fixedVectorEmbedFn(nil)}
	vs, err := embeddings.NewInMemory(counting.fn)
	if err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{SessionID: "sess-trivial", CWD: cwd, Prompt: "hola qué hora es"}

	out := captureStdout(t, func() {
		if err := hooks.RunPromptSubmit(ctx, in, st, vs, os.Stdout); err != nil {
			t.Fatalf("RunPromptSubmit: %v", err)
		}
	})

	if strings.Contains(out, "[kronos:relevante]") {
		t.Errorf("prompt trivial no debería inyectar nada: %q", out)
	}
	if got := st.count(); got != 0 {
		t.Errorf("prompt trivial no debería llamar a Search — se llamó %d veces", got)
	}
	if got := counting.count(); got != 0 {
		t.Errorf("prompt trivial no debería llamar al embedding — se llamó %d veces", got)
	}
}

// sleepingEmbedFnForQuery simula un provider (Ollama) colgado — pero solo
// para queryText (el prompt que dispara runRecall). Indexar el documento de
// prueba con el mismo EmbeddingFunc (chromem usa una única función para
// indexar y consultar) necesita resolver rápido, si no el propio setup del
// test se cuelga 5s antes de llegar siquiera a RunPromptSubmit — solo el
// texto de la consulta real debe demorarse, igual que un round-trip lento a
// Ollama. Respeta la cancelación de ctx, igual que un client HTTP real con
// contexto (ver slowSearchStore, mismo patrón).
func sleepingEmbedFnForQuery(queryText string) embeddings.EmbeddingFunc {
	return func(ctx context.Context, text string) ([]float32, error) {
		if text != queryText {
			return []float32{1, 0}, nil
		}
		select {
		case <-time.After(5 * time.Second):
			return []float32{1, 0}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// TestRunPromptSubmit_VectorTimeout_RespectsBudget cubre (e): si el camino
// vectorial se cuelga, RunPromptSubmit no debe bloquear más allá del
// presupuesto de timeout_ms — se corta y sigue sin injectar (FTS ya dio
// vacío) ni devolver error.
func TestRunPromptSubmit_VectorTimeout_RespectsBudget(t *testing.T) {
	setupTempConfigDir(t)
	cfg := config.Default()
	cfg.Recall.TimeoutMs = 300
	if err := cfg.Save(); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}

	st := newTestStore(t)
	ctx := context.Background()
	cwd, _ := os.Getwd()

	st.CreateSession(ctx, "sess-vector-timeout", "kronos-v2", cwd)
	st.PersistInjectedIDs(ctx, "sess-vector-timeout", []string{})

	obs, _ := st.SaveObservation(ctx, store.SaveParams{Type: store.TypeDiscovery, Title: "obs cualquiera", Content: "contenido cualquiera para indexar", Project: "kronos-v2"})

	prompt := "zzzunmatchedqueryzzz sin fts"
	vs, err := embeddings.NewInMemory(sleepingEmbedFnForQuery(prompt))
	if err != nil {
		t.Fatal(err)
	}
	if err := vs.Index(ctx, obs.ID, obs.Content); err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{SessionID: "sess-vector-timeout", CWD: cwd, Prompt: prompt}

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- hooks.RunPromptSubmit(ctx, in, st, vs, os.Stdout)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RunPromptSubmit returned error: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
			t.Errorf("elapsed=%v — no debería superar bastante el timeout_ms=300 configurado", elapsed)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Error("RunPromptSubmit no respetó timeout_ms=300 — el provider colgado bloqueó más de 1500ms")
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

// --- current_session.txt persistence ---

// storerWithPending envuelve un *store.Store real y le agrega PendingCount()
// — alcanza para satisfacer el chequeo `interface{ PendingCount() int }` que
// usa printBacklogWarnings, sin tener que levantar un DualStore/Postgres real.
type storerWithPending struct {
	*store.Store
	pending int
}

func (s *storerWithPending) PendingCount() int { return s.pending }

// TestRunSessionStart_WarnsOnSyncBacklog verifica que un backlog de sync por
// encima del umbral se avise proactivo en SessionStart, en vez de quedar
// invisible salvo que alguien pregunte mem_doctor explícitamente.
func TestRunSessionStart_WarnsOnSyncBacklog(t *testing.T) {
	setupTempDataDir(t)
	base := newTestStore(t)
	st := &storerWithPending{Store: base, pending: 150}

	in := hooks.Input{SessionID: "sess-backlog", CWD: t.TempDir()}
	out := captureStdout(t, func() {
		if err := hooks.RunSessionStart(context.Background(), in, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	if !strings.Contains(out, "150") || !strings.Contains(out, "sincronizar") {
		t.Errorf("expected sync backlog warning mentioning 150 pending ops, got: %s", out)
	}
}

// TestRunSessionStart_NoWarningBelowThreshold verifica que no se emite aviso
// cuando el backlog está por debajo del umbral (ruido innecesario).
func TestRunSessionStart_NoWarningBelowThreshold(t *testing.T) {
	setupTempDataDir(t)
	base := newTestStore(t)
	st := &storerWithPending{Store: base, pending: 5}

	in := hooks.Input{SessionID: "sess-nobacklog", CWD: t.TempDir()}
	out := captureStdout(t, func() {
		if err := hooks.RunSessionStart(context.Background(), in, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	if strings.Contains(out, "sincronizar") {
		t.Errorf("did not expect sync backlog warning below threshold, got: %s", out)
	}
}

// TestRunSessionStart_WritesSessionIDToFile verifies that session-start persists
// the session ID to current_session.txt so the pre-tool-use gate can resolve it.
func TestRunSessionStart_WritesSessionIDToFile(t *testing.T) {
	setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	cwd := t.TempDir()
	in := hooks.Input{SessionID: "sess-file-write", CWD: cwd}
	captureStdout(t, func() {
		if err := hooks.RunSessionStart(ctx, in, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	projName := project.Detect(cwd).Name
	sessPath, err := platform.CurrentSessionPath(projName)
	if err != nil {
		t.Fatalf("CurrentSessionPath: %v", err)
	}
	data, err := os.ReadFile(sessPath)
	if err != nil {
		t.Fatalf("current_session file not written at %s: %v", sessPath, err)
	}
	if got := string(data); got != "sess-file-write" {
		t.Errorf("current_session file = %q, want %q", got, "sess-file-write")
	}
}

// TestRunSessionStart_EmptySessionID_DoesNotWriteFile verifies that no file is
// written when session_id is absent from the hook payload.
func TestRunSessionStart_EmptySessionID_DoesNotWriteFile(t *testing.T) {
	setupTempDataDir(t)
	st := newTestStore(t)

	cwd := t.TempDir()
	in := hooks.Input{SessionID: "", CWD: cwd}
	captureStdout(t, func() {
		hooks.RunSessionStart(context.Background(), in, st)
	})

	sessPath, err := platform.CurrentSessionPath(project.Detect(cwd).Name)
	if err != nil {
		t.Fatalf("CurrentSessionPath: %v", err)
	}
	if _, err := os.ReadFile(sessPath); err == nil {
		t.Error("current_session file must not be written when session_id is empty")
	}
}

// TestRunSessionStart_SessionIDPersistedInDB verifies that session-start stores
// the session_id in the database so the gate can look it up.
func TestRunSessionStart_SessionIDPersistedInDB(t *testing.T) {
	setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	in := hooks.Input{SessionID: "sess-db-persist", CWD: t.TempDir()}
	captureStdout(t, func() {
		if err := hooks.RunSessionStart(ctx, in, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	sess, err := st.GetSession(ctx, "sess-db-persist")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess == nil {
		t.Fatal("session not found in DB after RunSessionStart")
	}
	if sess.ID != "sess-db-persist" {
		t.Errorf("session.ID = %q, want %q", sess.ID, "sess-db-persist")
	}
}

// TestRunSessionStop_DeletesFile_WhenOwner verifies that session-stop deletes
// current_session.txt when its content matches the stopping session ID.
// TestRunSessionStop_DoesNotDeleteFile reproduce el bug real de fondo: Stop
// dispara una vez POR TURNO (no solo al final real de la sesión). Antes
// borraba current_session_<proyecto>.txt en cada disparo — así que apenas
// terminaba el primer turno de una conversación larga, el archivo
// desaparecía y cualquier lookup posterior (mem_search buscando la sesión
// activa) dejaba de encontrarlo, pese a que la conversación seguía activa.
// Ahora Stop no toca el archivo en absoluto.
func TestRunSessionStop_DoesNotDeleteFile(t *testing.T) {
	setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	const sid = "sess-stop-owner"
	cwd := t.TempDir()
	filePath, err := platform.CurrentSessionPath(project.Detect(cwd).Name)
	if err != nil {
		t.Fatalf("CurrentSessionPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte(sid), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	st.CreateSession(ctx, sid, "p", "/tmp")
	if err := hooks.RunSessionStop(ctx, hooks.Input{SessionID: sid, CWD: cwd}, st); err != nil {
		t.Fatalf("RunSessionStop: %v", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("current_session file no debería borrarse — Stop dispara por turno, no solo al final: %v", err)
	}
	if string(data) != sid {
		t.Errorf("current_session file = %q, want %q", data, sid)
	}
}

// TestRunSessionStop_FileAbsent_Noop verifies graceful handling when
// current_session.txt does not exist.
func TestRunSessionStop_FileAbsent_Noop(t *testing.T) {
	setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	st.CreateSession(ctx, "sess-nofile", "p", "/tmp")
	if err := hooks.RunSessionStop(ctx, hooks.Input{SessionID: "sess-nofile"}, st); err != nil {
		t.Fatalf("RunSessionStop with absent file should not error: %v", err)
	}
}

// TestRunSessionStop_DoesNotEndSession reproduce el bug real de fondo: Stop
// dispara una vez por turno, no solo al final real de la conversación. Antes
// llamaba EndSession(ended_at=now) en cada disparo — confirmado en vivo dos
// veces: una sesión propia marcada como terminada pese a seguir en uso
// activo, y una sesión de Jerry marcada como cerrada 5 minutos después de
// arrancar. "La sesión terminó" ahora es responsabilidad exclusiva de
// mem_session_summary/mem_session_end — una señal explícita, no un hook que
// dispara todo el tiempo.
func TestRunSessionStop_DoesNotEndSession(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateSession(ctx, "sess-stop", "p", "/tmp"); err != nil {
		t.Fatal(err)
	}

	in := hooks.Input{SessionID: "sess-stop", CWD: "/tmp"}
	if err := hooks.RunSessionStop(ctx, in, st); err != nil {
		t.Fatalf("RunSessionStop: %v", err)
	}

	sess, err := st.GetSession(ctx, "sess-stop")
	if err != nil {
		t.Fatal(err)
	}
	if sess == nil || sess.EndedAt != nil {
		t.Errorf("Stop no debería setear ended_at — got sess=%+v", sess)
	}
}

// TestRunSessionStop_SessionNeverCreated_NoError — RunSessionStop no debe
// fallar ni paniquear si SessionStart nunca logró crear la sesión (ej. el
// daemon compartido reiniciándose por trabajo en OTRO proyecto).
func TestRunSessionStop_SessionNeverCreated_NoError(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// nunca se llama CreateSession — simula que SessionStart falló en silencio.
	in := hooks.Input{SessionID: "sess-nunca-creada", CWD: "/tmp"}
	if err := hooks.RunSessionStop(ctx, in, st); err != nil {
		t.Errorf("RunSessionStop no debería fallar aunque la sesión nunca se haya creado: %v", err)
	}
}

func TestRunSessionStop_EmptySessionID_Noop(t *testing.T) {
	st := newTestStore(t)
	in := hooks.Input{CWD: "/tmp"}
	if err := hooks.RunSessionStop(context.Background(), in, st); err != nil {
		t.Fatalf("empty session_id should be a no-op: %v", err)
	}
}

// TestRunSessionStop_AutoSavesCheckpointWhenNoSummary reproduce el gap real
// que Jerry señaló: cuando la sesión termina por compactación, PreCompact
// deja una red de seguridad (checkpoint auto-generado) si el agente se
// olvidó de llamar mem_session_summary. Pero un cierre NORMAL (Stop, sin
// compactar) no tenía ninguna red — si el agente se olvidaba, no quedaba
// nada. Mismo patrón que PreCompact acá.
func TestRunSessionStop_AutoSavesCheckpointWhenNoSummary(t *testing.T) {
	dataDir := setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	cwd := t.TempDir()
	projName := project.Detect(cwd).Name

	if _, err := st.CreateSession(ctx, "sess-stop-nosummary", projName, cwd); err != nil {
		t.Fatal(err)
	}

	if err := hooks.RunSessionStop(ctx, hooks.Input{SessionID: "sess-stop-nosummary", CWD: cwd}, st); err != nil {
		t.Fatalf("RunSessionStop: %v", err)
	}

	cp, err := checkpoint.Load(dataDir, projName)
	if err != nil {
		t.Fatal(err)
	}
	if cp == nil {
		t.Fatal("Stop debería autoguardar un checkpoint de respaldo cuando la sesión termina sin mem_session_summary")
	}
}

// TestRunSessionStop_NoCheckpointWhenSummaryAlreadySaved confirma que si el
// agente sí llamó mem_session_summary (que deja un Summary no vacío en la
// sesión vía EndSession), Stop no agrega ruido de "sesión sin resumen" —
// ya hay uno real.
func TestRunSessionStop_NoCheckpointWhenSummaryAlreadySaved(t *testing.T) {
	dataDir := setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	cwd := t.TempDir()
	projName := project.Detect(cwd).Name

	if _, err := st.CreateSession(ctx, "sess-stop-hassummary", projName, cwd); err != nil {
		t.Fatal(err)
	}
	// simula lo que hace mem_session_summary: EndSession con resumen real.
	if err := st.EndSession(ctx, "sess-stop-hassummary", "## Objetivo\nresumen real de la sesión"); err != nil {
		t.Fatal(err)
	}

	if err := hooks.RunSessionStop(ctx, hooks.Input{SessionID: "sess-stop-hassummary", CWD: cwd}, st); err != nil {
		t.Fatalf("RunSessionStop: %v", err)
	}

	if cp, _ := checkpoint.Load(dataDir, projName); cp != nil {
		t.Errorf("no debería autoguardar checkpoint cuando ya hay un resumen real, got: %+v", cp)
	}
}

// TestRunSessionStop_DoesNotOverwriteExistingCheckpoint — si ya hay un
// checkpoint activo (ej. de una compactación previa en la misma sesión de
// trabajo), Stop no debe pisarlo con el fallback genérico.
func TestRunSessionStop_DoesNotOverwriteExistingCheckpoint(t *testing.T) {
	dataDir := setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	cwd := t.TempDir()
	projName := project.Detect(cwd).Name

	if _, err := st.CreateSession(ctx, "sess-stop-hascp", projName, cwd); err != nil {
		t.Fatal(err)
	}
	real := checkpoint.State{Task: "checkpoint real de una compactación previa", Project: projName}
	if err := checkpoint.Save(dataDir, projName, real); err != nil {
		t.Fatal(err)
	}

	if err := hooks.RunSessionStop(ctx, hooks.Input{SessionID: "sess-stop-hascp", CWD: cwd}, st); err != nil {
		t.Fatalf("RunSessionStop: %v", err)
	}

	cp, err := checkpoint.Load(dataDir, projName)
	if err != nil {
		t.Fatal(err)
	}
	if cp == nil || cp.Task != "checkpoint real de una compactación previa" {
		t.Errorf("checkpoint existente no debería pisarse, got: %+v", cp)
	}
}

func TestRun_UnknownHook(t *testing.T) {
	// Run with unknown hook name should return error without panicking.
	// We can't easily test Run() directly since it reads stdin,
	// so we test the dispatch logic indirectly via the exported helpers.
	_ = hooks.RunSessionStop // just verify it's exported
}

// --- RunPreToolUse (task 3.4) ---

// captureStderr redirects os.Stderr during fn, returns captured string.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	r.Close()
	return buf.String()
}

// errStore is a fake Storer that returns an error on GetSession y en
// CountObservations — el gate consulta CountObservations antes que
// GetSession, así que "DB unavailable" tiene que fallar-abierto ahí también.
type errStore struct {
	store.Storer
}

func (e *errStore) CountObservations(_ context.Context, _ string) (int, error) {
	return 0, fmt.Errorf("db unavailable")
}

func (e *errStore) GetSession(_ context.Context, _ string) (*store.Session, error) {
	return nil, fmt.Errorf("db unavailable")
}

// gateCWD crea un directorio temporal con .kronos/config.json fijando
// project_name — así project.Detect(cwd) resuelve exactamente al nombre
// pedido, sin depender del remote git real del repo donde corren los tests
// (que RunPreToolUse SÍ ejercita vía project.Detect(in.CWD) para el chequeo
// de min_observations).
func gateCWD(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	kdir := filepath.Join(dir, ".kronos")
	if err := os.MkdirAll(kdir, 0o755); err != nil {
		t.Fatalf("gateCWD mkdir: %v", err)
	}
	data := fmt.Sprintf(`{"project_name": %q}`, name)
	if err := os.WriteFile(filepath.Join(kdir, "config.json"), []byte(data), 0o644); err != nil {
		t.Fatalf("gateCWD write config: %v", err)
	}
	return dir
}

// seedObservations guarda n observaciones de relleno en el proyecto dado —
// usado para superar gate.min_observations (default 5) en los tests que
// necesitan que el gate NO se salte por "proyecto sin nada que buscar".
func seedObservations(t *testing.T, st *store.Store, project string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		_, err := st.SaveObservation(ctx, store.SaveParams{
			Type:    store.TypeDiscovery,
			Title:   fmt.Sprintf("relleno %d", i),
			Content: fmt.Sprintf("observación de relleno %d para superar min_observations", i),
			Project: project,
		})
		if err != nil {
			t.Fatalf("seedObservations: %v", err)
		}
	}
}

func TestRunPreToolUse_NoSearchYet_WarnMode(t *testing.T) {
	t.Setenv("KRONOS_GATE_BLOCK", "")
	hooks.ResetGatedTools()
	st := newTestStore(t)
	ctx := context.Background()
	seedObservations(t, st, "proj", 5)
	st.CreateSession(ctx, "sess-gate-warn", "proj", "/tmp")

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-warn", ToolName: "Edit", CWD: gateCWD(t, "proj")}
	stderr := captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if !strings.Contains(stderr, "[kronos]") {
		t.Errorf("expected [kronos] warning on stderr, got: %q", stderr)
	}
	if exitCode != nil {
		t.Errorf("exitFn should NOT be called in warn mode, got code %d", *exitCode)
	}
	// El session_id va embebido literal en el mensaje: mem_search no recibe
	// el session_id real de Claude Code (issue anthropics/claude-code#41836)
	// y con sesiones concurrentes del mismo proyecto puede acreditar la
	// búsqueda a la sesión equivocada. Dárselo acá, en el momento exacto
	// del bloqueo, elimina la adivinanza.
	if !strings.Contains(stderr, `session_id="sess-gate-warn"`) {
		t.Errorf("expected the real session_id embedded in the gate message, got: %q", stderr)
	}
}

func TestRunPreToolUse_AfterSearch_Pass(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedObservations(t, st, "proj", 5)
	st.CreateSession(ctx, "sess-gate-pass", "proj", "/tmp")
	st.IncrementSearchCount(ctx, "sess-gate-pass")

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-pass", ToolName: "Edit", CWD: gateCWD(t, "proj")}
	stderr := captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if strings.Contains(stderr, "[kronos]") {
		t.Errorf("no warning expected after search, got: %q", stderr)
	}
	if exitCode != nil {
		t.Errorf("exitFn should not be called, got code %d", *exitCode)
	}
}

// TestRunPreToolUse_FewObservations_Skips cubre el caso medido: un proyecto
// con menos de gate.min_observations (default 5) no tiene nada contra qué
// medir "ya buscaste acá" — el gate se salta incluso en modo bloqueo.
func TestRunPreToolUse_FewObservations_Skips(t *testing.T) {
	t.Setenv("KRONOS_GATE_BLOCK", "1")
	hooks.ResetGatedTools()
	st := newTestStore(t)
	ctx := context.Background()
	seedObservations(t, st, "proyecto-chico", 3)
	st.CreateSession(ctx, "sess-gate-few-obs", "proyecto-chico", "/tmp")

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-few-obs", ToolName: "Edit", CWD: gateCWD(t, "proyecto-chico")}
	stderr := captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if strings.Contains(stderr, "[kronos]") {
		t.Errorf("proyecto con 3 observaciones no debería disparar el gate, got: %q", stderr)
	}
	if exitCode != nil {
		t.Errorf("exitFn no debería llamarse con 3 observaciones (< min_observations), got code %d", *exitCode)
	}
}

// TestRunPreToolUse_EnoughObservations_Blocks es el contraste directo del
// anterior: 10 observaciones (>= min_observations default 5) sí bloquean.
func TestRunPreToolUse_EnoughObservations_Blocks(t *testing.T) {
	t.Setenv("KRONOS_GATE_BLOCK", "1")
	hooks.ResetGatedTools()
	st := newTestStore(t)
	ctx := context.Background()
	seedObservations(t, st, "proyecto-grande", 10)
	st.CreateSession(ctx, "sess-gate-enough-obs", "proyecto-grande", "/tmp")

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-enough-obs", ToolName: "Edit", CWD: gateCWD(t, "proyecto-grande")}
	captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if exitCode == nil {
		t.Error("exitFn debería llamarse con 10 observaciones (>= min_observations)")
	} else if *exitCode != 2 {
		t.Errorf("exitFn called with code %d, want 2", *exitCode)
	}
}

// TestRunPreToolUse_ConfigMinObservations_Respected verifica que
// gate.min_observations en config.json se respeta cuando no hay env var.
func TestRunPreToolUse_ConfigMinObservations_Respected(t *testing.T) {
	setupTempConfigDir(t)
	cfg := config.Default()
	cfg.Gate.MinObservations = 2
	if err := cfg.Save(); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}
	t.Setenv("KRONOS_GATE_BLOCK", "1")
	hooks.ResetGatedTools()

	st := newTestStore(t)
	ctx := context.Background()
	seedObservations(t, st, "proyecto-config", 2)
	st.CreateSession(ctx, "sess-gate-config-min", "proyecto-config", "/tmp")

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-config-min", ToolName: "Edit", CWD: gateCWD(t, "proyecto-config")}
	captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if exitCode == nil {
		t.Error("con gate.min_observations=2 en config y 2 observaciones, el gate debería bloquear")
	}
}

// TestRunPreToolUse_EnvBlockOverridesConfig verifica que KRONOS_GATE_BLOCK
// gana sobre gate.block del config cuando está seteada — aunque la config
// diga block=true, la env en false debe ganar.
func TestRunPreToolUse_EnvBlockOverridesConfig(t *testing.T) {
	setupTempConfigDir(t)
	cfg := config.Default()
	cfg.Gate.Block = true
	if err := cfg.Save(); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}
	t.Setenv("KRONOS_GATE_BLOCK", "0")
	hooks.ResetGatedTools()

	st := newTestStore(t)
	ctx := context.Background()
	seedObservations(t, st, "proyecto-env-wins", 5)
	st.CreateSession(ctx, "sess-gate-env-wins", "proyecto-env-wins", "/tmp")

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-env-wins", ToolName: "Edit", CWD: gateCWD(t, "proyecto-env-wins")}
	stderr := captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if !strings.Contains(stderr, "[kronos]") {
		t.Errorf("se esperaba el warning (modo no-block), got: %q", stderr)
	}
	if exitCode != nil {
		t.Errorf("KRONOS_GATE_BLOCK=0 debería ganarle a gate.block=true del config, got exit code %d", *exitCode)
	}
}

// TestRunPreToolUse_ConfigDisablesGate verifica que gate.enabled=false en
// config (sin KRONOS_PRETOOL_GATE seteada) desactiva el gate por completo.
func TestRunPreToolUse_ConfigDisablesGate(t *testing.T) {
	setupTempConfigDir(t)
	cfg := config.Default()
	cfg.Gate.Enabled = false
	if err := cfg.Save(); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}
	hooks.ResetGatedTools()

	st := newTestStore(t)
	ctx := context.Background()
	seedObservations(t, st, "proyecto-gate-off-config", 10)
	st.CreateSession(ctx, "sess-gate-off-config", "proyecto-gate-off-config", "/tmp")

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-off-config", ToolName: "Edit", CWD: gateCWD(t, "proyecto-gate-off-config")}
	stderr := captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if strings.Contains(stderr, "[kronos]") {
		t.Errorf("gate.enabled=false debería suprimir el warning, got: %q", stderr)
	}
	if exitCode != nil {
		t.Errorf("exitFn no debería llamarse con gate.enabled=false, got code %d", *exitCode)
	}
}

func TestRunPreToolUse_NonGatedTool_Pass(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	st.CreateSession(ctx, "sess-gate-read", "proj", "/tmp")
	// No search — but tool is Read (not gated).

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-read", ToolName: "Read"}
	stderr := captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if strings.Contains(stderr, "[kronos]") {
		t.Errorf("Read tool should not trigger gate, got: %q", stderr)
	}
	if exitCode != nil {
		t.Errorf("exitFn should not be called for Read tool, got code %d", *exitCode)
	}
}

// TestRunPreToolUse_UnknownProject_Pass reproduce el bypass que antes vivía
// solo en el wrapper bash (kronos-gate.sh) — sin proyecto detectado, no tiene
// sentido bloquear por "no buscaste en este proyecto todavía".
func TestRunPreToolUse_UnknownProject_Pass(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	st.CreateSession(ctx, "sess-gate-unknown", "p", "/tmp")
	// sin search — el gate debería saltar igual por proyecto no detectado.

	t.Setenv("KRONOS_GATE_BLOCK", "1")
	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-unknown", ToolName: "Edit", CWD: filepath.Join(t.TempDir(), "!!!")}
	stderr := captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if strings.Contains(stderr, "[kronos]") {
		t.Errorf("proyecto unknown no debería disparar el gate, got: %q", stderr)
	}
	if exitCode != nil {
		t.Errorf("exitFn no debería llamarse con proyecto unknown, got code %d", *exitCode)
	}
}

func TestRunPreToolUse_EmptySessionID_Pass(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "", ToolName: "Edit"}
	stderr := captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if strings.Contains(stderr, "[kronos]") {
		t.Errorf("empty session_id should not trigger gate, got: %q", stderr)
	}
	if exitCode != nil {
		t.Errorf("exitFn should not be called for empty session, got code %d", *exitCode)
	}
}

func TestRunPreToolUse_GateOff_Pass(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	st.CreateSession(ctx, "sess-gate-off", "proj", "/tmp")

	t.Setenv("KRONOS_PRETOOL_GATE", "off")

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-off", ToolName: "Edit"}
	stderr := captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if strings.Contains(stderr, "[kronos]") {
		t.Errorf("gate=off should suppress warning, got: %q", stderr)
	}
	if exitCode != nil {
		t.Errorf("exitFn should not be called when gate is off, got code %d", *exitCode)
	}
}

func TestRunPreToolUse_BlockMode(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedObservations(t, st, "proj", 5)
	st.CreateSession(ctx, "sess-gate-block", "proj", "/tmp")

	t.Setenv("KRONOS_GATE_BLOCK", "1")
	hooks.ResetGatedTools()

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-block", ToolName: "Edit", CWD: gateCWD(t, "proj")}
	captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if exitCode == nil {
		t.Error("exitFn should be called in block mode")
	} else if *exitCode != 2 {
		t.Errorf("exitFn called with code %d, want 2", *exitCode)
	}
}

func TestRunPreToolUse_DBUnavailable_FailOpen(t *testing.T) {
	ctx := context.Background()

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-dberr", ToolName: "Edit"}
	stderr := captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, &errStore{})
	})

	if strings.Contains(stderr, "[kronos]") {
		t.Errorf("DB error should fail-open (no warning), got: %q", stderr)
	}
	if exitCode != nil {
		t.Errorf("exitFn should not be called on DB error, got code %d", *exitCode)
	}
}

// --- RunPreToolUse: gate.satisfied_by_injection ---

// TestRunPreToolUse_SatisfiedByInjection_CoreBlockItems_NoBlock cubre (a):
// una sesión cuyo InjectedObservationIDs ya trae IDs (simulando que
// SessionStart, vía el bloque core, ya inyectó items de PROYECTO — ver
// printCoreBlock/injectContinuity en session_start.go) no bloquea, aunque
// nunca haya llamado mem_search.
func TestRunPreToolUse_SatisfiedByInjection_CoreBlockItems_NoBlock(t *testing.T) {
	t.Setenv("KRONOS_GATE_BLOCK", "1")
	hooks.ResetGatedTools()
	st := newTestStore(t)
	ctx := context.Background()
	seedObservations(t, st, "proyecto-injected", 5)
	st.CreateSession(ctx, "sess-gate-injected", "proyecto-injected", "/tmp")

	obs, err := st.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "decision ya inyectada por el core block",
		Content: "esto simula un item de proyecto que el bloque core ya mostró en SessionStart",
		Project: "proyecto-injected",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PersistInjectedIDs(ctx, "sess-gate-injected", []string{fmt.Sprintf("%d", obs.ID)}); err != nil {
		t.Fatal(err)
	}

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-injected", ToolName: "Edit", CWD: gateCWD(t, "proyecto-injected")}
	stderr := captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if strings.Contains(stderr, "[kronos]") {
		t.Errorf("sesión ya informada por el bloque core no debería disparar el warning, got: %q", stderr)
	}
	if exitCode != nil {
		t.Errorf("exitFn no debería llamarse: la sesión ya recibió memoria inyectada, got code %d", *exitCode)
	}
}

// TestRunPreToolUse_NoInjection_NoSearch_Blocks cubre (b): una sesión sin
// InjectedObservationIDs (bloque core vacío o desactivado) y sin
// mem_search — sigue bloqueando en modo bloqueo, igual que antes de
// satisfied_by_injection.
func TestRunPreToolUse_NoInjection_NoSearch_Blocks(t *testing.T) {
	t.Setenv("KRONOS_GATE_BLOCK", "1")
	hooks.ResetGatedTools()
	st := newTestStore(t)
	ctx := context.Background()
	seedObservations(t, st, "proyecto-noinjection", 5)
	st.CreateSession(ctx, "sess-gate-noinjection", "proyecto-noinjection", "/tmp")

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-noinjection", ToolName: "Edit", CWD: gateCWD(t, "proyecto-noinjection")}
	captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if exitCode == nil {
		t.Error("sin inyección ni búsqueda, el gate debería bloquear")
	} else if *exitCode != 2 {
		t.Errorf("exitFn called with code %d, want 2", *exitCode)
	}
}

// TestRunPreToolUse_FewObservations_SkipsEvenIfSatisfiedByInjectionDisabled
// cubre (c): gate.min_observations sigue ganando primero — un proyecto con
// pocas observaciones no bloquea, incluso con satisfied_by_injection=false
// (ninguna de las dos señales debería importar todavía).
func TestRunPreToolUse_FewObservations_SkipsEvenIfSatisfiedByInjectionDisabled(t *testing.T) {
	setupTempConfigDir(t)
	cfg := config.Default()
	cfg.Gate.SatisfiedByInjection = false
	if err := cfg.Save(); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}
	t.Setenv("KRONOS_GATE_BLOCK", "1")
	hooks.ResetGatedTools()

	st := newTestStore(t)
	ctx := context.Background()
	seedObservations(t, st, "proyecto-chico-satisfecho", 3)
	st.CreateSession(ctx, "sess-gate-few-satisfied-off", "proyecto-chico-satisfecho", "/tmp")

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-few-satisfied-off", ToolName: "Edit", CWD: gateCWD(t, "proyecto-chico-satisfecho")}
	stderr := captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if strings.Contains(stderr, "[kronos]") {
		t.Errorf("proyecto con 3 observaciones no debería disparar el gate, got: %q", stderr)
	}
	if exitCode != nil {
		t.Errorf("exitFn no debería llamarse con 3 observaciones (< min_observations), aunque satisfied_by_injection=false, got code %d", *exitCode)
	}
}

// TestRunPreToolUse_SatisfiedByInjection_Disabled_StillBlocks verifica que
// gate.satisfied_by_injection=false vuelve al comportamiento anterior: una
// sesión con IDs inyectados pero sin mem_search bloquea igual.
func TestRunPreToolUse_SatisfiedByInjection_Disabled_StillBlocks(t *testing.T) {
	setupTempConfigDir(t)
	cfg := config.Default()
	cfg.Gate.SatisfiedByInjection = false
	if err := cfg.Save(); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}
	t.Setenv("KRONOS_GATE_BLOCK", "1")
	hooks.ResetGatedTools()

	st := newTestStore(t)
	ctx := context.Background()
	seedObservations(t, st, "proyecto-satisfied-off", 5)
	st.CreateSession(ctx, "sess-gate-satisfied-off", "proyecto-satisfied-off", "/tmp")
	if err := st.PersistInjectedIDs(ctx, "sess-gate-satisfied-off", []string{"1"}); err != nil {
		t.Fatal(err)
	}

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-satisfied-off", ToolName: "Edit", CWD: gateCWD(t, "proyecto-satisfied-off")}
	captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if exitCode == nil {
		t.Error("con satisfied_by_injection=false, la inyección no debería alcanzar para saltar el gate")
	} else if *exitCode != 2 {
		t.Errorf("exitFn called with code %d, want 2", *exitCode)
	}
}

// TestRunPreToolUse_EnvSatisfiedByInjectionOverridesConfig verifica que
// KRONOS_GATE_SATISFIED_BY_INJECTION gana sobre la config, igual que los otros
// knobs del gate: se puede apagar el atajo desde el entorno sin tocar
// config.json (config dice true, env dice "0" → vuelve a bloquear).
func TestRunPreToolUse_EnvSatisfiedByInjectionOverridesConfig(t *testing.T) {
	setupTempConfigDir(t)
	cfg := config.Default()
	cfg.Gate.SatisfiedByInjection = true
	if err := cfg.Save(); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}
	t.Setenv("KRONOS_GATE_BLOCK", "1")
	t.Setenv("KRONOS_GATE_SATISFIED_BY_INJECTION", "0")
	hooks.ResetGatedTools()

	st := newTestStore(t)
	ctx := context.Background()
	seedObservations(t, st, "proyecto-env-satisfied", 5)
	st.CreateSession(ctx, "sess-gate-env-satisfied", "proyecto-env-satisfied", "/tmp")
	if err := st.PersistInjectedIDs(ctx, "sess-gate-env-satisfied", []string{"1"}); err != nil {
		t.Fatal(err)
	}

	var exitCode *int
	hooks.SetExitFn(func(code int) { exitCode = &code })
	defer hooks.SetExitFn(nil)

	in := hooks.Input{SessionID: "sess-gate-env-satisfied", ToolName: "Edit", CWD: gateCWD(t, "proyecto-env-satisfied")}
	captureStderr(t, func() {
		hooks.RunPreToolUse(ctx, in, st)
	})

	if exitCode == nil {
		t.Error("con la env en 0 la inyección no debería alcanzar para saltar el gate")
	} else if *exitCode != 2 {
		t.Errorf("exitFn called with code %d, want 2", *exitCode)
	}
}

// --- PreCompact ---

func TestRunPreCompact_PrintsWarning(t *testing.T) {
	setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	out := captureStdout(t, func() {
		if err := hooks.RunPreCompact(ctx, hooks.Input{CWD: t.TempDir()}, st); err != nil {
			t.Fatalf("RunPreCompact: %v", err)
		}
	})

	if !strings.Contains(out, "mem_session_summary") {
		t.Errorf("esperaba un aviso mencionando mem_session_summary, got: %q", out)
	}
}

func TestRunPreCompact_AutoSavesCheckpointWhenNoneExists(t *testing.T) {
	dataDir := setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	cwd := t.TempDir()
	projName := project.Detect(cwd).Name

	// confirmar que no había checkpoint antes
	if cp, _ := checkpoint.Load(dataDir, projName); cp != nil {
		t.Fatalf("no debería haber checkpoint todavía: %+v", cp)
	}

	captureStdout(t, func() {
		hooks.RunPreCompact(ctx, hooks.Input{CWD: cwd}, st)
	})

	cp, err := checkpoint.Load(dataDir, projName)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cp == nil {
		t.Fatal("RunPreCompact debería autoguardar un checkpoint de respaldo cuando no hay uno activo")
	}
}

func TestRunPreCompact_DoesNotOverwriteExistingCheckpoint(t *testing.T) {
	dataDir := setupTempDataDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	cwd := t.TempDir()
	projName := project.Detect(cwd).Name

	real := checkpoint.State{Task: "tarea real en curso", NextStep: "seguir con X", Project: projName}
	if err := checkpoint.Save(dataDir, projName, real); err != nil {
		t.Fatal(err)
	}

	captureStdout(t, func() {
		hooks.RunPreCompact(ctx, hooks.Input{CWD: cwd}, st)
	})

	cp, err := checkpoint.Load(dataDir, projName)
	if err != nil {
		t.Fatal(err)
	}
	if cp == nil || cp.Task != "tarea real en curso" {
		t.Errorf("checkpoint real no debería pisarse, got: %+v", cp)
	}
}

// TestRunPostToolUse_RecordsCall reproduce el gap real: Activity.RecordSignificantAction
// (internal/mcp/activity.go) existía pero nunca se llamaba porque PostToolUse
// solo estaba cableado a code-review-graph, nunca a kronos — cero seguimiento
// persistente de qué tools se usaban.
func TestRunPostToolUse_RecordsCall(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd := t.TempDir()
	projName := project.Detect(cwd).Name

	in := hooks.Input{SessionID: "s1", ToolName: "Edit", CWD: cwd}
	if err := hooks.RunPostToolUse(ctx, in, st); err != nil {
		t.Fatalf("RunPostToolUse: %v", err)
	}

	stats, err := st.ToolUsageStats(ctx, projName, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].ToolName != "Edit" || stats[0].Count != 1 {
		t.Errorf("esperaba 1 registro de Edit, obtuve: %+v", stats)
	}
}

func TestRunPostToolUse_EmptyToolName_NoOp(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	in := hooks.Input{SessionID: "s1", ToolName: "", CWD: t.TempDir()}
	if err := hooks.RunPostToolUse(ctx, in, st); err != nil {
		t.Fatalf("RunPostToolUse: %v", err)
	}

	stats, err := st.ToolUsageStats(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 0 {
		t.Errorf("esperaba 0 registros con ToolName vacío, obtuve: %+v", stats)
	}
}
