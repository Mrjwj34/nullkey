# 08 · API 契约 v1

> 本地 daemon ↔ GUI 的契约草案。GUI 可按本文做 mock；完成 [交互验收](10-interaction-contract.md) 并与 OpenAPI 对齐后冻结，不把未定义的行为留给各端自行决定。
>
> Base URL：`http://127.0.0.1:<port>/api/v1`
>
> 认证：`Authorization: Bearer <daemon-token>`（启动时由 GUI/launcher 注入；只绑 127.0.0.1）
>
> OpenAPI：[../api/openapi.yaml](../api/openapi.yaml)

## 1. 约定

- JSON，字段 `snake_case`；时间 RFC 3339 UTC。
- ID 前缀：`sbx_`、`ws_`、`st_`、`cred_`、`mcp_`、`flow_`、`job_`、`term_`。
- 破坏性变更升 `/api/v2`；v1 只做加法。
- POST 支持 `Idempotency-Key`；重复 key 返回同一结果。
- 分页：`?limit=50&cursor=...`，响应 `{items, next_cursor}`。
- 所有响应带 `X-Request-Id`；错误带同一个 id。
- 长任务返回 `202 + job`；进度走 SSE。

## 2. 错误

```json
{
  "error": {
    "code": "state_locked",
    "message": "workspace is locked by another sandbox",
    "details": { "workspace_id": "ws_123", "locked_by": "sbx_456" },
    "request_id": "req_789"
  }
}
```

| code | HTTP | 场景 |
|---|---|---|
| `invalid_request` | 400 | 参数/JSON 错误 |
| `unauthorized` | 401 | daemon token 无效 |
| `not_found` | 404 | 资源不存在 |
| `conflict` | 409 | 名称/端口/资源冲突 |
| `state_locked` | 409 | workspace/state 被占用 |
| `token_invalid` | 401 | per-sandbox token 无效 |
| `reauth_required` | 401 | OAuth 过期/撤销 |
| `runtime_unavailable` | 503 | native runtime 启动失败 |
| `desktop_unavailable` | 501 | 未启用 desktop runtime |
| `service_not_found` | 404 | 服务未声明/未启动 |
| `ticket_invalid` | 401 | 隧道 ticket 过期/复用 |
| `network_denied` | 403 | egress 规则拒绝 |
| `job_failed` | 500 | 安装/OAuth/构建失败 |
| `terminal_not_ready` | 409 | PTY 尚未可写，可稍后重新申请 ticket |
| `internal` | 500 | 未分类 |

## 3. 资源

### 3.1 Health / capabilities

```http
GET /health
→ 200 { "ok": true, "version": "0.1.0", "uptime_ms": 12345 }
```

```http
GET /capabilities
→ 200 {
  "runtimes": ["native"],
  "desktop_available": false,
  "network_modes": ["services_only", "filtered", "open"],
  "tunnel": true,
  "artifacts": true,
  "oauth_providers": ["codex", "github"],
  "mcp_servers": ["github", "filesystem"]
}
```

`services_only` 显示为“仅允许已连接服务”：关闭通用 egress，模型/MCP/git 仍可经 gateway 联网；不是离线模式。`filtered`/`open` 同样保留 gateway。能力列表只返回当前 runtime 能实际执行的模式。

### 3.2 Harnesses

```http
GET /harnesses
→ 200 {
  "items": [{
    "id": "codex",
    "name": "Codex",
    "kind": "tui",
    "version": "0.x.y",
    "installed": true,
    "runtime": "native"
  }]
}
```

```http
POST /harnesses/{id}/install
{ "version": "0.x.y" }
→ 202 { "job_id": "job_123" }
```

```http
GET /jobs/{job_id}
→ 200 {
  "id": "job_123",
  "type": "harness.install",
  "status": "queued|running|cancelling|cancelled|succeeded|failed",
  "cancellable": true,
  "progress": 0.6,
  "message": "downloading",
  "error": null
}
```

```http
POST /jobs/{job_id}/cancel
→ 202 { "id": "job_123", "type": "harness.install", "status": "cancelling", "cancellable": true, "progress": 0.6 }
```

- harness/app 安装必须可取消；其他 job 用 `cancellable` 声明能力。已进入取消流程的重复请求返回当前 job，不重复执行。
- `cancelled` 只在下载/子进程停止、临时文件清理后发出；保留原已安装版本和完整校验过的共享缓存。取消不得留下半安装版本。
- 取消与最终安装提交互斥；已经成功/失败或不可取消返回 `409 conflict`，GUI 显示实际状态，不谎报“已取消”。进度/状态通过 `job.progress` 和 `GET /jobs/{job_id}` 获取。

### 3.3 Apps（桌面端 catalog）

```http
GET /apps
→ 200 { "items": [{
  "id": "codex-app",
  "name": "Codex App",
  "kind": "desktop",
  "installed": false,
  "requires_desktop": true
}]}
```

```http
POST /apps/{id}/install
→ 202 { "job_id": "job_..." }
```

```http
POST /sandboxes/{id}/apps/{app_id}/launch
→ 202 { "job_id": "job_..." }   // desktop runtime 未启动时先启动
```

### 3.4 Workspaces

```http
GET /workspaces
→ 200 { "items": [{
  "id": "ws_123",
  "name": "example",
  "root_path": "/Users/me/code/example",
  "storage_kind": "external",
  "default_mode": "rw",
  "size_bytes": 123456,
  "last_sandbox_id": "sbx_456",
  "locked_by": null,
  "created_at": "2026-10-23T05:00:00Z",
  "last_used_at": "2026-10-23T05:10:00Z"
}]}
```

```http
POST /workspaces
{ "name": "example", "root_path": "/Users/me/code/example" }
→ 201 { "id": "ws_123", ... }
```

- `root_path` 省略时创建在 `~/Library/Application Support/Nullkey/workspaces/<id>`。
- 自定义路径必须通过路径校验；禁止 home/SSH/Keychain。
- `storage_kind` 由 daemon 固定为 `managed`（省略 root_path 创建）或 `external`（用户指定目录）；GUI 展示实际路径和读写权限。

```http
DELETE /workspaces/{id}
→ 204   // 仅移除记录，保留磁盘文件

DELETE /workspaces/{id}?delete_files=true&confirm_name=example
→ 204   // 仅 managed：删除目录全部内容及记录
```

- `delete_files=true` 必须匹配当前 workspace 名称；缺失/不匹配返回 `400 invalid_request`。external 无论是否确认都拒绝删除文件，GUI 只提供“从列表移除”。
- managed 删除包括 `.git`、未提交和未跟踪文件，无法撤销；GUI 先展示完整路径与影响。
- 任一活动 sandbox（含只读）引用时返回 `409 state_locked`；检查与操作互斥。不隐式停止任务。
- v1 不提供 workspace reset；避免把“重置”解释为不同范围的文件删除。

产物审查（只读）：

```http
GET /workspaces/{id}/files?path=src/
→ 200 { "items": [{ "name": "main.ts", "type": "file", "size": 1234, "mtime": "..." }] }

GET /workspaces/{id}/file?path=src/main.ts
→ 200 { "content": "...", "encoding": "utf-8", "truncated": false, "mime": "text/plain" }

GET /workspaces/{id}/diff
→ 200 { "patch": "diff --git ...", "files": [...] }
```

- 路径必须 canonicalize 到 workspace；拒绝 symlink 逃逸。
- 只读；写操作走 harness。

### 3.5 States

```http
GET /states?harness_id=codex
→ 200 { "items": [{
  "id": "st_123",
  "harness_id": "codex",
  "path": "/Users/me/Library/.../states/codex/st_123",
  "size_bytes": 12345,
  "last_sandbox_id": "sbx_456",
  "created_at": "...",
  "last_used_at": "..."
}]}
```

```http
POST /states
{ "harness_id": "codex", "name": "default" }
→ 201 { "id": "st_123", ... }
```

```http
POST /states/{id}/reset
{ "confirm": true }
→ 200 { "ok": true }

DELETE /states/{id}?confirm=true
→ 204
```

reset 清除该 state 内的配置、会话和历史，保留记录，下次启动重新渲染配置；delete 同时删除目录和记录。均不影响 workspace；`confirm` 必须为 true，GUI 确认框明确损失范围。活动 sandbox 引用时返回 `409 state_locked`，与启动互斥。

### 3.6 Sandboxes

```http
POST /sandboxes
{
  "harness_id": "codex",
  "workspace_id": "ws_123",
  "state_id": "st_123",
  "runtime": "native",
  "network_mode": "filtered",
  "mounts": [
    { "source": "workspace", "target": "/workspace", "mode": "rw" },
    { "source": "state", "target": "/state", "mode": "rw" },
    { "source": "cache", "target": "/cache", "mode": "ro" }
  ]
}
→ 201 {
  "id": "sbx_456",
  "status": "starting",
  "runtime": "native",
  "network_mode": "filtered",
  "workspace_id": "ws_123",
  "state_id": "st_123",
  "created_at": "..."
}
```

```http
GET /sandboxes
→ 200 { "items": [ ... ] }

GET /sandboxes/{id}
→ 200 {
  "id": "sbx_456",
  "status": "running|starting|stopped|failed",
  "harness_id": "codex",
  "runtime": "native",
  "network_mode": "filtered",
  "workspace_id": "ws_123",
  "state_id": "st_123",
  "pid": 12345,
  "started_at": "...",
  "last_error": null
}
```

```http
POST /sandboxes/{id}/stop
→ 200 { "ok": true }

DELETE /sandboxes/{id}
→ 204
```

停止/删除都结束进程并撤销 token；delete 另移除 sandbox 记录。两者始终保留 workspace/state，不接受 `delete_state/delete_workspace` 级联参数（传入返回 `400 invalid_request`）。数据清理使用独立 workspace/state 接口。

### 3.7 Terminal

```http
POST /sandboxes/{id}/terminal
→ 200 {
  "ws_url": "ws://127.0.0.1:<port>/api/v1/terminal/sbx_456",
  "ticket": "one-time-ticket",
  "expires_at": "..."
}
```

WebSocket：

```json
// client → server
{ "type": "input", "data": "ls\r" }
{ "type": "resize", "cols": 120, "rows": 40 }
{ "type": "ping" }

// server → client
{ "type": "ready", "terminal_id": "term_123" }
{ "type": "output", "data": "..." }
{ "type": "exit", "code": 0 }
{ "type": "error", "code": "token_invalid", "message": "..." }
```

- ticket 一次性、30 秒有效；连接后校验 Origin/localhost。
- 终端输出是 UTF-8 文本；控制序列原样透传。
- PTY 归 daemon 管理；关闭/刷新窗口或 WebSocket 断线仅 detach，不停止进程、不撤销 token；daemon 持续读取并丢弃无人接收的输出，不能因没有连接而阻塞 harness。GUI 分开显示“连接已断开”和“沙箱已停止”。
- 重开时重新申请 ticket，attach 到同一 `terminal_id`/PTY；不启动第二个 harness。v1 只允许一个可写连接，新连接通过验证后替换旧连接，旧连接收到 `error: terminal_replaced` 后关闭，停止自动重连并提示“终端已在另一视图打开”，避免相互抢占和重复输入。
- PTY 尚未可写时，ticket 接口返回 `409 terminal_not_ready`；停止后返回 `409 conflict`。`ready` 仅在连接已绑定可写 PTY 后发送；GUI 收到它才启用输入，不能用 HTTP 201 或 WS open 代替。
- v1 不保存服务器端历史输出；每次 attach 后客户端发送当前 resize，daemon 即使尺寸相同也触发 SIGWINCH，请求 TUI 重绘。GUI 提示“已重新连接；断线期间历史输出不可恢复”，不能宣称完整恢复 scrollback。
- daemon 重启会丢失原 PTY 并使 token 失效；重启恢复阶段核对并停止遗留 sandbox 进程，完成后释放锁、将会话标记 stopped。GUI 在核对期间不宣称任务已停止、不允许同一 workspace 再启动；完成后提示原任务已中断，提供用已有 workspace/state 重新启动的入口。持久化会话恢复与连接恢复分开表达。

### 3.8 Desktop（可选）

```http
POST /sandboxes/{id}/desktop
→ 200 {
  "url": "http://127.0.0.1:<port>/desktop?ticket=...",
  "ticket": "...",
  "expires_at": "..."
}
```

- 未启用 desktop runtime / 不支持 → `501 desktop_unavailable`。
- 打开桌面可能触发桌面 runtime 启动；进度走 SSE。
- 桌面只绑 127.0.0.1；ticket 一次性。

### 3.9 Services / WebUI

```http
GET /sandboxes/{id}/services
→ 200 { "items": [{
  "id": "dsh-web",
  "name": "DSH WebUI",
  "protocol": "http",
  "status": "ready|starting|error",
  "websocket": true
}]}
```

```http
POST /sandboxes/{id}/services/{service_id}/ticket
→ 200 {
  "url": "http://127.0.0.1:49123/s/<ticket>/",
  "expires_at": "...",
  "service_id": "dsh-web"
}
```

- 隧道只绑 `127.0.0.1`；ticket 一次性/60s。
- 支持 HTTP、WebSocket、SSE。
- 只暴露声明过的服务；没有任意端口转发。

### 3.10 Stats / SLO

```http
GET /sandboxes/{id}/stats
→ 200 {
  "memory_bytes": 123456789,
  "cpu_percent": 1.2,
  "disk_bytes": 987654321,
  "pid": 12345,
  "uptime_ms": 123456,
  "startup_ms": 820,
  "terminal_rtt_ms_p95": 35
}
```

### 3.11 凭证注入检查

```http
POST /sandboxes/{id}/scan
→ 200 {
  "ok": true,
  "hits": 0,
  "scope": "generated_runtime",
  "excluded": ["workspace", "user_imports", "user_input"],
  "checks": [
    { "name": "env", "hits": 0 },
    { "name": "generated_config", "hits": 0 }
  ],
  "scanned_at": "..."
}
```

> `scope=generated_runtime` 仅检查 Nullkey 生成的进程环境和配置；不检查整个 workspace、用户导入或主动输入。`ok` 表示检查完成，`hits=0` 只表示此范围未发现凭证。检查失败返回错误，不伪装成零命中。GUI 展示检查范围、排除项和时间；不使用“0 secrets”或“可安全分享”结论。

### 3.12 Credentials

```http
GET /providers
→ 200 { "items": [{
  "id": "openai",
  "name": "OpenAI",
  "kind": "llm",
  "auth_types": ["api_key", "oauth"],
  "default_auth": "oauth",
  "base_url": "https://api.openai.com/v1"
}]}
```

```http
GET /credentials
→ 200 { "items": [{
  "id": "cred_123",
  "provider_id": "openai",
  "label": "Codex",
  "auth_type": "oauth",
  "fingerprint": "ab12cd34",
  "status": "connected|expired|revoked",
  "created_at": "...",
  "last_used_at": "..."
}]}
```

```http
POST /credentials
{ "provider_id": "anthropic", "label": "personal", "auth_type": "api_key", "value": "sk-..." }
→ 201 { "id": "cred_123", ... }

POST /credentials/{id}/test
→ 200 { "ok": true, "latency_ms": 123 }

DELETE /credentials/{id}
→ 204
```

- `value` 只在请求体中出现；daemon 写 Keychain，不落日志。
- GUI 永不显示明文；列表只返回 fingerprint。

### 3.13 OAuth

```http
GET /oauth/providers
→ 200 { "items": [{
  "id": "codex",
  "name": "Codex / OpenAI",
  "scopes": ["openid", "profile"],
  "status": "available"
}]}
```

```http
POST /oauth/start
{ "provider_id": "codex", "label": "personal" }
→ 200 {
  "flow_id": "flow_123",
  "auth_url": "https://...",
  "expires_at": "..."
}
```

GUI 打开 `auth_url`；浏览器回调到 daemon 的 localhost callback：

```http
GET /oauth/callback?code=...&state=...
→ 200 text/html（"可以关闭此页面"）
```

```http
GET /oauth/flows/{flow_id}
→ 200 {
  "id": "flow_123",
  "status": "pending|succeeded|failed|expired|cancelled",
  "credential_id": "cred_123",
  "error": null
}
```

```http
POST /oauth/flows/{flow_id}/cancel
→ 200 { "id": "flow_123", "status": "cancelled", "credential_id": null, "error": null }
```

- 取消 pending flow 时使 state/PKCE 上下文失效；迟到 callback 不得写入凭证。取消与凭证保存互斥，成功保存已发生时返回 `409 conflict`，GUI 显示已连接并提供断开入口。
- 重复取消 cancelled flow 返回同一状态；其他终态返回 `409 conflict`。浏览器窗口关闭不等同于取消，GUI 保留“等待授权 / 取消 / 重试”反馈；重试前先取消旧 pending flow。

```http
DELETE /oauth/connections/{credential_id}
→ 204

POST /oauth/connections/{credential_id}/refresh
→ 200 { "ok": true, "expires_at": "..." }
```

宿主已有登录导入（Codex/Claude Code）：

```http
GET /oauth/importable
→ 200 { "items": [{
  "provider_id": "codex",
  "source": "codex_host",
  "available": true,
  "expires_at": "..."
}]}
```

```http
POST /oauth/import
{ "provider_id": "codex", "source": "codex_host", "label": "personal" }
→ 201 { "credential_id": "cred_123", "status": "connected" }
```

### 3.14 MCP

```http
GET /mcp/catalog
→ 200 { "items": [{
  "id": "github",
  "name": "GitHub",
  "auth_types": ["oauth", "pat"],
  "requires_oauth_app": false
}]}
```

```http
GET /mcp/connections
→ 200 { "items": [{
  "id": "mcp_123",
  "server_id": "github",
  "label": "github",
  "status": "connected|error",
  "credential_id": "cred_123",
  "tools": ["search_repositories", "get_file_contents"]
}]}
```

```http
POST /mcp/connections
{ "server_id": "github", "label": "github", "credential_id": "cred_123" }
→ 201 { "id": "mcp_123", ... }

POST /mcp/connections/{id}/test
→ 200 { "ok": true, "tools": [...] }

POST /mcp/connections/{id}/oauth/start
→ 200 { "flow_id": "flow_123", "auth_url": "https://..." }

DELETE /mcp/connections/{id}
→ 204
```

### 3.15 Audit / events

```http
GET /audit?limit=50&cursor=...&type=sandbox.start
→ 200 { "items": [{
  "id": "evt_123",
  "ts": "...",
  "type": "sandbox.start",
  "sandbox_id": "sbx_456",
  "detail": { "harness_id": "codex", "network_mode": "filtered" }
}], "next_cursor": null }
```

```http
GET /events
→ text/event-stream

event: sandbox.status
data: {"sandbox_id":"sbx_456","status":"running"}

event: job.progress
data: {"job_id":"job_123","status":"running","progress":0.6,"message":"downloading"}

event: oauth.status
data: {"flow_id":"flow_123","status":"succeeded"}

event: network.block
data: {"sandbox_id":"sbx_456","host":"evil.example","reason":"not_allowlisted"}

event: service.opened
data: {"sandbox_id":"sbx_456","service_id":"dsh-web"}

event: service.closed
data: {"sandbox_id":"sbx_456","service_id":"dsh-web"}
```

## 4. 生命周期与状态机

```text
sandbox: creating → starting → running → stopping → stopped
                         └── failed
job:     queued → running → succeeded | failed
             └──────┴──→ cancelling → cancelled
oauth:   pending → succeeded | failed | expired | cancelled
```

## 5. GUI 对接顺序

1. `GET /capabilities`、`GET /harnesses`、`GET /providers`。
2. `POST /credentials` / `POST /oauth/start` → 轮询 flow。
3. `POST /workspaces`、`POST /states`。
4. `POST /sandboxes` → 申请 terminal ticket → WS `ready` 后启用输入；非就绪状态显示进度。
5. `GET /events` 更新状态；`GET /stats` 显示资源。
6. `POST /sandboxes/{id}/desktop`（可选）。

## 6. 兼容性

- 冻结后的 v1 只加兼容字段、不删字段；新增 enum 值需要 GUI 容忍未知值。
- 事件/WS 消息带 `type`；未知类型忽略。
- 本次发布前修订将 `offline` 改名为 `services_only`，移除 workspace reset 和 sandbox 级联删除参数；这是设计期修正，不声明已发布客户端兼容。GUI/OpenAPI 对齐并通过 [10](10-interaction-contract.md) 的验收后冻结；冻结后的破坏性变更走 `/api/v2`。
