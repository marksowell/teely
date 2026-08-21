#!/bin/sh

set -eu

SCRIPT_DIR=$(
  CDPATH= cd -- "$(dirname -- "$0")" && pwd
)
PROJECT_ROOT=$(
  CDPATH= cd -- "$SCRIPT_DIR/.." && pwd
)
CONFIG_PATH="${1:-${PROJECT_ROOT}/teely.local.json}"
BINARY_PATH="${PROJECT_ROOT}/teely"

if [ ! -x "$BINARY_PATH" ]; then
  printf 'ERROR: Missing Teely binary at %s\n' "$BINARY_PATH" >&2
  printf 'Build it first with: ./scripts/build-teely.sh\n' >&2
  exit 1
fi

if [ ! -f "$CONFIG_PATH" ]; then
  printf 'ERROR: Missing config file at %s\n' "$CONFIG_PATH" >&2
  exit 1
fi

eval "$("${PROJECT_ROOT}/scripts/print-config-paths.sh" "$CONFIG_PATH")"
OUTPUT_PATH="${2:-${CADDYFILE_PATH}}"
mkdir -p "$(dirname "$OUTPUT_PATH")"
"$BINARY_PATH" -config "$CONFIG_PATH" -print-caddyfile > "$OUTPUT_PATH"

printf 'Wrote Caddyfile:\n'
printf '  %s\n' "$OUTPUT_PATH"
