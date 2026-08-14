package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
)

// TestNotifyPreCompactCapture_SendsSessionAndTranscriptPath confirma el
// payload real que le llega al daemon — session_id/transcript_path/cwd tal
// cual vinieron del hook input.
func TestNotifyPreCompactCapture_SendsSessionAndTranscriptPath(t *testing.T) {
	var gotBody map[string]string
	var gotMethod string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()

	notifyPreCompactCapture(ts.URL, hooks.Input{
		SessionID:      "s1",
		TranscriptPath: `C:\transcripts\s1.jsonl`,
		CWD:            `C:\repo`,
	})

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotBody["session_id"] != "s1" {
		t.Errorf("session_id = %q", gotBody["session_id"])
	}
	if gotBody["transcript_path"] != `C:\transcripts\s1.jsonl` {
		t.Errorf("transcript_path = %q", gotBody["transcript_path"])
	}
	if gotBody["cwd"] != `C:\repo` {
		t.Errorf("cwd = %q", gotBody["cwd"])
	}
}

// TestNotifyPreCompactCapture_DaemonDown_ReturnsFalse reproduce el bug real:
// antes esto no devolvía nada, así que el caller no tenía forma de saber que
// el daemon no aceptó la captura y activar un fallback — la captura se
// perdía en silencio para siempre. Ahora el valor de retorno es justo la
// señal que runPreCompactHook usa para decidir si corre el fallback local.
func TestNotifyPreCompactCapture_DaemonDown_ReturnsFalse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := ts.URL
	ts.Close()

	if got := notifyPreCompactCapture(deadURL, hooks.Input{SessionID: "s1", TranscriptPath: "x.jsonl"}); got {
		t.Error("notifyPreCompactCapture con daemon caído debería devolver false")
	}
}

// TestNotifyPreCompactCapture_DaemonAccepts_ReturnsTrue confirma el camino
// feliz: daemon responde 202, no hace falta fallback.
func TestNotifyPreCompactCapture_DaemonAccepts_ReturnsTrue(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()

	if got := notifyPreCompactCapture(ts.URL, hooks.Input{SessionID: "s1", TranscriptPath: "x.jsonl"}); !got {
		t.Error("notifyPreCompactCapture con daemon sano debería devolver true")
	}
}

// TestNotifyPreCompactCapture_MissingFields_NeverCallsDaemon confirma que
// sin session_id o transcript_path ni siquiera se intenta la request — no
// tiene sentido avisarle al daemon sin esos dos datos. Devuelve true (no
// hace falta fallback: no hay nada que capturar).
func TestNotifyPreCompactCapture_MissingFields_NeverCallsDaemon(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()

	if got := notifyPreCompactCapture(ts.URL, hooks.Input{SessionID: "", TranscriptPath: "x.jsonl"}); !got {
		t.Error("sin session_id debería devolver true (nada que capturar)")
	}
	if got := notifyPreCompactCapture(ts.URL, hooks.Input{SessionID: "s1", TranscriptPath: ""}); !got {
		t.Error("sin transcript_path debería devolver true (nada que capturar)")
	}

	if called {
		t.Error("no debería haber llamado al daemon sin session_id o transcript_path")
	}
}

// TestRunLocalPreCompactCaptureFallback_MissingFields_NoopSinPanic confirma
// que el fallback local es seguro llamarlo con datos incompletos (mismo
// guard que notifyPreCompactCapture) — no debe panicar ni intentar nada.
func TestRunLocalPreCompactCaptureFallback_MissingFields_NoopSinPanic(t *testing.T) {
	runLocalPreCompactCaptureFallback(config.Config{}, nil, hooks.Input{SessionID: "", TranscriptPath: "x.jsonl"})
	runLocalPreCompactCaptureFallback(config.Config{}, nil, hooks.Input{SessionID: "s1", TranscriptPath: ""})
}
