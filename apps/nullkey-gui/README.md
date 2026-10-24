# Nullkey GUI

Nullkey 的 macOS 桌面客户端，Flutter + Forui。界面按 `docs/00-product.md` 和
`docs/10-interaction-contract.md` 实现：侧边栏是工作区列表，主区域是所选工作区的当前会话
（终端 / 产物 / 服务 / 桌面），凭证、MCP 和活动记录放在“管理”里。

客户端只经 `api/openapi.yaml` 描述的端点访问 daemon（`lib/services/daemon_backend.dart`），
连接按以下顺序解析：

1. `NULLKEY_DAEMON_URL` + `NULLKEY_DAEMON_TOKEN`（`--dart-define` 或同名环境变量）；
2. daemon 写出的 `<NULLKEY_HOME>/daemon-token`，默认
   `~/Library/Application Support/Nullkey/daemon-token`，地址用 `http://127.0.0.1:49152`；
3. 都没有时显示未连接，并自动等待 daemon 的 token 文件。不会隐式进入模拟模式。

Core Mock 引擎（`lib/services/core_mock_backend.dart`）须显式设置 `NULLKEY_DEMO=1`。

规则在 `lib/services/daemon_connection.dart`。为了读 token 文件，Debug 和 Release 都不开
app sandbox。Core Mock 按 `docs/08-api-contract.md` 的错误码、锁和任务状态机运行，
`main.dart` 给它加了 450 ms 的节奏，让启动阶段、安装进度和浏览器登录这些中间状态能被看到；
侧边栏底部的“模拟数据”里可以触发断线重连、nullkeyd 重启、网络拒绝、会话数据被占用等场景，
或回到首次使用。

## 本地运行

先在仓库根目录起 daemon（不设 `NULLKEY_DAEMON_TOKEN`，让它写 token 文件）：

```sh
make build && ./bin/nullkeyd --listen 127.0.0.1:49152
```

再运行客户端：

```sh
cd apps/nullkey-gui
flutter pub get
flutter run -d macos          # 自动发现本机 daemon；没有 token 就等待连接
flutter run -d macos --dart-define=NULLKEY_DEMO=1  # 显式演示模式
```

连别的端口或别的 token 时显式传：

```sh
flutter run -d macos \
  --dart-define=NULLKEY_DAEMON_URL=http://127.0.0.1:49152 \
  --dart-define=NULLKEY_DAEMON_TOKEN=dev-token-0123456789abcdef
```

已验证环境：Flutter 3.47.7 stable、Dart 3.13.5、Xcode 27.1、CocoaPods 1.17.0。

## 界面约定

- 系统字体（SF Pro，中文回落到苹方），等宽内容用 Menlo；Lucide 图标；跟随系统明暗模式和“增强对比度”。
- 窗口使用透明标题栏，内容延伸到顶部；`lib/widgets/common.dart` 的 `titlebarInset` 为红绿灯留出 28pt。
- 主题在 `lib/theme/`，从 Forui 预设生成后改成了 macOS 的密度、配色和字体；不要用 CLI 覆盖。
- 常用快捷键：⌘⇧M 把键盘焦点移到工作区列表，⌘N 添加工作区，⌘1–4 切换会话视图，⌘L 锁定，⌘/ 查看全部快捷键。

## 验证

真实 daemon 模式的敏感操作使用 macOS LocalAuthentication（Touch ID 或系统密码），
取消或认证不可用时停止操作。当前本机验证回执使用软件 P-256 签名，不是 WebAuthn
Passkey 或 Secure Enclave 密钥；该回执不参与 daemon API 鉴权。

```sh
flutter analyze
flutter test
flutter build macos --debug
```

真实后端集成测试（CI 自动运行，使用临时目录、file secret backend 和假凭证）：

```sh
# 在仓库根目录构建，再从 GUI 目录运行
go build -o bin/nullkeyd ./cmd/nullkeyd
cd apps/nullkey-gui
NULLKEY_TEST_DAEMON="$PWD/../../bin/nullkeyd" flutter test test/daemon_integration_test.dart
```

发布产物（ad-hoc 签名的 `.app` 与 dmg）由 `.github/workflows/release.yml` 构建，
见仓库 README 的“安装（发布产物）”。
