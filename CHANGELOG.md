# Changelog

All notable changes to `cpa-codex-agent-identity` are documented here.
Published registry and release assets are updated only after the tagged release
workflow has produced and checksummed the artifacts.

## [0.3.18] - 2026-09-08

Consolidated native PAT and WebSocket repair, assigned after runtime acceptance
and explicit release approval. Do not replace existing tags or release assets.

### Fixed

- Import PATs as native file-backed `type: codex` credentials, with no sidecar base URL or `runtime_only` flag. CPA owns model execution, quota proxy routing, status/field persistence, headers, identity remapping and WebSockets. AgentAssertion credentials still require the sidecar and are not claimed to have full native parity.
- Preserve the user's explicit WebSocket setting on token synchronization, including `false`.
- Verify that WebSocket settings actually persist using CPA-native boolean semantics; reject missing, null or invalid fields instead of falsely reporting synchronization success. Cover token refresh, manager recreation and idempotent synchronization without resetting native controls.
- Keep canonical native PATs out of the plugin's sidecar parser and reject stale sidecar fields during synchronization verification.
- Stop updating the container `latest` alias at tag-build time. A separate registry/version/digest-checked promotion workflow moves it only after acceptance or a verified rollback.
- Stage GitHub releases as prereleases, without moving Latest before exact-asset runtime acceptance and deployment.

### Verified

- Exact v0.3.18 plugin/image on stock CPA v7.2.152: fresh native PAT import; off/on, disabled/proxy/note persistence through refresh and restart; authenticated proxy quota/WS, upstream connection reuse, and fail-closed model/quota/cold validation with a refused proxy.
- Production preserves all five PAT account/Team/proxy/status settings, leaves CPA/Keeper images unchanged, and passes complete default HTTP/WS streams plus native quota. See [release evidence](docs/releases/v0.3.18.md).

### Rollback

- Withdraw v0.3.17 from the recommended registry and restore the existing v0.3.15 assets/image. The native runtime regression hid editing controls and caused account proxies to be used for the private CPA-to-sidecar hop.
- Same-host isolated acceptance on stock CPA v7.2.152 verified file-backed controls, disabled/enabled persistence across restarts, saved proxy changes, failed model/quota requests with a refused proxy, native quota HTTP 200 and a complete PAT-backed luna stream. This does not make unreachable customer proxies usable or validate every AgentAssertion workflow.

## [0.3.17] - 2026-09-08

Withdrawn after production regression; retained only as immutable historical assets.

### Fixed

- Persist the plugin dispatch type while returning native `codex` runtime auths. CPA selects parsers by file type; the previous native file type bypassed sidecar routing and expiry normalization. Synchronization now verifies the dispatch type and migrates explicitly managed legacy files without touching ordinary OAuth files.
- Trust the deployed private Compose container alias `cpa-codex-agent-identity-sidecar` on port 8787, without expanding arbitrary plaintext host access.

## [0.3.16] - 2026-09-08

Pre-release only; production acceptance failed and registry publication was withheld.

### Fixed

- Exclude residual native OAuth `expired` metadata from explicitly sidecar-managed runtime credentials. Preserve canonical `expires_at`, original storage JSON, disabled state, proxy routing, and ordinary native OAuth handling.
- Reject stale `expired` aliases during CPA synchronization verification instead of reporting a canonical upload as successful. Regression tests cover cleanup and preservation of user routing settings.

## [0.3.15] - 2026-09-08

### Fixed

- Preserve quota `proxy_url` through the custom CPA-compatible JSON decoder. Regression covers snake-case, camel-case and hyphenated field names plus invalid types; v0.3.14 protected stored per-identity routing but still dropped explicit request proxy overrides.

## [0.3.14] - 2026-09-08

### Fixed

- Honor authenticated quota requests' `proxy_url` and resolve each managed identity's CPA proxy before quota, model, image, credential refresh and startup validation requests. Routing lookup failures stop outbound requests instead of assuming direct access.
- Keep unavailable global proxy configuration blocked through retry backoff, reject unsupported proxy schemes, and recheck the default route before new outbound requests.
- Preserve connection pooling for explicit credential proxies without falling back to direct on connection failures.

## [0.3.13] - 2026-09-02

### Fixed

- Persist sidecar-managed auth files under CPA's native `codex` provider namespace while retaining the explicit `auth_mode: agent_identity_sidecar` marker. This keeps CPA's plugin parser scoped to managed files and lets CPA-compatible consumers such as Keeper use their native Codex quota path instead of classifying the credentials as unknown.

## [0.3.12] - 2026-09-02

### Changed

- Build the CPA plugin against CLIProxyAPI/v7 v7.2.146 so the Store artifact carries
  the latest compatible auth lifecycle and Codex runtime fixes without claiming
  or replacing CPA's native OAuth provider.
- Publish registry.json and SIDECAR_IMAGE from the verified v0.3.12 release assets only
  after the tagged workflow completes successfully.

## [0.3.11] - 2026-09-02

### Changed

- Keep sidecar-managed credentials classified as CPA OAuth/file-backed Codex auth
  by delivering the `cais_` runtime key through CPA's standard static
  `header:Authorization` attribute instead of `api_key`.

### Fixed

- Allow CPA-native Codex Header Defaults, including the configured User-Agent
  fallback and WebSocket `x-codex-beta-features`, to apply to Agent Identity and
  PAT credentials exactly as they do to file-backed Codex OAuth credentials.
- Preserve CPA's per-auth `identity-confuse` remapping for `prompt_cache_key`,
  installation identity, window/session identifiers, and turn metadata while
  keeping the real upstream token available to stock Management API clients.
- Reject CR/LF in sidecar client keys, and permit cleartext sidecar bearer-key
  transport only to loopback endpoints or the fixed private Compose service
  aliases; user-configured sidecar hosts must use HTTPS.

## [0.3.10] - 2026-09-02

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
