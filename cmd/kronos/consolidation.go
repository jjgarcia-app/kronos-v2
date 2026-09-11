package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/consolidate"
	"github.com/jjgarcia-app/kronos-v2/internal/relations"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// startConsolidationLoopIfEnabled arranca runConsolidationLoop en background
// SOLO si config.consolidation.enabled=true — apagado por defecto (ver
// config.Default). Devuelve si lo arrancó, para que el caller (y los tests)
// puedan verificar la decisión sin depender de timing real del ticker.
func startConsolidationLoopIfEnabled(ctx context.Context, local *store.Store, rel *relations.Detector, cfg config.Config) bool {
	if !cfg.Consolidation.Enabled || local == nil {
		return false
	}
	go runConsolidationLoop(ctx, local, rel, cfg)
	return true
}

// runConsolidationLoop corre en background mientras el daemon vive. Ejecuta
// la misma consolidación de duplicados semánticos que `kronos gc
// --consolidate`, pero SIEMPRE en modo dry-run — nunca escribe sola. Aplicar
// la fusión de verdad sigue siendo un paso manual vía `--no-dry-run`. El
// reporte queda en daemon.log (ver redirectLogsToFile). Nunca tira el daemon
// abajo por un fallo de la consolidación — solo lo loguea, igual que
// runBackupLoop.
func runConsolidationLoop(ctx context.Context, local *store.Store, rel *relations.Detector, cfg config.Config) {
	interval := time.Duration(cfg.Consolidation.IntervalHours) * time.Hour
	if interval <= 0 {
		interval = 24 * time.Hour
	}

	runOnce := func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "consolidación automática: panic recuperado: %v\n", r)
			}
		}()
		report, err := consolidate.Run(ctx, local, rel, consolidate.Options{
			Threshold:          float32(cfg.Consolidation.Threshold),
			RequireSameType:    cfg.Consolidation.RequireSameType,
			RequireSameProject: cfg.Consolidation.RequireSameProject,
			DryRun:             true,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "consolidación automática falló: %v\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "consolidación automática (dry-run): %d pares candidatos\n", len(report.Pairs))
		for _, p := range report.Pairs {
			fmt.Fprintf(os.Stderr, "  superviviente=#%d <- reemplazada=#%d (project=%s type=%s) %s\n",
				p.SurvivorID, p.ReplacedID, p.Project, p.Type, p.Reason)
		}
	}

	// sin corrida inicial a propósito — el arranque del daemon ya hace
	// reindexRecent (potencialmente pesado contra Ollama); no hace falta
	// sumarle la consolidación al mismo momento. La primera corrida llega en
	// el primer tick.
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}
