package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jjgarcia-app/kronos-v2/internal/checkpoint"
	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// ReasonCompact is the value of Input.Reason (or Input.Source) that indicates
// the session started after a context compaction event.
const ReasonCompact = "compact"

// RunSessionStart handles the SessionStart hook.
//
// Emits the 2-line bootstrapping signal, then injectContinuity — the active
// checkpoint plus real content (this session's own running digest if it
// exists, else the project's most recent observations). Bug found live
// 2026-09-03: this used to only happen post-compaction (reason == "compact",
// delegated to RunPostCompaction) — a plain resume/startup/clear got nothing
// but the signal, leaving the agent with zero real content unless it
// remembered to call mem_search itself. A resume gives Claude Code no
// guarantee the prior transcript is actually reloaded from kronos's side, so
// there's no safe case to skip this.
func RunSessionStart(ctx context.Context, in Input, st store.Storer) error {
	if in.EffectiveReason() == ReasonCompact {
		return RunPostCompaction(ctx, in, st)
	}

	proj := project.Detect(in.CWD)

	_, err := st.CreateSession(ctx, in.SessionID, proj.Name, in.CWD)
	if err != nil {
		// Non-fatal: session may already exist if Claude reconnects.
		_ = err
	}
	if in.SessionID != "" {
		if p, pErr := platform.CurrentSessionPath(proj.Name); pErr == nil {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
				_ = os.WriteFile(p, []byte(in.SessionID), 0o644)
			}
		}
	}

	n, _ := st.CountObservations(ctx, proj.Name)
	fmt.Printf("[kronos] %d observations available for %s\n", n, proj.Name)
	fmt.Println("[kronos] call mem_search with keywords from your task before editing OR before answering questions about past work — don't answer 'I don't know/have no record' from memory alone")
	// Claude Code no le pasa el session_id a los MCP servers por protocolo
	// (issue conocido: github.com/anthropics/claude-code/issues/41836) —
	// sin esto, mem_search/mem_context/etc. tienen que ADIVINAR la sesión
	// activa (archivo current_session_<proyecto>.txt o "más reciente en
	// DB"), y con varias sesiones concurrentes del mismo proyecto adivinan
	// mal: la búsqueda queda acreditada a otra sesión y el gate de
	// pre-tool-use sigue bloqueado aunque sí se buscó. Imprimirlo acá,
	// literal, es la única forma confiable de que el agente lo tenga a mano.
	if in.SessionID != "" {
		fmt.Printf("[kronos] your session_id is %q — pass it explicitly as session_id in every mem_* tool call this session (mem_search, mem_context, mem_checkpoint, mem_save...). Without it, kronos has to guess which of possibly several concurrent sessions is yours, and often guesses wrong.\n", in.SessionID)
	}
	// Puntero de conducta (ver hints.go): dónde comprobar el entorno y dónde
	// está la documentación. Son punteros con tope, no datos del entorno.
	fmt.Print(HintsPreamble(0))
	printBacklogWarnings(ctx, st, proj.Name)

	injectContinuity(ctx, st, proj.Name, in.SessionID)

	return nil
}

// maxContinuityItems caps how much real content injectContinuity prints —
// same k that RunPostCompaction always used (3), now shared so a normal
// start doesn't dump more than a post-compact restart does.
const maxContinuityItems = 3

// injectContinuity prints, best-effort, the active checkpoint (if any) plus
// real content to re-orient the agent: this session's own running digest
// (see internal/hooks/digest.go — topic_key "session/"+sessionID) if one
// exists, prioritized because it's the actual continuity thread for THIS
// conversation, then the project's most recent observations as a fallback
// (relevant when there's no digest yet, or to fill remaining slots). Shared
// between RunSessionStart's normal path and RunPostCompaction — both leave
// the agent with no usable transcript unless kronos hands it something here.
func injectContinuity(ctx context.Context, st store.Storer, projName, sessionID string) {
	coreProjectIDs := printCoreBlock(ctx, st, projName)

	if dataDir, err := platform.DataDir(); err == nil {
		if cp, err := checkpoint.Load(dataDir, projName); err == nil && cp != nil {
			fmt.Printf("[kronos] active task: %s | next: %s\n", cp.Task, cp.NextStep)
		}
	}

	var injectedIDs []string
	hasIntent := false

	if sessionID != "" {
		if digest, err := st.GetByTopicKey(ctx, projName, digestTopicKey(sessionID)); err == nil && digest != nil {
			printContinuityLine(digest.Title, digest.Type, digest.Content)
			hasIntent = hasIntent || digest.Type == store.TypeIntent
			injectedIDs = append(injectedIDs, strconv.FormatInt(digest.ID, 10))
		}
	}

	if len(injectedIDs) < maxContinuityItems {
		obs, err := pickRestoreObs(ctx, st, projName, sessionID, maxContinuityItems)
		if err == nil {
			for _, o := range obs {
				if len(injectedIDs) >= maxContinuityItems {
					break
				}
				id := strconv.FormatInt(o.ID, 10)
				if containsID(injectedIDs, id) {
					continue
				}
				printContinuityLine(o.Title, o.Type, o.Content)
				hasIntent = hasIntent || o.Type == store.TypeIntent
				injectedIDs = append(injectedIDs, id)
			}
		}
	}

	if hasIntent {
		fmt.Println(intentWarning)
	}

	// coreProjectIDs se agregan recién acá, DESPUÉS de que digest/pickRestoreObs
	// ya decidieron qué mostrar como continuidad — así no le sacan lugar a esas
	// líneas (que dedupean contra injectedIDs, ver containsID arriba) ni
	// cambian cuántas se imprimen. Solo importan para lo que persiste esta
	// función: el gate de pre-tool-use (gate.satisfied_by_injection, ver
	// pre_tool_use.go) lee sess.InjectedObservationIDs para saber que esta
	// sesión ya recibió memoria del proyecto vía el bloque core, sin que el
	// agente tuviera que llamar mem_search.
	for _, id := range coreProjectIDs {
		if !containsID(injectedIDs, id) {
			injectedIDs = append(injectedIDs, id)
		}
	}

	_ = st.PersistInjectedIDs(ctx, sessionID, injectedIDs)
}

// printContinuityLine imprime un item de continuidad. Los de tipo
// store.TypeIntent se marcan con el prefijo "[intent]" (en vez del formato
// "(tipo)" normal) — ver store.TypeIntent para el caso real del benchmark
// que motiva distinguirlos: un plan/afirmación sin verificar no debe leerse
// igual que un hecho confirmado.
func printContinuityLine(title string, typ store.ObservationType, content string) {
	if typ == store.TypeIntent {
		fmt.Printf("[kronos] [intent] %s: %s\n", title, preview80(content))
		return
	}
	fmt.Printf("[kronos] %s (%s): %s\n", title, typ, preview80(content))
}

// printCoreBlock imprime el bloque siempre-presente (ver core_block.go)
// antes de los items sueltos de injectContinuity, y devuelve los IDs de los
// items de PROYECTO que incluyó (ver CoreBlockMeta) — injectContinuity los
// usa para que el gate de pre-tool-use sepa que esta sesión ya recibió
// memoria del proyecto (gate.satisfied_by_injection, ver pre_tool_use.go).
// Carga la config con config.Load() en cada llamada — barato (un archivo
// chico local) y evita que un config.json editado a mano por Jerry requiera
// reiniciar nada más que la próxima sesión. Best-effort total: config rota,
// store caído o cfg.Core.Enabled=false simplemente no imprimen nada, nunca
// fallan el hook.
func printCoreBlock(ctx context.Context, st store.Storer, projName string) []string {
	cfg, _ := config.Load()
	if !cfg.Core.Enabled {
		return nil
	}
	block, meta, err := BuildCoreBlockWithMeta(ctx, st, projName, CoreBlockOptions{
		CharsLimit:        cfg.Core.CharsLimit,
		MaxItems:          cfg.Core.MaxItems,
		IncludeCheckpoint: cfg.Core.IncludeCheckpoint,
		GlobalsMaxChars:   cfg.Core.GlobalsMaxChars,
		GlobalsMaxItems:   cfg.Core.GlobalsMaxItems,
		RelevanceFilter:   cfg.Core.RelevanceFilter,
		ProjectMinChars:   cfg.Core.ProjectMinChars,
		MaxPerType:        cfg.Core.MaxPerType,
		MaxItemChars:      cfg.Core.MaxItemChars,
		MaxSessionItems:   cfg.Core.MaxSessionItems,
		StaleDays:         cfg.Core.StaleDays,
	})
	if err != nil || block == "" {
		return nil
	}
	fmt.Println(block)
	return meta.ProjectItemIDs
}

func containsID(ids []string, id string) bool {
	for _, existing := range ids {
		if existing == id {
			return true
		}
	}
	return false
}

// backlogSyncThreshold/backlogRelationsThreshold: a partir de cuánto se
// avisa proactivo en SessionStart. Antes esto era invisible salvo que
// alguien preguntara mem_doctor explícitamente — con Postgres caído un
// rato o Ollama sin correr, el backlog podía crecer sin que nadie se
// enterara.
const (
	backlogSyncThreshold      = 100
	backlogRelationsThreshold = 20
)

// printBacklogWarnings avisa si hay backlog de sync a Postgres o de
// relaciones sin juzgar por encima de un umbral. Todo best-effort — nunca
// bloquea ni falla el hook si algo no está disponible.
func printBacklogWarnings(ctx context.Context, st store.Storer, proj string) {
	if d, ok := st.(interface{ PendingCount() int }); ok {
		if pending := d.PendingCount(); pending > backlogSyncThreshold {
			fmt.Printf("[kronos] aviso: %d operaciones sin sincronizar a PostgreSQL (correr `kronos sync --pg-flush` o revisar `mem_doctor`)\n", pending)
		}
	}
	// st.ListRelations: ListRelations ya es primary-first en DualStore (ver
	// internal/store/dual_store.go), igual que el resto de las lecturas de
	// Storer. Antes esto pasaba por el buffer SQLite local a mano, sin importar
	// el estado del primary — mismo bug real que hacía que mem_doctor
	// mostrara 3 relaciones pendientes del buffer que ya no existían (o
	// nunca existieron) en el primary.
	rels, err := st.ListRelations(ctx, proj, store.JudgmentPending, backlogRelationsThreshold+1, 0)
	if err == nil && len(rels) > backlogRelationsThreshold {
		fmt.Printf("[kronos] aviso: más de %d relaciones sin juzgar para %s (usar mem_judge, o revisar si Ollama está corriendo)\n", backlogRelationsThreshold, proj)
	}
}

// localStoreOf resolvía el *store.Store SQLite subyacente sea cual sea el
// backend (mismo patrón que internal/mcp.Server.localStore()). Se eliminó
// cuando ListRelations pasó a ser primary-first: no quedaba ningún llamador y
// el linter del CI lo marcaba como código muerto.
