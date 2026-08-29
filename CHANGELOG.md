# Changelog

All notable changes to `cpa-codex-agent-identity` are documented here.
Published registry and release assets are updated only after the tagged release
workflow has produced and checksummed the artifacts.

## [Unreleased] - 0.3.7

### Changed

- Removed the optional card-specific Identity management shortcut from the
  Management Center overlay; plugin management is now exposed only through
  CPA's native plugin-pages ResourceRoute menu.
- Kept legacy `sidecar_url` and `sidecar_api_url` YAML parsing for upgrades,
  while hiding internal sidecar endpoints from fresh Plugin Store metadata.
- Added release-state validation so source version, plugin metadata, Makefile,
  registry version, and the published sidecar image cannot drift silently.

### Compatibility

- The current published registry remains `0.3.6` until the `v0.3.7` release
  assets are built, published, and checksummed.
- This release line does not claim to replace CPA's native Codex OAuth login
  implementation; native OAuth files remain owned by CPA.

## [0.3.6] - 2026-08-28

- Published CPA-native Codex Agent Identity and PAT routing, quota bridging,
  reset-credit support, and plugin-pages resource registration.
