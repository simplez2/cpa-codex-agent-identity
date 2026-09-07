# CPA 原生 WebSocket 开关：语义、验收与发布边界

## 开关实际控制什么

对原生文件型 PAT（`type: codex`、运行时 `runtime_only=false`），CPA 自己
选择传输；Agent 不应另造开关、强制升级 HTTP，也不应覆盖用户的认证文件设置。

在已验收的官方 CPA **v7.2.152** 中：

| 客户端到 CPA | 所选凭证 `websockets` | CPA 到 OpenAI |
| --- | --- | --- |
| HTTP / SSE | 开或关 | HTTP / SSE |
| WebSocket | `false` | HTTP / SSE，再由 CPA 转回下游 WS 帧 |
| WebSocket | `true` | 尝试原生 WebSocket |

因此，“浏览器看到 `101`”只证明下游升级；客户端仍用 HTTP 时，勾选此项
不会自动变成端到端 WS。认证列表开启也不等于每次都选中该凭证。
升级失败的处理取决于 CPA 版本、执行生命周期及接口；不能承诺所有 WS
错误都自动回退 HTTP，更不能通过绕过账号代理来使升级成功。

上游连接复用通常发生在**同一个客户端 WS 会话**中，而不是所有请求共用
一条连接。修改代理后应新建 WS 会话验证新出口，不能用已有连接证明
新代理生效。WebSocket 也不保证首字一定比 HTTP 快。

## 2026-09-08 实际验收

使用同主机隔离容器，官方 `eceasy/cli-proxy-api:v7.2.152` 与现有 v0.3.15
插件。复制一份已启用、未设置账号代理的 PAT；不修改生产凭证或配置。
测试模型为 `gpt-5.6-luna`，每次要求简短回复。**未覆盖真实代理或 JWT 路径。**

仅临时容器开启请求日志；完整日志留在所有者私有临时目录中，仅输出
白名单计数、布尔值及耗时。测试完成后删除临时容器、凭证副本和日志。

| 验收 | 上游证据 | 结果 |
| --- | --- | --- |
| 原生字段 PATCH 关闭，重启临时 CPA，再发 WS 请求 | HTTP POST 1 次，WS 请求/握手 0 次 | 有文本且 `response.completed` |
| 原生字段 PATCH 开启，重启临时 CPA，同一 WS 会话连续两次请求 | WS 请求 2 次、上游 101 握手 1 次、响应帧 24 个，HTTP POST 0 次 | 两次都有文本且完成，复用成功、没有 HTTP 回退 |
| 开关开启，客户端发普通 HTTP 流请求 | HTTP POST 1 次，WS 请求 0 次 | HTTP 200，文本且完成 |
| 生产默认路由只读复验 | 新下游会话 1 个，其会话 ID 匹配官方上游 WS connected 日志 | 101，文本且完成，首字 1.530 秒、完整流 1.596 秒 |

上游目标确认是 `wss://chatgpt.com/backend-api/codex/responses`，不是内网
sidecar。生产账号、Team、代理、启用状态均未改动；临时容器剩余 0 个。
耗时为服务器侧探测，不含用户到服务器的网络往返，不能当作性能基准。

### 复验时应记录的证据

1. 先确认实际镜像、文件型 `provider=codex` / `runtime_only=false`、文件和
   运行时的 `websockets` 值。不要输出原始 auth 文件。
2. 在独立 auth/config/log 目录及临时容器里测试 off/on，**不要切换生产
   账号开关来做试验**。只复制目标凭证，不改它的当前代理来追求成功。
3. 关闭会话后，只统计 `api.websocket.request`、`api.websocket.handshake`
   的状态 101、`api.websocket.response` 以及 `HTTP Method: POST`；确认
   官方上游 URL、输出及 `response.completed`。下游 101 不能代替这些证据。
4. 启用/禁用两种值均经原生 PATCH、落盘和重启再测；再做普通 HTTP 对照。
5. 不在生产打开全量敏感请求日志。测试结束清理独立容器和私有临时目录，
   验证生产设置没有变化。

## 同步保护：源码修复不等于已部署

**当前生产仍为已发布 v0.3.15 资产，没有新建版本、覆盖旧 tag 或发布资产。**

- v0.3.15 的 Agent 同步器每次生成 `websockets=true`，未保留用户设置。
  在该版本源码上运行相同 Upsert 回归用例，`false` 被覆盖的问题已复现。
  原生 CPA PATCH/重启本身能正确保存，问题在后续 **Agent 刷新/重新同步**，
  不应修改 CPA 或 Keeper 来补偿。
- 未发布的主线修复已让 CPA 文件设置优先于导入默认值，保留显式 `false`。
  本次新增完整管理 API 同步回归：首次导入、新 Manager 重读文件、两次
  token 刷新、开/关及布尔字符串、禁用状态/代理/备注保留、重复同步不重写。
- 本次进一步收紧持久化验证：缺失、null、非法布尔值不能被误报为同步成功；
  按 CPA Codex 执行器接受 bool / `strconv.ParseBool` 字符串，不再把数字或
  `yes` 等宽松旧值视为原生开启。七个验证反例在修复前失败，修复后通过。
- 根模块和插件模块的 `go test -race ./... -count=1`、`go vet ./...` 均通过。

冻结版本期间，上述源码增强**不应被描述为已经部署到 v0.3.15**。最终发布
仍需固定新资产身份，完成候选容器的同步+传输联合验收，再按发布流程上线。
此页不能据此宣称 Agent Identity JWT/AgentAssertion 或所有未来 CPA 功能
已经获得完整原生兼容。
