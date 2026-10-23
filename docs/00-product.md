# 00 · 产品与范围

## 1. 一句话

**Nullkey 是一个轻量的 macOS 沙箱：默认在终端里跑 Codex 等编码 Agent，凭证只存在宿主 Keychain，沙箱里只有 per-sandbox token；需要图形界面时再单独打开虚拟桌面。**

## 2. 目标用户

- 用 Codex、Claude Code、DSH 的个人开发者。
- 不是安全工程师，不想写 YAML、不想配代理。
- 愿意把 key/OAuth 授权放进一个本地 GUI，但不愿意把它们放进 agent 的会话、配置或磁盘。
- 用 macOS Apple Silicon，重视启动速度、内存占用和"一键"。

## 3. 产品形态

- 本地 GUI，默认页面是 **SSH-client 风格的 Terminal**，里面跑 harness 自己的 TUI；虚拟桌面是独立标签/窗口，按需打开。
- 用户选择 harness：Codex（Must）、Claude Code / DSH（Should）。
- 允许从 catalog 下载额外支持的桌面端（Codex app 等），但桌面 runtime 是可选后端。
- WebUI-only harness（DSH 等）通过宿主侧 service tunnel 打开；Agent 产物在 Artifacts 页审查。
- Harness/app 不随安装包分发：宿主侧下载、固定版本/哈希、缓存/只读安装。
- 凭证全局管理：OAuth（宿主侧 broker）或 API key；所有沙箱共用。
- 状态默认持久化：workspace、harness state、cache 可跨沙箱共享（见 [07](07-state-and-mounts.md)）。
- 所有 harness/MCP 配置由后端生成，用户不需要编辑 YAML 或在沙箱里填 key。

## 4. 关键决策

| # | 设计点 | 决策 | 理由 |
|---|---|---|---|
| 1 | Runtime | 默认 **native 进程沙箱（Seatbelt）**，不用 VM | 轻量、启动快、内存低 |
| 2 | 默认界面 | **SSH-client 风格终端页**（xterm.js + PTY），里面跑 harness 的 TUI | 编码 Agent 本来就是 TUI；不需要 Xvfb/noVNC |
| 3 | 虚拟桌面 | 可选 desktop runtime，按需启动，单独计量 | 需要 GUI/桌面应用时才用；不拖累默认 SLO |
| 4 | Harness 优先级 | **Codex Must**；Claude Code、DSH Should | Codex 开源、可定制 provider/OAuth，最容易跑通 |
| 5 | 凭证 | **OAuth Broker 一等公民** + API key | 很多服务不支持 API key |
| 6 | 状态 | workspace / state / cache **持久化 + 可共享** | 方便继续进度、复用登录/缓存 |
| 7 | 网络 | `offline` / `filtered`（默认）/ `open` | 断网局限太大；认证流量走 gateway 即可 |
| 8 | GUI | **不在本设计范围**；后端冻结 API 契约 | App 选型由独立团队负责 |
| 9 | 服务隧道 | 宿主 → 沙箱 ingress，只暴露声明服务，一次性 ticket | 让 WebUI-only harness 可用；产物可审查 |

## 5. OAuth

OAuth 是 v1 的核心能力：

- **宿主侧 OAuth Broker**：Authorization Code + PKCE、localhost callback、token 刷新、Keychain 存储。
- **Codex OAuth（Must）**：Codex 开源，优先支持宿主侧登录/导入；API key 作为 fallback。
- **MCP OAuth（Should）**：支持 OAuth 2.1 + 动态客户端注册（DCR）的 MCP server。
- **Claude Code（Should）**：支持 `setup-token` 或宿主 Keychain 导入；完整订阅流程视 provider 限制。
- **Slack/Notion/Google 等（v1.1）**：需要预注册 OAuth app，逐个接入。
- **沙箱内永远不发起登录**；OAuth token 只进 Keychain，由 gateway 注入。

## 6. 用户旅程

### 首次使用

1. 打开 GUI → 添加凭证：Codex OAuth（宿主浏览器）或 API key；GitHub OAuth 或 PAT。
2. 安装 Codex：宿主下载固定版本，校验，缓存。
3. 创建 workspace（或选择已有目录），选择网络模式（默认 `filtered`）。
4. 启动沙箱 → 直接在 Terminal 页进入 Codex TUI。
5. 需要时点"Open Desktop" → 启动可选桌面后端。

### 继续进度

1. 打开 GUI → 最近的 workspace/sandbox。
2. 直接启动：workspace、harness state、cache 都在；配置重新渲染，token 轮换。
3. 继续之前的会话（若 harness 支持 resume）。

### 泄漏演练（演示）

1. 沙箱终端 `env`、`cat ~/.codex/config.toml`、`grep -R "sk-"` → 只有 `NULLKEY_TOKEN`。
2. 把 workspace/state 打包 → 没有真实 key/OAuth token。
3. 停止沙箱 → token 失效；撤销凭证 → 下一次调用失败。

## 7. GUI 结构

| 页面 | 内容 | 关键交互 |
|---|---|---|
| Home | harness/app 卡片、最近 workspace | 安装、启动、继续 |
| Terminal | 默认页；SSH-client 风格，里面跑 harness TUI | 输入、复制、全屏 |
| Desktop | 可选虚拟桌面（独立窗口） | 打开/关闭、分辨率 |
| Services | 沙箱内声明的 WebUI/preview | 打开隧道 URL、健康状态 |
| Artifacts | workspace 文件树、预览、diff | 审查 Agent 产物 |
| Workspaces | workspace 列表、挂载模式、清理 | 新建、共享、只读挂载 |
| Credentials | OAuth 连接 + API key | 登录、测试、撤销；**永不显示明文** |
| MCP | catalog + 连接 | 连接、OAuth、启用/禁用 |
| Settings | 网络模式、资源、持久化策略 | 默认即可 |
| Audit | 沙箱/凭证/网络事件 | 过滤、导出 |

> GUI 由独立团队实现；本仓库只定义 API、事件和终端协议（见 [08](08-api-contract.md)）。

## 8. 范围

### Must（三天）

- Native runtime（Seatbelt）+ PTY/TUI WebSocket + workspace/state/cache。
- Codex 跑通：API key + OAuth（宿主侧）。
- Keychain 全局凭证；per-sandbox token；撤销。
- 模型 gateway；GitHub MCP；egress proxy + `filtered` 默认。
- 网络三档；状态持久化；API 契约冻结。
- 基础审计 + SLO 基线。

### Should

- Claude Code / DSH。
- MCP OAuth（DCR）。
- 可选 desktop runtime + Codex app。
- Service tunnel + Artifacts 审查。
- 第二个 MCP（Slack/Notion）。
- 状态共享的只读模式、快照/回滚。

### Won't

- 默认 VM / Colima / Docker Desktop。
- Windows / Linux host（不承诺时间表）。
- 多租户、公网访问。
- 完整桌面 app catalog。
- prompt injection / 恶意 Agent 防御。
- 按 harness/MCP 的凭证 scope（v1 全局）。

## 9. 体验原则

1. **默认 TUI**：打开就能用，不先等桌面。
2. **零密钥输入**：沙箱内任何页面都不要求填 key。
3. **状态默认持久化**：关掉再开，workspace/state/cache 还在。
4. **可撤销**：停止沙箱、撤销凭证、清理 state 都是一步操作。
5. **失败关闭**：gateway/token/网络失败 → 明确报错，不降级直连。
6. **不假装**：明确说明哪些攻击不在范围内。

## 10. 成功指标

- 首次到 Codex TUI < 5 分钟。
- 冷启动 p95 < 1.5s；daemon RSS < 80MB；单沙箱额外 < 40MB。
- 沙箱扫描：注册凭证命中数 = 0。
- Codex 跑通；Claude Code/DSH 至少一个。
- 2 个 MCP 可用，配置零敏感信息。
- 3 分钟演示不翻车。

详细指标见 [slo.md](../slo.md)；服务隧道见 [09](09-service-tunnel.md)。

## 11. 已确认决策

| # | 问题 | 决策 |
|---|---|---|
| 1 | OAuth 是否重要？ | **是**。v1 做宿主侧 OAuth Broker，Codex OAuth 为 Must；API key 作为 fallback |
| 2 | GUI/app 选型？ | **不在本设计范围**；由独立团队负责，本仓库冻结 API 契约 |
| 3 | 状态保留？ | **尽可能持久化且共用**：workspace/state/cache 默认持久化，支持共享/只读 |
| 4 | Windows/Linux 时间表？ | **不需要**；先跑 macOS MVP |
| 5 | 按 harness/MCP 的凭证 scope？ | **v1 不做**；全局凭证 + per-sandbox token |
