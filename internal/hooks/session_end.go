package hooks

import (
	"context"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// RunSessionEnd handles the SessionEnd hook — distinto de Stop (ver
// session_stop.go): SessionEnd dispara UNA sola vez cuando la sesión de la
// CLI termina de verdad (el usuario cierra la ventana, hace /clear, etc.),
// no en cada turno del agente. Por eso, a diferencia de Stop, acá sí es
// seguro cerrar la sesión — no hay riesgo de marcarla "cerrada" mientras
// sigue en uso.
//
// Antes de este hook, ninguna señal automática cerraba una sesión: solo
// mem_session_end/mem_session_summary (llamados explícitamente por el
// agente) lo hacían. Si el agente nunca los llamaba — sesión corta, ventana
// cerrada de golpe, proceso matado — la sesión quedaba ended_at=NULL para
// siempre, ensuciando la pantalla de Sesiones de la TUI con "activa" que no
// lo estaban.
func RunSessionEnd(ctx context.Context, in Input, st store.Storer) error {
	if in.SessionID == "" {
		return nil
	}
	// Ojo: solo se salta el cierre cuando GetSession confirma con certeza que
	// no hace falta (no existe, o ya está cerrada). Un error de lectura
	// (ej. primary reconectando) NO debe frenar el intento de EndSession —
	// a diferencia de Stop, SessionEnd dispara una sola vez; tragar un error
	// transitorio acá deja la sesión sin cerrar para siempre, exactamente el
	// bug que este hook existe para resolver.
	sess, err := st.GetSession(ctx, in.SessionID)
	if err == nil && (sess == nil || sess.EndedAt != nil) {
		return nil
	}
	// summary="" preserva el resumen real si mem_session_summary ya corrió antes.
	return st.EndSession(ctx, in.SessionID, "")
}
