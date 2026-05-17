package hooks

import (
	"context"
	"fmt"

	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/secrets"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// nudgeEveryN prompts without a save triggers a format-reminder nudge.
const nudgeEveryN = 15

// RunPromptSubmit handles the UserPromptSubmit hook.
// Saves the prompt for future context and injects a memory nudge every
// nudgeEveryN turns when nothing has been saved yet this session.
func RunPromptSubmit(ctx context.Context, in Input, st *store.Store) error {
	if in.Prompt == "" {
		return nil
	}

	proj := project.Detect(in.CWD)
	content := secrets.Redact(in.Prompt)

	if err := st.SavePrompt(ctx, in.SessionID, proj.Name, content); err != nil {
		return err
	}

	// Nudge: every nudgeEveryN prompts with no deliberate saves this session,
	// remind the agent to save using the standard format.
	if in.SessionID != "" {
		promptCount := st.CountSessionPrompts(ctx, in.SessionID)
		if promptCount > 0 && promptCount%nudgeEveryN == 0 {
			obsCount := st.CountSessionObservations(ctx, in.SessionID)
			if obsCount == 0 {
				fmt.Print(memoryNudge(promptCount))
			}
		}
	}

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
