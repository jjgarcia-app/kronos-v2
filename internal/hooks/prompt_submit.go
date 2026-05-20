package hooks

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/secrets"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// nudgeEveryN prompts without a save triggers a format-reminder nudge.
const nudgeEveryN = 15

// promptTimeout is the hard timeout for the entire prompt-submit search path.
const promptTimeout = 100 * time.Millisecond

// defaultMinSim is the cosine similarity threshold for vector search.
const defaultMinSim = float32(0.65)

// RunPromptSubmit handles the UserPromptSubmit hook.
// Saves the prompt, then performs dual-strategy vector+FTS search and emits
// the top relevant (non-duplicate) results to stdout.
func RunPromptSubmit(ctx context.Context, in Input, st store.Storer, vs *embeddings.VectorStore) error {
	if strings.TrimSpace(in.Prompt) == "" {
		return nil
	}

	proj := project.Detect(in.CWD)
	content := secrets.Redact(in.Prompt)

	_ = st.SavePrompt(ctx, in.SessionID, proj.Name, content)

	// Search path: apply hard timeout.
	ctx2, cancel := context.WithTimeout(ctx, promptTimeout)
	defer cancel()

	func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Debug("RunPromptSubmit: recovered panic in search path", "panic", r)
			}
		}()

		injectedIDs, _ := st.LoadInjectedIDs(ctx, in.SessionID)
		injectedSet := make(map[string]bool, len(injectedIDs))
		for _, id := range injectedIDs {
			injectedSet[id] = true
		}

		type result struct {
			id      string
			title   string
			typ     string
			content string
		}
		var results []result

		// Strategy 1: vector search (if vs is available).
		if vs != nil {
			sims, err := vs.Similar(ctx2, in.Prompt, 2, 0, defaultMinSim)
			if err != nil {
				slog.Debug("RunPromptSubmit: vector search error", "err", err)
			} else {
				for _, s := range sims {
					id := strconv.FormatInt(s.ObsID, 10)
					if injectedSet[id] {
						continue
					}
					obs, err := st.GetObservation(ctx2, s.ObsID)
					if err != nil || obs == nil {
						continue
					}
					results = append(results, result{
						id:      id,
						title:   obs.Title,
						typ:     string(obs.Type),
						content: obs.Content,
					})
					if len(results) >= 2 {
						break
					}
				}
			}
		}

		// Strategy 2: FTS fallback (if vector gave 0 results or vs is nil).
		if len(results) == 0 {
			ftsRes, err := st.Search(ctx2, store.SearchParams{
				Query:   in.Prompt,
				Project: proj.Name,
				Limit:   2,
			})
			if err != nil {
				slog.Debug("RunPromptSubmit: FTS search error", "err", err)
			} else {
				for _, r := range ftsRes {
					id := strconv.FormatInt(r.ID, 10)
					if injectedSet[id] {
						continue
					}
					results = append(results, result{
						id:      id,
						title:   r.Title,
						typ:     string(r.Type),
						content: r.Content,
					})
					if len(results) >= 2 {
						break
					}
				}
			}
		}

		for _, r := range results {
			fmt.Printf("[kronos] %s (%s): %s\n", r.title, r.typ, preview80(r.content))
		}
	}()

	// Nudge: every nudgeEveryN prompts with no deliberate saves this session,
	// remind the agent to save using the standard format.
	// Uses a separate recover to ensure fail-open.
	func() {
		defer func() { recover() }()
		if in.SessionID != "" {
			// Use the concrete *store.Store method if available; otherwise skip nudge.
			type promptCounter interface {
				CountSessionPrompts(ctx context.Context, sessionID string) int
				CountSessionObservations(ctx context.Context, sessionID string) int
			}
			if counter, ok := st.(promptCounter); ok {
				promptCount := counter.CountSessionPrompts(ctx2, in.SessionID)
				if promptCount > 0 && promptCount%nudgeEveryN == 0 {
					obsCount := counter.CountSessionObservations(ctx2, in.SessionID)
					if obsCount == 0 {
						fmt.Print(memoryNudge(promptCount))
					}
				}
			}
		}
	}()

	return nil
}

// memoryNudge returns the reminder injected into the agent's context.
// Includes the mandatory format so saves are consistent across sessions and agents.
func memoryNudge(turns int) string {
	return fmt.Sprintf(`
[Kronos — recordatorio de memoria, turno %d]
Llevas %d turnos sin guardar nada. Si descubriste algo importante
(decisión, bug resuelto, patrón, configuración), guarda AHORA con mem_save.

Formato obligatorio para el campo content:
  Qué: [qué ocurrió o se decidió]
  Por qué: [motivación, causa o restricción]
  Archivos: [path:línea si aplica, o "N/A"]
  Cómo aplicar: [regla práctica para sesiones futuras]

Reglas de campo:
  title   → "Verbo + qué" corto y buscable
  type    → bugfix | decision | architecture | discovery | pattern | config | preference
  topic_key → OBLIGATORIO si type es decision / architecture / pattern / config
              formato: "area/tema"  ej: "db/postgres-driver"

Si no hay nada relevante que guardar, ignora este mensaje.

`, turns, turns)
}
