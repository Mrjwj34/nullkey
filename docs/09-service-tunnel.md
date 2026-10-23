# 09 · 服务隧道与产物审查

> 两个方向要分开：
>
> - **Egress（沙箱 → 宿主）**：模型、MCP、私有 git、egress proxy；已有设计。
> - **Ingress（宿主 → 沙箱）**：打开沙箱里的 WebUI/preview/notebook；本文新增。

## 1. 目标

- 让只有 WebUI 的 harness（DSH 等）可用：浏览器经宿主机隧道打开沙箱内的 Web 服务。
- 让 Agent 产物更容易审查：文件树、diff、预览、运行中的 preview。
- 隧道只绑 `127.0.0.1`，一次性 ticket，只暴露声明过的服务；不是通用端口转发。

## 2. 两个方向

| 方向 | 路径 | 认证 |
|---|---|---|
| Egress | sandbox → `127.0.0.1:<gateway>` / `127.0.0.1:<proxy>` | per-sandbox token |
| Ingress | GUI → `127.0.0.1:<tunnel>` → sandbox service | one-time ticket |

## 3. 架构

```text
GUI
 └── http://127.0.0.1:<tunnel>/s/<ticket>/
        │
        ▼
 Tunnel Proxy（宿主，仅 loopback）
        │  校验 ticket、Origin、service_id
        ▼
 Runtime Service
   ├── native:  127.0.0.1:<port>（Seatbelt 限制 bind 到 loopback）
   └── desktop: VM/container 端口转发到宿主 loopback
```

- `Service Registry` 在 daemon 内：每个 sandbox 有哪些服务、端口、协议、健康检查。
- 服务由 harness adapter 声明（Codex app、DSH WebUI、preview server），也支持用户手动注册。
- 隧道不直接暴露端口；URL 带 ticket，ticket 绑定 `sandbox_id + service_id`。

## 4. Service Registry

```json
{
  "id": "dsh-web",
  "sandbox_id": "sbx_123",
  "name": "DSH WebUI",
  "protocol": "http",
  "target_host": "127.0.0.1",
  "target_port": 7860,
  "health_path": "/",
  "websocket": true,
  "auth": "ticket"
}
```

- Adapter 在启动后声明服务；daemon 做健康检查。
- 只允许 `127.0.0.1` / 沙箱内地址；禁止把宿主服务注册进来。
- 动态端口可由 adapter 上报；也支持固定端口。
- GUI 只展示声明过的服务；不提供"扫描端口"。

## 5. 隧道协议

1. GUI `POST /sandboxes/{id}/services/{service_id}/ticket`。
2. daemon 返回：
   ```json
   {
     "url": "http://127.0.0.1:49123/s/<ticket>/",
     "expires_at": "...",
     "service_id": "dsh-web"
   }
   ```
3. GUI 在新标签/iframe 打开；HTTP/WebSocket/SSE 都转发。
4. ticket 一次性或短 TTL（60s）；过期重新申请。
5. 代理只绑 `127.0.0.1`；不监听 LAN。
6. 支持 `Upgrade: websocket`、`text/event-stream`、chunked；超时/大小限制。
7. 请求头处理：
   - 删除 `Host` 中的外部值，重写为目标；
   - 限制 hop-by-hop headers；
   - `Set-Cookie` 重写 Path 到隧道前缀（v1 可先限制为同源）；
   - `X-Forwarded-*` 不信任客户端。

## 6. 安全

- **只暴露声明服务**：不提供任意端口/任意 host 的转发。
- **loopback only**：`127.0.0.1`；不绑 `0.0.0.0`。
- **ticket**：绑定 sandbox/service，短 TTL，一次性；URL 不包含长期凭证。
- **Origin/CSRF**：校验 `Origin`/`Sec-Fetch-Site`；写操作要求同源或 CSRF token。
- **SSRF**：目标只能是 sandbox 内声明的服务；禁止 `file://`、宿主 `169.254.169.254`、localhost 其他端口。
- **Cookie**：WebUI 登录 cookie 留在隧道命名空间；停止沙箱即失效；不要把宿主 cookie 带进去。
- **审计**：service open/close、bytes、status；不记录 body。
- **不共享**：隧道 URL 只在本机有效；不要把带 ticket 的 URL 发出去。

## 7. DSH / WebUI-only harness

DSH 这类只有 WebUI 的 harness：

1. 安装宿主侧固定版本。
2. 启动 `dsh web`（或等价命令），绑定 `127.0.0.1:<port>`。
3. Adapter 注册 service `dsh-web`。
4. 模型配置指向 `http://127.0.0.1:<gateway>/openai`；MCP 指向 gateway。
5. GUI 打开 service tunnel URL → 直接用 DSH WebUI。
6. 沙箱里仍然没有真实凭证。

这使 DSH 从"Should"变成"可以接"；是否进 v1 Must 取决于时间。

## 8. 产物审查

不经过隧道也能审查，但隧道让"运行中的 preview"和"文件"在同一个 GUI 里。

### 8.1 文件 API

```http
GET /workspaces/{id}/files?path=src/
→ 200 { "items": [{ "name": "main.ts", "type": "file", "size": 1234, "mtime": "..." }] }

GET /workspaces/{id}/file?path=src/main.ts
→ 200 { "content": "...", "encoding": "utf-8", "truncated": false, "mime": "text/plain" }

GET /workspaces/{id}/diff
→ 200 { "patch": "diff --git ...", "files": [...] }
```

- 路径必须 canonicalize 到 workspace 内；拒绝 symlink 逃逸。
- 二进制/大文件只返回元数据/下载 URL；默认 1MB 截断。
- 只读；写操作走 harness 或明确的 editor，不在审查 API 里做。
- `diff` 用 workspace 的 git；没有 git 则返回文件级变更/空。

### 8.2 GUI

- **Artifacts** tab：文件树、文本预览、图片预览、diff。
- **Services** tab：运行中的 WebUI/preview，点开走隧道。
- 审查不触发模型；内容不进入 prompt。

## 9. API 摘要

- `GET /sandboxes/{id}/services`
- `POST /sandboxes/{id}/services/{service_id}/ticket`
- `GET /workspaces/{id}/files`
- `GET /workspaces/{id}/file`
- `GET /workspaces/{id}/diff`
- `GET /events` 增加 `service.opened` / `service.closed`

完整字段见 [08-api-contract.md](08-api-contract.md)。

## 10. SLO

| 指标 | 目标 |
|---|---|
| 隧道 RTT（本机） | p95 < 20ms |
| 隧道吞吐 | > 50MB/s（本机） |
| 并发 WebSocket/SSE | ≥ 8 条/沙箱 |
| 文件列表（10k 文件） | p95 < 300ms |
| 文件预览首字节 | p95 < 100ms（1MB 内） |

## 11. 非目标

- 不做公网分享/端口映射。
- 不做任意端口转发/VPN。
- 不把宿主服务暴露给沙箱。
- 不替代 egress proxy/gateway。
- v1 不做多用户/权限模型；隧道只服务本机 GUI。
