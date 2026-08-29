<div align="center">
  <img src="assets/logo.svg" width="96" alt="CPA Codex Agent Identity 标志">
  <h1>CPA Codex Agent Identity</h1>
  <p><strong>为原版 CLIProxyAPI 提供加密的 Agent Identity 与 PAT 管理、原生 auth 文件接入和可靠 sidecar 数据面。</strong></p>
  <p>
    <a href="https://github.com/simplez2/cpa-codex-agent-identity/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/simplez2/cpa-codex-agent-identity/ci.yml?branch=main&amp;style=flat-square&amp;label=CI"></a>
    <a href="https://github.com/simplez2/cpa-codex-agent-identity/releases"><img alt="Release" src="https://img.shields.io/github/v/release/simplez2/cpa-codex-agent-identity?style=flat-square"></a>
    <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/license-MIT-111827?style=flat-square"></a>
    <img alt="CPA ABI" src="https://img.shields.io/badge/CPA-plugin%20ABI%20v1-374151?style=flat-square">
    <img alt="加密" src="https://img.shields.io/badge/store-AES--256--GCM-0f766e?style=flat-square">
  </p>
  <p>简体中文 · <a href="README.md">English</a></p>
</div>

> **版本边界：** 当前源码开发线为 **v0.3.8**，编译基线为 CLIProxyAPI **v7.2.145**。截至目前（2026 年 8 月 29 日），公开 registry 与可直接安装的发布资产仍为 **v0.3.7**。只有在 v0.3.8 完成测试、打包、Release、校验值和 registry 更新后，插件商店才会切换到 v0.3.8。

这是一个面向 CLIProxyAPI（CPA）的 Codex Agent Identity / Personal Access
Token 集成项目。首个公开版本由两个部分组成：

- codex-agent-identity.so：CPA 动态插件只声明私有的 `codex-agent-identity`
  auth-file provider；它只接管带有 `auth_mode: agent_identity_sidecar` 的 sidecar
  文件，然后把解析结果映射到 CPA 原生 `codex` runtime executor。原生 `type: codex`
  OAuth 的解析、登录、刷新和执行路径仍由 CPA 自己处理。插件同时暴露受
  Management key 保护的管理路由，以及供 CPAMC `plugin-pages` 使用的安全资源入口。
- sidecar：负责官方凭证验证、AES-256-GCM 加密存储、AgentAssertion、PAT
  转发、批量导入、CPA auth 文件同步以及 HTTP/SOCKS 代理热加载。

CPA 只会看到随机生成的 cais_ 客户端密钥，不会保存原始 Agent Identity
JWT 或 PAT。auth 文件使用 `type: codex-agent-identity` 触发插件解析，解析后
runtime provider 仍为 `codex`，因此会进入 CPA 原生 Codex executor。现有官方 OAuth 和第三方 API 渠道不由本插件接管。
## 文档导航

- [运行逻辑与安全边界](RUNTIME_LOGIC.zh-CN.md)
- [生产交接与运维手册](HANDOFF.zh-CN.md)
- [Architecture](ARCHITECTURE.md)
- [Security policy](SECURITY.md)
- [Management Center overlay](management-overlay/README.md)
- [发布与版本管理](RELEASE.md)

## 主要能力

- 通过 CPA 插件资源路由提供 CPAMC `plugin-pages` 的安全 HTML 包装器；动态身份操作仍由 sidecar 自己认证。
- 管理面板直接使用 `/agent-identity/`；所有身份操作仍必须验证管理密码。
- 支持 Agent Identity JWT 和当前以 at- 开头的 Personal Access Token。
- 支持粘贴或上传 TXT、JSON、JSONL，单批最多 200 条、4 MiB。
- 强制先预检后导入；预检验证官方信息，但不会写入磁盘或 CPA。
- 对本批输入和已导入凭证去重。
- 默认原子导入，失败时自动回滚并明确显示回滚失败项。
- 导出脱敏 JSON / CSV 结果，不回显原始 token。
- 支持启用、停用、刷新同步和删除凭证。
- 显示总数、启用、停用、Agent Identity、PAT、未同步统计。
- 兼容 HTTP、SSE、WebSocket、图片、额度和 reset-credit 路径。
- CPA 的全局 HTTP、HTTPS、SOCKS 代理变更可对新请求热生效。

## 版本边界

当前源码要求 Go 1.26.6 或更高补丁版本，并以 CLIProxyAPI v7.2.145 SDK 作为当前开发线的编译基线。版本状态由根目录的 VERSION、CHANGELOG.md、发布 workflow 和 registry 分阶段管理：源码可以先进入下一版本，但 registry 不得指向尚未发布的资产。

### 当前 plugin-pages 入口

CPA 的 /v0/resource/plugins/... 资源路由不经过 Management key 认证，因为 CPAMC 会在 iframe 中加载它。插件通过 ResourceRoute 注册 /open，由 CPA 原生 plugin-pages 菜单显示 Codex Agent Identity。这个 wrapper 不包含 Management key、token 或宿主回调，只负责嵌入 sidecar 页面；真正的身份列表、预检、导入、启用、停用和删除仍由 sidecar 的 Bearer 管理认证保护。

不要再把卡片按钮、外挂 overlay 入口和 plugin-pages 菜单混为一谈。当前推荐入口是 CPA 原生 plugin-pages；management-overlay 仅用于 reset-credit 可见性和 Codex 额度 API bridge，不修改插件卡片。

### 关于 CPA 原生 OAuth

插件保留 CPA 原生 Codex OAuth 文件的解析、刷新和执行归属，不冒充 CPA 的 codex OAuth provider。Agent Identity / PAT 文件使用独立的 codex-agent-identity 类型触发插件解析，然后映射到 CPA 的 Codex runtime executor。这样可避免接管现有官方 OAuth 账号；如果要让导入流程直接复用 CPA OAuth 页面，还需要 CPA host 提供“源 provider 与 runtime provider 分离”的登录保存契约，当前版本不伪造这一流程。

## 从 CPAMC Plugin Store 安装

官方 CPA Plugin Store 在 registry 更新后会收录已发布版本。若目标 CPA 版本或商店缓存尚未显示，可把本仓库的 registry.json 加入宿主机挂载的 CPA 配置作为明确回退：

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

全新安装通常不需要填写 sidecar_url 或 sidecar_api_url。插件默认使用本机 sidecar 地址；Docker 部署通过 CODEX_AGENT_IDENTITY_SIDECAR_HOSTS 和端口环境变量自动发现。旧版本配置仍会被兼容解析，但这些内部地址不再作为普通 Plugin Store 配置项展示。

如果 CPA 与 sidecar 通过反向代理发布，可在旧配置中保留 sidecar_url: "/agent-identity/"；它只能是无凭据、无查询参数和无片段的 HTTP(S) 地址或同源路径。

容器内 CPA 若要通过 Plugin Store 安装或升级，插件目录需要在该操作期间可写。
完成后建议恢复只读挂载。正常运行时推荐：

~~~yaml
volumes:
  - ./runtime/cpa-plugins:/CLIProxyAPI/plugins:ro
~~~

也可以从 GitHub Release 下载与架构对应的 zip。每个 zip 根目录都包含
codex-agent-identity.so，并由 checksums.txt 提供 SHA-256 校验。

注册表使用 CPA schema v2 的 direct 资产模式，并固定文件大小和 SHA-256。
因此安装不依赖服务器的 GitHub REST API 匿名额度。

不要同时加载旧的 codex-agent-identity-auth.so 和新的
codex-agent-identity.so，两者都会声明 Codex 凭证解析能力。

插件商店只会安装 `.so`，无法安全地自动创建 sidecar 容器、Docker network、加密密钥、management key 和持久化目录。全新部署建议先运行 `sh deploy/bootstrap-runtime.sh --start`，之后在 CPA 插件商店点击安装即可。

Docker 部署中，`sidecar_api_url` 留空时插件会自动读取 `CODEX_AGENT_IDENTITY_SIDECAR_HOSTS`，并默认使用 sidecar 容器的 `8787` 端口；宿主机安装仍使用 `http://127.0.0.1:18787/v0/management/api-call`。这是旧配置兼容项，新安装不需要填写。

## 部署 sidecar

全新 checkout 推荐使用 bootstrap helper。它会创建 runtime 目录和两份独立密钥，生成已启用插件的 CPA 配置、随机 CPA API key 和外部 Docker network，并可直接启动官方 CPA 与 sidecar：

~~~bash
sudo sh deploy/bootstrap-runtime.sh --start
~~~

默认本机浏览器使用 <code>http://127.0.0.1:18787/agent-identity/</code> 访问 sidecar。若 CPA 通过反向代理发布，推荐使用同源路径：

~~~bash
sudo sh deploy/bootstrap-runtime.sh --sidecar-url /agent-identity/ --start
~~~

已有部署不要覆盖原 config 和 .env；请手动合并同样的设置。参考 deploy/docker-compose.production.yml 时，要显式指定 project directory，确保根目录的 config、auth、logs 和 runtime 路径解析正确：

~~~bash
sudo sh deploy/init-runtime.sh ./runtime
cp .env.example .env
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
EMBED_ALLOWED_ORIGINS。管理密码只保存在当前标签页的 sessionStorage，不会
写入 localStorage、URL、Cookie 或导出文件。

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
make package-plugin GOOS=linux GOARCH=amd64
~~~

vX.Y.Z 标签会生成 Linux amd64/arm64 插件 zip、sidecar tar.gz、
checksums.txt、GitHub Release，以及 GHCR 的多架构 sidecar 镜像。

## 安全说明

- 原始凭证使用 AES-256-GCM 加密，密钥必须与数据卷分开保存。
- .so 是 CPA 进程内受信任代码，安装前必须校验发布哈希。
- 插件资源入口只能作为不含 secret 的 wrapper；不得把 token、Management key 或特权 API 操作放进
  `/v0/resource/plugins/...`，真正的身份操作必须继续由 sidecar Bearer 认证。
- 不要在 issue、日志、截图或导出中提交 token、管理密码、Cookie、代理密码、
  cais_ 密钥或 auth 文件。
- ALLOW_PLAINTEXT_STORE 和 ALLOW_INSECURE_UPSTREAM 仅用于本地测试。
- reset-credit consume 路径可能消耗额度，健康检查、启动和预检绝不会调用它。

本项目使用 MIT License，是独立集成项目，不是 OpenAI 官方产品。
