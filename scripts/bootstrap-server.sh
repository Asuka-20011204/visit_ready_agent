#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: bootstrap-server.sh <version>

Environment options:
  APP_MODE=live|demo              Deployment mode (default: live on first install)
  APP_PORT=8097                   Loopback host port
  REGISTRY_IMAGE=...              Docker image without a tag
  INSTALL_DIR=/opt/visit-ready    Persistent deployment directory
  INSTALL_DOCKER=true             Install Docker on Ubuntu when missing
  EXPECTED_SOURCE_COMMIT=40-char SHA  Bind Compose files to the image source
  EXPECTED_IMAGE_DIGEST=sha256:..     Bind the image to immutable content

Digest and source checks are optional for local/demo use and mandatory when
APP_MODE=live with AUTH_COOKIE_SECURE=true (the public deployment profile).
EOF
}

[[ $# -eq 1 ]] || { usage >&2; exit 2; }
VERSION="${1#v}"
REF="v$VERSION"
if ! [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]; then
  echo "Version must be a semantic version such as 1.0.1." >&2
  exit 2
fi

REGISTRY_IMAGE="${REGISTRY_IMAGE:-docker.io/asuka20011204/visit-ready-agent}"
INSTALL_DIR="${INSTALL_DIR:-/opt/visit-ready}"
REQUESTED_PORT="${APP_PORT:-}"
REQUESTED_MODE="${APP_MODE:-}"
INSTALL_DOCKER="${INSTALL_DOCKER:-false}"
COOKIE_SECURE="${AUTH_COOKIE_SECURE:-true}"
EXPECTED_IMAGE_DIGEST="${EXPECTED_IMAGE_DIGEST:-}"
EXPECTED_SOURCE_COMMIT="${EXPECTED_SOURCE_COMMIT:-}"
READY_TIMEOUT_SECONDS="${READY_TIMEOUT_SECONDS:-45}"
REPOSITORY_RAW_URL="${REPOSITORY_RAW_URL:-https://raw.githubusercontent.com/Asuka-20011204/visit_ready_agent}"

[[ "$INSTALL_DOCKER" == true || "$INSTALL_DOCKER" == false ]] || { echo 'INSTALL_DOCKER must be true or false.' >&2; exit 2; }
[[ "$COOKIE_SECURE" == true || "$COOKIE_SECURE" == false ]] || { echo 'AUTH_COOKIE_SECURE must be true or false.' >&2; exit 2; }
[[ "$READY_TIMEOUT_SECONDS" =~ ^[1-9][0-9]{0,2}$ ]] || { echo 'READY_TIMEOUT_SECONDS must be an integer from 1 to 999.' >&2; exit 2; }
[[ "$REPOSITORY_RAW_URL" == https://* ]] || { echo 'REPOSITORY_RAW_URL must use HTTPS.' >&2; exit 2; }
if [[ -n "$EXPECTED_IMAGE_DIGEST" ]] && ! [[ "$EXPECTED_IMAGE_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo 'EXPECTED_IMAGE_DIGEST must be a sha256 digest.' >&2
  exit 2
fi
if [[ -n "$EXPECTED_SOURCE_COMMIT" ]]; then
  [[ "$EXPECTED_SOURCE_COMMIT" =~ ^[0-9a-f]{40}$ ]] || { echo 'EXPECTED_SOURCE_COMMIT must be a full 40-character Git SHA.' >&2; exit 2; }
  REF="$EXPECTED_SOURCE_COMMIT"
fi

install_docker() {
  [[ "$INSTALL_DOCKER" == true ]] || {
    echo 'Docker Engine and Compose v2 are required. On Ubuntu, re-run with INSTALL_DOCKER=true.' >&2
    exit 1
  }
  (( EUID == 0 )) || { echo 'Automatic Docker installation requires sudo/root.' >&2; exit 1; }
  [[ -r /etc/os-release ]] || { echo 'Cannot identify this operating system.' >&2; exit 1; }
  # shellcheck disable=SC1091
  . /etc/os-release
  [[ "${ID:-}" == ubuntu ]] || {
    echo "Automatic Docker installation supports Ubuntu, not ${ID:-unknown}." >&2
    exit 1
  }
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl docker.io
  DEBIAN_FRONTEND=noninteractive apt-get install -y docker-compose-v2 || \
    DEBIAN_FRONTEND=noninteractive apt-get install -y docker-compose-plugin
  systemctl enable --now docker
}

if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
  install_docker
fi
docker compose version >/dev/null 2>&1 || { echo 'Docker Compose v2 is required.' >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo 'Docker is not running or this user lacks permission.' >&2; exit 1; }
command -v flock >/dev/null 2>&1 || { echo 'flock is required (package: util-linux).' >&2; exit 1; }
command -v sha256sum >/dev/null 2>&1 || { echo 'sha256sum is required (package: coreutils).' >&2; exit 1; }

if command -v curl >/dev/null 2>&1; then
  fetch() {
    curl --proto '=https' --proto-redir '=https' --fail --silent --show-error --location \
      --retry 3 --connect-timeout 10 --max-time 60 "$1" -o "$2"
  }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget --https-only --timeout=60 --tries=3 -q -O "$2" "$1"; }
else
  echo 'curl or wget is required.' >&2
  exit 1
fi

generate_token() {
  local bytes="$1"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 "$bytes" | tr '+/' '-_' | tr -d '=\n'
  else
    head -c "$bytes" /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n'
  fi
}

read_env_value() {
  local name="$1" file="$2"
  sed -n "s/^${name}=//p" "$file" | tail -n 1
}

set_env_value() {
  local name="$1" value="$2" file="$3" tmp_file
  tmp_file="$(mktemp "${file}.XXXXXX")"
  awk -v name="$name" -v replacement="${name}=${value}" '
    BEGIN { found = 0 }
    index($0, name "=") == 1 { if (!found) print replacement; found = 1; next }
    { print }
    END { if (!found) print replacement }
  ' "$file" > "$tmp_file"
  chmod 600 "$tmp_file"
  mv "$tmp_file" "$file"
}

validate_env_value() {
  local name="$1" value="$2"
  case "$value" in *$'\n'*|*$'\r'*|*'$'*) echo "$name contains a character that Compose cannot safely load." >&2; exit 1;; esac
}

wait_ready() {
  local port="$1" deadline
  deadline=$(( $(date +%s) + READY_TIMEOUT_SECONDS ))
  until {
    if command -v curl >/dev/null 2>&1; then
      curl --fail --silent --show-error --connect-timeout 3 --max-time 5 "http://127.0.0.1:${port}/readyz" >/dev/null 2>&1
    else
      wget --spider --quiet --timeout=5 "http://127.0.0.1:${port}/readyz" >/dev/null 2>&1
    fi
  }; do
    (( $(date +%s) < deadline )) || return 1
    sleep 2
  done
}

mkdir -p "$INSTALL_DIR"
exec 9>"$INSTALL_DIR/.deploy.lock"
flock -n 9 || { echo 'Another deployment is already running.' >&2; exit 1; }

ENV_FILE="$INSTALL_DIR/.env"
if [[ -L "$ENV_FILE" ]]; then
  echo "$ENV_FILE must not be a symbolic link." >&2
  exit 1
fi
if [[ -e "$ENV_FILE" && ! -f "$ENV_FILE" ]]; then
  echo "$ENV_FILE must be a regular file." >&2
  exit 1
fi

project_name="$(basename "$INSTALL_DIR" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9_-]//g')"
existing_volumes="$(docker volume ls --quiet --filter "label=com.docker.compose.project=$project_name")"
if [[ ! -f "$ENV_FILE" && ( -f "$INSTALL_DIR/.deployment-success" || -n "$existing_volumes" ) ]]; then
  echo 'Existing deployment data was found but .env is missing. Restore .env and SESSION_ENCRYPTION_KEY; refusing to generate a new key.' >&2
  exit 1
fi

MODE="${REQUESTED_MODE:-}"
APP_PORT_VALUE="${REQUESTED_PORT:-}"
if [[ -f "$ENV_FILE" ]]; then
  chmod 600 "$ENV_FILE"
  stored_mode="$(read_env_value APP_MODE "$ENV_FILE")"
  stored_port="$(read_env_value APP_PORT "$ENV_FILE")"
  if [[ -n "$REQUESTED_MODE" && "$REQUESTED_MODE" != "$stored_mode" ]]; then
    echo 'Changing APP_MODE on an existing deployment is not automatic; use a separate INSTALL_DIR.' >&2
    exit 2
  fi
  if [[ -n "$REQUESTED_PORT" && "$REQUESTED_PORT" != "$stored_port" ]]; then
    echo 'Change APP_PORT in the existing .env before updating.' >&2
    exit 2
  fi
  MODE="$stored_mode"
  APP_PORT_VALUE="$stored_port"
  COOKIE_SECURE="$(read_env_value AUTH_COOKIE_SECURE "$ENV_FILE")"
fi
MODE="${MODE:-live}"
APP_PORT_VALUE="${APP_PORT_VALUE:-8097}"
[[ "$MODE" == live || "$MODE" == demo ]] || { echo 'APP_MODE must be live or demo.' >&2; exit 2; }
if ! [[ "$APP_PORT_VALUE" =~ ^[0-9]+$ ]] || (( APP_PORT_VALUE < 1 || APP_PORT_VALUE > 65535 )); then
  echo 'APP_PORT must be an integer between 1 and 65535.' >&2
  exit 2
fi
if [[ "$MODE" == live && "$COOKIE_SECURE" == true ]]; then
  [[ -n "$EXPECTED_SOURCE_COMMIT" && -n "$EXPECTED_IMAGE_DIGEST" ]] || {
    echo 'Public live deployment requires EXPECTED_SOURCE_COMMIT and EXPECTED_IMAGE_DIGEST from the release summary.' >&2
    exit 1
  }
fi

write_initial_env() {
  [[ -f "$ENV_FILE" ]] && return
  local llm_endpoint='' llm_api_key='' llm_model='' bocha_api_key=''
  local mysql_password encryption_key
  if [[ "$MODE" == live ]]; then
    llm_endpoint="${LLM_ENDPOINT:-}"
    llm_api_key="${LLM_API_KEY:-}"
    llm_model="${LLM_MODEL:-}"
    bocha_api_key="${BOCHA_API_KEY:-}"
    [[ -n "$llm_endpoint" ]] || read -r -p 'LLM_ENDPOINT (HTTPS Chat Completions URL): ' llm_endpoint
    [[ -n "$llm_api_key" ]] || { read -r -s -p 'LLM_API_KEY: ' llm_api_key; echo; }
    [[ -n "$llm_model" ]] || read -r -p 'LLM_MODEL: ' llm_model
    [[ "$llm_endpoint" == https://* && -n "$llm_api_key" && -n "$llm_model" ]] || {
      echo 'Live mode requires an HTTPS LLM_ENDPOINT, LLM_API_KEY and LLM_MODEL.' >&2
      exit 1
    }
  fi
  validate_env_value LLM_ENDPOINT "$llm_endpoint"
  validate_env_value LLM_API_KEY "$llm_api_key"
  validate_env_value LLM_MODEL "$llm_model"
  validate_env_value BOCHA_API_KEY "$bocha_api_key"
  mysql_password="${MYSQL_PASSWORD:-$(generate_token 24)}"
  encryption_key="${SESSION_ENCRYPTION_KEY:-$(generate_token 32)}"
  [[ "$mysql_password" =~ ^[A-Za-z0-9_-]{16,128}$ ]] || { echo 'MYSQL_PASSWORD must contain 16-128 letters, digits, underscores or hyphens.' >&2; exit 1; }
  [[ "$encryption_key" =~ ^[A-Za-z0-9_-]{43}$ ]] || { echo 'SESSION_ENCRYPTION_KEY must be a 32-byte Base64URL value.' >&2; exit 1; }
  umask 077
  {
    printf 'APP_MODE=%s\nAUTH_MODE=%s\nAUTH_COOKIE_SECURE=%s\n' "$MODE" "$([[ "$MODE" == live ]] && echo required || echo disabled)" "$([[ "$MODE" == live ]] && echo "$COOKIE_SECURE" || echo false)"
    printf 'APP_PORT=%s\nAPP_VERSION=%s\nAPP_COMMIT=bootstrap\n' "$APP_PORT_VALUE" "$VERSION"
    printf 'LLM_ENDPOINT=%s\nLLM_API_KEY=%s\nLLM_MODEL=%s\n' "$llm_endpoint" "$llm_api_key" "$llm_model"
    printf 'BOCHA_ENDPOINT=https://api.bochaai.com/v1/web-search\nBOCHA_API_KEY=%s\n' "$bocha_api_key"
    printf 'SESSION_STORE=%s\nMYSQL_DSN=\nMYSQL_PASSWORD=%s\nSESSION_ENCRYPTION_KEY=%s\n' "$([[ "$MODE" == live ]] && echo mysql || echo memory)" "$mysql_password" "$encryption_key"
    printf 'SESSION_TTL=24h\nSESSION_CLEANUP_INTERVAL=1m\nUPSTREAM_TIMEOUT=25s\nWORKFLOW_TIMEOUT=45s\n'
  } > "$ENV_FILE"
}

write_initial_env
if [[ -n "$EXPECTED_IMAGE_DIGEST" ]]; then
  remote_image="$REGISTRY_IMAGE@$EXPECTED_IMAGE_DIGEST"
else
  remote_image="$REGISTRY_IMAGE:$VERSION"
fi
docker pull "$remote_image"
image_commit="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$remote_image")"
[[ "$image_commit" =~ ^[0-9a-f]{40}$ ]] || { echo 'Image does not contain a valid source revision label.' >&2; exit 1; }
if [[ -n "$EXPECTED_SOURCE_COMMIT" && "$image_commit" != "$EXPECTED_SOURCE_COMMIT" ]]; then
  echo "Image revision $image_commit does not match expected source commit." >&2
  exit 1
fi
REF="$image_commit"
docker tag "$remote_image" "visit-ready-agent:$VERSION"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
fetch "$REPOSITORY_RAW_URL/$REF/compose.yaml" "$tmp_dir/compose.yaml"
fetch "$REPOSITORY_RAW_URL/$REF/compose.mysql.yaml" "$tmp_dir/compose.mysql.yaml"
docker image inspect --format '{{index .RepoDigests 0}}' "$remote_image" > "$tmp_dir/image-digest" || true

cd "$INSTALL_DIR"
previous_version="$(read_env_value APP_VERSION "$ENV_FILE")"
rollback_dir="$INSTALL_DIR/.rollback"
mkdir -p "$rollback_dir"
chmod 700 "$rollback_dir"
has_previous=false
if [[ -n "$previous_version" && -f compose.yaml ]]; then
  has_previous=true
  install -m 0644 compose.yaml "$rollback_dir/compose.yaml"
  [[ ! -f compose.mysql.yaml ]] || install -m 0644 compose.mysql.yaml "$rollback_dir/compose.mysql.yaml"
  printf '%s\n' "$previous_version" > "$rollback_dir/version"
  [[ ! -f .image-digest ]] || install -m 0600 .image-digest "$rollback_dir/image-digest"
  [[ ! -f .source-commit ]] || install -m 0600 .source-commit "$rollback_dir/source-commit"
fi

compose_files=(-f compose.yaml)
[[ "$MODE" != live ]] || compose_files+=(-f compose.mysql.yaml)
if [[ "$MODE" == live && "$has_previous" == true && -f compose.mysql.yaml ]]; then
  db_wait_timeout="$READY_TIMEOUT_SECONDS"
  if (( db_wait_timeout < 120 )); then
    db_wait_timeout=120
  fi
  docker compose "${compose_files[@]}" up -d --wait --wait-timeout "$db_wait_timeout" db
  mkdir -p "$INSTALL_DIR/backups"
  chmod 700 "$INSTALL_DIR/backups"
  backup_file="$INSTALL_DIR/backups/visitready-before-${VERSION}-$(date -u +%Y%m%dT%H%M%SZ).sql"
  container_backup=/tmp/visitready-pre-update.sql
  if ! docker compose "${compose_files[@]}" exec -T db sh -c \
    'MYSQL_PWD="$MYSQL_PASSWORD" mysqldump --single-transaction --skip-lock-tables -uvisitready visitready > /tmp/visitready-pre-update.sql' || \
    ! docker compose "${compose_files[@]}" cp "db:$container_backup" "$backup_file"; then
    rm -f "$backup_file" "$backup_file.sha256"
    echo 'Database backup failed; deployment was not changed.' >&2
    exit 1
  fi
  docker compose "${compose_files[@]}" exec -T db rm -f "$container_backup" || true
  if [[ ! -s "$backup_file" ]] || (( $(wc -c < "$backup_file") < 64 )); then
    rm -f "$backup_file" "$backup_file.sha256"
    echo 'Database backup is empty or incomplete; deployment was not changed.' >&2
    exit 1
  fi
  chmod 600 "$backup_file"
  sha256sum "$backup_file" > "$backup_file.sha256"
  chmod 600 "$backup_file.sha256"
fi

install -m 0644 "$tmp_dir/compose.yaml" compose.yaml
install -m 0644 "$tmp_dir/compose.mysql.yaml" compose.mysql.yaml

rollback_application() {
  [[ -s "$rollback_dir/version" && -f "$rollback_dir/compose.yaml" ]] || return 1
  local rollback_version
  rollback_version="$(cat "$rollback_dir/version")"
  install -m 0644 "$rollback_dir/compose.yaml" compose.yaml
  [[ ! -f "$rollback_dir/compose.mysql.yaml" ]] || install -m 0644 "$rollback_dir/compose.mysql.yaml" compose.mysql.yaml
  set_env_value APP_VERSION "$rollback_version" "$ENV_FILE"
  export APP_VERSION="$rollback_version"
  local local_image="visit-ready-agent:$rollback_version"
  local saved_digest='' saved_commit='' actual_commit='' actual_digests='' restore_image=false
  [[ ! -s "$rollback_dir/image-digest" ]] || saved_digest="$(cat "$rollback_dir/image-digest")"
  [[ ! -s "$rollback_dir/source-commit" ]] || saved_commit="$(cat "$rollback_dir/source-commit")"
  if docker image inspect "$local_image" >/dev/null 2>&1; then
    actual_commit="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$local_image")"
    actual_digests="$(docker image inspect --format '{{ range .RepoDigests }}{{ println . }}{{ end }}' "$local_image")"
    [[ -z "$saved_commit" || "$actual_commit" == "$saved_commit" ]] || restore_image=true
    [[ -z "$saved_digest" ]] || grep -Fqx -- "$saved_digest" <<< "$actual_digests" || restore_image=true
  else
    restore_image=true
  fi
  if [[ "$restore_image" == true ]]; then
    local rollback_image="${saved_digest:-$REGISTRY_IMAGE:$rollback_version}"
    docker pull "$rollback_image" || return 1
    if [[ -n "$saved_commit" ]]; then
      actual_commit="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$rollback_image")"
      [[ "$actual_commit" == "$saved_commit" ]] || return 1
    fi
    docker tag "$rollback_image" "$local_image" || return 1
  fi
  docker compose "${compose_files[@]}" up --no-build --force-recreate -d || return 1
  wait_ready "$APP_PORT_VALUE" || return 1
  echo "Application rollback to $rollback_version is healthy." >&2
}

export APP_VERSION="$VERSION"
if ! docker compose "${compose_files[@]}" config --quiet; then
  echo "Compose configuration for $VERSION is invalid." >&2
  rollback_application || echo 'Previous application configuration could not be restored automatically.' >&2
  exit 1
fi
if ! docker compose "${compose_files[@]}" up --no-build --force-recreate -d; then
  echo "Containers for $VERSION failed to start." >&2
  rollback_application || echo 'Automatic application rollback failed; inspect Docker logs and the database backup.' >&2
  exit 1
fi
if ! wait_ready "$APP_PORT_VALUE"; then
  docker compose "${compose_files[@]}" logs --tail 80 app >&2 || true
  echo "Version $VERSION did not become ready within ${READY_TIMEOUT_SECONDS} seconds." >&2
  rollback_application || echo 'Automatic application rollback failed; inspect Docker logs and the database backup.' >&2
  exit 1
fi

set_env_value APP_VERSION "$VERSION" "$ENV_FILE"
printf '%s\n' "$VERSION" > "$INSTALL_DIR/.deployment-success"
install -m 0600 "$tmp_dir/image-digest" "$INSTALL_DIR/.image-digest"
printf '%s\n' "$image_commit" > "$INSTALL_DIR/.source-commit"
chmod 600 "$INSTALL_DIR/.source-commit"
docker compose "${compose_files[@]}" ps
echo "Visit Ready $VERSION ($MODE) is ready at http://127.0.0.1:${APP_PORT_VALUE}"
