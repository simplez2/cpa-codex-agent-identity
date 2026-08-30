# Changelog

All notable changes to `cpa-codex-agent-identity` are documented here.
Published registry and release assets are updated only after the tagged release
workflow has produced and checksummed the artifacts.

## [Unreleased] - 0.3.8

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
