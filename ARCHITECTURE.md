# Architecture

The project extends an otherwise unmodified CLIProxyAPI (CPA) deployment. The
integration is split into three independently replaceable parts:

1. **CPA plugin control plane**: registers an AuthProvider under the private
   `codex-agent-identity` provider key and one authenticated Management API route.
   It recognizes only sidecar-owned auth files marked `auth_mode=agent_identity_sidecar`,
   preserves the upstream credential as native `access_token` metadata, maps the
   separate opaque `sidecar_client_key` through CPA's static
   `header:Authorization` attribute without setting `api_key`, and returns
   `AuthData.Provider=codex` so those records use CPA's first-class Codex executor.
   Native `type=codex` OAuth files keep CPA's built-in parser, login, refresh, and
   executor path; the plugin never claims the native `codex` AuthProvider. It exposes
   the authenticated Management wrapper plus one safe resource wrapper for CPAMC
   plugin pages and never returns either credential through those wrappers.
2. **Sidecar management plane**: validates single or batch imports, stores
   credentials encrypted, and transactionally adds, disables, refreshes, or
   removes native Codex auth files through CPA's management API.
3. **Sidecar data plane**: maps a cais_ key to one encrypted credential,
   creates AgentAssertion for Agent Identity JWTs or uses a verified opaque
   Personal Access Token, and forwards the request to fixed OpenAI origins.

The first public release deliberately keeps the mature data plane in the
sidecar. Rewriting AgentAssertion, PAT validation, images, quota/reset-credit,
SSE, WebSocket, and proxy hot reload inside an in-process plugin would add risk
without improving CPA management integration. A future Executor capability can
replace the data plane without changing the encrypted store format.

The quota compatibility module accepts only the exact supported ChatGPT paths
and methods. The reset-credit consume route is preserved for CPA compatibility,
but startup, reconciliation, health checks, and deployment probes never call
it. Tests exercise it only through a local httptest upstream.

## Upgrade boundary

The plugin targets CPA dynamic plugin ABI v1 and is compiled with Go 1.26.6 or
later against the current verified source baseline, CLIProxyAPI v7.2.145.
The current source line is v0.3.11 and is built against CLIProxyAPI v7.2.145;
the published registry and directly installable assets remain v0.3.10 until the
v0.3.11 GitHub Release archives have been built and checksummed. The CPA image remains an
environment variable and is never rebuilt or forked here.

A CPA upgrade should follow this sequence:

1. Pull the candidate official CPA image without replacing production.
2. Start it on isolated canary ports with independent config, auth, log, data,
   and plugin paths.
3. Load the released plugin for the candidate architecture.
4. Verify registration, plugin-page resource availability, Management-key
   enforcement, the direct sidecar dashboard, import preview, auth-file
   synchronization, HTTP, SSE, WebSocket, image, quota, reset-credit, and proxy
   hot reload behavior.
5. Pin the verified image digest and replace production only after the canary
   passes.

The sidecar image, plugin directory, encryption key, and encrypted data volume
are host-mounted independently from the CPA image. This lets 1Panel recreate or
upgrade the official CPA container without erasing plugin or credential state.
The plugin mount is read-only by default. Temporarily use a writable mount only
when intentionally installing or updating through CPA Plugin Store.

When `sidecar_api_url` is blank, the plugin uses `CODEX_AGENT_IDENTITY_SIDECAR_API_URL`
when provided; otherwise it derives an internal HTTP endpoint from the first
`CODEX_AGENT_IDENTITY_SIDECAR_HOSTS` entry and its indexed
`CODEX_AGENT_IDENTITY_SIDECAR_HTTP_PORTS` value (default `8787`).

CODEX_AGENT_IDENTITY_SIDECAR_HOSTS is an explicit plugin-side hostname
allowlist. It avoids a dependency on one Docker service name without turning
the sidecar base URL into an arbitrary request destination. The parser also
trusts loopback (`localhost`, `127.0.0.1`, and `::1`) for standalone and Plugin
Store deployments. Plain HTTP stays restricted to port 8787, plus port 18787
for loopback; additional intentional ports require
CODEX_AGENT_IDENTITY_SIDECAR_HTTP_PORTS.

The direct image compatibility bridge accepts both JSON and multipart edits,
honors `response_format`, translates partial/completed image streams, and uses
the same assertion-aware 401 retry transport as normal Codex requests. This
keeps CPA's native image routes working when CPA selects a direct image model.

## Trust boundaries

~~~text
browser -> TLS reverse proxy -> sidecar dashboard -> authenticated identity API
                                                   |
                                                   +-> CPA auth-file API

Management-key client -> CPA /v0/management/codex-agent-identity/open
                              |
                              +-> authenticated HTML wrapper

CPAMC plugin page -> CPA /v0/resource/plugins/codex-agent-identity/open
                              |
                              +-> same wrapper; no secret in resource response

client -> CPA stock executor -> sidecar data plane -> fixed OpenAI origins
                                     |
                                     +-> encrypted owner-only identity store
~~~

CPA intentionally leaves `/v0/resource/plugins/...` outside Management-key
authentication because CPAMC loads these resources inside an iframe. The plugin
therefore registers a resource wrapper that contains no hard-coded Management key,
original credential, or privileged host callback. It reuses CPAMC's scoped
obfuscated auth state only through a source-, origin-, and nonce-checked
`postMessage`; the key is never added to the iframe URL or persisted by the
wrapper. The resource route embeds the sidecar dashboard; listing, previewing,
importing, enabling, disabling, refreshing, and deleting
identities still require the sidecar's own Bearer management password.

During registration, the plugin echoes the CPA host's requested schema version,
clamped to the SDK maximum. This keeps the dynamic library loadable by older CPA
builds that negotiate schema v1 while retaining the current contract when available.

CPA plugins are trusted in-process code. Anyone who can replace the .so can
execute with CPA's privileges, so archives are checksummed and the plugin
directory should be read-only during normal operation.

The host root account and Docker daemon remain trusted. Encryption at rest
protects copied volumes, backups, and accidental file disclosure; it cannot
protect against a fully compromised host that can read both the key and the
running process.

The encrypted store retains its historical AAD namespace for backward
compatibility. That string is not a live project identifier and must not be
renamed without an explicit, tested data migration.
