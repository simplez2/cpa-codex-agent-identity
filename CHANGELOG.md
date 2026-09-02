# Changelog

All notable changes to `cpa-codex-agent-identity` are documented here.
Published registry and release assets are updated only after the tagged release
workflow has produced and checksummed the artifacts.

## [Unreleased] - 0.3.10

### Added

- Add an end-to-end regression for the exact Keeper request path through stock
  CPA `/v0/management/api-call`, including Team-scoped PAT headers and upstream
  token substitution.
- Add multi-Team regression coverage proving that one login/token can retain
  distinct CPA auth files, auth indexes, account IDs, and sidecar runtime keys.

### Changed

- Split sidecar-managed CPA credentials into native `access_token` metadata for
  stock Management API clients and a separate `sidecar_client_key` mapped to
  the Codex executor's `api_key` attribute for model traffic through the
  sidecar.
- Fail closed when an auth-file upsert is missing the upstream credential;
  startup reconciliation upgrades existing encrypted identities to the new
  dual-field format without requiring token re-import.

### Fixed

- Prevent Keeper quota and reset-credit requests from substituting a `cais_`
  sidecar key into `Authorization: Bearer $TOKEN$`, which caused ChatGPT to
  return `401 Could not parse your authentication token`.

## [0.3.9] - 2026-09-02

### Added

- Add an authenticated diagnostics endpoint and CPA synchronization status so
  deployments can distinguish sidecar reachability, key delivery, and auth-file
  persistence failures without exposing credentials.
- Add a CPA-compatible quota and reset-credit bridge that resolves runtime-only
  sidecar auth files back to the original Agent Identity or PAT.

### Changed

- Keep `codex-agent-identity` as the private auth-file parser while returning
  `Provider: codex`, so Agent Identity and PAT traffic uses CPA's native Codex
  executor without intercepting ordinary CPA OAuth files.
- Treat the same account imported into different Team workspaces as distinct by
  including `account_id` in identity IDs and CPA auth-file names.

### Fixed

- Read the current CPAMC Management key from its scoped encrypted storage
  (`selection -> scope -> state.managementKey`) and pass it only through a
  nonce-bound same-origin `postMessage`; retain legacy storage fallback without
  putting the key in an iframe URL or persistent sidecar storage.
- Make CPA auth-file create, update, delete, migration, rollback, and final
  persistence checks tolerate the host's eventual consistency while failing
  closed when a sidecar auth index cannot be resolved.
- Prevent `cais_` runtime client keys from reaching CPA's ChatGPT JWT parser,
  which previously caused quota requests to fail with `Could not parse your
  authentication token`.

## [0.3.8] - 2026-08-30

### Fixed

- Reserve a distinct `-agent-identity` suffix for sidecar-managed Codex auth
  files so a PAT can coexist with CPA native OAuth for the same email and Team
  workspace instead of failing on an unmanaged filename collision.
- Build Linux `.so` release artifacts inside manylinux2014 (GLIBC 2.17) images,
  matching the baseline used by CPA's dynamic-plugin Linux releases.
- Fail the release before publishing when a plugin requests a newer GLIBC symbol
  or is missing any required CPA plugin ABI export.
- Document the distinction between the project registry and the public CPA
  Plugin Store registry, plus the exact "configured but not registered" recovery
  path.
- Use the same-origin `/agent-identity/` route by default in the Plugin Store
  management wrapper, while retaining explicit localhost and custom sidecar URLs
  for legacy deployments. This fixes remote CPA pages that previously stayed on
  `Connecting to Codex Agent Identity...`.

## [0.3.7] - 2026-08-29

### Changed

- Removed the optional card-specific Identity management shortcut from the
  Management Center overlay; plugin management is now exposed only through
  CPA's native plugin-pages ResourceRoute menu.
- Kept legacy `sidecar_url` and `sidecar_api_url` YAML parsing for upgrades,
  while hiding internal sidecar endpoints from fresh Plugin Store metadata.
- Added release-state validation so source version, plugin metadata, Makefile,
  registry version, and the published sidecar image cannot drift silently.

### Compatibility

- Published the v0.3.7 Linux plugin archives and checksums, then updated the
  registry metadata to the verified archive sizes and SHA-256 values.
- This release line does not claim to replace CPA's native Codex OAuth login
  implementation; native OAuth files remain owned by CPA.

## [0.3.6] - 2026-08-28

- Published CPA-native Codex Agent Identity and PAT routing, quota bridging,
  reset-credit support, and plugin-pages resource registration.
