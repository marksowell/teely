#!/bin/sh

set -eu

SCRIPT_DIR=$(
  CDPATH= cd -- "$(dirname -- "$0")" && pwd
)
PROJECT_ROOT=$(
  CDPATH= cd -- "$SCRIPT_DIR/.." && pwd
)
OUTPUT_PATH="${1:-${PROJECT_ROOT}/teely}"

cd "$PROJECT_ROOT"

VERSION=$(git describe --tags --always --dirty 2>/dev/null || printf 'dev')
LDFLAGS="-X github.com/marksowell/teely/internal/teely.Version=${VERSION}"

if [ "$(uname -s)" = "Darwin" ]; then
  TOOLCHAIN="${GOTOOLCHAIN:-go1.26.1}"
  env GOTOOLCHAIN="$TOOLCHAIN" go build -o "$OUTPUT_PATH" -ldflags="-linkmode=external ${LDFLAGS}" ./cmd/teely
else
  go build -o "$OUTPUT_PATH" -ldflags="$LDFLAGS" ./cmd/teely
fi

printf 'Built Teely:\n'
printf '  %s\n' "$OUTPUT_PATH"
printf '  version: %s\n' "$VERSION"
