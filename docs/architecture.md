# Arquitectura de kronos

Este documento describe cómo está construido kronos para alguien que no participó en su desarrollo. Si estás leyendo esto para decidir si confiar tu memoria de trabajo a este sistema, o para contribuir código, empieza acá.

## Qué es kronos

kronos es un servidor de memoria persistente para agentes de IA (hoy: Claude Code), pensado para correr **local, uno por desarrollador**. No es un backend multi-tenant — no hay concepto de usuario ni de equipo compartiendo una instancia. Cada desarrollador levanta su propio kronos, en su propia máquina.

Resuelve un problema concreto: un desarrollador que corre múltiples sesiones de Claude Code en paralelo (en distintos repos, o varias a la vez en el mismo) necesita que:
- las tools de IA busquen en decisiones/bugs/patrones pasados antes de repetir trabajo o repetir errores,
- lo que se descubre en una sesión quede guardado sin depender pura y exclusivamente de que el agente se acuerde de guardarlo,
- correr N sesiones en paralelo no multiplique la carga sobre la base de datos ni sobre el LLM local usado para juicios de calidad.

## Componentes

```
┌─────────────────┐  stdio (MCP)   ┌──────────────────────┐
│  Claude Code     │◄──────────────►│  kronos mcp (proxy)  │  ← uno por sesión de Claude Code
│  (sesión N)      │                │  proceso corto        │
└─────────────────┘                └───────────┬───────────┘
                                                 │ HTTP (StreamableHTTP MCP
                                                 │ + REST interno)
                                                 ▼
                                    ┌──────────────────────┐
                                    │  kronos serve         │  ← UN SOLO proceso,
                                    │  --daemon-mode         │     compartido por todas
                                    │  :4317                 │     las sesiones
                                    └───────────┬───────────┘
                                                 │
                        ┌────────────────────────┼────────────────────────┐
                        ▼                        ▼                        ▼
              ┌──────────────────┐   ┌──────────────────┐   ┌──────────────────┐
              │ SQLite (buffer)   │   │ Postgres (primary)│  │ Ollama (LLM local)│
              │ siempre local     │   │ local o remoto    │   │ embeddings + judge │
              └──────────────────┘   └──────────────────┘   └──────────────────┘
```

Además de MCP, Claude Code invoca kronos vía **hooks** (`SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PreCompact`, `Stop`, `SubagentStop`) — procesos cortos (`kronos hook <nombre>`) que corren en cada evento del ciclo de vida de una sesión. Algunos hooks (`prompt-submit`, `pre-compact`) delegan al daemon compartido por HTTP para no abrir su propia conexión a Postgres/Ollama en cada invocación; el resto abre una conexión local propia con timeout corto (ver `hookConnectTimeout` en `cmd/kronos/hook.go`).

## Por qué un daemon único, no un proceso por sesión

Antes de esto, cada sesión de Claude Code levantaba su propio proceso `kronos` completo — su propia conexión SQLite, su propia copia del vector store de embeddings en RAM, sus propios loops de reindex/judge contra Ollama. Con hasta 11 sesiones en paralelo (el caso real del desarrollador para el que se diseñó esto), eso multiplicaba directamente la contención contra Ollama (4 cores, sin GPU) — el mismo problema de contención que ya existía *dentro* de un proceso, multiplicado por 11.

El arreglo: `kronos serve` es el daemon (bindea `:4317`, corre indefinidamente, expone MCP vía `StreamableHTTPServer` en `/mcp` + un REST interno). `kronos mcp` es un proxy delgado — intenta conectar al daemon; si no responde, lo lanza detached y reintenta con backoff. El bind del puerto es un mutex implícito a nivel de SO: si 11 sesiones arrancan a la vez, una gana la carrera y las demás caen a ser proxies puros. Si el daemon muere a mitad de sesión, el proxy detecta el fallo en la próxima llamada y reintenta/relanza.

## Almacenamiento: `DualStore`

`internal/store.DualStore` es la pieza central. Envuelve dos backends:

- **`primary`**: Postgres (o SQLite, si no hay Postgres configurado — ver `internal/store/store_postgres.go` vs `store.go`).
- **`buffer`**: SQLite local, siempre presente.

Contrato: toda escritura intenta `primary` primero. Si `primary` está sano, escribe ahí y termina. Si falla, escribe en `buffer` **y** encola la operación en `sync_queue` (tabla local: `entity_type`, `payload` JSON, `created_at`). Un loop en background (`DualStore.syncLoop`, con retry schedule de 60s hasta 60min) drena esa cola contra `primary` apenas puede reconectar — `replayEntry` sabe reproducir cada tipo de operación (`save_observation`, `create_session`, `increment_search_count`, etc.) de forma idempotente.

Toda lectura intenta `primary` primero; si no está o no tiene la fila, cae a `buffer`.

**Punto importante para quien vaya a tocar esto**: cualquier método nuevo agregado a `*Store` (SQLite/Postgres concreto) necesita su contraparte explícita en `DualStore` — no hay type assertion mágica que lo resuelva solo. Ya pasó dos veces en producción (`CountSessionPrompts`/`CountSessionObservations` nunca se implementaron en `DualStore` durante meses; el nudge de guardado nunca disparó por eso).

**Otro punto importante**: las queries de `*Store` deben pasar por `s.queryRow`/`s.query`/`s.exec` (que aplican `rebind()`, traduciendo `?` a `$1`, `$2`... para Postgres) — nunca `s.db.QueryRowContext` directo con placeholders `?`. Postgres rechaza `?` con syntax error; con el error silenciado (`_ = row.Scan(&n)`, patrón común en este codebase para fail-open), el síntoma es una función que siempre devuelve el zero-value sin ningún error visible. Ya pasó en producción — ver `internal/store/store_postgres_test.go`, el único test que corre contra Postgres real en vez de SQLite y que hubiera atrapado esto antes.

## Nombres de proyecto

`internal/project.Detect(cwd)` resuelve el nombre de proyecto en 6 pasos de prioridad: `.kronos/config.json` explícito → `kronos.toml` → git remote → git root → único sub-repo hijo → basename del directorio. El resultado siempre pasa por `project.Normalize()` (minúsculas, no-alfanumérico → guión).

Cualquier código que reciba un nombre de proyecto **explícito** (no autodetectado) — por ejemplo el parámetro `project` de las tools MCP — también debe normalizarlo antes de tocar la DB. Esto vive centralizado en `internal/store` (`SaveObservation`, `CreateSession`), no en cada caller — es el choke point que evita que "ATISA", "atisa" y "atisa-provider-management-all-in-one" terminen como filas separadas (encontrado y corregido en producción: 27 nombres de proyecto para ~15 repos reales antes del fix).

## Identidad de sesión y el gate de pre-edición

Claude Code no le pasa el `session_id` real a los servidores MCP por protocolo (limitación documentada: `anthropics/claude-code#41836`). Esto importa porque `PreToolUse` (el hook que corre antes de cada `Edit`/`Write`/`Bash`) sí recibe el `session_id` real directo de Claude Code, y bloquea la edición si esa sesión todavía no llamó `mem_search` (`sess.SearchCount == 0`). Sin el `session_id` real, `mem_search` no puede acreditar la búsqueda a la sesión correcta.

Fix: `SessionStart` imprime el `session_id` real explícito al arrancar, y el mensaje de bloqueo de `PreToolUse` lo repite embebido — el agente lo tiene a mano en el momento exacto en que lo necesita, sin depender de adivinarlo por archivo o por "sesión más reciente en DB" (mecanismo que existe como respaldo, pero falla con sesiones concurrentes del mismo proyecto — se llegaron a medir 36 "activas" simultáneas para un solo proyecto).

## Captura de memoria: tres niveles, ninguno reemplaza al agente decidiendo guardar

1. **Explícito** (`mem_save`) — el agente decide qué vale la pena guardar. El camino principal, y el único con contenido curado.
2. **Nudge** (`prompt_submit.go`) — cada N prompts sin un `mem_save` real desde el último guardado (no desde el inicio de la sesión — ver `CountSessionPromptsSinceLastSave`), un recordatorio se inyecta en la salida del hook. Es un aviso, no un guardado — el agente puede ignorarlo.
3. **Captura pasiva vía LLM local** (`RunPreCompactCapture`, `internal/hooks/pre_compact_capture.go`) — justo antes de que Claude Code compacte el contexto (evento raro, no cada turno), el daemon lee la cola del transcript, le pregunta al LLM local (Ollama) si documenta algo guardable, y si sí lo guarda como `type: passive` — sin que el agente tenga que hacer nada. Corre async, fire-and-forget desde el hook (el hook solo le avisa al daemon con un POST de 500ms de timeout; el trabajo real sigue en una goroutine del daemon, desacoplada de esa request) — la compactación de Claude Code nunca se demora por esto.

## Mapa de paquetes (`internal/`)

| Paquete | Qué hace |
|---|---|
| `store` | `DualStore`, `*Store` (SQLite/Postgres), migraciones, sync queue — el corazón |
| `mcp` | Handlers de las tools MCP (`mem_save`, `mem_search`, etc.), servidor MCP |
| `hooks` | Lógica de negocio de cada hook de Claude Code (sin acoplarse a stdio/daemon) |
| `server` | Servidor HTTP del daemon — REST interno + endpoints que reusan el daemon (`/hooks/prompt-submit`, `/hooks/pre-compact-capture`) |
| `project` | Detección y normalización de nombre de proyecto |
| `secrets` | Detección/redacción de secretos antes de que entren a una observación |
| `embeddings` | Vector store para búsqueda semántica |
| `relations` | Detección de relaciones/conflictos entre observaciones |
| `judge` | Juicio de relaciones vía LLM (`mem_judge`, `AutoJudge`) |
| `llm` | Clientes de LLM: Ollama (local, siempre disponible sin config), OpenAI, Anthropic |
| `transcript` | Lectura acotada del `.jsonl` de transcript de una sesión (para la captura pasiva) |
| `checkpoint` | Red de seguridad de "en qué estaba trabajando" ante compactación/cierre sin resumen |
| `obsidian` | Espejo de observaciones a un vault de Obsidian |
| `platform` | Paths de datos/config por SO |
| `setup` | Instalador de hooks en `settings.json` de Claude Code |
| `doctor` | Diagnóstico de salud (`kronos doctor`) |
| `sync` | (ver también `store/sync_queue.go` — la lógica real vive ahí) |
| `config` | Carga de `config.json` |
| `tui` | Interfaz de terminal (`kronos tui`) |
| `wizard` | Asistente interactivo de setup |
| `api`, `session` | Directorios vacíos, sin uso — candidatos a limpieza |

## Limitaciones conocidas (al momento de escribir esto)

- Sin gestor de secretos: `postgres_dsn` (con contraseña) y tokens de API viven en texto plano en `config.json`/`settings.json`. El contenido de las observaciones sí puede cifrarse (ver abajo), la config del propio kronos todavía no.
- `kronos service <install|uninstall|start|stop|restart|status>` (`cmd/kronos/service.go`, vía `kardianos/service` — Windows Service/systemd/launchd) ya existe para correr el daemon supervisado por el SO en vez de a mano. `install` requiere privilegios de administrador y modifica el Service Control Manager — no se corrió contra ninguna máquina real todavía, queda a criterio de cada quien decidir cuándo activarlo.
- Cifrado de contenido (`internal/store/crypto.go`, `Store.SetEncryptionKey`) existe y está probado (AES-256-GCM, transparente en `SaveObservation`/`scanObservation`) pero **no está conectado** a `DualStore`/config todavía — falta resolver que la búsqueda FTS de Postgres no puede indexar contenido cifrado server-side. La política pendiente de decidir: forzar `Search` siempre al buffer local (plaintext) cuando primary tiene clave configurada, o algún otro mecanismo. Ver el TODO en `crypto.go`.
- Sync entre máquinas: `DualStore` ya lee de `primary` con "primary gana si está sano" — dos máquinas apuntando al mismo Postgres ya replican lectura en tiempo real hoy, sin código nuevo (probado en `multi_device_sync_test.go`). Lo que falta para que esto sea un producto real: la conexión de cifrado de arriba, y una forma de provisionar/gestionar el Postgres compartido sin que el usuario tenga que operarlo él mismo.
- `DualStore.SetLocalOnlyProjects` (`config.DBConfig.LocalOnlyProjects`) ya permite marcar proyectos que nunca salen de la máquina, incluso con primary sano — pensado como la base de "qué sincroniza y qué no" del roadmap de sync.

## Cómo correr esto localmente

```bash
go build ./...
go vet ./...
go test ./...
kronos setup          # instala los hooks en Claude Code
kronos doctor         # verifica config/DB/Ollama
kronos serve --daemon-mode   # levanta el daemon a mano (normalmente lo hace el proxy solo)
```

`kronos.toml` / `.kronos/config.json` controlan el nombre de proyecto explícito por repo. `~/.config/kronos/config.json` (o el equivalente por SO, ver `internal/platform`) controla backend de DB, Ollama, nudge, etc.
