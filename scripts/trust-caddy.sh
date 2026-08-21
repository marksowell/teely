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

if [ ! -f "$CADDYFILE_PATH" ]; then
  "${PROJECT_ROOT}/scripts/write-caddyfile.sh" "$CONFIG_PATH"
fi

find_root_cert() {
  if [ -n "${CADDY_DATA_DIR:-}" ] && [ -f "${CADDY_DATA_DIR}/pki/authorities/local/root.crt" ]; then
    printf '%s\n' "${CADDY_DATA_DIR}/pki/authorities/local/root.crt"
    return 0
  fi

  if [ -f "$HOME/Library/Application Support/Caddy/pki/authorities/local/root.crt" ]; then
    printf '%s\n' "$HOME/Library/Application Support/Caddy/pki/authorities/local/root.crt"
    return 0
  fi

  if [ -f "$HOME/.local/share/caddy/pki/authorities/local/root.crt" ]; then
    printf '%s\n' "$HOME/.local/share/caddy/pki/authorities/local/root.crt"
    return 0
  fi

  return 1
}

is_trusted() {
  cert_path=$1
  /usr/bin/security find-certificate -Z -c "Caddy Local Authority" /Library/Keychains/System.keychain 2>/dev/null | \
    /usr/bin/grep -qi "$(/usr/bin/openssl x509 -noout -fingerprint -sha256 -in "$cert_path" | /usr/bin/cut -d= -f2)"
}

ROOT_CERT_PATH=$(find_root_cert || true)
if [ -z "$ROOT_CERT_PATH" ]; then
  printf 'ERROR: Could not find Caddy local CA certificate.\n' >&2
  printf 'Start Teely once so Caddy can generate it, then run this command again.\n' >&2
  exit 1
fi

if is_trusted "$ROOT_CERT_PATH"; then
  printf 'Caddy local CA is already trusted:\n' >&2
  printf '  %s\n' "$ROOT_CERT_PATH"
  exit 0
fi

printf 'Trusting Caddy local CA in the macOS System keychain:\n'
printf '  %s\n' "$ROOT_CERT_PATH"
sudo /usr/bin/security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain "$ROOT_CERT_PATH"
printf 'Caddy local CA trusted successfully.\n'
