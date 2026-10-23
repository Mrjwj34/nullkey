# 08 · API 契约 v1

> 本地 daemon ↔ GUI 的唯一契约。GUI 团队可以按本文并行实现，不需要等后端。
>
> Base URL：`http://127.0.0.1:<port>/api/v1`
>
> 认证：`Authorization: Bearer <daemon-token>`（启动时由 GUI/launcher 注入；只绑 127.0.0.1）

## 1. 约定

- JSON，字段 `snake_case`；时间 RFC 3339 UTC。
- ID 前缀：`sbx_`、`ws_`、`st_`、`cred_`、`mcp_`、`flow_`、`job_`。
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
  "network_modes": ["offline", "filtered", "open"],
  "tunnel": true,
  "artifacts": true,
  "oauth_providers": ["codex", "github"],
  "mcp_servers": ["github", "filesystem"]
}
```

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
  "status": "running|succeeded|failed",
  "progress": 0.6,
  "message": "downloading",
  "error": null
}
```

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

```http
POST /workspaces/{id}/reset
{ "confirm": true }
→ 200 { "ok": true }
```

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
```

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

DELETE /sandboxes/{id}?delete_state=false&delete_workspace=false
→ 204
```

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
{ "type": "output", "data": "..." }
{ "type": "exit", "code": 0 }
{ "type": "error", "code": "token_invalid", "message": "..." }
```

- ticket 一次性、30 秒有效；连接后校验 Origin/localhost。
- 终端输出是 UTF-8 文本；控制序列原样透传。

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

### 3.11 无凭证扫描

```http
POST /sandboxes/{id}/scan
→ 200 {
  "ok": true,
  "hits": 0,
  "checks": [
    { "name": "env", "hits": 0 },
    { "name": "config", "hits": 0 },
    { "name": "grep_prefixes", "hits": 0 }
  ],
  "scanned_at": "..."
}
```

> 扫描是启发式验证；保证来自架构（凭证从未进入沙箱）。

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
  "status": "pending|succeeded|failed|expired",
  "credential_id": "cred_123",
  "error": null
}
```

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
data: {"job_id":"job_123","progress":0.6,"message":"downloading"}

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
oauth:   pending → succeeded | failed | expired
```

## 5. GUI 对接顺序

1. `GET /capabilities`、`GET /harnesses`、`GET /providers`。
2. `POST /credentials` / `POST /oauth/start` → 轮询 flow。
3. `POST /workspaces`、`POST /states`。
4. `POST /sandboxes` → WS terminal。
5. `GET /events` 更新状态；`GET /stats` 显示资源。
6. `POST /sandboxes/{id}/desktop`（可选）。

## 6. 兼容性

- v1 内只加字段、不删字段；新增 enum 值需要 GUI 容忍未知值。
- 事件/WS 消息带 `type`；未知类型忽略。
- 2026-10 冻结本契约；后续变更走 `/api/v2` 或 ADR。
