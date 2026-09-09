#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <image-version>" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export INSTALL_DIR="${INSTALL_DIR:-$ROOT}"
export REGISTRY_IMAGE="${REGISTRY_IMAGE:-ghcr.io/asuka-20011204/visit-ready-agent}"
exec "$ROOT/scripts/bootstrap-server.sh" "$1"
