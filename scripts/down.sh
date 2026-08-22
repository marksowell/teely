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

stop_from_pid_file() {
  pid_file=$1
  name=$2
  if [ -f "$pid_file" ]; then
    pid=$(cat "$pid_file")
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      i=0
      while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 10 ]; do
        sleep 1
        i=$((i + 1))
      done
      if kill -0 "$pid" 2>/dev/null; then
        kill -9 "$pid" 2>/dev/null || true
      fi
      printf 'Stopped %s (pid %s)
' "$name" "$pid"
    fi
    rm -f "$pid_file"
  fi
}

stop_listener_port() {
  port=$1
  name=$2
  if [ -z "$port" ]; then
    return
  fi
  pid=$(lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null | head -n 1 || true)
  if [ -n "$pid" ]; then
    kill "$pid" 2>/dev/null || true
    i=0
    while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 10 ]; do
      sleep 1
      i=$((i + 1))
    done
    if kill -0 "$pid" 2>/dev/null; then
      kill -9 "$pid" 2>/dev/null || true
    fi
    printf 'Stopped %s listener on port %s (pid %s)\n' "$name" "$port" "$pid"
  fi
}

stop_from_pid_file "${RUNTIME_DIR}/run/teely.pid" "Teely"
stop_listener_port "${TEELY_LISTEN_PORT}" "Teely"
stop_from_pid_file "${RUNTIME_DIR}/run/caddy.pid" "Caddy"
