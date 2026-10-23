# 04 · 三天实施计划

> 3 人 × 3 天 × 10 小时。只有 2 人时，砍掉 Should，保 Must。
>
> GUI 由独立团队按 [API 契约](08-api-contract.md) 并行实现；本计划只覆盖后端/runtime。

## 1. 范围

### Must

- Native runtime（Seatbelt，无 VM）+ PTY + Terminal WebSocket。
- Codex 跑通：API key + 宿主 OAuth。
- 全局凭证：Keychain、per-sandbox token、撤销。
- 模型 gateway；GitHub MCP；egress proxy + `filtered` 默认。
- workspace / state / cache 持久化；至少一个共享/复用场景。
- API 契约冻结；基础审计；SLO 基线。
- 3 分钟演示。

### Should

- Claude Code / DSH。
- MCP OAuth（DCR）。
- 私有 Git smart HTTP proxy。
- 可选 desktop runtime + Codex app。
- Service tunnel + Artifacts 审查。
- 第二个 MCP（Slack/Notion）。
- 状态只读共享、快照/回滚。

### Won't

- 默认 VM / Colima / Docker Desktop。
- Windows / Linux host。
- 多租户、公网访问。
- 完整桌面 app catalog。
- prompt injection / 恶意 Agent 防御。
- 按 harness/MCP 的凭证 scope。

## 2. 分工

| 角色 | 负责 | 产出 |
|---|---|---|
| A · Runtime | Seatbelt profile、PTY、进程生命周期、workspace/state | `NativeRuntime`、终端服务 |
| B · 安全/网关 | Keychain、模型 gateway、OAuth broker、MCP gateway、token | `nullkeyd` 核心、OAuth、审计 |
| C · 网络/适配/演示 | egress proxy、Codex 适配、GitHub MCP、SLO/演示 | 适配器、proxy、benchmark、演示脚本 |

第 1 小时对齐：API 草案、`Runtime` 接口、token 格式、错误码；完成 [交互验收](10-interaction-contract.md) 后冻结 GUI 契约。

## 3. 排期

### Day 1 · Native TUI + Codex

| 时间 | A | B | C |
|---|---|---|---|
| 09:00–10:00 | API 草案对齐、仓库骨架 | 同左 | 同左 |
| 10:00–12:00 | Seatbelt profile、PTY、Terminal WebSocket | Keychain helper、daemon 骨架 | Codex 安装/配置模板、egress proxy 骨架 |
| 12:00 | **Gate 1：Terminal 能进 shell；Seatbelt 阻止读宿主 home/Keychain** | | |
| 13:00–18:00 | Sandbox 生命周期、workspace/state、token env | 模型 gateway + OAuth broker 骨架 | Codex 跑通 API key；网络 `filtered` |
| 18:00–20:00 | 联调 | 流式转发、错误处理 | Codex 发一条消息；扫描无 key |
| 20:00 | **M1：Codex TUI 通过 gateway 调模型；沙箱只有 token** | | |

**M1 验收**：GUI/curl 启动 sandbox → Terminal 进入 Codex TUI → 回复一句话 → `env`/配置只有 token → 扫描 0。

### Day 2 · OAuth + MCP + 状态

| 时间 | A | B | C |
|---|---|---|---|
| 09:00–12:00 | workspace 共享/锁、state 持久化 | OAuth Broker（PKCE、callback、Keychain、refresh） | GitHub MCP + MCP gateway |
| 12:00 | **Gate 2：Codex 用宿主 OAuth 调模型；GitHub MCP 可用；配置无 token** | | |
| 13:00–17:00 | 资源/并发/统计指标 | token 撤销、审计、日志脱敏 | Git proxy（Should）或第二个 MCP；OAuth 错误处理 |
| 17:00–20:00 | SLO benchmark v1 | 状态重置/清理 | 演示脚本 v1 |
| 20:00 | **M2：OAuth + MCP + 持久化 state 跑通** | | |

### Day 3 · 加固、SLO、演示

| 时间 | A | B | C |
|---|---|---|---|
| 09:00–12:00 | Seatbelt 收紧、core dump、路径校验 | 审计导出、失败模式、OAuth 过期 | Claude/DSH WebUI 或 MCP OAuth（二选一）；Service tunnel/Artifacts |
| 12:00 | **Gate 3：Must 全过；SLO 达标；演示计时** | | |
| 13:00–15:00 | 彩排 ×2、录屏 | 文档/API 契约 | 评审要点 |
| 15:00–17:00 | Buffer | Buffer | Buffer |
| 17:00–18:00 | 最终检查：一键、离线录屏、清理假 key | | |
| 18:00 | **M3** | | |

## 4. Gate

| Gate | 时间 | 通过标准 | 不通过 |
|---|---|---|---|
| G1 Runtime | D1 12:00 | Terminal 可用；Seatbelt 阻断宿主 home/Keychain | 先用 `sandbox-exec` 最小 profile，不要等完美 |
| G2 OAuth/MCP | D2 12:00 | Codex OAuth + GitHub MCP 可用；配置无 secret | OAuth 降级为 API key + 导入；MCP 先 Filesystem |
| G3 演示 | D3 12:00 | Must 全过；SLO 达标；3 分钟不超时 | 砍 Claude/DSH、Git proxy、desktop |

**不要赌**：Seatbelt network 规则 2 小时验证不通，记录 runtime 阻塞并缩减可交付范围；无法执行声明策略的模式不对 GUI 报告可用，不静默退回更弱隔离。

## 5. 演示脚本（3 分钟）

| 时间 | 画面 | 动作/台词 |
|---|---|---|
| 0:00 | 标题 | "Agent 很方便，但 session 一泄漏，key 就没了。" |
| 0:15 | 传统方案 | `.env` / `env` 直接看到 key；transcript 里也有。 |
| 0:35 | Nullkey GUI | Credentials：Codex OAuth、GitHub 已连接；只显示 fingerprint。 |
| 0:50 | 启动 | 选 Codex → 启动 → 直接进入 Codex TUI（SSH-client 风格终端）。 |
| 1:10 | 正常任务 | 让 Codex 改一个文件、跑测试；模型 + GitHub MCP 工作。 |
| 1:30 | 网络不残废 | `npm install` / `curl docs` 可用；`curl 1.1.1.1` 失败。 |
| 1:45 | 沙箱检查 | `env`、`cat ~/.codex/config.toml`、`grep -R "sk-"`：只有 token。 |
| 2:00 | 持久化 | 停止 → 重启：workspace/state 还在；token 已轮换。 |
| 2:15 | 撤销 | 撤销 Codex 凭证 → 下一次调用失败。 |
| 2:30 | 检查 | “生成配置/环境凭证检查” → 0 命中，展示范围/排除项/时间；Artifacts 打开 diff。 |
| 2:40 | 架构 | key 在 Keychain，gateway 注入，沙箱只有 token。 |
| 2:50 | 边界 | “不防 prompt injection；Nullkey 管理的凭证不注入沙箱，项目自带秘密仍需保护。” |
| 3:00 | 结束 | “把凭证留在宿主，把工作留在沙箱。” |

演示前：

- [ ] 假 key/测试 OAuth 彩排；真实凭证最后才用，演示后撤销。
- [ ] 录屏兜底（模型/GitHub/OAuth 都可能挂）。
- [ ] 清理终端历史、日志、截图。
- [ ] 提前安装 Codex/构建 proxy。

## 6. 风险

| 风险 | 概率 | 影响 | 预案 |
|---|---|---|---|
| Seatbelt network 规则不够细 | 中 | 高 | 不宣称对应模式可用；启动失败关闭，重新评估 runtime 范围 |
| PTY/终端兼容性 | 中 | 中 | 用 xterm.js + `creack/pty`；先保 shell，再保 Codex |
| Codex 配置字段变化 | 中 | 中 | 固定版本 + smoke test |
| OAuth provider 限制/条款 | 中 | 高 | Codex 优先；失败降级 API key/导入 |
| MCP HTTP transport 不兼容 | 中 | 中 | stdio bridge；先 Filesystem |
| 内存/启动不达标 | 低 | 中 | native 本身轻；先测 Codex RSS/启动 |
| 时间不够 | 高 | 高 | 砍 Claude/DSH、Git proxy、desktop、service tunnel |
| 隧道暴露面 | 中 | 中 | 只绑 loopback、只声明服务、一次性 ticket、Origin 校验 |

## 7. 验收清单

- [ ] Terminal 能进入 Codex TUI；输入/输出/resize 正常。
- [ ] 沙箱内 `env`/配置只有 `NULLKEY_TOKEN`。
- [ ] 沙箱读不到宿主 `$HOME`/Keychain。
- [ ] Codex 通过 gateway 调模型（API key 或 OAuth）。
- [ ] GitHub MCP 通过 gateway 调用。
- [ ] `filtered` 下 npm/公开文档可用；直连 IP 失败。
- [ ] workspace/state 重启后还在；token 轮换。
- [ ] 停止/撤销后调用失败。
- [ ] 审计无 Nullkey 管理的凭证明文；受控测试项目的凭证注入检查为 0，展示范围/排除项/时间。
- [ ] SLO 基线达标（见 [slo.md](../slo.md)）。
- [ ] API 契约与 GUI 对齐；[交互验收](10-interaction-contract.md) 中关闭/重连、取消、清理和无障碍场景通过后冻结。
- [ ] Service tunnel 能打开声明过的 WebUI；未声明端口不可达。
- [ ] Artifacts 能看文件树/diff。

## 8. 交付物

- 代码仓库（`make dev`）。
- README + docs/ + `slo.md` + API 契约 + `api/openapi.yaml`。
- 3 分钟演示视频 + 离线备份。
- SLO benchmark 结果。
- 评审 PPT（问题、边界、架构、演示、路线图）。
