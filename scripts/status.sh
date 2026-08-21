#!/bin/sh

set -eu

SCRIPT_DIR=$(
  CDPATH= cd -- "$(dirname -- "$0")" && pwd
)
PROJECT_ROOT=$(
  CDPATH= cd -- "$SCRIPT_DIR/.." && pwd
)
CONFIG_PATH="${1:-${PROJECT_ROOT}/teely.local.json}"

eval "$(${PROJECT_ROOT}/scripts/print-config-paths.sh "$CONFIG_PATH")"

TEELY_LISTEN_PORT="${LISTEN_ADDRESS##*:}"

listener_pid() {
  port=$1
  lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null | head -n 1
}

report() {
  pid_file=$1
  name=$2
  hint=$3
  if [ -f "$pid_file" ]; then
    pid=$(cat "$pid_file")
    if kill -0 "$pid" 2>/dev/null; then
      printf '%s: running (pid %s)%s
' "$name" "$pid" "$hint"
      return
    fi
  fi
  printf '%s: stopped
' "$name"
}

report "${RUNTIME_DIR}/run/teely.pid" "Teely" ""
report "${RUNTIME_DIR}/run/caddy.pid" "Caddy" ""

orphan_teely_pid=$(listener_pid "$TEELY_LISTEN_PORT" || true)
if [ -n "$orphan_teely_pid" ]; then
  if [ ! -f "${RUNTIME_DIR}/run/teely.pid" ] || [ "$(cat "${RUNTIME_DIR}/run/teely.pid" 2>/dev/null || true)" != "$orphan_teely_pid" ]; then
    printf 'Teely listener detected outside managed pid file: pid %s on port %s
' "$orphan_teely_pid" "$TEELY_LISTEN_PORT"
  fi
fi
