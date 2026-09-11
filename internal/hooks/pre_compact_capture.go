package hooks

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/llm"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/secrets"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
	"github.com/jjgarcia-app/kronos-v2/internal/transcript"
)

// excerptMaxChars caps how much transcript text goes into the LLM prompt —
// llama3.2:1b has a small context window, and this only needs the last
// stretch of conversation, not the whole session.
const excerptMaxChars = 4000

// minExcerptChars: below this, there's not enough real dialogue to judge —
// a session that's mostly tool calls with little text would waste an Ollama
// round-trip for nothing.
const minExcerptChars = 200

// RunPreCompactCapture is the async counterpart to RunPreCompact's stdout
// warning: instead of relying on the agent to notice "COMPACTACIÓN
// INMINENTE" and call mem_session_summary itself, this reads the transcript
// directly and asks the local LLM whether the recent conversation documents
// something save-worthy — same criteria as the mem_save protocol (bugfix
// w/ root cause, decision, discovery, config change) — and saves it as
// type: passive if so, with zero dependency on the agent's cooperation.
//
// Runs OFF the hook's critical path (dispatched by the daemon in a
// goroutine, see internal/server/pre_compact_capture.go) — deliberately not
// wired into RunPreCompact itself, which must stay fast and never block
// Claude Code's actual compaction.
func RunPreCompactCapture(ctx context.Context, st store.Storer, llmClient *llm.Client, sessionID, transcriptPath, cwd string) error {
	if llmClient == nil {
		return nil // Ollama no disponible — sin captura pasiva, no es error
	}

	excerpt, err := transcript.TailExcerpt(transcriptPath, excerptMaxChars)
	if err != nil || len(strings.TrimSpace(excerpt)) < minExcerptChars {
		return nil // nada real que juzgar
	}

	finding, err := llmClient.ExtractFinding(ctx, excerpt)
	if err != nil || finding == nil || !finding.Found {
		return nil
	}

	title := strings.TrimSpace(secrets.Redact(finding.Title))
	content := strings.TrimSpace(secrets.Redact(finding.Content))
	if title == "" || content == "" {
		return nil
	}

	proj := project.Detect(cwd)
	// Igual que en el digest: observations tiene FK a sessions, así que si la
	// fila de la sesión no existe el INSERT falla y la captura se pierde.
	ensureSession(ctx, st, sessionID, proj.Name, cwd)
	if _, err = st.SaveObservation(ctx, store.SaveParams{
		SessionID: sessionID,
		Type:      store.TypePassive,
		Title:     title,
		Content:   content,
		Project:   proj.Name,
	}); err != nil {
		slog.Warn("captura pasiva: no se pudo guardar la observación",
			"session_id", sessionID, "project", proj.Name, "err", err)
		return err
	}
	return nil
}
