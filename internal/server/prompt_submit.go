package server

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
)

// handlePromptSubmit procesa el hook UserPromptSubmit usando el store y
// vector store YA ABIERTOS del daemon compartido, en vez de que cada
// prompt del usuario dispare un proceso corto (`kronos hook prompt-submit`)
// que abre su propia conexión a Postgres y su propio vector store desde
// cero — contención innecesaria contra Ollama en cada mensaje, multiplicada
// por cuántas sesiones de Claude Code estén abiertas a la vez.
//
// cmd/kronos/hook.go intenta este endpoint primero; si el daemon no
// responde a tiempo (caído, arrancando), cae al camino local original —
// este endpoint es una optimización, no una dependencia dura.
func (srv *Server) handlePromptSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var in hooks.Input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}

	var buf bytes.Buffer
	if err := hooks.RunPromptSubmit(r.Context(), in, srv.st, srv.vs, &buf); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}
