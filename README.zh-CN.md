<div align="center">
  <img src="assets/logo.svg" width="96" alt="CPA Codex Agent Identity 标志">
  <h1>CPA Codex Agent Identity</h1>
  <p><strong>把 Codex PAT 接入 CPA 原生工作流，让账号、代理与升级更可控。</strong></p>
  <p>
    <a href="https://github.com/simplez2/cpa-codex-agent-identity/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/simplez2/cpa-codex-agent-identity/ci.yml?branch=main&amp;style=flat-square&amp;label=CI"></a>
    <a href="https://github.com/simplez2/cpa-codex-agent-identity/releases"><img alt="Release" src="https://img.shields.io/github/v/release/simplez2/cpa-codex-agent-identity?style=flat-square"></a>
    <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/license-MIT-111827?style=flat-square"></a>
    <img alt="CPA ABI" src="https://img.shields.io/badge/CPA-plugin%20ABI%20v1-374151?style=flat-square">
    <img alt="加密" src="https://img.shields.io/badge/store-AES--256--GCM-0f766e?style=flat-square">
  </p>
  <p>简体中文 · <a href="README.md">English</a></p>
</div>

> **推荐版本：[v0.3.18](https://github.com/simplez2/cpa-codex-agent-identity/releases/tag/v0.3.18)** · Linux amd64 / arm64 · [验收证据](docs/releases/v0.3.18.md) · [兼容边界](docs/compatibility.md)
>
> 商店安装的是插件，**不包含 sidecar 的自动部署**。PAT 走 CPA 原生执行；
> Agent Identity JWT 仍需动态签名桥接。本项目独立维护，非 OpenAI / CPA 官方产品。

## 自己的凭证，熟悉的 CPA 操作

不替换 CPA 镜像、不接管已有 OAuth 登录，让自己的 Codex PAT 与 Agent Identity
凭证在 CPA 中更容易导入、管理和升级。

| 你关心的事 | 当前提供的能力 |
| --- | --- |
| 原生管理体验 | PAT 为可持久化原生文件，刷新同步保留启停、代理、备注、优先级与 WS 设置。 |
| 批量导入不混乱 | TXT / JSON / JSONL 先预检、按身份与 Team 区分、去重，默认原子导入并支持失败回滚。 |
| 出口可控 | 遵循当前凭证代理；代理不可达或路由读取失败时停止请求，不悄悄回退直连。 |
| 运维有据可查 | 原生 plugin-pages 入口，插件/sidecar/存储独立，正式资产校验及回滚记录可追溯。 |

**[开始安装](docs/getting-started.md)** · **[兼容性与限制](docs/compatibility.md)** ·
**[获取帮助](SUPPORT.md)** · **[路线图](ROADMAP.md)** · **[参与贡献](CONTRIBUTING.md)**

## 先选你的安装场景

- **已有 CPA / 1Panel：** [增量接入](docs/getting-started.md#existing-cpa)，保留原配置、账号和其他插件。
- **全新 Linux 部署：** [准备 sidecar、密钥与私有网络](docs/getting-started.md#fresh-linux-deployment)，再从商店装插件。
- **已有版本升级：** [核对资产并保留回滚](docs/getting-started.md#upgrade-safety)；不推荐已撤回的 v0.3.17 或未通过验收的 v0.3.16。

本仓库维护的已验证直连安装源：

```text
https://raw.githubusercontent.com/simplez2/cpa-codex-agent-identity/main/registry.json
```

## 原生路径与桥接路径分开说明

```text
导入面板 -> sidecar 加密存储 -> 同步 CPA 认证文件
PAT 模型调用 -> CPA 原生 Codex executor -> 当前凭证代理 -> 上游
JWT 模型调用 -> CPA 插件投影 -> 私有 sidecar 动态 AgentAssertion -> 上游
已有 OAuth -> CPA 原有登录、刷新与执行路径（本插件不接管）
```

PAT 不经 sidecar 模型数据面；JWT 仍需要桥接，不能宣传为两类凭证都完全原生。
sidecar 存储虽然加密，CPA auth 文件的 `access_token` 仍含上游凭证，必须按秘密
数据保护。准确范围见[凭证能力表](docs/compatibility.md#credential-paths)。

## 文档导航

| 场景 | 文档 |
| --- | --- |
| 安装与排障 | [开始使用](docs/getting-started.md) · [支持指南](SUPPORT.md) |
| 当前支持范围 | [兼容性](docs/compatibility.md) · [WebSocket 真实传输](docs/native-websockets.md) |
| 架构与运行 | [Architecture](ARCHITECTURE.md) · [运行逻辑](RUNTIME_LOGIC.zh-CN.md) |
| 运维与安全 | [生产运维](HANDOFF.zh-CN.md) · [Security policy](SECURITY.md) |
| 可选额度面板 | [Management overlay](management-overlay/README.md) |
| 开发与版本 | [贡献指南](CONTRIBUTING.md) · [维护规则](docs/maintenance.md) · [发布流程](RELEASE.md) · [变更日志](CHANGELOG.md) |

## 版本边界

当前源码要求 Go 1.26.6 或更高补丁版本，并以 CLIProxyAPI v7.2.146 SDK 作为当前开发线的编译基线。版本状态由根目录的 VERSION、CHANGELOG.md、发布 workflow 和 registry 分阶段管理：源码可以先进入下一版本，但 registry 不得指向尚未发布的资产。

### 当前 plugin-pages 入口

CPA 的 `/v0/resource/plugins/...` 资源路由不经过 Management key 认证，因为 CPAMC 会在 iframe 中加载它。插件通过 ResourceRoute 注册 `/open`，由 CPA 原生 plugin-pages 菜单显示 Codex Agent Identity。wrapper 不内置、不写入 URL、也不持久化 Management key；它会读取 CPAMC 当前 scoped 加密登录状态，并仅通过同时校验 source、origin 与随机 nonce 的 `postMessage` 把 key 交给同源 sidecar iframe。真正的身份列表、预检、导入、启用、停用和删除仍由 sidecar 的 Bearer 管理认证保护。

不要再把卡片按钮、外挂 overlay 入口和 plugin-pages 菜单混为一谈。当前推荐入口是 CPA 原生 plugin-pages；management-overlay 仅用于 reset-credit 可见性和 Codex 额度 API bridge，不修改插件卡片。

### 关于 CPA 原生 OAuth

插件不接管 CPA 原生 OAuth 登录和刷新。v0.3.18 将 PAT 明确分离为原生 `type: codex` 文件，由 CPA 直接完成模型、额度、代理及 WebSocket 传输，不经 sidecar 数据面。只有仍需 AgentAssertion 的 JWT 文件使用 `type: codex-agent-identity` 加 `auth_mode: agent_identity_sidecar` 的插件分派路径；不能把这一路径说成与原生文件完全等价。

原生 WebSocket 选项只在**客户端也用 WS、且实际选中的凭证开启**时选择上游 WS；普通 HTTP/SSE 请求不会因勾选该项而升级。已完成开/关真实上游传输、连接复用和重启持久化验收，详见[传输语义与同步保护](docs/native-websockets.md)。

## 从 CPAMC Plugin Store 安装

外部商店索引和 GitHub 元数据缓存可能落后于正式发布。为明确安装版本，推荐使用本仓库维护的已校验直连源；无需改动官方商店仓库。

本项目的 `registry.json` 是单独的 CPA schema v2 直接资产清单，固定已校验 `0.3.18` 资产的大小和 SHA-256，不依赖 GitHub Release 元数据查询。可将它作为明确安装源加入 CPA 配置：

~~~yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/simplez2/cpa-codex-agent-identity/main/registry.json"
  configs:
    codex-agent-identity:
      enabled: true
      priority: 1000
~~~

全新安装通常不需要填写 `sidecar_url` 或 `sidecar_api_url`。插件管理页默认使用与 CPA 同源的 `/agent-identity/`，远程部署不会再把浏览器请求错误地指向浏览器自己的 `127.0.0.1`。Docker 部署的 quota/reset bridge 会通过 `CODEX_AGENT_IDENTITY_SIDECAR_HOSTS` 和端口环境变量自动发现。旧版本配置仍会被兼容解析，但这些内部地址不再作为普通 Plugin Store 配置项展示。
如果 CPA 与 sidecar 通过反向代理发布，可在旧配置中保留 `sidecar_url: "/agent-identity/"`；它只能是无凭据、无查询参数和无片段的 HTTP(S) 地址或同源路径。

容器内 CPA 若要通过 Plugin Store 安装或升级，插件目录需要在该操作期间可写，完成后建议恢复只读挂载。

~~~yaml
volumes:
  # Plugin Store 安装或升级期间需要可写挂载。
  - ./runtime/cpa-plugins:/CLIProxyAPI/plugins:rw
~~~

安装或升级完成后，重新创建 CPA 并恢复只读挂载：

~~~yaml
volumes:
  - ./runtime/cpa-plugins:/CLIProxyAPI/plugins:ro
~~~

也可以从 GitHub Release 下载与架构对应的 zip。每个 zip 根目录都包含 `codex-agent-identity.so`，并由 `checksums.txt` 提供 SHA-256 校验。

注册表使用 CPA schema v2 的 direct 资产模式，并固定文件大小和 SHA-256。因此安装不依赖服务器的 GitHub REST API 匿名额度。

不要同时加载旧的 `codex-agent-identity-auth.so` 和新的 `codex-agent-identity.so`，两者都会声明 Codex 凭证解析能力。

插件商店只会安装 `.so`，无法安全地自动创建 sidecar 容器、Docker network、加密密钥、management key 和持久化目录。全新部署建议先运行 `sh deploy/bootstrap-runtime.sh --start`，之后在 CPA 插件商店点击安装即可。

Docker 部署中，`sidecar_api_url` 留空时插件会自动读取 `CODEX_AGENT_IDENTITY_SIDECAR_HOSTS`，并默认使用 sidecar 容器的 `8787` 端口。直接宿主机安装可以在旧配置中显式保留 `http://127.0.0.1:18787/agent-identity/`；这是兼容项，新安装不需要填写。
## 部署 sidecar

全新 checkout 推荐使用 bootstrap helper。它会创建 runtime 目录和两份独立密钥，生成已启用插件的 CPA 配置、随机 CPA API key 和外部 Docker network，并可直接启动官方 CPA 与 sidecar：

~~~bash
sudo sh deploy/bootstrap-runtime.sh --sidecar-url /agent-identity/ --start
~~~

远程浏览器应像上面一样显式选择同源路径，并单独配置反向代理。bootstrap helper 本身默认使用 loopback 地址，仅适用于浏览器就在同一宿主机的情况：

~~~bash
sudo sh deploy/bootstrap-runtime.sh --sidecar-url http://127.0.0.1:18787/agent-identity/ --start
~~~

已有部署不要覆盖原 config 和 .env；请手动合并同样的设置。参考 deploy/docker-compose.production.yml 时，要显式指定 project directory，确保根目录的 config、auth、logs 和 runtime 路径解析正确：

~~~bash
sudo sh deploy/init-runtime.sh ./runtime
[ -e .env ] || cp .env.example .env
docker network inspect agent-identity >/dev/null 2>&1 || docker network create agent-identity
docker compose --project-directory . --env-file .env -f deploy/docker-compose.production.yml up -d
~~~

CPA remote-management 密码与 sidecar 的 management-key 应保持一致，这样
sidecar 才能自动创建、停用、刷新和删除 CPA 原生 Codex auth 文件。
初始化脚本会把 data-v3 和 secrets 设置为镜像内非特权 UID/GID 65532 所有；
如果修改 SIDECAR_UID 或 SIDECAR_GID，运行脚本时必须使用相同的值。

建议通过与 CPA 相同的 TLS 反向代理发布 sidecar UI：

~~~nginx
location ^~ /agent-identity/ {
    proxy_pass http://127.0.0.1:18787;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    client_max_body_size 5m;
}
~~~

如果确实需要跨来源嵌入，必须把受信任页面的完整 origin 加入
EMBED_ALLOWED_ORIGINS。sidecar 管理页只把管理密码保存在当前标签页的 sessionStorage；CPAMC 自己可能使用 scoped 混淆 localStorage 保存登录状态。
wrapper 仅复用当前选中的 scope，并通过校验 source、origin 与随机 nonce 的 `postMessage` 转交；不会把 key 写入 iframe URL、Cookie、导出文件或 sidecar localStorage。

插件解析器默认允许容器服务名以及 `localhost`、`127.0.0.1`、`::1`。HTTP 默认只允许 8787，loopback 额外允许宿主机映射端口 18787；其他明确需要的 HTTP 端口必须通过 `CODEX_AGENT_IDENTITY_SIDECAR_HTTP_PORTS` 逐项加入。

CPA 选择 `gpt-image-1.5` 或 `gpt-image-2` 的 direct image 路径时，sidecar 会转回 Codex Responses image tool，并保留 JSON/multipart 编辑、`response_format=url`、partial/completed SSE 以及 Agent Identity 401 重新注册重试。

## 批量导入格式

逐行 TXT：

~~~text
at-first-token
at-second-token
header.payload.signature
~~~

JSON：

~~~json
[
  {"token": "...", "label": "account-a"},
  {"codex_access_token": "..."},
  "at-another-token"
]
~~~

JSONL：

~~~jsonl
{"token":"...","label":"account-a"}
{"access_token":"...","name":"account-b"}
~~~

状态包括 ready、imported、duplicate、invalid、upstream_unavailable、failed、
rolled_back、rollback_failed 和 aborted。

## 1Panel 与官方 CPA 升级

配置、auth、日志、插件、sidecar 数据和密钥全部放在宿主机持久目录，不写进
CPA 容器层。以后升级时只修改 CPA_IMAGE 为经过 canary 的官方镜像或 digest，
无需维护自定义 CPA 镜像，也不会因为 1Panel 重建容器而丢失插件和凭证。

生产建议流程：拉取候选官方镜像、使用独立端口和独立数据目录加载 .so、验证
旧公开资源 404、受保护路由无 key 401、直接管理页面、批量预检、auth 同步、
HTTP/SSE/WebSocket/图片/额度/代理，再固定镜像
digest 并替换生产。不要直接用 latest 覆盖正在工作的实例。

## 构建与发布

版本流程和发布边界记录在 [CHANGELOG.md](CHANGELOG.md)；`VERSION` 是源码开发线版本，`registry.json` 是已发布可安装版本。不要在 Release 资产尚未生成和校验前修改 registry。推荐先运行 `go run ./.github/scripts/verify-release-state.go`，再执行构建、测试、打 tag、等待 Release，最后单独更新 registry。

~~~bash
make test
make race
make vet
make build
make package-plugin-portable GOOS=linux GOARCH=amd64
~~~

vX.Y.Z 标签会生成 Linux amd64/arm64 插件 zip、sidecar tar.gz、
checksums.txt、GitHub Release，以及 GHCR 的多架构 sidecar 镜像。

## 安全说明

- 原始凭证在 sidecar store 中使用 AES-256-GCM 加密；为兼容 Keeper 原生链路，同一上游凭证也会写入 CPA auth 文件的 `access_token`，CPA auth 目录必须按秘密数据保护。
- .so 是 CPA 进程内受信任代码，安装前必须校验发布哈希。
- 插件资源入口不得内置或持久化 token、Management key 或特权 API；只允许通过 source/origin/nonce 校验的 `postMessage` 复用 CPAMC 当前 scoped 登录状态，
  `/v0/resource/plugins/...` 和 iframe URL 都不得携带 secret，真正的身份操作必须继续由 sidecar Bearer 认证。
- 不要在 issue、日志、截图或导出中提交 token、管理密码、Cookie、代理密码、
  cais_ 密钥或 auth 文件。
- ALLOW_PLAINTEXT_STORE 和 ALLOW_INSECURE_UPSTREAM 仅用于本地测试。
- reset-credit consume 路径可能消耗额度，健康检查、启动和预检绝不会调用它。

本项目使用 MIT License，是独立集成项目，不是 OpenAI 官方产品。
