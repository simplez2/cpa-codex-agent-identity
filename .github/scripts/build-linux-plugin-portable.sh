#!/usr/bin/env bash
set -euo pipefail

# Build a Linux c-shared plugin against the CPA-compatible GLIBC 2.17 baseline.
# The manylinux2014 images are based on CentOS 7 and intentionally keep the
# host ABI old enough for stock CPA Linux releases.

version="${1:?usage: build-linux-plugin-portable.sh VERSION GOARCH OUTPUT}"
goarch="${2:?usage: build-linux-plugin-portable.sh VERSION GOARCH OUTPUT}"
output="${3:?usage: build-linux-plugin-portable.sh VERSION GOARCH OUTPUT}"

case "${goarch}" in
  amd64)
    image="quay.io/pypa/manylinux2014_x86_64"
    platform="linux/amd64"
    ;;
  arm64)
    image="quay.io/pypa/manylinux2014_aarch64"
    platform="linux/arm64"
    ;;
  *)
    echo "unsupported Linux plugin architecture: ${goarch}" >&2
    exit 2
    ;;
esac

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if [[ "${output}" = /* ]]; then
  output_abs="${output}"
else
  output_abs="${PWD}/${output}"
fi
mkdir -p "$(dirname "${output_abs}")"
output_abs="$(cd "$(dirname "${output_abs}")" && pwd)/$(basename "${output_abs}")"
case "${output_abs}" in
  "${repo_root}"/*) ;;
  *)
    echo "plugin output must stay inside the repository: ${output_abs}" >&2
    exit 1
    ;;
esac
go_version="$(awk '$1 == "go" { print $2; exit }' "${repo_root}/go.mod")"
if [[ -z "${go_version}" ]]; then
  echo "could not resolve Go version from go.mod" >&2
  exit 1
fi
case "${go_version}:${goarch}" in
  1.26.6:amd64) go_checksum="708effb774be8237570d0add163225abbdfaf4fca28b2611df167beba4feef89" ;;
  1.26.6:arm64) go_checksum="d0507e9e9d7fe012aae570108cbd76c15de879e17130ab8cb90d4d7445cb1f2e" ;;
  *)
    echo "Go ${go_version} ${goarch} is not pinned in build-linux-plugin-portable.sh" >&2
    exit 1
    ;;
esac

# Clear the manylinux wrapper entrypoint and invoke Bash explicitly.
docker run --rm \
  --platform="${platform}" \
  --entrypoint="" \
  -e "PLUGIN_VERSION=${version}" \
  -e "PLUGIN_GOARCH=${goarch}" \
  -e "GO_VERSION=${go_version}" \
  -e "GO_CHECKSUM=${go_checksum}" \
  -e "OUTPUT=/src/$(realpath --relative-to="${repo_root}" "${output_abs}")" \
  -v "${repo_root}:/src" \
  -w /src \
  "${image}" \
  /bin/bash -c '
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
    (cd plugin/codex-agent-identity && \
      GOOS=linux GOARCH="${PLUGIN_GOARCH}" \
      go build -trimpath -buildvcs=false \
        -buildmode=c-shared \
        -ldflags "-s -w -X main.pluginVersion=${PLUGIN_VERSION}" \
        -o "${OUTPUT}" .)
    rm -f "${OUTPUT%.so}.h"
  '
