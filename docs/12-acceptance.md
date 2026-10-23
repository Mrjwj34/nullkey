# 12 · 验收断言

> 目的：在写 kernel 功能之前，先把「什么算完成」变成可执行、可复核的断言。
>
> 断言 ID 稳定，不随实现顺序变化。实现 = 把 `pending` 断言变成真实断言并通过严格模式。
>
> 相关：[Kernel 实施计划](11-kernel-implementation-plan.md) ·
> [交互契约](10-interaction-contract.md) · [API 契约](08-api-contract.md) ·
> [宿主 CLI/沙箱服务/Terraform（v1.1）](13-host-tools-and-services.md) ·
> [SLO](../slo.md) · [AGENTS.md](../AGENTS.md)

## 1. 运行方式

```bash
# 默认 CI：已实现断言必须通过；未实现断言 skip
make acceptance-ci

# 里程碑 gate：未实现断言直接失败（严格模式）
make acceptance

# 只检查验收代码能编译
make acceptance-compile
```

严格模式等价于：

```bash
NULLKEY_ACCEPTANCE_STRICT=1 go test -tags acceptance -count=1 ./test/acceptance
```

### 1.1 约定

- 黑盒：测试启动真实 `nullkeyd`，只通过 HTTP/WS/文件系统观察。
- 测试不读取 daemon 内部状态，除非断言本身要求（如 token 哈希、DB 无明文）。
- **不使用真实凭证**；fake key 只放在测试进程内存。
- 每个断言必须能回答：前置条件、动作、可观察结果、失败信息。
- 时间类断言取 30 次样本的 p50/p95；硬安全断言必须 100% 通过。
- 断言只能新增或标记 `superseded`，不能为了变绿删除。
- `v1.1` 断言不进入 v1 严格模式，也不阻塞 v1 gate。

### 1.2 PR CI 与 release CI

- **普通 PR CI**：只跑 static/build/unit/acceptance（非严格）/OpenAPI contract；
  不跑产品能力与 SLO。
- **Release CI**：`.github/workflows/release.yml` 在 GitHub Release `published`
  时运行 `TestProduct_*` 与 `TestSLO_*`，并上传 SLO 输出。
- Release 套件在 `test/acceptance/release_product_test.go`、
  `release_slo_test.go`，使用 `//go:build acceptance && release`。
- SLO 未实现项在 `NULLKEY_RELEASE_STRICT=1` 下失败；这是发布 gate，不能用 skip
  伪装通过。

### 1.3 状态

| 状态 | 含义 |
|---|---|
| ✅ | 骨架/已实现，CI 默认执行 |
| ⏳ | 已写断言，功能未实现；严格模式失败 |
| 📊 | benchmark/脚本，release CI 或手动运行 |
| 🔍 | 人工/演示检查 |
| 🔜 | v1.1，明确不在 v1 范围 |

## 2. API 与控制面

| ID | 级别 | 断言 | 验证 | 状态 |
|---|---|---|---|---|
| AC-API-001 | P0 | `GET /api/v1/health` 无需 bearer，返回 200，`ok=true`，`version` 非空，响应带 `X-Request-Id`。 | `TestAC_API_001_Health` | ✅ |
| AC-API-002 | P0 | 除 health、OAuth callback、OPTIONS 外，缺少/错误 bearer 返回 401 `unauthorized`；正确 daemon token 放行。 | `TestAC_API_002_Auth` | ✅ |
| AC-API-003 | P0 | 错误体严格为 `{error:{code,message,request_id}}`，`request_id` 等于 `X-Request-Id`；未知路由 404 `not_found`。 | `TestAC_API_003_ErrorEnvelope` | ✅ |
| AC-API-004 | P0 | `GET /capabilities` 返回 `runtimes`（含 `native`）、`network_modes=["services_only","filtered","open"]`；布尔字段存在；数组不为 `null`。 | `TestAC_API_004_Capabilities` | ✅ |
| AC-API-005 | P0 | OpenAPI 中每个 path+method 都有 kernel 路由；无缺失、无多余（53 条）；base path 为 `/api/v1`；`bearerAuth` 为 http bearer；`/oauth/callback` 显式 `security: []`。 | `internal/contract.TestRoutesMatchOpenAPI` | ✅ |
| AC-API-006 | P0 | 非 loopback 监听地址启动失败；CORS 只对 loopback `Origin` 回显；非 loopback `Origin` 返回 403。 | `TestAC_API_006_LoopbackCORS` + config 单测 | ✅ |
| AC-API-007 | P0 骨架 | 未实现端点返回 501 `not_implemented`，不得返回伪造的 200 或空成功；实现后此断言改为该端点的功能断言。 | `TestAC_API_007_UnimplementedExplicit` | ✅ |
| AC-API-008 | P1 | `POST` 携带相同 `Idempotency-Key` 返回同一结果；分页响应为 `{items,next_cursor}`；`limit` 上限生效。 | `TestAC_API_008_IdempotencyPagination` | ⏳ |
| AC-API-009 | P1/P9 | `GET /events` 返回 `text/event-stream`；sandbox/job/oauth/network/service 事件按契约 `type` 推送；慢客户端不阻塞。 | `TestAC_API_009_SSEEvents` | ⏳ |
| AC-API-010 | P1 | `POST /jobs/{job_id}/cancel` 仅取消可取消的安装 job；状态 `cancelling → cancelled`；重复取消返回当前状态；已成功/失败/不可取消返回 409；取消后无半安装版本。 | `TestAC_API_010_JobCancel` | ⏳ |
| AC-API-011 | P5 | `POST /oauth/flows/{flow_id}/cancel` 使 pending flow 进入 `cancelled` 并失效 state/PKCE；迟到 callback 不写凭证；已成功返回 409。 | `TestAC_API_011_OAuthCancel` | ⏳ |
| AC-PROD-001 | Release | Release 时黑盒验证 health/capabilities（`native`、`services_only/filtered/open`）、auth 错误 envelope、loopback CORS；响应不含 daemon token 或凭证前缀。 | `TestProduct_*` | ✅（P0 子集） |

## 3. 零凭证与 token

| ID | 级别 | 断言 | 验证 | 状态 |
|---|---|---|---|---|
| AC-SEC-001 | P2 | 沙箱内 `env` / `ps e` / 进程环境只有 `NULLKEY_TOKEN`、`NULLKEY_GATEWAY`、`NULLKEY_PROXY`（及明确允许的遥测关闭变量）；不出现任何宿主凭证。 | `TestAC_SEC_001_SandboxEnv` | ⏳ |
| AC-SEC-002 | P2 | 沙箱不能读取宿主 `$HOME`、`~/.ssh`、`~/.aws`、`~/Library/Keychains`；读取失败而不是返回空文件。 | `TestAC_SEC_002_HostFsIsolation` | ⏳ |
| AC-SEC-003 | P2 | sandbox token 为 32 字节随机数的 Base64URL；DB 只存 SHA-256；API/日志不返回明文；不同 sandbox token 不同。 | `TestAC_SEC_003_TokenFormatAndHash` | ⏳ |
| AC-SEC-004 | P2 | `POST /sandboxes/{id}/stop` 后立即用旧 token 调 gateway 返回 401 `token_invalid`；删除同样失效。 | `TestAC_SEC_004_StopRevokesToken` | ⏳ |
| AC-SEC-005 | P2 | daemon 重启后，重启前的 sandbox token 全部失效（fail closed）；遗留 sandbox 进程被停止，不产生孤儿进程。 | `TestAC_SEC_005_RestartFailsClosed` | ⏳ |
| AC-SEC-006 | P1/P9 | 宿主日志、审计、终端输出、错误体都不含已注册凭证明文；请求 body/Authorization/query 不落日志。 | `TestAC_SEC_006_LogRedaction` | ⏳ |
| AC-SEC-007 | P3 | `GET /credentials` 只返回 fingerprint/元数据；任何响应/事件都不含 secret；`value` 只在 `POST` 请求体出现。 | `TestAC_SEC_007_CredentialsNoPlaintext` | ⏳ |
| AC-SEC-008 | P3 | 凭证写入/读取不走 argv；进程列表不可见 `sk-*`/OAuth token；使用 Security.framework 或等价 API。 | `TestAC_SEC_008_KeychainNoArgv` + 人工检查 | ⏳ |
| AC-SEC-009 | P4 | daemon token 不能访问 gateway/proxy；sandbox token 不能访问 `/api/v1`；两个平面 listener 独立。 | `TestAC_SEC_009_TokenPlaneSeparation` | ⏳ |
| AC-SEC-010 | P9 | `POST /sandboxes/{id}/scan` 只检查 `generated_runtime`，报告 `excluded=["workspace","user_imports","user_input"]`；受控样本 `hits=0`；检查失败返回错误，不伪装零命中。 | `TestAC_SEC_010_NoSecretScan` | ⏳ |

## 4. Runtime 与终端

| ID | 级别 | 断言 | 验证 | 状态 |
|---|---|---|---|---|
| AC-RT-001 | P2 | `POST /sandboxes` 创建并启动 native sandbox，状态从 `starting` 到 `running` 且有 pid；`stop` 后为 `stopped`；`DELETE` 返回 204 且始终保留 workspace/state。 | `TestAC_RT_001_SandboxLifecycle` | ⏳ |
| AC-RT-002 | P2 | 终端 ticket 一次性、30s 有效；WS 先发 `ready`（含 `terminal_id`）再接受输入；支持 input/output/resize/exit；错误 ticket 返回 401 `ticket_invalid`。 | `TestAC_RT_002_TerminalWebSocket` | ⏳ |
| AC-RT-003 | P2 | 进程在 Seatbelt profile 下运行；profile 应用失败时 sandbox 启动失败，绝不无沙箱裸跑。 | `TestAC_RT_003_SeatbeltFailClosed` | ⏳ |
| AC-RT-004 | P1/P2 | workspace rw 同一时间只允许一个 sandbox，第二个返回 409 `state_locked`；ro 可并发；停止后锁释放。 | `TestAC_RT_004_WorkspaceLock` | ⏳ |
| AC-RT-005 | P1/P2 | 停止再启动后 workspace/state 内容还在；新 sandbox token 与旧值不同；配置文件只引用环境变量。 | `TestAC_RT_005_PersistenceAndRotation` | ⏳ |
| AC-RT-006 | P2 | `GET /sandboxes/{id}/stats` 返回 memory/cpu/pid/uptime/startup_ms，数值非负且随进程存活变化。 | `TestAC_RT_006_Stats` | ⏳ |
| AC-RT-007 | P2 | daemon 收到 SIGTERM 后优雅关闭：停服务、停 sandbox 进程组、释放锁、标记 stopped；旧 token 失效，无孤儿进程。 | `TestAC_RT_007_GracefulShutdown` | ⏳ |
| AC-RT-008 | P2 | 关闭 WS 只 detach，不杀 PTY；无人接收时 daemon 持续 drain；新 ticket attach 同一 `terminal_id` 并替换旧写连接，旧连接收 `terminal_replaced`；attach 后触发 SIGWINCH；不承诺 scrollback。 | `TestAC_RT_008_TerminalReconnect` | ⏳ |
| AC-RT-009 | P2 | PTY 未可写时 ticket 返回 409 `terminal_not_ready`；sandbox 停止后返回 409 `conflict`；GUI 不能以 WS open 代替 `ready`。 | `TestAC_RT_009_TerminalState` | ⏳ |

## 5. Gateway 与网络

| ID | 级别 | 断言 | 验证 | 状态 |
|---|---|---|---|---|
| AC-NET-001 | P4 | 沙箱用 sandbox token 调 gateway 模型端点；gateway 注入真实凭证并流式返回；响应/日志不含真实凭证。 | `TestAC_NET_001_ModelGateway` | ⏳ |
| AC-NET-002 | P4 | 无效/过期/跨 sandbox token 调 gateway 返回 401 `token_invalid`；不向上游发请求。 | `TestAC_NET_002_InvalidToken` | ⏳ |
| AC-NET-003 | P7 | `filtered` 允许 allowlist 域名、拒绝其他域名（403 `network_denied`）；`open` 放行；`services_only` 关闭通用 egress。 | `TestAC_NET_003_EgressAllowlist` | ⏳ |
| AC-NET-004 | P7 | 沙箱直连公网 IP/DNS 失败；不识别 proxy 的程序 fail closed；loopback gateway/proxy 可达。 | `TestAC_NET_004_DirectOutboundBlocked` | ⏳ |
| AC-NET-005 | P4 | gateway 非上游额外延迟 p95 < 30ms；流式首字节额外延迟 p95 < 50ms；以受控上游 stub 对比直连。 | `TestAC_NET_005_GatewayLatency` + `scripts/slo.sh` | 📊 |
| AC-NET-006 | P7 | 私有 repo 通过 gateway clone/fetch/push 成功；沙箱 `.git/config` 与 remote 不含真实 token。 | `TestAC_NET_006_GitProxy` | ⏳ |
| AC-NET-007 | P4/P7 | gateway/proxy 不可用或上游错误时返回明确 5xx/错误码；不降级直连、不静默重试到其他目的地。 | `TestAC_NET_007_FailClosed` | ⏳ |
| AC-NET-008 | P7 | 三档模式都保留 gateway；`services_only` 明确不是离线；能力列表只报告 runtime 能实际执行的模式。 | `TestAC_NET_008_ModeSemantics` | ⏳ |

## 6. 凭证与 OAuth

| ID | 级别 | 断言 | 验证 | 状态 |
|---|---|---|---|---|
| AC-CRED-001 | P3 | `POST /credentials` 只返回 id/fingerprint；`GET` 列表无明文；`DELETE` 后 Keychain 项删除且记录撤销。 | `TestAC_CRED_001_ApiKeyLifecycle` | ⏳ |
| AC-CRED-002 | P3 | 撤销 credential 后，使用该 provider 的下一次 gateway 调用失败（401/`reauth_required`）。 | `TestAC_CRED_002_RevokeAffectsGateway` | ⏳ |
| AC-CRED-003 | P5 | OAuth start 返回 `auth_url`/`flow_id`；PKCE + state 校验；callback 成功写 Keychain；refresh 更新过期时间；过期返回 `reauth_required`。 | `TestAC_CRED_003_OAuthPKCE` | ⏳ |
| AC-CRED-004 | P5 | 宿主已有 Codex/Claude 登录可被检测（`/oauth/importable`）并导入为 credential；导入后 fingerprint 可读、无明文。 | `TestAC_CRED_004_HostImport` | ⏳ |
| AC-CRED-005 | P3 | `GET /providers` 至少含 Codex/OpenAI/Anthropic/GitHub；字段 `auth_types`/`base_url` 与 registry 一致。 | `TestAC_CRED_005_ProviderRegistry` | ⏳ |

## 7. MCP

| ID | 级别 | 断言 | 验证 | 状态 |
|---|---|---|---|---|
| AC-MCP-001 | P6 | 创建 GitHub MCP connection 后 `test` 返回工具列表；沙箱配置只含 gateway URL + token。 | `TestAC_MCP_001_GitHubConnection` | ⏳ |
| AC-MCP-002 | P6 | 通过 gateway 调一个 GitHub MCP tool 成功；宿主注入真实 token；响应/日志/沙箱配置无 token。 | `TestAC_MCP_002_ToolCall` | ⏳ |
| AC-MCP-003 | P6 | tool allowlist 拒绝未列出的 tool；删除 connection 后调用 404/401；跨 sandbox 不可复用。 | `TestAC_MCP_003_AllowlistAndRevoke` | ⏳ |

## 8. 工作区、状态与产物

| ID | 级别 | 断言 | 验证 | 状态 |
|---|---|---|---|---|
| AC-STATE-001 | P1 | 省略 `root_path` 创建的 workspace 为 `managed`；自定义目录为 `external`；路径 canonicalize 后仍允许；`$HOME`/SSH/Keychain/Docker socket 被拒绝。 | `TestAC_STATE_001_WorkspaceValidation` | ⏳ |
| AC-STATE-002 | P1 | `DELETE /workspaces/{id}` 默认只删记录并保留文件；`delete_files=true` 仅 managed 且 `confirm_name` 匹配时删目录；external 拒绝；活动 sandbox 引用返回 409 `state_locked`。不再提供 workspace reset。 | `TestAC_STATE_002_WorkspaceDelete` | ⏳ |
| AC-STATE-003 | P1 | `DELETE /states/{id}?confirm=true` 删目录+记录；`reset` 清空内容但保留记录；两者都不影响 workspace；活动引用 409。 | `TestAC_STATE_003_StateResetDelete` | ⏳ |
| AC-STATE-004 | P1 | `DELETE /sandboxes/{id}` 始终保留 workspace/state；传入 `delete_state`/`delete_workspace`（即使 false）返回 400 `invalid_request`。 | `TestAC_STATE_004_SandboxDelete` | ⏳ |
| AC-ART-001 | P8 | 文件列表/预览只读；路径 canonicalize 到 workspace；symlink 逃逸和 `..` 被拒；>1MB 截断并标记。 | `TestAC_ART_001_FileSafety` | ⏳ |
| AC-ART-002 | P8 | `GET /workspaces/{id}/diff` 返回 git patch；非 git 目录返回空/文件级变更而非 500。 | `TestAC_ART_002_Diff` | ⏳ |

## 9. 沙箱服务与隧道

| ID | 级别 | 断言 | 验证 | 状态 |
|---|---|---|---|---|
| AC-SVC-001 | P8 | Service Supervisor 按依赖拓扑启动声明服务，健康检查通过后注册；sandbox 停止时先停服务再停 harness；失败不阻塞终端。 | `TestAC_SVC_001_Lifecycle` | ⏳ |
| AC-SVC-002 | P8 | 服务只绑 `127.0.0.1` 的分配端口；只允许 manifest 声明端口；生成式认证 token/密码停止即失效。 | `TestAC_SVC_002_LoopbackAuth` | ⏳ |
| AC-SVC-003 | P8 | 服务崩溃按 manifest 重启或标记 error；日志经 redaction；不把服务日志送进模型上下文。 | `TestAC_SVC_003_RestartLogs` | ⏳ |
| AC-TUN-001 | P8 | `GET /services` 只列声明服务；`ticket` 绑定 sandbox+service、60s 一次性；过期/复用返回 401 `ticket_invalid`。 | `TestAC_TUN_001_ServiceTicket` | ⏳ |
| AC-TUN-002 | P8 | 隧道支持 HTTP/WS/SSE；只绑 `127.0.0.1`；非 loopback `Origin` 被拒；cookie/路径前缀正确重写。 | `TestAC_TUN_002_HttpWsSse` | ⏳ |
| AC-TUN-003 | P8 | 未声明 service/端口不可达，返回 404 `service_not_found`；无任意端口转发。 | `TestAC_TUN_003_UndeclaredService` | ⏳ |

## 10. 审计与 SLO

| ID | 级别 | 断言 | 验证 | 状态 |
|---|---|---|---|---|
| AC-AUD-001 | P1/P9 | sandbox start/stop、credential add/delete、oauth、network block、service open/close 产生审计事件；事件含 ts/type/sandbox_id/detail，且 detail 已 redact。 | `TestAC_AUD_001_AuditEvents` | ⏳ |
| AC-SLO-001 | P9 | 冷启动从 `POST /sandboxes` 到收到可写 PTY 的 WS `ready` 并启用输入：p50 < 800ms、p95 < 1.5s；daemon 空闲 RSS < 80MB；单 sandbox 额外 < 40MB。 | `TestAC_SLO_001_ColdStartAndMemory` + `scripts/slo.sh` | 📊 |
| AC-SLO-002 | P9 | 终端输入 RTT p95 < 50ms；gateway 额外延迟 p95 < 30ms；流式首字节额外 < 50ms。 | `TestAC_SLO_002_TerminalAndGatewayRTT` | 📊 |
| AC-SLO-003 | P9 | 隧道 RTT p95 < 20ms、吞吐 > 50MB/s；10k 文件列表 p95 < 300ms；文件预览首字节 p95 < 100ms。 | `TestAC_SLO_003_TunnelAndArtifacts` | 📊 |
| AC-SLO-004 | P9 | 3 个并发 sandbox 冷启动 p95 < 3s；3 个 Codex sandbox 总内存 < 1.5GB。 | `TestAC_SLO_004_Concurrency` | 📊 |
| AC-SLO-005 | P9 | 另报同一启动起点到 harness TUI 可交互的 p50/p95；adapter 无法识别时标“未测”，不能用 S1 代替。 | `scripts/slo.sh` 报告 | 📊 |
| AC-SLO-006 | Release | daemon 空闲 RSS < 80MB；control-plane health p95 < 50ms（release smoke，不替代 S9）。 | `TestSLO_DaemonIdleRSS`、`TestSLO_HealthLatency` | 📊 |

## 11. 交互契约（kernel 可观察部分）

[10-interaction-contract.md](10-interaction-contract.md) 的 UI/键盘/辅助功能由 GUI 团队验收；kernel 负责以下可观察行为：

| 交互要求 | 对应断言 |
|---|---|
| POST → WS `ready` 后才启用输入；`terminal_not_ready` 可重试 | AC-RT-002/009、AC-SLO-001 |
| 关闭/刷新/重开终端接回同一 PTY、同 `terminal_id`；单写连接；不重放输入 | AC-RT-008 |
| daemon 重启：停止遗留进程、释放锁、明确任务中断；不伪装恢复 | AC-RT-007、AC-SEC-005 |
| 安装取消：`cancelling → cancelled`；无半安装；重复/竞态返回真实状态 | AC-API-010 |
| OAuth 取消：迟到回调不写凭证；已完成返回 409 | AC-API-011、AC-CRED-003 |
| 删除语义：sandbox 保留数据；workspace managed/external；state confirm | AC-STATE-002/003/004、AC-RT-001 |
| 网络模式：`services_only` 不是离线；gateway 始终保留；不静默降级 | AC-NET-003/004/008 |
| 凭证检查范围/排除项/失败不伪装零命中 | AC-SEC-010 |
| 停止/删除/重启后可继续使用 workspace/state；token 轮换 | AC-RT-005、AC-SEC-004 |

## 12. 里程碑 gate

| Gate | 必须通过 | 允许 pending |
|---|---|---|
| **G0 骨架** | AC-API-001..007（含 OpenAPI 53 条契约） | 其余全部 |
| **G1 零凭证沙箱** | + AC-SEC-001..005、AC-RT-001..004/007/008/009、AC-STATE-001..004 | gateway/MCP/OAuth |
| **G2 模型 + MCP + 取消** | + AC-NET-001/002/007/008、AC-CRED-001/002/005、AC-MCP-001..003、AC-API-010/011 | egress 细粒度、tunnel |
| **G3 演示** | + AC-NET-003/004、AC-SEC-006/010、AC-AUD-001、AC-API-009、AC-SLO-001/002；交付时加 AC-SVC-001..003、AC-TUN-001..003、AC-ART-001/002 | desktop/v1.1 可砍 |
| **Release** | AC-PROD-001、AC-SLO-001..006（`make release-check` / release workflow） | 不适用；未达标不发布 |

## 13. v1.1（不阻塞 v1）

| ID | 范围 | 断言 | 状态 |
|---|---|---|---|
| AC-HT-001 | Host Tool Gateway | shim 只发 argv 数组；allowlist/subcommand/cwd=workspace；sanitized env；输出 redact；审批绑定 argv hash；沙箱无宿主凭证。 | 🔜 |
| AC-TF-001 | Terraform | `init/validate/plan` 可执行；`apply` 绑定 plan hash 并审批；`destroy` 默认拒绝；plan/state 不进入模型上下文。 | 🔜 |

> v1.1 落地前先补 ADR 与 `api/openapi.yaml`；v1 不实现、不提前加 GUI 端点。

## 14. 断言变更规则

1. 新功能先加 AC ID，再写测试，最后实现。
2. 删除/弱化断言必须在 PR 里写原因，并在本文标记 `superseded by AC-XXX`。
3. 实现改变可观察行为时，先更新 [08 API 契约](08-api-contract.md)、
   `api/openapi.yaml` 与 [10 交互契约](10-interaction-contract.md)，再改断言；
   不接受「测试跟着 bug 改」。
4. 安全类断言（AC-SEC-*）不允许在里程碑 gate 中豁免。
