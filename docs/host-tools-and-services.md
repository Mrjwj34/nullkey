# 宿主 CLI、沙箱服务与 Terraform

> 状态：v1.1 设计。v1 kernel 不实现 Host Tool Gateway / Terraform；Service Supervisor 为 v1 Should。
> 本文不修改 `api/openapi.yaml`；v1.1 落地前先补 API 契约与 ADR，避免动到已冻结的 GUI 契约。
>
> 架构图里的箭头是**凭证路径**，不是全部执行路径。本文补三件事：
>
> 1. 宿主 CLI 工具（`gh` / `aws` / `kubectl` / `terraform`）怎么在不在沙箱里放 key 的前提下使用。
> 2. 沙箱内需要认证的服务怎么启动、怎么认证、怎么被 Agent/用户访问。
> 3. Terraform 这种"工具 + 云凭证 + 状态 + 破坏性操作"的特殊情况。

## 1. 三个执行面

| 执行面 | 运行位置 | 例子 | 凭证 |
|---|---|---|---|
| Sandbox process | 沙箱内 | Codex TUI、shell、`npm`、`git` | 只有 per-sandbox token |
| Host Tool Gateway | 宿主 | `gh`、`aws`、`kubectl`、`terraform` | Keychain/env，沙箱不可见 |
| Sandbox Service Supervisor | 沙箱内 | DSH WebUI、preview server、本地 DB | 生成式/网关代理，无宿主 key |

Agent 不能直接访问任意宿主服务；宿主 CLI 通过 shim → gateway；沙箱服务通过 localhost/tunnel。

## 2. Host Tool Gateway（v1.1）

### 2.1 问题

沙箱是 native 进程，`gh` / `aws` / `terraform` 二进制在宿主上也能跑，但它们需要：

- `~/.config/gh`、`~/.aws`、`~/.kube`、云凭证；
- 这些目录不能挂进沙箱（否则 key 就进沙箱了）；
- 用沙箱的 `HOME` 跑，它们又没有登录态。

### 2.2 设计

```text
sandbox: nullkey-gh / nullkey-aws / nullkey-kubectl / nullkey-terraform
  → 以 argv 数组发给 127.0.0.1:<gateway>/tools/<id>/exec
  → Host Tool Gateway 校验 sandbox token + 命令 allowlist
  → 宿主执行真实 CLI（sanitized env + Keychain 凭证）
  → stdout/stderr/exit code 回传；输出脱敏
```

- shim 是极小的二进制/脚本，放在沙箱 `PATH`；不含凭证。
- 只接受 **argv 数组**，不经过 `sh -c`；拒绝 shell 元字符注入。
- allowlist 到 tool + subcommand + 参数模式；默认拒绝。
- cwd 固定为 workspace；不能访问宿主 home。
- 超时、输出大小限制、审计（tool、argv hash、exit、耗时）。
- 破坏性操作（`terraform apply/destroy`、`kubectl delete`、`aws ... delete`）需要 GUI 审批；审批 token 绑定 argv hash。
- 输出经过 Scrubber；不把宿主凭证/敏感 header 回传。

### 2.3 例子

| Shim | 允许 | 默认拒绝 |
|---|---|---|
| `nullkey-gh` | `pr list/view/create`、`issue list/view`、`repo view` | `gh auth`、任意 API 写 |
| `nullkey-aws` | 只读/列举类；按 profile 限制 | IAM/删除/密钥管理 |
| `nullkey-kubectl` | `get/describe/logs`；限定 context/namespace | `delete/exec/apply` 默认审批 |
| `nullkey-terraform` | `init/validate/plan/show` | `apply/destroy` 需审批 |

### 2.4 为什么 v1.1

- 3 天内先保 Codex TUI + OAuth + MCP + 状态；宿主 CLI 是独立的策略/审批/审计工程。
- 没有它，Agent 仍可用 MCP GitHub 代替 `gh`；Terraform 属于 v1.1。
- 如果评审强需求，优先做 `terraform plan` + `gh` 两个 shim，砍其他 Should。

## 3. 沙箱内服务

### 3.1 服务类型

| 类型 | 例子 | 认证 |
|---|---|---|
| 无认证 | preview server、静态站点 | 无 |
| 生成式认证 | 本地 Postgres/Redis、临时 WebUI | 启动时生成随机密码，存 state，停止即失效 |
| 网关代理 | 需要宿主凭证的服务 | 服务调用 `127.0.0.1:<gateway>`，不持有 key |

### 3.2 Service Supervisor

每个 sandbox 有一个 supervisor，按 manifest 启动/停止服务：

```json
{
  "id": "dsh-web",
  "command": "dsh",
  "args": ["web", "--host", "127.0.0.1", "--port", "7860"],
  "cwd": "/workspace",
  "ports": [{ "port": 7860, "protocol": "http" }],
  "health": { "path": "/", "timeout_ms": 30000 },
  "depends_on": [],
  "restart": "on-failure",
  "auth": "generated"
}
```

- 启动顺序：依赖拓扑；失败重试/回滚；日志保留。
- 健康检查通过后注册到 Service Registry；GUI 显示"Open"。
- 停止 sandbox → 停所有服务；token/生成密码失效。
- 只允许声明过的端口；禁止任意端口转发。
- 日志经 redaction 后展示；不进入模型 prompt。

### 3.3 Native 的 loopback 限制

- native runtime 没有 network namespace；沙箱服务和宿主共享 loopback。
- Seatbelt 限制 outbound 到 gateway/proxy，但**不能把 loopback 完全隔离**。
- 因此：
  - 服务只绑 `127.0.0.1:<sandbox-port-range>`；
  - 端口由 supervisor 分配并登记；
  - 服务认证用生成 token；不依赖"网络不可达"。
  - 需要强隔离时用 desktop/VM 后端。
- 这是 native 的已知代价，不宣称多租户网络隔离。

### 3.4 用户/Agent 访问

- Agent：沙箱内 `127.0.0.1:<port>` 直接访问；服务 token 从 state/env 注入。
- 用户：GUI 通过 [service tunnel](09-service-tunnel.md) 打开 WebUI；一次性 ticket。
- 宿主其他进程：不经过 tunnel 不应访问；服务只绑 loopback，且 token 保护。

## 4. Terraform

### 4.1 为什么特殊

- 需要云凭证（AWS/GCP/Azure），不能放沙箱；
- provider 下载需要网络；
- state 可能包含敏感信息；
- `apply/destroy` 是破坏性操作；
- plan/apply 需要人工审查。

### 4.2 方案 A（推荐，v1.1）：Host Tool Gateway

```text
agent → nullkey-terraform init/plan
      → host gateway 在 workspace 执行 terraform
      → plan 文件写回 workspace
      → GUI 展示 plan diff
      → 用户批准 apply
      → host gateway 执行 apply（绑定 plan hash）
```

- 云凭证只在宿主；沙箱没有。
- workspace 挂载给宿主 terraform；state 留 workspace/state。
- provider 下载由宿主网络完成。
- `destroy` 默认拒绝；`apply` 必须审批。
- 输出/plan 可能含敏感信息；不进入模型 prompt，GUI 审查。

### 4.3 方案 B（兼容，v1.1）：沙箱内 + 短期凭证

- gateway 通过 OIDC/STS/Workload Identity 换短期云凭证，注入沙箱进程；
- terraform 在沙箱内跑；凭证短期、可撤销，但**不是 zero-secret**；
- 适合没有宿主 terraform/审批流的场景；
- 必须标注为"低价值短期凭证"模式，不和默认零密钥承诺混淆。

### 4.4 不做什么

- 不做 `terraform destroy` 无审批；
- 不把云长期 key 写进 workspace/state；
- 不把 plan/state 内容送进模型上下文；
- 不自动 apply。

## 5. Agent 能跟谁交互

| 目标 | 路径 | 说明 |
|---|---|---|
| 模型 API | gateway | 已有 |
| MCP server | gateway | 宿主侧 MCP |
| 私有 git | gateway git proxy | 已有 |
| 宿主 CLI | shim → Host Tool Gateway | v1.1 |
| 宿主服务（DB/内部 API） | gateway 的 allowlisted tool | 不直连宿主端口 |
| 沙箱内服务 | `127.0.0.1:<port>` | 生成 token/服务自身认证 |
| 沙箱 WebUI（用户） | service tunnel | 一次性 ticket |
| 互联网 | egress proxy | 未认证流量 |

- Agent **不能**直接访问宿主 `~/.aws`、`~/.kube`、`~/.config/gh`。
- Agent **不能**访问任意宿主端口；需要宿主服务时，加 gateway tool。
- 沙箱服务之间可以 localhost 互访；跨沙箱不行。

## 6. 初始化顺序

1. 创建/复用 workspace、state、cache；加锁。
2. 渲染 harness/MCP 配置（token 从 env 注入）。
3. 启动 harness。
4. 按依赖启动声明的服务；健康检查；注册 service。
5. 返回 terminal/service ticket。
6. 停止：停服务 → 停 harness → 撤 token → 释放锁。

Terraform 的 `init/plan` 是命令，不是长驻服务；由 host tool 或 sandbox 执行，取决于方案 A/B。

## 7. 安全边界

- Host Tool Gateway 是 TCB 的一部分：allowlist、argv 校验、审批、审计、输出脱敏。
- 沙箱服务只绑 loopback；token/生成密码；停止即失效。
- native 不能提供强 loopback 隔离；desktop/VM 才可以。
- Terraform apply/destroy 必须审批；默认拒绝 destroy。
- 不把宿主凭证、plan/state 内容回传到模型。

## 8. v1 范围

| 能力 | v1 |
|---|---|
| Service Supervisor + tunnel（DSH WebUI/preview） | Should |
| Host Tool Gateway（gh/aws/kubectl） | v1.1 |
| Terraform host-side plan/apply | v1.1 |
| Terraform sandbox + short-lived creds | v1.1 |
| 任意宿主 CLI / 任意端口 | 不做 |

## 9. API 草案（v1.1，不进当前冻结的 OpenAPI）

Host Tool Gateway：

```http
GET  /tools
→ 200 { "items": [{ "id": "terraform", "installed": true }] }

POST /tools/{id}/exec
{ "argv": ["plan", "-out", "tfplan"], "cwd": "/workspace", "timeout_ms": 120000 }
→ 200 {
  "execution_id": "exec_123",
  "exit_code": 0,
  "stdout": "...",
  "stderr": "...",
  "approval": null
}

POST /tools/{id}/executions/{execution_id}/approve
{ "decision": "allow_once", "argv_hash": "..." }
→ 200 { "ok": true }
```

Service Supervisor：

```http
POST /sandboxes/{id}/services
{ "id": "dsh-web", "command": "dsh", "args": ["web", "--port", "7860"],
  "ports": [{ "port": 7860, "protocol": "http" }],
  "health": { "path": "/", "timeout_ms": 30000 } }
→ 201 { "id": "dsh-web", "status": "starting" }

GET  /sandboxes/{id}/services/{service_id}/logs?tail=200
→ 200 { "lines": ["..."] }

POST /sandboxes/{id}/services/{service_id}/stop
→ 200 { "ok": true }
```

- v1.1 落地前先补 ADR + 与 GUI 团队确认，再改 `docs/08` 与 `api/openapi.yaml`。
- 不在本 PR 修改 OpenAPI。
