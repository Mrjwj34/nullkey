# AGENTS.md

> 给在本仓库工作的编码 Agent 的规则。先读本文件，再读
> [docs/11-kernel-implementation-plan.md](docs/11-kernel-implementation-plan.md) 与
> [docs/12-acceptance.md](docs/12-acceptance.md)。

## 1. 使命与不变量

Nullkey 是 macOS 上的本地 Agent 沙箱。**唯一不可妥协的产品保证**：

> 真实 API key、OAuth token、password、SSH key 永远不进入沙箱。
> 沙箱里只有 per-sandbox token；真实凭证只在宿主 Keychain + gateway。

任何改动如果让凭证可能进入沙箱、日志、审计、终端或 argv，直接拒绝。

## 2. 硬边界

- **GUI 在 `apps/nullkey-gui`，与 kernel 同仓异责**。GUI 只能通过
  [`api/openapi.yaml`](api/openapi.yaml) 描述的端点访问 daemon：不读宿主凭证文件、
  不碰 Keychain、不绕过 control API。改 GUI 不等于可以顺手改契约；需要新端点就走
  §7 的流程。daemon 侧改动不要为了迁就 GUI 临时放宽契约或鉴权。
- **不要修改 OpenAPI 语义**。设计期修订已完成（`services_only`、取消端点、删除语义）；
  之后的 v1 只做加法。删除/重命名字段、改错误码、改路径需要 ADR + GUI 团队确认。
- **不要实现 v1.1 的 Host Tool Gateway/Terraform**。`gh`/`aws`/`kubectl`/`terraform`
  走宿主 shim 是 v1.1；v1 不新增工具端点，不修改当前 OpenAPI。见
  [docs/13-host-tools-and-services.md](docs/13-host-tools-and-services.md)。
- **不要把 `services_only` 说成离线**。三档网络都保留 gateway；它只关闭通用 egress。
- **不要为了通过测试而弱化断言**。先改断言/契约，再改实现。
- **不要在仓库里放真实凭证**。测试用 `sk-test-*`、fake token；不要用宿主
  Keychain 里的真实值。
- **不要用 `security` 命令行传 secret**（argv 会泄漏）；走 Security.framework /
  cgo wrapper / Keychain API。
- **不要默认绑 `0.0.0.0`**；control API、gateway、proxy、tunnel 都只绑 loopback。
- **不要无沙箱裸跑**。Seatbelt/profile 失败时启动失败，不降级。
- **不要记录请求 body、Authorization、Cookie、query string**。日志只记
  method/path/status/bytes/duration/request_id。
- macOS Apple Silicon 是主要目标；Linux 仅用于 CI 编译/单测，不承诺 host 支持。

## 3. 读什么

按顺序：

1. [docs/00-product.md](docs/00-product.md) — 产品与范围。
2. [docs/01-threat-model.md](docs/01-threat-model.md) — 范围/非保证/验证方法。
3. [docs/02-architecture.md](docs/02-architecture.md) — 组件、信任边界。
4. [docs/08-api-contract.md](docs/08-api-contract.md) + [api/openapi.yaml](api/openapi.yaml) — GUI 契约。
5. [docs/10-interaction-contract.md](docs/10-interaction-contract.md) — 用户可观察行为。
6. [docs/11-kernel-implementation-plan.md](docs/11-kernel-implementation-plan.md) — 实施计划。
7. [docs/12-acceptance.md](docs/12-acceptance.md) — 验收断言。
8. [docs/13-host-tools-and-services.md](docs/13-host-tools-and-services.md) — v1.1 边界。
9. [docs/06-decisions.md](docs/06-decisions.md) — 已确认 ADR。

## 4. 仓库结构

```text
cmd/nullkeyd/             daemon 入口
internal/api/             control-plane HTTP（路由/中间件/错误/DTO）
internal/contract/        OpenAPI ↔ 路由表契约测试
internal/domain/          领域类型/枚举
internal/id/              ID/token 生成
internal/kernel/          配置、token 装载、组装、listener 生命周期
internal/ports/           adapter 接口
internal/version/         版本（ldflags）
test/acceptance/          黑盒验收（-tags acceptance）
api/openapi.yaml          GUI 契约（冻结）
apps/nullkey-gui/         macOS GUI（Flutter + Forui，只经 OpenAPI 访问 daemon）
docs/                    设计/计划/验收
```

未来 adapter 的目录规划见计划 §5；不要预先创建空目录，按里程碑落地。

## 5. 常用命令

```bash
make check                # gofmt + vet + unit + build + contract + acceptance compile
make test                 # go test ./...
make test-race            # go test -race ./...
make build                # bin/nullkeyd
make run                  # 本地启动 nullkeyd
make contract             # OpenAPI ↔ 路由契约
make acceptance-ci        # 非严格验收（当前无 pending，等价严格）
make acceptance           # 严格验收（任何失败都阻断）
make acceptance-compile   # 只编译验收代码
make release-compile      # 编译 release 套件（-tags acceptance release）
make release-product      # release-only：产品能力断言
make release-slo          # release-only：SLO gate（严格）
make coverage             # 全局 Go 覆盖率门禁（≥90%）
```

GUI（在 `apps/nullkey-gui` 下）：

```bash
flutter pub get
flutter analyze           # 必须干净
flutter test              # widget/单测
flutter build macos --release
```

普通 PR CI 并行跑 static/build/unit/acceptance/coverage/contract 与 GUI 的
analyze+test+原生构建/启动检查，不跑 product/SLO；覆盖率必须 ≥90%。
发布 GitHub Release 时 `.github/workflows/release.yml` 才运行 product 与 SLO gate，
两者通过后构建并上传发布产物（两个架构的 daemon 压缩包 + `Nullkey.dmg` + 校验和，
ad-hoc 签名，未公证）；不要把 release-only 断言塞回 PR CI。

本地启动：

```bash
NULLKEY_HOME=/tmp/nullkey-dev NULLKEY_DAEMON_TOKEN=dev-token-0123456789abcdef \
NULLKEY_SECRET_BACKEND=file \
  go run ./cmd/nullkeyd --listen 127.0.0.1:49152
```

- token 优先读 `NULLKEY_DAEMON_TOKEN`；未设置时生成/复用
  `<NULLKEY_HOME>/daemon-token`（0600）。
- 本地开发/CI 用 `NULLKEY_SECRET_BACKEND=file`。未签名 daemon 重建后 macOS 可能
  对 Keychain 条目弹一次授权；拒绝后负缓存 60 秒并失败关闭，不要靠反复重试。
- 不要把 token 放进命令行参数。
- 健康检查：`curl -s http://127.0.0.1:49152/api/v1/health`。

## 6. Go 约定

- Go 1.24+；`gofmt` 必须干净；`go vet ./...` 必须干净。
- 标准库优先。新增依赖前说明理由，并更新计划 §4/§15。
- 包路径全部 `internal/`（除 `cmd/`），避免外部依赖。
- 错误：`fmt.Errorf("...: %w", err)`；API 层统一映射错误码，不把内部错误直接返回。
- `context.Context` 作为第一个参数；不要存进 struct；请求取消要传到 runtime/process。
- 不要用全局可变状态；依赖通过 struct 注入。
- 日志用 `log/slog`，结构化字段；禁止拼接 secret。
- HTTP handler 只做 transport：解析/校验/映射；业务在 service/port。
- 新增 API DTO 用显式 struct + json tag；不要用 `map[string]any` 作为长期契约。
- 并发：每个 sandbox 一个 supervisor；PTY pump 独立 goroutine；共享状态加锁/事务。
- 时间统一 UTC；响应时间 RFC 3339；ID 带前缀。

## 7. 契约工作流

`api/openapi.yaml` 是 GUI 的唯一真相。kernel 路由表必须与它完全一致：

- 新增/修改端点：先改 `docs/08-api-contract.md` 与 `api/openapi.yaml`，再改
  `internal/api/routes.go`，最后 `make contract`。
- 端点必须真实实现，不要返回伪造成功；`desktop` 是唯一允许长期 `501 desktop_unavailable` 的端点。
- 每个响应带 `X-Request-Id`；错误严格用 `{error:{code,message,details?,request_id}}`。
- `POST` 支持 `Idempotency-Key`；分页用 `{items,next_cursor}`。
- `/oauth/callback` 是浏览器回调，免 daemon token；用一次性 `state` 防伪。
- 安装取消走 `POST /jobs/{id}/cancel`；OAuth 取消走 `POST /oauth/flows/{id}/cancel`；
  取消与提交互斥，迟到回调不得写凭证。
- workspace reset 已移除；workspace 删除默认只删记录，`delete_files=true` 只限
  `managed` 且匹配 `confirm_name`；external 永不删文件。
- sandbox `DELETE` 不接受 `delete_state/delete_workspace`；state `DELETE` 必须 `confirm=true`。
- 修改 `openapi.yaml` 必须在 PR 里注明对 GUI 的影响。

## 8. 验收工作流（TDD）

1. 在 [docs/12-acceptance.md](docs/12-acceptance.md) 找到/新增 AC ID。
2. 在 `test/acceptance` 写/启用对应 `TestAC_*`；未实现时用 `pending(t, id, reason)`。
3. 先跑 `make acceptance` 看失败；再实现。
4. 实现后移除 `pending`，跑 `make acceptance`（严格）通过。
5. 产品能力用 `TestProduct_*`，SLO 用 `TestSLO_*`，放在 `//go:build acceptance && release`；
   普通 PR CI 不执行它们。
6. 更新 `docs/12` 状态列；如行为变化，先更新契约与 `docs/10`。
7. 安全类 AC（`AC-SEC-*`）不允许豁免。

验收测试是黑盒：真实启动 `nullkeyd`，不 import 业务实现（domain/ports 除外）。

## 9. 安全审查清单（每个 PR）

- [ ] 新增路径是否可能读到宿主 home/SSH/Keychain？
- [ ] 新增日志/审计/错误是否含 body/header/token/secret？
- [ ] 新增 listener 是否 loopback？CORS 是否只允许 loopback origin？
- [ ] token 是否只存哈希？停止/重启是否失效？
- [ ] 失败时是否 fail closed（不直连、不裸跑、不静默降级）？
- [ ] 新依赖是否必要、维护正常、无凭证泄漏风险？
- [ ] 测试是否使用 fake 凭证？
- [ ] `make check` 与 `make acceptance`（严格，若该里程碑）是否通过？

## 10. 子系统注意事项

### Runtime / Seatbelt

- 先验证 `sandbox-exec` profile；失败则 cgo `sandbox_init`；都失败就 fail closed。
- profile `deny default`；只允许系统库、harness 安装、workspace/state/cache；
  network 只允许 loopback gateway/proxy 端口。
- 沙箱 `HOME` 指向 state/home；不要挂宿主 home。
- 进程组 kill；daemon 退出要清理子进程。
- core dump 关闭。

### Keychain

- Service `com.nullkey.credentials`；Account = credential UUID。
- 不用 argv；值只在 API 请求体/内存/Keychain 中出现。
- Keychain 不可用时凭证操作失败关闭；测试用内存实现。
- 只返回 fingerprint（末 4 位/哈希前缀），永不返回明文。

### Gateway

- 三个 listener 分离：control / gateway / proxy；token 不跨平面。
- gateway 是唯一读取 secret 的 data-plane 组件；不把 secret 写回响应/日志。
- 流式转发，不整包缓冲；只记录元数据。
- 未知 provider/MCP/repo 一律拒绝；不猜测、不转发到默认上游。

### Egress proxy

- 只做 CONNECT + allowlist；不做 TLS MITM；不装 CA。
- 拒绝 loopback/私有网段/link-local 目标。
- `services_only` 关闭通用 egress，但 gateway 仍联网；`open` 需显式警告；`filtered` 默认。
- 三档模式都保留模型/MCP/git 的 gateway 路径；不要写成离线。

### Terminal

- PTY 归 daemon 管理；WS 断线只 detach，不杀进程；无人连接时持续 drain。
- 新 ticket attach 同一 `terminal_id`/PTY，替换旧写连接；旧连接收 `terminal_replaced`。
- 先发 `ready`（含 `terminal_id`）再接受输入；attach 后即使尺寸相同也触发 SIGWINCH。
- 不保存服务器端 scrollback；不重放输入；daemon 重启先停遗留进程再释放锁。

### Service Supervisor / Tunnel / Artifacts

- Service Supervisor 按 manifest 依赖顺序启动/健康检查/注册；停止时先服务后 harness。
- 服务只绑 `127.0.0.1` 的分配端口；生成式 token/密码停止即失效；native 不宣称强隔离。
- 只转发声明服务；一次性 ticket；只绑 loopback。
- 校验 Origin；不信任客户端 `X-Forwarded-*`。
- 文件 API 只读；canonicalize + symlink 解析后仍须在 workspace 内。
- 不把宿主服务注册进 sandbox registry；Host Tool Gateway 是 v1.1，不在 v1 实现。

## 11. 当前状态（P0–P9 已实现）

已实现：

- `nullkeyd` 三平面：control API（49152）、gateway（49153）、egress proxy（49154），全部只绑 loopback。
- SQLite（WAL + immediate tx）持久化 workspace/state/sandbox/lock/credential/MCP/service/job/oauth-flow/audit/idempotency。
- native runtime：Seatbelt（cgo `sandbox_init`，失败关闭）、PTY、进程组、SIGWINCH、优雅停止、重启对账。
- terminal：一次性 ticket、WS `ready`/`terminal_id`、单写连接、`terminal_replaced`、无 scrollback。
- credentials：macOS Keychain（cgo Security.framework，无 argv）、加密文件 CI fallback、只返回 fingerprint。
- gateway：模型（OpenAI/Anthropic/Codex）、MCP（JSON-RPC/SSE/tool allowlist）、GitHub smart HTTP；流式转发。
- gateway TLS：同一 loopback 端口 mux 明文 HTTP 与本地 CA 签发的 HTTPS；CA 只通过
  `CODEX_CA_CERTIFICATE` 提供给沙箱，不装系统信任库。
- Codex ChatGPT：导入保留 access/id/refresh token + account id，JWT `exp` 作为
  `expires_at`，过期前刷新；网关按凭证注入 `ChatGPT-Account-Id` 并剥离沙箱同名头；
  推理路由到 `chatgpt.com/backend-api/codex`；`/backend-api/*` 仅 allowlist。
- 沙箱 Codex 配置由 daemon 渲染：HTTPS provider、`--no-daemon`、
  `daemon_auto_start=false`、关闭 plugins/apps/system proxy/telemetry；并且
  `approval_policy="never"`、`sandbox_mode="danger-full-access"`、workspace
  `trust_level="trusted"`——codex 在我们的 Seatbelt 里不能再套一层自己的沙箱，
  否则每条命令都变成审批提示。隔离由 Nullkey 的 profile 保证，不靠 codex 自觉。
- Seatbelt profile 对 workspace/state/cache 的各级父目录放 `file-read-metadata`
  （`literal`，不是 `subpath`）：解析绝对路径会 lstat 每一段，缺了就连
  `CODEX_HOME` 都 canonicalize 不了。
- OAuth：PKCE、callback、取消/迟到回调互斥、refresh、宿主导入。
- egress：CONNECT/HTTP、三档模式、allowlist、SSRF 拒绝、拒绝审计。
- services：manifest 依赖拓扑、端口范围、健康检查、重启、停止顺序、生成 token。
- tunnel：声明服务、一次性 ticket、cookie session、HTTP/WS/SSE、Origin/路径重写。
- artifacts：只读列表/预览/diff，canonicalize + symlink 防逃逸，1MiB 截断。
- audit/SSE/redaction、scan（仅 generated_runtime）、SLO 基线。
- GUI：`apps/nullkey-gui`（Flutter + Forui）按 OpenAPI 对接 daemon，连接来源为
  dart-define/环境变量 → `<NULLKEY_HOME>/daemon-token`；缺少 daemon 时等待并重试，
  Core Mock 仅在显式设置 `NULLKEY_DEMO=1` 时启用。

未实现/明确不做：

- Host Tool Gateway/Terraform（v1.1）。
- desktop runtime（稳定返回 `501 desktop_unavailable`）。

测试门禁：

- 普通 PR CI：static/build/unit/acceptance（严格）/coverage（≥90%）/OpenAPI contract。
- Release CI：`TestProduct_*` + `TestSLO_*`。
- 本地：`make check`、`make acceptance`、`make release-check`。

## 12. 完成定义

见计划 §15。简要版：

- 端点真实实现，不再 501；
- 对应 AC 严格模式通过；
- `make check` 绿；
- 无新增凭证泄漏面；
- 文档/契约/AGENTS 同步；
- PR 写明测试命令与安全影响。
