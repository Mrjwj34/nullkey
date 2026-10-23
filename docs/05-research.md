# 05 · 调研与选型

> 调研时间：2026-10-23。开源项目迭代快，落地前再核对 license 与 API。

## 1. 结论

1. **"沙箱里只有占位符/token，真实凭证在沙箱外注入"是成熟模式**：microsandbox、E2B、Envoy 都有实现；不需要发明代理机制。
2. **默认 runtime 选原生 macOS 进程沙箱（Seatbelt），不选 VM**：Codex/Claude Code/DSH 都有 macOS 原生二进制；VM 的启动/内存代价与"轻量"目标冲突。
3. **网络与凭证正交**：互联网经 egress proxy，认证经 gateway；不需要为了安全把沙箱做成断网。
4. **OAuth 是一等公民**：Codex 开源、可做宿主 OAuth；MCP 用 OAuth 2.1 + DCR；Slack/Notion 等预注册 app 逐步接入。
5. 三天的主线是：native TUI + Codex + OAuth + gateway + 持久化状态 + API 契约，而不是虚拟化。

## 2. 直接先例

### 2.1 microsandbox Secrets

- 沙箱里只有占位符，真实值在宿主侧；发往允许主机时由宿主 TLS 代理替换。
- 目标校验：DNS + TLS 身份 + HTTP authority；header 白名单；违规 block/terminate。
- 仍是 beta。

**启示**：目标绑定、header 白名单、失败关闭是正确做法。  
**差异**：它是"环境变量占位符 + 平台代理"，不是"GUI 管 harness/MCP 登录 + 状态"。

来源：[repo](https://github.com/superradcompany/microsandbox) · [Secrets](https://docs.microsandbox.dev/sandboxes/secrets)

### 2.2 E2B Secrets

- Apache-2.0、Firecracker microVM、Desktop 模板、Secrets 生命周期。
- 自托管重；云托管意味着凭证经过第三方云。

来源：[repo](https://github.com/e2b-dev/E2B) · [Secrets](https://docs.e2b.dev/secrets)

### 2.3 Claude Code sandbox

- 官方 Bash sandbox 用 Seatbelt（macOS）/ bubblewrap（Linux）+ 网络代理 allowlist。
- 证明"原生进程沙箱 + 代理"是可用组合；不是只有 VM 才能隔离。

来源：[docs](https://code.claude.com/docs/en/sandboxing)

### 2.4 其它

| 方案 | 机制 | 与本项目的关系 |
|---|---|---|
| [Envoy `credential_injector`](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/credential_injector_filter) | 出口按 host 注入凭证 | 证明出口注入可行；本项目认证走 gateway，不做通用 MITM |
| [1Password secret references](https://developer.1password.com/docs/cli/secret-references/) | `op://` 运行时解析 | "引用而非值"的 UX 先例；假设进程可信 |
| [Vault Agent Injector](https://developer.hashicorp.com/vault/docs/deploy/kubernetes/injector) | 注入 Pod | 反例：凭证进了工作负载 |
| [MCP Security Best Practices](https://modelcontextprotocol.io/specification/2025-06-18/basic/security_best_practices) | confused deputy、token passthrough | 宿主侧 gateway 防 confused deputy |
| [The lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/) | 私有数据 + 不可信内容 + 外发 | 不在范围内；只解决凭证泄漏 |

## 3. Runtime 对比

| 方案 | License | 隔离 | 启动/内存 | 桌面 | 结论 |
|---|---|---|---|---|---|
| **Seatbelt（native）** | macOS 内置 | 进程沙箱 | **<1s / 低** | ❌（需另开 desktop） | **v1 默认** |
| Colima + Docker | Colima MIT / Docker Apache-2.0 | VM + 容器 | 10–30s / 数百 MB | 可 | 太重，不选 |
| Docker Desktop | 商业 | VM + 容器 | 同上 | 可 | 许可 + 重，不选 |
| microVM（microsandbox 等） | Apache-2.0 | microVM | 快 / 中 | 社区方案 | 可选 desktop 后端 |
| Apple `container` | Apache-2.0 | microVM | 快 / 中 | 无 | 可选 desktop 首选（macOS 26+） |
| E2B | Apache-2.0 | Firecracker | 云/自托管重 | ✅ | 长期/云端 |

**选择 Seatbelt 的理由**：

- Codex/Claude Code/DSH 都有 macOS 原生版本，不需要 Linux。
- 默认 TUI 不需要 X11/noVNC；省掉整个桌面栈。
- 启动/内存接近原生；符合 SLO。
- 进程沙箱 + 独立 workspace + Seatbelt 路径/网络限制，足以覆盖 session 泄漏威胁模型。

**代价**：

- 隔离弱于 VM；不宣称防 determined escape。
- 需要仔细写 Seatbelt profile；网络端口级限制要验证。
- 桌面/GUI 需要可选的虚拟化后端（不属于默认 SLO）。

## 4. OAuth 复杂度

| 场景 | 现状 | v1 |
|---|---|---|
| Codex 订阅 | 开源（Apache-2.0），OAuth/refresh 可复用 | **Must**：宿主 OAuth 或导入 |
| Claude Code 订阅 | 闭源；有 gateway 环境变量；OAuth 无第三方文档 | Should：`setup-token`/Keychain 导入 |
| MCP OAuth | 规范有 OAuth 2.1 + DCR；需要 metadata 发现、PKCE、401 重授权 | Should：通用 DCR client |
| Slack/Notion/Google | 需要预注册 OAuth app + scope/条款 | v1.1 |
| API key | 简单、稳定 | Must fallback |

**结论**：v1 做 OAuth Broker + Codex + GitHub + 通用 MCP DCR；其余逐个接入。不要承诺"所有 provider 一次到位"。

参考：[Claude Code LLM gateway](https://code.claude.com/docs/en/llm-gateway-connect) · [Codex](https://github.com/openai/codex) · [MCP client registration](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization/client-registration)

## 5. 网络

- 未认证流量（包管理器、公开文档、公开 git）经 egress proxy；认证流量（模型/MCP/git）经 gateway。
- 代理只做 CONNECT + host/SNI allowlist，不做 MITM、不装 CA。
- 默认 `filtered`；`offline` 用于敏感仓库；`open` 用于长尾。
- 参考实现：Squid（成熟）或自写 Go CONNECT proxy。

## 6. 对设计的启示

1. **凭证不进沙箱**：环境变量、配置、transcript、workspace 都不应该有。
2. **认证走 gateway**：模型、MCP、私有 git 都走专用端点，不走通用代理。
3. **网络可以放开**：包安装、文档、公开 git 是刚需；断网不是安全前提。
4. **默认 native**：VM 太重；Seatbelt 足够覆盖 session 泄漏。
5. **终端页优先**：GUI 默认是 SSH-client 风格终端，里面跑 harness 自己的 TUI；不是 GUI 采用 TUI 风格。
6. **状态持久化**：workspace/state/cache 默认保留，方便继续进度。
7. **OAuth 先做 Codex/MCP DCR**：不要为了"全 provider"拖垮三天。
8. **ingress 隧道**：类似 `ssh -L` 的本机转发，但只暴露声明服务 + ticket；让 WebUI-only harness 可用，也方便审查产物。

## 7. 参考

- microsandbox：[repo](https://github.com/superradcompany/microsandbox) · [Secrets](https://docs.microsandbox.dev/sandboxes/secrets)
- E2B：[repo](https://github.com/e2b-dev/E2B) · [Secrets](https://docs.e2b.dev/secrets)
- Claude Code sandbox：[docs](https://code.claude.com/docs/en/sandboxing)
- Claude Code gateway：[docs](https://code.claude.com/docs/en/llm-gateway-connect)
- Codex：[repo](https://github.com/openai/codex)
- MCP client registration：[docs](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization/client-registration)
- Envoy credential injector：[docs](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/credential_injector_filter)
- 1Password secret references：[docs](https://developer.1password.com/docs/cli/secret-references/)
- Vault Agent Injector：[docs](https://developer.hashicorp.com/vault/docs/deploy/kubernetes/injector)
- The lethal trifecta：[article](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/)
