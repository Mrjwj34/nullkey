# Nullkey GUI · 第一轮设计提案

状态：第一轮历史方案，已由 [第二轮重规划](GUI-PLAN-V2.md) 取代。原型存在工作区、沙箱与会话状态混用的问题，不应作为前端实现依据。下文保留用于追踪第一轮交付。

## 交付与体验方式

打开 `design/index.html`，无需安装依赖。默认进入已有会话；右下角可以切换首次使用、授权过期、网络阻止、工作区锁定、启动失败和终端断线。可导航页面、完成模拟引导、停止/继续工作、撤销并恢复连接、输入模拟终端消息。

这是纯本地交互原型：没有真实 OAuth、密钥输入、模型调用、PTY、文件访问或安全隔离。真实前端应以 xterm.js 接入 PTY，保留 harness 原有交互，不自行拼接聊天 UI。

## 设计方向

桌面工作台，暗色中性底色搭配低饱和绿色。内容以终端为中心，连接状态保持低干扰；授权、隔离和连接失败时在上下文中呈现原因与恢复动作。字体使用 macOS 系统字体，终端使用系统等宽字体。

基础 token：背景 #101312、面板 #191d1b、边界 #2a302d、正文 #e7ede8、次要文字 #8d9992、强调 #b8ed92、警示 #edc481。按钮最小高度约 36px；可见焦点；状态同时使用文字，不仅依赖颜色。原型默认桌面布局，窄窗口折叠辅助面板。

## 信息架构与优先级

| 层级 | 页面 | 第一轮范围 |
|---|---|---|
| 工作区 | 概览 | 最近工作区、新建、继续 |
| 工作区 | Terminal | 默认入口、停止/继续、异常恢复、工作环境摘要 |
| 工作区 | Artifacts | 只读文件审查；本原型提供静态预览 |
| 工作区 | Services | 已声明服务；本原型覆盖空状态 |
| 管理 | Workspaces | 项目列表、锁定状态、新建 |
| 管理 | Credentials | 状态、测试、重新授权、撤销 |
| 管理 | MCP | 连接与能力摘要；本原型为只读示意 |
| 管理 | Audit | 关键事件；本原型保留本次交互记录 |
| 管理 | Settings | 默认策略；本原型为只读示意 |

首次无连接且无工作区进入引导；已有运行会话进入 Terminal；仅有历史工作区进入概览。Desktop 根据 capabilities 隐藏或显示不可用说明，不占一级导航。

## 三条核心流程

1. 首次：连接 Codex → 宿主浏览器授权 → 等待 OAuth flow 成功 → 安装并校验固定版本 → 选择/创建工作区 → 默认 filtered → 启动 → 进入 Terminal。
2. 继续：最近工作区 → 检查 workspace/state 锁 → 创建新 sandbox 并轮换 token → 恢复 PTY → harness 支持时恢复会话。不把“保留文件”表述成保证恢复进程。
3. 授权失效：Terminal 保留 → 行内提示 → 在宿主重新授权 → 回到同一终端。是否重试原请求由 harness 能力决定；不静默重放可能有副作用的调用。

原型中的“连接”和“安装”是模拟成功；正式版必须等待后端结果。原型工作区名称代替真实目录选择器；正式版接入宿主目录选择，用户取消时保留已填设置。

## 状态与恢复

| 状态/事件 | 界面行为 | 主动作 |
|---|---|---|
| creating / starting | 保留当前配置，禁用重复提交，展示阶段 | 失败后重试 |
| running | 终端可输入，显示工作区、harness、网络 | 停止 |
| stopping | 禁用重复停止；不要提前宣称 token 已撤销 | 等待确认 |
| stopped | 保留终端画面与项目入口 | 继续工作 |
| runtime_unavailable / failed | 显示隔离失败；不渲染为运行成功 | 重试 / 查看诊断 |
| reauth_required | 保留终端；提示受影响连接 | 重新连接 |
| network_denied | 提示目标域名与原因，不自动放宽网络 | 查看活动 |
| state_locked | 阻止写入启动，显示占用 sandbox | 选择其他工作区 |
| WebSocket 断开 | 保留输出；区分连接状态与进程状态 | 重新连接 |
| ticket_invalid | 重新申请短期 ticket，不要求用户复制 token | 重连/重新打开 |
| daemon 不可达 | 显示全局离线状态，禁用依赖 API 的操作 | 重试连接 |
| 安装失败 | 显示错误与可重试阶段，保留工作区配置 | 重试安装 |
| OAuth pending / expired / failed | 宿主等待页；可重新发起；不伪造成功 | 重新授权 |

后四项作为下一轮补充画面，本原型未提供独立场景。只读共享 workspace、共享 state、快照/回滚仍按 Should 处理，不作为锁冲突的默认替代动作。

## API 对接清单

| 交互 | API / 协议 | 设计约束 |
|---|---|---|
| 启动检查 | GET /health, /capabilities, /harnesses, /providers | 按能力显示入口 |
| OAuth | POST /oauth/start; GET /oauth/flows/{flow_id} | 仅后端成功后更新状态 |
| 安装 | POST /harnesses/{id}/install; GET /jobs/{job_id}; SSE job.progress | 进度未知时使用不定进度条 |
| 创建项目状态 | POST /workspaces, /states | 保留 workspace/state ID |
| 启动 | POST /sandboxes | 提交使用 Idempotency-Key；订阅状态变化 |
| 终端 | POST /sandboxes/{id}/terminal; WebSocket | ticket 一次性/30 秒；输入、resize、输出、exit |
| 停止 | POST /sandboxes/{id}/stop | 等待服务端确认；保留文件与会话 |
| 凭证 | GET /credentials; POST /credentials/{id}/test; DELETE /credentials/{id} | 撤销说明全局影响 |
| 工具 | GET /mcp/catalog, /mcp/connections | 能力来自实际 tools 字段 |
| 审查 | GET /workspaces/{id}/files, /file, /diff | 处理截断、二进制、路径拒绝 |
| 预览服务 | GET /sandboxes/{id}/services; POST …/{service_id}/ticket | 只打开声明服务，ticket 不持久化 |
| 活动 | GET /audit; GET /events | 正文和凭证不进入事件详情 |

## 组件交付约定

AppShell、SidebarNav、WorkspaceHeader、StatusBadge、InlineNotice、ConnectionCard、WorkspaceCard、SetupStepper、JobProgress、TerminalPane、SessionInspector、EmptyState、ConfirmationDialog。

每个可交互组件覆盖默认、hover、focus、disabled、loading；数据容器覆盖 loading、empty、error。Toast 仅用于短暂反馈，阻塞问题必须持续显示并给出动作。生产版弹窗需要焦点圈定、Escape/取消、关闭后焦点恢复。原型撤销动作使用浏览器原生确认框。

Terminal 必须覆盖中文输入法、选择复制、大量输出、resize、键盘快捷键冲突、断线重连、输出回放与滚动位置。实时输出是否可重放尚需后端协议确认；断线时不可直接宣称任务退出。

## 安全文案

- 正常连接：“凭证保存在本机 Keychain，Agent 通过网关使用。”
- 停止成功：“沙箱已停止，访问令牌已失效。代码与会话已保留。”
- 撤销：“使用该凭证的所有沙箱将无法继续模型调用，项目文件保留。”
- Open 模式：“允许通过代理访问任意目标，会扩大项目数据外发风险。”
- 解释边界：“凭证隔离不阻止 Agent 通过已允许的服务发送项目数据。”

不使用“绝对安全”“完全防泄漏”。模型原始错误不直接作为用户文案；提供可复制的脱敏诊断和 request_id。

## 评审问题与接口缺口

1. 确认应用壳技术、默认语言、最小窗口尺寸；本轮采用中文、桌面优先、暗色。
2. 继续工作使用新 sandbox 还是恢复停止记录？当前 API 没有 start/resume endpoint，设计暂按 POST /sandboxes 复用 workspace/state。
3. 创建沙箱没有明确 provider/credential/MCP 选择字段；多连接默认绑定方式需后端确认，GUI 不虚构关联接口。
4. 全局设置保存、allowlist 编辑、安装取消、服务注册接口未定义；第一版不提供这些可提交表单。
5. workspace reset 的删除语义未冻结；首轮不提供重置/删除入口。
6. 原型 Services 空状态与 Artifacts 样例用于布局评审；多文件 diff、服务就绪/过期画面下一轮补齐。

## 评审验收

请团队成员独立完成：首次进入终端、停止并继续、识别授权失效并恢复、解释网络阻止、处理工作区锁定。记录卡住的位置、误解的文案、是否知道文件保留、是否把模拟终端误认为真实 Agent。验证通过后，再把页面拆成前端组件并接入契约 mock。
