package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
)

// digestUpdateOverhead es el margen que se le suma al presupuesto de
// enriquecimiento por LLM (digest.timeout_ms, ver hooks.DigestEnrichTimeout)
// para dimensionar el timeout de la goroutine completa — cubre la lectura
// del transcript y las escrituras a la base, que no pasan por ese timeout
// pero corren en la misma goroutine. Generoso: esas operaciones son
// milisegundos, esto solo evita que el margen sea exactamente cero.
const digestUpdateOverhead = 5 * time.Second

// digestUpdateTimeout acota el trabajo de actualizar el digest corriente de
// la sesión (leer transcript + juzgar con el LLM) — corre en una goroutine
// desacoplada de la request HTTP que lo disparó (mismo patrón que
// pre-compact-capture, ver pre_compact_capture.go), así que esto nunca
// demora la respuesta real de UserPromptSubmit. digest.timeout_ms (default
// 60s) es deliberadamente más alto que llm.timeout_ms (30s): esta
// actualización periódica corre async en el daemon, así que esperar más no
// le cuesta nada al usuario, y un pico de carga que haría fallar la llamada
// a los 30s tiene margen para terminar igual.
func digestUpdateTimeout(cfg config.Config) time.Duration {
	return hooks.DigestEnrichTimeout(cfg) + digestUpdateOverhead
}

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

	// Digest corriente de la sesión: chequeo barato (sin tocar el LLM) antes
	// de decidir si vale la pena lanzar la goroutine — IsDigestDue evita
	// spawnear una goroutine + tocar el LLM client en CADA prompt cuando la
	// gran mayoría de las veces todavía no corresponde actualizar.
	cfg := srv.captureConfig()
	if in.SessionID != "" && in.TranscriptPath != "" && hooks.IsDigestDue(r.Context(), srv.st, cfg, in.SessionID, in.CWD) {
		st := srv.st
		sessionID, transcriptPath, cwd := in.SessionID, in.TranscriptPath, in.CWD
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), digestUpdateTimeout(cfg))
			defer cancel()
			// getCaptureLLM adentro de la goroutine — si hace falta
			// reintentar el ping a Ollama (ver Server.getCaptureLLM), ese
			// costo no debe demorar nada del lado de la request original.
			llmClient := srv.getCaptureLLM(ctx)
			_ = hooks.MaybeUpdateDigest(ctx, st, cfg, llmClient, sessionID, transcriptPath, cwd, false)
		}()
	}
}
