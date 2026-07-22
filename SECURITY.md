# Security policy

Do not open a public issue containing a Codex token, Agent Identity JWT,
Personal Access Token, cais_ key, CPA management key, encryption key, auth
file, proxy credential, Cookie, or captured Authorization header. Revoke an
exposed credential first, then use a private GitHub Security Advisory for this
repository.

Only the latest release receives security fixes. Before reporting a problem,
reproduce it with an unmodified release artifact and redact all secrets,
account identifiers, request bodies, and private hostnames.

## Deployment requirements

- Treat every CPA dynamic plugin as trusted in-process code. Verify
  checksums.txt before installation and keep the plugin mount read-only during
  normal operation.
- If CPAMC Plugin Store manages updates, make the host plugin directory writable
  only for the intentional install/update window, then restore read-only mode.
- Keep CPA and the sidecar on a private Docker network.
- Bind management and UI host ports to loopback and publish them only through a
  TLS reverse proxy.
- Use DATA_ENCRYPTION_KEY_FILE and keep the 32-byte key outside the encrypted
  data volume, owner-readable only, and backed up separately.
- Ensure the encrypted data and secret directories are owned by the configured
  unprivileged sidecar UID/GID. Do not solve permission errors by running the
  sidecar as root or making secret files world-readable.
- Use secret files for management and proxy credentials so container metadata
  does not expose them.
- Keep CPA_MANAGEMENT_URL on the private container network. Proxy hot reload
  reads only CPA's management configuration and never logs the proxy value.
- Run the sidecar read-only with all Linux capabilities dropped and
  no-new-privileges.
- Pin both CPA and sidecar images by digest after canary verification. Test
  every CPA/plugin ABI update in an isolated instance before production.
- Never log request headers, request bodies, tokens, encrypted identity files,
  batch-import bodies, or secret file contents.

## Management and browser boundary

- The dashboard may be served publicly, but every identity API requires the
  management key and compares it in constant time.
- The browser stores that key only in sessionStorage. It is intentionally not
  persisted in localStorage, cookies, exports, URLs, or DOM attributes.
- Use a same-origin /agent-identity/ deployment when possible. Cross-origin
  embedding must be explicitly listed in EMBED_ALLOWED_ORIGINS.
- Batch imports are limited to 4 MiB and 200 credentials, use bounded
  validation concurrency, and return only redacted identity metadata.
- Atomic import is the default because it avoids silently leaving a partially
  synchronized credential set. A rollback failure is surfaced explicitly and
  requires operator review.

ALLOW_PLAINTEXT_STORE=true and ALLOW_INSECURE_UPSTREAM=true are development
escape hatches. They must not be used in production.

The POST /backend-api/wham/rate-limit-reset-credits/consume compatibility route
may spend a reset credit. Monitoring, startup, reconciliation, health checks,
and validation must never invoke it. A deliberate user action in CPA is the
only expected caller.
