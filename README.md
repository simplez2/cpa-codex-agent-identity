# CPA Codex Agent Identity

[简体中文](README.zh-CN.md) | English

CPA-native management and routing support for Codex Agent Identity JWTs and opaque Personal Access Tokens whose current prefix is at-.

The project combines two deliberately separate components:

- A CPA dynamic plugin named codex-agent-identity.so. It registers the Codex auth parser and a CPAMC management entry, similar to the Keeper plugin experience.
- A hardened sidecar. It validates credentials, encrypts original tokens, creates AgentAssertion headers, forwards Codex traffic, synchronizes native CPA auth files, and follows CPA proxy changes without a restart.

The first public release keeps the mature sidecar data plane instead of rewriting streaming, image, quota, WebSocket, and AgentAssertion behavior inside the plugin. The CPA control plane is native today, while a future pure-plugin executor can be added without changing the encrypted data format.

## Highlights

- Appears in CPAMC as an Agent Identity resource page.
- Supports Agent Identity JWT and Personal Access Token credentials.
- Imports plain text, JSON, JSONL, and TXT files.
- Previews and validates a batch without writing anything.
- Deduplicates both within the submitted batch and against the encrypted store.
- Supports atomic batch import with automatic rollback.
- Produces redacted JSON and CSV result reports.
- Shows active, disabled, unsynchronized, Agent Identity, and PAT counts.
- Supports enable, disable, refresh, and delete actions.
- Preserves disabled state during credential refresh and sidecar reconciliation.
- Encrypts original tokens with AES-256-GCM.
- Stores only random cais_ proxy keys in CPA auth files.
- Hot-reloads CPA global HTTP, HTTPS, and SOCKS proxy changes for new requests.
- Keeps the official CPA image lifecycle separate from plugin, sidecar, data, and overlay mounts.

## Architecture

~~~text
Browser / CPAMC
  -> CPA plugin resource: /v0/resource/plugins/codex-agent-identity/open
  -> same-origin sidecar UI: /agent-identity/?embed=cpamc
  -> authenticated sidecar management API

Codex client
  -> CPA request translation and credential selection
       Authorization: Bearer cais_<random-sidecar-key>
  -> encrypted Agent Identity sidecar
       JWT: verify JWKS, register/cache task, create AgentAssertion
       PAT: verify whoami, forward as Bearer at-...
  -> https://chatgpt.com/backend-api/codex
~~~

CPA never receives the original Agent Identity JWT or PAT. It receives only a random, revocable sidecar client key plus non-secret display and routing metadata.

## Security boundary

- Original tokens are encrypted at rest with AES-256-GCM.
- The data directory is owner-only on POSIX systems and identity files use mode 0600.
- The data encryption key is mounted separately from the encrypted data volume.
- Management endpoints use constant-time management-key comparison.
- The browser stores the management password only in sessionStorage, never localStorage.
- Batch responses and exports never contain an original token, Authorization header, Cookie, private key, account ID, task ID, or proxy password.
- Import requests have size and item-count limits.
- Batch validation uses bounded concurrency.
- The UI builds result rows with DOM text nodes instead of inserting untrusted HTML.
- Resource embedding is denied by default. CPAMC mode permits same-origin embedding, plus explicitly configured origins.
- The sidecar uses a fixed upstream origin and strips proxy and authorization headers that must not be forwarded.
- Agent Identity 401 responses invalidate the cached task and retry once only when the request body is replayable.
- PAT 401 responses are not retried with an ineffective Agent Identity flow.

Treat the management password, encryption key, and generated cais_ values as secrets.

## Requirements

- A CPA build with dynamic plugin ABI v1, AuthProvider, Management API resources, and host auth-file management support.
- CLIProxyAPI v7.2.94 is the SDK baseline for release v0.3.1. The plugin uses
  dynamic plugin ABI v1; always canary-test it against the exact CPA image you
  plan to deploy.
- Linux amd64 or Linux arm64 for the released .so files.
- Docker or another process supervisor for the sidecar.
- A reverse proxy that publishes CPA and /agent-identity/ under the same browser origin is strongly recommended.

## Build and test

The repository currently uses Go 1.26. The Makefile runs both Go modules:

~~~bash
make test
make race
make vet
make build
make package-plugin VERSION=0.3.1 GOOS=linux GOARCH=amd64
~~~

Equivalent direct commands are:

~~~bash
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build -trimpath -buildvcs=false -o bin/codex-agent-identity-sidecar ./cmd/sidecar

cd plugin/codex-agent-identity
go test ./... -count=1
CGO_ENABLED=1 go build -trimpath -buildvcs=false -buildmode=c-shared -o ../../bin/codex-agent-identity.so .
~~~

The integration suite covers JWT and PAT validation, HTTP, SSE, WebSocket, images, quota/reset-credit routing, task rebuild after 401, concurrent task reuse, proxy hot reload, batch preview, duplicate detection, non-atomic import, atomic abort, rollback helpers, and plugin resource registration.

## CPA plugin installation

### CPAMC Plugin Store

Add this repository's registry to the host-mounted CPA configuration:

~~~yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/simplez2/cpa-codex-agent-identity/main/registry.json"
~~~

The released Plugin Store assets follow CPA's required names:

~~~text
codex-agent-identity_0.3.1_linux_amd64.zip
codex-agent-identity_0.3.1_linux_arm64.zip
checksums.txt
~~~

Each archive contains codex-agent-identity.so at its root. CPA verifies the
archive checksum before installation. A containerized CPA needs a writable
host plugin mount during the install or update. Restore read-only mode after
the operation.

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
    image: eceasy/cli-proxy-api:v7.2.94
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
      sidecar_url: "/agent-identity/"
~~~

The sidecar_url value can be a root-relative same-origin URL or a full HTTP/HTTPS URL. It must not contain credentials, query parameters, or a fragment.

Do not load codex-agent-identity.so and the legacy codex-agent-identity-auth.so at the same time. Both claim the Codex auth parser.

## Sidecar deployment

The durable example is deploy/docker-compose.production.yml. Generate two independent secrets before starting it:

~~~bash
sudo sh deploy/init-runtime.sh ./runtime
cp .env.example .env
docker network inspect agent-identity >/dev/null 2>&1 || docker network create agent-identity
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
| PUBLIC_CPA_BASE_URL | internal sidecar URL | URL written to CPA auth files |
| CPA_PROXY_CONFIG_POLL_INTERVAL | 1s | CPA global proxy polling interval |
| OUTBOUND_PROXY_FILE | none | Fallback HTTP, HTTPS, or SOCKS proxy file |
| EMBED_ALLOWED_ORIGINS | none | Comma-separated additional CPAMC origins allowed to frame the UI |
| UPSTREAM_ORIGIN | https://chatgpt.com | Fixed Codex upstream origin |
| JWKS_URL | official Agent Identity JWKS | JWT signing keys |
| AUTH_API_BASE_URL | official account API | Agent Identity task registration |
| PERSONAL_ACCESS_TOKEN_AUTH_API_BASE_URL | official account API | PAT whoami validation |
| MAX_REPLAY_BODY_BYTES | 16777216 | Maximum body retained for a safe 401 retry |

Use secret files instead of environment values whenever possible so credentials are not exposed by container inspection.

## Reverse proxy

Publish the sidecar UI under the same origin as CPAMC:

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

Open the Agent Identity entry in CPAMC, enter the CPA management password, and paste or select one of these formats.

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
4. Rename the CPA plugin config key to codex-agent-identity and set sidecar_url.
5. Restart only the staging CPA instance first.
6. Confirm the plugin resource, existing credential list, quota card, streaming, image, and WebSocket paths.
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

registry.json is a directly usable CPA Plugin Store source. Inclusion in the
built-in official CPA registry requires a separate reviewed pull request to
router-for-me/CLIProxyAPI-Plugins-Store.

## Management Center reset-credit overlay

management-overlay contains the existing reproducible reset-credit patch for the CPA Management Center. It remains optional and separate from the .so plugin. The generated management.html is intentionally ignored by Git so public history contains the patch and build recipe, not an environment-specific build artifact.

## License and status

MIT licensed. This is an independent integration project and is not an official OpenAI product. Review SECURITY.md before exposing the management UI to the internet.
