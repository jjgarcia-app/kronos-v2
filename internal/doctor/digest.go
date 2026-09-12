package doctor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/llm"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// checkAutoDigest reporta el estado del digest automático por sesión (ver
// internal/hooks/digest.go, MaybeUpdateDigest): cuándo se guardó el último y
// de qué sesión, y si el cortacircuitos del LLM local está abierto. Motivado
// por un bug real: el digest nunca funcionó en producción (0 observaciones
// creadas por el mecanismo automático) sin que nada lo reportara — este
// check existe para que un digest roto se note en `kronos doctor` en vez de
// descubrirse meses después leyendo la base a mano.
func checkAutoDigest(ctx context.Context, cfg config.Config) Check {
	if !cfg.Digest.Enabled {
		return Check{Name: "Digest automático", Detail: "digest.enabled=false — desactivado", Status: StatusWarn}
	}

	dbPath, err := platform.DBPath()
	if err != nil {
		return Check{Name: "Digest automático", Detail: "no se pudo resolver la ruta del buffer local", Status: StatusWarn}
	}
	if cfg.DB.SQLitePath != "" {
		dbPath = cfg.DB.SQLitePath
	}
	buffer, err := store.New(dbPath)
	if err != nil {
		return Check{Name: "Digest automático", Detail: "no se pudo abrir DB local", Status: StatusWarn}
	}
	defer buffer.Close()

	latest, err := buffer.LatestByType(ctx, store.TypeSession)
	if err != nil {
		return Check{Name: "Digest automático", Detail: fmt.Sprintf("error leyendo el último digest: %v", err), Status: StatusWarn}
	}

	detail := "sin datos — todavía no se guardó ningún digest automático"
	if latest != nil {
		detail = fmt.Sprintf("último hace %s (sesión %s)", formatDuration(time.Since(latest.UpdatedAt)), shortSessionID(latest))
	}

	status := StatusOK
	if breakerDetail, open := breakerStatus(cfg); breakerDetail != "" {
		detail += " | " + breakerDetail
		if open {
			status = StatusWarn
		}
	}

	detail += " | " + llmProviderStatus(cfg)

	if pendingDetail := digestPendingDetail(); pendingDetail != "" {
		detail += " | " + pendingDetail
	}

	return Check{Name: "Digest automático", Detail: detail, Status: status}
}

// digestPendingDetail reporta cuántas sesiones tienen un enriquecimiento por
// LLM pendiente de reintento (ver internal/llm.DigestPending) — sin esto, un
// pico de carga que hace fallar el enriquecimiento (ver
// internal/hooks.MaybeUpdateDigest) queda invisible hasta que alguien nota
// que faltan hechos tipados. No es una cuota ni un bloqueo, solo
// información: "" si no se pudo resolver el data dir o no hay pendientes.
func digestPendingDetail() string {
	dataDir, err := platform.DataDir()
	if err != nil {
		return ""
	}
	n := llm.NewDigestPending(llm.DefaultDigestPendingPath(dataDir)).Count()
	if n == 0 {
		return ""
	}
	if n == 1 {
		return "enriquecimiento pendiente: 1 sesión"
	}
	return fmt.Sprintf("enriquecimiento pendiente: %d sesiones", n)
}

// llmProviderStatus arma el fragmento "proveedor: X, modelo: Y[, guardián de
// carga salteando llamadas ahora (...)]" que se agrega al detalle del check
// de digest automático — así un `kronos doctor` deja ver de un vistazo qué
// LLM va a usar la captura automática (digest + captura pasiva) y si el
// guardián de carga (ver internal/llm.LoadGuardStatus) está descartando
// llamadas en este momento, sin tener que ir a leer /proc/loadavg a mano.
func llmProviderStatus(cfg config.Config) string {
	provider := cfg.LLM.Provider
	if provider == "" {
		provider = "ollama"
	}
	model := cfg.LLM.Model
	if model == "" {
		switch provider {
		case "claude-cli":
			model = llm.DefaultClaudeCLIModel
		default:
			model = cfg.Embeddings.OllamaLLMModel
			if model == "" {
				model = llm.DefaultModel
			}
		}
	}

	detail := fmt.Sprintf("LLM captura automática: proveedor=%s, modelo=%s", provider, model)

	// El guardián de carga solo aplica a los backends que generan con CPU de
	// esta máquina (Ollama). Con un proveedor remoto (claude-cli) no se saltea
	// nada, así que decir "SALTEANDO" ahí sería mentir sobre lo que va a pasar.
	if provider != "ollama" {
		if _, load1, cpus, available := llm.LoadGuardStatus(cfg.LLM.MaxLoadPerCPU); available {
			detail += fmt.Sprintf(" | guardián de carga: no aplica a %s (solo protege al modelo local; load1=%.2f / %d CPUs)",
				provider, load1, cpus)
		} else {
			detail += " | guardián de carga: desactivado"
		}
		if provider == "claude-cli" {
			detail += claudeCLILastFailureDetail()
		}
		return detail
	}

	if skipping, load1, cpus, available := llm.LoadGuardStatus(cfg.LLM.MaxLoadPerCPU); available {
		if skipping {
			detail += fmt.Sprintf(" | guardián de carga: SALTEANDO llamadas (load1=%.2f / %d CPUs > umbral %g)",
				load1, cpus, cfg.LLM.MaxLoadPerCPU)
		} else {
			detail += fmt.Sprintf(" | guardián de carga: ok (load1=%.2f / %d CPUs, umbral %g)",
				load1, cpus, cfg.LLM.MaxLoadPerCPU)
		}
	} else {
		detail += " | guardián de carga: desactivado"
	}

	return detail
}

// breakerStatus lee (sin modificar) el estado del cortacircuitos del LLM
// local — mismo archivo que consultan/actualizan internal/llm.Breaker desde
// el daemon y desde cada proceso de hook. "" si no se pudo resolver el data
// dir (no hay nada que reportar, no es un fallo del check).
func breakerStatus(cfg config.Config) (detail string, open bool) {
	dataDir, err := platform.DataDir()
	if err != nil {
		return "", false
	}
	openFor := time.Duration(cfg.LLM.BreakerMinutes) * time.Minute
	b := llm.NewBreaker(llm.DefaultBreakerPath(dataDir), cfg.LLM.BreakerFailures, openFor)
	st := b.State()

	if st.ConsecutiveFailures == 0 && st.OpenUntil.IsZero() {
		return "cortacircuitos LLM: cerrado, sin fallos registrados", false
	}

	open = !st.OpenUntil.IsZero() && time.Now().Before(st.OpenUntil)
	if open {
		return fmt.Sprintf("cortacircuitos LLM: ABIERTO hasta %s — %d fallos consecutivos, último: %s",
			st.OpenUntil.Format(time.RFC3339), st.ConsecutiveFailures, st.LastError), true
	}
	return fmt.Sprintf("cortacircuitos LLM: cerrado (%d fallos consecutivos registrados)", st.ConsecutiveFailures), false
}

// claudeCLILastFailureDetail arma " | último fallo: <clasificación> (hace X)"
// a partir de la última falla de claude-cli clasificada y persistida (ver
// internal/llm.ReadLastFailure) — "" si nunca falló, para no ensuciar el
// detalle de un proveedor que viene andando bien. A diferencia de
// breakerStatus, esto no se borra cuando el cortacircuitos cierra tras un
// éxito: el objetivo es poder seguir viendo "último fallo: hace X" incluso
// después de que el proveedor se recuperó.
func claudeCLILastFailureDetail() string {
	dataDir, err := platform.DataDir()
	if err != nil {
		return ""
	}
	lf, ok := llm.ReadLastFailure(llm.DefaultLastFailurePath(dataDir))
	if !ok {
		return ""
	}
	return fmt.Sprintf(" | último fallo: %s (hace %s)", lf.Kind, formatDuration(time.Since(lf.At)))
}

func shortSessionID(obs *store.Observation) string {
	id := obs.SessionID
	if id == "" {
		id = strings.TrimPrefix(obs.TopicKey, "session/")
	}
	if len(id) > 8 {
		id = id[:8]
	}
	if id == "" {
		return "desconocida"
	}
	return id
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "menos de 1 minuto"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%.1f h", d.Hours())
	default:
		return fmt.Sprintf("%.1f días", d.Hours()/24)
	}
}
