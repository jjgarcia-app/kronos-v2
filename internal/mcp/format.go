package mcp

import (
	"fmt"
	"strings"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// typesRequiringTopicKey are the observation types that MUST have a topic_key
// to enable upsert and prevent duplicates across sessions.
var typesRequiringTopicKey = map[store.ObservationType]bool{
	store.TypeDecision:     true,
	store.TypeArchitecture: true,
	store.TypePattern:      true,
	store.TypeConfig:       true,
}

const saveContentTemplate = `Qué: [qué ocurrió o se decidió]
Por qué: [motivación, causa o restricción]
Archivos: [path:línea si aplica, o "N/A"]
Cómo aplicar: [regla práctica para sesiones futuras]`

const summaryTemplate = `## Objetivo
[Qué se buscaba lograr]

## Completado
- [tarea 1]
- [tarea 2]

## Descubrimientos clave
[Hallazgos no obvios, bugs, decisiones]

## Próximos pasos
[Qué queda pendiente]

## Archivos relevantes
[paths principales]`

// validateSaveParams validates content format and topic_key requirements.
// Returns a descriptive error if the agent used the wrong format.
// Passive, preference and session observations bypass format validation.
func validateSaveParams(content string, typ store.ObservationType, topicKey string) error {
	if typ == store.TypePassive || typ == store.TypeSession || typ == store.TypePreference {
		if strings.TrimSpace(content) == "" {
			return fmt.Errorf("content no puede estar vacío")
		}
		return nil
	}

	// Enforce content format — accept Spanish and English headers
	lower := strings.ToLower(content)
	missingQue := !strings.Contains(lower, "qué:") && !strings.Contains(lower, "que:") &&
		!strings.Contains(lower, "what:")
	missingPorque := !strings.Contains(lower, "por qué:") && !strings.Contains(lower, "por que:") &&
		!strings.Contains(lower, "why:")

	if missingQue || missingPorque {
		return fmt.Errorf(`el content no tiene el formato requerido.

Formato obligatorio:
%s

También se aceptan headers en inglés: What: / Why:
Todos los agentes deben usar este mismo formato para mantener
la memoria consistente y buscable entre sesiones.`, saveContentTemplate)
	}

	// Enforce topic_key for structural types
	if typesRequiringTopicKey[typ] && topicKey == "" {
		return fmt.Errorf(`topic_key es OBLIGATORIO para type=%s.

Usa una clave estable en formato "area/tema".
Ejemplos: "db/postgres-driver", "auth/jwt-strategy", "api/rate-limiting"

El topic_key permite upsert — si el mismo tema se actualiza,
no se crea un duplicado. Sin él, cada save crea una entrada nueva.`, typ)
	}

	return nil
}

// validateSummaryFormat validates that a session summary has the required sections.
func validateSummaryFormat(summary string) error {
	lower := strings.ToLower(summary)
	missing := []string{}

	if !strings.Contains(lower, "objetivo") {
		missing = append(missing, "## Objetivo")
	}
	if !strings.Contains(lower, "completado") {
		missing = append(missing, "## Completado")
	}

	if len(missing) == 0 {
		return nil
	}

	return fmt.Errorf(`el summary no tiene el formato requerido. Faltan secciones: %s

Formato obligatorio:
%s`, strings.Join(missing, ", "), summaryTemplate)
}
