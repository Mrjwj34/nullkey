# 07 · 状态、文件挂载与持久化

> 目标：关掉沙箱再打开，进度还在；多个沙箱能复用 workspace/state/cache；同时不把宿主敏感目录带进来。

## 1. 三类状态

| 类型 | 内容 | 默认 | 共享 |
|---|---|---|---|
| **workspace** | 项目文件、代码、构建产物 | 持久化 | 可 rw 独占 / ro 共享 |
| **state** | harness 配置、会话、历史、登录缓存（不含真实凭证） | 持久化 | 默认单沙箱独占；可 ro 共享 |
| **cache** | harness 安装包、npm/pip/cargo 缓存 | 持久化 | 全局共享 |

真实凭证不在以上任何一类里：只在 Keychain，由 gateway 注入。

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
- 锁信息：`workspace_locks(workspace_id, sandbox_id, mode, acquired_at)`；daemon 重启后清理孤儿锁。

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
- 停止沙箱默认保留 workspace/state；删除 sandbox 不删状态，除非显式 `delete_state/delete_workspace`。

## 7. 重置与清理

| 操作 | 影响 |
|---|---|
| Reset state | 删除 harness 配置/会话；保留 workspace |
| Reset workspace | 危险；需二次确认；默认保留 git 仓库内容？不做自动删除 |
| Clear cache | 删除 harness 安装包/包管理器缓存；下次重装 |
| Delete sandbox | 停进程、撤 token；默认保留 workspace/state |
| Delete workspace | 显式删除目录；需输入名称确认 |

- 提供 `nullkey gc` / GUI "清理"：按 LRU 删除未使用 cache，不碰 workspace。
- 审计记录 reset/delete 操作。

## 8. 安全边界

- 持久化状态可能包含用户代码、transcript、历史；**不包含真实凭证**。
- 共享 workspace 可能包含用户自己的 secrets（`.env`）；这是用户数据，不扫描、不承诺。
- state 里的 harness 登录缓存：v1 不使用（OAuth token 在 Keychain）；如果未来使用，必须加密。
- 备份/Time Machine 会备份 workspace/state；因为没有真实凭证，可接受；用户可选择排除 cache。
- 自定义 mount 是最大的风险点；默认关闭，需确认，且禁止敏感路径。

## 9. 与 runtime 的关系

- **Native**：直接使用宿主目录，无需 bind mount；Seatbelt 只允许 workspace/state/cache 路径。
- **Desktop（可选）**：bind mount 同样的目录到 Linux 桌面；只读 cache，rw workspace/state。
- 两种 runtime 使用同一套 workspace/state/cache 语义，切换不丢进度。

## 10. API 字段

- `POST /workspaces` `{name, root_path?}`
- `POST /sandboxes` `{harness_id, workspace_id, state_id?, mounts?}`
- `GET /workspaces/{id}` 返回 `last_sandbox_id`、`size_bytes`、`locked_by`
- `POST /workspaces/{id}/reset`、`POST /states/{id}/reset`
- 产物审查（文件树/预览/diff）与服务隧道见 [09-service-tunnel.md](09-service-tunnel.md)。
- 完整 API 见 [08-api-contract.md](08-api-contract.md)。
