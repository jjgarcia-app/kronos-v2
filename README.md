# Kronos v2

Memoria persistente para agentes de IA. Servidor MCP que captura, indexa y recupera conocimiento entre sesiones de trabajo.

## Instalación

### macOS / Linux

```bash
curl -fsSL https://raw.githubusercontent.com/jjgarcia-app/kronos-v2/main/install.sh | sh
```

### Windows (PowerShell)

```powershell
irm https://raw.githubusercontent.com/jjgarcia-app/kronos-v2/main/install.ps1 | iex
```

### Con Go instalado

```bash
go install github.com/jjgarcia-app/kronos-v2/cmd/kronos@latest
kronos init
```

Ambos scripts detectan tu OS y arquitectura automáticamente, descargan el binario correcto y lanzan el asistente de configuración.

---

## Primeros pasos

El wizard interactivo configura todo:

```
kronos init
```

Guía paso a paso:
1. Verifica que el binario esté en PATH
2. Configura la ruta de la base de datos
3. Detecta o instala Ollama (opcional — habilita búsqueda semántica)
4. Registra Kronos en tus agentes de IA (Claude Code, Cursor, Windsurf)

---

## Agentes soportados

| Agente | Tipo de integración |
|--------|---------------------|
| Claude Code | Hooks + MCP server |
| Cursor | MCP server |
| Windsurf | MCP server |

---

## Comandos

```
kronos init      Asistente de configuración guiado
kronos serve     Inicia el servidor MCP (stdio)
kronos tui       Interfaz visual interactiva
kronos doctor    Verificar estado del sistema
kronos setup     Instalar en agentes manualmente
kronos config    Ver y editar configuración
kronos export    Exportar vault de Obsidian
kronos version   Mostrar versión
```

---

## Herramientas MCP

Una vez conectado, el agente tiene acceso a:

| Tool | Descripción |
|------|-------------|
| `memory_save` | Guardar una observación |
| `memory_search` | Buscar por texto (FTS + semántica) |
| `memory_context` | Recuperar contexto relevante para el prompt actual |
| `memory_update` | Actualizar observación existente |
| `memory_get` | Obtener observación por ID |
| `memory_session_start` | Iniciar sesión |
| `memory_session_end` | Cerrar sesión |
| `memory_session_summary` | Guardar resumen de sesión |

---

## Backends

| Componente | Por defecto | Alternativa |
|------------|-------------|-------------|
| Base de datos | SQLite (puro Go, sin CGO) | PostgreSQL |
| Embeddings | Ollama (bge-m3) | Sin embeddings |
| Búsqueda | FTS5 (SQLite) / pg_tsvector (PG) | — |

---

## Verificar instalación

```
kronos doctor
```

```
[OK] Config file:       ~/.config/kronos/config.json
[OK] Base de datos:     ~/.local/share/kronos/kronos.db
[OK] Ollama:            http://localhost:11434 OK
[OK] Modelo embeddings: bge-m3 instalado
[OK] Hooks Claude Code: instalados
[OK] Binario en PATH:   /usr/local/bin/kronos
```

---

## Requisitos

- Go 1.21+ (solo para compilar desde fuente)
- Sin CGO — binario estático, sin dependencias del sistema
- Ollama opcional para búsqueda semántica

## Licencia

MIT
