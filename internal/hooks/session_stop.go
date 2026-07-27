package hooks

import (
	"context"
	"os"

	"github.com/jjgarcia-app/kronos-v2/internal/checkpoint"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// RunSessionStop handles the Stop hook.
// Closes the active memory session. Runs async so failures are non-critical.
func RunSessionStop(ctx context.Context, in Input, st store.Storer) error {
	if in.SessionID == "" {
		return nil
	}
	proj := project.Detect(in.CWD)
	if p, err := platform.CurrentSessionPath(proj.Name); err == nil {
		if data, readErr := os.ReadFile(p); readErr == nil && string(data) == in.SessionID {
			_ = os.Remove(p)
		}
	}

	// Red de seguridad, mismo patrón que PreCompact: la sesión terminó sin
	// pasar por compactación (Stop dispara en un cierre normal), y el hook
	// no puede escribir un resumen real — no vio la conversación, solo el
	// agente (Claude) la vio. Si el agente se olvidó de llamar
	// mem_session_summary, esto deja al menos una miga de pan en vez de
	// nada; no reemplaza un resumen real.
	saveFallbackCheckpointIfMissing(ctx, st, in.SessionID, proj.Name)

	return st.EndSession(ctx, in.SessionID, "")
}

func saveFallbackCheckpointIfMissing(ctx context.Context, st store.Storer, sessionID, project string) {
	sess, err := st.GetSession(ctx, sessionID)
	if err != nil || sess == nil || sess.Summary != "" {
		return // ya tiene resumen real, o no se pudo leer — no pisar nada
	}
	dataDir, err := platform.DataDir()
	if err != nil {
		return
	}
	if existing, _ := checkpoint.Load(dataDir, project); existing != nil {
		return // ya hay un checkpoint activo (ej. de una compactación previa)
	}
	_ = checkpoint.Save(dataDir, project, checkpoint.State{
		Task:     "Sesión cerrada sin mem_session_summary",
		NextStep: `Revisar mem_context o mem_search ("resumen de sesión") para reconstruir en qué se estaba trabajando`,
		Notes:    "Auto-generado por el hook Stop — no reemplaza un resumen real, es solo una red de seguridad.",
		Project:  project,
	})
}
