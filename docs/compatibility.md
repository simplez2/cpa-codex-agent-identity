# Compatibility and limitations / 兼容性与边界

Updated 2026-09-08 for **v0.3.18**. Build compatibility, unit tests and live
acceptance are different claims. Do not extrapolate one tested deployment to
all accounts, proxies, future CPA versions or credential types.

## Version map

| Item | Current baseline | Meaning |
| --- | --- | --- |
| Recommended plugin and sidecar | v0.3.18 | [Immutable assets and acceptance](releases/v0.3.18.md) |
| CPA SDK used to compile the plugin | v7.2.146 | Build baseline, not a promise for every newer runtime |
| Exact stock CPA runtime accepted for v0.3.18 | v7.2.152 | Native PAT acceptance; see the recorded test scope |
| Bundled CPA image example | v7.2.146 | Older example baseline; explicitly select and test the CPA image you deploy |
| Portable plugin targets | Linux amd64 / arm64, GLIBC 2.17 | Release ABI checks on both architectures; the recorded production smoke is amd64 |
| Go toolchain baseline | 1.26.6 | Both modules and build paths must move together |

## Credential paths

| Capability | Managed PAT | Agent Identity JWT | Ordinary CPA OAuth |
| --- | --- | --- | --- |
| Credential import/storage | Encrypted sidecar store + native CPA auth file | Encrypted sidecar store + plugin projection | Owned by CPA, untouched |
| Model transport | CPA native `codex` executor, no sidecar model hop | Sidecar required for dynamic AgentAssertion | Owned by CPA |
| Enable/disable, proxy, note, priority | Native file-backed controls; tested across synchronization/restart | Do not assume identical file-backed controls | Owned by CPA |
| User-Agent and identity remapping | CPA applies its native policy | Scoped bridge compatibility; not full parity | Owned by CPA |
| WebSocket | Requires a WS client **and** the selected credential's flag | Separate sidecar transport; not covered by all PAT acceptance | Owned by CPA |
| Quota | Native CPA path; respect the credential's current proxy | Dynamic authorization may require the bridge | Owned by CPA |

Sidecar storage is encrypted, but CPA's `access_token` contains the upstream
credential for native compatibility. Protect the CPA auth directory and backups
as plaintext secrets. See [security policy](../SECURITY.md).

## Reset credits: counts are not details

Read-only checks on 2026-09-08 found PAT detail requests rejected with
`401 / rejected_by_access_enforcement / no_matching_rule`, while the usage
endpoint still exposed aggregate counts. The sidecar's compatible fallback is:

```json
{"credits": null, "available_count": 3, "applicable_available_count": 0}
```

This is an illustrative response shape, **not a promised balance**.

- `available_count` is banked credit count; `applicable_available_count` governs
  current eligibility when provided. A banked credit is not necessarily usable now.
- `credits: null` means detail is unavailable. `credits: []` means a successful
  empty detail list. Do not treat them as equivalent.
- Without credit IDs and dates, neither a plugin nor a UI can select a credit
  by grant/expiry time. The quota window's reset time is not a credit expiry.
- The optional [management overlay](../management-overlay/README.md) has code
  for per-credit selection and UTC+8 dates when details are provided. It is not
  included by merely installing the `.so`; live redemption is not part of our
  acceptance tests.
- A separate management-plugin response escaping discrepancy is tracked in the
  [issue list](https://github.com/simplez2/cpa-codex-agent-identity/issues?q=is%3Aissue%20label%3A%22area%3Aquota%22).
  Do not treat the bridge's HTTP 200 alone as proof of valid JSON.

**Never consume a reset credit to diagnose quota visibility.** No reset-credit
consumption is authorized by these instructions.

## Installation boundary

The Plugin Store downloads a `.so`; it does not provision Docker services,
secrets, networks or durable storage. The sidecar and same-origin management
route remain prerequisites. Follow [getting started](getting-started.md), then
test your exact CPA image before upgrading production.

中文：PAT 尽量走 CPA 原生路径，JWT 仍需动态签名桥接，两者不能混称完全原生。
额度券只有数量时，不能推测日期或指定某张券；功能兼容不等于绕过上游权限。
