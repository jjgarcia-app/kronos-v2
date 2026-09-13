# Verificación de la memoria

`scripts/verify-memory.sh` corre, en un solo comando y contra el binario real y
la base real (sin mocks), las cosas que de verdad importan de esta
memoria. Cada chequeo existe porque esa parte **se rompió al menos una vez** y
el fallo no se veía desde afuera: el agente simplemente "no se acordaba".

```
scripts/verify-memory.sh                 # todo (toca la fixture kronos-bench del vault)
scripts/verify-memory.sh --sin-vault     # solo bloque core, recall y gate
scripts/verify-memory.sh --bin /ruta/a/kronos
```

Sale con 0 si todo pasó, 1 si algo falló. Las latencias se reportan como WARN y
no hacen fallar la verificación: en una máquina cargada son ruido.

## Qué verifica y por qué

| Chequeo | Qué comprueba | El fallo que previene |
|---|---|---|
| **1. Bloque core** | que `SessionStart` inyecte items del proyecto, que ningún item venga cortado (`recortado por presupuesto`), cuántos items entraron, y que los digests de sesión (`type=session`) nunca superen `core.max_session_items` (default 1) | el bloque llegó a tener 6-7 items de ATISA sobre 12 en un proyecto que no era ATISA, y a cortar una línea al medio por presupuesto (ronda 4); sin el tope de `session`, los resúmenes automáticos de sesión desplazan decisiones/bugs/patrones reales |
| **2. Recall** | que un prompt conversacional traiga memoria y que un prompt trivial no gaste nada, con la latencia real, y que una vez que FTS ya alcanzó no se pague el round-trip vectorial (FTS-first) | los prompts conversacionales no inyectaban **nada** (el FTS usaba AND implícito y solo matcheaban queries de 2 palabras), y el camino vectorial podía comerse 1,5 s con Ollama cargado (ronda 3) |
| **3. Gate** | que una sesión ya informada **no** sea obligada a buscar, que con `satisfied_by_injection=0` sí bloquee, y que un proyecto sin memoria no bloquee nunca | con el gate en modo bloqueo cada sesión de código gastaba un turno buscando algo que kronos ya le había mostrado; y en proyectos sin memoria el bloqueo era trámite puro (rondas 2 y 4) |
| **4. Vault** | ida y vuelta de una edición hecha a mano: se detecta, se aplica, el `revision` del archivo queda al día y el export siguiente **no** marca la nota como "editada a mano" | una edición del vault se revertía en el export siguiente; y (ronda 4) el hash quedaba mal calculado y la nota salía del circuito del export para siempre |
| **5. Doctor: uso de generación** | que `kronos doctor` muestre el contador de llamadas de generación (por proveedor y resultado) | sin el contador, el consumo de la suscripción de Claude Code vía `llm.provider=claude-cli` era invisible — no había forma de saber cuánto se gastaba sin leer el archivo de uso a mano |
| **6. Doctor: pendientes de digest** | que la línea del digest desglose los reintentos pendientes por tipo (`N enriquecimiento, M hechos`, o `al día` sin ninguno) | antes de distinguir el tipo de pendiente, un enriquecimiento que tuvo éxito en la prosa pero sin sección de hechos explícita se perdía en silencio: no quedaba nada pendiente y nadie se enteraba que faltaban hechos tipados |

El chequeo 4 trabaja sobre el proyecto fixture `kronos-bench` del vault: guarda
la nota original, hace la prueba y la restaura al terminar. Solo avanza el
contador `revision` de esa nota.

Los chequeos 1 a 3 crean sesiones sintéticas (prefijo `verify-`) en la base real,
porque los hooks solo se pueden ejercitar de verdad con payloads reales. El
script las marca como borradas al terminar (`deleted_at`, reversible), así no
ensucian las estadísticas de sesiones.

## Qué NO verifica

  - **Si la memoria es útil**: eso no se mide con un script, se mide con trabajo
    real. Lo que sí se mide acá es que el canal funcione (que llegue, que sea
    pertinente, que no cueste de más).
  - **El camino con Claude Code de punta a punta**: para eso están las sesiones
    de benchmark (`~/orca/workspaces/kronos-bench`, 8-9 tareas reales), que son
    más lentas y se corren por ronda, no en cada cambio.
  - **Postgres**: la verificación usa lo que tenga configurado el binario. Si el
    store cae al buffer local, el bloque y el recall siguen funcionando (a
    propósito) pero con menos datos; `kronos doctor` es el que avisa de eso.

## Línea base medida (2026-09-11)

| Ronda | Qué cambió | Efecto medido |
|---|---|---|
| 1 | instrumentación del dual-store, índice GIN, umbral de recall 0,62, bloque core siempre-presente | la inyección automática funciona; 1 de 3 sesiones de código consultó memoria |
| 2 | gate en modo bloqueo, reparto del bloque core, tipo `intent` | 3 de 3 sesiones consultaron; costo: +57 s en la tarea de bugfix (117 → 174 s) |
| 3 | FTS por OR con guarda de precisión, presupuesto de 400 ms, sonda de proveedor; curaduría del bloque; consolidación con pre-filtro; vault de ida y vuelta | los prompts conversacionales empiezan a inyectar; 0 duplicados reales en el corpus; consolidación de 32 s a ~1 s |
| 4 | globales filtradas por pertinencia y con tope duro; gate satisfecho por inyección | 0 items de otro proyecto sobre 12 (antes 6-7); sin `recortado`; la búsqueda forzada por sesión desaparece cuando ya hubo inyección |

Números de hoy, con `kronos v2.18.4` y esta base: bloque core `12 items |
1432/2000 chars | project kronos-v2 | omitidos: 14 globales (poco pertinentes),
24 por límite de tipo, 6 por presupuesto`; recall 65-311 ms medidos incluyendo
el arranque del proceso; 925 observaciones, 654 sesiones, 22 proyectos, 0
relaciones pendientes; suite completa en 20 paquetes en verde.

## Cómo se verifica un cambio en la memoria

1. `go build ./... && go test ./... -count=1` — la suite (incluye los tests
   internos del vault y los del gate).
2. `scripts/verify-memory.sh` — el canal de punta a punta contra el binario real.
3. Si el cambio toca el recall o el bloque core: la ronda de benchmark con
   sesiones reales de Claude Code en `~/orca/workspaces/kronos-bench`, y
   comparar tiempos y comportamiento contra la tabla de arriba.
4. Si el cambio toca el almacenamiento: `kronos doctor` y revisar
   `~/.local/share/kronos/daemon.log` (los eventos de "primary caído" ahora
   dicen la causa).
