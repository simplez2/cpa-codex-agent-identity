# Management panel overlay

This directory contains a small patch for the official CLIProxyAPI Management
Center. It is deliberately separate from both the official CPA binary and the
sidecar image.

The overlay is pinned to upstream commit
`6a6a22af85ce8763e8898c0d8641de3137f3ffd9` from
`router-for-me/Cli-Proxy-API-Management-Center`.

It applies two small, reviewable patches.

`codex-quota-api-bridge.patch` routes the Codex quota and reset-credit calls
through the installed `codex-agent-identity` plugin route. The plugin forwards
the CPA-compatible request to the sidecar, which dynamically creates the
correct `AgentAssertion` or selects the correct PAT Team before calling the
upstream endpoint. Non-managed native Codex OAuth files are forwarded back to
CPA's original `/v0/management/api-call`, so installing the overlay does not
change native OAuth behavior. This bridge is required for Keeper/CPAMC quota
views because stock CPA's generic api-call would otherwise send the sidecar
`cais_` client key as a Bearer token and receive a 401.

`reset-credit-visibility.patch` adds official-style reset-credit management to
the Codex quota card:

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

The reset action follows official Codex behavior: a banked reset becomes
actionable only after the account reaches an eligible rate limit. When
`applicable_available_count` is present, it is authoritative for whether the
action is enabled; `available_count` remains the total banked count shown to the
user. Older responses that omit the applicable count fall back to the banked
count for compatibility. When detail rows are unavailable, the request omits
`credit_id` and lets the upstream select the next applicable credit. No expiry
is guessed from the monthly quota window, because that reset time is not the
reset credit's expiry. The sidecar forwards the request body unchanged for both
Agent Identity and mounted `codex_access_token` credentials.

The overlay intentionally does not modify the CPA plugin card or add a
second management entry point. The `codex-agent-identity` plugin registers its
own safe `/v0/resource/plugins/.../open` resource, which CPAMC exposes through
its native plugin-pages menu. This keeps the plugin discoverable through the
same surface as other CPA plugins instead of introducing a card-specific
shortcut. The sidecar UI performs its own Bearer-key authentication before
listing, previewing, or importing identities.

The sidecar import flow supports pasted text or a local file, requires a
preview, reports ready/duplicate/invalid entries, and can commit atomically.
Sensitive import text is not stored by this overlay.

Installing the plugin `.so` or adding its registry source is enough for the
native plugin-pages entry on CPA builds that support ResourceRoute menus. Open
`management.html#/plugins`, select the **Codex Agent Identity** menu, and use
the embedded page. The direct fallback remains `/agent-identity/`. The overlay
is optional and only supplies reset-credit visibility plus the quota bridge.

## Rebuild

Run the PowerShell helper from the repository root:

```powershell
.\management-overlay\build.ps1 -BunPath (Get-Command bun).Source
```

The helper clones the pinned public upstream, applies the two functional
patches in order,
runs tests/lint/build, and writes the verified single-file page to
`management-overlay/out/management.html`. CI pins the upstream-declared Bun
release and verifies required entry markers plus the absence of the legacy
public plugin route. The printed SHA-256 is informational because Rolldown's
chunk ordering can differ across otherwise equivalent build environments. An
invalid build is rejected before it can replace the requested output file.

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
