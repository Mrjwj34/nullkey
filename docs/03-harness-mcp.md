# 03 · Harness 与 MCP 接入

> 目标：**沙箱里的 harness 和 MCP 配置，没有任何敏感信息。** 所有配置只含 gateway URL + per-sandbox token。

## 1. 支持矩阵（v1）

| Harness | 模型接入 | MCP 接入 | 状态 |
|---|---|---|---|
| **Claude Code** | `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN` | `--mcp-config` / `~/.claude.json`，HTTP transport | Must（先跑通） |
| **Codex** | `~/.codex/config.toml` 的 `model_providers` + `env_key` | `[mcp_servers.*]` | Should |
| **DSH** | provider 配置指向 OpenAI-compatible gateway | MCP 配置 | Should |

> 具体字段以各 harness 当前版本为准；实现时用固定版本 + smoke test 固化。下表给出的是"注入点"，不是最终 CLI 参数。

### 1.1 登录模式

| Harness | API key | 宿主登录导入（v1 实验） | 完整 OAuth |
|---|---|---|---|
| **Claude Code** | ✅ `ANTHROPIC_AUTH_TOKEN` | ✅ `claude setup-token` / Keychain 导入 | ❌ v1.1 |
| **Codex** | ✅ `env_key` | ✅ `~/.codex/auth.json` 导入 | ❌ v1.1 |
| **DSH** | ✅ | n/a | ❌ |

原则：**登录只发生在宿主，token 只进 Keychain，网关注入；沙箱内不发起登录。**

### 1.2 模型 provider 矩阵（v1）

| Provider | auth_type | 备注 |
|---|---|---|
| Anthropic | `header` (`x-api-key`) | Claude API |
| OpenAI | `bearer` | 标准 |
| DeepSeek | `bearer` | 标准 |
| Google Gemini | `query` | API key 在 query |
| OpenRouter | `bearer` | 聚合 |
| Groq | `bearer` | 标准 |
| Mistral | `bearer` | 标准 |
| xAI | `bearer` | 标准 |
| Azure OpenAI | `header` (`api-key`) | 需要 endpoint + deployment |
| Ollama / LM Studio | 无 | 本地 endpoint |
| Custom | `bearer` / `header` | 任意 OpenAI-compatible |

AWS Bedrock（SigV4）与 Google Vertex（service account）放 v1.1。


## 2. Claude Code

### 安装（宿主侧）

- 从官方渠道下载固定版本；记录版本号 + SHA-256；缓存到 `~/Library/Application Support/Nullkey/harnesses/claude-code/<version>/`。
- 只读挂载到沙箱 `/opt/nullkey/harnesses/claude-code/`。
- 不随 Nullkey 安装包分发。

### 模型配置

```bash
export ANTHROPIC_BASE_URL="http://gateway:8080/anthropic"
export ANTHROPIC_AUTH_TOKEN="<per-sandbox-token>"
```

- Gateway 收到 `Authorization: Bearer <token>` 后，替换成真实 Anthropic key（`x-api-key`）。
- 如果 Claude Code 还发送其他 Anthropic 头（`anthropic-version`、`anthropic-beta`），原样透传。
- 禁用非必要流量/自动更新（按官方环境变量；实现时确认），避免沙箱尝试联网。

### MCP 配置

渲染到沙箱内的 `~/.claude.json` 或独立 `--mcp-config` 文件：

```json
{
  "mcpServers": {
    "github": {
      "type": "http",
      "url": "http://gateway:8080/mcp/github",
      "headers": {
        "Authorization": "Bearer <per-sandbox-token>"
      }
    }
  }
}
```

- token 是 per-sandbox、可撤销的，不是 GitHub token。
- 如果 Claude Code 只支持 stdio transport，则用 `nullkey-mcp-bridge`：

```json
{
  "mcpServers": {
    "github": {
      "command": "/opt/nullkey/bin/nullkey-mcp-bridge",
      "args": ["--gateway", "http://gateway:8080", "--connection", "github"],
      "env": { "NULLKEY_TOKEN": "<per-sandbox-token>" }
    }
  }
}
```

### 记录位置

- transcript：`~/.claude/projects/.../*.jsonl`（只有 token）。
- 配置：`~/.claude.json`、`~/.claude/settings.json`（只有 token）。
- 停止沙箱默认删除 home，这些文件不会留在宿主。

### OAuth

Claude Code 的订阅登录会在 `~/.claude` 写 OAuth token。**v1 不支持在沙箱里登录**；支持 API key / gateway 模式，订阅登录可通过宿主导入（见第 6 节）。

## 3. Codex

### 安装（宿主侧）

- 固定版本 + SHA-256；缓存；只读挂载。

### 模型配置

`~/.codex/config.toml`：

```toml
model_provider = "nullkey"

[model_providers.nullkey]
name = "Nullkey"
base_url = "http://gateway:8080/v1"
env_key = "NULLKEY_TOKEN"
# wire_api 按 Codex 版本选择 "responses" 或 "chat"
```

沙箱环境：

```bash
export NULLKEY_TOKEN="<per-sandbox-token>"
```

- Gateway 在 `/v1/*` 上把它换成真实 OpenAI key。
- Codex 的 ChatGPT 登录（OAuth）不在 v1 范围。

### MCP 配置

按 Codex 当前版本支持的字段，指向 gateway：

```toml
[mcp_servers.github]
url = "http://gateway:8080/mcp/github"
bearer_token_env_var = "NULLKEY_TOKEN"
```

如果只支持 stdio，用 `nullkey-mcp-bridge`（同 Claude Code）。

### 记录位置

- sessions：`~/.codex/sessions`（只有 token）。
- 配置：`~/.codex/config.toml`（只有 token）。

## 4. DSH

DSH 走 OpenAI-compatible provider 配置：

```text
base_url = http://gateway:8080/v1
api_key  = <per-sandbox-token>
```

MCP 配置同样指向 gateway。具体字段在实现时按 DSH 版本确认。

- DSH 的 transcript/日志在沙箱内，停止即删除。
- DSH 通常用 API key，没有订阅 OAuth 问题。

## 5. MCP Gateway

### 5.1 两类 MCP

| 类型 | 运行位置 | 凭证 | 例子 |
|---|---|---|---|
| **有凭证** | 宿主侧 | Keychain → 网关注入 | GitHub、Slack、Notion、Postgres |
| **无凭证** | 沙箱侧 | 无 | Filesystem（只限 workspace） |

有凭证的 MCP **绝不能在沙箱里跑**：它需要 token，token 就会出现在沙箱的 env/配置/内存里。

### 5.2 连接模型

```text
sandbox 配置:
  url = http://gateway:8080/mcp/<connection-id>
  Authorization: Bearer <per-sandbox-token>

Gateway:
  token → sandbox_id
  connection-id → host-side MCP server + 真实凭证
  转发 JSON-RPC；注入 header/env；返回结果
```

- 一个 sandbox 只能访问 GUI 里启用的 connection。
- 每个 connection 有 tool allowlist（例如 GitHub 只开 `search_repositories`、`get_file_contents`）。
- MCP server 的 stdout/stderr 留在宿主；stderr 脱敏；不挂进沙箱。
- MCP server 崩溃 → 503；不自动换 server。

### 5.3 v1 catalog

| Server | 认证 | 典型 tools | 优先级 |
|---|---|---|---|
| **GitHub** | fine-grained PAT（v1）；OAuth（v1.1） | search repos、read file、list issues | Must |
| **Filesystem** | 无（沙箱侧） | read/write workspace | Must |
| **Slack** | bot token | search、post message | Should |
| **Notion** | integration token | search、read page | Should |
| **Postgres** | connection string | query（只读） | Could |

v1 先做 **GitHub + Filesystem**；Slack/Notion 作为 Should。不要承诺"所有主流 MCP"，架构可以泛化，适配器要逐个做。

### 5.4 stdio bridge

给只支持 stdio 的 harness：

```text
sandbox: nullkey-mcp-bridge
  stdin/stdout JSON-RPC
  → HTTP POST http://gateway:8080/mcp/<id>
  → Authorization: Bearer <per-sandbox-token>
```

- bridge 是极小的 Go/Rust 二进制；不含凭证；只做转发。
- 可以静态编译，放进只读镜像。
- 超时/重连/错误码要可观测。

## 6. OAuth

### 6.1 v1：只做宿主登录导入

v1 不实现 OAuth 授权流程，只提供"导入宿主已有登录"（实验性）：

| Harness | 导入来源 | v1 行为 |
|---|---|---|
| Codex | `~/.codex/auth.json` | 用户授权后读取 → Keychain；网关注入；按开源实现刷新 |
| Claude Code | `claude setup-token` 或宿主 Keychain | 导入长期 token；过期后重新导入；不保证自动刷新 |
| DSH | 无 | 使用 API key |

UI：

- Keys 页面显示"导入宿主登录"（仅 macOS，检测到已安装/已登录的 CLI 时可用）。
- 导入后显示 `Connected` / `Expired`，不显示 token。
- 沙箱内永远不出现登录页。

### 6.2 为什么 v1 不做完整流程

- **工作量**：浏览器回调、PKCE、状态校验、动态客户端注册、刷新、多账号，至少 3–5 天。
- **闭源**：Claude Code 闭源，OAuth 端点/刷新行为没有第三方文档；只能依赖 `setup-token` 或导入已存 token。
- **条款**：Codex 虽开源（Apache-2.0），但把 ChatGPT 订阅 token 用于第三方网关可能违反 provider 条款；需要用户自行确认。
- **MCP**：Slack/Notion 等需要注册 OAuth app；GitHub 可用 PAT。v1 用静态 token 更稳。

### 6.3 v1.1：完整宿主侧 broker

```text
GUI: "Sign in with ..." → 宿主浏览器 OAuth（PKCE + state）
  → localhost callback
  → access/refresh token 存 Keychain
  → Gateway 注入；到期前刷新
  → 沙箱里仍然只有 per-sandbox token
```

MCP OAuth：

- 支持动态客户端注册（DCR）的 server，用通用 OAuth client。
- 需要预注册 app 的 server（Slack/Notion），逐个适配。
- 处理 401 → 重新授权；scope 最小化。

### 6.4 时间估算

| 工作 | 预估 |
|---|---|
| 广谱静态 key + 通用 auth 类型 | 0.5–1 天 |
| Codex 宿主登录导入 + 刷新 | 1–1.5 天 |
| Claude Code token 导入 | 0.5–1 天 |
| 完整 OAuth 授权流程（单 provider） | 2–3 天 |
| MCP OAuth（DCR） | 1–2 天 |

结论：v1 做"广谱静态 key + 可选导入"，完整 OAuth 放 v1.1；否则三天的主线（沙箱 + 网关 + 零密钥）会被挤掉。

## 7. "无敏感信息"检查清单

每个沙箱启动后自动检查：

- [ ] `env` 只有 `NULLKEY_TOKEN`、`NULLKEY_GATEWAY`、harness 的禁用遥测开关。
- [ ] `~/.claude.json` / `~/.claude/settings.json` / `~/.codex/config.toml` 只有 gateway URL + token。
- [ ] MCP 配置只有 gateway URL + token。
- [ ] 没有 `~/.ssh`、`~/.aws`、`~/.config/gh`、`.env`。
- [ ] 没有宿主目录挂载。
- [ ] `git config` 没有 credential helper 指向宿主。
- [ ] shell history 没有 key。
- [ ] 沙箱内 `grep -R` 已知 key 前缀无命中。

## 8. 兼容性测试计划

每个 harness 固定一个版本，跑：

1. 启动 → 发一条 "hello" → 收到模型响应。
2. 调用一个 MCP tool（GitHub search）→ 成功。
3. `env` / 配置检查 → 只有 token。
4. 停止 → 重放 → 401。
5. 撤销 key → 调用失败。
6. 升级 harness 版本 → 重跑以上。

版本升级策略：不自动升级；GUI 显示"有新版本"，用户确认后宿主下载、校验、切换。
