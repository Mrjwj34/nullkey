# 03 · Harness / MCP / OAuth 接入

> 目标：**Nullkey 生成的 harness 和 MCP 配置不含其管理的真实凭证。** 认证只使用 localhost gateway URL + per-sandbox token；用户自带项目或配置不受此保证覆盖。

## 1. 支持矩阵

| Harness | 优先级 | 模型接入 | 登录 | MCP |
|---|---|---|---|---|
| **Codex** | **Must** | `~/.codex/config.toml` 自定义 provider | API key + 宿主 OAuth | `[mcp_servers.*]` |
| **Claude Code** | Should | `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN` | API key + 宿主导入 | `--mcp-config` / `~/.claude.json` |
| **DSH** | Should | OpenAI-compatible provider | API key | MCP 配置 |
| **桌面端（Codex app 等）** | Should | 由 desktop runtime 提供 | 宿主 OAuth/API key | 视 app 而定 |

> 字段以各 harness 当前版本为准；实现时固定版本 + smoke test。

## 2. Codex（Must）

### 安装

- 宿主侧下载固定版本（npm 包或官方二进制），记录版本 + SHA-256。
- 缓存到 `~/Library/Application Support/Nullkey/cache/harnesses/codex/<version>/`。
- 沙箱内通过只读路径/`PATH` 使用；不随安装包分发。

### 模型配置

`~/.codex/config.toml`（指向 state 目录，按 sandbox 渲染）：

```toml
model_provider = "nullkey"

[model_providers.nullkey]
name = "Nullkey"
base_url = "http://127.0.0.1:<gateway>/openai"
env_key = "NULLKEY_TOKEN"
# wire_api 按 Codex 版本选择 "responses" 或 "chat"
```

沙箱环境：

```bash
export NULLKEY_TOKEN="<per-sandbox-token>"
```

- Gateway 在 `/openai/*` 上把 token 换成真实 API key 或 OAuth access token。
- 不在配置文件里写真实 key；token 只从 env 读取。

### 登录

| 方式 | v1 | 说明 |
|---|---|---|
| API key | ✅ | 最简单；用户填在 GUI |
| 宿主 OAuth | ✅ Must | 宿主浏览器完成；token 存 Keychain；gateway 注入/刷新 |
| 沙箱内 `codex login` | ❌ | 会把 OAuth token 写进沙箱 |

### MCP 配置

```toml
[mcp_servers.github]
url = "http://127.0.0.1:<gateway>/mcp/github"
bearer_token_env_var = "NULLKEY_TOKEN"
```

- 只支持 stdio 时，用 `nullkey-mcp-bridge`。
- 配置里没有 GitHub token，只有 per-sandbox token。

### 状态

- `~/.codex` 映射到持久化 state 目录；token 不写入。
- 停止沙箱保留 state；下次启动重新渲染配置、轮换 token。
- 用户可明确确认后清除 state 内的配置/会话/历史；workspace 保留，活动任务使用中的 state 不可清理。

## 3. Claude Code（Should）

### 模型配置

```bash
export ANTHROPIC_BASE_URL="http://127.0.0.1:<gateway>/anthropic"
export ANTHROPIC_AUTH_TOKEN="<per-sandbox-token>"
```

- Gateway 替换成真实 Anthropic key（`x-api-key`）或 OAuth token。
- 透传 `anthropic-version` / `anthropic-beta`。

### MCP 配置

`~/.claude.json` 或 `--mcp-config`：

```json
{
  "mcpServers": {
    "github": {
      "type": "http",
      "url": "http://127.0.0.1:<gateway>/mcp/github",
      "headers": { "Authorization": "Bearer <per-sandbox-token>" }
    }
  }
}
```

### 登录

- v1 支持 API key。
- 宿主 `claude setup-token`（若版本支持）或 Keychain 导入作为 Should。
- 不在沙箱内 `claude login`。

## 4. DSH（Should）

- 配置 OpenAI-compatible provider：`base_url = http://127.0.0.1:<gateway>/openai`，`api_key = <per-sandbox-token>`。
- MCP 配置同上。
- DSH 一般用 API key，没有订阅 OAuth 问题。
- **只有 WebUI 时**：启动 `dsh web`（或等价命令）绑定 `127.0.0.1:<port>`；adapter 注册 service；GUI 通过 [service tunnel](09-service-tunnel.md) 打开。
- WebUI 里的模型/MCP 仍走 gateway；页面里没有真实 key。

## 4.1 宿主 CLI / Terraform（v1.1）

`gh` / `aws` / `kubectl` / `terraform` 不把宿主配置挂进沙箱，而是通过 Host Tool Gateway 的 shim 执行；Terraform 的 plan/apply 需要审批。设计见 [host-tools-and-services.md](host-tools-and-services.md)。

## 5. 桌面端 catalog（Should）

- 允许下载额外支持的桌面端（Codex app、其他 GUI 工具）。
- 每个 app 的 catalog 条目声明：
  - `runtime: desktop`（需要虚拟桌面）；
  - 安装源/版本/哈希；
  - 启动命令；
  - 是否需要 OAuth/API key（由 gateway 注入，不填在 app 里）。
- App 安装在宿主缓存/state；桌面 runtime 按需启动；默认 TUI 不启动桌面。
- 桌面 app 的登录态（cookie/token）属于凭证，按 OAuth/Keychain 处理；v1 先支持 Codex app 的公开/API 模式。

## 6. MCP Gateway

### 6.1 两类 MCP

| 类型 | 运行位置 | 凭证 | 例子 |
|---|---|---|---|
| 有凭证 | 宿主侧 | Keychain → 网关注入 | GitHub、Slack、Notion、Postgres |
| 无凭证 | 沙箱侧 | 无 | Filesystem（仅 workspace） |

有凭证的 MCP **绝不能在沙箱里跑**。

### 6.2 连接模型

```text
sandbox: url = http://127.0.0.1:<gateway>/mcp/<connection-id>
         Authorization: Bearer <per-sandbox-token>

gateway: token → sandbox_id
         connection-id → 宿主侧 MCP server + 真实凭证
         转发 JSON-RPC；注入 header/env；返回结果
```

- 每个 connection 有 tool allowlist。
- MCP server stdout/stderr 留在宿主；stderr 脱敏。
- 崩溃 → 503；不自动换 server。

### 6.3 v1 catalog

| Server | 认证 | 优先级 |
|---|---|---|
| **GitHub** | OAuth（推荐）或 PAT | Must |
| **Filesystem** | 无（沙箱侧） | Must |
| **Slack** | OAuth（预注册 app） | Should |
| **Notion** | OAuth（预注册 app） | Should |
| **Postgres** | connection string | Could |

### 6.4 stdio bridge

给只支持 stdio 的 harness：

```text
sandbox: nullkey-mcp-bridge
  stdin/stdout JSON-RPC
  → HTTP POST http://127.0.0.1:<gateway>/mcp/<id>
  → Authorization: Bearer <per-sandbox-token>
```

bridge 不含凭证，只做转发。

## 7. OAuth

### 7.1 架构

```text
GUI: "Connect Codex/GitHub"
  → 宿主浏览器 OAuth（Authorization Code + PKCE，state）
  → localhost callback
  → access/refresh token 存 Keychain
  → Gateway 注入 / 到期刷新
  → 沙箱里只有 per-sandbox token
```

### 7.2 v1 范围

| Provider | v1 | 说明 |
|---|---|---|
| **Codex（OpenAI）** | **Must** | Codex 开源；宿主 OAuth 或导入；刷新 |
| **MCP OAuth（DCR）** | Should | 支持动态客户端注册的 MCP server |
| **Claude Code** | Should | `setup-token`/Keychain 导入；完整订阅流程视 provider |
| **GitHub** | Must | OAuth 或 PAT；MCP 用 |
| **Slack/Notion/Google** | v1.1 | 需要预注册 OAuth app |

### 7.3 规则

- 登录只发生在宿主；沙箱内不出现登录页。
- OAuth token 不进配置文件、不进日志、不进终端。
- 刷新失败 → `reauth_required`；GUI 提示重新登录。
- provider 条款/限制由用户确认；不绕过 provider 的客户端限制。

### 7.4 为什么不做"所有 provider 一次到位"

- 每个 provider 的 OAuth app 注册、scope、刷新、条款都不同。
- 预注册 app 是产品/合规工作，不只是代码。
- v1 先把 Codex + GitHub + 通用 MCP DCR 跑通；其余逐个加。

## 8. 网络行为（harness 视角）

默认 `filtered`：未认证流量经 egress proxy，认证流量经 gateway。

| 行为 | 可用性 | 说明 |
|---|---|---|
| `npm install` / `pip install` / `cargo` / `go get` | ✅ | 走 proxy；默认 allowlist 覆盖主流 registry |
| 公开文档/网页 | ✅ | 走 proxy |
| `git clone` 公开 repo | ✅ | 走 proxy |
| 私有 `git clone/push` | ✅ | remote 指向 gateway；token 不进沙箱 |
| 带认证的 `curl`/SDK | ❌ | 沙箱里没有 key；用 MCP/tool/gateway |
| `gh` / `aws` 等认证 CLI | ❌ v1 | 需要 credential helper |
| SSH git | ❌ v1 | 用 HTTPS remote |
| 桌面 app 登录 | 视 app | 登录态按凭证处理 |

- `HTTP_PROXY/HTTPS_PROXY=http://127.0.0.1:<proxy>`、`NO_PROXY=127.0.0.1,localhost`。
- 不识别 proxy 的程序失败（fail closed）。
- 浏览器登录 cookie 是凭证；v1 只建议公开浏览。

## 9. 凭证注入检查清单

每个 sandbox 启动后检查 Nullkey 生成的配置和环境；结果展示范围、排除项和时间，不对整个用户项目作“无秘密”结论：

- [ ] `env` 只有 `NULLKEY_TOKEN`、`NULLKEY_GATEWAY`、`NULLKEY_PROXY`、禁用遥测开关。
- [ ] `~/.codex/config.toml` / `~/.claude.json` 只有 gateway URL + token。
- [ ] MCP 配置只有 gateway URL + token。
- [ ] Nullkey 不把宿主 `~/.ssh`、`~/.aws`、`~/.config/gh`、`.env` 复制到生成配置中；workspace 自带文件不在检查范围。
- [ ] 没有宿主 home/Keychain 访问。
- [ ] `git config` 没有真实 credential。
- [ ] Nullkey 不向命令行或 shell history 写入所管理的真实 key。
- [ ] 发布测试在无自带凭证的受控项目中检查导出文件，注册测试凭证无命中；不把此前缀扫描当作用户目录安全证明。

## 10. 兼容性测试

每个 harness 固定版本：

1. 启动 → 发一条 "hello" → 模型响应。
2. 调一个 MCP tool → 成功。
3. `env` / 配置检查 → 只有 token。
4. 停止 → 重放 → 401。
5. 撤销凭证 → 调用失败。
6. OAuth 过期 → `reauth_required` → 重新登录恢复。
7. 升级 harness → 重跑以上。
