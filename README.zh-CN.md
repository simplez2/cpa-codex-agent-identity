# CPA Codex Agent Identity

简体中文 | [English](README.md)

这是一个面向 CLIProxyAPI（CPA）的 Codex Agent Identity / Personal Access
Token 集成项目。首个公开版本由两个部分组成：

- codex-agent-identity.so：CPA 原生动态插件，注册 Codex AuthProvider 和
  CPAMC 的 Agent Identity 管理入口。
- sidecar：负责官方凭证验证、AES-256-GCM 加密存储、AgentAssertion、PAT
  转发、批量导入、CPA auth 文件同步以及 HTTP/SOCKS 代理热加载。

CPA 只会看到随机生成的 cais_ 客户端密钥，不会保存原始 Agent Identity
JWT 或 PAT。现有官方 OAuth 和第三方 API 渠道不由本插件接管。

## 主要能力

- 在 CPAMC 左侧显示 Agent Identity 管理入口，交互方式与 Keeper 插件一致。
- 直接复用 CPAMC 的亮色/暗色主题变量，CPA 切换主题时插件壳与 sidecar iframe 同步切换。
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

v0.3.2 使用 Go 1.26，并以 CLIProxyAPI v7.2.95 SDK 为编译基线。插件使用
动态插件 ABI v1，但正式升级 CPA 前仍必须用目标官方镜像做独立 canary。

首版保留稳定 sidecar 数据面，没有仓促把 AgentAssertion、PAT、图片、SSE、
WebSocket、额度和代理逻辑全部重写进进程内插件。以后可以在同一仓库增加纯
Executor 实现，并保持现有加密数据格式不变。

## 从 CPAMC Plugin Store 安装

把本仓库注册表加入宿主机挂载的 CPA 配置：

~~~yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/simplez2/cpa-codex-agent-identity/main/registry.json"
  configs:
    codex-agent-identity:
      enabled: true
      priority: 1000
      sidecar_url: "/agent-identity/"
~~~

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

## 部署 sidecar

参考 deploy/docker-compose.production.yml：

~~~bash
sudo sh deploy/init-runtime.sh ./runtime
cp .env.example .env
docker network inspect agent-identity >/dev/null 2>&1 || docker network create agent-identity
docker compose -f deploy/docker-compose.production.yml up -d
~~~

CPA remote-management 密码与 sidecar 的 management-key 应保持一致，这样
sidecar 才能自动创建、停用、刷新和删除 CPA 原生 Codex auth 文件。
初始化脚本会把 data-v3 和 secrets 设置为镜像内非特权 UID/GID 65532 所有；
如果修改 SIDECAR_UID 或 SIDECAR_GID，运行脚本时必须使用相同的值。

推荐把 CPAMC 和 sidecar 发布在同一浏览器来源：

~~~nginx
location ^~ /agent-identity/ {
    proxy_pass http://127.0.0.1:18787;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    client_max_body_size 5m;
}
~~~

如果跨来源嵌入，必须把 CPAMC 的完整 origin 加入
EMBED_ALLOWED_ORIGINS。管理密码只保存在当前标签页的 sessionStorage，不会
写入 localStorage、URL、Cookie 或导出文件。

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
CPAMC、批量预检、auth 同步、HTTP/SSE/WebSocket/图片/额度/代理，再固定镜像
digest 并替换生产。不要直接用 latest 覆盖正在工作的实例。

## 构建与发布

~~~bash
make test
make race
make vet
make build
make package-plugin VERSION=0.3.2 GOOS=linux GOARCH=amd64
~~~

vX.Y.Z 标签会生成 Linux amd64/arm64 插件 zip、sidecar tar.gz、
checksums.txt、GitHub Release，以及 GHCR 的多架构 sidecar 镜像。

## 安全说明

- 原始凭证使用 AES-256-GCM 加密，密钥必须与数据卷分开保存。
- .so 是 CPA 进程内受信任代码，安装前必须校验发布哈希。
- 不要在 issue、日志、截图或导出中提交 token、管理密码、Cookie、代理密码、
  cais_ 密钥或 auth 文件。
- ALLOW_PLAINTEXT_STORE 和 ALLOW_INSECURE_UPSTREAM 仅用于本地测试。
- reset-credit consume 路径可能消耗额度，健康检查、启动和预检绝不会调用它。

本项目使用 MIT License，是独立集成项目，不是 OpenAI 官方产品。
