#!/bin/sh
set -eu

usage() {
  cat <<'EOF'
Usage: sh deploy/bootstrap-runtime.sh [--start] [--sidecar-url URL]

Prepares a fresh CPA + Codex Agent Identity deployment in this repository.
The default sidecar URL is http://127.0.0.1:18787/agent-identity/ for a browser on the same host.
Use --sidecar-url /agent-identity/ when a reverse proxy exposes that path on the CPA origin.
EOF
}

start_stack=false
sidecar_url=${SIDECAR_UI_URL:-http://127.0.0.1:18787/agent-identity/}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --start)
      start_stack=true
      ;;
    --sidecar-url)
      shift
      [ "$#" -gt 0 ] || { echo "--sidecar-url requires a value" >&2; exit 2; }
      sidecar_url=$1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

case "$sidecar_url" in
  /*|http://*|https://*) ;;
  *)
    echo "sidecar URL must start with /, http://, or https://" >&2
    exit 2
    ;;
esac
case "$sidecar_url" in
  *'"'*|*'#'*|*'?'*)
    echo "sidecar URL must not contain quotes, query parameters, or fragments" >&2
    exit 2
    ;;
esac

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
project_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
runtime_root="$project_root/runtime"
env_file="$project_root/.env"
config_file="$project_root/config.yaml"
api_key_file="$runtime_root/secrets/cpa-api-key"

command -v openssl >/dev/null 2>&1 || { echo "openssl is required" >&2; exit 1; }

SIDECAR_UID=${SIDECAR_UID:-65532} SIDECAR_GID=${SIDECAR_GID:-65532} \
  sh "$script_dir/init-runtime.sh" "$runtime_root"
mkdir -p "$project_root/auths" "$project_root/logs"
chmod 700 "$project_root/auths" "$project_root/logs" "$runtime_root/cpa-plugins" 2>/dev/null || true

if [ ! -s "$api_key_file" ]; then
  umask 077
  printf 'cpak_%s\n' "$(openssl rand -hex 24)" > "$api_key_file"
fi
management_key=$(cat "$runtime_root/secrets/management-key")
api_key=$(cat "$api_key_file")

if [ ! -e "$env_file" ]; then
  cp "$project_root/.env.example" "$env_file"
  echo "Created $env_file"
else
  echo "Keeping existing $env_file"
fi
network_name=agent-identity
if [ -s "$env_file" ]; then
  configured_network=$(sed -n 's/^AGENT_IDENTITY_NETWORK[[:space:]]*=[[:space:]]*//p' "$env_file" | tail -n 1)
  if [ -n "$configured_network" ]; then
    network_name=$configured_network
  fi
fi

if [ ! -e "$config_file" ]; then
  umask 077
  cat > "$config_file" <<EOF
host: ""
port: 8317

remote-management:
  allow-remote: true
  secret-key: "$management_key"
  disable-control-panel: false

auth-dir: "~/.cli-proxy-api"
api-keys:
  - "$api_key"

logging-to-file: true
usage-statistics-enabled: true

plugins:
  enabled: true
  dir: "plugins"
  configs:
    codex-agent-identity:
      enabled: true
      priority: 1000
      sidecar_url: "$sidecar_url"
EOF
  chmod 600 "$config_file" 2>/dev/null || true
  echo "Created $config_file"
else
  echo "Keeping existing $config_file"
  cat <<EOF
Ensure its plugin section includes:

plugins:
  enabled: true
  dir: "plugins"
  configs:
    codex-agent-identity:
      enabled: true
      priority: 1000
      sidecar_url: "$sidecar_url"

CPA remote-management.secret-key must match:
  $runtime_root/secrets/management-key
EOF
fi

if command -v docker >/dev/null 2>&1; then
  if ! docker network inspect "$network_name" >/dev/null 2>&1; then
    docker network create "$network_name" >/dev/null
    echo "Created Docker network $network_name"
  fi
else
  echo "Docker is not installed; preparation completed without starting containers." >&2
  start_stack=false
fi

if [ "$start_stack" = true ]; then
  docker info >/dev/null 2>&1 || { echo "Docker daemon is not available" >&2; exit 1; }
  docker compose \
    --project-directory "$project_root" \
    --env-file "$env_file" \
    -f "$script_dir/docker-compose.production.yml" \
    up -d
fi

cat <<EOF

Bootstrap complete.

Next steps:
1. Start the stack (skip if --start was used):
   docker compose --project-directory "$project_root" --env-file "$env_file" -f "$script_dir/docker-compose.production.yml" up -d
2. Open http://127.0.0.1:8317/management.html#/plugin-store and install codex-agent-identity.
3. Open $sidecar_url and enter the same management key used by CPA.
4. After Plugin Store installation/upgrades, set CPA_PLUGIN_MOUNT_MODE=ro in $env_file and recreate CPA.

The generated CPA API key is stored at:
  $api_key_file
Do not publish config.yaml, .env, runtime/secrets, auths, or logs.
EOF