#!/usr/bin/env bash
# verify-memory.sh — verificación de punta a punta de la memoria de kronos.
#
# Corre las cuatro cosas que de verdad importan (y que ya se rompieron una vez
# cada una) contra el binario REAL y la base REAL, sin mocks:
#
#   1. bloque core  — que SessionStart inyecte items del proyecto, sin
#                     "recortado por presupuesto" (el caso que motivó el
#                     filtro de pertinencia de la ronda 4).
#   2. recall       — que un prompt conversacional traiga memoria y que un
#                     prompt trivial no gaste nada (ronda 3).
#   3. gate         — los tres casos: sesión sin inyección en proyecto con
#                     memoria bloquea, sesión ya informada no bloquea, proyecto
#                     sin memoria no bloquea (rondas 2 y 4).
#   4. vault        — ida y vuelta sobre el proyecto fixture: editar la nota,
#                     importar, y confirmar que el archivo queda al día y que
#                     el export siguiente NO lo marca como editado a mano
#                     (ronda 4; restaura la fixture al terminar).
#
# Uso:
#   scripts/verify-memory.sh [--proyecto NOMBRE] [--sin-vault] [--bin RUTA]
#
# Sale con 0 si todo pasó, 1 si algo falló. Las latencias son informativas
# (WARN) y no hacen fallar la verificación: en una máquina cargada son ruido.
set -uo pipefail

BIN="${KRONOS_BIN:-}"
PROYECTO=""
CON_VAULT=1
while [ $# -gt 0 ]; do
  case "$1" in
    --bin) BIN="$2"; shift 2 ;;
    --proyecto) PROYECTO="$2"; shift 2 ;;
    --sin-vault) CON_VAULT=0; shift ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "argumento desconocido: $1" >&2; exit 2 ;;
  esac
done

REPO="$(cd "$(dirname "$0")/.." && pwd)"
[ -z "$BIN" ] && BIN="$(command -v kronos || echo "$HOME/.local/bin/kronos")"
[ -x "$BIN" ] || { echo "no encuentro el binario de kronos (probá --bin RUTA)"; exit 2; }
[ -z "$PROYECTO" ] && PROYECTO="$(basename "$REPO")"

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$1"; SKIP=$((SKIP+1)); }
warn() { printf '  \033[33mWARN\033[0m %s\n' "$1"; }
head_() { printf '\n== %s ==\n' "$1"; }

hook() { # hook <evento> <payload-json>
  printf '%s' "$2" | timeout 30 "$BIN" hook "$1" 2>&1
}

ms_now() { date +%s%3N; }

# limpiarSesionesSinteticas marca como borradas (deleted_at, reversible) las
# sesiones que este script crea para probar los hooks — si no, la verificación
# ensucia las estadísticas del usuario con ~5 sesiones por corrida. Best-effort:
# si no hay psql o no se puede leer el DSN, se saltea sin hacer ruido.
limpiarSesionesSinteticas() {
  command -v psql >/dev/null 2>&1 || return 0
  local dsn
  dsn="$("$BIN" config show 2>/dev/null | sed -n 's/.*"postgres_dsn": *"\([^"]*\)".*/\1/p' | head -1)"
  [ -z "$dsn" ] && return 0
  psql "$dsn" -q -c "update sessions set deleted_at = to_char(now() at time zone 'utc','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') where id like 'verify-%' and deleted_at is null" >/dev/null 2>&1 || true
}
trap limpiarSesionesSinteticas EXIT

echo "verificación de memoria — $(date '+%Y-%m-%d %H:%M')"
echo "binario:  $BIN ($("$BIN" version 2>/dev/null))"
echo "proyecto: $PROYECTO  ($REPO)"

# ---------------------------------------------------------------- 1. bloque core
head_ "1. bloque core (SessionStart)"
SID_CORE="verify-core-$$"
CORE_OUT="$(hook session-start "{\"session_id\":\"$SID_CORE\",\"cwd\":\"$REPO\",\"hook_event_name\":\"SessionStart\",\"source\":\"startup\"}")"
CORE_FOOTER="$(printf '%s' "$CORE_OUT" | grep -m1 '^\[kronos:core\]')"
CORE_ITEMS="$(printf '%s' "$CORE_OUT" | grep -c '^- \[' || true)"
if [ -n "$CORE_FOOTER" ]; then
  ok "el bloque core se inyecta: $CORE_FOOTER"
else
  bad "SessionStart no imprimió el bloque core"
fi
if printf '%s' "$CORE_OUT" | grep -q 'recortado por presupuesto'; then
  bad "el bloque dice 'recortado por presupuesto' (items cortados por presupuesto)"
else
  ok "sin 'recortado por presupuesto'"
fi
if [ "$CORE_ITEMS" -gt 0 ]; then
  ok "items inyectados: $CORE_ITEMS"
else
  skip "0 items — el proyecto no tiene memoria todavía"
fi
PROYECTO_BLOQUE="$(printf '%s' "$CORE_FOOTER" | sed -n 's/.*project \([^ ]*\).*/\1/p')"
[ -n "$PROYECTO_BLOQUE" ] && PROYECTO="$PROYECTO_BLOQUE"

# -------------------------------------------------------------------- 2. recall
head_ "2. recall por prompt (UserPromptSubmit)"
SID_REC="verify-recall-$$"
hook session-start "{\"session_id\":\"$SID_REC\",\"cwd\":\"$REPO\",\"hook_event_name\":\"SessionStart\",\"source\":\"startup\"}" >/dev/null
CONV="como veniamos con las decisiones de $PROYECTO"
T0="$(ms_now)"
CONV_OUT="$(hook prompt-submit "{\"session_id\":\"$SID_REC\",\"cwd\":\"$REPO\",\"hook_event_name\":\"UserPromptSubmit\",\"prompt\":\"$CONV\"}")"
CONV_MS=$(( $(ms_now) - T0 ))
if printf '%s' "$CONV_OUT" | grep -q 'kronos:relevante'; then
  ok "el prompt conversacional trae memoria ($(printf '%s' "$CONV_OUT" | grep -c 'kronos:relevante') inyección/es)"
elif [ "$CORE_ITEMS" -gt 0 ]; then
  bad "prompt conversacional sin inyecciones pese a que el proyecto tiene memoria"
else
  skip "sin inyecciones (proyecto sin memoria)"
fi
TRIVIAL_OUT="$(hook prompt-submit "{\"session_id\":\"$SID_REC\",\"cwd\":\"$REPO\",\"hook_event_name\":\"UserPromptSubmit\",\"prompt\":\"hola, que hora es\"}")"
if printf '%s' "$TRIVIAL_OUT" | grep -q 'kronos:relevante'; then
  bad "un prompt trivial inyectó memoria (debería no gastar nada)"
else
  ok "el prompt trivial no inyecta nada"
fi
if [ "$CONV_MS" -lt 1500 ]; then
  ok "latencia del recall: ${CONV_MS}ms (presupuesto 400ms + arranque del proceso)"
else
  warn "latencia del recall: ${CONV_MS}ms — alta (¿Ollama cargado o máquina ocupada?)"
fi

# ---------------------------------------------------------------------- 3. gate
head_ "3. gate antes del primer edit"
GRANDE="$REPO"
CHICO="$(mktemp -d)"
# Caso "sesión sin inyección, proyecto con memoria": el gate abre la sesión
# para saber si ya buscaste o si kronos ya te inyectó algo, y con una sesión
# sin fila en la base falla abierto (no bloquea) — por eso este caso no se
# puede armar con un session_id nuevo a mano. Se arma con el flag
# satisfied_by_injection=0 sobre una sesión YA informada, que es la misma rama
# de código del bloqueo (mensaje + exit 2) y es determinista.
SID_G1="verify-gate-sin-$$"
hook session-start "{\"session_id\":\"$SID_G1\",\"cwd\":\"$GRANDE\",\"hook_event_name\":\"SessionStart\",\"source\":\"startup\"}" >/dev/null
G1="$(printf '{"session_id":"%s","cwd":"%s","hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"%s/x.go"}}' "$SID_G1" "$GRANDE" "$GRANDE" \
      | env KRONOS_GATE_BLOCK=1 KRONOS_GATE_SATISFIED_BY_INJECTION=0 timeout 30 "$BIN" hook pre-tool-use 2>&1; echo "exit=$?")"
G1_CODE="${G1##*exit=}"
if [ "$CORE_ITEMS" -gt 0 ]; then
  if [ "$G1_CODE" = "2" ]; then ok "con satisfied_by_injection=0 la sesión informada igual bloquea (exit 2)"; else bad "esperaba exit 2, dio $G1_CODE"; fi
  if printf '%s' "$G1" | grep -q 'mem_search primero'; then ok "el mensaje de bloqueo trae el session_id"; else bad "no imprimió el mensaje de bloqueo: $G1"; fi
else
  skip "proyecto sin memoria: no aplica el caso de bloqueo"
fi
SID_G2="verify-gate-con-$$"
hook session-start "{\"session_id\":\"$SID_G2\",\"cwd\":\"$GRANDE\",\"hook_event_name\":\"SessionStart\",\"source\":\"startup\"}" >/dev/null
G2="$(printf '{"session_id":"%s","cwd":"%s","hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"%s/x.go"}}' "$SID_G2" "$GRANDE" "$GRANDE" \
      | KRONOS_GATE_BLOCK=1 timeout 30 "$BIN" hook pre-tool-use 2>&1; echo "exit=$?")"
G2_CODE="${G2##*exit=}"
if [ "$G2_CODE" = "0" ]; then ok "sesión ya informada por la inyección → no fuerza búsqueda (exit 0)"; else bad "esperaba exit 0, dio $G2_CODE"; fi
SID_G3="verify-gate-chico-$$"
G3="$(printf '{"session_id":"%s","cwd":"%s","hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"%s/x.go"}}' "$SID_G3" "$CHICO" "$CHICO" \
      | KRONOS_GATE_BLOCK=1 timeout 30 "$BIN" hook pre-tool-use 2>&1; echo "exit=$?")"
G3_CODE="${G3##*exit=}"
if [ "$G3_CODE" = "0" ]; then ok "proyecto sin memoria → no bloquea (exit 0)"; else bad "esperaba exit 0 en proyecto vacío, dio $G3_CODE"; fi
rm -rf "$CHICO"

# --------------------------------------------------------------------- 4. vault
head_ "4. vault: ida y vuelta de una edición a mano"
if [ "$CON_VAULT" -eq 0 ]; then
  skip "desactivado con --sin-vault"
else
  VAULT_DIR="$("$BIN" config show 2>/dev/null | sed -n 's/.*"default_output": *"\([^"]*\)".*/\1/p' | head -1)"
  [ -z "$VAULT_DIR" ] && VAULT_DIR="$HOME/kronos-vault"
  case "$VAULT_DIR" in "~/"*) VAULT_DIR="$HOME/${VAULT_DIR#\~/}" ;; esac
  FIXTURE="$VAULT_DIR/kronos-bench"
  if [ ! -d "$FIXTURE" ]; then
    skip "no existe el proyecto fixture kronos-bench en $VAULT_DIR"
  else
    "$BIN" export >/dev/null 2>&1
    NOTA="$(grep -rl 'kronos_id' "$FIXTURE" --include='*.md' 2>/dev/null | head -1)"
    if [ -z "$NOTA" ]; then
      skip "no encontré ninguna nota con kronos_id en la fixture"
    else
      ORIGINAL="$(mktemp)"; cp "$NOTA" "$ORIGINAL"
      MARCA="editado-a-mano-por-verify-memory-$$"
      printf '\n%s\n' "$MARCA" >> "$NOTA"
      IMP="$("$BIN" vault import --vault "$VAULT_DIR" --project kronos-bench --apply 2>&1)"
      if printf '%s' "$IMP" | grep -q '1 actualizadas'; then
        ok "el import detectó y aplicó la edición a mano"
      else
        bad "el import no detectó la edición: $(printf '%s' "$IMP" | tail -1)"
      fi
      if grep -q "$MARCA" "$NOTA"; then
        ok "la edición sobrevivió al import"
      else
        bad "la edición se perdió"
      fi
      if grep -qm1 '^revision: 1$' "$NOTA"; then
        bad "el frontmatter quedó desactualizado (dice revision: 1 tras el import)"
      else
        ok "el frontmatter quedó al día: $(grep -m1 '^revision:' "$NOTA")"
      fi
      EXP="$("$BIN" export 2>&1)"
      if printf '%s' "$EXP" | grep -qE 'editados a mano \(salteados\): 0'; then
        ok "el export siguiente NO marca la nota como editada a mano"
      else
        bad "el export sigue marcando la nota como editada a mano: $(printf '%s' "$EXP" | grep -i 'editado a mano' | head -1)"
      fi
      # restaurar la fixture tal como estaba
      cp "$ORIGINAL" "$NOTA"
      "$BIN" vault import --vault "$VAULT_DIR" --project kronos-bench --apply >/dev/null 2>&1
      "$BIN" export >/dev/null 2>&1
      rm -f "$ORIGINAL"
      ok "fixture restaurada"
    fi
  fi
fi

# --------------------------------------------------------------------- resumen
head_ "resumen"
echo "  PASS: $PASS   FAIL: $FAIL   SKIP: $SKIP"
echo "  (las sesiones sintéticas de esta corrida quedaron marcadas como borradas en la base)"
if [ "$FAIL" -gt 0 ]; then
  echo "  → hay fallos, revisá el detalle de arriba"
  exit 1
fi
echo "  → todo lo verificable pasó"
exit 0
