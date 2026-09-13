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
| LLM de generación (digest, captura pasiva, judge) | Ollama (local, auto-detect) | `claude-cli` (`claude -p`, usa la suscripción de Claude Code) · OpenAI · Anthropic |

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
[OK] Digest automático: último hace 4 min (sesión abc12345) | pendientes: al día
[OK] Uso de generación LLM: generación: 3 llamadas en la última hora (ollama 3: 3 ok) | hoy: 12 (12 ok) | última: hace 4 min
```

`kronos doctor` también cuenta cuántas llamadas de generación se hicieron (por proveedor y resultado: ok, error, salteada por carga/cortacircuitos/sin credenciales) y clasifica la última falla de `claude-cli` si la hubo — sin esto, el consumo de la suscripción de Claude Code vía `llm.provider=claude-cli` era invisible. El daemon (`kronos serve --daemon-mode`) escribe su log a `~/.local/share/kronos/daemon.log` en vez de a la terminal.

Para verificar el canal de memoria de punta a punta (que el bloque core llegue
pertinente, que el recall traiga lo conversacional, que el gate no estorbe y
que el vault permita ida y vuelta), hay un script que corre las cuatro cosas
contra el binario real:

```bash
scripts/verify-memory.sh
```

Sale 0 si todo pasó. El detalle de qué mide cada chequeo y la línea base medida
están en [`docs/verificacion-memoria.md`](docs/verificacion-memoria.md).

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
  },
  "core": {
    "max_session_items": 1
  },
  "recall": {
    "max_session_items": 1,
    "fts_timeout_ms": 5000,
    "total_budget_ms": 400
  },
  "digest": {
    "timeout_ms": 60000,
    "max_facts": 3,
    "promote_facts": true
  }
}
```

Knobs menos obvios:

| Clave | Default | Qué controla |
|---|---|---|
| `core.max_session_items` | 1 | Tope de resúmenes de sesión (`type=session`) en el bloque siempre-presente que inyecta `SessionStart` — sin tope, los digests automáticos desplazan decisiones/bugs/patrones reales. |
| `recall.max_session_items` | 1 | Mismo tope, para la inyección por relevancia de cada prompt (`UserPromptSubmit`). |
| `recall.fts_timeout_ms` | 5000 | Presupuesto exclusivo de la fase FTS del recall (barata, corre siempre primero). Generoso a propósito: si se corta, el recall devuelve vacío aunque el resultado ya estuviera en la mano — la fase entrega lo que ya obtuvo en vez de descartarlo. |
| `recall.total_budget_ms` | 400 | Presupuesto exclusivo de la fase **vectorial** oportunista (solo se paga si FTS no encontró nada) — separado de `fts_timeout_ms` para que un pico de carga no le robe presupuesto a la fase barata. |
| `digest.timeout_ms` | 60000 | Cuánto puede tardar el enriquecimiento por LLM del digest de sesión en la actualización periódica, que corre async en el daemon (más generoso que `llm.timeout_ms` porque nada del lado del usuario espera este resultado). |
| `digest.max_facts` | 3 | Cuántos hechos tipados (bugfix/decision/config/discovery/pattern/preference) puede promover a observaciones propias una sola actualización de digest. |
| `digest.promote_facts` | true | Si además de la prosa del digest se extraen y guardan esos hechos como observaciones individuales (misma llamada al LLM, no agrega una llamada nueva). |
| `llm.provider` | `ollama` | `ollama` (local) o `claude-cli` (invoca `claude -p --model <llm.model>`, usa la suscripción de Claude Code en vez de pagar una API aparte). |
| `llm.model` | `llama3.2` (ollama) / `haiku` (claude-cli) | Modelo de generación. |
| `llm.cli_path` | `claude` | Binario a invocar cuando `provider=claude-cli` (se resuelve por PATH si no es ruta absoluta). |
| `llm.timeout_ms` | 30000 | Timeout de una llamada de generación con `claude-cli`. |
| `llm.max_load_per_cpu` | 1.0 | Umbral del guardián de carga (`load1/NumCPU`) que saltea llamadas de generación en máquinas saturadas. Solo protege al modelo **local** (Ollama) — `claude-cli` genera en la nube, así que no hay CPU local que proteger y el guardián no le aplica. |

---

## Requisitos

- Sin CGO — binario estático, sin dependencias del sistema
- Go 1.21+ solo si compilas desde fuente
- Ollama opcional (búsqueda semántica + auto-judge LLM)
- PostgreSQL opcional (replicación async)

---

## Licencia

MIT
