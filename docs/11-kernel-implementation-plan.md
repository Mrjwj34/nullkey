# 11 · Kernel 实施计划（Go）

> 状态：**P0–P9 已实现；普通 PR CI 与 release CI 门禁已启用**。
>
> 本文只覆盖后端 kernel（`nullkeyd`）。GUI 由独立团队按
> [api/openapi.yaml](../api/openapi.yaml) 并行实现；本仓库不修改 GUI 代码。
>
> 验收断言见 [12-acceptance.md](12-acceptance.md)，协作规则见
> [AGENTS.md](../AGENTS.md)，交互行为见 [10-interaction-contract.md](10-interaction-contract.md)，
> 宿主 CLI/沙箱服务/Terraform（v1.1）见 [13-host-tools-and-services.md](13-host-tools-and-services.md)。
> 现有架构/威胁模型/API 契约见 [02](02-architecture.md)、[01](01-threat-model.md)、[08](08-api-contract.md)。

## 1. 目标与约束

### 1.1 目标

用 Go 实现一个常驻宿主机的 kernel：

- 本地控制面 API（`/api/v1`），供 GUI 调用；
- 数据面 gateway（模型 / MCP / 私有 git），供沙箱内 harness 调用；
- egress proxy（`services_only` / `filtered` / `open`；三档都保留 gateway）；
- native runtime（macOS Seatbelt + PTY）、工作区/状态/缓存；
- Keychain 凭证、per-sandbox token、OAuth Broker、审计与事件；
- 服务隧道、可选 Service Supervisor 与只读产物审查；
- 满足 [slo.md](../slo.md) 的基线。

### 1.2 硬约束

| # | 约束 | 落地方式 |
|---|---|---|
| C1 | **真实凭证永不进入沙箱** | 只在 Keychain + data plane 注入；沙箱只看到 `NULLKEY_TOKEN` |
| C2 | GUI 不在本仓库范围 | 只通过 `api/openapi.yaml` 协作；控制面契约只做加法 |
| C3 | 默认 macOS Apple Silicon、native、无 VM | Seatbelt profile + 原生进程；desktop 只做可选后端 |
| C4 | 默认只绑 `127.0.0.1` | 启动时校验所有 listener；禁止 `0.0.0.0` |
| C5 | 失败关闭 | profile/token/gateway/proxy 失败时明确报错，不降级为无沙箱/直连 |
| C6 | 语言与生态 | Go；优先标准库，第三方库按 §4 白名单引入 |
| C7 | 契约稳定 | 路由由 OpenAPI 派生校验；破坏性变更走 `/api/v2` 或 ADR |
| C8 | 不把凭证写进日志/审计/argv | 结构化日志 + redaction + Keychain API，不做命令行传参 |

### 1.3 非目标（v1）

- 防 prompt injection / 恶意 Agent；
- VM 级隔离、Windows/Linux host、多租户、公网访问；
- 完整桌面 app catalog；
- 按 harness/MCP 的凭证 scope；
- Host Tool Gateway / `gh`/`aws`/`kubectl`/`terraform`（v1.1，见 docs/13）；
- 把沙箱内任意端口暴露给宿主；
- 强 loopback/网络隔离（native runtime 的已知限制；service supervisor 用生成 token 补偿）。

## 2. 三个 HTTP 平面

Kernel 必须把「给 GUI 的控制面」和「给沙箱的数据面」分开，不能共用一个 token 或一个 listener。

| 平面 | 默认地址 | 认证 | 调用方 | 契约 |
|---|---|---|---|---|
| Control API | `127.0.0.1:49152/api/v1` | daemon token | GUI / launcher | `api/openapi.yaml` |
| Gateway | `127.0.0.1:49153` | per-sandbox token | 沙箱内 harness | 本文 §7 + 适配器文档 |
| Egress proxy | `127.0.0.1:49154` | 无（仅 allowlist） | 沙箱内任意进程 | HTTP proxy 语义 |

规则：

- **token 不跨平面**：daemon token 不能访问 gateway；sandbox token 不能访问 control API。
- 数据面路径不属于 `/api/v1`，不加入 GUI 的 OpenAPI。
- 端口可在配置中覆盖；每个 listener 启动时独立校验 loopback。
- 只有 control API 会被 GUI 发现；gateway/proxy 地址通过 sandbox 环境变量注入。

> 三个 listener 均已接入；配置与端口模型保持本节冻结的语义。
> `services_only` 只关闭通用 egress；模型/MCP/git 仍可经 gateway 访问远端，**不是离线模式**。

## 3. 总体架构

```mermaid
flowchart LR
  GUI["GUI（独立仓库）"] -->|daemon token| API["Control API<br/>/api/v1"]
  API --> CORE["Kernel services<br/>sandbox / workspace / credentials"]
  CORE --> STORE[("SQLite")]
  CORE --> KC[("macOS Keychain")]
  CORE --> RT["Native Runtime<br/>Seatbelt + PTY"]
  CORE --> OA["OAuth Broker"]
  CORE --> EV["Audit + Events"]
  RT -->|sandbox token| GW["Gateway<br/>model / MCP / git"]
  RT -->|HTTP(S)_PROXY| EP["Egress Proxy"]
  GW -->|real credential| UP["Upstream"]
  EP -->|allowlist| NET["Internet"]
  RT -->|WebSocket| TS["Terminal Service"]
  TS --> API
  TP["Service Tunnel"] --> RT
  API --> TP
```

### 3.1 依赖方向

```text
cmd/nullkeyd
  └── internal/kernel            composition root / config / lifecycle
        ├── internal/api         HTTP transport, middleware, route table
        │     └── internal/domain
        ├── internal/ports       接口（Runtime / SecretStore / Gateway / Store ...）
        │     └── internal/domain
        └── adapters (future)    sqlite / keychain / native / oauth / mcp / egress ...
              └── internal/ports + internal/domain
```

- `api` 不直接依赖任何 adapter；handler 依赖 service/port。
- adapter 不 import `api`；由 `kernel` 组装。
- `domain` 不 import 上层包，保持纯类型/枚举/状态机。
- 公开包只有 `cmd/`；其余全部 `internal/`，避免被外部误用。

## 4. Go 技术选型

| 能力 | 选型 | 说明 |
|---|---|---|
| 语言 | Go 1.24+ | `go.mod` 固定 `go 1.24.0`；本机/CI 可用更新工具链 |
| HTTP | `net/http` + Go 1.22 路由模式 | 骨架已用；不引入 chi/gin |
| JSON | `encoding/json` | DTO 显式定义；禁用 `map[string]any` 作为长期 API 模型 |
| 日志 | `log/slog`（JSON） | 只记录 method/path/status/request_id，不记录 body/header/query |
| ID | `crypto/rand` + Base64URL | `sbx_` / `ws_` / `st_` / `cred_` / `mcp_` / `flow_` / `job_` / `evt_` / `req_` |
| 存储 | `modernc.org/sqlite` | 纯 Go、无 cgo；WAL + `busy_timeout`；migrations 用 `embed` |
| PTY | `github.com/creack/pty` | macOS 原生 PTY；Windows 不支持（非目标） |
| WebSocket | `github.com/coder/websocket` | context-aware；终端与 SSE 之外的双向通道 |
| Keychain | `github.com/keybase/go-keychain`（候选） | Security.framework；**必须验证不走 argv**；失败则自写 cgo wrapper |
| OAuth | `golang.org/x/oauth2` | Authorization Code + PKCE；refresh 自己管理 |
| MCP | `github.com/modelcontextprotocol/go-sdk`（候选） | 先验证 stdio/streamable HTTP；不满足则自写 JSON-RPC 代理 |
| Git smart HTTP | `net/http/httputil.ReverseProxy` | 注入 Basic/OAuth；保留流式 clone/push |
| Seatbelt | `sandbox-exec`（先）或 cgo `sandbox_init` | 先验证 `sandbox-exec` profile；失败关闭，不裸跑 |
| 测试 | `testing` + `httptest` + build tag | 单元 + OpenAPI 契约 + 黑盒验收 |
| 基准 | `testing.B` + `scripts/slo.sh` | 30 次取 p50/p95；结果写入发布记录 |
| CLI | `flag` | 单二进制；不引入 cobra |

依赖策略：

- 新依赖必须能说明「标准库为什么不够」、维护状态、许可证、macOS 支持。
- 安全敏感路径（Keychain、Seatbelt、token）不允许「为了省事」换成 shell 命令。
- 禁止把真实凭证放进测试 fixture；统一使用 `testdata/fake-*`。

## 5. 代码结构

### 5.1 当前实现

```text
cmd/nullkeyd/                 daemon 入口 + __sandbox_exec helper
internal/
  api/                        control-plane HTTP：路由、中间件、错误、handlers
  contract/                   OpenAPI ↔ 路由表契约测试
  core/                       领域服务：workspace/state/sandbox/job/credential/
                              oauth/mcp/service/tunnel/gateway/audit
  domain/                     领域类型/枚举
  id/                         ID/token 生成
  kernel/                     配置、token 装载、组装、三个 listener 生命周期
  ports/                      adapter 接口
  store/sqlite/               Store + migrations
  secret/                     Keychain（darwin cgo）/ 加密文件 CI fallback
  runtime/native/             Seatbelt + process + PTY + service process
  terminal/                   ticket + WebSocket bridge
  gateway/                    gateway listener
  gateway/mcp/                JSON-RPC/SSE 代理 + tool allowlist
  egress/                     CONNECT + allowlist proxy
  events/                     SSE bus
  audit/                      append-only + redaction
  artifacts/                  只读文件树 / 预览 / diff
  redact/                     credential-shape redaction
  version/                    版本（ldflags）
test/acceptance/              黑盒验收（acceptance / acceptance+release）
testdata/tunnel_server.py     HTTP/SSE/WS 隧道测试服务
scripts/coverage.sh           全局覆盖率门禁（≥90%）
api/openapi.yaml              GUI ↔ kernel 唯一契约
docs/10, 11, 12, 13           交互契约、本计划、验收断言、v1.1 边界
```

### 5.2 实现期新增（不预先创建空目录）

```text
internal/
  store/sqlite/               Store + migrations
  credentials/keychain/       SecretStore
  runtime/native/             Seatbelt + process + PTY
  terminal/                   ticket + WebSocket bridge
  gateway/
    model/                    provider registry + auth adapters + streaming proxy
    mcp/                      host-side MCP + allowlist + stdio bridge
    git/                      smart HTTP proxy
    oauth/                    PKCE broker + refresh + host import
  egress/                     CONNECT + allowlist proxy
  services/                   Service Supervisor + registry
  tunnel/                     declared services + one-time tickets
  artifacts/                  read-only file tree / preview / diff
  audit/                      append-only + redaction
  events/                     SSE bus
  adapters/                   codex / claude / dsh / github catalog entries
  locks/                      workspace/state leases
  (v1.1) tools/               Host Tool Gateway / Terraform，v1 不实现
scripts/slo.sh                SLO benchmark
test/integration/             runtime/Seatbelt/Keychain 集成测试（darwin tag）
test/bench/                   Go benchmarks
```

## 6. 控制面 API 实施矩阵

路由表以 `api/openapi.yaml` 为唯一真相；`internal/contract` 会比对
「OpenAPI 的 path+method」与「kernel 注册的路由」。漏一条或凭空多一条都会让 CI 失败。

| 组 | 端点 | 主要实现 | 里程碑 | 验收 |
|---|---|---|---|---|
| system | `GET /health`、`GET /capabilities` | `api` | P0 已实现 | AC-API-001/004 |
| harness/app | `/harnesses*`、`/apps*`、`/jobs/{id}`、`/jobs/{id}/cancel` | `adapters` + `jobs` | P2/P6 | AC-API-008/010 |
| workspace | `/workspaces*`、files/file/diff | `workspaces` + `artifacts` | P1/P8 | AC-STATE-001/002、AC-ART-001/002 |
| state | `/states*`（delete 需 confirm） | `workspaces`/state service | P1 | AC-STATE-003 |
| sandbox | `/sandboxes*`、stop、stats、scan | `kernel` + `runtime` | P2/P9 | AC-RT-001/006、AC-SEC-010 |
| terminal | `POST /sandboxes/{id}/terminal` + WS `ready` | `terminal` | P2 | AC-RT-002/008/009 |
| desktop | `POST /sandboxes/{id}/desktop` | 可选 adapter | P8（可选） | 允许稳定返回 `501 desktop_unavailable` |
| services/tunnel | `/services`、`/ticket` + Service Supervisor | `services` + `tunnel` | P8 | AC-SVC-001..003、AC-TUN-001..003 |
| credentials | `/providers`、`/credentials*` | `credentials` + Keychain | P3 | AC-CRED-001/002/005 |
| oauth | `/oauth/*`、`/oauth/flows/{id}/cancel` | `gateway/oauth` | P5 | AC-CRED-003/004、AC-API-011 |
| mcp | `/mcp/*` | `gateway/mcp` | P6 | AC-MCP-001/002/003 |
| audit/events | `/audit`、`/events` | `audit` + `events` | P1/P9 | AC-AUD-001、AC-API-009 |

约定：

- 所有端点真实实现；不得返回伪造的 `200` 或 `501 not_implemented`。
- `desktop` 是唯一允许长期返回 `501 desktop_unavailable` 的正式端点。
- 每个端点实现时：先补/启用对应验收用例，再改 `RouteSpec.Implemented`。
- `POST` 支持 `Idempotency-Key`；分页统一 `{items, next_cursor}`。
- 所有响应带 `X-Request-Id`；错误体严格为
  `{error:{code,message,details?,request_id}}`。
- `/oauth/callback` 是浏览器回调，必须免 daemon token；OpenAPI 已标 `security: []`，
  实现时保持，并用一次性 `state` 防伪。
- workspace reset 已从契约移除；删除 workspace 默认只删记录，`delete_files=true` 仅限
  managed 且必须匹配 `confirm_name`；external 目录永不删除。
- sandbox `DELETE` 不再接受 `delete_state/delete_workspace`；传入即 400。
- state `DELETE` 必须 `confirm=true`；`reset` 保留 state 记录。
- install/OAuth 取消分别使用 `POST /jobs/{id}/cancel`、`POST /oauth/flows/{id}/cancel`；
  取消与提交互斥，迟到回调不得写凭证。

## 7. 数据面设计

### 7.1 模型 gateway

```text
sandbox: POST http://127.0.0.1:<gateway>/openai/responses
         Authorization: Bearer <sandbox-token>
gateway: token -> sandbox_id
         provider registry -> upstream + auth adapter
         inject Authorization/x-api-key
         strip sandbox token; stream response
```

- 路由前缀：`/openai/*`、`/anthropic/*`，后续按 provider 增加。
- 认证适配器：`bearer`、`header`（如 `x-api-key`）、`basic`、`query`、`oauth`。
- 只允许 token 绑定的 provider；未知 provider/MCP/git repo 一律 403/404。
- 透传模型协议需要的 header（`anthropic-version`、`anthropic-beta`、`OpenAI-*`），
  不转发 `Authorization`/`Cookie`/`X-Api-Key` 等沙箱输入。
- 流式：`FlushInterval: -1` 或逐块 flush；禁止整包缓冲。
- 日志：只记录 provider、status、bytes、duration、request_id；**不记录 body**。

### 7.2 MCP gateway

- 有凭证的 MCP 在宿主侧运行；沙箱只连 `/mcp/<connection-id>`。
- 每个 connection 绑定 sandbox、server、credential、tool allowlist。
- 支持 Streamable HTTP/SSE；stdio-only 用 `nullkey-mcp-bridge`（只转发，无凭证）。
- 宿主 MCP 进程的 stdout/stderr 留在宿主并 redaction；崩溃 → 503，不换 server。

### 7.3 私有 git

- 沙箱 remote：`http://127.0.0.1:<gateway>/git/github/<owner>/<repo>.git`。
- gateway 校验 token + repo 策略，注入 GitHub token（Basic/OAuth），转发 smart HTTP。
- 不把 PAT 写入 `.git/config`；SSH git 不支持。

### 7.4 egress proxy

- 独立 listener；`CONNECT host:port` + 普通 HTTP 代理。
- allowlist 按 hostname/SNI；`filtered` 默认，`open` 全放行，`services_only` 不提供通用 egress。
- 不做 TLS MITM，不装 CA，不改 header。
- 拒绝 loopback/私有网段/link-local 目标，避免 proxy 变成宿主内网跳板。
- 日志：host、status、bytes、duration；不记录 URL path/query/body。
- 三档模式都不阻断 gateway：`services_only` 是“仅已连接服务”，不是离线。

### 7.5 沙箱内服务（Service Supervisor，v1 Should）

- adapter 声明 manifest；supervisor 按依赖拓扑启动、健康检查、注册 Service Registry。
- 只绑定 `127.0.0.1` 的分配端口范围；停止 sandbox 时先停服务，再停 harness。
- 无认证服务仅限 preview；需要认证的服务用生成式 token/密码（存 state，停止即失效）。
- 需要宿主凭证的服务通过 gateway，不在沙箱内持有 key。
- native runtime 与宿主共享 loopback，不能提供网络命名空间级隔离；这是已知限制，
  服务安全依赖“只绑 loopback + 生成 token + tunnel ticket”，不宣称多租户隔离。
- manifest 通过 adapter/内部配置进入，v1 不新增 GUI 控制面端点；若需要新端点，先补
  OpenAPI/ADR。宿主 CLI/Terraform 属于 v1.1，不在本里程碑。

## 8. 数据模型

SQLite（WAL）表：

```sql
workspaces(id TEXT PK, name, root_path, storage_kind, default_mode, created_at, last_used_at)
states(id TEXT PK, harness_id, name, path, created_at, last_used_at)
sandboxes(id TEXT PK, harness_id, workspace_id, state_id, runtime,
          network_mode, status, token_hash BLOB, pid, last_error,
          created_at, started_at, stopped_at)
workspace_locks(workspace_id PK, sandbox_id, mode, acquired_at)
credentials(id TEXT PK, provider_id, label, keychain_account,
            fingerprint, auth_type, oauth_status, expires_at, created_at, last_used_at)
mcp_connections(id TEXT PK, server_id, label, credential_id,
                config_json, status, created_at)
services(id TEXT PK, sandbox_id, name, protocol, target_host, target_port,
         health_path, websocket, command_json, ports_json, status, created_at)
jobs(id TEXT PK, type, status, cancellable, progress, message, error, created_at, updated_at)
oauth_flows(id TEXT PK, provider_id, status, credential_id, error,
            state_hash BLOB, created_at, expires_at)
audit_events(id TEXT PK, ts, type, sandbox_id, detail_json)
schema_migrations(version PK, applied_at)
```

规则：

- `token_hash = SHA-256(token)`；**不存明文 token**。
- `keychain_account` 只是引用；DB 不存 secret。
- `detail_json` 写入前经过统一 redaction。
- `storage_kind` 由创建方式固定：省略 `root_path` 为 `managed`，用户指定为 `external`。
- 所有时间 RFC 3339 UTC；ID 带前缀；terminal 会话使用 `term_`（WS `ready` 返回）。
- `services` 只登记声明服务；不持久化任意端口转发。
- migration 嵌入二进制，启动时在事务中串行应用；失败即拒绝启动。
- `workspace_locks` 在启动时清理孤儿锁（对应 sandbox 不存在或已停止）。
- 删除 sandbox 始终保留 workspace/state；不接受 `delete_state/delete_workspace` 级联参数。

## 9. 安全不变量与执行点

| ID | 不变量 | 执行点 | 验证 |
|---|---|---|---|
| SI-1 | 沙箱 env 只有 `NULLKEY_TOKEN` / `NULLKEY_GATEWAY` / `NULLKEY_PROXY` / 遥测关闭 | runtime env 构造器（白名单） | AC-SEC-001 |
| SI-2 | 沙箱读不到宿主 home/SSH/Keychain | Seatbelt profile + 路径校验 | AC-SEC-002 |
| SI-3 | sandbox token 32B 随机，仅存哈希，停止即失效 | token service + DB + gateway | AC-SEC-003/004 |
| SI-4 | daemon 重启后旧 sandbox token 全部失效 | token 表只在内存 / 启动清空 | AC-SEC-005 |
| SI-5 | 日志/审计/终端无明文凭证 | redaction + 禁止 body/header 日志 | AC-SEC-006 |
| SI-6 | Credentials API 只返回 fingerprint | DTO 白名单 | AC-SEC-007 |
| SI-7 | Keychain 值不走 argv | Security.framework/cgo | AC-SEC-008 |
| SI-8 | control/data plane token 不互通 | 独立 listener + 独立校验 | AC-SEC-009 |
| SI-9 | 未声明服务不可达 | service registry + ticket | AC-TUN-001/003 |
| SI-10 | 失败关闭 | profile/gateway/proxy 错误直接 5xx/4xx | AC-NET-007 |
| SI-11 | 检查范围诚实 | scan 只覆盖 generated_runtime，报告 excluded | AC-SEC-010 |
| SI-12 | 终端重连不重复输入 | 单写连接 + `ready` + `terminal_replaced` | AC-RT-002/008 |

实现要求：

- 配置渲染器只接受 `SandboxSpec` + token，不接收 credential 对象。
- gateway 是唯一能读取 secret 的 data-plane 组件；它不把 secret 写回响应。
- 任何新增日志字段都要过 redaction 单测。
- 扫描（`POST /sandboxes/{id}/scan`）是启发式验证，不是安全边界。

## 10. 生命周期与并发

### 10.1 Sandbox 状态机

```text
creating -> starting -> running -> stopping -> stopped
                    \-> failed
```

- `POST /sandboxes` 同步完成校验、锁、token、进程启动；返回 `starting|running`。
- 启动失败：释放锁、撤销 token、清理进程、记录 `failed` + `last_error`。
- `stop`：撤销 token → 停服务 → 停 PTY/子进程组 → 释放锁 → `stopped`。
- `delete`：先 stop，再删 DB 记录；始终保留 workspace/state，不接受级联删除参数。
- daemon 退出：停止所有 sandbox（kill 进程组），避免孤儿进程持有 token。
- daemon 重启恢复：先核对并停止遗留 sandbox 进程，再释放锁并标记 `stopped`；
  不把旧 PTY 伪装成可恢复，不宣称任务仍在运行。
- 终端：PTY 归 daemon 管理；WS 断线只 detach，不杀进程；无人连接时持续 drain 输出；
  新 ticket attach 到同一 PTY 并替换旧写连接（旧连接收 `terminal_replaced`）；
  attach 后即使尺寸相同也触发 SIGWINCH；不保存服务器端 scrollback。

### 10.2 并发

- 每个 sandbox 一个 supervisor goroutine；PTY pump 独立 goroutine。
- 全局 `Store` 由 SQLite 串行化；锁表用事务获取，避免 TOCTOU。
- `context.Context` 贯穿 API → service → runtime → process；请求取消要传递。
- SSE subscriber 有界缓冲；慢客户端断开，不阻塞事件总线。
- 终端 WebSocket 背压：输出队列有上限，超限断开并审计。
- Service Supervisor 每个 sandbox 一个；服务退出/健康失败按 manifest 重启或标记 error，
  不阻塞 harness；停止顺序为服务 → harness。

### 10.3 错误与幂等

- 统一错误码见 [08 §2](08-api-contract.md)；新增码先更新契约文档。
- `Idempotency-Key` 在 `POST` 上持久化（表/内存 TTL），重复 key 返回同一结果。
- 5xx 只返回安全消息；细节进宿主日志（redacted）。

## 11. 测试策略

### 11.1 层次

| 层 | 位置 | 触发 | 内容 |
|---|---|---|---|
| 单元 | `internal/**` | `go test ./...` | 中间件、错误、config、redaction、状态机、路径校验 |
| 契约 | `internal/contract` | CI | OpenAPI path+method ↔ 路由表；security scheme；base path |
| 验收（黑盒） | `test/acceptance` | `-tags acceptance` | 启动真实 `nullkeyd`，走 HTTP/WS；按 AC ID 组织 |
| 集成 | `test/integration` | `darwin` tag | Seatbelt、Keychain、PTY、真实 harness smoke |
| SLO | `test/bench` + `scripts/slo.sh` | release / 手动 | 30 次 p50/p95；RSS/CPU/延迟 |
| Release | `test/acceptance`（`acceptance && release`） | `release: published` / 手动 | 产品能力断言 + SLO gate；不在 PR 运行 |

### 11.2 验收运行方式

```bash
make check                 # fmt/vet/unit/contract/build/acceptance-compile
make acceptance-ci         # 非严格（当前无 pending，等价严格）
make acceptance            # 严格：任何失败都阻断
make release-product       # release-only：产品能力断言
make release-slo           # release-only：SLO gate（严格）
```

- **普通 PR CI**：并行跑 static、build（linux/darwin）、unit（linux/macOS）、
  acceptance（linux/macOS）、OpenAPI contract；不跑 product/SLO。
- **Release CI**：`.github/workflows/release.yml` 在 GitHub Release `published` 时
  运行产品能力断言与 SLO gate，并上传 SLO 输出；SLO 必须通过。

- 严格模式 = `NULLKEY_ACCEPTANCE_STRICT=1`。
- 断言 ID 稳定；实现一个功能就把对应 `pending` 改为真实断言，并更新
  [12-acceptance.md](12-acceptance.md) 的状态列。
- 不允许为了让 CI 变绿而删除断言；只能标记 superseded 并写原因。
- 测试不使用真实凭证；用 `sk-test-*` / fake token，且不放进 git。

### 11.3 安全测试

- 沙箱内 `env`、`ps e`、`/proc`（Linux 集成）、Nullkey 生成的配置扫描 = 0；
  扫描范围明确为 `generated_runtime`，不把用户 workspace 标成无秘密。
- 停止后重放 gateway 请求 → 401。
- 撤销 credential 后下一次模型/MCP 调用失败。
- 日志/审计/终端输出扫描无 secret。
- 路径穿越/symlink 逃逸/非 loopback bind/CORS 非 loopback Origin 负例。

## 12. 里程碑

| 阶段 | 交付 | 退出标准 | 主要 AC |
|---|---|---|---|
| **P0 骨架** | 计划、验收、AGENTS、CI、控制面路由/health/auth | `make check` 绿；OpenAPI 53 条路由全覆盖 | AC-API-001..007 |
| **P1 API + 存储** | SQLite/migrations、ID、workspace/state CRUD、锁、审计、SSE、取消语义 | 资源 CRUD 与错误码稳定；重启数据还在；取消/删除语义符合 docs/08 | AC-STATE-001/002/003、AC-AUD-001、AC-API-008/009/010/011 |
| **P2 Native runtime + PTY** | Seatbelt profile、进程 supervisor、terminal WS `ready`/重连、stats | Terminal 可交互；越权读失败；停止即失效；重连不重复输入 | AC-RT-001..008、AC-SEC-001..005 |
| **P3 Credentials** | Keychain、provider registry、credential CRUD、token service | 只返回 fingerprint；撤销生效 | AC-CRED-001/002/005、AC-SEC-007/008 |
| **P4 Model gateway** | 独立 listener、provider registry、auth adapter、流式代理 | Codex 通过 gateway 回一句话；无 key 泄漏 | AC-NET-001/002/005/007 |
| **P5 OAuth** | PKCE、callback、flow/cancel、refresh、宿主导入 | Codex/GitHub 登录/刷新；取消后迟到回调不写凭证 | AC-CRED-003/004、AC-API-011 |
| **P6 MCP** | 宿主 MCP、GitHub、tool allowlist、stdio bridge | 配置零 secret；tool call 成功 | AC-MCP-001..003 |
| **P7 Network** | egress proxy、三档模式、Seatbelt 网络规则 | filtered 可用、直连失败、open 警告 | AC-NET-003/004 |
| **P8 Services + Tunnel + Artifacts** | Service Supervisor、服务注册、一次性 ticket、HTTP/WS/SSE、只读文件/diff | 依赖顺序/健康检查；未声明服务 404；路径逃逸被拒 | AC-SVC-001..003、AC-TUN-001..003、AC-ART-001/002 |
| **P9 SLO + 加固** | benchmark、redaction 扫描、core dump、演示脚本 | SLO 基线达标；受控样本扫描 0；演示 3 分钟 | AC-SLO-001..004、AC-SEC-010、AC-API-009 |

并行建议（与 [04-plan](04-plan.md) 的 A/B/C 对应）：

- P1/P2 可与 P3/P4 并行，但共享 `domain`/`ports` 接口要先冻结。
- P5/P6 依赖 P3 的 Keychain；P7 依赖 P2 的进程环境。
- P8 依赖 P1/P2；GUI 可在 P1 后开始接控制面。
- Release workflow 在所有 P9 项完成后才应转绿；发布前必须检查 SLO artifact。

## 13. 风险与预案

| 风险 | 影响 | 预案 |
|---|---|---|
| Seatbelt 端口级网络规则不可用 | 无法只允许 loopback | 退回 proxy + token + 威胁模型；记录为已知限制，不在文档宣称强隔离 |
| Native 服务共享 loopback | 服务可被本机其他进程访问 | 只绑 loopback + 分配端口 + 生成 token；强隔离依赖 desktop/VM |
| `sandbox-exec` 被系统移除 | runtime 不可用 | 验证 cgo `sandbox_init`；两者都失败则 fail closed，不裸跑 |
| PTY/TUI 兼容性（Codex TUI） | 演示失败 | 先保证 shell，再 Codex；固定版本 smoke test |
| Keychain cgo/签名问题 | 凭证不可用 | 用 cgo wrapper；测试用内存 SecretStore；生产失败关闭 |
| OAuth provider 条款/限制 | Codex 无法登录 | API key fallback；宿主导入；Codex 优先验证 |
| MCP SDK 不成熟 | GitHub MCP 不通 | 自写 JSON-RPC/stdio 代理；先 Filesystem |
| gateway 流式缓冲 | 首字节延迟超标 | `ReverseProxy` + `FlushInterval`/逐块 flush；benchmark 守住 |
| 契约漂移 | GUI 联调失败 | `internal/contract` + 只做加法；变更走 ADR |
| 时间不足 | Must 未完成 | 按 P0→P4→P2→P3→P6→P7 砍 Should；先保 Codex + token |

## 14. 已确认的实现决策

1. **daemon token 交付**：优先 `NULLKEY_DAEMON_TOKEN`；否则生成/复用
   `<home>/daemon-token`（0600）。
2. **CORS origin 白名单**：仅 loopback origin；非 loopback 直接 403。
3. **Keychain**：darwin 使用 Security.framework cgo wrapper（service
   `com.nullkey.credentials`，无 argv）；CI/Linux 使用 AES-GCM 加密文件 fallback，
   可用 `NULLKEY_SECRET_BACKEND=file` 显式选择。
4. **MCP**：使用自写 JSON-RPC/SSE 代理（stdlib），支持 Streamable HTTP 与
   tool allowlist；stdio bridge 仍按需后续补充。
5. **Codex 配置**：模型 gateway 以 provider base URL + auth adapter 注入；
   不在沙箱写真实 key。
6. **GitHub OAuth**：支持 PAT/宿主导入；OAuth app 由 `NULLKEY_OAUTH_GITHUB_*`
   配置，未配置时状态为 unavailable。
7. **desktop runtime**：v1 稳定返回 `501 desktop_unavailable`。
8. **Service Supervisor manifest**：由 workspace 的 `.nullkey/services.json`
   提供，v1 不新增 GUI 端点；端口限制在 1024–65535（非特权端口）。
9. **Host Tool Gateway/Terraform**：v1.1；v1 不实现、不修改当前 OpenAPI。

## 15. 完成定义（Definition of Done）

一个 kernel 功能算完成，必须同时满足：

- [x] 对应 OpenAPI 端点/事件有真实实现，不再返回 `501`；
- [x] 对应 AC ID 从 `pending` 变为真实断言并通过严格模式；
- [x] 全局 Go 测试覆盖率 ≥90%（`make coverage`）；
- [ ] 错误码、`X-Request-Id`、分页/幂等符合 [08](08-api-contract.md)；
- [ ] 不新增凭证泄漏面；redaction/路径/token 测试通过；
- [ ] `make check` 绿；涉及 runtime/Keychain 的改动跑过集成测试；
- [ ] 文档（本文/12/08/09/10）与 `AGENTS.md` 同步；
- [ ] PR 描述写清测试命令与安全影响。

## 16. 参考

- [00 产品](00-product.md)、[01 威胁模型](01-threat-model.md)
- [02 架构](02-architecture.md)、[03 Harness/MCP/OAuth](03-harness-mcp.md)
- [04 三天计划](04-plan.md)、[06 决策](06-decisions.md)
- [07 状态与挂载](07-state-and-mounts.md)、[08 API 契约](08-api-contract.md)
- [09 服务隧道](09-service-tunnel.md)、[10 交互契约](10-interaction-contract.md)
- [13 宿主 CLI/沙箱服务/Terraform（v1.1）](13-host-tools-and-services.md)
- [SLO](../slo.md)、[OpenAPI](../api/openapi.yaml)、[验收断言](12-acceptance.md)
