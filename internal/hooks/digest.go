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

	tailFacts, _ := transcript.TailFacts(transcriptPath, digestMaxEvents)
	deterministic := renderDeterministicDigest(tailFacts)
	if deterministic == "" {
		return nil // nada real que guardar todavía (transcript vacío/ilegible)
	}

	content := deterministic
	var facts []llm.DigestFact

	if cfg.Digest.LLMEnrichment && llmClient != nil {
		// Misma llamada, mismo round-trip: update.Facts viene de la MISMA
		// respuesta del LLM que ya genera la prosa (ver
		// llm.Client.UpdateDigest) — no se agrega una llamada nueva, así que
		// el contador de uso sigue subiendo en 1 por actualización de
		// digest, no en 2.
		if update := tryDigestLLMEnrichment(ctx, llmClient, existing, transcriptPath, sessionID); update != nil {
			content = update.Content + "\n\n" + deterministic
			facts = update.Facts
		}
	}

	// La observación se guarda con session_id, y observations tiene FK a
	// sessions: si la fila de la sesión todavía no existe (SessionStart no
	// corrió para esta sesión — caso real medido: 712 sesiones en la base y
	// el digest se perdía con "FOREIGN KEY constraint failed" que el caller
	// descartaba), el INSERT falla y el digest se pierde en silencio. Con
	// esto el digest se guarda igual.
	ensureSession(ctx, st, sessionID, proj.Name, cwd)

	if _, err = st.SaveObservation(ctx, store.SaveParams{
		SessionID: sessionID,
		Type:      store.TypeSession,
		Title:     digestTitle(sessionID),
		Content:   strings.TrimSpace(secrets.Redact(content)),
		Project:   proj.Name,
		TopicKey:  topicKey,
	}); err != nil {
		// Antes este error se descartaba en el caller: el digest no se
		// guardaba y nadie se enteraba. Que quede en el log.
		slog.Warn("digest: no se pudo guardar la observación",
			"session_id", sessionID, "project", proj.Name, "err", err)
		return err
	}

	// El digest en sí (tipo "session") se guarda siempre igual, arriba —
	// esto es puramente aditivo: promueve lo que el LLM extrajo (si algo)
	// como observaciones propias tipadas, sin cambiar el formato del digest.
	if cfg.Digest.PromoteFacts && len(facts) > 0 {
		promoteDigestFacts(ctx, st, cfg, facts, sessionID, proj.Name)
	}

	return nil
}

// digestFactTypes son los tipos de observación válidos para un hecho
// promovido desde el digest — session/passive/intent quedan afuera a
// propósito: session es justo el tipo que este cambio busca dejar de
// sobrecargar, y passive/intent pertenecen a otros caminos de captura.
var digestFactTypes = map[string]store.ObservationType{
	"bugfix":     store.TypeBugfix,
	"decision":   store.TypeDecision,
	"config":     store.TypeConfig,
	"discovery":  store.TypeDiscovery,
	"pattern":    store.TypePattern,
	"preference": store.TypePreference,
}

// digestFactMinTitleChars / digestFactMinContentChars: piso de longitud para
// descartar hechos genéricos o truncados que el LLM puede devolver pese a
// que el prompt pide "solo hechos útiles a 30 días" (ej. "se corrieron
// tests", o un campo vacío) — no vale la pena guardarlos como observación
// propia.
const (
	digestFactMinTitleChars   = 8
	digestFactMinContentChars = 20
)

// digestDefaultMaxFacts es el fallback cuando cfg.Digest.MaxFacts no está
// seteado (config vieja, o Digest{} zero-value en un test).
const digestDefaultMaxFacts = 3

// promoteDigestFacts guarda cada hecho propuesto por el LLM (extraído en la
// MISMA llamada que la prosa del digest, ver llm.Client.UpdateDigest) como
// observación PROPIA con su tipo — a diferencia del digest de sesión (tipo
// "session", enterrado y limitado a uno por la inyección automática — ver
// core.max_session_items / recall.max_session_items), estas SÍ reaparecen en
// esa inyección sin ese tope.
//
// No confía en que el LLM sea preciso: tipos fuera del whitelist o contenido
// demasiado corto/genérico se descartan acá (ver digestFactTypes y los pisos
// de longitud), y el dedupe por hash de título+contenido que ya tiene
// SaveObservation evita duplicar un hecho ya guardado en una corrida
// anterior del digest — re-correrlo con el mismo excerpt no crea filas
// nuevas, solo bumpea duplicate_count de las existentes.
func promoteDigestFacts(ctx context.Context, st store.Storer, cfg config.Config, facts []llm.DigestFact, sessionID, project string) {
	max := cfg.Digest.MaxFacts
	if max <= 0 {
		max = digestDefaultMaxFacts
	}
	saved := 0
	for _, f := range facts {
		if saved >= max {
			break
		}
		typ, ok := digestFactTypes[strings.ToLower(strings.TrimSpace(f.Type))]
		if !ok {
			continue
		}
		title := strings.TrimSpace(f.Title)
		content := strings.TrimSpace(f.Content)
		if len(title) < digestFactMinTitleChars || len(content) < digestFactMinContentChars {
			continue
		}
		if _, err := st.SaveObservation(ctx, store.SaveParams{
			SessionID: sessionID,
			Type:      typ,
			Title:     title,
			Content:   strings.TrimSpace(secrets.Redact(content)),
			Project:   project,
		}); err != nil {
			slog.Debug("digest: no se pudo guardar hecho promovido", "title", title, "err", err)
			continue
		}
		saved++
	}
}

// ensureSession crea la fila de sesión si no existe — necesario antes de
// guardar cualquier observación con session_id (FK). Idempotente y fail-open:
// si la sesión ya existe no hace nada, y si CreateSession falla (carrera con
// otro proceso, o store sin tablas de sesiones) sigue adelante: el INSERT de
// la observación dirá si de verdad no se pudo.
func ensureSession(ctx context.Context, st store.Storer, sessionID, proj, cwd string) {
	if sessionID == "" {
		return
	}
	if sess, err := st.GetSession(ctx, sessionID); err == nil && sess != nil {
		return
	}
	_, _ = st.CreateSession(ctx, sessionID, proj, cwd)
}

// tryDigestLLMEnrichment intenta la prosa (y los hechos standalone, misma
// llamada — ver llm.Client.UpdateDigest) del LLM sobre el excerpt de texto
// plano (TailExcerpt, no Facts de transcript.TailFacts, que es algo
// distinto: hechos determinísticos del transcript, no del LLM) — devuelve
// nil ante cualquier falla o respuesta vacía/sin cambios, logueando cada
// falla real (motivo + tiempo transcurrido) para que un LLM roto deje de
// fallar en silencio como pasaba antes.
func tryDigestLLMEnrichment(ctx context.Context, llmClient *llm.Client, existing *store.Observation, transcriptPath, sessionID string) *llm.DigestUpdate {
	excerpt, err := transcript.TailExcerpt(transcriptPath, excerptMaxChars)
	if err != nil || len(strings.TrimSpace(excerpt)) < minExcerptChars {
		return nil // nada real que resumir en prosa todavía
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
		return nil
	}
	if update == nil {
		return nil
	}
	prose := strings.TrimSpace(update.Content)
	if prose == "" || prose == strings.TrimSpace(previous) {
		return nil // el LLM dijo "nada nuevo" — no aporta sobre el determinístico
	}
	update.Content = prose
	return update
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
