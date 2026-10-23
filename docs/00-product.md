# 00 · 产品形态与范围

## 1. 一句话

**Nullkey 是一个桌面级 GUI：用户选一个 harness，一键启动一个没有密钥的 Linux 沙箱；harness 和 MCP 的所有敏感配置都由宿主侧网关代填，沙箱里只有一枚可撤销的 per-sandbox token。**

## 2. 目标用户

- 用 Claude Code / Codex / DSH 的个人开发者。
- 不是安全工程师，不想写 YAML、不想配代理、不想理解 mTLS。
- 愿意把 key 填进一个本地 GUI，但不愿意把 key 放进 agent 的会话、配置或磁盘。
- 用 macOS Apple Silicon，重视"一键"和"看得见的桌面"。

## 3. 产品形态（按你的描述）

- 基础形态是 GUI；先只支持 Apple M，但选型不绑死 macOS。
- 用户进入页面，选择要启动的 harness（Claude Code / Codex / DSH 等）。
- Harness 不随二进制打包，按需下载。
- 点击启动 → 创建一个沙箱；agent 的操作被隔离在沙箱内。
- GUI 支持配置全局密钥，初版所有沙箱共用。
- 允许连接到沙箱的虚拟桌面。

## 4. 建议的收敛

大方向不需要改。下面 7 条是为了三天内能交付、且不把"安全"做成口号：

| # | 你的设定 | 建议 | 原因 |
|---|---|---|---|
| 1 | Harness 按需下载 | **宿主侧下载 + 固定版本/哈希 + 只读挂载**，不要在沙箱里下载 | 沙箱内下载会把供应链和网络暴露带进来；每次启动都下载也慢 |
| 2 | GUI 配置全局密钥 | 保留；但每个沙箱生成 **独立 per-sandbox token**，可单独撤销、单独审计 | 全局 key 是"能力共享"，不是"密钥共享"；泄漏一个 sandbox token 不应影响其他沙箱 |
| 3 | 连接虚拟桌面 | 保留；noVNC **只绑 127.0.0.1**，一次性 ticket；剪贴板/文件传输默认关闭 | 桌面是最大的"人工带出"通道，默认收敛 |
| 4 | 所有主流 MCP 零敏感信息 | **宿主侧 MCP Gateway + 精选 catalog**；v1 先做 2–3 个 | "所有"是路线图，不是 v1 承诺；架构可以泛化，但适配器要逐个做 |
| 5 | 沙箱严格隔离 | 增加一条：**沙箱默认无互联网，只能访问网关** | 这是最强的"session 泄漏"防线，也简化了网络策略 |
| 6 | 主流 harness | v1 先保证 **Claude Code** 跑通；Codex / DSH 作为 Should | 三个 harness 的配置注入都能做，但联调时间要留够 |
| 7 | GUI | **本地 Web GUI 优先**（daemon 提供页面），Tauri 外壳放 v1.1 | 三天内把时间花在安全链路，不花在签名/打包/WebView 兼容 |

### 最大的产品缺口：OAuth

Claude Code / Codex 的很多用户用的是 **Claude Pro / ChatGPT Plus 订阅登录**，不是 API key。订阅登录的 token 会落在 `~/.claude` / `~/.codex`，如果登录发生在沙箱里，就违反"zero-secret"。

v1 的选择：

- **只支持 API key / gateway token 模式**（推荐，三天可控）。
- 或者做 **宿主侧 OAuth broker**：登录在宿主完成，token 存 Keychain，网关注入并负责刷新；沙箱里只有 per-sandbox token。工作量明显更大，放 v1.1。

文档按"v1 只支持 API key 模式"落；产品页要明确写出来，避免用户以为订阅可以直接用。

## 5. 用户旅程

### 首次使用

1. 打开 Nullkey（本地 GUI）。
2. 添加一个模型 key：Anthropic / OpenAI / DeepSeek；点"测试连接"。
3. 看到 harness 卡片：Claude Code（未安装）→ 点"安装"；宿主下载、校验、缓存。
4. 连接 MCP：GitHub → 粘贴 fine-grained token（或后续 OAuth）→ 测试。
5. 点"启动"→ 选择 Claude Code → 30 秒内进入沙箱桌面。
6. 在桌面里用 Claude Code；模型和 GitHub 都可用。

### 日常使用

1. 打开 GUI → 最近的沙箱 → "继续"或"新建"。
2. 选 harness → 启动 → 打开桌面。
3. 用完 → "停止"；默认删除 home/transcript；token 立即失效。

### 泄漏演练（也是 demo）

1. 在沙箱终端里 `env`、`cat ~/.claude.json`、`grep -R "sk-" ~`。
2. 只能看到 `NULLKEY_TOKEN=<sandbox-token>` 之类的非敏感值。
3. 把整个沙箱 home 打包带走 → 没有真实 key。
4. 在 GUI 点"撤销 key"或"停止沙箱"→ 再用就失败。

## 6. GUI 结构

| 页面 | 内容 | 关键交互 |
|---|---|---|
| Home | harness 卡片、最近沙箱、快速启动 | 安装/启动/继续 |
| Sandbox | 状态、桌面、日志、审计、"扫描密钥" | 打开桌面、停止、删除 |
| Keys | 全局 key 列表（provider、label、last used） | 添加、测试、删除；**永不显示明文** |
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
- Keychain 全局密钥：添加、测试、删除。
- 模型网关：per-sandbox token → 真实 key；Claude Code 跑通。
- 宿主侧 MCP 网关 + GitHub MCP；沙箱配置零敏感信息。
- 启动/停止/删除沙箱；token 撤销；审计事件。
- "沙箱无密钥"检查；3 分钟演示脚本。

### Should

- Codex、DSH 适配。
- 第二个 MCP（Filesystem 或 Slack）。
- Key 轮换、审计导出、Tauri 外壳。
- 沙箱资源限制、空闲自动停止。

### Won't

- OAuth 订阅登录。
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
- 3 分钟 demo 不翻车。

## 10. 待确认

1. OAuth 订阅登录是否必须？如果评委/用户强需求，v1 要改成宿主侧 OAuth broker。
2. GUI 是否必须是原生 `.app`？如果必须，Tauri 外壳要占半天到一天。
3. 沙箱停止后，workspace 是否保留？默认删除 home，workspace 可选保留。
4. 是否需要 Windows/Linux 的路线图承诺？
5. 全局 key 是否要按 harness/MCP 做 scope？v1 先全局，v1.1 再做。
