#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "$TEST_ROOT"' EXIT
MOCK_BIN="$TEST_ROOT/bin"
INSTALL_DIR="$TEST_ROOT/install"
STATE_FILE="$TEST_ROOT/current-version"
CALL_LOG="$TEST_ROOT/calls.log"
mkdir -p "$MOCK_BIN"

cat > "$MOCK_BIN/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$MOCK_CALL_LOG"
case "${1:-} ${2:-}" in
  "compose version"|"info "|"volume ls"|"pull "|"tag ") exit 0 ;;
  "image inspect")
    if [[ " $* " == *"org.opencontainers.image.revision"* ]]; then
      printf '0123456789abcdef0123456789abcdef01234567\n'
    else
      printf 'example@sha256:%064d\n' 0
    fi
    exit 0
    ;;
esac
if [[ " $* " == *" up "* ]]; then
  printf '%s\n' "${APP_VERSION:-}" > "$MOCK_STATE_FILE"
fi
if [[ " $* " == *" cp "* ]]; then
  destination="${!#}"
  printf '%s\n' '-- mock SQL backup' "$(printf '%0128d' 0)" > "$destination"
fi
exit 0
EOF

cat > "$MOCK_BIN/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
url=''
output=''
while (($#)); do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    http://*|https://*) url="$1"; shift ;;
    *) shift ;;
  esac
done
if [[ "$url" == http://127.0.0.1:*/* ]]; then
  printf '%s\n' "$url" >> "$MOCK_CALL_LOG"
  [[ "$(cat "$MOCK_STATE_FILE" 2>/dev/null || true)" != '1.0.2' ]]
  exit
fi
case "$url" in
  */compose.yaml) cp "$MOCK_SOURCE_ROOT/compose.yaml" "$output" ;;
  */compose.mysql.yaml) cp "$MOCK_SOURCE_ROOT/compose.mysql.yaml" "$output" ;;
  *) echo "unexpected URL: $url" >&2; exit 1 ;;
esac
EOF
chmod +x "$MOCK_BIN/docker" "$MOCK_BIN/curl"

cat > "$MOCK_BIN/flock" <<'EOF'
#!/usr/bin/env bash
# This single-process behavior test does not need cross-process locking.
exit 0
EOF
chmod +x "$MOCK_BIN/flock"

export PATH="$MOCK_BIN:$PATH"
export MOCK_CALL_LOG="$CALL_LOG"
export MOCK_STATE_FILE="$STATE_FILE"
export MOCK_SOURCE_ROOT="$ROOT"
export INSTALL_DIR
export APP_MODE=demo
export READY_TIMEOUT_SECONDS=1
export REPOSITORY_RAW_URL=https://example.test/repository
export MYSQL_PASSWORD=mock_password_123
export SESSION_ENCRYPTION_KEY=QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI

"$ROOT/scripts/bootstrap-server.sh" 1.0.1 >/dev/null
grep -qx 'APP_PORT=8097' "$INSTALL_DIR/.env"
grep -qx 'APP_MODE=demo' "$INSTALL_DIR/.env"
grep -qx 'APP_VERSION=1.0.1' "$INSTALL_DIR/.env"
grep -qx '1.0.1' "$INSTALL_DIR/.deployment-success"
grep -qx 'example@sha256:0000000000000000000000000000000000000000000000000000000000000000' "$INSTALL_DIR/.image-digest"
grep -qx '0123456789abcdef0123456789abcdef01234567' "$INSTALL_DIR/.source-commit"

sed -i 's/^APP_PORT=.*/APP_PORT=9007/' "$INSTALL_DIR/.env"
unset APP_MODE
"$ROOT/scripts/bootstrap-server.sh" 1.0.3 >/dev/null
grep -qx 'APP_VERSION=1.0.3' "$INSTALL_DIR/.env"
grep -q 'http://127.0.0.1:9007/readyz' "$CALL_LOG"
grep -qx '1.0.1' "$INSTALL_DIR/.rollback/version"
grep -qx 'example@sha256:0000000000000000000000000000000000000000000000000000000000000000' "$INSTALL_DIR/.rollback/image-digest"
grep -qx '0123456789abcdef0123456789abcdef01234567' "$INSTALL_DIR/.rollback/source-commit"

if "$ROOT/scripts/bootstrap-server.sh" 1.0.2 >/dev/null 2>&1; then
  echo 'unhealthy update unexpectedly succeeded' >&2
  exit 1
fi
grep -qx 'APP_VERSION=1.0.3' "$INSTALL_DIR/.env"
grep -qx '1.0.3' "$STATE_FILE"

LIVE_DIR="$TEST_ROOT/live"
mkdir -p "$LIVE_DIR"
cp "$ROOT/compose.yaml" "$LIVE_DIR/compose.yaml"
cp "$ROOT/compose.mysql.yaml" "$LIVE_DIR/compose.mysql.yaml"
cat > "$LIVE_DIR/.env" <<'EOF'
APP_MODE=live
AUTH_MODE=required
AUTH_COOKIE_SECURE=true
APP_PORT=8098
APP_VERSION=1.0.1
MYSQL_PASSWORD=mock_password_123
SESSION_ENCRYPTION_KEY=QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI
EOF
printf '1.0.1\n' > "$LIVE_DIR/.deployment-success"
INSTALL_DIR="$LIVE_DIR" \
EXPECTED_SOURCE_COMMIT=0123456789abcdef0123456789abcdef01234567 \
EXPECTED_IMAGE_DIGEST="sha256:$(printf '%064d' 0)" \
  "$ROOT/scripts/bootstrap-server.sh" 1.0.3 >/dev/null
backup_file="$(find "$LIVE_DIR/backups" -name 'visitready-before-1.0.3-*.sql' -print -quit)"
[[ -s "$backup_file" && -s "$backup_file.sha256" ]]

if "$ROOT/scripts/bootstrap-server.sh" 'not-a-version' >/dev/null 2>&1; then
  echo 'invalid version unexpectedly succeeded' >&2
  exit 1
fi

echo 'bootstrap-server behavior OK'
