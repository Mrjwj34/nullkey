# 04 · 三天实施计划

> 假设：3 人 × 3 天 × 10 小时。只有 2 人时，砍掉 Should，保 Must。

## 1. 范围

### Must（不做完不上台）

- 本地 GUI（React + daemon API）+ Colima/Docker 沙箱 + 虚拟桌面。
- macOS Keychain 全局密钥：添加、测试、删除；provider registry 覆盖 8–10 个模型 provider + 通用 auth 类型。
- 模型网关：per-sandbox token → 真实 key；Claude Code 跑通。
- 宿主侧 MCP 网关 + GitHub MCP；沙箱配置零敏感信息。
- 沙箱启动/停止/删除；token 撤销；桌面访问。
- 审计事件 + "无密钥"检查。
- 一键启动脚本 + 3 分钟演示。

### Should

- Codex、DSH 适配。
- Codex/Claude 宿主登录导入（实验）。
- Filesystem MCP（沙箱侧）+ Slack/Notion 之一。
- Key 轮换、审计导出。
- Tauri 外壳。
- 空闲自动停止。

### Won't

- 完整 OAuth 授权流程；沙箱内登录；MCP OAuth（v1.1）。
- Windows / Linux host。
- 多用户、公网访问。
- microVM / 认证级强隔离。
- 原生 `.dmg`、签名、自动更新。
- 完整 MCP catalog。
- prompt injection / 恶意 Agent 防御。

## 2. 分工

| 角色 | 负责 | 产出 |
|---|---|---|
| A · 平台/沙箱 | Colima/Docker、镜像、桌面、端口、生命周期 | `SandboxProvider`、镜像、noVNC 代理 |
| B · 安全/网关 | Keychain、模型网关、MCP 网关、token、审计 | `nullkeyd` 核心、网关、redaction |
| C · GUI/适配/演示 | React GUI、harness 适配、MCP 连接、演示 | 页面、配置模板、演示脚本 |

第 1 小时冻结接口：`SandboxProvider`、`/api/sandboxes`、token 格式、审计字段。

## 3. 排期

### Day 1 · 打通"无密钥沙箱 + 模型调用"

| 时间 | A | B | C |
|---|---|---|---|
| 09:00–10:00 | 仓库、接口冻结 | 同左 | 同左 |
| 10:00–12:00 | Colima + Docker + Webtop/noVNC 桌面跑通 | daemon 骨架、Keychain helper | GUI 骨架（Home/Keys） |
| 12:00 | **Gate 1：浏览器能看到沙箱桌面，沙箱无外网** | | |
| 13:00–18:00 | SandboxProvider、token 注入、端口映射 | 模型网关（Anthropic/OpenAI）、key 存取 | Claude Code 安装/配置模板；GUI 启动流程 |
| 18:00–20:00 | 联调 | 流式转发、错误处理 | 端到端：Claude Code 发一条消息 |
| 20:00 | **M1：Claude Code 在沙箱里通过网关调用模型；沙箱内只有 token** | | |

**M1 验收**：GUI 启动沙箱 → 桌面 → Claude Code 回一句话 → 沙箱 `env` 只有 `NULLKEY_TOKEN` → 宿主日志无 key。

### Day 2 · MCP 与多 harness

| 时间 | A | B | C |
|---|---|---|---|
| 09:00–12:00 | 桌面体验（分辨率、剪贴板关闭） | MCP Gateway、GitHub MCP、tool allowlist | MCP 配置渲染；GitHub 连接 UI |
| 12:00 | **Gate 2：Claude Code 通过 MCP 调用 GitHub，配置里无 token** | | |
| 13:00–17:00 | 资源限制、停止/删除、卷清理 | token 撤销、审计、日志 redaction | Codex 适配；DSH 适配（至少一个） |
| 17:00–20:00 | 联调 | "无密钥"检查 | 演示脚本 v1 |
| 20:00 | **M2：2 个 harness + 1 个 MCP + 停止即失效** | | |

**M2 验收**：Claude Code + Codex（或 DSH）都能启动；GitHub MCP 可用；停止后重放 → 401；撤销 key → 失败。

### Day 3 · 加固、验证、彩排

| 时间 | A | B | C |
|---|---|---|---|
| 09:00–12:00 | 加固（只读、cap-drop、无挂载、core dump） | 审计导出、错误码、扫描脚本 | 红队检查、演示素材 |
| 12:00 | **Gate 3：所有 Must 检查通过，演示计时** | | |
| 13:00–15:00 | 彩排 ×2、录屏 | 文档/README | 评审要点 |
| 15:00–17:00 | Buffer | Buffer | Buffer |
| 17:00–18:00 | 最终检查：一键、离线、备份视频 | | |
| 18:00 | **M3** | | |

## 4. 里程碑与 Gate

| Gate | 时间 | 通过标准 | 不通过 |
|---|---|---|---|
| G1 沙箱 | D1 12:00 | 桌面可见；无外网；无宿主挂载 | 换 Docker Desktop/Webtop 镜像；Colima 2 小时不通即切换 |
| G2 MCP | D2 12:00 | Claude Code 通过网关调用 GitHub；配置只有 token | 先做 Filesystem MCP（无凭证），GitHub 放 Should |
| G3 演示 | D3 12:00 | Must 全过；3 分钟不超时 | 砍 Codex/DSH，保 Claude Code + GitHub + 扫描 |

**底座以可交付为先**：Colima 网络 2 小时不通即切换 Docker Desktop（如果已安装）；`SandboxProvider` 保证切换成本低。

## 5. 演示脚本（3 分钟）

| 时间 | 画面 | 动作/台词 |
|---|---|---|
| 0:00 | 标题 | "Agent 很方便，但 session 一泄漏，key 就没了。" |
| 0:15 | 传统方案 | 终端里 `.env` / `env` 直接看到 key；transcript 里也有。 |
| 0:35 | Nullkey GUI | Keys 页面：Anthropic、GitHub 已配置，只显示 fingerprint。 |
| 0:50 | 启动 | 选 Claude Code → 启动 → 桌面出现。 |
| 1:10 | 正常任务 | 让 Claude Code "搜一下 GitHub 上 X 项目并总结"；模型 + MCP 都工作。 |
| 1:35 | 沙箱内检查 | 终端 `env`、`cat ~/.claude.json`、`grep -R "sk-" ~`：只有 `NULLKEY_TOKEN`。 |
| 1:55 | 泄漏演练 | 把 `~/.claude` 打包下载，打开：transcript 只有 token。 |
| 2:10 | 撤销 | GUI 停止沙箱；桌面里的请求失败；重启后新 token。 |
| 2:25 | 扫描 | 点"扫描密钥"→ 0 命中。 |
| 2:35 | 架构 | 一张图：key 在 Keychain，网关注入，沙箱只有 token。 |
| 2:50 | 边界 | "我们不防 prompt injection；我们保证 key 不在沙箱。" |
| 3:00 | 结束 | "Session 可以泄漏，key 不会。" |

演示前：

- [ ] 用假 key 彩排；真实 key 只在最后一次用，演示后撤销。
- [ ] 准备离线录屏（Colima/GitHub/模型都可能挂）。
- [ ] 清理终端历史、GUI 日志，避免真实 key 入镜。
- [ ] 提前构建镜像，避免现场拉取。

## 6. 风险

| 风险 | 概率 | 影响 | 预案 |
|---|---|---|---|
| Colima 内网/端口不通 | 中 | 高 | 换 Docker Desktop；或 bridge 容器 + host.docker.internal |
| arm64 桌面镜像难搞 | 中 | 高 | 自建 Debian + Xfce + noVNC；或先用 headless + 截图 |
| Claude Code 配置字段变化 | 中 | 中 | 固定版本；适配器 smoke test；降级到 `ANTHROPIC_BASE_URL` |
| MCP HTTP transport 不兼容 | 中 | 中 | 用 stdio bridge；先做 Filesystem |
| OAuth 范围膨胀 | 高 | 高 | v1 只做广谱 key + 可选导入；完整流程放 v1.1 |
| 模型/网络延迟 | 中 | 中 | 预热、缓存、录屏 |
| 时间不够 | 高 | 高 | 砍 Codex/DSH/Slack；保 Claude Code + GitHub + 扫描 |
| 评审问"能防注入吗" | 高 | 中 | 明确边界：不防；产品解决 session 泄漏 |

## 7. 验收清单

- [ ] GUI 能添加/测试/删除 key，永不显示明文。
- [ ] 能启动/停止/删除沙箱；桌面可访问。
- [ ] 沙箱无外网（`curl` 失败）。
- [ ] 沙箱无宿主挂载（`mount`/`ls` 检查）。
- [ ] Claude Code 通过网关调用模型。
- [ ] Claude Code 通过网关调用 GitHub MCP。
- [ ] 沙箱配置只有 `NULLKEY_TOKEN`。
- [ ] 停止沙箱后 token 失效。
- [ ] 撤销 key 后调用失败。
- [ ] 审计有事件，无 key 明文。
- [ ] "扫描密钥" 0 命中。
- [ ] 一键启动 + 离线录屏。

## 8. 交付物

- 代码仓库（一键 `make dev` / `docker compose up`）。
- README + docs/。
- 3 分钟演示视频 + 离线备份。
- 红队/检查报告（截图）。
- 评审 PPT（问题、边界、架构、演示、路线图）。
