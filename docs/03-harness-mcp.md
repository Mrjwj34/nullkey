# 03 · Harness 与 MCP 接入

> 目标：**沙箱里的 harness 和 MCP 配置，没有任何敏感信息。** 所有配置只含 gateway URL + per-sandbox token。

## 1. 支持矩阵（v1）

| Harness | 模型接入 | MCP 接入 | 状态 |
|---|---|---|---|
| **Claude Code** | `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN` | `--mcp-config` / `~/.claude.json`，HTTP transport | Must（先跑通） |
| **Codex** | `~/.codex/config.toml` 的 `model_providers` + `env_key` | `[mcp_servers.*]` | Should |
| **DSH** | provider 配置指向 OpenAI-compatible gateway | MCP 配置 | Should |

> 具体字段以各 harness 当前版本为准；实现时用固定版本 + smoke test 固化。下表给出的是"注入点"，不是最终 CLI 参数。

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

Claude Code 的订阅登录会在 `~/.claude` 写 OAuth token。**v1 不支持在沙箱里登录**；只支持 API key / gateway 模式。宿主侧 OAuth 见第 6 节。

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

## 6. OAuth（v1.1）

### 为什么 v1 不做

- Claude Code / Codex 的订阅登录会在沙箱里写 OAuth token；这直接违反 zero-secret。
- 宿主侧 OAuth 需要：浏览器回调、token 刷新、provider 特定 header、过期处理；三天做不扎实。
- API key 模式已经能覆盖"开发者用 API 计费"的场景。

### v1.1 设计草案

```text
GUI: "Sign in with Claude" → 宿主浏览器 OAuth
  → token 存 Keychain
  → Gateway 用 OAuth token 注入上游
  → 到期前用 refresh token 刷新
  → 沙箱里仍然只有 per-sandbox token
```

风险：

- provider 可能限制第三方客户端/网关；
- OAuth token 的 scope 可能比 API key 大；
- 刷新失败要让用户重新登录；
- 需要处理多账号。

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
