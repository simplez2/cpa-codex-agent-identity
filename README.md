<div align="center">
  <img src="assets/logo.svg" width="96" alt="CPA Codex Agent Identity logo">
  <h1>CPA Codex Agent Identity</h1>
  <p><strong>Bring Codex PATs into CPA’s native workflow. Keep control of your accounts, proxies and upgrades.</strong></p>
  <p>
    <a href="https://github.com/simplez2/cpa-codex-agent-identity/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/simplez2/cpa-codex-agent-identity/ci.yml?branch=main&amp;style=flat-square&amp;label=CI"></a>
    <a href="https://github.com/simplez2/cpa-codex-agent-identity/releases"><img alt="Release" src="https://img.shields.io/github/v/release/simplez2/cpa-codex-agent-identity?style=flat-square"></a>
    <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/license-MIT-111827?style=flat-square"></a>
    <img alt="CPA ABI" src="https://img.shields.io/badge/CPA-plugin%20ABI%20v1-374151?style=flat-square">
    <img alt="Encryption" src="https://img.shields.io/badge/store-AES--256--GCM-0f766e?style=flat-square">
  </p>
  <p>English · <a href="README.zh-CN.md">简体中文</a></p>
</div>

> **Recommended: [v0.3.18](https://github.com/simplez2/cpa-codex-agent-identity/releases/tag/v0.3.18)** · Linux amd64 / arm64 · [Verified scope](docs/releases/v0.3.18.md) · [Known limitations](docs/compatibility.md)
>
> The Plugin Store installs the plugin, **not** the required sidecar. PATs use
> CPA's native execution path; Agent Identity JWTs still need a bridge. This is
> an independent project, not an official OpenAI or CPA product.

## Your credentials. CPA's native controls.

Import and manage your own Codex PATs and Agent Identity credentials without
replacing your CPA image or taking over its existing OAuth accounts.

| What you need | What this project provides |
| --- | --- |
| Familiar account controls | File-backed PATs keep CPA's enable/disable, proxy, note, priority and WebSocket settings through synchronization. |
| Safer bulk import | Preview TXT/JSON/JSONL, distinguish Team-scoped identities, validate first and use atomic rollback on failure. |
| Predictable networking | Use the current credential proxy; blocked routing fails closed instead of silently choosing direct access. |
| A manageable deployment | Native plugin-pages entry, separate sidecar/storage, pinned artifacts and documented rollback boundaries. |

**[Get started](docs/getting-started.md)** · **[简体中文](README.zh-CN.md)** ·
**[Compatibility](docs/compatibility.md)** · **[Get help](SUPPORT.md)** ·
**[Roadmap](ROADMAP.md)** · **[Contribute](CONTRIBUTING.md)**

## Choose your installation

- **Already running CPA / 1Panel?** [Add to the existing deployment](docs/getting-started.md#existing-cpa); keep your config, accounts and other plugins.
- **Starting fresh on Linux?** [Prepare the sidecar, secrets and private network](docs/getting-started.md#fresh-linux-deployment), then install from the Store.
- **Upgrading?** [Verify the accepted artifacts and preserve a rollback](docs/getting-started.md#upgrade-safety). Do not use withdrawn v0.3.17 or unaccepted v0.3.16.

The verified direct registry is maintained **in this repository**:

```text
https://raw.githubusercontent.com/simplez2/cpa-codex-agent-identity/main/registry.json
```

## How the pieces fit

```text
Import UI -> encrypted sidecar store -> CPA auth-file synchronization

PAT model request
  Client -> CPA native Codex executor -> current credential proxy -> upstream

Agent Identity JWT model request
  Client -> CPA plugin projection -> private sidecar (AgentAssertion) -> upstream

Existing native OAuth
  Client -> CPA's existing OAuth path (not taken over by this plugin)
```

The sidecar owns import, validation and synchronization. It is **not a PAT model
proxy**. JWT projections and native PAT files do not have identical controls;
see the [credential capability matrix](docs/compatibility.md#credential-paths).
CPA auth files hold the upstream `access_token` for native compatibility and
must be protected as plaintext secrets even though the sidecar store is encrypted.

## Documentation map

| Use | Guide |
| --- | --- |
| Install and first verification | [Getting started](docs/getting-started.md) / [中文说明](README.zh-CN.md) |
| Support and reported limitations | [Support](SUPPORT.md) / [Compatibility](docs/compatibility.md) |
| Runtime design | [Architecture](ARCHITECTURE.md) / [运行逻辑](RUNTIME_LOGIC.zh-CN.md) |
| Operations | [运维手册](HANDOFF.zh-CN.md) / [WebSocket semantics](docs/native-websockets.md) |
| Optional quota UI | [Management overlay](management-overlay/README.md) |
| Development and maintenance | [Contributing](CONTRIBUTING.md) / [Roadmap](ROADMAP.md) / [Maintenance](docs/maintenance.md) |
| Security and releases | [Security policy](SECURITY.md) / [Release process](RELEASE.md) / [Changelog](CHANGELOG.md) |

## Security boundary

- Original tokens are encrypted at rest in the sidecar store with AES-256-GCM.
- For native Keeper compatibility, the same upstream credential is also present in the CPA auth file's `access_token`; protect the CPA auth directory as secret material.
- The sidecar data directory is created with mode 0700 and its identity files with mode 0600 on POSIX systems. The sidecar only uploads and restores CPA auth files through CPA's Management API; operators must enforce an owner-only CPA auth directory and mode 0600 for those auth files.
- The data encryption key is mounted separately from the encrypted data volume.
- Management endpoints use constant-time management-key comparison.
- The sidecar UI stores the management password only in the current tab's `sessionStorage`. The plugin wrapper can read CPAMC's own scoped, obfuscated auth state, but never copies the key into an iframe URL, Cookie, export, or sidecar `localStorage`.
- Batch responses and exports never contain an original token, Authorization header, Cookie, private key, account ID, task ID, or proxy password.
- Import requests have size and item-count limits.
- Batch validation uses bounded concurrency.
- The UI builds result rows with DOM text nodes instead of inserting untrusted HTML.
- The plugin advertises one browser-navigable ResourceRoute at
  `/v0/resource/plugins/codex-agent-identity/open`, so supported CPAMC builds can
  show it under the plugin-pages menu. The wrapper never embeds or persists a
  Management key; it reads CPAMC's current scoped auth selection and transfers
  the key only to the same-origin sidecar frame through a nonce-bound
  `postMessage`.
- The authenticated Management API wrapper remains available at
  `/v0/management/codex-agent-identity/open`; the resource route is only an
  entry point and the sidecar still authenticates every identity operation.
- Sidecar embedding is denied by default and requires an explicitly trusted origin.
- The sidecar runtime bearer key may use cleartext HTTP only on loopback or the fixed private Compose service aliases and approved ports; user-configured sidecar hosts must use HTTPS, and CR/LF-bearing keys are rejected before CPA receives them.
- The sidecar uses a fixed upstream origin and strips proxy and authorization headers that must not be forwarded.
- Agent Identity 401 responses invalidate the cached task and retry once only when the request body is replayable.
- PAT 401 responses are not retried with an ineffective Agent Identity flow.

Treat the management password, encryption key, CPA auth files, upstream credentials, and generated cais_ values as secrets.

## Requirements

- A CPA build with dynamic plugin ABI v1, AuthProvider, Management API routes, and host auth-file management support.
- CLIProxyAPI v7.2.146 remains the build SDK baseline. The plugin uses
  dynamic plugin ABI v1; always canary-test it against the exact CPA image you
  plan to deploy.
- Linux amd64 or Linux arm64 for the released .so files.
- Docker or another process supervisor for the sidecar.
- A reverse proxy that publishes CPA and /agent-identity/ under the same browser origin is strongly recommended.

## Build and test

The repository requires Go 1.26.6 or later. The Makefile runs both Go modules:

~~~bash
make test
make race
make vet
make build
make verify-release-state
make package-plugin-portable GOOS=linux GOARCH=amd64
~~~

Equivalent direct commands are:

~~~bash
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build -trimpath -buildvcs=false -o bin/codex-agent-identity-sidecar ./cmd/sidecar

cd plugin/codex-agent-identity
go test ./... -count=1
cd ../..
make package-plugin-portable GOOS=linux GOARCH=amd64
make verify-plugin-compatibility GOOS=linux GOARCH=amd64
~~~

The integration suite covers JWT and PAT validation, HTTP, SSE, WebSocket, images, quota/reset-credit routing, task rebuild after 401, concurrent task reuse, proxy hot reload, batch preview, duplicate detection, non-atomic import, atomic abort, rollback helpers, authenticated ManagementRoute and ResourceRoute registration, and schema-version negotiation.

### Native plugin-page entry

CPA deliberately leaves `/v0/resource/plugins/...` outside Management-key
authentication because CPAMC loads these resources inside an iframe. The current
development line therefore advertises `/open` as a browser-navigable ResourceRoute while keeping
all credential operations in the sidecar's own Bearer-key-protected API. The
wrapper contains no hard-coded Management key, token, or privileged callback. It
can reuse CPAMC's scoped encrypted login state and delivers the key only through
a source-, origin-, and nonce-checked `postMessage`; the iframe URL remains
secret-free.

On a CPAMC build with plugin resources enabled, restart CPA after installing the
plugin and the **Codex Agent Identity** entry should appear under plugin pages.
The authenticated fallback remains `/v0/management/codex-agent-identity/open`,
and the direct sidecar entry remains `/agent-identity/`. The `management-overlay` is optional and only supplies reset-credit visibility
and the quota API bridge; it is not required for the plugin-page entry and does not
modify the installed plugin card.

## CPA plugin installation

### CPAMC Plugin Store

External Store indexes and cached GitHub metadata can lag behind a release.
For an explicit version and verified downloads, merge this repository's CPA
schema v2 direct source into the host-mounted CPA configuration. It pins the
accepted published **v0.3.18** archives by size and SHA-256; no upstream Store
repository changes are required.

~~~yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/simplez2/cpa-codex-agent-identity/main/registry.json"
~~~

The released Plugin Store assets follow CPA's required names:

~~~text
codex-agent-identity_<version>_linux_amd64.zip
codex-agent-identity_<version>_linux_arm64.zip
checksums.txt
~~~

Each archive contains codex-agent-identity.so at its root. CPA verifies the
archive checksum before installation. A containerized CPA needs a writable
host plugin mount during the install or update. Restore read-only mode after
the operation.
The store installs the `.so` only; it cannot safely create the sidecar container,
Docker network, encryption key, management key, or persistent data directory.
For a fresh Plugin Store installation, the management page uses the same-origin
`/agent-identity/` route by default, so remote CPA deployments do not point a
browser at its own `127.0.0.1`. The quota/reset bridge uses the internal sidecar
service from `CODEX_AGENT_IDENTITY_SIDECAR_HOSTS` (port `8787` by default) when
`sidecar_api_url` is blank. Direct host installs may keep an explicit local
`sidecar_url` such as `http://127.0.0.1:18787/agent-identity/`.
For a fresh deployment, `deploy/bootstrap-runtime.sh --start` prepares those
prerequisites so the Plugin Store step is the only manual installation action.

If the Plugin Store card says **configured** but **not registered**, remove stale
copies of `codex-agent-identity.so` and `codex-agent-identity-auth.so`, make sure
CPA is using its dynamic-plugin Linux build, and restart CPA after installation.
The Linux archives produced by this repository are now built and checked in CI
against CPA's GLIBC 2.17 compatibility baseline; a log containing `GLIBC_2.32`,
`GLIBC_2.34`, `dlopen`, or a missing `cliproxy_plugin_init` indicates an old or
locally rebuilt artifact and should not be used.

The registry uses CPA schema v2 direct artifacts with pinned sizes and SHA-256
digests. Installation therefore does not consume the server's anonymous GitHub
REST API quota.

### Manual installation

Extract the release archive so the dynamic library is at the root of the host plugin directory:

~~~text
runtime/
  cpa-plugins/
    codex-agent-identity.so
~~~

Mount the host directory into CPA:

~~~yaml
services:
  cli-proxy-api:
    image: eceasy/cli-proxy-api:v7.2.146
    volumes:
      - ./config.yaml:/CLIProxyAPI/config.yaml
      - ./auths:/root/.cli-proxy-api
      - ./logs:/CLIProxyAPI/logs
      - ./runtime/cpa-plugins:/CLIProxyAPI/plugins:ro
~~~

Enable the plugin in the host-mounted CPA configuration:

~~~yaml
plugins:
  enabled: true
  configs:
    codex-agent-identity:
      enabled: true
      priority: 1000
~~~

Fresh installations should omit `sidecar_url`: the plugin uses the same-origin
`/agent-identity/` management route and derives the internal quota/reset bridge
from the Docker environment when available. The legacy `sidecar_url` value
remains accepted for existing deployments, including direct local installs and
custom reverse-proxy paths. It may be a
root-relative URL or a full HTTP/HTTPS URL, and must not contain credentials,
query parameters, or a fragment. The wrapper contains no secret; the sidecar UI
must still authenticate before listing, previewing, or importing.

Do not load codex-agent-identity.so and the legacy codex-agent-identity-auth.so at the same time. Both claim the Codex Agent Identity auth-file provider/parser.

## Sidecar deployment

For a new checkout, the bootstrap helper creates the runtime directories, two independent secrets, a fresh CPA config with the plugin enabled, a random CPA API key, and the external Docker network. It can start the official CPA image and sidecar immediately:

~~~bash
sudo sh deploy/bootstrap-runtime.sh --sidecar-url /agent-identity/ --start
~~~

For a remote browser, explicitly select the same-origin path as above and
configure the reverse proxy separately. The helper itself defaults to a
loopback URL, suitable only for a browser on the same host:

~~~bash
sudo sh deploy/bootstrap-runtime.sh --sidecar-url http://127.0.0.1:18787/agent-identity/ --start
~~~

For an existing deployment, keep its config and env files and apply the equivalent settings manually. The durable example is deploy/docker-compose.production.yml; use an explicit project directory so the root-level config, auth, logs, and runtime paths resolve correctly:

~~~bash
sudo sh deploy/init-runtime.sh ./runtime
[ -e .env ] || cp .env.example .env
docker network inspect agent-identity >/dev/null 2>&1 || docker network create agent-identity
docker compose --project-directory . --env-file .env -f deploy/docker-compose.production.yml up -d
~~~

Use the same management password for CPA and the sidecar when automatic native auth-file synchronization is enabled.
Set that value in CPA's remote-management configuration and in
runtime/secrets/management-key. Keep CPA_PLUGIN_MOUNT_MODE=ro during normal
operation; temporarily use rw only for a deliberate CPAMC Plugin Store update.
The initializer gives runtime/data-v3 and runtime/secrets to the image's
unprivileged UID/GID 65532; changing SIDECAR_UID or SIDECAR_GID requires running
the initializer with the same values.

Important environment variables:

| Variable | Default | Purpose |
|---|---|---|
| LISTEN_ADDR | :8787 | Sidecar listen address |
| DATA_DIR | /data | Encrypted identity storage |
| DATA_ENCRYPTION_KEY_FILE | none | Preferred owner-only encryption-key file |
| MANAGEMENT_KEY_FILE | none | Preferred sidecar management-key file |
| CPA_MANAGEMENT_URL | none | CPA management base used for native auth-file sync and proxy hot reload |
| CPA_MANAGEMENT_KEY_FILE | sidecar key | CPA management-key file |
| PUBLIC_CPA_BASE_URL | http://127.0.0.1:8787/backend-api/codex | URL written to CPA auth files; Compose overrides this with the sidecar service name |
| CPA_PROXY_CONFIG_POLL_INTERVAL | 1s | CPA global proxy polling interval |
| OUTBOUND_PROXY_FILE | none | Fallback HTTP, HTTPS, or SOCKS proxy file |
| EMBED_ALLOWED_ORIGINS | http://127.0.0.1:8317 | Comma-separated trusted origins allowed to frame the sidecar UI; add the complete CPA origin when using a custom domain |
| UPSTREAM_ORIGIN | https://chatgpt.com | Fixed Codex upstream origin |
| JWKS_URL | official Agent Identity JWKS | JWT signing keys |
| AUTH_API_BASE_URL | official account API | Agent Identity task registration |
| PERSONAL_ACCESS_TOKEN_AUTH_API_BASE_URL | official account API | PAT whoami validation |
| MAX_REPLAY_BODY_BYTES | 16777216 | Maximum body retained for a safe 401 retry |
| CODEX_AGENT_IDENTITY_SIDECAR_API_URL | none | Advanced compatibility override; leave blank for automatic sidecar discovery |
| CODEX_AGENT_IDENTITY_SIDECAR_HOSTS | built-in container names plus loopback | Additional comma-separated sidecar hostnames accepted by the CPA plugin parser and used by the quota bridge |
| CODEX_AGENT_IDENTITY_SIDECAR_HTTP_PORTS | none | Indexed internal sidecar ports; the first host defaults to 8787, while additional explicit ports are also accepted by the parser |

Direct image requests selected by CPA for `gpt-image-1.5` or `gpt-image-2`
are bridged back through the Codex Responses image tool. JSON and multipart
edits, `response_format=url`, partial-image SSE, completed-image SSE, and the
Agent Identity 401 re-registration retry are preserved.

Use secret files instead of environment values whenever possible so credentials are not exposed by container inspection.

## Reverse proxy

Publish the sidecar UI through the same TLS reverse proxy as CPA when practical:

~~~nginx
location /agent-identity/ {
    proxy_pass http://127.0.0.1:18787;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    client_max_body_size 5m;
}
~~~

Stock CPA quota-card requests for sidecar-managed credentials also need the compatibility route:

~~~nginx
location = /v0/management/api-call {
    proxy_pass http://127.0.0.1:18787;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    client_max_body_size 2m;
    add_header Cache-Control no-store;
}
~~~

Unmanaged API calls are forwarded back to stock CPA unchanged, so official OAuth and third-party channels keep their existing behavior.

## Batch import

Open `/agent-identity/`, enter the CPA management password, and paste or select one of these formats.

Plain text or TXT:

~~~text
at-first-token
at-second-token
header.payload.signature
~~~

JSON array:

~~~json
[
  {"token": "...", "label": "account-a"},
  {"codex_access_token": "..."},
  "at-another-token"
]
~~~

JSONL:

~~~jsonl
{"token":"...","label":"account-a"}
{"access_token":"...","name":"account-b"}
~~~

The UI requires a preview before commit. Atomic mode is enabled by default. Preview validates official metadata but writes neither the encrypted store nor CPA auth files.

Management API endpoints:

| Method | Path | Purpose |
|---|---|---|
| POST | /agent-identity/api/identities/import | Backward-compatible single import |
| POST | /agent-identity/api/identities/import/batch?preview=true&atomic=true | Batch preview |
| POST | /agent-identity/api/identities/import/batch?preview=false&atomic=true | Batch commit |
| GET | /agent-identity/api/identities | Redacted list and summary |
| POST | /agent-identity/api/identities/{id}/actions | enable, disable, or refresh |
| DELETE | /agent-identity/api/identities/{id} | Delete encrypted token and CPA auth file |

Batch item statuses include ready, imported, duplicate, invalid, upstream_unavailable, failed, rolled_back, rollback_failed, and aborted.

## Proxy hot reload

When CPA_MANAGEMENT_URL is configured, the sidecar polls CPA global proxy-url. A non-empty CPA value overrides OUTBOUND_PROXY_FILE or OUTBOUND_PROXY. Clearing it restores the fallback. New requests use the new route without restarting CPA or the sidecar; in-flight requests and WebSocket sessions keep their existing connection.

Temporary CPA management outages preserve the last usable route. Proxy URLs and credentials are never written to logs.

## Upgrade-safe 1Panel layout

Keep all mutable state and plugins on host paths instead of inside the CPA container:

~~~text
/opt/codex-agent-sidecar/
  config.yaml
  auths/
  logs/
  runtime/
    cpa-plugins/
    data-v3/
    secrets/
  overlays/
    management.html
~~~

With these bind mounts, a 1Panel image pull or container recreation can follow
the official CPA image lifecycle without losing the plugin, encrypted
identities, auth files, logs, or optional management overlay. CPA_IMAGE is the
only value that needs to change for an official CPA image upgrade; this project
does not replace the CPA executable or bake a private CPA fork. Always
canary-test the candidate image, then pin its digest before production because
the plugin ABI and Management Center frontend can change independently.

## Migrating from the legacy plugin

1. Back up the sidecar data directory, encryption key, CPA config, and auth directory.
2. Keep the sidecar data volume unchanged.
3. Replace codex-agent-identity-auth.so with codex-agent-identity.so.
4. Rename the CPA plugin config key to codex-agent-identity; keep an existing sidecar_url only if the deployment uses a custom reverse-proxy path, otherwise omit it.
5. Restart only the staging CPA instance first.
6. Confirm that the legacy public resource path returns 404, the protected
   Management route returns 401 without a key, and the direct dashboard,
   existing credential list, quota card, streaming, image, and WebSocket paths
   still work.
7. Roll out to production only after the staging checks pass.

No token re-import is required because the encrypted store format remains compatible.

## Releases and plugin registry

Tags named vX.Y.Z run formatting, unit, integration, race, and vet checks; build
Linux amd64 and Linux arm64 plugin archives; generate checksums; publish a
GitHub Release; and publish a multi-architecture sidecar image to GHCR.

Release assets include:

~~~text
codex-agent-identity_<version>_linux_amd64.zip
codex-agent-identity_<version>_linux_arm64.zip
cpa-codex-agent-identity-sidecar_<version>_linux_amd64.tar.gz
cpa-codex-agent-identity-sidecar_<version>_linux_arm64.tar.gz
checksums.txt
~~~

`registry.json` is the directly usable, verified Plugin Store source for
**v0.3.18**. Source changes can accumulate under an unnumbered `Unreleased`
section without changing the recommended version. Tags first stage prerelease
assets; exact-asset acceptance comes **before** registry publication and Latest
promotion. Follow [Release process](RELEASE.md); merging a PR does not publish
or deploy a release.

## Optional Management Center overlay

`management-overlay` contains reproducible patches for reset-credit visibility
and the Codex quota API bridge. It deliberately does **not** add a second
Identity management button to the installed plugin card. The overlay remains
optional: the plugin registers its own native ResourceRoute, which CPAMC can
show under the plugin-pages menu without modifying stock card UI. Generated `management.html` is intentionally ignored by Git so
public history contains the patches and build recipe, not an environment-specific
build artifact.

Per-credit dates and selection require upstream detail access; aggregate counts
alone are insufficient. See [current limits](docs/compatibility.md#reset-credits-counts-are-not-details).

## License and status

MIT licensed. This is an independent integration project and is not an official OpenAI product. Review SECURITY.md before exposing the management UI to the internet.
