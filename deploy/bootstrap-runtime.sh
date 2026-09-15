#!/bin/sh
set -eu

usage() {
  cat <<'EOF'
Usage: sh deploy/bootstrap-runtime.sh [--start] [--sidecar-url URL]

Prepares a fresh CPA + Codex Agent Identity deployment in this repository.
The native plugin page is embedded in the .so and does not need a browser-facing
sidecar URL. Use --sidecar-url only to keep a direct dashboard fallback, for
example /agent-identity/ behind the CPA origin or a host-local loopback URL.
This helper initializes only SIDECAR_ROOT=./runtime. Initialize any custom root
with init-runtime.sh and start Compose manually using that exact path.
EOF
}

path_exists() {
  [ -e "$1" ] || [ -L "$1" ]
}

invalid_sidecar_url() {
  echo "sidecar URL is invalid; use an unencoded absolute path or HTTP(S) URL without credentials" >&2
  exit 2
}

start_stack=false
sidecar_url=${SIDECAR_UI_URL:-}
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
      echo "unknown option" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

if [ -n "$sidecar_url" ]; then
  case "$sidecar_url" in
    *'
'*) invalid_sidecar_url ;;
  esac
  if LC_ALL=C printf '%s' "$sidecar_url" | LC_ALL=C grep -q '[[:space:][:cntrl:]]'; then
    invalid_sidecar_url
  fi
  case "$sidecar_url" in
    *\\*|*'"'*|*'#'*|*'?'*|*'%'*) invalid_sidecar_url ;;
  esac
  case "$sidecar_url" in
    //*) invalid_sidecar_url ;;
    /*) url_path=$sidecar_url ;;
    http://*|https://*)
      authority=${sidecar_url#*://}
      url_path=${authority#*/}
      if [ "$url_path" = "$authority" ]; then url_path=""; else url_path="/$url_path"; fi
      authority=${authority%%/*}
      case "$authority" in
        ""|*'@'*) invalid_sidecar_url ;;
      esac
      ;;
    *) invalid_sidecar_url ;;
  esac
  case "$url_path" in
    *//*|*/../*|*/..|*/./*|*/.) invalid_sidecar_url ;;
  esac
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
project_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
runtime_root="$project_root/runtime"
env_file="$project_root/.env"
config_file="$project_root/config.yaml"
api_key_file="$runtime_root/secrets/cpa-api-key"

if [ "$start_stack" = true ] && { path_exists "$env_file" || path_exists "$config_file"; }; then
  echo "refusing --start for an existing deployment; review it and start it explicitly" >&2
  exit 3
fi

case "${SIDECAR_ROOT:-./runtime}" in
  ./runtime) ;;
  *)
    echo "bootstrap supports only SIDECAR_ROOT=./runtime; initialize a custom root and start Compose manually" >&2
    exit 3
    ;;
esac

network_name=agent-identity
if path_exists "$env_file"; then
  env_source=$env_file
  env_description="existing .env"
else
  env_source="$project_root/.env.example"
  env_description=".env.example"
fi
[ -f "$env_source" ] && [ -r "$env_source" ] && [ ! -L "$env_source" ] || {
  echo "$env_description must be a readable regular file" >&2
  exit 3
}
configured_sidecar_root=$(
  sed -n 's/^[[:space:]]*\(export[[:space:]][[:space:]]*\)\{0,1\}SIDECAR_ROOT[[:space:]]*=[[:space:]]*//p' "$env_source" |
    tr -d '\r' |
    tail -n 1
)
case "$configured_sidecar_root" in
  ""|./runtime) ;;
  *)
    echo "bootstrap supports only SIDECAR_ROOT=./runtime; initialize a custom root and start Compose manually" >&2
    exit 3
    ;;
esac
configured_network=$(
  sed -n 's/^[[:space:]]*\(export[[:space:]][[:space:]]*\)\{0,1\}AGENT_IDENTITY_NETWORK[[:space:]]*=[[:space:]]*//p' "$env_source" |
    tr -d '\r' |
    tail -n 1
)
if [ -n "$configured_network" ]; then
  network_name=$configured_network
fi
network_name=${AGENT_IDENTITY_NETWORK:-$network_name}
case "$network_name" in
  ""|-*|*[!A-Za-z0-9_.-]*)
    echo "AGENT_IDENTITY_NETWORK must be a simple Docker network name" >&2
    exit 3
    ;;
esac

for directory in \
  "$runtime_root" \
  "$runtime_root/data-v3" \
  "$runtime_root/secrets" \
  "$runtime_root/cpa-plugins" \
  "$project_root/auths" \
  "$project_root/logs"
do
  if [ -L "$directory" ] || { path_exists "$directory" && [ ! -d "$directory" ]; }; then
    echo "refusing a symlink or non-directory deployment path" >&2
    exit 3
  fi
done

if path_exists "$config_file" && { [ ! -f "$config_file" ] || [ -L "$config_file" ]; }; then
  echo "existing config.yaml must be a regular file" >&2
  exit 3
fi

for secret_file in \
  "$runtime_root/secrets/data-encryption-key" \
  "$runtime_root/secrets/management-key" \
  "$api_key_file"
do
  if path_exists "$secret_file" && { [ ! -f "$secret_file" ] || [ -L "$secret_file" ] || [ ! -s "$secret_file" ]; }; then
    echo "refusing an unsafe or empty existing secret file" >&2
    exit 3
  fi
done

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
EOF
  if [ -n "$sidecar_url" ]; then
    printf '      sidecar_url: "%s"\n' "$sidecar_url" >> "$config_file"
  fi
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

CPA remote-management.secret-key must match:
  $runtime_root/secrets/management-key
EOF
  if [ -n "$sidecar_url" ]; then
    cat <<EOF
Optional direct-dashboard fallback:
      add the validated --sidecar-url value as the quoted sidecar_url setting
EOF
  else
    echo "Leave sidecar_url unset; the embedded plugin page uses the private sidecar route."
  fi
fi

if command -v docker >/dev/null 2>&1; then
  if ! docker network inspect "$network_name" >/dev/null 2>&1; then
    docker network create "$network_name" >/dev/null
    echo "Created Docker network."
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
3. Open plugin-pages -> Codex Agent Identity. The embedded page reuses the same management key used by CPA.
4. After Plugin Store installation/upgrades, set CPA_PLUGIN_MOUNT_MODE=ro in $env_file and recreate CPA.

The generated CPA API key is stored at:
  $api_key_file
Do not publish config.yaml, .env, runtime/secrets, auths, or logs.
EOF
if [ -n "$sidecar_url" ]; then
  echo "Optional direct-dashboard fallback configured."
fi
