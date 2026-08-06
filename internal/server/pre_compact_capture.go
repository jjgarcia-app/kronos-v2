package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
)

// handlePreCompactCapture recibe el aviso de PreCompact que cmd/kronos/hook.go
// dispara fire-and-forget (timeout corto del lado del cliente, no espera
// respuesta) y responde 202 de inmediato — el trabajo real (leer el
// transcript, juzgar con el LLM local, guardar si corresponde) corre en una
// goroutine con su propio context.Background(), desacoplado de la request
// HTTP que lo disparó. Así el hook de PreCompact nunca se demora esperando
// esto, y la compactación de Claude Code no se retrasa un solo milisegundo
// por esta feature.
func (srv *Server) handlePreCompactCapture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var in struct {
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		CWD            string `json:"cwd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusAccepted)

	if in.SessionID == "" || in.TranscriptPath == "" {
		return
	}
	st, llmClient := srv.st, srv.captureLLM
	go func() {
		_ = hooks.RunPreCompactCapture(context.Background(), st, llmClient, in.SessionID, in.TranscriptPath, in.CWD)
	}()
}
