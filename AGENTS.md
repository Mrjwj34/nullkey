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

- **不要动 GUI**。GUI 由独立团队负责；本仓库没有 GUI 代码。唯一协作面是
  [`api/openapi.yaml`](api/openapi.yaml)。
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
make acceptance-ci        # 非严格验收（未实现项 skip）
make acceptance           # 严格验收（未实现项失败，里程碑 gate）
make acceptance-compile   # 只编译验收代码
```

本地启动：

```bash
NULLKEY_HOME=/tmp/nullkey-dev NULLKEY_DAEMON_TOKEN=dev-token-0123456789abcdef \
  go run ./cmd/nullkeyd --listen 127.0.0.1:49152
```

- token 优先读 `NULLKEY_DAEMON_TOKEN`；未设置时生成/复用
  `<NULLKEY_HOME>/daemon-token`（0600）。
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
- 未实现端点返回 `501 not_implemented`，不要返回伪造成功。
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
5. 更新 `docs/12` 状态列；如行为变化，先更新契约与 `docs/10`。
6. 安全类 AC（`AC-SEC-*`）不允许豁免。

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

## 11. 当前状态（P0）

已实现：

- `nullkeyd` 启动/优雅关闭；loopback 校验；token 装载（env 或 0600 文件）。
- `/api/v1/health`、`/api/v1/capabilities`。
- daemon bearer 认证、request id、错误 envelope、CORS、访问日志、panic recover。
- OpenAPI 53 条 path+method 的路由骨架；未实现端点 501（含 job/OAuth cancel）。
- OpenAPI 契约测试、验收 harness/基线断言、CI。

未实现：

- 所有资源 CRUD、SQLite、runtime/Seatbelt/PTY、credentials/Keychain、
  gateway、OAuth、MCP、egress、service supervisor、tunnel、artifacts、SLO。
- Host Tool Gateway/Terraform（v1.1）明确不做。
- 不要在没有对应 AC 的情况下「顺手」实现这些；按 [P1..P9](docs/11-kernel-implementation-plan.md#12-里程碑) 推进。

## 12. 完成定义

见计划 §15。简要版：

- 端点真实实现，不再 501；
- 对应 AC 严格模式通过；
- `make check` 绿；
- 无新增凭证泄漏面；
- 文档/契约/AGENTS 同步；
- PR 写明测试命令与安全影响。
