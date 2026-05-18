# Kronos

Memoria persistente para agentes de IA. Servidor MCP que captura, indexa y recupera conocimiento entre sesiones de trabajo — para que los agentes recuerden qué pasó ayer, qué decidiste la semana pasada y qué errores ya resolviste.

## Cómo funciona

Kronos corre como servidor MCP en stdio. Tu agente lo invoca como herramienta; Kronos persiste todo en SQLite local (opcionalmente replicado a PostgreSQL). Cada sesión queda registrada, cada decisión indexada, cada error aprendido.

```
Claude Code → MCP (stdio) → Kronos → SQLite / PostgreSQL
                                    → Vector store (bge-m3)
                                    → Hooks (SessionStart, Prompts, Stop)
```

Los hooks inyectan contexto automáticamente al inicio de cada conversación — el agente ya sabe en qué proyecto estás, qué se hizo en la última sesión y qué tareas quedaron pendientes.

---

## Instalación

### macOS / Linux

```bash
curl -fsSL https://raw.githubusercontent.com/jjgarcia-app/kronos-v2/main/install.sh | sh
```

### Windows

```powershell
irm https://raw.githubusercontent.com/jjgarcia-app/kronos-v2/main/install.ps1 | iex
```

### Con Go

```bash
go install github.com/jjgarcia-app/kronos-v2/cmd/kronos@latest
```

Tras instalar, ejecuta el asistente de configuración:

```bash
kronos init
```

---

## Primeros pasos

`kronos init` guía el proceso completo:

1. Verifica que el binario esté en PATH
2. Configura la base de datos (SQLite local o PostgreSQL)
3. Detecta o instala Ollama (habilita búsqueda semántica con bge-m3)
4. Registra Kronos en Claude Code, Cursor y/o Windsurf

---

## Agentes soportados

| Agente | Integración |
|--------|-------------|
| Claude Code | Hooks + MCP server |
| Cursor | MCP server |
| Windsurf | MCP server |

Para registrar manualmente:

```bash
kronos setup claude-code
kronos setup cursor
kronos setup --all
```

---

## Comandos

| Comando | Descripción |
|---------|-------------|
| `kronos init` | Asistente de configuración guiado (TUI) |
| `kronos serve` | Inicia el servidor MCP en stdio |
| `kronos tui` | Explorador visual de la memoria |
| `kronos doctor` | Verifica el estado del sistema |
| `kronos setup` | Registra Kronos en agentes de IA |
| `kronos config` | Ver y editar configuración |
| `kronos sync` | Exportar / importar vault entre máquinas |
| `kronos export` | Exportar observaciones a Obsidian |
| `kronos gc` | Limpiar observaciones obsoletas |
| `kronos rules` | Generar fragmento de CLAUDE.md |
| `kronos version` | Mostrar versión |

---

## Herramientas MCP (20 tools)

Una vez conectado, el agente dispone de:

### Memoria

| Tool | Descripción |
|------|-------------|
| `mem_save` | Guardar observación (decisión, aprendizaje, patrón, bug resuelto) |
| `mem_search` | Buscar por texto — FTS5 + semántica (BM25 + bge-m3 + RRF) |
| `mem_context` | Recuperar las observaciones más recientes del proyecto activo |
| `mem_get_observation` | Obtener observación completa por ID |
| `mem_update` | Actualizar observación existente |
| `mem_delete` | Eliminar observación (soft-delete) |
| `mem_capture_passive` | Capturar output de sub-agentes o herramientas externas |

### Sesiones

| Tool | Descripción |
|------|-------------|
| `mem_session_start` | Abrir sesión de trabajo |
| `mem_session_end` | Cerrar sesión |
| `mem_session_summary` | Guardar resumen de la sesión |
| `mem_save_prompt` | Registrar prompt importante |

### Análisis y relaciones

| Tool | Descripción |
|------|-------------|
| `mem_judge` | Evaluar relación entre dos observaciones |
| `mem_compare` | Comparar versiones de una observación |
| `mem_suggest_topic_key` | Sugerir clave de tópico para una observación |
| `mem_timeline` | Ver evolución temporal de un tema |
| `mem_stats` | Estadísticas del vault |

### Estado y proyectos

| Tool | Descripción |
|------|-------------|
| `mem_checkpoint` | Guardar estado de tarea en progreso |
| `mem_current_project` | Obtener proyecto activo detectado |
| `mem_merge_projects` | Fusionar dos proyectos |
| `mem_doctor` | Diagnosticar estado interno de Kronos |

---

## Backends

| Componente | Por defecto | Alternativa |
|------------|-------------|-------------|
| Base de datos | SQLite (WAL, puro Go, sin CGO) | PostgreSQL (DualStore async) |
| Búsqueda textual | FTS5 (SQLite) / pg_tsvector (PG) | — |
| Embeddings | Ollama bge-m3 (chromem-go, sin CGO) | Sin embeddings |
| Reranking | RRF k=60 (FTS + vector) | Solo FTS |
| LLM para relaciones | Ollama (auto-detect) | OpenAI · Anthropic |

PostgreSQL es opcional. Cuando está configurado, actúa como réplica async — el servidor arranca inmediatamente aunque Postgres no esté disponible.

---

## Auto-judge de relaciones

Kronos detecta observaciones similares y las clasifica automáticamente en background:

- **Similitud < 0.30** → `not_conflict` (falso positivo BM25)
- **Similitud > 0.70** → `related` (observaciones relacionadas)
- **0.30 – 0.70** → delega al LLM generativo para resolución

El goroutine de auto-judge corre cada 5 minutos. Sin LLM configurado, las relaciones ambiguas quedan pendientes para revisión manual.

---

## Verificar instalación

```bash
kronos doctor
```

```
[OK] Config:          ~/.config/kronos/config.json
[OK] Base de datos:   ~/.local/share/kronos/kronos.db  (v40)
[OK] Ollama:          http://localhost:11434
[OK] Modelo:          bge-m3 instalado
[OK] Hooks:           SessionStart, UserPromptSubmit, SubagentStop, Stop
[OK] PATH:            /usr/local/bin/kronos
[OK] MCP:             Claude Code conectado — 20 tools
```

---

## Configuración

```bash
kronos config path        # muestra la ruta del archivo de config
kronos config show        # muestra config actual
kronos config set key val # actualiza un valor
```

Archivo de configuración en `~/.config/kronos/config.json`:

```json
{
  "db": {
    "backend": "sqlite",
    "sqlite_path": "~/.local/share/kronos/kronos.db"
  },
  "embeddings": {
    "ollama_url": "http://localhost:11434",
    "model": "bge-m3"
  },
  "llm": {
    "provider": "ollama",
    "model": "llama3.2"
  }
}
```

---

## Requisitos

- Sin CGO — binario estático, sin dependencias del sistema
- Go 1.21+ solo si compilas desde fuente
- Ollama opcional (búsqueda semántica + auto-judge LLM)
- PostgreSQL opcional (replicación async)

---

## Licencia

MIT
