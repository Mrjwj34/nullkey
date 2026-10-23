# 01 · 威胁模型与安全保证

## 1. 目的

本文只回答一个问题：**Nullkey 如何避免把其管理的 key/OAuth token 注入沙箱，从而减少 session 泄漏带出的凭证。**

保证范围是 Nullkey 的凭证存取、配置渲染和网关注入路径。用户已有 workspace（如 `.env`）、导入文件、主动粘贴或上游返回内容可能自带凭证；不承诺任意沙箱数据无秘密，也不因扫描为零就允许公开分享。

产品明确把"Agent 被 prompt injection 操控"和"目标网站被劫持"排除在外。这不是忽略安全，而是把有限的三天集中在一个更常见、也更可验证的风险上：**session 泄漏**。

## 2. 资产

| 资产 | 说明 | 存储位置 |
|---|---|---|
| 模型 API key | Anthropic / OpenAI / DeepSeek 等 | macOS Keychain |
| MCP 凭证 | GitHub token、Slack token、数据库密码等 | macOS Keychain |
| OAuth access/refresh token | 宿主侧 OAuth Broker；Codex/GitHub 等 | macOS Keychain |
| per-sandbox token | 沙箱访问网关的能力令牌，不是长期密钥 | 沙箱配置 + daemon 内存/哈希 |
| 沙箱数据 | 代码、transcript、日志、桌面文件 | workspace/state；默认持久化，可清理 |
| 宿主机 | Keychain、文件系统、网络 | 可信计算基（TCB） |

## 3. 范围内：session 泄漏

威胁模型假设 **Agent 不是恶意的**，但 session 可能以各种方式离开沙箱：

- 用户把 transcript / 日志发给别人，或上传到 issue。
- workspace / state / 备份被复制。
- 崩溃转储、core dump、swap 被拿走。
- 用户共享屏幕、截图、录屏。
- 用户把沙箱里的文件导出到宿主，或反向导入。
- 恶意/粗糙的 harness 把配置写进日志。
- MCP server 把 header 写进 stderr。
- 宿主侧日志、代理日志、审计日志被读取。
- 模型上下文被 provider 记录（transcript 里不应有 key）。

**核心断言**：Nullkey 不把其管理的真实 key 注入沙箱；该路径不会向 session 提供真实 key。用户自带数据及宿主日志需分别处理，不能从这一断言推导“整个目录无凭证”。

## 4. 范围外

以下攻击不在 v1 的承诺内，文档和 UI 都要明确说：

| 不在范围 | 原因 |
|---|---|
| Agent 被 prompt injection 操控 | 需要能力约束/人审/目的绑定，三天做不扎实；且与"session 泄漏"是不同问题 |
| Agent 本身恶意、主动滥用自己的权限 | 同上；提供隔离，不承诺"防内鬼" |
| 目标网站/上游 API 被劫持 | 供应链/服务端信任问题，不在本地沙箱能解决的范围 |
| 恶意 MCP server 实现 | 不向它暴露 key，但不审计它的全部行为 |
| 沙箱逃逸（determined attacker） | 提供 Seatbelt 进程沙箱，不宣称 VM 级隔离 |
| 供应链攻击（harness/MCP/镜像） | 固定版本/哈希、最小镜像，但不做完整 SLSA |
| 物理访问、宿主 root | 超出应用层能力 |
| 用户主动把 key 粘贴进 agent 聊天 | UI 会警告，但无法阻止用户行为 |
| 通过允许的 MCP/模型通道或互联网外发私有数据 | 这是数据外泄，不是 key 泄漏 |

## 5. 泄漏向量与对策

| # | 向量 | 例子 | 对策 | 残余 |
|---|---|---|---|---|
| 1 | Harness transcript | `~/.claude/projects/*.jsonl`、`~/.codex/sessions` | 沙箱里只有 per-sandbox token | 无 key；token 可撤销 |
| 1b | Harness 登录 token | Claude / Codex 的 OAuth token 存在宿主 | 只导入宿主 Keychain；网关注入；沙箱内从不登录 | 宿主存储；provider ToS |
| 2 | 环境变量 / 进程参数 | `env`、`ps e`、`/proc/*/environ` | 只注入 `NULLKEY_TOKEN`；真实 key 不进入进程 | 无 |
| 3 | Harness 配置 | `~/.claude.json`、`~/.codex/config.toml` | 配置里只有 gateway URL + token | 无 |
| 4 | MCP 配置 | `mcpServers.*.env` 里的 token | MCP 配置只有 gateway URL + token；真实 server 在宿主 | 无 |
| 5 | shell history | `~/.zsh_history` | 不把 key 写进命令行；只写 token | 无 |
| 6 | workspace/state | 整个项目目录/会话目录被复制 | Nullkey 不写入其管理的真实 key/OAuth token | 用户自带凭证、私有代码仍可能泄漏 |
| 7 | 崩溃转储 | core dump 捕获进程内存 | 沙箱内只有 token；禁用 core dump | 无 |
| 8 | 桌面/截图/剪贴板 | 用户截到配置、复制 env | 配置里只有 token；剪贴板默认关闭 | 用户主动导出 token |
| 9 | 导出文件 | 用户把 state/workspace 打包 | Nullkey 生成的配置只引用能力 token；停止即失效 | 不保证导出包没有用户自带凭证 |
| 10 | 宿主日志 | daemon 打印 Authorization | 统一 redaction；不记录 body | 实现 bug 风险 |
| 11 | MCP server 日志 | 第三方 server 打印 header | server 在宿主；stderr 重定向 + redaction；不挂进沙箱 | 第三方自身日志 |
| 12 | 模型上下文 | key 被写进 prompt | 沙箱里没有 key；网关不把 key 放进响应 | 无 |
| 13 | 多沙箱共享 | 一个沙箱的 token 被另一个用 | token 绑定 sandbox；网关校验 | 同一沙箱内共享 |
| 14 | 用户粘贴 | 用户把 key 发进聊天 | UI 提示"不要在对话里粘贴 key"；不承诺阻止 | 用户行为 |
| 15 | 沙箱网络外发 | 未认证流量经 proxy；认证流量经 gateway | egress allowlist + 日志；`open` 模式警告 | 数据外泄不在范围 |
| 16 | 服务隧道暴露 | WebUI 被 LAN/其他应用访问 | 只绑 127.0.0.1；只声明服务；一次性 ticket；Origin 校验 | cookie 仍可能被本机其他进程读取 |

## 6. 安全保证

| ID | 保证 | 验证方式 |
|---|---|---|
| **G1** | Nullkey 不向沙箱环境、进程参数、生成配置或日志注入其管理的 key/OAuth token 明文 | 使用无自带凭证的测试项目，检查生成环境/配置及相关日志 |
| **G2** | harness/MCP 配置只包含 per-sandbox token，不包含真实凭证 | 读取配置模板 + 渲染结果 |
| **G3** | 沙箱没有到 Keychain/宿主文件系统的路径；没有默认外网路由，互联网只经 egress proxy；认证流量只经 gateway | 网络探测 + 挂载检查 |
| **G4** | 停止/删除沙箱立即使 token 失效；workspace/state 默认保留，用户可清理 | 停止后重放请求 → 401 |
| **G5** | 撤销 key 后，所有沙箱的下一次调用失败 | 撤销 + 调用 → 失败 |
| **G6** | 宿主日志/审计不含 key 明文 | 日志扫描 + 单测 |
| **G7** | Nullkey 不把其管理的 key/OAuth token 持久化到 workspace/state | 受控测试项目导出并检查；不覆盖用户自带凭证 |
| **G8** | 服务隧道只暴露声明服务，只绑 loopback，ticket 一次性 | 端口探测 + 未声明服务访问 → 404 |

> G1/G7 约束 Nullkey 的注入行为。扫描是有明确范围的启发式验证，不是整个用户项目的安全证明。

## 7. 非保证

- 不保证 Agent 不会做坏事；Nullkey 不向它注入所管理的真实 key。
- 不保证上游 API 不记录用户数据；Nullkey 不向沙箱注入所管理的 key/OAuth token 明文。
- 不保证沙箱绝对无法逃逸；只保证默认没有宿主 home/Keychain 访问、没有默认外网路由。
- 不保证用户不会自己把 key 贴进聊天或导出到不安全的地方。
- 不保证 per-sandbox token 不被滥用；它是能力令牌，泄漏后可在 GUI 撤销。

## 8. 为什么这样取舍

三天内，同时做"防 prompt injection"和"防 session 泄漏"通常两个都做不好。prompt injection 需要：

- 工具调用级能力绑定、目的地 allowlist、人审；
- 对模型输出做语义判断；
- 持续的红队和策略调优。

这些在演示里很难证明，而且会显著增加配置复杂度，违背"不能太技术化"的产品要求。

相反，**Nullkey 管理的凭证随 session 泄漏**可以通过不注入凭证来减少风险；用户代码、自带凭证和能力 token 仍需保护。演示和 UI 都应使用这一限定范围。

## 9. 验证方法

### 自动化检查（运行时与发布验收）

1. 运行时检查 Nullkey 生成的环境和配置；不把真实凭证作为检查工具参数传入沙箱。
2. 结果返回 `scope=generated_runtime`、排除项与检查时间；用户 workspace/导入/主动输入不在此检查范围，检查失败不能返回零命中。
3. 读取 harness/MCP 配置：只允许 gateway URL + `NULLKEY_TOKEN`。
4. 停止沙箱 → 重放模型/MCP 请求 → 401。
5. 撤销 key → 下一次调用失败。
6. 宿主日志扫描：无 key 明文。
7. 发布验收用不含自带凭证的测试项目导出 workspace/state，再检查注册测试凭证未被写入；这不是面向任意用户目录的保证。

### 手工演示

- 沙箱终端里 `env`、`cat ~/.codex/config.toml`、`cat ~/.claude.json`。
- 把 state 目录打包下载，打开给观众看：只有 token。
- 在 GUI 里撤销凭证，回到终端继续用 → 失败。

> 注意：扫描是启发式验证，不能证明“绝对没有”。UI 应显示“生成配置/环境未发现注册凭证”，同时展示排除项；不能显示“整个项目无密钥”。

## 10. 设计选择：透明网关与工具级能力引用

v1 采用**透明网关**，不要求模型理解任何占位符语法：

- **模型网关**：harness 使用 per-sandbox token 作为 API key；网关替换成真实 key。
- **MCP 网关**：MCP 配置指向网关；真实 server 在宿主侧，凭据由网关注入。
- 沙箱内的配置只有 gateway URL + token。

**工具级能力引用**（例如 `cred://provider/account`）用于 Agent 直接调用自定义工具/CLI、且工具参数必须携带 key 的场景。它属于 v1.1 的扩展，不是 v1 的主路径：主流 harness 的模型调用和 MCP 调用都可以用 base URL + token 透明代理解决。v1 不要求模型学习特殊格式，也不需要模型配合。
