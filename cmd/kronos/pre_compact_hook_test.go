package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

// TestNotifyPreCompactCapture_DaemonDown_NeverPanics confirma el contrato
// fire-and-forget: sin nada escuchando, no debe panicar ni bloquear —
// simplemente no hace nada.
func TestNotifyPreCompactCapture_DaemonDown_NeverPanics(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := ts.URL
	ts.Close()

	notifyPreCompactCapture(deadURL, hooks.Input{SessionID: "s1", TranscriptPath: "x.jsonl"})
}

// TestNotifyPreCompactCapture_MissingFields_NeverCallsDaemon confirma que
// sin session_id o transcript_path ni siquiera se intenta la request — no
// tiene sentido avisarle al daemon sin esos dos datos.
func TestNotifyPreCompactCapture_MissingFields_NeverCallsDaemon(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()

	notifyPreCompactCapture(ts.URL, hooks.Input{SessionID: "", TranscriptPath: "x.jsonl"})
	notifyPreCompactCapture(ts.URL, hooks.Input{SessionID: "s1", TranscriptPath: ""})

	if called {
		t.Error("no debería haber llamado al daemon sin session_id o transcript_path")
	}
}
