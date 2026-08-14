package hooks

import (
	"context"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/llm"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/secrets"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
	"github.com/jjgarcia-app/kronos-v2/internal/transcript"
)

// digestUpdateInterval: cuánto tiempo mínimo tiene que pasar desde la
// última actualización del digest de una sesión (o desde que arrancó, si
// nunca se actualizó) antes de intentar otra. No tan seguido como para
// gastar un round-trip de LLM en cada prompt, pero suficiente para que el
// hilo de lo que se viene trabajando quede armado para cuando se consulte
// después — mismo umbral que ya usa la TUI para "sesión inactiva" (ver
// internal/tui/view.go sessionStaleAfter), reusado acá por la misma lógica:
// suficiente tiempo real de trabajo como para que valga la pena.
const digestUpdateInterval = 20 * time.Minute

// digestTopicKey identifica la observación "resumen en curso" de una
// sesión — topic_key estable por sesión, así SaveObservation la actualiza
// en el lugar (upsert, sube revision_count) en vez de crear una fila nueva
// en cada actualización periódica.
func digestTopicKey(sessionID string) string {
	return "session/" + sessionID
}

// IsDigestDue chequea, sin tocar el LLM, si corresponde intentar actualizar
// el digest de una sesión — barato (una lectura), pensado para llamarse en
// cada prompt sin costo real, y para que el caller decida si vale la pena
// construir un cliente LLM (que en el camino de fallback local implica un
// ping a Ollama) antes de intentar la actualización de verdad.
func IsDigestDue(ctx context.Context, st store.Storer, sessionID, cwd string) bool {
	if sessionID == "" {
		return false
	}
	proj := project.Detect(cwd)
	existing, err := st.GetByTopicKey(ctx, proj.Name, digestTopicKey(sessionID))
	if err != nil {
		return false
	}
	return existing == nil || time.Since(existing.UpdatedAt) >= digestUpdateInterval
}

// MaybeUpdateDigest actualiza el resumen corriendo de una sesión si
// corresponde (mismo chequeo que IsDigestDue, repetido acá para que sea
// seguro llamar directo sin depender de que el caller ya haya chequeado) —
// lee el transcript reciente, le pide al LLM local que extienda el resumen
// anterior con lo nuevo, y lo guarda con upsert por topic_key.
//
// Es el complemento periódico de RunPreCompactCapture: en vez de una sola
// foto justo antes de compactar, esto arma un hilo continuo mientras la
// sesión sigue activa — para que mem_search/mem_context encuentren en qué
// se está trabajando sin depender de que el agente llame mem_save a mano.
//
// Fail-open en cada paso: nunca debe interrumpir el hot path de
// UserPromptSubmit por esto.
func MaybeUpdateDigest(ctx context.Context, st store.Storer, llmClient *llm.Client, sessionID, transcriptPath, cwd string) error {
	if llmClient == nil || sessionID == "" || transcriptPath == "" {
		return nil
	}

	proj := project.Detect(cwd)
	topicKey := digestTopicKey(sessionID)

	existing, err := st.GetByTopicKey(ctx, proj.Name, topicKey)
	if err != nil {
		return nil
	}
	if existing != nil && time.Since(existing.UpdatedAt) < digestUpdateInterval {
		return nil // todavía no toca
	}

	excerpt, err := transcript.TailExcerpt(transcriptPath, excerptMaxChars)
	if err != nil || len(strings.TrimSpace(excerpt)) < minExcerptChars {
		return nil // nada real que resumir desde la última vez
	}

	previous := ""
	if existing != nil {
		previous = existing.Content
	}

	update, err := llmClient.UpdateDigest(ctx, previous, excerpt)
	if err != nil || update == nil {
		return nil
	}
	content := strings.TrimSpace(secrets.Redact(update.Content))
	if content == "" || content == strings.TrimSpace(previous) {
		return nil // el LLM dijo "nada nuevo" — no pisar con una revisión idéntica
	}

	_, err = st.SaveObservation(ctx, store.SaveParams{
		SessionID: sessionID,
		Type:      store.TypeSession,
		Title:     "Resumen en curso de la sesión",
		Content:   content,
		Project:   proj.Name,
		TopicKey:  topicKey,
	})
	return err
}
