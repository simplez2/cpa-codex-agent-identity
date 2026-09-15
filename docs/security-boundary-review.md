# Security boundary review — September 15, 2026

## Scope and release status

This is a bounded source review, not a certification or proof of complete CPA
parity. Changes belong only to Agent Identity. No CPA, Keeper or official
Plugin Store repository changes, real account probes, credit redemption,
production changes, tag replacement or stable promotion are part of this patch.
The accepted registry stays on v0.3.18; these changes are **not** in the existing
immutable v0.3.19 prerelease assets.

## Confirmed findings and repairs

| ID | Priority / boundary | Evidence and repair |
| --- | --- | --- |
| B1 | Medium — UI request parsing | `normalizeSidecarUIAPIRequest` previously used `URL.Query()`, losing parse errors. Strict `ParseQuery`, the existing method/route/query allowlist and canonical encoding now reject malformed or duplicate parameters before forwarding. |
| B2 | Medium — configured URL paths | Dot segments, encoded separators and double escaping could be interpreted differently by the plugin, browser and proxy. `validateCanonicalURLPath` rejects ambiguity without silently cleaning destinations. Invalid URL errors no longer include the raw input. Configuration is operator-controlled; this is not evidence of an unauthenticated SSRF. |
| B3 | Medium — browser and HTTP credential boundaries | The wrapper used to send management keys on iframe load, and retained a nonce across candidates. Delivery now waits for a matching source/origin/nonce readiness event. Retry/fallback rotate the nonce; no-crypto mode retains manual login without automatic key delivery. Both HTTP bridges forward only CPA-supported management authentication headers. |
| B4 | High impact if triggered — identity persistence | File mutations and memory publication used different lock boundaries; failed deletion could clear indexes while retaining the credential on disk. Store mutations now serialize files/indexes, preserve state after unlink failure, and distinguish applied changes from failed durability. Windows no longer pre-deletes the old file before replacement. Management imports roll back applied writes on durability failure; failed rollback remains explicit in batch reports. Applied deletion does not recreate a dangling CPA credential. |
| B5 | Medium — bootstrap and diagnostic tools | Existing deployments with `--start` are rejected before initialization. URL/control-character/YAML ambiguity and unsupported runtime roots are rejected; existing configuration/secrets are preserved. Plugin hashing uses one non-blocking descriptor and checks regular-file type before reading, retaining mapped inode/device/digest verification. |

Relevant rules: GO-PATH-001, GO-CONFIG-001, GO-CONC-001,
GO-HTTPCLIENT-001 and JS-MSG-001. Priorities describe the bounded scenarios,
not a claim that each issue is remotely exploitable.

Source locations in this candidate:

- B1/B2: `plugin/codex-agent-identity/plugin.go:634` and `:875`.
- B3: `plugin/codex-agent-identity/plugin.go:904`, `:1399` and `:1492`.
- B4: `internal/store/store.go:265`, `:358`, `:540`, `:569` and `:599`;
  `internal/server/management_import.go:42`, `internal/server/server.go:493`,
  and `internal/server/batch_import.go:97`.
- B5: `deploy/bootstrap-runtime.sh:88` and `:113`;
  `.github/scripts/debug-plugin-install.py:32`.

## Counterevidence and retained contracts

- CPA's `Authorization` and `X-Management-Key` are intentionally forwarded to
  the configured **trusted** sidecar, which authenticates with the same operator
  key. Removing them breaks native management integration. Inner UI request
  headers cannot override outer authentication.
- The private CPA-to-sidecar management hop intentionally bypasses environment
  HTTP proxies. This is separate from the credential's upstream proxy policy;
  this patch does not change native PAT execution or quota proxy routing.
- Nonce readiness is not isolation from malicious same-origin scripts. CPA and
  the configured dashboard origin remain trusted; credentials are never sent
  using a wildcard target origin.
- A Store mutex protects one Store instance, not multiple processes sharing
  its directory. Keep exclusive ownership of its runtime data directory.

## Validation and remaining gates

Regression suites cover malformed queries/encoded paths, exact management
header forwarding, generated-wrapper handshake/failover/retry/manual login,
concurrent same-identity mutations, multi-Team isolation, unlink/write/sync
failure, rollback reporting, bootstrap sentinels and diagnostic file types.
The wrapper tests execute rendered JavaScript in Node, not a real browser.

The new four plugin regressions fail against the pre-fix plugin source and pass
against the repaired source. Plugin validation is performed separately against
the committed CPA SDK baseline and the locally retained v7.3.0 SDK change.
Unrelated local module changes are not part of this patch.

Windows skips POSIX FIFO/device/proc-mapping and symlink tests; CI must execute
those on Linux. Injected sync failures test business semantics, not real disk
failure or crash durability. Windows DACLs and hostile concurrent ancestor
symlink replacement are not certified by this review.

Local Docker is unavailable, so portable Linux artifact builds, disposable
stock-CPA installation/restart, browser login, real account persistence,
complete HTTP/WS streams and refused-proxy acceptance remain release gates.
Do not advertise zero-setup/full native parity or promote the candidate based
only on these source tests. Follow [the release procedure](../RELEASE.md).

### Local validation record

Using Go 1.26.6 on Windows:

- Root module: `go test -mod=readonly ./... -count=1`, `-race`, and `go vet`
  passed, including the new management failure-path tests.
- Plugin: the same test/race/vet checks passed on both the committed v7.2.146
  SDK and the separately preserved local v7.3.0 SDK configuration.
- Bootstrap: 14 tests, 2 POSIX skips. Diagnostic helper: 10 tests, 3 POSIX
  skips. Community validation: 10 tests passed.
- Shell syntax, version lifecycle guard and its tests passed.
- Gitleaks 8.30.1 found no secrets in the isolated selected source tree.
- `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...` did **not** complete:
  downloading the scanner from `proxy.golang.org` timed out. This is not a clean
  vulnerability scan; the CI scan remains required.
