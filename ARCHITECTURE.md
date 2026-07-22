# Architecture

The project extends an otherwise unmodified CLIProxyAPI (CPA) deployment. The
integration is split into three independently replaceable parts:

1. **CPA plugin control plane**: registers the Codex AuthProvider and an Agent
   Identity Management API resource in CPAMC. It recognizes only sidecar-owned
   Codex auth files, supplies their internal base URL and opaque cais_ key to
   CPA's stock Codex executor, and embeds the sidecar dashboard. It never
   receives an original Agent Identity JWT or Personal Access Token.
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

The plugin targets CPA dynamic plugin ABI v1 and is compiled against the latest
verified CPA SDK baseline. Release v0.3.0 uses CLIProxyAPI v7.2.94. The CPA
image remains an environment variable and is never rebuilt or forked here.

A CPA upgrade should follow this sequence:

1. Pull the candidate official CPA image without replacing production.
2. Start it on isolated canary ports with independent config, auth, log, data,
   and plugin paths.
3. Load the released plugin for the candidate architecture.
4. Verify registration, CPAMC resource embedding, import preview, auth-file
   synchronization, HTTP, SSE, WebSocket, image, quota, reset-credit, and proxy
   hot reload behavior.
5. Pin the verified image digest and replace production only after the canary
   passes.

The sidecar image, plugin directory, encryption key, and encrypted data volume
are host-mounted independently from the CPA image. This lets 1Panel recreate or
upgrade the official CPA container without erasing plugin or credential state.
The plugin mount is read-only by default. Temporarily use a writable mount only
when intentionally installing or updating through CPA Plugin Store.

CODEX_AGENT_IDENTITY_SIDECAR_HOSTS is an explicit plugin-side hostname
allowlist. It avoids a dependency on one Docker service name without turning
the sidecar base URL into an arbitrary request destination.

## Trust boundaries

~~~text
browser -> TLS reverse proxy -> plugin resource -> sidecar dashboard
                                      |                 |
                                      |                 +-> authenticated management API
                                      |                           |
                                      |                           +-> CPA auth-file API
                                      |
client  -> CPA stock executor -> sidecar data plane -> fixed OpenAI origins
                                      |
                                      +-> encrypted owner-only identity store
~~~

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
