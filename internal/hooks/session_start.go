package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jjgarcia-app/kronos-v2/internal/checkpoint"
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
	if dataDir, err := platform.DataDir(); err == nil {
		if cp, err := checkpoint.Load(dataDir, projName); err == nil && cp != nil {
			fmt.Printf("[kronos] active task: %s | next: %s\n", cp.Task, cp.NextStep)
		}
	}

	var injectedIDs []string

	if sessionID != "" {
		if digest, err := st.GetByTopicKey(ctx, projName, digestTopicKey(sessionID)); err == nil && digest != nil {
			fmt.Printf("[kronos] %s (%s): %s\n", digest.Title, digest.Type, preview80(digest.Content))
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
				fmt.Printf("[kronos] %s (%s): %s\n", o.Title, o.Type, preview80(o.Content))
				injectedIDs = append(injectedIDs, id)
			}
		}
	}

	_ = st.PersistInjectedIDs(ctx, sessionID, injectedIDs)
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
	ls := localStoreOf(st)
	if ls == nil {
		return
	}
	rels, err := ls.ListRelations(ctx, proj, store.JudgmentPending, backlogRelationsThreshold+1, 0)
	if err == nil && len(rels) > backlogRelationsThreshold {
		fmt.Printf("[kronos] aviso: más de %d relaciones sin juzgar para %s (usar mem_judge, o revisar si Ollama está corriendo)\n", backlogRelationsThreshold, proj)
	}
}

// localStoreOf resuelve el *store.Store SQLite subyacente sea cual sea el
// backend — mismo patrón que internal/mcp.Server.localStore().
func localStoreOf(st store.Storer) *store.Store {
	if ls, ok := st.(interface{ LocalStore() *store.Store }); ok {
		return ls.LocalStore()
	}
	if s, ok := st.(*store.Store); ok {
		return s
	}
	return nil
}

