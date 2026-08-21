#!/bin/sh

set -eu

LABEL="${TEELY_CADDY_LAUNCHD_LABEL:-com.marksowell.teely.caddy}"
SCRIPT_DIR=$(
  CDPATH= cd -- "$(dirname -- "$0")" && pwd
)
PROJECT_ROOT=$(
  CDPATH= cd -- "$SCRIPT_DIR/.." && pwd
)
PLIST_DIR="${HOME}/Library/LaunchAgents"
PLIST_PATH="${PLIST_DIR}/${LABEL}.plist"
CONFIG_PATH="${1:-${PROJECT_ROOT}/teely.local.json}"
STDOUT_PATH="${TMPDIR:-/tmp}/teely-caddy.stdout.log"
STDERR_PATH="${TMPDIR:-/tmp}/teely-caddy.stderr.log"

eval "$("${PROJECT_ROOT}/scripts/print-config-paths.sh" "$CONFIG_PATH")"
CADDYFILE_PATH="${2:-${CADDYFILE_PATH}}"
CADDY_BIN="${CADDY_BIN:-${CADDY_BINARY_PATH}}"

if [ ! -x "$CADDY_BIN" ]; then
  printf 'ERROR: Missing Teely-managed Caddy binary at %s\n' "$CADDY_BIN" >&2
  printf 'Install it first with: ./scripts/install-caddy.sh %s\n' "$CONFIG_PATH" >&2
  exit 1
fi

"${PROJECT_ROOT}/scripts/write-caddyfile.sh" "$CONFIG_PATH" "$CADDYFILE_PATH"
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
      <string>${CADDY_BIN}</string>
      <string>run</string>
      <string>--config</string>
      <string>${CADDYFILE_PATH}</string>
    </array>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <true/>

    <key>WorkingDirectory</key>
    <string>${PROJECT_ROOT}</string>

    <key>StandardOutPath</key>
    <string>${STDOUT_PATH}</string>

    <key>StandardErrorPath</key>
    <string>${STDERR_PATH}</string>
  </dict>
</plist>
EOF

launchctl unload "$PLIST_PATH" >/dev/null 2>&1 || true
launchctl load "$PLIST_PATH"

printf 'Installed Caddy launch agent:\n'
printf '  %s\n' "$PLIST_PATH"
printf '\n'
printf 'Caddyfile:\n'
printf '  %s\n' "$CADDYFILE_PATH"
printf '\n'
printf 'Logs:\n'
printf '  stdout: %s\n' "$STDOUT_PATH"
printf '  stderr: %s\n' "$STDERR_PATH"
