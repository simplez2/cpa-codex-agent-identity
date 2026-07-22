#!/bin/sh
set -eu

runtime_root=${1:-./runtime}
sidecar_uid=${SIDECAR_UID:-65532}
sidecar_gid=${SIDECAR_GID:-65532}

case "$runtime_root" in
  ""|/)
    echo "refusing unsafe runtime root: $runtime_root" >&2
    exit 1
    ;;
esac

umask 077
mkdir -p "$runtime_root/data-v3" "$runtime_root/secrets" "$runtime_root/cpa-plugins"

if [ ! -s "$runtime_root/secrets/data-encryption-key" ]; then
  openssl rand -hex 32 > "$runtime_root/secrets/data-encryption-key"
fi
if [ ! -s "$runtime_root/secrets/management-key" ]; then
  openssl rand -base64 36 > "$runtime_root/secrets/management-key"
fi

chmod 700 "$runtime_root/data-v3" "$runtime_root/secrets"
chmod 600 "$runtime_root/secrets/data-encryption-key" "$runtime_root/secrets/management-key"
chown -R "$sidecar_uid:$sidecar_gid" "$runtime_root/data-v3" "$runtime_root/secrets"

echo "Initialized $runtime_root for sidecar uid:gid $sidecar_uid:$sidecar_gid"
echo "Use the generated management-key value as CPA remote-management.secret-key."
