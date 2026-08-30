#!/usr/bin/env bash
set -euo pipefail

export PATH="/opt/python/cp311-cp311/bin:${PATH}"
command -v gcc >/dev/null
command -v curl >/dev/null
command -v tar >/dev/null
command -v awk >/dev/null
command -v sha256sum >/dev/null

rm -rf /tmp/go /tmp/go.tar.gz
curl --fail --silent --show-error --location \
  --output /tmp/go.tar.gz \
  "https://go.dev/dl/go${GO_VERSION}.linux-${PLUGIN_GOARCH}.tar.gz"
printf '%s  %s\n' "${GO_CHECKSUM}" /tmp/go.tar.gz | sha256sum --check --status -
tar -xzf /tmp/go.tar.gz -C /tmp

export PATH="/tmp/go/bin:${PATH}"
export GOTOOLCHAIN=local
export CGO_ENABLED=1
go version
mkdir -p "$(dirname "${OUTPUT}")"
(
  cd /src/plugin/codex-agent-identity
  GOOS=linux GOARCH="${PLUGIN_GOARCH}" \
    go build -trimpath -buildvcs=false \
      -buildmode=c-shared \
      -ldflags "-s -w -X main.pluginVersion=${PLUGIN_VERSION}" \
      -o "${OUTPUT}" .
)
rm -f "${OUTPUT%.so}.h"