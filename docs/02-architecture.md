# 02 · 架构

## 1. 总览

```mermaid
flowchart LR
  subgraph HOST["宿主机 · TCB（可信）"]
    GUI["GUI<br/>127.0.0.1"]
    D["nullkeyd<br/>API / 调度 / 审计"]
    KC[("macOS Keychain")]
    MG["Model Gateway"]
    MCPG["MCP Gateway"]
    DP["Desktop Proxy"]
    SM["Sandbox Manager"]
    BR["gateway-bridge<br/>（无密钥）"]

    GUI --> D
    D --> KC
    D --> MG
    D --> MCPG
    D --> DP
    D --> SM
    MG --> BR
    MCPG --> BR
    BR --> D
  end

  subgraph SBX["沙箱 · 不可信（防泄漏，不假设恶意 Agent）"]
    H["Harness<br/>Claude Code / Codex / DSH"]
    CFG["配置<br/>只有 per-sandbox token"]
    DT["Desktop<br/>Xvfb + noVNC"]
    WS[("Workspace / ephemeral home")]
    H --> CFG
    DT --> WS
  end

  H -->|"HTTP: gateway:8080"| BR
  DP -->|"noVNC ws"| DT
  SM --> H
  MG -->|"real key"| UP["模型 API"]
  MCPG -->|"real key"| MS["MCP Server<br/>宿主侧进程"]
```

三个关键点：

1. **沙箱没有互联网**，只有到 `gateway:8080` 的内部路由。
2. **沙箱里只有 per-sandbox token**，没有真实 key。
3. **真实 key 只在宿主**：Keychain + daemon + 宿主侧 MCP server。

## 2. 组件职责

| 组件 | 位置 | 职责 | 不做什么 |
|---|---|---|---|
| GUI | 宿主 | harness 选择、key/MCP 管理、沙箱控制、桌面、审计 | 不把 key 传给沙箱 |
| `nullkeyd` | 宿主 | API、调度、配置渲染、审计、日志脱敏 | 不把 key 写日志 |
| Keychain helper | 宿主 | 读写 macOS Keychain | 不走 argv 传 key |
| Model Gateway | 宿主 | 校验 sandbox token，注入真实模型 key，流式转发 | 不记录 body |
| MCP Gateway | 宿主 | 托管/代理 MCP server，注入真实凭证，按连接路由 | 不把凭证返回沙箱 |
| Desktop Proxy | 宿主 | noVNC WebSocket 代理 + 一次性 ticket | 不暴露到 LAN |
| Sandbox Manager | 宿主 | 创建/启动/停止/删除沙箱，渲染配置 | 不挂宿主目录 |
| gateway-bridge | 宿主容器 | 把内部网络的 HTTP 转发到 daemon 的 UDS | 不持有任何密钥 |
| Sandbox | 不可信 | 跑 harness、桌面、workspace | 没有 key、没有互联网 |
| MCP Server | 宿主 | 实际执行 MCP tool；持有自己的凭证 | 不把凭证写回沙箱 |

## 3. 信任边界

- **宿主 = TCB**：`nullkeyd`、Keychain、Model/MCP Gateway、MCP server。
- **沙箱 = 不可信**：我们假设它可能被导出、被读、被复制；不假设 Agent 恶意。
- **上游 = 外部**：模型 API、第三方 MCP 服务；不在本地控制范围。
- **MCP server = 需要凭证的第三方代码**：它必须拿到真实凭证才能工作，因此它本身是"被信任到可以使用凭证"的组件；它不能把凭证回传给沙箱。恶意 MCP server 不在范围内（见威胁模型）。

## 4. 沙箱运行时

### 4.1 v1：Colima + Docker + 自定义桌面镜像

- **Colima**（Apache-2.0）：在 macOS Apple Silicon 上跑一个 Linux VM（Lima/vz），提供 Docker API。
- **Docker**：每个 sandbox 一个容器；容器共享 Colima VM 内核。
- **镜像**：Ubuntu ARM64 + Xfce + Xvfb + noVNC + 常用 CLI（git、node、python、ripgrep）。
- **隔离故事**：容器是内层，Colima VM 是外层；不挂宿主目录、不挂 Docker socket、不挂 Keychain；没有互联网。

为什么不是 Docker Desktop：商业使用需要订阅；Colima 是 Apache-2.0，本地优先。  
为什么不是 microVM：microsandbox / Apple `container` 更未来，但 beta / 桌面 / IPC 有不确定性；v1 先用 Colima 保交付，`SandboxProvider` 留好替换点。

### 4.2 Provider 接口

```go
type SandboxProvider interface {
    EnsureImage(ctx context.Context, image string) error
    Create(ctx context.Context, spec SandboxSpec) (Sandbox, error)
    Start(ctx context.Context, id string) error
    Stop(ctx context.Context, id string) error
    Destroy(ctx context.Context, id string) error
    Exec(ctx context.Context, id string, cmd []string) (ExecResult, error)
    CopyIn(ctx context.Context, id string, src, dst string) error
    Desktop(ctx context.Context, id string) (DesktopEndpoint, error)
    Status(ctx context.Context, id string) (Status, error)
}
```

`SandboxSpec`：harness、image、cpus、memory、workspace size、env（只有 token）、mounts（只读 harness 缓存）。

v0.2 可以增加 `MicroVMProvider`（microsandbox / Apple `container`），GUI 和网关不变。

## 5. 凭证路径

### 5.1 模型调用

```text
Harness 配置:
  base_url = http://gateway:8080/anthropic   (或 /openai)
  token    = <per-sandbox token>

Harness → Gateway:
  Authorization: Bearer <per-sandbox token>

Gateway:
  1. 校验 token → sandbox_id
  2. 查 sandbox 绑定的 provider（anthropic/openai/deepseek）
  3. 从 Keychain 取真实 key
  4. 替换 Authorization（Anthropic 用 x-api-key，OpenAI 用 Bearer）
  5. 转发到上游，SSE 流式回传

Harness 永远看不到真实 key。
```

- 每个 harness 有独立的适配器（见 [03-harness-mcp.md](03-harness-mcp.md)），负责把 base URL/token 写进正确的位置。
- Gateway 不记录请求/响应 body；只记录 provider、model、token 用量、状态码、耗时。
- 模型白名单：sandbox 只能请求 GUI 里启用的模型。
- 失败关闭：token 无效、key 缺失、上游 401 → 直接报错，不回退到直连。

### 5.2 MCP 调用

**宿主侧 MCP（有凭证）**：

```text
GUI: 连接 GitHub → 存 token 到 Keychain → 启动宿主侧 GitHub MCP server
Sandbox 配置:
  url = http://gateway:8080/mcp/github
  Authorization: Bearer <per-sandbox token>

Harness → Gateway → 路由到 GitHub MCP server（注入真实 token）→ 结果回传
```

- MCP server 的 stdout/stderr 留在宿主；stderr 捕获并脱敏，不挂进沙箱。
- Gateway 按连接 ID 路由；一个 sandbox 只能访问 GUI 里启用的连接。
- 支持两种 transport：
  - **Streamable HTTP**：harness 直接连 gateway。
  - **stdio**：沙箱里放一个极小的 `nullkey-mcp-bridge`，把 stdio JSON-RPC 转发到 gateway（同样带 token）。bridge 不含密钥。
- Tool allowlist：每个连接可以限制可用 tool（例如 GitHub 只允许 `search_repositories` / `get_file_contents`）。

**沙箱侧 MCP（无凭证）**：

- Filesystem 这类不需要密钥的 server 可以直接在沙箱里跑。
- 它只能看 workspace，不能看宿主。

### 5.3 虚拟桌面

```text
Sandbox: noVNC 监听 127.0.0.1:<port>
Host:    Desktop Proxy 把 127.0.0.1:<random> 映射到 sandbox 的 noVNC
GUI:     iframe / 新标签打开，带一次性 ticket
```

- 只绑 `127.0.0.1`，不绑 `0.0.0.0`、不暴露 LAN。
- 一次性 ticket（30 秒有效），防止别人猜端口。
- 剪贴板同步默认关闭；文件导入/导出走显式按钮。
- 桌面里的终端可以看到 `NULLKEY_TOKEN`，但看不到真实 key。

### 5.4 CLI / 自定义工具（v1 不做）

`git push`、`aws`、`gh` 这类需要 key 的 CLI 不在 v1 范围。未来用 **credential helper**：

- `git credential helper` 向 daemon 要一个短时、限定 host/repo 的令牌。
- 令牌进入沙箱，但短时、可撤销、权限窄。
- 这不是"零密钥"，是"低价值令牌"；在文档里单独标注。

## 6. Per-sandbox token

- 32 字节随机；Base64URL 编码；只在创建时显示给配置渲染器，不显示在 GUI。
- daemon 只存 `SHA-256(token)`；校验时哈希比对。
- 绑定 `sandbox_id`；不能跨沙箱使用。
- 生命周期 = 沙箱生命周期；停止/删除立即失效。
- 权限 = 该沙箱被允许的模型 provider + MCP 连接。
- 泄漏后的影响：在沙箱存活期间消耗模型/MCP 配额；可一键撤销。**不是长期 key 泄漏**。

## 7. Keychain

- Service：`com.nullkey.credentials`；Account：credential UUID。
- 使用 Security.framework（或一个极小的 Swift helper），**不要把 key 放进 argv**（`ps` 可见）。
- 访问控制：只允许 Nullkey 的签名身份；开发构建会出现系统授权弹窗。
- 元数据 SQLite：id、provider、label、fingerprint（SHA-256 前 8 字节）、created_at、last_used_at、status。
- GUI 永不显示明文；只显示 fingerprint 末四位。
- 删除 key：删 Keychain item + 标记 metadata；下一次调用失败。

## 8. 网络与端口

```text
Host
  nullkeyd
    ├── 127.0.0.1:<api-port>      GUI / REST
    ├── unix socket               gateway-bridge → daemon
    └── 127.0.0.1:<desktop-port>  noVNC proxy

Docker internal network: nullkey-net
  ├── gateway-bridge:8080         （无密钥，只转发到 daemon UDS）
  └── sandbox                    （只能访问 gateway:8080）
```

- `nullkey-net` 使用 Docker `internal` 网络：没有默认路由，没有互联网，只有容器间通信。
- 沙箱的 `/etc/hosts` 或 Docker DNS 解析 `gateway` → bridge。
- 沙箱默认不发布端口；只有 noVNC 通过 Docker 的 `127.0.0.1:<random>` 映射暴露，Desktop Proxy 加一次性 ticket。
- daemon 的 API 只绑 `127.0.0.1`；GUI 不需要远程访问。
- 所有出站（模型、MCP、OAuth）都由宿主侧发起。

## 9. 数据模型与 API

```sql
credentials(id, provider, label, keychain_account, fingerprint, created_at, last_used_at, status)
mcp_connections(id, server_id, label, auth_type, keychain_account, config_json, status)
sandboxes(id, harness_id, status, token_hash, container_id, desktop_port, created_at, last_used_at)
audit_events(id, ts, sandbox_id, type, detail_json)
```

- `config_json` 只存非敏感配置（scopes、repo allowlist、tool allowlist）。
- `detail_json` 不含 key/token 明文。

API 草图：

```text
GET    /api/harnesses
POST   /api/harnesses/:id/install
GET    /api/credentials
POST   /api/credentials              {provider,label,value}
POST   /api/credentials/:id/test
DELETE /api/credentials/:id

GET    /api/mcp/catalog
GET    /api/mcp/connections
POST   /api/mcp/connections          {server_id,label,auth_type,value,config}
POST   /api/mcp/connections/:id/test
DELETE /api/mcp/connections/:id

POST   /api/sandboxes                {harness_id}
POST   /api/sandboxes/:id/stop
DELETE /api/sandboxes/:id
GET    /api/sandboxes/:id/desktop
POST   /api/sandboxes/:id/scan

GET    /api/audit
```

## 10. 生命周期

### 创建

1. GUI 选择 harness，`POST /api/sandboxes`。
2. daemon 生成 sandbox_id + token；插入 DB。
3. 确保镜像（首次拉取/构建）。
4. 创建 ephemeral home + workspace volume。
5. 渲染 harness 配置、MCP 配置（只有 gateway URL + token）。
6. 创建容器（internal network，只读 rootfs，非 root，资源限制）。
7. 启动 Xvfb + noVNC + harness。
8. 健康检查（gateway 可达、noVNC 就绪）。
9. 返回 desktop URL（带 ticket）。

### 停止 / 删除

- 停止：撤销 token → 停止容器 → 默认删除 home/transcript → 保留 workspace（可选）。
- 删除：删除容器 + volumes + DB 记录 + token。
- daemon 重启：**所有 sandbox token 失效**（fail closed）；GUI 提示重建沙箱。

## 11. 失败模式

| 情况 | 行为 |
|---|---|
| Gateway 不可用 | 模型/MCP 调用 502；不直连、不回退 |
| token 无效/过期 | 401；GUI 提示重建沙箱 |
| key 缺失/被撤销 | 424 `credential_unavailable`；提示去 Keys 添加 |
| MCP server 崩溃 | 503；不自动改用其他 server |
| 上游 401/429 | 透传状态；不重试风暴 |
| Colima 未启动 | GUI 引导启动；不静默失败 |
| 桌面端口冲突 | 重新分配随机端口 |
| 沙箱健康检查失败 | 标记 failed；保留日志；允许重试 |

## 12. 加固清单

- 容器：非 root、只读 rootfs、`/tmp` tmpfs、`cap-drop ALL`、`no-new-privileges`、pids/memory 限制。
- 不挂：宿主目录、`~/.ssh`、`~/.aws`、`~/.config/gh`、Keychain、Docker socket。
- 网络：internal only；无 DNS 外网；只有 `gateway`。
- 镜像：固定 digest；无 secret layer；`gitleaks` 扫描；最小化。
- 日志：统一 redaction；不记录 body；MCP stderr 捕获。
- core dump：沙箱内禁用；宿主 daemon 也禁用（避免内存里的 key 落盘）。
- noVNC：127.0.0.1 + 一次性 ticket；不要放到 LAN。
- 剪贴板：默认关闭；文件传输显式。
- 停止：撤销 token + 删除 home；workspace 保留需用户选择。

## 13. 为什么不是其它方案

| 方案 | 为什么不够 |
|---|---|
| 把 key 放 env / `.env` | session 泄漏（transcript、日志、`env`）直接带走 key |
| 只做模型占位符 | 需要模型配合 + schema 兼容；主流 harness/MCP 用 base URL 就能透明解决 |
| 只做出口代理（TLS MITM） | 复杂、证书问题；MCP/harness 配置仍然要填东西 |
| 只做容器隔离 | 隔离不等于没密钥；容器里照样能 `env` |
| 用 Docker Desktop | 商业许可；Colima 更轻、Apache-2.0 |
| v1 直接上 microVM | beta/桌面/IPC 风险；先用 Colima 保交付，接口留好 |

## 14. Harness 集成摘要

每个 harness 一个 adapter，负责：

1. 安装（宿主缓存、版本/哈希、只读挂载）。
2. 配置渲染（base URL、per-sandbox token、MCP 配置路径）。
3. 启动命令 + 环境变量（禁用自动更新/遥测）。
4. 健康检查。

具体配置见 [03-harness-mcp.md](03-harness-mcp.md)。
