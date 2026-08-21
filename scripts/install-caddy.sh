#!/bin/sh

set -eu

SCRIPT_DIR=$(
  CDPATH= cd -- "$(dirname -- "$0")" && pwd
)
PROJECT_ROOT=$(
  CDPATH= cd -- "$SCRIPT_DIR/.." && pwd
)
CONFIG_PATH="${1:-${PROJECT_ROOT}/teely.local.json}"
VERSION="${CADDY_VERSION:-latest}"

eval "$("${PROJECT_ROOT}/scripts/print-config-paths.sh" "$CONFIG_PATH")"

mkdir -p "$(dirname "$CADDY_BINARY_PATH")"

case "$(uname -s)" in
  Darwin) os_name="mac" ;;
  *)
    printf 'ERROR: install-caddy.sh currently supports macOS only.\n' >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  arm64|aarch64) arch_name="arm64" ;;
  x86_64) arch_name="amd64" ;;
  *)
    printf 'ERROR: Unsupported CPU architecture: %s\n' "$(uname -m)" >&2
    exit 1
    ;;
esac

tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/teely-caddy.XXXXXX")
cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT INT TERM

release_json="${tmpdir}/release.json"
if [ "$VERSION" = "latest" ]; then
  release_url="https://api.github.com/repos/caddyserver/caddy/releases/latest"
else
  release_url="https://api.github.com/repos/caddyserver/caddy/releases/tags/v${VERSION}"
fi

curl -fsSL "$release_url" -o "$release_json"

asset_url=$(
  python3 - "$release_json" "$os_name" "$arch_name" <<'PY'
import json
import sys

release_json, os_name, arch_name = sys.argv[1:4]
with open(release_json, "r", encoding="utf-8") as fh:
    data = json.load(fh)
for asset in data.get("assets", []):
    url = asset.get("browser_download_url", "")
    name = asset.get("name", "")
    if os_name in name and arch_name in name and name.endswith(".tar.gz"):
        print(url)
        raise SystemExit(0)
raise SystemExit("no matching Caddy release asset found")
PY
)

archive_path="${tmpdir}/caddy.tar.gz"
curl -fsSL "$asset_url" -o "$archive_path"
tar -xzf "$archive_path" -C "$tmpdir"

if [ ! -f "${tmpdir}/caddy" ]; then
  printf 'ERROR: Downloaded Caddy archive did not contain the expected binary.\n' >&2
  exit 1
fi

mv "${tmpdir}/caddy" "$CADDY_BINARY_PATH"
chmod +x "$CADDY_BINARY_PATH"
xattr -d com.apple.quarantine "$CADDY_BINARY_PATH" >/dev/null 2>&1 || true

printf 'Installed Caddy for Teely:\n'
printf '  %s\n' "$CADDY_BINARY_PATH"
