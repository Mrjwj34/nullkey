# 00 · 产品形态与范围

## 1. 一句话

**Nullkey 是一个桌面级 GUI：用户选一个 harness，一键启动一个没有密钥的 Linux 沙箱；harness 和 MCP 的所有敏感配置都由宿主侧网关代填，沙箱里只有一枚可撤销的 per-sandbox token。**

## 2. 目标用户

- 用 Claude Code / Codex / DSH 的个人开发者。
- 不是安全工程师，不想写 YAML、不想配代理、不想理解 mTLS。
- 愿意把 key 填进一个本地 GUI，但不愿意把 key 放进 agent 的会话、配置或磁盘。
- 用 macOS Apple Silicon，重视"一键"和"看得见的桌面"。

## 3. 产品形态

- 本地 GUI：先支持 macOS Apple Silicon，架构不绑定 macOS。
- 用户选择要启动的 harness（Claude Code / Codex / DSH 等）。
- Harness 不随 Nullkey 安装包分发，按需下载。
- 点击启动后创建沙箱；Agent 的读写、网络、桌面操作都限制在沙箱内。
- GUI 统一管理全局密钥；v1 所有沙箱共用一组 key。
- 浏览器内可打开沙箱虚拟桌面。
- 所有 harness/MCP 配置由 GUI 自动生成，用户不需要编辑 YAML 或填写密钥。

## 4. 关键设计取舍

以下取舍决定 v1 的边界：

| # | 设计点 | 最终方案 | 理由 |
|---|---|---|---|
| 1 | Harness 分发 | 宿主侧下载 + 固定版本/哈希 + 只读挂载；沙箱内不下载 | 避免供应链与网络暴露；启动更快、可复现 |
| 2 | 密钥模型 | 全局 key + 每个沙箱独立的 per-sandbox token，可单独撤销、单独审计 | 全局 key 是能力共享，不是密钥共享；token 泄漏影响有限 |
| 3 | 虚拟桌面 | noVNC 只绑 `127.0.0.1`，一次性 ticket；剪贴板/文件传输默认关闭 | 桌面是最大的人工带出通道，默认收敛 |
| 4 | MCP 覆盖 | 宿主侧 MCP Gateway + 精选 catalog；v1 先做 2–3 个 | 架构可泛化，但适配器要逐个做 |
| 5 | 隔离 | 沙箱默认无互联网，只能访问网关 | 最强的 session 泄漏防线，也简化网络策略 |
| 6 | Harness 覆盖 | v1 保证 Claude Code；Codex / DSH 作为 Should | 配置注入都能做，但联调时间要留够 |
| 7 | GUI 形态 | 本地 Web GUI 优先，Tauri 外壳放 v1.1 | 三天内把时间花在安全链路，不花在签名/打包/WebView 兼容 |

### OAuth 的分层策略

OAuth 不是一个开关，至少有三层：

| 层 | 例子 | v1 |
|---|---|---|
| MCP OAuth | 远程 MCP 的 OAuth 2.1 / 动态客户端注册 | 不做；v1 用静态 token |
| Harness 订阅登录 | Claude Pro、ChatGPT Plus | 不做完整流程；实验性导入宿主已有登录 |
| 企业 SSO/OIDC | 团队身份 | 不做 |

**v1 做什么**：

- 广谱 API key/token（见 README）。
- 实验性"导入宿主登录"：
  - **Codex**：读取宿主 `~/.codex/auth.json`（用户授权，路径以版本为准），存 Keychain；网关注入；刷新按 Codex 公开实现适配。
  - **Claude Code**：优先用 `claude setup-token` 生成长期 token（若当前版本支持），或读取宿主 Keychain 中已有的登录；过期后重新导入。
  - 沙箱内永远不发起登录，登录 token 永远不进沙箱。
- 明确标注"实验性"，不承诺所有 provider/版本。

**为什么不做完整流程**：

- 浏览器回调、PKCE、动态客户端注册、刷新、多账号，至少 3–5 天。
- Claude Code 闭源，OAuth 端点/刷新行为没有第三方文档；Codex 虽开源（Apache-2.0），但订阅 token 用于第三方网关有 ToS 风险。
- 三天内做完整 OAuth 会挤掉"零密钥 + 沙箱 + MCP"的主线。

**若 v1 必须包含 OAuth**：只做一个 provider 的宿主导入（建议 Codex），并相应削减 harness/MCP 适配范围。

## 5. 用户旅程

### 首次使用

1. 打开 Nullkey（本地 GUI）。
2. 添加一个模型 key：Anthropic / OpenAI / DeepSeek；点"测试连接"。如果已有 Claude Code / Codex 登录，可以点"导入宿主登录"（实验）。
3. 看到 harness 卡片：Claude Code（未安装）→ 点"安装"；宿主下载、校验、缓存。
4. 连接 MCP：GitHub → 粘贴 fine-grained token（或后续 OAuth）→ 测试。
5. 点"启动"→ 选择 Claude Code → 30 秒内进入沙箱桌面。
6. 在桌面里用 Claude Code；模型和 GitHub 都可用。

### 日常使用

1. 打开 GUI → 最近的沙箱 → "继续"或"新建"。
2. 选 harness → 启动 → 打开桌面。
3. 用完 → "停止"；默认删除 home/transcript；token 立即失效。

### 泄漏演练（也是演示）

1. 在沙箱终端里 `env`、`cat ~/.claude.json`、`grep -R "sk-" ~`。
2. 只能看到 `NULLKEY_TOKEN=<sandbox-token>` 之类的非敏感值。
3. 把整个沙箱 home 打包带走 → 没有真实 key。
4. 在 GUI 点"撤销 key"或"停止沙箱"→ 再用就失败。

## 6. GUI 结构

| 页面 | 内容 | 关键交互 |
|---|---|---|
| Home | harness 卡片、最近沙箱、快速启动 | 安装/启动/继续 |
| Sandbox | 状态、桌面、日志、审计、"扫描密钥" | 打开桌面、停止、删除 |
| Keys | 全局 key 列表（provider、label、last used、auth type） | 添加、导入宿主登录、测试、删除；**永不显示明文** |
| MCP | catalog + 已连接 server | 连接、测试、启用/禁用 |
| Settings | 资源、保留策略、网络、高级 | 默认值即可，不暴露 YAML |
| Audit | 沙箱/凭证/MCP 事件 | 过滤、导出 |

交互原则：

- **不在沙箱里出现任何密钥输入框**。
- 不在 GUI 里提供"查看明文/复制 key"按钮；只显示末四位和指纹。
- 删除沙箱、撤销 key 要二次确认。
- 默认值能覆盖 90% 场景；高级选项折叠。

## 7. 范围

### Must（三天）

- 本地 GUI + daemon 骨架；Colima/Docker 沙箱 + 虚拟桌面。
- Keychain 全局密钥：添加、测试、删除；provider registry 覆盖 8–10 个模型 provider + 通用 auth 类型。
- 模型网关：per-sandbox token → 真实 key；Claude Code 跑通。
- 宿主侧 MCP 网关 + GitHub MCP；沙箱配置零敏感信息。
- 启动/停止/删除沙箱；token 撤销；审计事件。
- "沙箱无密钥"检查；3 分钟演示脚本。

### Should

- Codex、DSH 适配。
- Codex/Claude 宿主登录导入（实验）。
- 第二个 MCP（Filesystem 或 Slack）。
- Key 轮换、审计导出、Tauri 外壳。
- 沙箱资源限制、空闲自动停止。

### Won't

- 完整 OAuth 授权流程；沙箱内登录；MCP OAuth（v1.1）。
- Windows / Linux host。
- 多用户、公网访问。
- microVM / 强隔离认证。
- 原生 `.dmg` 安装包、代码签名、自动更新。
- 完整 MCP catalog。
- prompt injection / 恶意 Agent 防御。

## 8. 体验原则

1. **零密钥输入**：沙箱内任何页面都不要求填 key。
2. **一键**：选 harness → 启动；不需要理解容器、网络、代理。
3. **可见的安全状态**：每个沙箱显示"0 secrets"、token 状态、最近一次扫描。
4. **可撤销**：停止沙箱、撤销 key、删除数据都是一步操作。
5. **失败关闭**：网关不可用、token 过期、MCP 连接失败 → 明确报错，不降级直连。
6. **不假装**：明确告诉用户哪些攻击不在范围内（prompt injection、恶意 Agent）。

## 9. 成功指标

- 首次到第一个沙箱 < 5 分钟。
- 沙箱扫描：注册 key 的命中数 = 0。
- Claude Code、Codex、DSH 至少 2 个跑通（理想 3 个）。
- 至少 2 个 MCP server 可用，配置里无敏感信息。
- 冷启动 < 60 秒，热启动 < 30 秒。
- 3 分钟演示不翻车。

## 10. 待确认

1. OAuth 订阅登录是否必须？如果评审或用户明确要求，v1 需要增加宿主侧 OAuth broker。
2. GUI 是否必须是原生 `.app`？如果必须，Tauri 外壳要占半天到一天。
3. 沙箱停止后，workspace 是否保留？默认删除 home，workspace 可选保留。
4. 是否需要 Windows/Linux 的路线图承诺？
5. 全局 key 是否要按 harness/MCP 做 scope？v1 先全局，v1.1 再做。
