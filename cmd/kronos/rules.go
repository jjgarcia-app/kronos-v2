package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// claudeMDFragment is the CLAUDE.md section for Layer 2 harness.
// Users add this to their project or global CLAUDE.md for stronger enforcement.
const claudeMDFragment = `## Kronos — Memoria persistente entre sesiones

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

func runRules(args []string) error {
	// with --install flag, write directly into current directory's CLAUDE.md
	if len(args) > 0 && args[0] == "--install" {
		return installRules()
	}

	// default: print the fragment to stdout
	fmt.Print(claudeMDFragment)
	fmt.Println("\n# Para instalarlo directamente en CLAUDE.md del proyecto actual:")
	fmt.Println("#   kronos rules --install")
	return nil
}

func installRules() error {
	target := filepath.Join(".", "CLAUDE.md")

	existing, err := os.ReadFile(target)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("leer CLAUDE.md: %w", err)
	}

	// Don't add if already present
	if contains(string(existing), "Kronos — Memoria persistente") {
		fmt.Println("CLAUDE.md ya contiene la sección de Kronos. Sin cambios.")
		return nil
	}

	f, err := os.OpenFile(target, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("abrir CLAUDE.md: %w", err)
	}
	defer f.Close()

	separator := ""
	if len(existing) > 0 {
		separator = "\n---\n\n"
	}

	_, err = fmt.Fprintf(f, "%s%s", separator, claudeMDFragment)
	if err != nil {
		return fmt.Errorf("escribir CLAUDE.md: %w", err)
	}

	fmt.Printf("Sección de Kronos agregada a %s\n", target)
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
