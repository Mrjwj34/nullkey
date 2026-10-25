# Nullkey GUI 交互原型

当前版本：V2，依据 main `56c0ebe` 的技术文档、第二轮规划与
[`docs/14-ui-prototype-spec.md`](../docs/14-ui-prototype-spec.md) 优化。视觉令牌以
[`design-system/nullkey/MASTER.md`](../design-system/nullkey/MASTER.md) 为准。
`index.html` 自动进入 `v2.html`。可直接用浏览器打开，也可在仓库目录运行
`python3 -m http.server 8765 --bind 127.0.0.1`，访问 `/design/v2.html`。

品牌标记使用用户提供的 `design/assets/nullkey-cat.svg`，保留原始黑色圆角底板、白色猫脸、
黑色眼睛与鼻子。界面采用黑白主品牌层，灰白强调用于导航与焦点，青绿/琥珀/红色只保留给
运行、风险和错误状态。SVG 作为矢量资源直接使用，因此 logo 本身不需要重新绘制。

这些文件是用户负责的独立 GUI 设计交付，不修改 kernel 或 OpenAPI。所有资源与事件均为内存假数据；刷新重置评审样例。不读取项目磁盘、执行命令或接收真实密钥。

## 评审入口

1. 个人网站：运行选择器区分两次运行；停止 sbx_web_02 后，文档站继续运行。
2. 文档站：连接同一 term_docs_01 / PID 41203。通过异常选择器使 Codex 授权过期，沙箱与终端仍独立显示。
3. API 服务：失败运行没有成功的 TUI 输出，可重新配置启动。
4. 新建工作区：创建第四个独立项目；支持托管目录或假外部目录。启动可新建/复用兼容 state；活动 workspace/state 锁阻止重复写入。
5. 凭证：OAuth 成功、失败、过期、取消；API key/PAT 仅预置只读假值。首次体验从“设置 → 体验首次使用”开始，既有项目仍保留。
6. 首页安装：手动模拟成功/失败；取消先呈现清理中，再进入 cancelled。
7. 文件与产物是工作区级别；Services 与运行详情属于指定 sandbox。

## 本轮验证记录

### 石墨工作舱视觉修订

按用户确认的新方向，新增 `cockpit.js` / `cockpit.css` 作为原型展示层：石墨深色、
单层工作视图导航、终端主工作面和隔离边界检查器。边界面板展示凭证托管、工作区权限、
网络策略与 Seatbelt 的实际范围；小窗口将检查器重排到工作面下方。
终端内容为不可输入的示例，不宣称真实 Codex 会话已运行。


已完成 JavaScript 语法检查、diff 空白检查，以及浏览器中的精准停止、跨项目切换、同 PTY 重连、授权取消与重试、第四项目新增和 creating → running → ready 流程验证。

视觉优化后另完成：

- 1440 × 900 与 1024 × 700 实际浏览器检查；最小窗口无横向滚动，终端区域为 788 × 420。
- 工作区、终端、凭证管理和 OAuth 对话框的布局与焦点语义检查。
- Light 主题实际渲染检查；Dark 主题使用独立语义令牌和对比度计算检查。
- 控制台无 JavaScript error/warning；`v2.js`、`management.js`、`daily.js` 均通过 `node --check`。
- `prefers-reduced-motion`、`prefers-contrast` 与 `prefers-reduced-transparency` 样式已提供。

尚未进行真实 daemon 联调、xterm.js / PTY / SIGWINCH、真实 OAuth / Keychain、200% 缩放、VoiceOver、Dark 模式实际设备截图和跨浏览器验收。静态文件/diff、服务与 ticket 都是设计样例，不能作为后端可用性证明。新连接替换状态仅演示错误与恢复入口，没有真实多窗口写入竞争。

后续接入前端时，以 `docs/10-interaction-contract.md` 和 `api/openapi.yaml` 为准。完整方案与实现边界见 `GUI-PLAN-V2.md`。
