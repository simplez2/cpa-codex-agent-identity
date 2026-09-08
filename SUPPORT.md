# Support / 获取帮助

This is an independent community integration, not an official OpenAI or CPA
support channel. Reports in English and 简体中文 are welcome. There is no promised
response-time SLA; a small reproducible report is the fastest way to help.

## Choose a channel

| Need | Where to go |
| --- | --- |
| First installation or upgrade | [Getting started](docs/getting-started.md) |
| Confirm what is supported | [Compatibility and limitations](docs/compatibility.md) |
| Installation/configuration question | [Installation help form](https://github.com/simplez2/cpa-codex-agent-identity/issues/new?template=installation.yml) |
| Something reproducibly broken | [Bug report form](https://github.com/simplez2/cpa-codex-agent-identity/issues/new?template=bug_report.yml) |
| New capability or better UX | [Feature request form](https://github.com/simplez2/cpa-codex-agent-identity/issues/new?template=feature_request.yml) |
| Security issue or credential exposure | [Private security advisory](https://github.com/simplez2/cpa-codex-agent-identity/security/advisories/new) |

Search existing issues first. Do not open the same report in CPA, Keeper and
this project without identifying which component failed. Native OAuth files
are intentionally outside this plugin's credential ownership.

## What a useful report contains

- Plugin **installed** version, sidecar tag/digest and exact CPA image version.
- OS and CPU architecture; Docker/Compose, 1Panel or a direct-host deployment.
- Credential **kind** (PAT, Agent Identity JWT or native OAuth), not its value.
- Minimal steps, expected/actual result, a short redacted error and timestamp
  with timezone. State HTTP/SSE vs WebSocket and whether a proxy is configured.
- For a settings regression, say whether the value survived save, refresh and
  restart. For a streaming failure, distinguish HTTP status from stream completion.

**Do not upload whole logs or configuration archives.** Replace private emails,
Team IDs, hostnames and proxy addresses with consistent placeholders. Never
include tokens, API keys, cookies, reset-credit IDs or secret files. If a secret
was exposed, revoke it first and follow [SECURITY.md](SECURITY.md).

## Common symptoms

| Symptom | First check |
| --- | --- |
| Store displays an old version | Use this repository's verified [direct registry](registry.json); compare installed and advertised versions, not only the card title. |
| Configured but not registered | Check the exact Linux architecture, official artifact checksum, plugin-load error and duplicate legacy plugin. Keep a backup before replacing files. |
| Plugin page remains connecting | The store installs only the `.so`. Check sidecar health, private network and same-origin `/agent-identity/` reverse proxy. |
| WS enabled but upstream remains HTTP | Both the client transport and selected credential must allow WS. See [wire semantics](docs/native-websockets.md). |
| Quota works but reset-credit dates are absent | Counts and per-credit details are separate permissions. `credits: null` means details unavailable, not no credits. See [limitations](docs/compatibility.md). |
| Sync succeeds with an unavailable proxy | Warm validation metadata can be cached; sync success alone does not prove an outbound request. Do not clear a proxy to make a probe pass. |
| 401, 404, 503 or 504 | Include the inner error, timestamp and selected credential kind. A generic status alone cannot distinguish upstream policy, routing, cooldown or network failure. |

本仓库欢迎中文反馈。请提供**版本、环境、复现步骤和脱敏错误**，不要发 PAT、
认证文件或整份配置。额度券查询和使用是两件事：排查问题不需要、也不应消耗券。
