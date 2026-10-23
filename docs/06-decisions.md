# 06 · 决策记录

> 已确认的架构决策；后续变更用新 ADR 覆盖。

## ADR-001 · 产品名与仓库

**Decision**：产品名 **Nullkey**；仓库 `nullkey`；docs-first。
**Consequences**：名字短、可扩展；README 用一句话解释"no key in the sandbox"。

## ADR-002 · 威胁模型只覆盖 session 泄漏

**Decision**：范围内只覆盖 session 泄漏；prompt injection、恶意 Agent、上游劫持、沙箱逃逸、供应链明确排除。
**Consequences**：

- 不需要 HITL、目的地 allowlist 作为安全控制。
- 重点做"零凭证 + 隔离 + 可验证"。
- 文档与演示必须主动说明边界。

## ADR-003 · 透明网关优先于模型占位符

**Decision**：v1 用 base URL + per-sandbox token 的透明网关；模型不需要理解占位符语法。
**Consequences**：

- 主流 harness 都能接。
- MCP 配置只指向 gateway。
- `cred://` 工具级能力引用留到 v1.1，用于 CLI/自定义工具。

## ADR-004 · 默认 native runtime，不用 VM

**Context**：Colima/Docker 需要 Linux VM，启动/内存重；Codex/Claude Code/DSH 都有 macOS 原生版本；默认 TUI 不需要桌面。
**Decision**：v1 默认 **macOS Seatbelt 进程沙箱**；VM/容器只作为可选 desktop 后端。
**Consequences**：

- 启动/内存接近原生；满足 SLO。
- 隔离弱于 VM；不宣称防 determined escape。
- 需要仔细维护 Seatbelt profile。
- 如果评审要求认证级强隔离，需要另立 VM 后端。

## ADR-005 · 有凭证的 MCP 必须在宿主侧

**Decision**：有凭证的 MCP 在宿主侧运行；沙箱只拿 gateway URL + per-sandbox token。无凭证 MCP（Filesystem）可以沙箱侧。
**Consequences**：

- 沙箱配置零敏感信息。
- 宿主 MCP server 是被信任到可以使用凭证的组件；恶意 server 不在范围。
- 需要 stdio bridge 兼容只支持 stdio 的 harness。

## ADR-006 · 全局凭证 + per-sandbox token

**Decision**：v1 全局凭证；每个沙箱独立 token、独立审计、可单独撤销；停止即失效。
**Consequences**：

- UX 简单，符合"不要太技术化"。
- 一个 sandbox token 泄漏只影响该沙箱生命周期。
- 不做按 harness/MCP 的凭证 scope（v1）。

## ADR-007 · OAuth 是一等公民

**Context**：很多服务不支持 API key；Codex 开源、OAuth 可做；MCP 规范有 OAuth 2.1 + DCR。
**Decision**：

- v1 实现宿主侧 **OAuth Broker**（PKCE、localhost callback、refresh、Keychain）。
- **Codex OAuth 为 Must**；GitHub OAuth 为 Must；MCP OAuth（DCR）为 Should；Claude Code 导入为 Should。
- Slack/Notion/Google 等预注册 app 放 v1.1。
- 沙箱内不发起登录。

**Consequences**：

- 覆盖订阅/无 API key 的服务。
- 需要处理 provider 条款、scope、刷新失败。
- 不是"所有 provider 一次到位"；逐个接入。

## ADR-008 · GUI 不在本设计范围

**Decision**：GUI/app 选型由独立团队负责；本仓库只冻结 API 契约、事件、终端协议。
**Consequences**：

- 后端与 GUI 可并行。
- 任何 GUI 技术栈都可以接。
- API 破坏性变更需要版本号/迁移说明。

## ADR-009 · 网络默认 filtered

**Decision**：三档网络 `offline` / `filtered`（默认）/ `open`；未认证流量经 egress proxy，认证流量经 gateway。
**Consequences**：

- 包安装、公开文档、公开 git 可用。
- 代理不做 TLS MITM；认证不做通用 header 注入。
- `open` 有数据外发风险，需要警告。
- 不识别 proxy 的程序失败（fail closed）。

## ADR-010 · Harness 宿主侧下载、只读挂载

**Decision**：宿主下载固定版本、校验 SHA-256、缓存；沙箱只读使用。
**Consequences**：

- 启动快、可复现、可审计。
- 需要维护版本清单和适配器。
- 不自动升级；用户确认后切换。

## ADR-011 · Provider registry + 通用 auth 适配器

**Decision**：用 registry 描述 `base_url / auth_type / header_name / models`；网关实现少量 auth 适配器（bearer、header、basic、query、custom、OAuth）。
**Consequences**：

- 新增 provider 主要是配置。
- SigV4/Vertex 等需要额外签名适配器，放 v1.1。
- 自定义 endpoint 覆盖长尾 provider。

## ADR-012 · 登录只发生在宿主

**Decision**：harness/MCP 的登录、OAuth、刷新都在宿主完成；沙箱只拿 per-sandbox token。
**Consequences**：

- 沙箱内不出现登录页、不出现 OAuth token。
- 需要处理"宿主已登录/未登录"的状态与重新授权。
- 对不支持 API key 的 harness，v1 依赖 OAuth/导入。

## ADR-013 · 网络与凭证隔离正交

**Decision**：允许互联网（经 proxy），认证流量走 gateway；沙箱无默认外网路由。
**Consequences**：

- 产品可用性提升；安全论证仍成立。
- 需要区分未认证/认证流量。
- 数据外泄是另一个问题，不在威胁模型内。

## ADR-014 · 状态默认持久化 + 可共享

**Decision**：workspace / state / cache 默认持久化；workspace 支持 rw 独占 / ro 共享；cache 全局共享。
**Consequences**：

- 继续进度、复用登录/缓存。
- 需要锁、重置、清理、路径校验。
- 共享 state 时 token 必须在启动时轮换，避免旧 token 残留。
- 详见 [07-state-and-mounts.md](07-state-and-mounts.md)。

## ADR-015 · 默认 TUI（harness 的 TUI）

**Decision**：GUI 默认是 SSH-client 风格的 Terminal 页，里面跑 harness 自己的 TUI；虚拟桌面是独立可选视图。
**Consequences**：

- 编码 Agent 的交互模式不变。
- 默认不需要 Xvfb/noVNC。
- 桌面 app/虚拟桌面需要可选 desktop runtime，单独计量。

## 已确认的产品决策

| # | 问题 | 决策 |
|---|---|---|
| 1 | OAuth | 重要；v1 做 OAuth Broker，Codex OAuth Must；API key fallback |
| 2 | GUI/app 选型 | 不在本设计范围；API 契约为边界 |
| 3 | 状态 | 尽可能持久化且共用 |
| 4 | Windows/Linux | 不做；跑 macOS MVP |
| 5 | 凭证 scope | v1 全局；不做按 harness/MCP 的 scope |
