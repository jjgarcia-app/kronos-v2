package mcp

import (
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func toolMemSave() mcpgo.Tool {
	return mcpgo.NewTool("mem_save",
		mcpgo.WithDescription(`CUÁNDO LLAMAR — llama este tool cuando ocurra cualquiera de estas condiciones:
  • Se resuelve un bug y se identifica la causa raíz
  • Se toma una decisión de arquitectura, diseño o implementación
  • El usuario confirma una preferencia, patrón o comportamiento esperado
  • Se descubre algo no obvio: quirks de configuración, comportamiento inesperado de una API o librería, restricciones del entorno
  • Se encuentra un workaround o restricción que costó tiempo resolver

NO LLAMAR para: información trivial u obvia, cosas ya documentadas en el código, estado transitorio, resúmenes de conversación.

CAMPO topic_key — OBLIGATORIO para types: decision / architecture / pattern / config
  Usa una clave path-like estable. Ejemplos: "db/postgres-driver", "auth/jwt-strategy", "api/rate-limiting"
  El topic_key permite upsert: si el mismo tema se actualiza, no se crea duplicado.

CAMPO content — usa esta estructura:
  Qué: [descripción del hecho]
  Por qué: [motivación o causa]
  Archivos: [path:línea de los archivos relevantes]
  Cómo aplicar: [consecuencia práctica o regla a seguir]

CAMPO scope — usa "global" solo para patrones reutilizables entre proyectos. Default "project" para todo lo demás.`),
		mcpgo.WithString("title", mcpgo.Required(), mcpgo.Description("Frase verbal corta y buscable. Formato: Verbo + qué. Ej: 'Elegimos pgx sobre lib/pq por compatibilidad con RETURNING'")),
		mcpgo.WithString("content", mcpgo.Required(), mcpgo.Description("Nota estructurada: Qué ocurrió | Por qué importa | Archivos relevantes (path:línea) | Cómo aplicar o reproducir")),
		mcpgo.WithString("type", mcpgo.Required(), mcpgo.Description("Tipo: bugfix | decision | architecture | discovery | pattern | config | preference | passive")),
		mcpgo.WithString("project", mcpgo.Description("Nombre del proyecto. Si se omite, se detecta automáticamente del directorio de trabajo")),
		mcpgo.WithString("session_id", mcpgo.Description("ID de la sesión activa. Proveerlo si está disponible")),
		mcpgo.WithString("topic_key", mcpgo.Description("OBLIGATORIO para types decision/architecture/pattern/config. Clave estable tipo path. Ej: 'db/connection-pool'. Permite upsert — actualiza sin duplicar")),
		mcpgo.WithString("scope", mcpgo.Description("'project' (default) para observaciones del proyecto actual. 'global' solo si el patrón aplica a cualquier proyecto")),
	)
}

func toolMemSearch() mcpgo.Tool {
	return mcpgo.NewTool("mem_search",
		mcpgo.WithDescription(`CUÁNDO LLAMAR — llama este tool ANTES de:
  • Responder cualquier pregunta sobre historial del proyecto, decisiones pasadas, errores conocidos o configuraciones
  • Implementar una feature no trivial (verificar si fue intentada antes o hay una decisión previa)
  • Depurar un error (verificar si fue visto y resuelto antes)
  • Recomendar una librería, herramienta o enfoque (verificar elecciones previas del proyecto)
  • El usuario pregunta "¿recuerdas...?" o "¿ya vimos...?"

NO esperes a que el usuario pida explícitamente buscar. Busca de forma proactiva cuando el contexto del proyecto sea necesario.

TIPS:
  • Usa términos específicos: nombres de librerías, mensajes de error, nombres de archivo, keywords de decisiones
  • Soporta operadores: AND, OR, NOT y frases entre comillas
  • Si no encuentras resultados, prueba términos más cortos o sinónimos`),
		mcpgo.WithString("query", mcpgo.Required(), mcpgo.Description("Términos de búsqueda. Ej: 'postgres driver error', 'jwt auth decision', 'migrations FK constraint'")),
		mcpgo.WithString("project", mcpgo.Description("Filtrar por proyecto. Si se omite, busca en todos los proyectos")),
		mcpgo.WithString("limit", mcpgo.Description("Máximo de resultados (default: 10)")),
	)
}

func toolMemContext() mcpgo.Tool {
	return mcpgo.NewTool("mem_context",
		mcpgo.WithDescription(`NOTA: el hook SessionStart ya inyecta automáticamente las últimas 8 observaciones del proyecto al inicio de cada conversación. No es necesario llamar este tool al inicio de sesión.

CUÁNDO LLAMAR:
  • Necesitas más de 8 observaciones del proyecto
  • Quieres filtrar observaciones de la sesión actual específicamente
  • El usuario cambia de contexto de proyecto a mitad de conversación
  • Quieres refrescar el contexto después de varios turnos sin memoria reciente

Retorna observaciones ordenadas por fecha descendente.`),
		mcpgo.WithString("project", mcpgo.Required(), mcpgo.Description("Nombre del proyecto")),
		mcpgo.WithString("session_id", mcpgo.Description("Si se provee, retorna solo observaciones de esa sesión")),
		mcpgo.WithString("limit", mcpgo.Description("Máximo de observaciones (default: 10)")),
	)
}

func toolMemGetObservation() mcpgo.Tool {
	return mcpgo.NewTool("mem_get_observation",
		mcpgo.WithDescription(`CUÁNDO LLAMAR: cuando mem_search retorna un resultado truncado (...) y necesitas el contenido completo para tomar una decisión o dar una respuesta precisa.

No hagas inferencias basadas en contenido truncado — obtén la observación completa primero.`),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("ID numérico de la observación (aparece en los resultados de mem_search)")),
	)
}

func toolMemUpdate() mcpgo.Tool {
	return mcpgo.NewTool("mem_update",
		mcpgo.WithDescription(`CUÁNDO LLAMAR: cuando una observación existente ya no es precisa o nueva información la refina.

PREFERIR mem_update sobre mem_save cuando el topic_key coincide con una observación existente — actualizar incrementa revision_count y preserva historial.

Flujo recomendado:
  1. mem_search para encontrar la observación a actualizar
  2. mem_get_observation si el contenido está truncado
  3. mem_update con el ID y los campos modificados`),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("ID de la observación a actualizar (obtenido con mem_search)")),
		mcpgo.WithString("title", mcpgo.Description("Nuevo título (omitir para no cambiar)")),
		mcpgo.WithString("content", mcpgo.Description("Nuevo contenido (omitir para no cambiar)")),
		mcpgo.WithString("type", mcpgo.Description("Nuevo tipo (omitir para no cambiar)")),
	)
}

func toolMemSessionStart() mcpgo.Tool {
	return mcpgo.NewTool("mem_session_start",
		mcpgo.WithDescription(`NOTA: en uso normal con Claude Code, el hook SessionStart crea la sesión automáticamente. No es necesario llamar este tool en conversaciones normales.

CUÁNDO LLAMAR:
  • Usas Kronos fuera de Claude Code (via API directa o testing)
  • Inicias una sub-sesión independiente para una tarea específica
  • El hook no corrió (verificable si mem_context no retorna sesión activa)

project es OBLIGATORIO. session_id: si se omite, Kronos genera uno automáticamente.`),
		mcpgo.WithString("project", mcpgo.Required(), mcpgo.Description("Nombre del proyecto")),
		mcpgo.WithString("directory", mcpgo.Description("Directorio de trabajo absoluto")),
		mcpgo.WithString("session_id", mcpgo.Description("ID único de sesión. Si se omite, se genera automáticamente")),
	)
}

func toolMemSessionEnd() mcpgo.Tool {
	return mcpgo.NewTool("mem_session_end",
		mcpgo.WithDescription(`IMPORTANTE: siempre llama mem_session_summary ANTES de este tool. Cerrar una sesión sin resumen pierde el registro de aprendizajes de esa sesión.

NOTA: el hook Stop cierra la sesión automáticamente al terminar la conversación en Claude Code. Llama este tool manualmente solo cuando cierras una sub-sesión que iniciaste explícitamente con mem_session_start.`),
		mcpgo.WithString("session_id", mcpgo.Required(), mcpgo.Description("ID de la sesión a cerrar")),
	)
}

func toolMemSessionSummary() mcpgo.Tool {
	return mcpgo.NewTool("mem_session_summary",
		mcpgo.WithDescription(`ACCIÓN MÁS IMPORTANTE AL CERRAR UNA SESIÓN. Llama este tool cuando:
  • El usuario indica que terminó la tarea o la conversación
  • Se completó un bloque de trabajo significativo
  • Antes de que el usuario cierre Claude Code

El hook Stop cierra la sesión automáticamente, pero NO guarda el resumen — eso es responsabilidad del agente.

ESTRUCTURA OBLIGATORIA del summary:

## Objetivo
[Qué se buscaba lograr en esta sesión]

## Completado
- [tarea completada 1]
- [tarea completada 2]

## Descubrimientos clave
[Hallazgos no obvios, bugs resueltos, decisiones tomadas — cada uno debería tener su mem_save correspondiente]

## Próximos pasos
[Qué queda pendiente]

## Archivos relevantes
[Lista de paths principales modificados o consultados]`),
		mcpgo.WithString("session_id", mcpgo.Required(), mcpgo.Description("ID de la sesión activa")),
		mcpgo.WithString("summary", mcpgo.Required(), mcpgo.Description("Resumen estructurado siguiendo la plantilla: Objetivo / Completado / Descubrimientos clave / Próximos pasos / Archivos relevantes")),
		mcpgo.WithString("project", mcpgo.Description("Nombre del proyecto (recomendado para trazabilidad)")),
	)
}

func toolMemDelete() mcpgo.Tool {
	return mcpgo.NewTool("mem_delete",
		mcpgo.WithDescription(`CUÁNDO LLAMAR: cuando una observación es factualmente incorrecta, fue guardada por error, o está tan desactualizada que confundiría al agente en futuras sesiones.

NO USAR PARA: información antigua pero válida — la memoria persistente es intencional. NO usar para "limpiar" — usa mem_update si el contenido cambió.

Flujo: mem_search → verificar con mem_get_observation → mem_delete con el ID.
El borrado es soft-delete (deleted_at). Los datos siguen en la DB pero no aparecen en búsquedas.`),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("ID numérico de la observación a eliminar (obtenido con mem_search)")),
	)
}

func toolMemCheckpoint() mcpgo.Tool {
	return mcpgo.NewTool("mem_checkpoint",
		mcpgo.WithDescription(`HERRAMIENTA ANTI-PÉRDIDA DE CONTEXTO. Guarda el estado exacto de una tarea en progreso para que sea recuperado automáticamente al inicio de la siguiente conversación, incluso después de una compactación de contexto.

CUÁNDO LLAMAR — OBLIGATORIO en estos momentos:
  • Al inicio de cualquier tarea que tome más de un turno
  • Después de completar cada paso significativo de una tarea en curso
  • Antes de cualquier operación larga (compilación, tests, deploy)
  • Cuando el usuario dice "continúa mañana", "sigue después" o similar
  • Cada vez que actualizas el estado de lo que estás haciendo

CUÁNDO CERRAR (status: "completed"):
  • Cuando la tarea se completa exitosamente
  • Cuando el usuario confirma que se terminó
  • Antes de cambiar a una tarea completamente diferente

CAMPOS:
  • task (obligatorio): descripción clara de qué estás haciendo. Ej: "Implementar autenticación JWT en el servicio de usuarios"
  • next_step (obligatorio): la acción CONCRETA e inmediata que debe ejecutarse al retomar. Ej: "Agregar middleware validateToken en routes/auth.go línea 45"
  • progress: último paso que completaste. Ej: "Escribí el handler generateToken, compila sin errores"
  • files: archivos que estás modificando activamente (separados por coma). Ej: "internal/auth/jwt.go, routes/auth.go"
  • notes: restricciones importantes, blockers o contexto no obvio. Ej: "pgx requiere $N placeholders, no usar ?"
  • project: nombre del proyecto (auto-detectado si se omite)`),
		mcpgo.WithString("task", mcpgo.Required(), mcpgo.Description("Descripción clara de qué estás trabajando actualmente")),
		mcpgo.WithString("next_step", mcpgo.Required(), mcpgo.Description("Acción concreta e inmediata a ejecutar al retomar. Debe ser lo suficientemente específica para actuar sin contexto adicional")),
		mcpgo.WithString("progress", mcpgo.Description("Último paso completado antes de este checkpoint")),
		mcpgo.WithString("files", mcpgo.Description("Archivos activos separados por coma: path/to/file.go, path/to/other.go")),
		mcpgo.WithString("notes", mcpgo.Description("Restricciones críticas, blockers o contexto no obvio que debe recordarse")),
		mcpgo.WithString("project", mcpgo.Description("Nombre del proyecto. Si se omite, se usa el configurado")),
		mcpgo.WithString("status", mcpgo.Description("'active' (default) para guardar/actualizar. 'completed' para cerrar el checkpoint cuando la tarea termina")),
	)
}

func toolMemJudge() mcpgo.Tool {
	return mcpgo.NewTool("mem_judge",
		mcpgo.WithDescription(`Resuelve un conflicto pendiente entre dos observaciones asignándole un verbo de relación.

CUÁNDO LLAMAR: cuando mem_save devuelve "Conflictos potenciales detectados" con judgment_id(s).
Resolver conflictos es importante para mantener la base de conocimiento coherente.

VERBOS VÁLIDOS:
  • related — relacionadas pero no conflictivas
  • compatible — compatibles, pueden coexistir
  • scoped — una es subconjunto de la otra
  • conflicts_with — información contradictoria (requiere reason)
  • supersedes — la observación fuente reemplaza a la destino
  • not_conflict — falso positivo del detector, ignorar`),
		mcpgo.WithString("judgment_id", mcpgo.Required(), mcpgo.Description("ID de la relación pendiente (aparece en el output de mem_save)")),
		mcpgo.WithString("relation", mcpgo.Required(), mcpgo.Description("Verbo: related | compatible | scoped | conflicts_with | supersedes | not_conflict")),
		mcpgo.WithString("reason", mcpgo.Required(), mcpgo.Description("Explicación del juicio. Obligatoria para conflicts_with y supersedes")),
		mcpgo.WithString("evidence", mcpgo.Description("Cita textual o archivo que respalda el juicio")),
		mcpgo.WithString("confidence", mcpgo.Description("Confianza 0.0–1.0 (default: 0.8)")),
		mcpgo.WithString("session_id", mcpgo.Description("ID de la sesión activa")),
	)
}

func toolMemCompare() mcpgo.Tool {
	return mcpgo.NewTool("mem_compare",
		mcpgo.WithDescription(`Registra un veredicto semántico directo entre dos observaciones (sin pasar por el detector BM25).

CUÁNDO LLAMAR: cuando identifies manualmente que dos observaciones tienen una relación semántica importante
que el detector no capturó, o cuando quieres registrar una relación entre observaciones específicas.

Acepta IDs numéricos o sync_ids. UPSERT: si la relación ya existe en cualquier dirección, se actualiza.`),
		mcpgo.WithString("source_id", mcpgo.Required(), mcpgo.Description("ID numérico o sync_id de la observación origen")),
		mcpgo.WithString("target_id", mcpgo.Required(), mcpgo.Description("ID numérico o sync_id de la observación destino")),
		mcpgo.WithString("relation", mcpgo.Required(), mcpgo.Description("Verbo: related | compatible | scoped | conflicts_with | supersedes | not_conflict")),
		mcpgo.WithString("reason", mcpgo.Required(), mcpgo.Description("Por qué se establece esta relación")),
		mcpgo.WithString("confidence", mcpgo.Description("Confianza 0.0–1.0 (default: 0.7)")),
	)
}

func toolMemSuggestTopicKey() mcpgo.Tool {
	return mcpgo.NewTool("mem_suggest_topic_key",
		mcpgo.WithDescription(`Genera una sugerencia de topic_key estable a partir del título y tipo de una observación.

CUÁNDO LLAMAR: antes de hacer mem_save con type=decision/architecture/pattern/config cuando no estás
seguro de qué topic_key usar. Un topic_key consistente evita duplicados entre sesiones.

El topic_key generado sigue el formato "tipo/palabra1-palabra2-palabra3".`),
		mcpgo.WithString("title", mcpgo.Required(), mcpgo.Description("Título de la observación que se quiere guardar")),
		mcpgo.WithString("type", mcpgo.Description("Tipo de observación (default: misc)")),
	)
}

func toolMemTimeline() mcpgo.Tool {
	return mcpgo.NewTool("mem_timeline",
		mcpgo.WithDescription(`Muestra las observaciones antes y después de una observación específica dentro de la misma sesión.

CUÁNDO LLAMAR: cuando quieres entender el contexto cronológico de una observación — qué llevó a ella
y qué decisiones se tomaron inmediatamente después. Útil al revisar el historial de una sesión.`),
		mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("ID de la observación pivot")),
		mcpgo.WithString("window", mcpgo.Description("Número de observaciones antes y después (default: 3)")),
	)
}

func toolMemStats() mcpgo.Tool {
	return mcpgo.NewTool("mem_stats",
		mcpgo.WithDescription(`Muestra estadísticas del estado de la base de conocimiento de Kronos.

CUÁNDO LLAMAR:
  • Al inicio de una sesión de trabajo para tener una visión general
  • Cuando sospechas que hay muchos conflictos pendientes sin resolver
  • Para verificar el estado de sincronización con PostgreSQL`),
		mcpgo.WithString("project", mcpgo.Description("Proyecto para incluir stats de relaciones/conflictos")),
	)
}

func toolMemCurrentProject() mcpgo.Tool {
	return mcpgo.NewTool("mem_current_project",
		mcpgo.WithDescription(`Detecta el proyecto del directorio de trabajo usando el algoritmo de 5 casos de Kronos.

CUÁNDO LLAMAR: cuando no estás seguro de qué nombre de proyecto usar en mem_save/mem_search,
o cuando el hook SessionStart no corrió y necesitas verificar la detección automática.

El algoritmo prioriza: config file → git remote → git root → child repo → dirname.`),
		mcpgo.WithString("directory", mcpgo.Description("Directorio absoluto a inspeccionar. Si se omite, usa el directorio de trabajo actual")),
	)
}

func toolMemCapturePassive() mcpgo.Tool {
	return mcpgo.NewTool("mem_capture_passive",
		mcpgo.WithDescription(`Captura aprendizajes del output de sub-agentes o herramientas externas como observación tipo "passive".

CUÁNDO LLAMAR: cuando un sub-agente, tool de bash, o proceso externo produce output que contiene
información útil para recordar — pero el agente principal no lo generó directamente.

A diferencia de mem_save, no requiere formato estructurado. El título se extrae de la primera línea.`),
		mcpgo.WithString("content", mcpgo.Required(), mcpgo.Description("Texto del output a capturar")),
		mcpgo.WithString("project", mcpgo.Required(), mcpgo.Description("Nombre del proyecto")),
		mcpgo.WithString("session_id", mcpgo.Description("ID de la sesión activa")),
	)
}

func toolMemMergeProjects() mcpgo.Tool {
	return mcpgo.NewTool("mem_merge_projects",
		mcpgo.WithDescription(`Fusiona todas las observaciones de un proyecto en otro (renombra el proyecto origen).

CUÁNDO LLAMAR: cuando un proyecto fue guardado con dos nombres distintos ("kronos-v2" y "kronos_v2",
por ejemplo) y quieres unificarlos. Todas las observaciones de "from" pasan a "to".

PRECAUCIÓN: operación irreversible. Verifica con mem_search primero qué hay en cada proyecto.`),
		mcpgo.WithString("from", mcpgo.Required(), mcpgo.Description("Nombre del proyecto origen (se vacía)")),
		mcpgo.WithString("to", mcpgo.Required(), mcpgo.Description("Nombre del proyecto destino (recibe todas las observaciones)")),
	)
}

func toolMemDoctor() mcpgo.Tool {
	return mcpgo.NewTool("mem_doctor",
		mcpgo.WithDescription(`Ejecuta un diagnóstico completo del estado de Kronos.

CUÁNDO LLAMAR:
  • Cuando Kronos parece comportarse de forma inesperada
  • Al inicio de una sesión de depuración
  • Para verificar si hay relaciones pendientes o sync atrasado

Retorna: estado del store, conteos, relaciones pendientes, sync queue, data dir.`),
	)
}

func toolMemSavePrompt() mcpgo.Tool {
	return mcpgo.NewTool("mem_save_prompt",
		mcpgo.WithDescription(`NOTA: el hook UserPromptSubmit guarda los prompts del usuario automáticamente en cada turno de conversación con Claude Code. No es necesario llamar este tool en uso normal.

CUÁNDO LLAMAR: solo si usas Kronos fuera de Claude Code (via API directa o testing sin hooks activos).`),
		mcpgo.WithString("content", mcpgo.Required(), mcpgo.Description("Contenido del prompt del usuario")),
		mcpgo.WithString("project", mcpgo.Required(), mcpgo.Description("Nombre del proyecto")),
		mcpgo.WithString("session_id", mcpgo.Description("ID de la sesión")),
	)
}
