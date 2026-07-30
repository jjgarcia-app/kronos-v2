package hooks_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// submitPrompt runs RunPromptSubmit once and returns what it wrote to w.
func submitPrompt(t *testing.T, ctx context.Context, st *store.Store, sessionID, cwd, prompt string) string {
	t.Helper()
	var buf bytes.Buffer
	in := hooks.Input{SessionID: sessionID, CWD: cwd, Prompt: prompt}
	if err := hooks.RunPromptSubmit(ctx, in, st, nil, &buf); err != nil {
		t.Fatalf("RunPromptSubmit: %v", err)
	}
	return buf.String()
}

// TestRunPromptSubmit_NudgesAfterThreshold confirma que el aviso de guardado
// aparece en la salida del hook una vez que se cruza el umbral de prompts
// (nudgeEveryN=15) sin ningún mem_save, y no antes.
func TestRunPromptSubmit_NudgesAfterThreshold(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd := t.TempDir()
	sessionID := "s-nudge-1"

	if _, err := st.CreateSession(ctx, sessionID, "p", cwd); err != nil {
		t.Fatal(err)
	}

	var last string
	for i := 1; i <= 15; i++ {
		last = submitPrompt(t, ctx, st, sessionID, cwd, "prompt")
	}

	if !strings.Contains(last, "recordatorio de memoria") {
		t.Errorf("prompt #15 sin save debería incluir el nudge, salida: %q", last)
	}
}

// TestRunPromptSubmit_NudgesAgainAfterSave_LongUnsavedStretch es la
// regresión real: antes, CountSessionObservations == 0 era la condición de
// disparo — apenas había UN mem_save en la sesión, el nudge quedaba en
// silencio para siempre, sin importar cuánto trabajo sin guardar viniera
// después (el caso real: un barrido de 51 documentos que nunca se guardó
// porque una sesión anterior ya había guardado algo). Ahora el conteo es
// "prompts desde el último save", así que el nudge debe volver a disparar
// tras un tramo largo sin guardar, incluso habiendo guardado antes.
func TestRunPromptSubmit_NudgesAgainAfterSave_LongUnsavedStretch(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd := t.TempDir()
	sessionID := "s-nudge-2"

	if _, err := st.CreateSession(ctx, sessionID, "p", cwd); err != nil {
		t.Fatal(err)
	}

	// Un save temprano en la sesión.
	if _, err := st.SaveObservation(ctx, store.SaveParams{
		SessionID: sessionID, Type: store.TypeDiscovery, Title: "obs temprana", Content: "c", Project: "p",
	}); err != nil {
		t.Fatal(err)
	}
	// created_at tiene precisión de segundo — cruzar el límite a propósito
	// para que "prompts desde el último save" cuente solo lo posterior.
	time.Sleep(1100 * time.Millisecond)

	var last string
	for i := 1; i <= 15; i++ {
		last = submitPrompt(t, ctx, st, sessionID, cwd, "prompt tras la sesión larga sin guardar")
	}

	if !strings.Contains(last, "recordatorio de memoria") {
		t.Errorf("tras 15 prompts sin guardar DESPUÉS del save temprano, debería nudgear de nuevo; salida: %q", last)
	}
}

// TestRunPromptSubmit_NoNudgeBeforeThreshold confirma que no hay ruido antes
// de llegar al umbral.
func TestRunPromptSubmit_NoNudgeBeforeThreshold(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	cwd := t.TempDir()
	sessionID := "s-nudge-3"

	if _, err := st.CreateSession(ctx, sessionID, "p", cwd); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 14; i++ {
		out := submitPrompt(t, ctx, st, sessionID, cwd, "prompt")
		if strings.Contains(out, "recordatorio de memoria") {
			t.Fatalf("nudge disparó antes de tiempo en el prompt #%d", i)
		}
	}
}
