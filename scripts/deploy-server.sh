#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <image-version>" >&2
  exit 2
fi

VERSION="$1"
if ! [[ "$VERSION" =~ ^[0-9A-Za-z][0-9A-Za-z._-]{0,127}$ ]]; then
  echo "Invalid image version: $VERSION" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REGISTRY_IMAGE="${REGISTRY_IMAGE:-ghcr.io/asuka-20011204/visit-ready-agent}"
REMOTE_IMAGE="${REGISTRY_IMAGE}:${VERSION}"
LOCAL_IMAGE="visit-ready-agent:${VERSION}"

cd "$ROOT"
docker pull "$REMOTE_IMAGE"
docker tag "$REMOTE_IMAGE" "$LOCAL_IMAGE"
export APP_VERSION="$VERSION"
docker compose -f compose.yaml -f compose.mysql.yaml up --no-build --force-recreate -d
docker compose -f compose.yaml -f compose.mysql.yaml ps
curl --fail --silent --show-error http://127.0.0.1:"${APP_PORT:-8080}"/readyz
