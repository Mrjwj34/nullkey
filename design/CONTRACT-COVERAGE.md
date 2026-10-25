# GUI 交互需求对齐清单

基线：本地 main 56c0ebe 的 docs/07、08、10；当前均为 GUI mock，非真实后端联调结果。

| 契约需求 | 原型入口与行为 |
|---|---|
| 工作区生命周期 | 工作区页“管理工作区”；活动资源显示占用运行；提交再次检查锁 |
| external 移除 | 仅移除列表记录，说明文件保留；没有磁盘删除按钮 |
| managed 删除 | 显示完整路径、.git/未提交/未跟踪损失；名称完全匹配才启用；返回首页 |
| 保留独立资源 | workspace 移除不级联删 state/sandbox；首页显示已移除项目的旧运行摘要 |
| state reset / delete | 全局管理“会话状态”；运行详情直达对应记录；显示 Agent、关联项目与引用运行；活动引用保护；损失确认 |
| sandbox delete | 运行详情“删除此沙箱记录”；活动时先 stopping；保留 workspace/state |
| 凭证 test / delete | 凭证页测试、撤销；提示全局影响，不自动停止沙箱 |
| OAuth / API key / PAT | 模拟授权等待、成功、失败、过期、取消；pending 禁切换方式；API key/PAT 用只读假值 |
| 宿主授权导入 | 凭证页“体验宿主登录导入”；明确不读取实际 token |
| MCP 独立连接 | MCP 页账号与工具连接区分；建立/测试/移除；移除工具连接保留凭证 |
| 启动与继续 | 选择 Agent/state/network；创建新 sandbox；不承诺旧进程/对话恢复 |
| 安装取消 | running → cancelling 清理 → cancelled；返回启动草稿 |
| 终端 | 连接原 PTY，ready 前不可输入；不重放输出；另一视图替换后手动连接；停止中关闭输入 |
| 文件与服务 | 按 workspace / sandbox 分开归属；只读样例；ticket 过期与重新申请 |
| 凭证扫描 | 仅 generated_runtime，展示范围/排除项/时间，不宣称项目无秘密 |
| 管理审计 | 清理、撤销和工具连接变更写入原型活动记录 |

未以真实接口实现：目录选择、Keychain/OAuth、PTY、服务打开、文件/diff、扫描、真实测试失败与请求错误、分页/SSE/资源统计。当前不伪造这些操作已真实完成。

前端联调仍需覆盖：异步删除失败/409 竞态、daemon 对账期间未知状态、取消与成功同时发生、终端输入法与 resize、200% 缩放/VoiceOver、请求 request_id、后端分页。设置保存/allowlist 编辑等未定义契约不提供假提交。工作区菜单替换窗口保留原生 dialog 焦点圈定；所有删除仅作用于内存样例。
