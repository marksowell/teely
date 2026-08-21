#!/bin/sh

set -eu

SCRIPT_DIR=$(
  CDPATH= cd -- "$(dirname -- "$0")" && pwd
)
PROJECT_ROOT=$(
  CDPATH= cd -- "$SCRIPT_DIR/.." && pwd
)
CONFIG_PATH="${1:-${PROJECT_ROOT}/teely.local.json}"

if [ ! -f "$CONFIG_PATH" ]; then
  cp "${PROJECT_ROOT}/teely.json" "$CONFIG_PATH"
  printf 'Created local config from sample:
'
  printf '  %s
' "$CONFIG_PATH"
  printf '
'
  printf 'Edit that file with your real app paths and commands, then run this command again.
'
  exit 0
fi

eval "$(${PROJECT_ROOT}/scripts/print-config-paths.sh "$CONFIG_PATH")"

mkdir -p "${RUNTIME_DIR}/logs" "${RUNTIME_DIR}/run"

"${PROJECT_ROOT}/scripts/build-teely.sh"

if [ ! -x "$CADDY_BINARY_PATH" ]; then
  "${PROJECT_ROOT}/scripts/install-caddy.sh" "$CONFIG_PATH"
fi

"${PROJECT_ROOT}/scripts/write-caddyfile.sh" "$CONFIG_PATH"

TEELY_PID_FILE="${RUNTIME_DIR}/run/teely.pid"
TEELY_LOG="${RUNTIME_DIR}/logs/teely.log"
CADDY_PID_FILE="${RUNTIME_DIR}/run/caddy.pid"
CADDY_LOG="${RUNTIME_DIR}/logs/caddy.log"
TEELY_LISTEN_PORT="${LISTEN_ADDRESS##*:}"

is_running() {
  pid_file=$1
  if [ -f "$pid_file" ]; then
    pid=$(cat "$pid_file")
    if kill -0 "$pid" 2>/dev/null; then
      return 0
    fi
    rm -f "$pid_file"
  fi
  return 1
}

listener_pid() {
  port=$1
  lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null | head -n 1
}

wait_for_http() {
  url=$1
  tries=${2:-30}
  i=0
  while [ "$i" -lt "$tries" ]; do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  return 1
}

wait_for_pid() {
  pid_file=$1
  tries=${2:-10}
  i=0
  while [ "$i" -lt "$tries" ]; do
    if is_running "$pid_file"; then
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  return 1
}

if is_running "$TEELY_PID_FILE"; then
  printf 'Teely already running with pid %s
' "$(cat "$TEELY_PID_FILE")"
else
  existing_pid=$(listener_pid "$TEELY_LISTEN_PORT" || true)
  if [ -n "$existing_pid" ]; then
    printf 'Teely port %s is already in use by pid %s.
' "$TEELY_LISTEN_PORT" "$existing_pid" >&2
    printf 'Another Teely instance may already be running outside the managed scripts.
' >&2
    printf 'Stop it first or free the port, then run ./scripts/up.sh again.
' >&2
    exit 1
  fi
  nohup "${PROJECT_ROOT}/teely" -config "$CONFIG_PATH" >"$TEELY_LOG" 2>&1 &
  printf '%s
' "$!" > "$TEELY_PID_FILE"
fi

if ! wait_for_pid "$TEELY_PID_FILE" 5 || ! wait_for_http "http://127.0.0.1:8417/" 10; then
  printf 'ERROR: Teely did not start cleanly.
' >&2
  printf 'Log: %s
' "$TEELY_LOG" >&2
  exit 1
fi

if is_running "$CADDY_PID_FILE"; then
  printf 'Caddy already running with pid %s
' "$(cat "$CADDY_PID_FILE")"
else
  nohup "$CADDY_BINARY_PATH" run --config "$CADDYFILE_PATH" >"$CADDY_LOG" 2>&1 &
  printf '%s
' "$!" > "$CADDY_PID_FILE"
fi

if ! wait_for_pid "$CADDY_PID_FILE" 5; then
  printf 'ERROR: Caddy did not stay running.
' >&2
  printf 'Log: %s
' "$CADDY_LOG" >&2
  exit 1
fi

printf '
Teely is starting.
'
printf 'Manager UI: https://teely.localhost
'
for app_hostname in $(python3 - "$CONFIG_PATH" <<'PYSNIP'
import json, sys
with open(sys.argv[1], 'r', encoding='utf-8') as fh:
    cfg = json.load(fh)
for app in cfg.get('apps', []):
    host = app.get('hostname')
    if host:
        print(host)
PYSNIP
); do
  printf 'App URL: https://%s
' "$app_hostname"
done
printf '
Logs:
'
printf '  Teely: %s
' "$TEELY_LOG"
printf '  Caddy: %s
' "$CADDY_LOG"
printf "\nIf this is your first HTTPS run on this Mac, trust Caddy's local CA with:\n"
printf '  ./scripts/trust-caddy.sh
'
