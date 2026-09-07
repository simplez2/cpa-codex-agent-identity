# PAT 原生认证回档记录

## 根因

v0.3.17 将 PAT 文件改为 `type: codex-agent-identity`，触发插件后返回
`Provider: codex`，但同时设置了 `runtime_only=true` 和私有 sidecar
`base_url`。这不是 CPA 的原生文件凭据：

1. CPAMC 的认证卡片根据 `runtime_only` 隐藏开启/关闭、下载、设置等控件。
2. CPA 的运行时凭据不会将原生状态/字段 PATCH 保存为普通文件变更。
3. 账号 `proxy_url` 被原样复制到运行时，CPA 因而先通过外部 SOCKS
   连接内网 sidecar。日志中 60 秒后的 499 是客户端/前置入口超时取消，
   不能将其归因为 OpenAI 模型过载。
4. Native management api-call 同样依赖该账号代理；简单清空运行时代理
   会让额度查询绕过代理，因此不采用这种修补方式。

PAT 是可被 CPA 原生执行器直接使用的 Bearer 凭据，不需要每次经
sidecar 签名。它应与需要 AgentAssertion 的 Agent Identity JWT 分开处理。

## 已执行回档

- 恢复已有、校验过的 v0.3.15 插件和 sidecar 镜像，不覆盖版本资产。
- 五个 PAT 的存储类型恢复为 `codex`，运行时均为 `runtime_only=false`。
- 保留当前凭据、Team、代理、启用状态、优先级和自定义头，不使用旧账号
  备份覆盖当前用户设置。回档前留存独立、仅所有者可读的备份。
- 不更改 CPA/Keeper 的代码、镜像、冷却配置或账号路由策略。

## 已完成验收与边界

同主机、同官方 CPA v7.2.152 镜像的临时隔离容器：

| 验收 | 结果 |
| --- | --- |
| 原生 `provider=codex`, `runtime_only=false`, `source=file` | 通过 |
| 关闭后落盘、重启仍关闭 | 通过 |
| 开启后落盘、重启仍开启 | 通过 |
| 原生设置接口保存代理 | 通过 |
| 拒绝连接的 SOCKS 代理下模型请求 | HTTP 500，立即失败 |
| 同一坏代理下原生额度请求 | HTTP 502 |
| 无代理 PAT 原生额度请求 | HTTP 200 |
| 无代理 PAT 原生 luna 流式请求 | HTTP 200，文本、finish、[DONE] 齐全，1.43 秒 |

线上回档后的独立只读复验：五个账号均为普通文件凭据、sidecar/Keeper
健康、插件页面 HTTP 200、目标 PAT 的原生额度查询 HTTP 200。
未在生产上切换用户账号的开关来做测试。

三个用户现有代理的 TCP 建连均在 3 秒探测窗口内超时。未清空、绕过或
替换这些代理；默认路由仍可能选中它们而失败。这不是“所有线上请求已通”
的验收。真实可用代理的端到端矩阵、AgentAssertion 的原生控件/代理支持
仍需单独完成，不能由上述无代理成功外推。

## 尚未发布的修复

- PAT 导入明确生成原生文件，不再依赖旧版本偶然绕过插件的行为。
- 原生 PAT 文件不写入 sidecar `base_url`、`runtime_only` 或残留 `expired`。
- 同步保留用户的 `websockets=false`，避免刷新凭据覆盖原生设置。
- 原生 PAT 保持在 CPA 内建解析/执行路径；JWT 的动态签名仍使用 sidecar。
- 版本暂不递增。当前 source baseline 不代表线上部署这些尚未发布的改动。
