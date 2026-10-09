package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// kronosRulesFragment es el contenido de la capa 2 (refuerzo de instrucciones)
// que empuja al agente a usar la memoria de Kronos de forma proactiva —
// independiente del agente destino (Claude Code lee CLAUDE.md, Codex lee
// AGENTS.md, pero el contenido es el mismo: los nombres de tool mem_* son
// MCP y funcionan igual en ambos).
const kronosRulesFragment = `## Kronos — Memoria persistente entre sesiones

Tienes acceso a un servidor MCP de memoria (Kronos). Úsalo de forma proactiva.

### Reglas obligatorias

**Tarea multi-paso → checkpoint inmediato**
Al recibir cualquier tarea que tome más de un turno: llama ` + "`mem_checkpoint`" + ` con ` + "`task`" + ` y ` + "`next_step`" + ` antes de empezar. Actualiza el checkpoint después de cada paso completado o antes de cualquier operación larga.

**Buscar antes de implementar o responder**
Ante cualquier pregunta sobre el proyecto, antes de implementar una feature, o al depurar un error: llama ` + "`mem_search`" + ` primero. El contexto relevante puede estar guardado de sesiones anteriores.

**Guardar sin que te lo pidan**
- Bug resuelto → ` + "`mem_save(type:\"bugfix\")`" + ` con causa raíz y archivos
- Decisión tomada → ` + "`mem_save(type:\"decision\", topic_key:\"area/tema\")`" + `
- Descubrimiento no obvio → ` + "`mem_save(type:\"discovery\")`" + `

**Cerrar la sesión correctamente**
Cuando el usuario termine o indique que para: ` + "`mem_session_summary`" + ` con estructura Objetivo/Completado/Descubrimientos/Próximos pasos. Luego ` + "`mem_checkpoint(status:\"completed\")`" + ` si había tarea activa.

**Recuperar contexto perdido**
Si perdiste el hilo de lo que hacías: revisa el bloque "TAREA EN PROGRESO" inyectado al inicio, o llama ` + "`mem_context`" + ` para recargar observaciones recientes.

### topic_key obligatorio
Para types ` + "`decision`" + `, ` + "`architecture`" + `, ` + "`pattern`" + `, ` + "`config`" + `: siempre incluye ` + "`topic_key`" + ` en formato ` + "`area/tema`" + ` (ej: ` + "`\"db/postgres-driver\"`" + `). Esto permite upsert y evita duplicados.
`

// claudeMDFragment se mantiene como alias por compatibilidad — algún código o
// documentación externa puede referenciarlo con este nombre.
const claudeMDFragment = kronosRulesFragment

// rulesTarget describe un archivo de instrucciones de un agente: dónde vive
// y cómo se llama en los mensajes de usuario.
type rulesTarget struct {
	id       string // "claude-code" | "codex"
	label    string // nombre para mensajes
	filename string // "CLAUDE.md" | "AGENTS.md"
}

var rulesTargets = map[string]rulesTarget{
	"claude-code": {id: "claude-code", label: "Claude Code", filename: "CLAUDE.md"},
	"codex":       {id: "codex", label: "Codex", filename: "AGENTS.md"},
}

// runRules: `kronos rules [--install] [--target claude-code|codex]`.
// Sin --target, por compatibilidad hacia atrás, apunta a claude-code
// (CLAUDE.md) — comportamiento histórico del comando.
func runRules(args []string) error {
	targetID := "claude-code"
	install := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--install":
			install = true
		case "--target":
			if i+1 >= len(args) {
				return fmt.Errorf("--target requiere un valor: claude-code | codex")
			}
			targetID = args[i+1]
			i++
		}
	}

	target, ok := rulesTargets[targetID]
	if !ok {
		return fmt.Errorf("target desconocido %q — usa: claude-code | codex", targetID)
	}

	if install {
		return installRules(target)
	}

	fmt.Print(kronosRulesFragment)
	fmt.Printf("\n# Para instalarlo directamente en %s del proyecto actual:\n", target.filename)
	fmt.Printf("#   kronos rules --install --target %s\n", target.id)
	return nil
}

func installRules(target rulesTarget) error {
	path := filepath.Join(".", target.filename)

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("leer %s: %w", target.filename, err)
	}

	// Don't add if already present
	if contains(string(existing), "Kronos — Memoria persistente") {
		fmt.Printf("%s ya contiene la sección de Kronos. Sin cambios.\n", target.filename)
		return nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("abrir %s: %w", target.filename, err)
	}
	defer f.Close()

	separator := ""
	if len(existing) > 0 {
		separator = "\n---\n\n"
	}

	if _, err := fmt.Fprintf(f, "%s%s", separator, kronosRulesFragment); err != nil {
		return fmt.Errorf("escribir %s: %w", target.filename, err)
	}

	fmt.Printf("Sección de Kronos agregada a %s\n", path)
	return nil
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
