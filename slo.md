# SLO

> 适用于 **native runtime（默认，无 VM）**。桌面 runtime 单独计量，不计入本 SLO。
>
> 参考硬件：Apple M1/M2/M3，16GB RAM，macOS 14+，本地 SSD。

## 1. 指标与目标

| # | 指标 | 目标 | 说明 |
|---|---|---|---|
| S1 | 冷启动到 Terminal 可输入 | p50 < 800ms，p95 < 1.5s | 从 `POST /sandboxes` 到 WS 可写 |
| S2 | 热启动到 Terminal 可输入 | p95 < 500ms | 进程已在 cache |
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
| S14 | 无凭证扫描 | 100% 0 命中 | 硬指标；架构保证 |
| S15 | 会话无崩溃率 | > 99% | MVP 目标；崩溃不丢 workspace |

> S5/S6 受 harness 版本影响；发布前用固定版本测。S14 不是"测出来的"，是"凭证从未进入沙箱"的构造保证。

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

```bash
# 冷启动：从 POST 到 WS ready
time curl -s -X POST .../sandboxes -d '...'

# 内存
ps -o rss= -p <pid>   # KB

# CPU
ps -o %cpu= -p <pid>

# 终端 RTT
# 前端记录 input timestamp；PTY echo 回来时打点；取 p95

# gateway 开销
curl -w '%{time_total}\n' http://127.0.0.1:<gateway>/health
# 与直连上游对比；排除上游波动

# 文件 I/O
dd if=/dev/zero of=workspace/test.bin bs=1m count=512
# 对比宿主同目录
```

建议每次发布跑 30 次取 p50/p95；记录 macOS 版本、芯片、harness 版本。

## 5. 验收

- [ ] S1/S2 达标（30 次）
- [ ] S3/S4 达标
- [ ] S5 或 S6 至少一个达标（按主推 harness）
- [ ] S8/S9 达标
- [ ] S11 达标
- [ ] S13 达标
- [ ] S14 在演示沙箱为 0
- [ ] SLO 结果写入发布记录
