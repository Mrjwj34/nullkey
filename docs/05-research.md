# 05 · 调研与选型

> 调研时间：2026-10-23。开源项目迭代快，落地前再核对 license 与 API。

## 1. 结论

1. **"沙箱里只有占位符/token，真实凭证在沙箱外注入"已经是成熟模式**：microsandbox、E2B、Envoy 都有实现。我们不需要发明新机制，只需要把机制用在 harness + MCP 的产品体验上。
2. 但它们的粒度是"平台级 secrets"：环境变量占位符 + 目标主机。**没有解决"用户在 GUI 里配一次，Claude Code / Codex / DSH 和 MCP 全都不用再填 key"这个产品问题。**
3. v1 底座选 **Colima（MIT）+ Docker + 自建 Ubuntu ARM 桌面镜像**：本地 VM 边界、Apache/MIT、Apple Silicon、无需 Docker Desktop 商业许可。
4. microsandbox / Apple `container` 作为 v0.2 的 microVM 后端，通过 `SandboxProvider` 替换；不要在三天里赌 beta。

## 2. 直接先例

### 2.1 microsandbox Secrets

- 沙箱里只有占位符（默认 `$MSB_<ENV_VAR>`），真实值在宿主侧。
- 发往允许的主机时，由宿主侧 TLS 代理在 guest 之外替换。
- 目标校验：DNS + TLS 身份 + HTTP authority；"伪造 hostname 或写死 IP 不够"。
- 替换位置：默认 header（含 Basic Auth），query/body 默认关闭；header 字段可进一步限制。
- 违规动作：`block` / `block-and-log` / `block-and-terminate`。
- 默认要求 TLS 拦截；仍是 beta。

**启示**：目标绑定 + TLS 身份 + header 白名单 + 失败关闭，是正确做法。  
**差异**：它是"平台给沙箱进程注入环境变量"，不是"GUI 管 harness/MCP 配置"。

来源：[repo](https://github.com/superradcompany/microsandbox) · [Secrets](https://docs.microsandbox.dev/sandboxes/secrets)

### 2.2 E2B Secrets

- Apache-2.0、Firecracker microVM、成熟。
- 有 Desktop 模板、Secrets 的创建/注入/轮换/删除/列表、workload identity。
- 自托管需要 Terraform + 云资源，三天不划算；用云托管则"个人 key 经过第三方云"。

来源：[repo](https://github.com/e2b-dev/E2B) · [Secrets](https://docs.e2b.dev/secrets) · [Desktop](https://docs.e2b.dev/template/examples/desktop)

### 2.3 Daytona Secrets

- 0.192.0 加入 secrets。
- 主体 Apache-2.0，CLI 仍 AGPL-3.0；面向 dev environment / sandbox，没有现成 GUI 桌面。

来源：[repo](https://github.com/daytonaio/daytona) · [changelog](https://www.daytona.io/changelog/secrets-support-and-go-sdk-module-path-migration)

### 2.4 其它可借鉴的机制

| 方案 | 机制 | 与我们的关系 |
|---|---|---|
| [Envoy `credential_injector`](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/credential_injector_filter) | 出口 HTTP filter，按 host 注入凭证 | 证明"出口注入"可行；我们不做通用 MITM |
| [1Password `op run` / secret references](https://developer.1password.com/docs/cli/secret-references/) | 脚本写 `op://...`，运行时解析 | "引用而非值"的 UX 先例；假设进程可信 |
| [Vault Agent Injector](https://developer.hashicorp.com/vault/docs/deploy/kubernetes/injector) | 把密钥注入 Pod | 反例：密钥进了工作负载 |
| git credential helper / ssh-agent | 不持有长期 key，只拿能力 | 未来 CLI 的方向 |
| [Claude Code sandbox](https://code.claude.com/docs/en/sandboxing) | 文件系统隔离 + 网络代理 allowlist | 业界同款"默认断网 + 代理" |
| [MCP Security Best Practices](https://modelcontextprotocol.io/specification/2025-06-18/basic/security_best_practices) | confused deputy、token passthrough | 宿主侧网关防 confused deputy |
| [The lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/) | 私有数据 + 不可信内容 + 外发 = 泄漏 | 我们明确不解决这一段；只解决 key 泄漏 |

## 3. 底座对比

| 方案 | License | 隔离 | 现成桌面 | 三天适配 | 结论 |
|---|---|---|---|---|---|
| **Colima + Docker + 自建镜像** | Colima MIT；Docker Apache-2.0 | VM + 容器 | ✅ 自建 Xfce + noVNC | **最高** | **v1 主选** |
| **Docker Desktop** | 商业许可 | VM + 容器 | ✅ Webtop | 高 | 保底（有许可风险） |
| **Lima 直接用** | Apache-2.0 | VM | 需自建桌面 | 中 | 备选 |
| **microsandbox** | Apache-2.0 | microVM | 社区镜像，非官方稳定 | 中低（beta） | v0.2 |
| **Apple `container`** | Apache-2.0 | 每容器轻量 VM | ❌ | 低（太新） | 观察 |
| **E2B** | Apache-2.0 | Firecracker | ✅ | 低（自托管重） | 长期 |
| **Daytona** | Apache-2.0 + CLI AGPL | 容器/VM | ❌ | 中低 | 不选 |
| **OpenHands runtime** | MIT | Docker | 浏览器工具，非桌面 | 中 | 是 Agent，不是中性底座 |

补充：

- 本机已装 Docker 29.4.1、Colima 0.10.1、Lima 2.1.1；没有 Apple `container`。
- Docker daemon 跑在 Ubuntu 24.04 aarch64 的 VM 里。
- Colima 的 VM 边界 + Docker 容器隔离，对"session 泄漏"这个威胁模型足够；对"恶意 Agent 逃逸"不宣称强隔离。
- 如果评委要求 microVM，v0.2 用 microsandbox / Apple `container`；接口已经留好。

## 4. 选型理由

### 为什么 v1 用 Colima，而不是 microsandbox

- microsandbox 是 beta；桌面、端口转发、宿主 IPC 的不确定性会吃掉 D1。
- Colima 已安装、MIT、Apple Silicon 成熟；Docker 生态的镜像/网络/工具链齐全。
- 我们的核心风险是"配置注入 + 网关 + GUI"，不是"虚拟化层"；先把产品链路跑通。
- `SandboxProvider` 让 microVM 成为 2–4 小时的替换，而不是重写。

### 为什么自建桌面镜像

- arm64 的现成 Webtop 镜像可用性不稳定；自建 Debian/Ubuntu + Xfce + Xvfb + noVNC 最可控。
- 镜像可以预装 harness 运行依赖（node、python、git、ripgrep），减少运行时下载。
- 镜像固定 digest；不带任何 secret layer。

### 为什么不是 Docker Desktop

- 商业使用需要订阅；Colima 是 MIT，本地优先，符合"开源底座二开"。
- Docker Desktop 作为保底（如果现场 Colima 网络不通）。

## 5. 对设计的启示

1. **key 不能放 env**：session 泄漏第一条就是 `env`/transcript。
2. **base URL + per-sandbox token 足够覆盖主流 harness**：不需要模型学习占位符。
3. **有凭证的 MCP 必须宿主侧运行**：MCP server 自己需要 token，放沙箱就破功。
4. **默认断网**：沙箱只留 gateway 一条路；这比"允许上网 + 代理"简单得多。
5. **per-sandbox token**：可撤销、可审计、不是长期密钥；泄漏影响有限。
6. **OAuth 是产品缺口**：订阅用户多，但 v1 只能 API key；要提前和用户/评委说清楚。
7. **不要过度承诺**：prompt injection、恶意 Agent、上游劫持都不在范围；把"session 泄漏"讲透。
8. **验证方式要硬**：导出整个沙箱 home/镜像，扫描无 key；这比任何架构图都有说服力。

## 6. 参考链接

- microsandbox：[repo](https://github.com/superradcompany/microsandbox) · [Secrets](https://docs.microsandbox.dev/sandboxes/secrets)
- E2B：[repo](https://github.com/e2b-dev/E2B) · [Secrets](https://docs.e2b.dev/secrets) · [Desktop](https://docs.e2b.dev/template/examples/desktop)
- Daytona：[repo](https://github.com/daytonaio/daytona) · [changelog](https://www.daytona.io/changelog/secrets-support-and-go-sdk-module-path-migration)
- Colima：[repo](https://github.com/abiosoft/colima)（MIT）
- Lima：[repo](https://github.com/lima-vm/lima)（Apache-2.0）
- Apple container：[repo](https://github.com/apple/container)
- OpenHands：[repo](https://github.com/All-Hands-AI/OpenHands)（MIT）
- Envoy credential injector：[文档](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/credential_injector_filter)
- 1Password secret references：[文档](https://developer.1password.com/docs/cli/secret-references/)
- Vault Agent Injector：[文档](https://developer.hashicorp.com/vault/docs/deploy/kubernetes/injector)
- Claude Code sandbox：[文档](https://code.claude.com/docs/en/sandboxing)
- MCP Security Best Practices：[文档](https://modelcontextprotocol.io/specification/2025-06-18/basic/security_best_practices)
- The lethal trifecta：[文章](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/)
