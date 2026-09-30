package hooks

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/llm"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
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

// digestDefaultTimeoutMs es el fallback cuando cfg.Digest.TimeoutMs no está
// seteado (config vieja, o Digest{} zero-value en un test) — mismo valor que
// el default de config.Default().Digest.TimeoutMs.
const digestDefaultTimeoutMs = 60000

// DigestEnrichTimeout expone el presupuesto que MaybeUpdateDigest le da al
// intento de enriquecimiento por LLM (digest.timeout_ms, default 60s) en la
// actualización periódica (force=false) — la que corre async en el daemon
// (ver internal/server/prompt_submit.go) y puede permitirse esperar más que
// el resto de las llamadas de generación porque nada del lado del usuario
// depende de su resultado. internal/server/prompt_submit.go la usa para
// dimensionar el timeout de la goroutine que envuelve la llamada completa,
// así ese límite externo no corta la espera antes de que digest.timeout_ms
// tenga chance de vencer. Los caminos interactivos (captura antes de
// compactar, fallback local del hook) llaman a MaybeUpdateDigest con
// force=true y NO pasan por este timeout — usan el general de llm.timeout_ms.
func DigestEnrichTimeout(cfg config.Config) time.Duration {
	ms := cfg.Digest.TimeoutMs
	if ms <= 0 {
		ms = digestDefaultTimeoutMs
	}
	return time.Duration(ms) * time.Millisecond
}

// digestPendingStore resuelve el almacén de reintentos de enriquecimiento
// pendientes (ver internal/llm.DigestPending) contra el data dir real — nil
// si no se pudo resolver, lo que es inofensivo porque todos sus métodos son
// nil-safe (se comportan como "sin pendientes"/no-op, ver digest_pending.go).
func digestPendingStore() *llm.DigestPending {
	dataDir, err := platform.DataDir()
	if err != nil {
		return nil
	}
	return llm.NewDigestPending(llm.DefaultDigestPendingPath(dataDir))
}

// IsDigestDue chequea, sin tocar el LLM ni leer el transcript, si
// corresponde intentar actualizar el digest de una sesión — barato (una
// lectura a la base más un archivo chico), pensado para llamarse en cada
// prompt sin costo real. Una sesión con un enriquecimiento pendiente de
// reintento (ver MaybeUpdateDigest) está due sin importar el intervalo
// normal, para que el reintento no tenga que esperar a que venza de nuevo.
func IsDigestDue(ctx context.Context, st store.Storer, cfg config.Config, sessionID, cwd string) bool {
	if sessionID == "" || !cfg.Digest.Enabled {
		return false
	}
	if digestPendingStore().Due(sessionID) {
		return true
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
// última actualización. Estos caminos son interactivos (le importan al
// usuario, no corren desacoplados en el daemon): el enriquecimiento por LLM
// que disparan usa el timeout general (llm.timeout_ms), no digest.timeout_ms
// — ver el comentario de DigestEnrichTimeout.
//
// Un enriquecimiento pendiente de reintento (ver DigestPending) también
// salta digestInterval, igual que force — sin esperar a que venza de nuevo,
// el reintento ocurre en la primera oportunidad. A diferencia de force, este
// caso SÍ usa digest.timeout_ms: el reintento sigue siendo la actualización
// periódica async del daemon, solo que adelantada.
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
	pending := digestPendingStore()

	existing, err := st.GetByTopicKey(ctx, proj.Name, topicKey)
	if err != nil {
		return nil
	}
	// retryKind: "" cuando no hay nada pendiente, o el tipo de pendiente que
	// toca reintentar ya (ver DigestPendingKindEnrichment/Facts en
	// digest_pending.go) — ambos saltan digestInterval igual que force, sin
	// esperar a que venza de nuevo.
	retryKind := ""
	if !force {
		retryKind = pending.PendingKind(sessionID)
	}
	if !force && retryKind == "" && existing != nil && time.Since(existing.UpdatedAt) < digestInterval(cfg) {
		return nil // todavía no toca, y no hay un reintento pendiente esperando
	}

	tailFacts, _ := transcript.TailFacts(transcriptPath, digestMaxEvents)
	deterministic := renderDeterministicDigest(tailFacts)
	if deterministic == "" {
		return nil // nada real que guardar todavía (transcript vacío/ilegible)
	}

	content := deterministic
	var facts []llm.DigestFact
	var factsExcerpt string // el excerpt que sustentó `facts` — se usa para descartar hechos fabricados antes de promoverlos, ver promoteDigestFacts

	if cfg.Digest.LLMEnrichment && llmClient != nil {
		enrichTimeout := time.Duration(0) // 0 = timeout general (llm.timeout_ms) — camino interactivo (force)
		if !force {
			enrichTimeout = DigestEnrichTimeout(cfg)
		}

		if retryKind == llm.DigestPendingKindFacts {
			// La prosa ya se guardó en un tick anterior (está en `existing`,
			// que incluye el determinístico de esa corrida) — este tick pide
			// SOLO los hechos, con un pedido acotado y sin volver a resumir
			// (ver llm.Client.ExtractDigestFacts). No se toca `content`: no
			// hay nada nuevo que aportarle sin llamar de nuevo a la prosa
			// completa, y llamarla de nuevo sería justo la llamada extra que
			// este reintento acotado evita.
			if existing != nil && strings.TrimSpace(existing.Content) != "" {
				content = existing.Content
			}
			retryFacts, retryExcerpt, explicit := tryDigestFactsOnlyRetry(ctx, llmClient, transcriptPath, cfg, sessionID, enrichTimeout)
			if explicit {
				facts = retryFacts
				factsExcerpt = retryExcerpt
				pending.Clear(sessionID)
			} else {
				pending.MarkFactsPending(sessionID)
			}
		} else {
			// Misma llamada, mismo round-trip: update.Facts viene de la MISMA
			// respuesta del LLM que ya genera la prosa (ver
			// llm.Client.UpdateDigest) — no se agrega una llamada nueva, así
			// que el contador de uso sigue subiendo en 1 por actualización de
			// digest, no en 2.
			update, updateExcerpt, failed := tryDigestLLMEnrichment(ctx, llmClient, existing, transcriptPath, sessionID, enrichTimeout)
			switch {
			case update != nil:
				content = update.Content + "\n\n" + deterministic
				if update.FactsKnown {
					// (a) éxito con hechos, o el modelo dijo explícitamente
					// que no hay ninguno — en ambos casos no hace falta
					// reintentar nada más.
					facts = update.Facts
					factsExcerpt = updateExcerpt
					pending.Clear(sessionID)
				} else {
					// (b) éxito en la prosa, pero sin respuesta explícita
					// sobre hechos (ni lista ni "FACTS: ninguno") — antes
					// esto se perdía en silencio (medido: digest con prosa,
					// cero hechos, sin pendiente). Se reintenta SOLO los
					// hechos en el próximo tick, sin volver a pedir la prosa
					// que ya se guarda más abajo.
					pending.MarkFactsPending(sessionID)
				}
			case failed:
				// No se pierde nada: el determinístico se guarda igual más
				// abajo. Queda anotado para reintentar el enriquecimiento
				// completo en el próximo MaybeUpdateDigest de esta sesión,
				// sin esperar a digestInterval.
				pending.MarkFailed(sessionID)
			}
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
		promoteDigestFacts(ctx, st, cfg, facts, factsExcerpt, sessionID, proj.Name)
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
// digestIdentifierPattern captura identificadores técnicos con forma de
// nombre de tabla/columna/función/archivo: snake_case o camelCase de al
// menos 2 partes, o algo con punto/slash (ruta, extensión) — no palabras
// sueltas en español, que dan demasiados falsos positivos.
var digestIdentifierPattern = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9]*(?:_[A-Za-z0-9]+)+\b|\b[a-z][a-z0-9]*(?:[A-Z][a-z0-9]*)+\b|\b[\w./-]+\.[a-z]{1,4}\b`)

// digestSchemaPhrasePattern busca menciones en español de una entidad de
// esquema/config ("tabla X", "columna Y", ...) seguidas de hasta 3 palabras
// — es la parte del bug real que digestIdentifierPattern NO cubre: el hecho
// fabricado (obs 231072704912801792) no usaba ningún identificador de
// código, solo prosa natural ("columna embeddings", "tabla de precios") que
// describía una entidad inexistente. Cortamos la frase en la primera
// conjunción/verbo común para no arrastrar la oración entera.
var digestSchemaPhrasePattern = regexp.MustCompile(`(?i)\b(tabla|columna|campo|función|funcion|endpoint|variable|clase|módulo|modulo|archivo|parámetro|parametro|flag)\s+([\p{L}0-9_./-]+(?:\s+[\p{L}0-9_./-]+){0,2})`)

// digestSchemaPhraseStopwords corta la frase capturada antes de un
// conector/verbo — sin esto "tabla X que bloqueaba Y" arrastraría "que" y
// "bloqueaba" como si fueran parte del nombre de la entidad.
var digestSchemaPhraseStopwords = map[string]bool{
	"que": true, "con": true, "para": true, "solo": true, "sólo": true,
	"en": true, "de": true, "del": true, "la": true, "el": true,
	"tenía": true, "tenia": true, "permitía": true, "permitia": true,
	"bloqueando": true, "bloqueaba": true, "y": true, "sin": true,
}

// digestSchemaPhrase corta la frase capturada en la primera stopword —
// "tabla de precios" queda "tabla de precios" completo porque "de" solo
// corta si es la PRIMERA palabra (una tabla puede legítimamente llamarse
// "de precios" en la superficie del texto); a partir de la segunda palabra
// cualquier stopword corta.
func digestSchemaPhrase(keyword, rest string) string {
	words := strings.Fields(rest)
	kept := []string{keyword}
	for i, w := range words {
		lw := strings.ToLower(w)
		if i > 0 && digestSchemaPhraseStopwords[lw] {
			break
		}
		kept = append(kept, w)
	}
	return strings.ToLower(strings.Join(kept, " "))
}

// digestFactIsGrounded exige que TODO identificador técnico mencionado en
// título+contenido de un hecho aparezca literalmente en el excerpt que le
// dio origen — barato (regex + substring, sin LLM) y es la única defensa
// determinística contra un caso real observado: el enriquecimiento por LLM
// resumió tres fixes distintos de llm_usage_log (RLS, MODEL_RATES, lane de
// embeddings) en una sola oración sintética que citaba una "columna
// embeddings" y una "tabla de precios con RLS" que no existen en ningún lado
// del excerpt real (obs 231072704912801792, temis-saas, 2026-09-30). Pedirle
// al prompt que no conflacione ayuda pero no es verificable a demanda (no es
// reproducible bajo demanda); esta guarda sí es determinística y barata de
// correr en cada hecho antes de guardarlo como observación propia.
//
// Si el excerpt no está disponible (excerptUsed == ""), no bloquea nada —
// fail-open, como el resto del camino del digest: es mejor un hecho sin
// verificar que perder promoción por un problema de plumbing.
func digestFactIsGrounded(title, content, excerptUsed string) bool {
	if strings.TrimSpace(excerptUsed) == "" {
		return true
	}
	text := title + " " + content
	ids := digestIdentifierPattern.FindAllString(text, -1)
	for _, id := range ids {
		if !strings.Contains(excerptUsed, id) {
			return false
		}
	}

	excerptLower := strings.ToLower(excerptUsed)
	for _, m := range digestSchemaPhrasePattern.FindAllStringSubmatch(text, -1) {
		phrase := digestSchemaPhrase(m[1], m[2])
		if !strings.Contains(excerptLower, phrase) {
			return false
		}
	}
	return true
}

func promoteDigestFacts(ctx context.Context, st store.Storer, cfg config.Config, facts []llm.DigestFact, excerptUsed, sessionID, project string) {
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
		if !digestFactIsGrounded(title, content, excerptUsed) {
			slog.Warn("digest: hecho descartado por mencionar un identificador que no está en el excerpt (posible conflación/fabricación)",
				"session_id", sessionID, "title", title)
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
// distinto: hechos determinísticos del transcript, no del LLM).
//
// Devuelve (nil, false) cuando no había nada real que resumir todavía o el
// LLM respondió pero sin nada nuevo que aportar — ninguno de los dos es una
// falla, así que no ameritan anotar la sesión para reintento. Devuelve
// (nil, true) únicamente cuando la llamada al LLM en sí falló o se pasó de
// tiempo (timeout incluido) — MaybeUpdateDigest usa ese true para anotar el
// enriquecimiento pendiente (ver DigestPending) y reintentarlo en la próxima
// oportunidad. Cada falla real se loguea (motivo + tiempo transcurrido) para
// que un LLM roto deje de fallar en silencio como pasaba antes.
func tryDigestLLMEnrichment(ctx context.Context, llmClient *llm.Client, existing *store.Observation, transcriptPath, sessionID string, timeout time.Duration) (update *llm.DigestUpdate, excerptUsed string, failed bool) {
	excerpt, err := transcript.TailExcerpt(transcriptPath, excerptMaxChars)
	if err != nil || len(strings.TrimSpace(excerpt)) < minExcerptChars {
		return nil, "", false // nada real que resumir en prosa todavía
	}

	previous := ""
	if existing != nil {
		previous = existing.Content
	}

	start := time.Now()
	result, err := llmClient.UpdateDigest(ctx, previous, excerpt, timeout)
	elapsed := time.Since(start)
	if err != nil {
		slog.Warn("digest: la actualización por LLM falló, se guarda solo el determinístico",
			"session_id", sessionID, "elapsed", elapsed, "error", err)
		return nil, "", true
	}
	if result == nil {
		return nil, "", false
	}
	prose := strings.TrimSpace(result.Content)
	if prose == "" || prose == strings.TrimSpace(previous) {
		return nil, "", false // el LLM dijo "nada nuevo" — no aporta sobre el determinístico
	}
	result.Content = prose
	return result, excerpt, false
}

// tryDigestFactsOnlyRetry pide, con una llamada acotada (ver
// llm.Client.ExtractDigestFacts), solo los hechos de una sesión cuyo
// enriquecimiento anterior ya guardó la prosa pero dejó los hechos
// pendientes (ver DigestPendingKindFacts). No vuelve a pedir la prosa.
//
// explicit=true cuando el modelo dio una respuesta interpretable (una lista
// de hechos, vacía o no, o "FACTS: ninguno") — el pendiente se puede limpiar
// sin importar si facts vino vacío. explicit=false ante cualquier otro caso
// (la llamada falló, se pasó de tiempo, o la respuesta no se pudo parsear) —
// el caller debe volver a anotar el pendiente para el próximo tick, hasta el
// tope de digestPendingMaxAttempts.
func tryDigestFactsOnlyRetry(ctx context.Context, llmClient *llm.Client, transcriptPath string, cfg config.Config, sessionID string, timeout time.Duration) (facts []llm.DigestFact, excerptUsed string, explicit bool) {
	excerpt, err := transcript.TailExcerpt(transcriptPath, excerptMaxChars)
	if err != nil || len(strings.TrimSpace(excerpt)) < minExcerptChars {
		// Nada real que pedir todavía — no es una falla del LLM, así que no
		// vale la pena seguir reintentando por esto puntualmente.
		return nil, "", true
	}

	max := cfg.Digest.MaxFacts
	if max <= 0 {
		max = digestDefaultMaxFacts
	}

	start := time.Now()
	result, explicitResp, err := llmClient.ExtractDigestFacts(ctx, excerpt, max, timeout)
	if err != nil {
		slog.Warn("digest: el reintento de hechos por LLM falló, se sigue reintentando",
			"session_id", sessionID, "elapsed", time.Since(start), "error", err)
		return nil, "", false
	}
	return result, excerpt, explicitResp
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
