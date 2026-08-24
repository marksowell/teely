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

mkdir -p "${RUNTIME_DIR}/logs" "${RUNTIME_DIR}/run"

TEELY_LOG="${RUNTIME_DIR}/logs/teely.log"
CADDY_LOG="${RUNTIME_DIR}/logs/caddy.log"
CADDY_PID_FILE="${RUNTIME_DIR}/run/caddy.pid"

if [ ! -x "${PROJECT_ROOT}/teely" ]; then
  printf 'ERROR: Missing Teely binary at %s\n' "${PROJECT_ROOT}/teely" >&2
  printf 'Build it first with: ./scripts/build-teely.sh\n' >&2
  exit 1
fi

if [ ! -x "$CADDY_BINARY_PATH" ]; then
  printf 'ERROR: Missing Teely-managed Caddy binary at %s\n' "$CADDY_BINARY_PATH" >&2
  printf 'Install it first with: ./scripts/install-caddy.sh %s\n' "$CONFIG_PATH" >&2
  exit 1
fi

"${PROJECT_ROOT}/scripts/write-caddyfile.sh" "$CONFIG_PATH"

if [ -f "$CADDY_PID_FILE" ]; then
  pid=$(cat "$CADDY_PID_FILE" 2>/dev/null || true)
  if [ -n "${pid:-}" ] && kill -0 "$pid" 2>/dev/null; then
    :
  else
    rm -f "$CADDY_PID_FILE"
  fi
fi

if [ ! -f "$CADDY_PID_FILE" ]; then
  nohup "$CADDY_BINARY_PATH" run --config "$CADDYFILE_PATH" >"$CADDY_LOG" 2>&1 &
  printf '%s\n' "$!" > "$CADDY_PID_FILE"
fi

exec "${PROJECT_ROOT}/teely" -config "$CONFIG_PATH" >>"$TEELY_LOG" 2>&1
