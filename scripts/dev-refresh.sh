#!/bin/sh

set -eu

LABEL="${TEELY_LAUNCHD_LABEL:-com.marksowell.teely}"
SCRIPT_DIR=$(
  CDPATH= cd -- "$(dirname -- "$0")" && pwd
)
PROJECT_ROOT=$(
  CDPATH= cd -- "$SCRIPT_DIR/.." && pwd
)
CONFIG_PATH="${1:-${PROJECT_ROOT}/teely.local.json}"
PLIST_PATH="${HOME}/Library/LaunchAgents/${LABEL}.plist"

wait_for_http() {
  url=$1
  tries=${2:-15}
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

if [ ! -f "$CONFIG_PATH" ]; then
  printf 'ERROR: Missing config file at %s\n' "$CONFIG_PATH" >&2
  printf 'Create one first, for example:\n' >&2
  printf '  cp teely.json teely.local.json\n' >&2
  exit 1
fi

"${PROJECT_ROOT}/scripts/build-teely.sh"

if [ ! -f "$PLIST_PATH" ]; then
  "${PROJECT_ROOT}/scripts/install-launchd.sh" "$CONFIG_PATH"
else
  launchctl kickstart -k "gui/$(id -u)/${LABEL}"
fi

if wait_for_http "http://127.0.0.1:8417/" 15; then
  printf '\nTeely refreshed.\n'
  printf 'Manager UI: https://teely.localhost\n'
else
  printf '\nTeely was restarted, but the local UI did not answer in time.\n' >&2
  printf 'Check status with:\n' >&2
  printf '  launchctl print gui/$(id -u)/%s\n' "$LABEL" >&2
  exit 1
fi
