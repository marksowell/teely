#!/bin/sh

set -eu

SCRIPT_DIR=$(
  CDPATH= cd -- "$(dirname -- "$0")" && pwd
)
PROJECT_ROOT=$(
  CDPATH= cd -- "$SCRIPT_DIR/.." && pwd
)
CONFIG_PATH="${1:-${PROJECT_ROOT}/teely.local.json}"

python3 - "$CONFIG_PATH" <<'PY'
import json
import os
import sys

config_path = os.path.abspath(sys.argv[1])
base_dir = os.path.dirname(config_path)
with open(config_path, "r", encoding="utf-8") as fh:
    cfg = json.load(fh)

runtime_dir = cfg.get("runtime_dir") or ".teely"
if not os.path.isabs(runtime_dir):
    runtime_dir = os.path.normpath(os.path.join(base_dir, runtime_dir))

caddy = cfg.get("caddy") or {}
binary_path = caddy.get("binary_path") or os.path.join(runtime_dir, "bin", "caddy")
caddyfile_path = caddy.get("caddyfile_path") or os.path.join(runtime_dir, "caddy", "Caddyfile")
if not os.path.isabs(binary_path):
    binary_path = os.path.normpath(os.path.join(base_dir, binary_path))
if not os.path.isabs(caddyfile_path):
    caddyfile_path = os.path.normpath(os.path.join(base_dir, caddyfile_path))

print(f'RUNTIME_DIR="{runtime_dir}"')
print(f'CADDY_BINARY_PATH="{binary_path}"')
print(f'CADDYFILE_PATH="{caddyfile_path}"')
PY
