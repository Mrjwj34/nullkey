# SLO

> 适用于 **native runtime（默认，无 VM）**。桌面 runtime 单独计量，不计入本 SLO。
>
> 参考硬件：Apple M1/M2/M3，16GB RAM，macOS 14+，本地 SSD。

## 1. 指标与目标

| # | 指标 | 目标 | 说明 |
|---|---|---|---|
| S1 | 冷启动到 Terminal 可输入 | p50 < 800ms，p95 < 1.5s | 客户端发起 `POST /sandboxes` 到收到绑定可写 PTY 的 WS `ready` 并启用输入；不等同于 harness 已就绪 |
| S2 | 热启动到 Terminal 可输入 | p95 < 500ms | 安装/文件缓存预热，创建新进程；终点与 S1 一致 |
| S3 | daemon 常驻内存 | < 80MB RSS | 空闲，无 sandbox |
| S4 | 单 sandbox 额外内存 | < 40MB | 不含 harness 本身 |
| S5 | Codex TUI 总内存 | p95 < 450MB | 含 harness；参考值 |
| S6 | Claude Code / DSH 总内存 | p95 < 700MB | Node 运行时，参考值 |
| S7 | 空闲 CPU | < 1% | 单 sandbox，无输出 |
| S8 | 终端输入 RTT | p95 < 50ms | 本机 GUI；按键到回显 |
| S9 | gateway 额外延迟 | p95 < 30ms | 非流式请求；不含上游 |
| S10 | 模型流式首字节额外延迟 | p95 < 50ms | 相对直连 |
| S11 | workspace 文件 I/O | ≥ 90% 原生 | 顺序读写；无 VM |
| S12 | 冷启动 3 个并发 sandbox | p95 < 3s | 16GB 机器 |
| S13 | 3 个 Codex sandbox 总内存 | < 1.5GB | 含 daemon |
| S14 | 凭证注入检查 | 受控样本 100% 0 命中 | 检查 Nullkey 生成的环境/配置；用户项目自带凭证不在覆盖范围 |
| S15 | 会话无崩溃率 | > 99% | MVP 目标；崩溃不丢 workspace |
| S16 | 隧道 RTT（本机） | p95 < 20ms | WebUI/preview |
| S17 | 隧道吞吐 | > 50MB/s | 本机 |
| S18 | 并发 WebSocket/SSE | ≥ 8 条/沙箱 | |
| S19 | 文件列表（10k） | p95 < 300ms | Artifacts |

> S5/S6 受 harness 版本影响；发布前用固定版本测。S14 验证 Nullkey 不注入所管理的凭证，不能证明任意 workspace 没有秘密。报告必须附检查范围、排除项和时间。另记录从同一启动起点到 harness TUI 可交互的 p50/p95；adapter 无法识别时标记“未测”，不能用 S1 代替。

## 2. 不包含

- 模型 API 的网络延迟/限流。
- egress proxy 到互联网的延迟（取决于目标站点）。
- 桌面 runtime（虚拟桌面/桌面 app）：单独目标，见下。
- 首次下载 harness/镜像的时间。
- 用户自定义 mount 的外部磁盘性能。

## 3. 桌面 runtime（可选，单独计量）

| 指标 | 目标 |
|---|---|
| 打开虚拟桌面（冷） | p95 < 20s |
| 打开虚拟桌面（热） | p95 < 8s |
| 桌面 runtime 内存 | < 800MB |
| 桌面输入 RTT | p95 < 80ms |

桌面 runtime 需要虚拟化；默认 TUI 不启动它，因此不影响 S1–S13。

## 4. 测量方法

启动基准由 GUI 测试客户端使用同一个单调时钟记录：

1. 发起 `POST /sandboxes` 前记 `t0`；HTTP 201 / `status=starting` 只表示已创建。
2. 申请 terminal ticket；`terminal_not_ready` 时继续观察状态并重试，等待时间计入总耗时。
3. 建立 WebSocket；收到绑定可写 PTY 的 `ready` 且界面启用输入时记 `t1`；S1/S2 = `t1 - t0`。不能以 WS open 或 ticket 签发作为终点。
4. adapter 或 UI 检查确认 harness 已显示可交互提示时另记 `t2`，报告 `t2 - t0`；同时用输入/回显检查验证终端可用。
5. 冷/热样本分别运行 30 次，失败/超时单列，不丢弃失败样本后声称达标。冷启动无存活 sandbox/harness 进程；热启动允许安装/文件缓存预热，但仍创建新进程。

```bash
# 内存
ps -o rss= -p <pid>   # KB

# CPU
ps -o %cpu= -p <pid>

# 终端 RTT
# 前端记录 input timestamp；PTY echo 回来时打点；取 p95

# gateway 开销：对同一受控上游 stub 交替执行直连和 gateway 转发
# 使用相同请求/响应；分别测非流式总耗时和流式首字节，再统计额外开销
# /health 不经过转发链路，不能用它作为 S9/S10 的测量值

# 文件 I/O
dd if=/dev/zero of=workspace/test.bin bs=1m count=512
# 对比宿主同目录
```

建议每次发布跑 30 次取 p50/p95；记录 macOS 版本、芯片、harness 版本。

## 5. 验收

- [ ] S1/S2 按 POST → WS ready → 输入启用测量达标（各 30 次）；另报 harness 就绪时间及失败/超时数
- [ ] S3/S4 达标
- [ ] S5 或 S6 至少一个达标（按主推 harness）
- [ ] S8/S9 达标
- [ ] S11 达标
- [ ] S13 达标
- [ ] S14 在无自带凭证的演示项目中为 0，并展示检查范围、排除项和时间
- [ ] S16/S17 达标（如交付 tunnel）
- [ ] SLO 结果写入发布记录
