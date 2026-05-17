package mcp

import (
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func toolMemSave() mcpgo.Tool {
	return mcpgo.NewTool("mem_save",
		mcpgo.WithDescription("Guarda una observación en memoria persistente. Usar después de decisiones, bugs resueltos, descubrimientos o preferencias confirmadas."),
		mcpgo.WithString("title", mcpgo.Required(), mcpgo.Description("Título corto y buscable. Formato: Verbo + qué. Ej: 'Elegimos SQLite sobre Postgres'")),
		mcpgo.WithString("content", mcpgo.Required(), mcpgo.Description("Contenido detallado. Incluir: Qué, Por qué, Dónde (archivos), Aprendizajes")),
		mcpgo.WithString("type", mcpgo.Required(), mcpgo.Description("Tipo: bugfix | decision | architecture | discovery | pattern | config | preference | passive")),
		mcpgo.WithString("project", mcpgo.Description("Nombre del proyecto. Si se omite, se detecta automáticamente")),
		mcpgo.WithString("session_id", mcpgo.Description("ID de la sesión activa")),
		mcpgo.WithString("topic_key", mcpgo.Description("Clave estable para upsert. Ej: 'architecture/auth'. Si existe, actualiza en vez de crear")),
		mcpgo.WithString("scope", mcpgo.Description("'project' (default) o 'global' para que sea visible en todos los proyectos")),
	)
}

func toolMemSearch() mcpgo.Tool {
	return mcpgo.NewTool("mem_search",
		mcpgo.WithDescription("Busca en memoria por palabras clave usando FTS5. Retorna resultados ordenados por relevancia."),
		mcpgo.WithString("query", mcpgo.Required(), mcpgo.Description("Términos de búsqueda. Soporta frases entre comillas y operadores AND/OR/NOT")),
		mcpgo.WithString("project", mcpgo.Description("Filtrar por proyecto. Si se omite, busca en todos")),
		mcpgo.WithString("limit", mcpgo.Description("Máximo de resultados (default: 10)")),
	)
}

func toolMemContext() mcpgo.Tool {
	return mcpgo.NewTool("mem_context",
		mcpgo.WithDescription("Retorna las observaciones más recientes de la sesión actual o del proyecto. Usar al inicio de sesión para recuperar contexto previo."),
		mcpgo.WithString("project", mcpgo.Required(), mcpgo.Description("Nombre del proyecto")),
		mcpgo.WithString("session_id", mcpgo.Description("Si se provee, filtra por sesión")),
		mcpgo.WithString("limit", mcpgo.Description("Máximo de observaciones (default: 10)")),
	)
}

func toolMemGetObservation() mcpgo.Tool {
	return mcpgo.NewTool("mem_get_observation",
		mcpgo.WithDescription("Obtiene el contenido completo de una observación por ID. Usar cuando mem_search retorna un resultado truncado."),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("ID numérico de la observación")),
	)
}

func toolMemUpdate() mcpgo.Tool {
	return mcpgo.NewTool("mem_update",
		mcpgo.WithDescription("Actualiza título, contenido o tipo de una observación existente. Incrementa revision_count."),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("ID de la observación a actualizar")),
		mcpgo.WithString("title", mcpgo.Description("Nuevo título (opcional)")),
		mcpgo.WithString("content", mcpgo.Description("Nuevo contenido (opcional)")),
		mcpgo.WithString("type", mcpgo.Description("Nuevo tipo (opcional)")),
	)
}

func toolMemSessionStart() mcpgo.Tool {
	return mcpgo.NewTool("mem_session_start",
		mcpgo.WithDescription("Inicia una nueva sesión de memoria para el proyecto."),
		mcpgo.WithString("project", mcpgo.Required(), mcpgo.Description("Nombre del proyecto")),
		mcpgo.WithString("directory", mcpgo.Description("Directorio de trabajo")),
		mcpgo.WithString("session_id", mcpgo.Description("ID de sesión (se genera automáticamente si se omite)")),
	)
}

func toolMemSessionEnd() mcpgo.Tool {
	return mcpgo.NewTool("mem_session_end",
		mcpgo.WithDescription("Cierra la sesión activa."),
		mcpgo.WithString("session_id", mcpgo.Required(), mcpgo.Description("ID de la sesión a cerrar")),
	)
}

func toolMemSessionSummary() mcpgo.Tool {
	return mcpgo.NewTool("mem_session_summary",
		mcpgo.WithDescription("Guarda el resumen de la sesión al cerrar. OBLIGATORIO antes de terminar. Incluir: Goal, Accomplished, Discoveries, Next Steps, Relevant Files."),
		mcpgo.WithString("session_id", mcpgo.Required(), mcpgo.Description("ID de la sesión")),
		mcpgo.WithString("summary", mcpgo.Required(), mcpgo.Description("Resumen estructurado de la sesión")),
		mcpgo.WithString("project", mcpgo.Description("Nombre del proyecto")),
	)
}

func toolMemSavePrompt() mcpgo.Tool {
	return mcpgo.NewTool("mem_save_prompt",
		mcpgo.WithDescription("Guarda el prompt del usuario para contexto futuro."),
		mcpgo.WithString("content", mcpgo.Required(), mcpgo.Description("Contenido del prompt")),
		mcpgo.WithString("project", mcpgo.Required(), mcpgo.Description("Nombre del proyecto")),
		mcpgo.WithString("session_id", mcpgo.Description("ID de la sesión")),
	)
}
