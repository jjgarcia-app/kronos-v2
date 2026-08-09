package hooks

import (
	"context"

	"github.com/jjgarcia-app/kronos-v2/internal/checkpoint"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// RunSessionStop handles the Stop hook.
//
// Bug real encontrado en producción: Stop NO dispara una sola vez al final
// real de una sesión — dispara cada vez que el agente principal termina de
// responder y devuelve el control (una vez por turno, muchas veces en una
// conversación larga). Antes esto llamaba EndSession(ended_at=now) y
// borraba el archivo current_session_<proyecto>.txt en CADA turno — así
// que apenas terminaba el primer turno, cualquier lookup posterior
// (mem_search buscando la sesión activa, GetActiveSession, etc.) ya veía
// la sesión como "terminada" y el archivo ya no existía, pese a que la
// conversación seguía activa. Confirmado en vivo dos veces: una sesión
// propia marcada ended_at pese a seguir en uso, y una sesión de Jerry en
// otro proyecto marcada como cerrada 5 minutos después de arrancar.
//
// Por eso ahora Stop NO toca ended_at ni borra el archivo — solo deja la
// red de seguridad del checkpoint. "La sesión terminó de verdad" queda a
// cargo exclusivo de una señal explícita (mem_session_summary /
// mem_session_end), no de un hook que dispara todo el tiempo.
func RunSessionStop(ctx context.Context, in Input, st store.Storer) error {
	if in.SessionID == "" {
		return nil
	}
	proj := project.Detect(in.CWD)

	// Red de seguridad, mismo patrón que PreCompact: si todavía no hay
	// resumen ni checkpoint para esta sesión, deja al menos una miga de pan.
	// Idempotente (checkpoint.Load) — dispararse en cada turno no genera
	// ruido ni pisa nada una vez que existe.
	saveFallbackCheckpointIfMissing(ctx, st, in.SessionID, proj.Name)

	return nil
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
