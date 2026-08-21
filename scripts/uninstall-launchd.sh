#!/bin/sh

set -eu

LABEL="${TEELY_LAUNCHD_LABEL:-com.marksowell.teely}"
PLIST_PATH="${HOME}/Library/LaunchAgents/${LABEL}.plist"

if [ -f "$PLIST_PATH" ]; then
  launchctl unload "$PLIST_PATH" >/dev/null 2>&1 || true
  rm -f "$PLIST_PATH"
  printf 'Removed Teely launch agent:\n'
  printf '  %s\n' "$PLIST_PATH"
else
  printf 'No Teely launch agent found at:\n'
  printf '  %s\n' "$PLIST_PATH"
fi
