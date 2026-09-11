package hooks

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/llm"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/secrets"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
	"github.com/jjgarcia-app/kronos-v2/internal/transcript"
)

// digestDefaultIntervalMinutes es el fallback cuando cfg.Digest.IntervalMinutes
// no está seteado (config vieja, o Digest{} zero-value en un test) — mismo
// valor que el default de config.Default().Digest.IntervalMinutes.
const digestDefaultIntervalMinutes = 20

// digestMaxEvents acota cuántas líneas del final del transcript entran en
// transcript.TailFacts — generoso para una sesión real (ver verificación
// end-to-end con un transcript de 788 líneas), sin dejar de ser barato: leer
// y parsear esa cantidad de líneas JSON es cuestión de milisegundos.
const digestMaxEvents = 500

// digestTopicKey identifica la observación "resumen en curso" de una
// sesión — topic_key estable por sesión, así SaveObservation la actualiza
// en el lugar (upsert, sube revision_count) en vez de crear una fila nueva
// en cada actualización periódica.
func digestTopicKey(sessionID string) string {
	return "session/" + sessionID
}

// digestTitle arma el título de la observación del digest — estable por
// sesión (no cambia entre actualizaciones), para que el upsert por
// topic_key nunca lo pise con algo distinto.
func digestTitle(sessionID string) string {
	short := sessionID
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("Resumen de sesión %s (automático)", short)
}

func digestInterval(cfg config.Config) time.Duration {
	minutes := cfg.Digest.IntervalMinutes
	if minutes <= 0 {
		minutes = digestDefaultIntervalMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// IsDigestDue chequea, sin tocar el LLM ni leer el transcript, si
// corresponde intentar actualizar el digest de una sesión — barato (una
// lectura a la base), pensado para llamarse en cada prompt sin costo real.
func IsDigestDue(ctx context.Context, st store.Storer, cfg config.Config, sessionID, cwd string) bool {
	if sessionID == "" || !cfg.Digest.Enabled {
		return false
	}
	proj := project.Detect(cwd)
	existing, err := st.GetByTopicKey(ctx, proj.Name, digestTopicKey(sessionID))
	if err != nil {
		return false
	}
	return existing == nil || time.Since(existing.UpdatedAt) >= digestInterval(cfg)
}

// MaybeUpdateDigest actualiza el resumen corriendo de una sesión si
// corresponde — SIEMPRE arma primero la versión determinística (milisegundos,
// sin red, leyendo transcript.TailFacts) y la guarda; si hay un LLM
// disponible, el presupuesto lo permite y cfg.Digest.LLMEnrichment está
// activo, reemplaza el contenido por una prosa del LLM MÁS el bloque
// determinístico al final, para nunca perder los hechos concretos (prompts,
// archivos, comandos) aunque el LLM alucine o resuma de más.
//
// Bug real que motiva que el determinístico sea el camino principal: medido
// en producción, el camino con LLM (llmClient.UpdateDigest) nunca terminaba
// en esta máquina bajo carga — Ollama respondía rápido al ping pero colgaba
// en la llamada de generación, muy por encima del presupuesto del hook — así
// que el digest nunca se guardaba, en silencio, siempre. El determinístico no
// depende de Ollama para nada: si Facts tiene contenido real, se guarda.
//
// force salta el chequeo de digestInterval — usado desde PreCompact (ver
// runPreCompactHook / handlePreCompactCapture): justo antes de que el
// transcript completo desaparezca es el único momento donde vale la pena
// refrescar aunque hayan pasado menos minutos que digestInterval desde la
// última actualización.
//
// Fail-open en cada paso: nunca debe interrumpir el hot path de
// UserPromptSubmit por esto. llmClient puede ser nil (Ollama no disponible,
// cortacircuitos abierto) — el digest determinístico se guarda igual.
func MaybeUpdateDigest(ctx context.Context, st store.Storer, cfg config.Config, llmClient *llm.Client, sessionID, transcriptPath, cwd string, force bool) error {
	if !cfg.Digest.Enabled || sessionID == "" || transcriptPath == "" {
		return nil
	}

	proj := project.Detect(cwd)
	topicKey := digestTopicKey(sessionID)

	existing, err := st.GetByTopicKey(ctx, proj.Name, topicKey)
	if err != nil {
		return nil
	}
	if !force && existing != nil && time.Since(existing.UpdatedAt) < digestInterval(cfg) {
		return nil // todavía no toca
	}

	facts, _ := transcript.TailFacts(transcriptPath, digestMaxEvents)
	deterministic := renderDeterministicDigest(facts)
	if deterministic == "" {
		return nil // nada real que guardar todavía (transcript vacío/ilegible)
	}

	content := deterministic

	if cfg.Digest.LLMEnrichment && llmClient != nil {
		if prose := tryDigestLLMEnrichment(ctx, llmClient, existing, transcriptPath, sessionID); prose != "" {
			content = prose + "\n\n" + deterministic
		}
	}

	_, err = st.SaveObservation(ctx, store.SaveParams{
		SessionID: sessionID,
		Type:      store.TypeSession,
		Title:     digestTitle(sessionID),
		Content:   strings.TrimSpace(secrets.Redact(content)),
		Project:   proj.Name,
		TopicKey:  topicKey,
	})
	return err
}

// tryDigestLLMEnrichment intenta la prosa del LLM sobre el excerpt de texto
// plano (TailExcerpt, no Facts) — devuelve "" ante cualquier falla o
// respuesta vacía/sin cambios, logueando cada falla real (motivo + tiempo
// transcurrido) para que un LLM roto deje de fallar en silencio como pasaba
// antes.
func tryDigestLLMEnrichment(ctx context.Context, llmClient *llm.Client, existing *store.Observation, transcriptPath, sessionID string) string {
	excerpt, err := transcript.TailExcerpt(transcriptPath, excerptMaxChars)
	if err != nil || len(strings.TrimSpace(excerpt)) < minExcerptChars {
		return "" // nada real que resumir en prosa todavía
	}

	previous := ""
	if existing != nil {
		previous = existing.Content
	}

	start := time.Now()
	update, err := llmClient.UpdateDigest(ctx, previous, excerpt)
	elapsed := time.Since(start)
	if err != nil {
		slog.Warn("digest: la actualización por LLM falló, se guarda solo el determinístico",
			"session_id", sessionID, "elapsed", elapsed, "error", err)
		return ""
	}
	if update == nil {
		return ""
	}
	prose := strings.TrimSpace(update.Content)
	if prose == "" || prose == strings.TrimSpace(previous) {
		return "" // el LLM dijo "nada nuevo" — no aporta sobre el determinístico
	}
	return prose
}

// renderDeterministicDigest arma el digest sin LLM a partir de los hechos
// extraídos del transcript — "" si no hay nada concreto todavía (sin
// prompts ni archivos tocados), para que el caller no guarde basura.
// Secciones vacías (sin comandos, por ejemplo) se omiten.
func renderDeterministicDigest(f transcript.Facts) string {
	if len(f.Prompts) == 0 && len(f.Files) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## En qué se viene trabajando (automático, sin LLM)\n")

	if len(f.Prompts) > 0 {
		sb.WriteString("\n**Prompts recientes**\n")
		for _, p := range f.Prompts {
			sb.WriteString("- " + p + "\n")
		}
	}
	if len(f.Files) > 0 {
		sb.WriteString("\n**Archivos tocados**\n")
		for _, path := range f.Files {
			sb.WriteString("- " + path + "\n")
		}
	}
	if len(f.Commands) > 0 {
		sb.WriteString("\n**Comandos**\n")
		for _, c := range f.Commands {
			sb.WriteString("- " + c + "\n")
		}
	}
	if f.LastAssistant != "" {
		sb.WriteString("\n**Última respuesta del agente**\n> " + f.LastAssistant + "\n")
	}

	return strings.TrimSpace(sb.String())
}
