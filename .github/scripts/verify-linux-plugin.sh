#!/usr/bin/env bash
set -euo pipefail

library="${1:?usage: verify-linux-plugin.sh LIBRARY [GOARCH]}"
expected_arch="${2:-}"

if [[ ! -f "${library}" ]]; then
  echo "plugin library does not exist: ${library}" >&2
  exit 1
fi
command -v readelf >/dev/null || { echo "readelf is required" >&2; exit 1; }

header="$(readelf -h "${library}")"
if ! grep -q 'Type:.*DYN' <<<"${header}"; then
  echo "plugin is not an ELF shared object: ${library}" >&2
  exit 1
fi

if [[ -n "${expected_arch}" ]]; then
  case "${expected_arch}" in
    amd64) expected_machine='Advanced Micro Devices X86-64' ;;
    arm64) expected_machine='AArch64' ;;
    *) echo "unsupported expected architecture: ${expected_arch}" >&2; exit 2 ;;
  esac
  if ! grep -q "Machine:.*${expected_machine}" <<<"${header}"; then
    echo "plugin machine does not match ${expected_arch}: ${library}" >&2
    grep 'Machine:' <<<"${header}" >&2 || true
    exit 1
  fi
fi

symbols="$(readelf -Ws "${library}")"
for symbol in cliproxy_plugin_init cliproxyPluginCall cliproxyPluginFree cliproxyPluginShutdown; do
  if ! grep -Eq "[[:space:]]${symbol}$" <<<"${symbols}"; then
    echo "plugin is missing required exported symbol ${symbol}: ${library}" >&2
    exit 1
  fi
done

if ! readelf -d "${library}" | grep -q 'Shared library: \[libc.so.6\]'; then
  echo "plugin does not link against glibc libc.so.6: ${library}" >&2
  exit 1
fi

versions="$(readelf --version-info "${library}" 2>/dev/null | grep -oE 'GLIBC_[0-9]+(\.[0-9]+)+' | sort -u -V || true)"
while IFS= read -r symbol; do
  [[ -z "${symbol}" ]] && continue
  version="${symbol#GLIBC_}"
  major="${version%%.*}"
  minor="${version#*.}"
  minor="${minor%%.*}"
  if (( major > 2 || (major == 2 && minor > 17) )); then
    echo "plugin requires ${symbol}, above CPA's GLIBC 2.17 baseline: ${library}" >&2
    exit 1
  fi
done <<<"${versions}"

echo "verified Linux plugin: ${library} (GLIBC <= 2.17, required exports present)"
