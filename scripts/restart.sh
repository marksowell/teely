#!/bin/sh

set -eu

SCRIPT_DIR=$(
  CDPATH= cd -- "$(dirname -- "$0")" && pwd
)
PROJECT_ROOT=$(
  CDPATH= cd -- "$SCRIPT_DIR/.." && pwd
)
CONFIG_PATH="${1:-${PROJECT_ROOT}/teely.local.json}"

"${PROJECT_ROOT}/scripts/down.sh" "$CONFIG_PATH"
printf '\nRestarting Teely and Caddy...\n\n'
"${PROJECT_ROOT}/scripts/up.sh" "$CONFIG_PATH"
