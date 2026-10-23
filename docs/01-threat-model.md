# 01 · 威胁模型与安全保证

## 1. 目的

本文只回答一个问题：**哪些情况下 key 会泄漏，Nullkey 如何让这些情况拿不到 key。**

产品明确把"Agent 被 prompt injection 操控"和"目标网站被劫持"排除在外。这不是忽略安全，而是把有限的三天集中在一个更常见、也更可验证的风险上：**session 泄漏**。

## 2. 资产

| 资产 | 说明 | 存储位置 |
|---|---|---|
| 模型 API key | Anthropic / OpenAI / DeepSeek 等 | macOS Keychain |
| MCP 凭证 | GitHub token、Slack token、数据库密码等 | macOS Keychain |
| OAuth access/refresh token | 订阅登录导入（实验）；v1.1 完整 broker | macOS Keychain |
| per-sandbox token | 沙箱访问网关的能力令牌，不是长期密钥 | 沙箱配置 + daemon 内存/哈希 |
| 沙箱数据 | 代码、transcript、日志、桌面文件 | 沙箱卷；停止时默认删除 |
| 宿主机 | Keychain、文件系统、网络 | 可信计算基（TCB） |

## 3. 范围内：session 泄漏

我们假设 **Agent 不是恶意的**，但 session 可能以各种方式离开沙箱：

- 用户把 transcript / 日志发给别人，或上传到 issue。
- 沙箱磁盘、快照、备份被复制。
- 崩溃转储、core dump、swap 被拿走。
- 用户共享屏幕、截图、录屏。
- 用户把沙箱里的文件导出到宿主，或反向导入。
- 恶意/粗糙的 harness 把配置写进日志。
- MCP server 把 header 写进 stderr。
- 宿主侧日志、代理日志、审计日志被读取。
- 模型上下文被 provider 记录（transcript 里不应有 key）。

**核心断言**：如果真实 key 从未进入沙箱，上述任何一种泄漏都拿不到 key。

## 4. 范围外

以下攻击不在 v1 的承诺内，文档和 UI 都要明确说：

| 不在范围 | 原因 |
|---|---|
| Agent 被 prompt injection 操控 | 需要能力约束/人审/目的绑定，三天做不扎实；且与"session 泄漏"是不同问题 |
| Agent 本身恶意、主动滥用自己的权限 | 同上；我们做隔离，不做"防内鬼" |
| 目标网站/上游 API 被劫持 | 供应链/服务端信任问题，不在本地沙箱能解决的范围 |
| 恶意 MCP server 实现 | 我们不让它拿到 key，但不审计它的全部行为 |
| 沙箱逃逸（determined attacker） | 我们做 VM/容器隔离，但不宣称认证级强隔离 |
| 供应链攻击（harness/MCP/镜像） | 固定版本/哈希、最小镜像，但不做完整 SLSA |
| 物理访问、宿主 root | 超出应用层能力 |
| 用户主动把 key 粘贴进 agent 聊天 | UI 会警告，但无法阻止用户行为 |
| 通过允许的 MCP/模型通道外发私有数据 | 这是数据外泄，不是 key 泄漏 |

## 5. 泄漏向量与对策

| # | 向量 | 例子 | 对策 | 残余 |
|---|---|---|---|---|
| 1 | Harness transcript | `~/.claude/projects/*.jsonl`、`~/.codex/sessions` | 沙箱里只有 per-sandbox token | 无 key；token 可撤销 |
| 1b | Harness 登录 token | Claude / Codex 的 OAuth token 存在宿主 | 只导入宿主 Keychain；网关注入；沙箱内从不登录 | 宿主存储；provider ToS |
| 2 | 环境变量 / 进程参数 | `env`、`ps e`、`/proc/*/environ` | 只注入 `NULLKEY_TOKEN`；真实 key 不进入进程 | 无 |
| 3 | Harness 配置 | `~/.claude.json`、`~/.codex/config.toml` | 配置里只有 gateway URL + token | 无 |
| 4 | MCP 配置 | `mcpServers.*.env` 里的 token | MCP 配置只有 gateway URL + token；真实 server 在宿主 | 无 |
| 5 | shell history | `~/.zsh_history` | 不把 key 写进命令行；只写 token | 无 |
| 6 | 沙箱磁盘/快照 | 整个 container/VM 镜像被复制 | 真实 key 从未写入；home 默认临时 | 无 |
| 7 | 崩溃转储 | core dump 捕获进程内存 | 沙箱内只有 token；禁用 core dump | 无 |
| 8 | 桌面/截图/剪贴板 | 用户截到配置、复制 env | 配置里只有 token；剪贴板默认关闭 | 用户主动导出 token |
| 9 | 导出文件 | 用户把 `~/.claude` 打包 | 只有 token；停止即失效 | 无 key |
| 10 | 宿主日志 | daemon 打印 Authorization | 统一 redaction；不记录 body | 实现 bug 风险 |
| 11 | MCP server 日志 | 第三方 server 打印 header | server 在宿主；stderr 重定向 + redaction；不挂进沙箱 | 第三方自身日志 |
| 12 | 模型上下文 | key 被写进 prompt | 沙箱里没有 key；网关不把 key 放进响应 | 无 |
| 13 | 多沙箱共享 | 一个沙箱的 token 被另一个用 | token 绑定 sandbox；网关校验 | 同一沙箱内共享 |
| 14 | 用户粘贴 | 用户把 key 发进聊天 | UI 提示"不要在对话里粘贴 key"；不承诺阻止 | 用户行为 |

## 6. 安全保证

| ID | 保证 | 验证方式 |
|---|---|---|
| **G1** | 沙箱文件系统、env、进程参数、transcript、配置中不存在任何已注册 key/OAuth token 的明文 | 沙箱内 `env`/`/proc`/`grep` + 配置检查 |
| **G2** | harness/MCP 配置只包含 per-sandbox token，不包含真实凭证 | 读取配置模板 + 渲染结果 |
| **G3** | 沙箱没有互联网路由，也没有到 Keychain/宿主文件系统的路径；只能访问网关 | 网络探测 + 挂载检查 |
| **G4** | 停止/删除沙箱会删除 home/transcript，并使 token 立即失效 | 停止后重放请求 → 401 |
| **G5** | 撤销 key 后，所有沙箱的下一次调用失败 | 撤销 + 调用 → 失败 |
| **G6** | 宿主日志/审计不含 key 明文 | 日志扫描 + 单测 |
| **G7** | 即使拿到完整沙箱磁盘镜像，也拿不到 key | 导出镜像 + 扫描 |

> G1/G7 是"由构造保证"的：key 从来没有被传给沙箱。扫描是验证手段，不是唯一防线。

## 7. 非保证

- 不保证 Agent 不会做坏事；只保证它拿不到 key。
- 不保证上游 API 不记录用户数据；只保证 key/OAuth token 不以明文出现在沙箱。
- 不保证沙箱绝对无法逃逸；只保证默认没有宿主挂载、没有互联网、没有 Keychain。
- 不保证用户不会自己把 key 贴进聊天或导出到不安全的地方。
- 不保证 per-sandbox token 不被滥用；它是能力令牌，泄漏后可在 GUI 撤销。

## 8. 为什么这样取舍

三天内，同时做"防 prompt injection"和"防 session 泄漏"通常两个都做不好。prompt injection 需要：

- 工具调用级能力绑定、目的地 allowlist、人审；
- 对模型输出做语义判断；
- 持续的红队和策略调优。

这些在演示里很难证明，而且会显著增加配置复杂度，违背"不能太技术化"的产品要求。

相反，**session 泄漏**是一个可以用架构直接消除的问题：只要 key 不在沙箱里，transcript/disk/log 泄漏都无效。这个断言可验证、可演示、对用户也好解释。

## 9. 验证方法

### 自动化检查（每次启动/停止）

1. 沙箱内 `env`、`/proc/self/environ`、`ps e` 不含注册 key。
2. `grep -R` 已知 key 前缀（`sk-`、`ghp_`、`xoxb-` 等）在 `/home`、`/root`、`/etc`、`/tmp` 无命中。
3. 读取 harness/MCP 配置：只允许 gateway URL + `NULLKEY_TOKEN`。
4. 停止沙箱 → 重放模型/MCP 请求 → 401。
5. 撤销 key → 下一次调用失败。
6. 宿主日志扫描：无 key 明文。
7. 导出沙箱 home/镜像 → 扫描无 key。

### 手工演示

- 桌面终端里 `env`、`cat ~/.claude.json`、`cat ~/.codex/config.toml`。
- 把 `~/.claude` 打包下载，打开给观众看：只有 token。
- 在 GUI 里撤销 token，回到桌面继续用 → 失败。

> 注意：扫描是启发式验证，不能证明"绝对没有"。真正的保证是架构上 key 从未进入沙箱。

## 10. 设计选择：透明网关与工具级能力引用

v1 采用**透明网关**，不要求模型理解任何占位符语法：

- **模型网关**：harness 使用 per-sandbox token 作为 API key；网关替换成真实 key。
- **MCP 网关**：MCP 配置指向网关；真实 server 在宿主侧，凭据由网关注入。
- 沙箱内的配置只有 gateway URL + token。

**工具级能力引用**（例如 `cred://provider/account`）用于 Agent 直接调用自定义工具/CLI、且工具参数必须携带 key 的场景。它属于 v1.1 的扩展，不是 v1 的主路径：主流 harness 的模型调用和 MCP 调用都可以用 base URL + token 透明代理解决。v1 不要求模型学习特殊格式，也不需要模型配合。
