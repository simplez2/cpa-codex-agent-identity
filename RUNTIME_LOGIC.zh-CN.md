# CPA Codex Agent Identity 运行逻辑与安全边界

本文按当前源码说明插件、sidecar、CPA auth 文件、批量导入、代理热加载和 reset-credit 的真实运行路径。当前源码、已发布 registry、可直接安装资产和 sidecar 镜像均为 v0.3.13，v0.3.13 Release 资产的大小、SHA-256、归档根目录和目标架构均已校验。
Release baseline: CLIProxyAPI v7.2.146；v0.3.13 的 registry、Release 资产和 sidecar 镜像均已完成校验。

## 1. 三个可独立替换的平面

~~~mermaid
flowchart LR
    U[Codex client] --> CPA[Stock CLIProxyAPI]
    P[CPA dynamic plugin] --> CPA
    CPA -->|Bearer cais_random| D[Sidecar data plane]
    D --> O[Fixed Codex upstream]
    B[Browser] --> M[Sidecar management plane]
    M --> S[(Encrypted identity store)]
    M -->|Management auth-file API| CPA
~~~

### CPA plugin control plane

- 注册私有的 `codex-agent-identity` AuthProvider，不声明 native `codex`；
- 只识别 `type: codex-agent-identity` 且带有 `auth_mode: agent_identity_sidecar` 的 sidecar auth 文件；
- 保留真实上游凭证为 CPA metadata 的 `access_token`，通过标准静态 `header:Authorization` attribute 把独立随机 `sidecar_client_key` 仅用于访问内部 sidecar，且不设置 `api_key`，最后返回 `Provider: codex`；
- 注册受 CPA Management key 保护的 GET /v0/management/codex-agent-identity/open；
- 注册 `/v0/resource/plugins/codex-agent-identity/open` 作为 CPAMC plugin-pages 的安全 wrapper；不内置 secret，仅通过 source、origin 与 nonce 校验的 `postMessage` 复用 CPAMC 当前 scoped 登录状态。

### Sidecar management plane

- 验证单条或批量 Agent Identity/PAT；
- 加密保存原始凭据；
- 通过 CPA Management API 增删改 native auth 文件；
- 提供 /agent-identity/ UI 和受 Bearer 保护的 identity API；
- 处理启用、停用、刷新同步、删除、预检、导入和回滚。

### Sidecar data plane

- 根据 cais_ key 找到一个加密 identity；
- 解密只在内存中进行；
- Agent Identity JWT 生成 AgentAssertion；
- PAT 使用原 token 作为 Bearer；
- 代理 HTTP、SSE、WebSocket、图片与配额请求到固定上游。

## 2. 凭据导入生命周期

### 2.1 输入与限制

支持纯文本、TXT、JSON 和 JSONL。请求体最大 4 MiB，每批最多 200 条，验证使用有界 worker 并发。输入可包含 token 字符串或 token/access_token/codex_access_token 等兼容字段。

### 2.2 预检

预检执行：

1. 解析格式和条目数量；
2. 对同一批次 token hash 去重；
3. 对已加密存储的 token hash 去重；
4. 调用对应验证端点确认凭据类型和非敏感元数据；
5. 返回 ready、duplicate、invalid 或 upstream_unavailable；
6. 不写磁盘、不写 CPA auth 文件、不返回原 token。

### 2.3 提交和原子回滚

默认 atomic=true：

- 任一条预检失败时，所有 ready 条目变为 aborted；
- 提交过程中失败时，按逆序删除此前已经写入的 CPA auth 文件和 encrypted identity；
- 回滚成功标记 rolled_back；
- 删除 CPA 文件或 encrypted store 失败会明确标记 rollback_failed，要求人工处理；
- 非原子模式允许成功项保留，但报告 partial_failure。

前端要求预检后才能提交，并在输入改变时让旧预检失效，避免“预检 A、提交 B”。

## 3. 加密存储

Store 使用 AES-256-GCM：

- DATA_ENCRYPTION_KEY_FILE 提供与数据卷分离的 32-byte key；
- 每条 token 使用独立 nonce；
- 目录以 0700、文件以 0600 创建；
- 写入临时文件、fsync 后原子 rename；
- persisted JSON 保存 ciphertext、nonce、client key hash 和非敏感元数据；
- list API 不返回 token、明文 client key 或内部 hash。

ALLOW_PLAINTEXT_STORE=true 只用于本地迁移/测试，生产必须关闭。加密保护离线卷、备份和误传；若主机同时泄露 key 和运行进程，则不在其威胁模型内。

## 4. CPA auth 文件同步

每个已导入 identity 在 CPA 中对应一个原生 Codex auth 文件，核心字段包括：

~~~json
{
  "type": "codex-agent-identity",
  "auth_mode": "agent_identity_sidecar",
  "access_token": "<upstream-token>",
  "sidecar_client_key": "cais_<random>",
  "base_url": "<internal-sidecar-url>",
  "agent_identity_id": "agent-<id>",
  "disabled": false
}
~~~

CPA 文件按原生 OAuth 的秘密语义保存真实上游凭证，同时保存独立的 sidecar key。同步逻辑先读取已存在文件，在写入或字段验证失败时恢复原内容；启动 reconciliation 会自动把旧的 `access_token=cais_...` 文件迁移成双字段格式。刷新 identity 时保留 disabled 状态。
The plugin parser only claims `type=codex-agent-identity` files carrying `auth_mode=agent_identity_sidecar`; it preserves metadata `access_token`, maps `sidecar_client_key` to the static `header:Authorization` attribute without setting `api_key`, and returns `Provider: codex` for the runtime. Ordinary `type=codex` OAuth files remain on CPA native parse/login/refresh/executor paths.

### 同邮箱多个 Team workspace

只用 email 和 plan type 会让同一登录邮箱的多个 Team workspace 文件名冲突。当前 PR 在存在 account_id 时计算 SHA-256，并取前 8 个十六进制字符作为不直接暴露完整 account ID 的稳定短摘要：

~~~text
codex-<workspace-hash>-<sanitized-email>-<plan>-agent-identity.json
~~~

这既保持同 workspace 文件名稳定，又避免公开完整 account ID。缺少 account_id 的旧 identity 保持兼容命名。

## 5. Agent Identity 请求路径

1. CPA 选择并解析 sidecar-owned auth 文件；
2. CPA 将 cais_ key 发给 sidecar；
3. sidecar 常数时间匹配 client-key hash；
4. 解密 JWT，验证 JWKS、issuer/audience/expiry 和 claims；
5. 以 identity、token hash、session 组合查找 task cache；
6. 同一 task key 的并发注册通过 single-flight 合并；
7. 为每个上游请求生成新的 AgentAssertion；
8. 固定上游收到 AgentAssertion，而不是原始 JWT。

若 Agent Identity 上游返回 401，且请求体可安全重放，sidecar 会使 task cache 失效、重新注册并最多重试一次。它不会无限重试。

## 6. PAT 请求路径

1. 导入时通过 PAT whoami/验证接口确认凭据；
2. CPA auth 文件同时保存 PAT 于 `access_token` 和独立的 `sidecar_client_key`；模型请求只使用后者调用 sidecar；
3. sidecar 解密 PAT 并以 Authorization: Bearer 转发；
4. 保留需要的 ChatGPT-Account-ID 等安全路由元数据；
5. PAT 401 直接返回，不进入无效的 Agent Identity task 重建流程。

Keeper 等客户端调用 stock CPA `/v0/management/api-call` 时，CPA 从 metadata `access_token` 替换 `$TOKEN$`，因此 PAT 额度请求使用真实 Bearer token；Codex executor 先按 OAuth 语义读取该 metadata token，再由 CPA 标准静态 `header:Authorization` attribute 把仅发往 sidecar 的 Authorization 覆盖为 `Bearer cais_...`。由于 attributes 中不存在 `api_key`，CPA 原生 Codex Header Defaults、WebSocket `x-codex-beta-features` 和 `identity-confuse` 路径不会被 API-key 分类短路。原版 OAuth auth 文件不属于 sidecar-managed identity，不会被插件误接管。

## 7. Management 与浏览器边界

### 受保护的插件路由

GET /v0/management/codex-agent-identity/open 由 CPA Management key 保护，返回一个带 CSP、X-Frame-Options、no-store 和 no-referrer 的包装页面。sidecar_url 必须是无 credentials/query/fragment 的 http(s) URL或根路径。

### 独立 sidecar UI

/agent-identity/ 静态页面本身可以通过反代访问，但以下 API 都要求 Bearer management key：
默认浏览器 URL 是与 CPA 同源的 `/agent-identity/`；空值和 `/` 会规范化为该路径。像 `http://127.0.0.1:18787/agent-identity/` 这样的历史绝对本地 URL，在显式配置时仍会被接受。

- identities list；
- single import；
- batch preview/commit；
- enable/disable/refresh；
- delete。

sidecar 管理页只把管理密码保存在当前标签页的 `sessionStorage`。CPAMC 自己可能使用 scoped 混淆 `localStorage` 保存登录状态；wrapper 只读取当前选中的 scope，并通过带随机 nonce 的同源 `postMessage` 转交，不写入 iframe URL、Cookie、导出文件或 sidecar `localStorage`。页面用 DOM text node 渲染不可信内容，不把 token 或 opaque credit ID 插入 DOM。

### plugin-pages 如何加载

CPA 当前的 `/v0/resource/plugins/...` 路由族不经过 Management key 认证，但 CPAMC 会把它作为插件页面 iframe 的资源入口。当前插件注册 `/v0/resource/plugins/codex-agent-identity/open`，wrapper 不内置 secret，而是按 CPAMC 当前 `selection -> scope -> state.managementKey` scoped storage 结构读取登录状态，再通过校验 source、origin 与 nonce 的 `postMessage` 交给同源 `/agent-identity/` iframe；Management key 不进入 URL。

资源入口不等于身份操作免认证：sidecar 的列表、预检、导入、启用、停用、刷新和删除仍必须通过自己的 Bearer 管理密码。若 CPAMC 仍不显示菜单，先确认 CPA 与插件都启用并重启 CPA；旧版 CPAMC 可直接使用 `/agent-identity/`。overlay 只提供 quota/reset-credit 兼容层，不负责创建插件入口。
## 8. Proxy 热加载

sidecar 启动时先读取 CPA 当前 proxy-url，再开始 identity inspection。之后按 CPA_PROXY_CONFIG_POLL_INTERVAL 轮询：

1. CPA 非空 proxy-url 优先；
2. CPA 清空时恢复 OUTBOUND_PROXY_FILE/OUTBOUND_PROXY fallback；
3. direct/none 切换为直连；
4. 支持 HTTP、HTTPS 和 SOCKS URL；
5. 新值先构造一个完整 http.Transport；
6. 原子 swap 后关闭旧 transport 的 idle connections。

已经在途的 HTTP 请求和 WebSocket 不被强切，新请求立即使用新路由。日志只记录模式，不记录含密码的 proxy URL。

## 9. Usage 与 reset-credit

quota compatibility policy 只允许明确的方法和固定路径。reset-credit consume 是可能花费权益的 POST 操作，只有用户在 CPA 管理界面明确点击时才应发生。

- available_count 表示银行中总数；
- applicable_available_count 存在时，是按钮能否使用的权威值；
- 详细 credit row 可按 expiry 选择，但 opaque credit_id 不渲染进 DOM；
- 没有详情时省略 credit_id，让上游选择下一个 applicable credit；
- 不从 monthly quota reset 猜测 credit expiry；
- healthcheck、startup、reconcile、preview 和普通 quota GET 不消费 credit。

## 10. 启动和 reconciliation

启动顺序：

1. 读取 management、encryption、CPA Management 和可选 proxy secret；
2. 验证固定 upstream origin；
3. 加载当前 CPA proxy；
4. 打开并解密 owner-only store；
5. 创建 CPA Manager 与 sidecar server；
6. 对每个 stored identity 重新 inspect 非敏感元数据；
7. Upsert native CPA auth 文件，保留 disabled 状态；
8. 启动 proxy Watch 和 HTTP server。

单个 identity reconcile 失败只记录脱敏 identity ID，不打印 token 或上游正文；其它 identity 继续处理。

## 11. 关键不变量

- CPA auth 文件中的 `access_token` 必须保存真实上游凭证，`sidecar_client_key` 必须保存独立 `cais_`；两者用途不得互换。
- sidecar auth 文件中的 cais_ key 必须随机、可撤销且不能公开。
- 同邮箱不同 Team workspace 不得覆盖同一个 CPA auth 文件。
- PAT 401 不进行 Agent Identity 重试。
- Agent Identity 401 最多重建 task 并重试一次。
- 导入报告和 UI 不回显 secret。
- proxy 变化对新请求热生效，不需要重启 CPA 或 sidecar。
- resource wrapper 保持无 secret、无宿主回调；敏感操作仍在 sidecar Bearer 认证之后执行。
- reset-credit consume 不得被任何自动探针触发。
