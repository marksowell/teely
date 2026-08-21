#!/bin/sh

set -eu

LABEL="${TEELY_LAUNCHD_LABEL:-com.marksowell.teely}"
SCRIPT_DIR=$(
  CDPATH= cd -- "$(dirname -- "$0")" && pwd
)
PROJECT_ROOT=$(
  CDPATH= cd -- "$SCRIPT_DIR/.." && pwd
)
PLIST_DIR="${HOME}/Library/LaunchAgents"
PLIST_PATH="${PLIST_DIR}/${LABEL}.plist"
BINARY_PATH="${PROJECT_ROOT}/teely"
CONFIG_PATH="${1:-${PROJECT_ROOT}/teely.local.json}"
WORKDIR="${PROJECT_ROOT}"
STDOUT_PATH="${TMPDIR:-/tmp}/teely.stdout.log"
STDERR_PATH="${TMPDIR:-/tmp}/teely.stderr.log"

if [ ! -x "$BINARY_PATH" ]; then
  printf 'ERROR: Missing Teely binary at %s\n' "$BINARY_PATH" >&2
  printf 'Build it first with: ./scripts/build-teely.sh\n' >&2
  exit 1
fi

if [ ! -f "$CONFIG_PATH" ]; then
  printf 'ERROR: Missing config file at %s\n' "$CONFIG_PATH" >&2
  printf 'Create one first, for example:\n' >&2
  printf '  cp teely.json teely.local.json\n' >&2
  exit 1
fi

mkdir -p "$PLIST_DIR"

cat > "$PLIST_PATH" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>${LABEL}</string>

    <key>ProgramArguments</key>
    <array>
      <string>${BINARY_PATH}</string>
      <string>-config</string>
      <string>${CONFIG_PATH}</string>
    </array>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <true/>

    <key>WorkingDirectory</key>
    <string>${WORKDIR}</string>

    <key>StandardOutPath</key>
    <string>${STDOUT_PATH}</string>

    <key>StandardErrorPath</key>
    <string>${STDERR_PATH}</string>
  </dict>
</plist>
EOF

launchctl unload "$PLIST_PATH" >/dev/null 2>&1 || true
launchctl load "$PLIST_PATH"

printf 'Installed Teely launch agent:\n'
printf '  %s\n' "$PLIST_PATH"
printf '\n'
printf 'Teely will now start at login and restart if it exits.\n'
printf 'Logs:\n'
printf '  stdout: %s\n' "$STDOUT_PATH"
printf '  stderr: %s\n' "$STDERR_PATH"
printf '\n'
printf 'Check status with:\n'
printf '  launchctl list | grep %s\n' "$LABEL"
