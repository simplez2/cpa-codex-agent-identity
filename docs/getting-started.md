# Getting started / 开始使用

Choose your path before installing. This project adds **a plugin and a sidecar**
to stock CPA; the Plugin Store does not create the sidecar for you.

| Your situation | Start here |
| --- | --- |
| CPA already runs in Docker/1Panel | Preserve its config/auths; follow [existing deployment](#existing-cpa) |
| New Linux installation | Follow [fresh deployment](#fresh-linux-deployment) |
| Unsure which image/version to use | Read the [compatibility matrix](compatibility.md) |
| Upgrading an existing working install | Read [upgrade safety](#upgrade-safety) first |

## Existing CPA

1. Back up the **current** CPA config/auth directory, sidecar data and encryption
   key separately. Do not replace your Compose file with the new-install example.
2. Deploy the sidecar with owner-only persistent storage and the same CPA
   management password. Join CPA and sidecar to a private network; see
   [sidecar deployment](../README.md#sidecar-deployment) / [中文部署](../README.zh-CN.md#部署-sidecar).
3. Expose `/agent-identity/` on the **same HTTPS origin** as CPA. Configure
   `EMBED_ALLOWED_ORIGINS` for that exact origin; use the
   [reverse-proxy example](../README.md#reverse-proxy).
4. Merge this repository's direct source into your existing `plugins` section,
   retaining other sources and settings:

   ```yaml
   plugins:
     enabled: true
     store-sources:
       - "https://raw.githubusercontent.com/simplez2/cpa-codex-agent-identity/main/registry.json"
     configs:
       codex-agent-identity:
         enabled: true
         priority: 1000
   ```

5. Make the plugin mount writable only for the install/update window. Install
   **Codex Agent Identity** in the Store, then restart CPA if needed to load the
   dynamic library. Restore the normal read-only plugin mount afterward.
6. Open the native **plugin-pages → Codex Agent Identity** entry. Preview a small
   credential import, check the intended Team and confirm the import privately.
   Do not paste tokens into GitHub issues.

## Fresh Linux deployment

Prerequisites: Linux amd64/arm64, Git, Docker with Compose, OpenSSL, and an
operator who can configure the private network and TLS reverse proxy. The helper
starts new services and creates secret files; it is **not** an existing-install
migration command.

```sh
git clone --branch v0.3.18 --depth 1 https://github.com/simplez2/cpa-codex-agent-identity.git
cd cpa-codex-agent-identity
cp .env.example .env
# Review .env before starting. To reproduce v0.3.18's recorded acceptance,
# set CPA_IMAGE=eceasy/cli-proxy-api:v7.2.152 (the example still pins v7.2.146).
sudo sh deploy/bootstrap-runtime.sh --sidecar-url /agent-identity/ --start
```

The explicit `--sidecar-url /agent-identity/` is for a same-origin reverse proxy;
the helper does **not** configure that proxy. Finish steps 3–6 above. For direct
host access from a browser on the same host, the helper also supports
`--sidecar-url http://127.0.0.1:18787/agent-identity/`; that loopback URL is not
correct for a browser on another machine.

## Verify without spending a reset credit

- Plugin version is **installed, registered and effective**, not merely configured.
- Sidecar is healthy; the plugin page loads and authenticates.
- A PAT import creates a file-backed CPA `type: codex` credential; ordinary OAuth
  files remain untouched. Save/refresh does not reset native fields.
- Use the ordinary quota **read** operation with the current credential proxy.
  Do not click reset/use-credit as a health check.
- If you explicitly choose to test a model, verify the complete stream, not only
  its HTTP status. A model call can consume normal model quota.

## Upgrade safety

Use [the latest accepted release](https://github.com/simplez2/cpa-codex-agent-identity/releases/latest)
and verify `checksums.txt`. Keep the prior plugin/image and owner-only backups
until the exact new pair passes your checks. Preserve account/Team, proxy,
disabled/enabled, WebSocket, note and priority settings.

Do not install withdrawn v0.3.17 or unaccepted v0.3.16 as upgrades. Do not reuse
an old version label for new bytes. For detailed lifecycle and rollback rules,
see [RELEASE.md](../RELEASE.md).

中文：先准备 sidecar、私有网络、密钥和同源入口，再到商店装插件。已有 CPA
不要覆盖配置；验证不需要消耗重置券。卡片“已配置”不等于“已注册并生效”。
