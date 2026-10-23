# Nullkey

> 桌面级 Agent 安全沙箱。API key、password 永远不进入沙箱。
>
> A desktop-grade secure sandbox for AI coding agents. Keys never enter the sandbox.

**Status:** design phase · macOS Apple Silicon first · 3-day MVP

## 为什么需要它

Agent session 会以很多方式离开本机：transcript、调试日志、环境变量、MCP 配置、磁盘镜像、崩溃转储、共享桌面、导出的文件。只要真实 key 在沙箱里，任何一次 session 泄漏都可能变成 key 泄漏。

Nullkey 把沙箱变成 **zero-secret zone**：

- 沙箱里没有 API key、没有 password、没有 OAuth token、没有 SSH key。
- 沙箱里的 harness / MCP 配置只有一枚 **per-sandbox token**（可撤销、只在当前沙箱有效）。
- 真实凭证只在宿主机：macOS Keychain + Nullkey daemon。
- 模型请求和 MCP 调用都经过宿主侧网关，由网关在沙箱外注入真实凭证。
- 沙箱默认走 `filtered` 网络：互联网经 egress proxy（域名 allowlist），认证流量走网关。

**即使整个沙箱磁盘/transcript 被拿走，也拿不到真实 key。**

## 怎么工作

```mermaid
flowchart LR
  subgraph Host["宿主机 · 可信"]
    GUI["Nullkey GUI"]
    D["nullkeyd<br/>daemon"]
    KC[("macOS Keychain")]
    MG["Model Gateway"]
    MCP["MCP Gateway"]
    EP["Egress Proxy<br/>allowlist"]
    SM["Sandbox Manager"]
    GUI --> D
    D --> KC
    D --> MG
    D --> MCP
    D --> SM
  end

  subgraph SBX["沙箱 · 不可信（但无需假设 Agent 恶意）"]
    H["Harness<br/>Claude Code / Codex / DSH"]
    DT["Desktop<br/>Xvfb + noVNC"]
    T["per-sandbox token<br/>（配置）"]
    H --> T
  end

  H --> T
  H -->|model / MCP / git| D
  H -->|HTTP(S)_PROXY| EP
  EP -->|allowlist| NET["Internet"]
  DT -->|noVNC| D
  SM --> H
  MG -->|real key| UP["模型 API"]
  MCP -->|real key| MS["MCP Server（宿主侧）"]
```

一次模型调用：

```text
Harness → base_url=http://gateway/anthropic
        → Authorization: Bearer <sandbox-token>
        → Gateway 校验 token，换成真实 Anthropic key
        → 上游 API
        → 流式返回给 Harness
```

一次 MCP 调用：

```text
Harness → http://gateway/mcp/github
        → Authorization: Bearer <sandbox-token>
        → Gateway 找到宿主侧 GitHub MCP server
        → 注入真实 GitHub token，执行 tool call
        → 结果返回给 Harness
```

未认证流量（`npm`/`pip`/公开文档/公开 git）经 egress proxy；认证流量（模型/MCP/git）经 gateway。沙箱没有默认外网路由，不能直连 IP。

## 产品形态

- **本地 GUI**：首页选 harness；一键启动沙箱；浏览器里看虚拟桌面；Keys / MCP / Audit 三个管理页。
- **Harness 不随安装包分发**：从官方源下载到宿主缓存，校验版本/哈希，只读挂载进沙箱。
- **全局密钥（v1）**：所有沙箱共用一组 key；每个沙箱有独立 token、独立审计、可单独撤销。
- **凭证零填写**：MCP 配置、harness 配置、环境变量里都没有敏感信息；用户在 GUI 里填一次，之后不再碰。
- **macOS Apple Silicon 优先**：后端通过 `SandboxProvider` 抽象，后续接 Linux / Windows / microVM。

## 安全边界

**范围内**：session 泄漏导致的 key 泄漏。

- transcript / 日志 / 环境变量 / 进程参数
- harness 配置、MCP 配置、shell history
- 沙箱磁盘、快照、崩溃转储
- 导出文件、共享桌面、截图
- 宿主侧日志与 MCP server 日志

**范围外**（明确不承诺）：

- Agent 被 prompt injection 操控
- Agent 本身恶意、主动滥用自己的权限
- 目标网站/上游 API 被劫持
- 恶意 MCP server 实现
- 沙箱逃逸（提供隔离，但不宣称经过认证的强隔离）
- 通过互联网/MCP/模型通道外发私有数据
- 供应链攻击、物理访问

该取舍是有意为之：三天内把"key 不进沙箱"做到可信、可验证、好用，比声称能防住所有 Agent 攻击更有价值。

## 计划支持

| 类别 | v1 | 说明 |
|---|---|---|
| Harness | Claude Code、Codex、DSH | 通过 base URL + per-sandbox token 接入；API key 为主，订阅登录可实验性导入 |
| MCP | GitHub、Slack、Notion、Postgres；Filesystem | 有凭证的在宿主侧，凭据由网关注入；Filesystem 在沙箱侧（无凭证）；v1 先做 2–3 个 |
| Network | `offline` / `filtered`（默认）/ `open` | 互联网只经 egress proxy；认证只经 gateway |
| Host | macOS Apple Silicon | 后续 Linux / Windows |
| Auth | API key/token 广谱支持；OAuth 宿主登录导入（实验） | 不做完整 OAuth 授权流程；详见 [03](docs/03-harness-mcp.md#6-oauth) |

## 凭证支持

v1 的目标是"常用 key 都能放进来"，不是"只支持两家"。

| 类型 | v1 支持 |
|---|---|
| 模型 API key | Anthropic、OpenAI、DeepSeek、Google Gemini、OpenRouter、Groq、Mistral、xAI、Azure OpenAI、Ollama/LM Studio（无 key）、任意 OpenAI-compatible endpoint |
| MCP token | GitHub PAT、Slack bot token、Notion integration token、Linear/Sentry API key、Postgres connection string、通用 Bearer/Header |
| 认证方式 | Bearer、x-api-key、api-key header、Basic、query、custom header |
| OAuth | **v1 不做完整授权流程**；提供实验性"导入宿主已有登录"（Codex 优先；Claude Code 支持 setup-token/Keychain 导入）。完整宿主侧 OAuth broker + MCP OAuth 放 v1.1 |

为什么 OAuth 不放进 Must：完整 OAuth 要处理浏览器回调、PKCE、动态客户端注册、刷新、provider ToS；Claude Code 闭源，Codex 虽开源但把订阅 token 用在第三方网关有条款风险。详见 [03 · Harness 与 MCP 接入](docs/03-harness-mcp.md#6-oauth)。

## 文档

- [00 · 产品形态与范围](docs/00-product.md)
- [01 · 威胁模型与安全保证](docs/01-threat-model.md)
- [02 · 架构](docs/02-architecture.md)
- [03 · Harness 与 MCP 接入](docs/03-harness-mcp.md)
- [04 · 三天实施计划](docs/04-plan.md)
- [05 · 调研与选型](docs/05-research.md)
- [06 · 决策记录与开放问题](docs/06-decisions.md)

## 三天 MVP

Must：

- 本地 GUI + daemon；Colima/Docker 沙箱 + 虚拟桌面。
- Keychain 全局密钥；模型网关；Claude Code 跑通。
- 宿主侧 MCP 网关 + GitHub MCP；配置里零敏感信息。
- 启动/停止/删除沙箱；per-sandbox token 撤销；审计。
- "沙箱无密钥"检查 + 演示脚本。

Should：Codex / DSH 适配、第二个 MCP、Tauri 外壳。

Won't：OAuth、Windows/Linux host、多用户、microVM、原生安装包。

## 开发

TBD（docs-first，代码随后）。

## License

TBD。
