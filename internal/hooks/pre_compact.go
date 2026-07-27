package hooks

import (
	"context"
	"fmt"

	"github.com/jjgarcia-app/kronos-v2/internal/checkpoint"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/project"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// RunPreCompact handles the PreCompact hook — dispara justo ANTES de que
// Claude Code compacte el contexto, con la conversación completa todavía
// disponible. Existe porque depender de que el agente se acuerde de llamar
// mem_session_summary antes de perder contexto es exactamente el tipo de
// cosa que no hay que dejar en manos del LLM cuando importa de verdad.
//
// Dos acciones, ninguna depende de que el agente coopere:
//
//  1. Un aviso en stdout — Claude Code lo inyecta de vuelta al agente antes
//     de compactar — recordando llamar mem_session_summary AHORA, mientras
//     todavía hay contexto completo para escribir un resumen que valga algo.
//  2. Si no hay checkpoint activo para el proyecto, se autoguarda uno
//     mínimo — red de seguridad determinística: aunque el agente ignore el
//     aviso de arriba, RunPostCompaction (SessionStart con reason=compact)
//     va a tener algo concreto para reinyectar, en vez de nada.
func RunPreCompact(_ context.Context, in Input, _ store.Storer) error {
	proj := project.Detect(in.CWD)

	fmt.Println("[kronos] COMPACTACIÓN INMINENTE — si todavía no llamaste mem_session_summary, hacelo AHORA. Es la última oportunidad con el contexto completo antes de perderlo.")

	dataDir, err := platform.DataDir()
	if err != nil {
		return nil // best-effort — no bloquear la compactación por esto
	}
	if existing, _ := checkpoint.Load(dataDir, proj.Name); existing != nil {
		return nil // ya hay uno activo, no lo pisamos
	}
	_ = checkpoint.Save(dataDir, proj.Name, checkpoint.State{
		Task:     "Sesión compactada sin checkpoint ni resumen explícito guardado antes",
		NextStep: `Revisar mem_context o mem_search ("resumen de sesión") para reconstruir en qué se estaba trabajando antes de la compactación`,
		Notes:    "Auto-generado por el hook PreCompact — no reemplaza un resumen real, es solo una red de seguridad.",
		Project:  proj.Name,
	})
	return nil
}
