# 06 · 决策记录与开放问题

> 格式：Context / Decision / Consequences。状态：Accepted / Open。

## ADR-001 · 产品名与仓库

**Context**：需要一个公开、可记忆、能表达"密钥不在沙箱"的名字。  
**Decision**：产品名 **Nullkey**；仓库 `nullkey`；docs-first，代码随后。  
**Consequences**：名字短、可扩展；"Null" 可能被理解为"空指针"，需要在 README 里用一句话解释。

## ADR-002 · 威胁模型只覆盖 session 泄漏

**Context**：三天内同时做"防注入"和"防泄漏"通常两个都做不好；题目核心是"沙箱内不出现敏感信息"。  
**Decision**：范围内只覆盖 session 泄漏；prompt injection、恶意 Agent、上游劫持、沙箱逃逸、供应链明确排除。  
**Consequences**：

- 设计更简单：不需要 HITL、目的地 allowlist 作为安全控制。
- 可以更专注地做"零密钥 + 隔离 + 可验证"。
- 文档与演示必须主动说明边界。

## ADR-003 · 透明网关优先于模型占位符

**Context**：原始思路是让模型用特殊格式占位符，工具调用时替换；这需要模型配合、schema 兼容、工具适配。  
**Decision**：v1 用 **base URL + per-sandbox token** 的透明网关；模型不需要知道任何占位符语法。  
**Consequences**：

- 主流 harness（Claude Code / Codex / DSH）都能接。
- MCP 配置只指向 gateway，不需要模型理解。
- `cred://` 工具级能力引用留到 v1.1，用于 CLI/自定义工具。

## ADR-004 · v1 沙箱用 Colima + Docker

**Context**：microsandbox 更"未来"，但 beta；Colima 已安装、MIT、Apple Silicon 成熟。  
**Decision**：v1 用 Colima（Lima VM）+ Docker + 自建 Ubuntu ARM 桌面镜像；`SandboxProvider` 抽象。  
**Consequences**：

- 交付风险低；容器共享 VM 内核，不宣称认证级强隔离。
- v0.2 可替换为 microsandbox / Apple `container`。
- 需要自己维护桌面镜像。

## ADR-005 · 有凭证的 MCP 必须在宿主侧

**Context**：MCP server 需要 token 才能工作；如果跑在沙箱，token 就在沙箱的 env/配置/内存里。  
**Decision**：有凭证的 MCP 在宿主侧运行；沙箱只拿 gateway URL + per-sandbox token。无凭证的 MCP（Filesystem）可以沙箱侧。  
**Consequences**：

- 沙箱配置零敏感信息。
- 宿主侧 MCP server 是"被信任到可以使用凭证"的组件；恶意 server 不在范围。
- 需要 stdio bridge 兼容只支持 stdio 的 harness。

## ADR-006 · 全局 key + per-sandbox token

**Context**：题目要求"初版给所有沙箱共用"，同时要控制泄漏影响。  
**Decision**：v1 全局 key；每个沙箱独立 token、独立审计、可单独撤销；停止即失效。  
**Consequences**：

- UX 简单，符合"不要太技术化"。
- 一个沙箱 token 泄漏只影响该沙箱生命周期；不是长期 key 泄漏。
- v1.1 再做按 harness/MCP 的 scope。

## ADR-007 · v1 不做完整 OAuth，做广谱静态凭证 + 宿主登录导入

**Context**：Claude Pro / ChatGPT Plus 用户多，但 OAuth token 会落在沙箱，违反 zero-secret；完整宿主侧 OAuth 需要 PKCE、回调、刷新、多账号，且 Claude Code 闭源、provider 条款存在不确定性。  
**Decision**：

- v1 支持广谱 API key/token（8–10 个模型 provider + 通用 auth 类型）。
- v1 **不实现完整 OAuth 授权流程**。
- v1 提供实验性"导入宿主已有登录"：Codex 读 `~/.codex/auth.json`；Claude Code 用 `setup-token` 或 Keychain 导入。
- v1.1 做完整宿主侧 OAuth broker + MCP OAuth（DCR）。

**Consequences**：

- 订阅用户可以"导入"已有登录，但不保证自动刷新/所有版本。
- 必须在 README 和产品页明确"实验性"与 API key 的差异。
- v1.1 需要处理浏览器回调、刷新、多账号、provider 限制与条款。

## ADR-008 · 本地 Web GUI 优先

**Context**：三天内做原生 `.app` 的签名/打包/WebView 兼容会吃掉安全链路时间。  
**Decision**：v1 由 daemon 提供本地 Web GUI（127.0.0.1）；Tauri 外壳放 v1.1。  
**Consequences**：

- 交付快、跨平台、易调试。
- "桌面级"体验稍弱；可以在 v1.1 用 Tauri 包一层。
- 必须确保 API 只绑 127.0.0.1，避免 LAN 暴露。

## ADR-009 · 沙箱默认 filtered，而不是断网

**Context**：断网最安全，但会让包安装、公开文档、公开 git、浏览器都不可用，产品局限太大；而 key 隔离并不依赖断网。  
**Decision**：三档网络：`offline` / `filtered`（默认）/ `open`。互联网只经 egress proxy；代理只做 CONNECT + 域名 allowlist，不做 TLS MITM；认证流量走 gateway。  
**Consequences**：

- 日常开发可用；敏感仓库可切 `offline`。
- `open` 有数据外发风险，需明确警告；prompt injection/恶意 Agent 不在威胁模型内。
- 需要维护默认 allowlist 和代理日志。
- 不识别 proxy 的程序会失败（fail closed），v1.1 可用 iptables REDIRECT 兼容。

## ADR-010 · Harness 宿主侧下载、只读挂载

**Context**：沙箱内下载会引入网络、供应链、重复下载；题目要求 harness 不随二进制打包。  
**Decision**：宿主下载固定版本、校验 SHA-256、缓存；只读挂载进沙箱。  
**Consequences**：

- 启动快、可复现、可审计。
- 需要维护 harness 版本清单和适配器。
- 不自动升级；用户确认后切换。

## ADR-011 · Provider registry + 通用 auth 适配器

**Context**：用户希望"密钥支持尽可能全面"，但逐个写 provider 适配器不可持续。  
**Decision**：用 provider registry 描述 `base_url / auth_type / header_name / models`；网关只实现少量 auth 适配器（bearer、header、basic、query、custom）；OpenAI-compatible 覆盖大多数模型 provider。  
**Consequences**：

- 新增 provider 主要是配置，不是代码。
- SigV4（Bedrock）、Service Account（Vertex）需要额外签名适配器，放 v1.1。
- 自定义 endpoint 让用户覆盖长尾 provider。

## ADR-012 · 登录只发生在宿主，沙箱内不登录

**Context**：任何在沙箱内完成的登录都会把 OAuth token 写进沙箱文件/Keychain，直接违反 zero-secret。  
**Decision**：harness/MCP 的登录、导入、刷新都在宿主完成；沙箱只拿 per-sandbox token。  
**Consequences**：

- 沙箱内不出现登录页、不出现 OAuth token。
- 需要处理"宿主已登录/未登录"的状态显示与重新导入。
- 对不支持 API key 的 harness，v1 只能依赖导入或暂不支持。

## ADR-013 · 网络与密钥隔离正交

**Context**：容易把"沙箱安全"等同于"断网"，但真正防 key 泄漏的是"key 不在沙箱 + 认证走网关"。  
**Decision**：允许互联网，但强制经 egress proxy；认证流量（模型/MCP/git）走 gateway；沙箱无默认路由，不能直连。  
**Consequences**：

- 产品可用性显著提升；安全论证仍然成立。
- 需要区分"未认证流量"与"认证流量"，后者不能用通用代理。
- 数据外泄是另一个问题，不在本威胁模型内。

## 开放问题

| # | 问题 | 影响 | 建议 |
|---|---|---|---|
| 1 | OAuth 是否必须进 v1？ | 高 | v1 做广谱 key + 可选导入；若评审明确要求完整流程，砍 Codex/DSH 换单 provider OAuth |
| 2 | GUI 是否必须原生 `.app`？ | 中 | v1 本地 Web；Tauri 放 v1.1 |
| 3 | 停止后 workspace 保留多久？ | 中 | 默认保留 workspace、删除 home；GUI 可选"全删" |
| 4 | 是否承诺 Windows/Linux 时间表？ | 中 | 只承诺 `SandboxProvider` 接口；不承诺日期 |
| 5 | MCP catalog 做多宽？ | 中 | v1 GitHub + Filesystem；Slack/Notion 作为 Should |
| 6 | 是否做 microVM？ | 中 | v1 不做；v0.2 评估 microsandbox / Apple container |
| 7 | 许可证？ | 低 | 当前未加 LICENSE；建议 Apache-2.0（与依赖兼容） |
| 8 | 是否支持多沙箱并行？ | 中 | v1 允许，但资源限制；默认串行更稳 |
| 9 | 默认 allowlist 放多宽？ | 中 | 先覆盖 npm/PyPI/GitHub/常见文档；可切 `open` |
