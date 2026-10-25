# 14 · GUI 演示：模型调用时由 gateway 注入凭证

> 适用版本：**v0.1.3** 或更新。  
> 演示形态：真人操作 GUI；另开一个终端看 fake 上游收到的请求。  
> 不需要真实 OpenAI/ChatGPT 账号，也不会碰你的 Keychain 或正常 Nullkey 数据。

## 1. 这个演示说明什么

演示链路：

```text
GUI 启动 Codex sandbox
  → Codex 只有 per-sandbox NULLKEY_TOKEN
  → Codex 请求本地 gateway
  → gateway 用宿主 credential 换成 fake-oauth-token
  → fake 上游校验 Bearer 并返回 pong
```

观众应该看到三件事：

1. GUI 里 Codex 正常启动；
2. Codex 收到 `pong from fake upstream`；
3. fake 上游日志里出现 `Authorization: Bearer fake-oauth-token`，而这个值不在 sandbox 里。

**这不是**“任意第三方网站/CLI 的通用注入”。当前 v1 的注入面是模型、MCP、Git 三条网关；`gh`/`aws`/`kubectl`/`terraform` 的 Host Tool Gateway 是 v1.1，MCP connection 也还没有自动渲染进 Codex 配置。

## 2. 准备

- macOS Apple Silicon；
- 安装 **Nullkey v0.1.3**：<https://github.com/Mrjwj34/nullkey/releases/tag/v0.1.3>；
- 克隆仓库（fake 上游脚本在仓库里）：

```bash
git clone https://github.com/Mrjwj34/nullkey.git
cd nullkey
```

- Python 3、`curl`、`lsof`；
- 宿主 `codex` 已安装，并且至少运行过一次，确保存在：

```bash
ls -l ~/.codex/models_cache.json
```

- 49152、49153、49154、49960 未被占用；
- 退出正在运行的 Nullkey GUI 和 daemon（演示使用独立临时 home）。
安装 app 后如被 Gatekeeper 拦截：

```bash
xattr -dr com.apple.quarantine /Applications/Nullkey.app
```

## 3. 一键准备后端

在仓库根目录执行：

```bash
./scripts/demo-gui-setup.sh
```

它会：

- 检查 app、Codex、模型缓存和端口；
- 在临时 home `/tmp/nullkey-gui-demo` 启动隔离 daemon；
- 在 49960 启动 fake 鉴权上游；
- 预置 credential `Demo ChatGPT` 和 workspace `demo`；
- 打印 GUI 启动命令、fake 日志路径和清理方式。

脚本不会自动打开 GUI。它只是准备环境；演示发生在 GUI。

## 4. 终端 1：挂上 fake 日志

```bash
tail -f /tmp/nullkey-gui-demo/fake.log
```

这个终端留到演示结束。

## 5. 终端 2：启动 GUI（必须带同一个 home）

脚本会打印一条类似这样的命令，直接复制：

```bash
NULLKEY_HOME=/tmp/nullkey-gui-demo /Applications/Nullkey.app/Contents/MacOS/Nullkey
```

不要用 `open`，`open` 不会继承 `NULLKEY_HOME`。GUI 必须连到脚本启动的那个隔离 daemon。

如果 app 不在 `/Applications`，准备时用：

```bash
NULLKEY_APP=/path/to/Nullkey.app ./scripts/demo-gui-setup.sh
```

## 6. GUI 操作

打开后按顺序：

1. **连接 Codex** 应已打勾（凭证名 `Demo ChatGPT`）。
2. **安装 Codex** → 点「检查安装」，应通过。
3. **选择工作区** → 选 `demo`。
4. 点 **启动 Codex**。
5. 终端出现 Codex TUI 后，输入：

```text
hello
```

回车。

也可以输入：

```text
只回复：pong from fake upstream
```

不要让它执行命令、改文件或调用工具；fake 上游固定返回 `pong from fake upstream`，这次只演示模型调用凭证注入。

## 7. 演示时看什么

Codex 屏幕应出现：

```text
codex
pong from fake upstream
```

终端 1（fake 日志）应出现：

```text
GET /backend-api/codex/models?client_version=0.164.0 auth=Bearer fake-oauth-token account=
POST /backend-api/codex/responses auth=Bearer fake-oauth-token account=
```

可以在终端 2 或第三个终端补一刀：

```bash
grep env_key /tmp/nullkey-gui-demo/states/codex/*/home/.codex/config.toml
# env_key = "NULLKEY_TOKEN"
```

口播建议：

> Codex 在沙箱里只拿到 `NULLKEY_TOKEN`，它不知道 `fake-oauth-token`。  
> 请求到达宿主 gateway 后，gateway 才从 credential 里取出 `fake-oauth-token` 并注入 Authorization。  
> fake 服务只接受这个 bearer，所以屏幕上能看到 `pong`，但沙箱里看不到这个 token。

## 8. 清理

1. 在 GUI 里停止/删除 demo sandbox；
2. 退出 GUI；
3. 在仓库根目录执行：

```bash
./scripts/demo-gui-cleanup.sh
```

它会停掉 demo daemon/fake 上游，并删除 `/tmp/nullkey-gui-demo` 和 `/tmp/nullkey-gui-demo-ws`。

如果准备时用了非默认端口或路径，清理时带同样的环境变量，例如：

```bash
NULLKEY_DEMO_HOME=/tmp/nullkey-gui-demo \
NULLKEY_DEMO_CONTROL_PORT=49971 \
./scripts/demo-gui-cleanup.sh
```

## 9. 故障排查

**`port 49152 is already in use`**  
有旧 daemon/GUI 在跑。先退出 GUI，再：

```bash
lsof -nP -iTCP:49152 -sTCP:LISTEN
```

**`Nullkey GUI is already running`**  
退出正在运行的 Nullkey，再执行 setup。

**「检查安装」失败**  
说明 GUI/daemon 不是 v0.1.3，或启动的是旧 daemon：

```bash
/Applications/Nullkey.app/Contents/Resources/nullkeyd --version
```

应为 `0.1.3` 或更新。重新执行 setup，确保 GUI 用脚本打印的 `NULLKEY_HOME` 启动。

**Codex 没有回复 / fake 日志没有 POST**  
先看 fake 日志是否只有 `GET models`；再看：

```bash
tail -50 /tmp/nullkey-gui-demo/daemon.log
```

常见原因：

- GUI 没带 `NULLKEY_HOME`，连到了默认 home；
- fake 端口被别的进程占用；
- `~/.codex/models_cache.json` 不存在（先运行一次 `codex`）；
- Codex TUI 还停在引导/模型选择页。

**fake 返回 401**  
说明请求没有经过 gateway，或 credential 没配置好。检查 GUI 的 **管理 → 凭证** 是否有 `Demo ChatGPT`，以及 fake 日志里的 `Authorization`。

## 10. 演示边界（不要在视频里说错）

- ✅ 模型调用：Codex → gateway → fake 上游，凭证由 gateway 注入；
- ✅ Codex sandbox 启动、workspace 锁定；
- ⚠️ MCP gateway 能注入，但 v0.1.3 还没把 MCP connection 渲染进 Codex 的 `[mcp_servers.*]`，所以模型→MCP 工具调用暂时演不了；
- ⚠️ Git gateway 能注入 GitHub PAT，但需要额外的 fake Git HTTP 服务；
- ❌ `gh` / `aws` / `kubectl` / `terraform` 的 Host Tool Gateway 是 v1.1；
- ❌ 任意第三方网站 header 注入没有；egress proxy 只做 allowlist，不做 MITM。

如果要拍“模型调用 MCP 工具”，需要先补 Codex MCP 配置渲染，并准备 fake MCP server。
