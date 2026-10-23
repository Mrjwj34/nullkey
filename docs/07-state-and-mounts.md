# 07 · 状态、文件挂载与持久化

> 目标：关掉沙箱再打开，进度还在；多个沙箱能复用 workspace/state/cache；同时不把宿主敏感目录带进来。

## 1. 三类状态

| 类型 | 内容 | 默认 | 共享 |
|---|---|---|---|
| **workspace** | 项目文件、代码、构建产物 | 持久化 | 可 rw 独占 / ro 共享 |
| **state** | harness 配置、会话、历史、登录缓存（不含真实凭证） | 持久化 | 默认单沙箱独占；可 ro 共享 |
| **cache** | harness 安装包、npm/pip/cargo 缓存 | 持久化 | 全局共享 |

Nullkey 管理的凭证只在 Keychain，由 gateway 注入，不写入以上目录。用户已有项目、导入文件或粘贴内容可能自带凭证，不受这一保证覆盖。

## 2. 目录布局

```text
~/Library/Application Support/Nullkey/
  workspaces/
    <workspace-id>/            # 用户项目；可指向外部目录
  states/
    <harness-id>/
      <state-id>/
        home/                  # 作为沙箱 HOME
          .codex/              # 每次启动重新渲染，不含真实 token
          .claude/
        sessions/              # 可选持久化
  cache/
    harnesses/<harness>/<version>/
    npm/
    pip/
```

- workspace 可以让用户选择已有目录（例如 `~/code/foo`），但只挂载该目录，不挂父目录/home。
- `root_path` 省略时为 `storage_kind: managed`；显式选择目录时为 `external`，即使路径位于 Nullkey 目录内也不改变归属。GUI 展示实际路径和读写权限；`rw` 会直接修改原始文件，不是副本。
- state 默认在 Nullkey 目录；也可以指定外部目录，但必须显式确认。沙箱 `HOME` 指向 `state/home`。
- cache 只读挂载给沙箱；写入由宿主侧的安装器/包管理器代理完成，或使用 copy-on-write。

## 3. 挂载模型

```json
{
  "mounts": [
    { "source": "workspace", "target": "/workspace", "mode": "rw" },
    { "source": "state",     "target": "/state",     "mode": "rw" },
    { "source": "cache",     "target": "/cache",     "mode": "ro" }
  ]
}
```

规则：

- 默认只挂这三类；自定义 `host` mount 必须显式添加并二次确认。
- 自定义 mount 默认 `ro`；`rw` 需要用户勾选。
- 禁止：`/`、`$HOME`、`~/.ssh`、`~/.aws`、`~/.config/gh`、`~/Library/Keychains`、Docker socket。
- 路径必须 canonicalize；解析 symlink 后仍在允许根内；拒绝 `..` 穿越。
- 不把宿主 home 映射成沙箱 home；沙箱 home 是 state 目录。
- 文件所有者/权限按当前用户；不提权。

## 4. 共享与锁

- **workspace rw**：同一时间只允许一个沙箱写；获取独占锁；第二个沙箱默认拒绝（409）。
- **workspace ro**：可多个沙箱同时挂载；适合 reviewer/并行分析。
- **state**：默认单沙箱独占；并发使用同一 state 会导致配置/token 冲突。需要并行时用 `ro` 或复制 state。
- **cache**：允许多个沙箱共享；包管理器自身有锁；Nullkey 只读挂载，避免互相污染。
- 锁信息：`workspace_locks(workspace_id, sandbox_id, mode, acquired_at)`；daemon 重启后先核对并停止遗留 sandbox 进程，再释放锁并标记 stopped，不能仅按丢失 PTY 就释放写锁。

## 5. Token 与配置渲染

- state 里的配置**不保存真实 token**：
  - Codex：`env_key = "NULLKEY_TOKEN"`，token 从进程环境注入。
  - Claude Code：`ANTHROPIC_AUTH_TOKEN` 从环境注入。
  - MCP：需要 header 的地方渲染到 per-sandbox overlay，不写共享 state。
- 每次启动生成新 token；停止即失效。
- 共享 state 时，旧 token 不会残留；配置文件只引用环境变量。
- 如果某个 harness 只支持把 token 写进配置文件，使用 per-sandbox overlay（复制一份配置），不污染共享 state。

## 6. 继续进度

- 启动时选择已有 workspace/state；配置重新渲染；token 轮换。
- 如果 harness 支持 resume（Codex/Claude Code 的 session），保留 `sessions/`；否则至少 workspace 代码还在。
- `GET /workspaces/{id}` 返回 `last_sandbox_id`；GUI 显示"继续"。
- 关闭终端视图只断开连接，运行中的任务继续；重新打开接回同一 PTY，见 [08](08-api-contract.md#37-terminal)。
- 停止或删除 sandbox 始终保留 workspace/state；清除会话或删除文件必须走独立操作，不提供级联删除开关。

## 7. 重置与清理

| 操作 | 影响 |
|---|---|
| Reset state / 清除会话 | 删除该 state 内的 harness 配置、会话和历史；保留 state 记录及 workspace；下次启动重建配置；需确认 |
| Reset workspace | v1 不提供；不猜测应清除未提交文件、构建产物还是整个仓库 |
| Clear cache | 删除 harness 安装包/包管理器缓存；下次重装 |
| Delete sandbox | 停进程、撤 token、移除 sandbox 记录；始终保留 workspace/state |
| Remove workspace / 从列表移除 | 仅移除 workspace 记录；文件保持原样，适用于 managed/external |
| Delete workspace files / 删除磁盘文件 | 仅允许 managed；删除整个目录（含 `.git`、未提交和未跟踪文件）及记录；展示完整路径、说明不可恢复，输入名称确认 |
| Delete state / 删除会话记录 | 删除 state 目录及记录；保留 workspace；展示路径和会话影响，需确认 |

- 提供 `nullkey gc` / GUI "清理"：按 LRU 删除未使用 cache，不碰 workspace。
- external workspace 不允许由 Nullkey 删除磁盘文件；用户在 Finder 中自行管理。
- workspace/state 被任一 `creating/starting/running/stopping` sandbox 引用时，移除、删除、重置均返回 `409 state_locked`（含只读使用者）；不隐式停止任务。检查引用与执行操作必须互斥，避免同时启动/清理。
- 清除会话和删除文件没有撤销能力；确认应说明具体损失。停止沙箱不清除数据，不用相同的危险操作提示。
- 审计记录 reset/delete 操作。

## 8. 安全边界

- 持久化状态可能包含用户代码、transcript、历史；Nullkey 不把其管理的真实凭证写入这些数据。
- 共享 workspace 可能包含用户自己的 secrets（`.env`）；默认凭证检查不扫描用户项目，不承诺导出目录无凭证。选择已有目录时展示这一边界。
- state 里的 harness 登录缓存：v1 不使用（OAuth token 在 Keychain）；如果未来使用，必须加密。
- 备份/Time Machine 可能备份 workspace/state；其中的项目秘密和私有内容仍需用户保护，不能因凭证检查为零就认定可公开分享。
- 自定义 mount 是最大的风险点；默认关闭，需确认，且禁止敏感路径。

## 9. 与 runtime 的关系

- **Native**：直接使用宿主目录，无需 bind mount；Seatbelt 只允许 workspace/state/cache 路径。
- **Desktop（可选）**：bind mount 同样的目录到 Linux 桌面；只读 cache，rw workspace/state。
- 两种 runtime 使用同一套 workspace/state/cache 语义，切换不丢进度。

## 10. API 字段

- `POST /workspaces` `{name, root_path?}`
- `POST /sandboxes` `{harness_id, workspace_id, state_id?, mounts?}`
- `GET /workspaces/{id}` 返回 `storage_kind`、`last_sandbox_id`、`size_bytes`、`locked_by`
- `DELETE /workspaces/{id}` 默认只移除记录；`delete_files=true&confirm_name=<name>` 仅适用于 managed
- `POST /states/{id}/reset`、`DELETE /states/{id}?confirm=true`；v1 无 workspace reset 接口
- 产物审查（文件树/预览/diff）与服务隧道见 [09-service-tunnel.md](09-service-tunnel.md)。
- 完整 API 见 [08-api-contract.md](08-api-contract.md)。
