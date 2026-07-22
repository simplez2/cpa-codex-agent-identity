# Management panel overlay

This directory contains a small patch for the official CLIProxyAPI Management
Center. It is deliberately separate from both the official CPA binary and the
sidecar image.

The patch is pinned to upstream commit
`6a6a22af85ce8763e8898c0d8641de3137f3ffd9` from
`router-for-me/Cli-Proxy-API-Management-Center`.

It adds official-style reset-credit management to the Codex quota card:

- one compact available-credit count, which also controls whether the reset
  action is shown;
- a picker for individual available credits, ordered by expiry, with grant and
  expiry times shown in `Asia/Shanghai`;
- the selected opaque `credit_id` in the consume request without rendering the
  identifier into the DOM;
- an official-compatible "use next available credit" fallback when the
  credential exposes only a summary count and no detail rows.

Chinese copy calls these items "reset credits" instead of "active resets" so
they are not confused with the quota window's automatic reset. Detailed expiry
timestamps are normalized from ISO-8601 or Unix seconds/milliseconds and shown
in `Asia/Shanghai` (UTC+8) without applying the offset twice.

The reset action follows official Codex behavior and disappears only when
`available_count` reaches zero. `applicable_available_count` is informational
and does not hide a count-only Personal Access Token credit. When detail rows
are unavailable, the request omits `credit_id` and lets the upstream select the
next available credit. No expiry is guessed from the monthly quota window,
because that reset time is not the reset credit's expiry. The sidecar forwards
the request body unchanged for both Agent Identity and mounted
`codex_access_token` credentials.

## Rebuild

Run the PowerShell helper from the repository root:

```powershell
.\management-overlay\build.ps1 -BunPath (Get-Command bun).Source
```

The helper clones the pinned public upstream, applies
`reset-credit-visibility.patch`, runs tests/lint/build, and writes the verified
single-file page to `management-overlay/out/management.html`.

## Durable production mount

Store the generated file outside the CPA container and bind-mount it over the
stock management page:

```yaml
services:
  cli-proxy-api:
    volumes:
      - ./overlays/management.html:/CLIProxyAPI/static/management.html:ro
```

Set `remote-management.disable-auto-update-panel: true` in the host-mounted CPA
configuration. Otherwise the built-in panel updater may repeatedly try to
replace the read-only overlay.

An official CPA image pull or container recreation then keeps the patched page.
For each Management Center upgrade, rebase the patch onto the new upstream
commit and rerun the full verification before replacing the mounted file.
