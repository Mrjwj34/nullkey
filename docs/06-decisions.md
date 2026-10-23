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
- 答辩时必须主动说明边界，避免被问穿。

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

## ADR-007 · v1 不支持 OAuth 订阅登录

**Context**：Claude Pro / ChatGPT Plus 用户多，但 OAuth token 会落在沙箱，违反 zero-secret；宿主侧 OAuth 刷新复杂。  
**Decision**：v1 只支持 API key / gateway token；OAuth 放 v1.1，做宿主侧 broker。  
**Consequences**：

- 部分用户/评委可能失望；必须在 README 和产品页明确。
- API key 模式可以先证明安全链路。
- v1.1 需要处理浏览器回调、刷新、多账号、provider 限制。

## ADR-008 · 本地 Web GUI 优先

**Context**：三天内做原生 `.app` 的签名/打包/WebView 兼容会吃掉安全链路时间。  
**Decision**：v1 由 daemon 提供本地 Web GUI（127.0.0.1）；Tauri 外壳放 v1.1。  
**Consequences**：

- 交付快、跨平台、易调试。
- "桌面级"体验稍弱；可以在 v1.1 用 Tauri 包一层。
- 必须确保 API 只绑 127.0.0.1，避免 LAN 暴露。

## ADR-009 · 沙箱默认无互联网

**Context**：允许上网会增加 exfiltration、供应链、代理配置的复杂度；我们的威胁模型只关心 key 泄漏。  
**Decision**：沙箱只有到 gateway 的内部路由；模型/MCP 的出站由宿主完成。  
**Consequences**：

- 最强、最简单的隔离；session 泄漏也无法把 key 发出去。
- Agent 不能直接浏览网页；需要网页能力时，后续加宿主侧 web tool。
- harness 的自动更新/遥测要禁用。

## ADR-010 · Harness 宿主侧下载、只读挂载

**Context**：沙箱内下载会引入网络、供应链、重复下载；题目要求 harness 不随二进制打包。  
**Decision**：宿主下载固定版本、校验 SHA-256、缓存；只读挂载进沙箱。  
**Consequences**：

- 启动快、可复现、可审计。
- 需要维护 harness 版本清单和适配器。
- 不自动升级；用户确认后切换。

## 开放问题

| # | 问题 | 影响 | 建议 |
|---|---|---|---|
| 1 | OAuth 是否必须进 v1？ | 高 | 先按 API key 做；如果评委强需求，砍 Codex/DSH，换宿主侧 OAuth |
| 2 | GUI 是否必须原生 `.app`？ | 中 | v1 本地 Web；Tauri 放 v1.1 |
| 3 | 停止后 workspace 保留多久？ | 中 | 默认保留 workspace、删除 home；GUI 可选"全删" |
| 4 | 是否承诺 Windows/Linux 时间表？ | 中 | 只承诺 `SandboxProvider` 接口；不承诺日期 |
| 5 | MCP catalog 做多宽？ | 中 | v1 GitHub + Filesystem；Slack/Notion 作为 Should |
| 6 | 是否做 microVM？ | 中 | v1 不做；v0.2 评估 microsandbox / Apple container |
| 7 | 许可证？ | 低 | 当前未加 LICENSE；建议 Apache-2.0（与依赖兼容） |
| 8 | 是否支持多沙箱并行？ | 中 | v1 允许，但资源限制；默认串行更稳 |
