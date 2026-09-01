# CPA Codex Agent Identity 生产交接与运维手册

本文用于部署、升级、备份、恢复和发布当前项目。正式 Release 以 GitHub tag 与 registry 为准；生产验证分支不自动等于正式版本。

## 1. 交接资产

接管前确认以下资产分别存在且有备份：

| 资产 | 作用 | 是否敏感 |
|---|---|---|
| codex-agent-identity.so | CPA 进程内插件 | 需校验哈希，非 secret。 |
| sidecar image/digest | 管理面和数据面 | 需校验签名/摘要。 |
| data-v3 | 加密 identity 文件 | 敏感备份。 |
| data-encryption-key | 解密 data-v3 的 32-byte key | 最高敏感，单独备份。 |
| management-key | sidecar 与 CPA 管理认证 | 最高敏感。 |
| CPA auths | 含 cais_ key 的 native auth 文件 | 敏感。 |
| config.yaml | CPA/plugin 配置 | 可能含内部路径，不应公开。 |
| management overlay | 可选前端补丁产物 | 非 secret，但必须与 CPA 前端版本对齐。 |

只有 data-v3 没有 encryption key 无法恢复；只有 key 没有 data-v3 也无意义。二者应分开存放和恢复演练。

## 2. 版本边界

当前源码开发线是 v0.3.9，pluginVersion 与根目录 VERSION 必须一致；公开 registry 和可直接安装资产仍是 v0.3.8。只有 Linux 资产、GitHub Release、checksums 和下载验证全部完成后，才能在单独的发布提交中把 registry 切换到 v0.3.9。

发布新版本时必须同步：

- 根目录 VERSION；
- plugin/codex-agent-identity/plugin.go 的 pluginVersion；
- CHANGELOG.md 的 Unreleased 版本；
- registry.json version、asset URL、size、SHA-256（仅在资产发布后更新）；
- Git tag；
- plugin zip、sidecar tar、checksums；
- GHCR tag/digest；
- README 版本说明。

## 3. 推荐 1Panel/Compose 布局

~~~text
deployment/
  config.yaml
  auths/
  logs/
  runtime/
    cpa-plugins/
      codex-agent-identity.so
    data-v3/
    secrets/
      management-key
      data-encryption-key
  overlays/
    management.html
~~~

- CPA 使用官方 image/digest，不构建私有 CPA fork；
- plugin、auth、logs、data、secrets、overlay 全部宿主挂载；
- CPA plugin mount 正常为 ro，只在 Plugin Store 安装/升级期间临时 rw；
- sidecar read_only、drop ALL capabilities、no-new-privileges、非 root UID/GID；
- 管理端口绑定 loopback，通过 TLS reverse proxy 对外；
- CPA 与 sidecar 只通过私有 Docker network 通信。

这样在 1Panel 点击拉取或重建官方 CPA 镜像时，不会删除插件、凭据或前端 overlay。

## 4. 首次部署

1. 运行 deploy/init-runtime.sh 初始化 0700/0600 目录和 secret。
2. 把 management-key 配置为 CPA remote-management.secret-key，或明确挂载独立 CPA key。
3. 下载 Release 资产并对照 checksums.txt。
4. 将动态库放到 runtime/cpa-plugins 根目录。
5. 启用 codex-agent-identity 插件；新安装不要填写 sidecar_url，只有旧的自定义反向代理部署才保留它。
6. 使用 deploy/docker-compose.canary.yml 先启动隔离 canary。
7. 验证 plugin registration、/healthz、/agent-identity/ 登录和空 identity list。
8. 预检一条测试凭据，再确认 CPA auth 文件只含 cais_ key。
9. 验证 HTTP、SSE、WebSocket、图片、usage 与 proxy 热加载。
10. 通过后固定 CPA/sidecar digest，再部署 production compose。

## 5. 无中断或低中断升级

### Sidecar

1. 备份 data-v3 和 key；
2. 拉取候选 digest；
3. 用只读副本数据启动 canary，避免写生产 CPA auth；
4. 跑兼容测试；
5. 滚动替换 sidecar；
6. 检查 /healthz、identity 数、CPA sync 和 proxy mode；
7. 保留旧 digest 以便立即回滚。

### CPA plugin

动态库运行在 CPA 进程内。除非目标 CPA 明确证明支持可靠热加载，否则优先短暂重启 CPA：

1. 把新库写到同目录临时文件；
2. 校验 SHA-256；
3. 原子 rename；
4. 重启 canary CPA并检查 ABI；
5. 生产窗口内重启 CPA；
6. 不修改 sidecar data/key/auth 挂载。

### Management overlay

overlay 与官方 Management Center commit 绑定。每次 CPA 前端升级都要：

1. 更新 pinned upstream commit；
2. 重新应用两个 patch；
3. 运行 TypeScript、lint、locale JSON 和 production build；
4. 确认包含 reset-credit UI 和 Codex quota bridge；
5. 确认不包含旧 public resource route 或任何 Management key 文本；
6. 原子替换宿主 management.html。

## 6. 批量导入操作规程

1. 只在受信任设备打开 /agent-identity/；
2. 使用与 CPA 一致或独立受控的 management password；
3. 粘贴/上传后先 preview；
4. 核对 ready、duplicate、invalid、upstream_unavailable；
5. 保持 atomic=true；
6. 输入未变化时才 commit；
7. 下载脱敏报告；
8. 点击清空敏感输入并关闭标签页；
9. 核对 CPA auth 数与 sidecar summary；
10. 若 rollback_failed，立即人工核对 encrypted store 和 CPA auth 文件，不能重复盲导。

## 7. 多 Team workspace 验收

同一邮箱导入不同 Team workspace 时：

- sidecar identity ID 不同；
- account_id 不同；
- CPA auth filename 的 workspace hash 前缀不同；
- 两个文件都能独立 enable/disable/refresh/delete；
- 选择一个 workspace 发请求时 ChatGPT-Account-ID 正确；
- 删除一个不会删除另一个。

不要在公开日志或 issue 中粘贴完整 account ID、文件内容或真实邮箱。

## 8. Reset credit 验收

- available_count 仍显示全部 banked credits；
- applicable_available_count=0 时按钮禁用或不展示消费动作；
- applicable 大于 0 时才允许用户点击；
- 有详情时按 expiry 排序，DOM 不显示 opaque credit_id；
- 无详情时 consume body 省略 credit_id；
- 正常刷新 usage 不调用 consume；
- 没有 reset 机会时用本地 mock/集成测试验证，不应在真实账号上反复尝试。

## 9. Proxy 热加载验收

按顺序测试：

1. CPA proxy-url 为空，使用 fallback 或 direct；
2. CPA 改为 HTTP proxy，新请求切换；
3. 改为 SOCKS proxy，新请求切换；
4. 清空 CPA proxy-url，恢复 fallback；
5. 已建立 WebSocket 不被强制断开；
6. 日志只显示 mode，不显示用户名、密码或完整 URL。

代理变更无需重启 CPA 或 sidecar。若没有生效，检查 CPA_MANAGEMENT_URL/key、poll interval 和 sidecar 到 CPA Management config 的连通性。

## 10. 日常巡检

- /healthz 返回 200；
- sidecar 使用非 root、read-only、无 capabilities；
- encrypted store 和 secret 权限保持 0700/0600；
- summary 中 unsynced=0；
- CPA auth files 的 base_url 和 cais_ key格式正确；
- proxy reload 无持续错误；
- `/v0/resource/plugins/codex-agent-identity/open` 返回 wrapper，且 wrapper 不包含 secret；
- Management route 无 key 被拒绝；
- dashboard API 无 key 返回 401；
- CPA/sidecar/plugin/overlay 版本与 digest 有记录。

## 11. 常见故障

### failed to synchronize CPA Codex credential

检查 CPA_MANAGEMENT_URL、Management key、auth 目录写权限、CPA auth API响应和同邮箱 workspace 文件名。不要重新导入前先确认 store 是否已写入；原子模式可能已经回滚。

### 同邮箱不同 Team 冲突

确认使用包含 workspace-hash 命名修复的 branch/release，并确认 credential inspection 返回 account_id。不要人工把两个 workspace 改成同一个 auth 文件名。

### 插件-pages 菜单不显示或资源入口返回 404

当前插件不再依赖外挂卡片按钮。确认安装的是包含 ResourceRoute 的插件版本（源码开发线为 v0.3.9；公开 registry 在 v0.3.9 发布前仍可能提供 v0.3.8），CPA 的 `plugins.enabled` 和该插件配置的 `enabled` 都为 `true`，然后重启 CPA。CPA 资源入口是 `/v0/resource/plugins/codex-agent-identity/open`，正常应返回 HTML wrapper；若仍为 404，通常是插件没有注册成功、CPA 使用不支持资源路由的旧版本，或 CPAMC/CPA 仍在使用旧插件进程。直接入口 `/agent-identity/` 仍可作为回退。

### 401

- dashboard API 401：management password 不一致；
- cais_ proxy 401：key 已撤销、auth 文件与 store 不同步；
- Agent Identity upstream 401：最多自动重建 task 一次；
- PAT upstream 401：不会重试，需刷新/重新导入 PAT。

### 无法解密 identity

通常是 encryption key 错误或 data/key 不匹配。立即停止写入，恢复匹配的备份。不要生成新 key 覆盖旧 key，也不要开启 plaintext 试图绕过。

## 12. 备份与恢复

### 备份

1. 记录 CPA/sidecar/plugin digest；
2. 暂停导入和删除操作；
3. 快照 data-v3；
4. 单独备份 data-encryption-key；
5. 备份 CPA auths、config 和 overlay；
6. 对备份加密并限制访问；
7. 定期在隔离环境做恢复演练。

### 恢复

1. 恢复匹配的 data-v3 与 key；
2. 确认 UID/GID 和权限；
3. 先启动 sidecar canary读取；
4. 使用独立 CPA auth 目录执行 reconciliation；
5. 核对 identity 数、kind、workspace 区分和 disabled 状态；
6. 再切回生产 CPA。

## 13. 回滚

- CPA image：恢复上一个官方 digest，保留所有宿主挂载；
- plugin：恢复上一个经过相同 CPA image canary 的动态库；
- sidecar：恢复上一个 digest，保持 data format兼容；
- overlay：恢复对应 CPA Management Center 版本的上一份 management.html；
- 数据：只有确认新版本产生不兼容写入时才恢复 data 快照，恢复前停止 sidecar mutation。

## 14. 发布检查表

- [ ] 根 module 与 nested plugin module 的 gofmt/test/race/vet 通过。
- [ ] Linux amd64/arm64 c-shared build 通过。
- [ ] sidecar build 与 integration tests 通过。
- [ ] overlay pinned build、TypeScript、lint、locale 和安全 marker 通过。
- [ ] `make verify-release-state` 通过，且 registry 没有领先源码版本。
- [ ] registry schema、asset size、SHA-256 正确。发布后再运行 `make verify-published-release`。
- [ ] gitleaks 检查当前树与完整 Git 历史。
- [ ] 搜索真实邮箱、IP、域名、token、Cookie、auth ID 和容器名。
- [ ] canary 使用目标官方 CPA image digest。
- [ ] plugin resource route 返回 wrapper；wrapper 不内置或持久化 Management key/token，不把 secret 放进 iframe URL，并只用 source/origin/nonce 校验的 `postMessage` 复用 CPAMC scoped 登录状态。
- [ ] Management 和 sidecar API 未认证访问被拒绝。
- [ ] Draft PR 更新现有编号，不创建重复 PR。

## 15. 禁止事项

- 不要把原 token、cais_ key、Management key、encryption key 或 proxy password 提交到 GitHub。
- 不要把生产 auth 文件、encrypted data 或生成的 management.html 提交到仓库。
- 不要把凭证列表、预检、导入、启用、停用、刷新、删除等特权操作或 secret 放回 /v0/resource/plugins/...；该路由只允许返回不含凭据和 Management key 的 HTML wrapper。
- 不要为了修权限让 sidecar 以 root 运行或把 secret chmod 777。
- 不要在健康检查或自动任务中调用 reset-credit consume。
- 不要对 PAT 401进行 Agent Identity 重试。
- 不要用同一数据卷和 key同时挂载到可写的生产与 canary sidecar。
