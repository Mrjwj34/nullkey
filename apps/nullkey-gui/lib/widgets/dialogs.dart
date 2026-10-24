import 'package:flutter/widgets.dart';
import 'package:forui/forui.dart';
import 'package:material_ui/material_ui.dart' show SelectableText;

import '../models/nullkey_models.dart';
import '../state/nullkey_studio_controller.dart';
import 'common.dart';

/// Standard sheet layout: title, optional explanation, content, and buttons
/// on the trailing edge with the default action last.
class AppDialog extends StatelessWidget {
  final Animation<double> animation;
  final String title;
  final String? description;
  final Widget? child;
  final List<Widget> actions;
  final double width;

  const AppDialog({
    required this.animation,
    required this.title,
    required this.actions,
    this.description,
    this.child,
    this.width = 460,
    super.key,
  });

  @override
  Widget build(BuildContext context) => FDialog(
    animation: animation,
    semanticsLabel: title,
    constraints: BoxConstraints(minWidth: width, maxWidth: width),
    builder: (context, style) => Padding(
      padding: const EdgeInsets.fromLTRB(20, 18, 20, 16),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            title,
            style: context.typo.md.copyWith(
              fontWeight: FontWeight.w600,
              color: context.colors.foreground,
            ),
          ),
          if (description != null) ...[
            const SizedBox(height: 4),
            Text(
              description!,
              style: context.bodyStyle.copyWith(
                color: context.colors.mutedForeground,
              ),
            ),
          ],
          if (child != null) ...[
            const SizedBox(height: 16),
            Flexible(child: SingleChildScrollView(child: child)),
          ],
          const SizedBox(height: 20),
          Wrap(
            alignment: WrapAlignment.end,
            spacing: 8,
            runSpacing: 8,
            children: actions,
          ),
        ],
      ),
    ),
  );
}

Widget dialogButton(
  String label,
  VoidCallback? onPress, {
  FButtonVariant variant = .outline,
  Key? key,
}) => FButton(
  key: key,
  variant: variant,
  size: .sm,
  mainAxisSize: MainAxisSize.min,
  onPress: onPress,
  child: Text(label),
);

class _FieldLabel extends StatelessWidget {
  final String text;

  const _FieldLabel(this.text);

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.only(bottom: 8),
    child: Text(text, style: context.strongStyle),
  );
}

class _ErrorText extends StatelessWidget {
  final String text;

  const _ErrorText(this.text);

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.only(top: 12),
    child: Semantics(
      liveRegion: true,
      child: Text(
        text,
        style: context.bodyStyle.copyWith(color: context.colors.destructive),
      ),
    ),
  );
}

/// A full path the user must be able to read in its entirety.
class PathBox extends StatelessWidget {
  final String path;

  const PathBox(this.path, {super.key});

  @override
  Widget build(BuildContext context) => Container(
    width: double.infinity,
    padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
    decoration: BoxDecoration(
      color: context.colors.muted,
      borderRadius: BorderRadius.circular(6),
      border: Border.all(color: context.colors.border),
    ),
    child: SelectableText(path, style: context.mono()),
  );
}

// -----------------------------------------------------------------------------
// Add workspace
// -----------------------------------------------------------------------------

Future<void> showCreateWorkspaceDialog(
  BuildContext context,
  NullkeyStudioController c, {
  WorkspaceKind initialKind = WorkspaceKind.managed,
  bool openAfterCreate = true,
}) => showFDialog<void>(
  context: context,
  builder: (context, style, animation) => _CreateWorkspaceDialog(
    controller: c,
    animation: animation,
    initialKind: initialKind,
    openAfterCreate: openAfterCreate,
  ),
);

class _CreateWorkspaceDialog extends StatefulWidget {
  final NullkeyStudioController controller;
  final Animation<double> animation;
  final WorkspaceKind initialKind;
  final bool openAfterCreate;

  const _CreateWorkspaceDialog({
    required this.controller,
    required this.animation,
    required this.initialKind,
    required this.openAfterCreate,
  });

  @override
  State<_CreateWorkspaceDialog> createState() => _CreateWorkspaceDialogState();
}

class _CreateWorkspaceDialogState extends State<_CreateWorkspaceDialog> {
  late WorkspaceKind _kind = widget.initialKind;
  final _name = TextEditingController();
  final _path = TextEditingController();
  MountMode _mode = MountMode.rw;
  bool _nameEdited = false;
  bool _busy = false;
  String? _error;

  @override
  void dispose() {
    _name.dispose();
    _path.dispose();
    super.dispose();
  }

  void _onPathChanged(TextEditingValue value) {
    if (_nameEdited) {
      return;
    }
    final segments = value.text
        .trim()
        .split('/')
        .where((s) => s.isNotEmpty)
        .toList();
    _name.text = segments.isEmpty ? '' : segments.last;
  }

  Future<void> _submit() async {
    if (_busy) {
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });
    final error = await widget.controller.createWorkspace(
      name: _name.text,
      kind: _kind,
      hostPath: _kind == WorkspaceKind.external ? _path.text : null,
      defaultMode: _kind == WorkspaceKind.external ? _mode : MountMode.rw,
      open: widget.openAfterCreate,
    );
    if (!mounted) {
      return;
    }
    if (error == null) {
      Navigator.of(context).pop();
    } else {
      setState(() {
        _busy = false;
        _error = error;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final external = _kind == WorkspaceKind.external;
    return AppDialog(
      animation: widget.animation,
      title: '添加工作区',
      width: 460,
      actions: [
        dialogButton('取消', () => Navigator.of(context).pop()),
        dialogButton(
          '添加',
          _busy ? null : _submit,
          variant: .primary,
          key: const Key('create-workspace-submit'),
        ),
      ],
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          SegmentedControl<WorkspaceKind>(
            semanticsLabel: '工作区类型',
            selected: _kind,
            onChanged: (kind) => setState(() {
              _kind = kind;
              _error = null;
            }),
            segments: const [
              Segment(
                value: WorkspaceKind.managed,
                label: '新建文件夹',
                key: Key('create-workspace-kind-managed'),
              ),
              Segment(
                value: WorkspaceKind.external,
                label: '已有文件夹',
                key: Key('create-workspace-kind-external'),
              ),
            ],
          ),
          const SizedBox(height: 16),
          if (external) ...[
            FTextField(
              key: const Key('create-workspace-path-field'),
              control: .managed(controller: _path, onChange: _onPathChanged),
              label: const Text('位置'),
              hint: '/Users/you/projects/app',
              autofocus: true,
              autocorrect: false,
              onSubmit: (_) => _submit(),
            ),
            const SizedBox(height: 12),
          ],
          FTextField(
            key: const Key('create-workspace-name-field'),
            control: .managed(
              controller: _name,
              onChange: (_) => _nameEdited = true,
            ),
            label: const Text('名称'),
            hint: 'my-project',
            autofocus: !external,
            autocorrect: false,
            onSubmit: (_) => _submit(),
          ),
          const SizedBox(height: 8),
          if (!external)
            Text(
              '将在 ~/Library/Application Support/Nullkey/workspaces/ 中新建。',
              style: context.secondaryStyle,
            )
          else ...[
            const SizedBox(height: 8),
            const _FieldLabel('权限'),
            FRadio(
              key: const Key('create-workspace-mode-rw'),
              label: const Text('读写'),
              description: const Text('直接修改原文件'),
              value: _mode == MountMode.rw,
              onChange: (_) => setState(() => _mode = MountMode.rw),
            ),
            const SizedBox(height: 8),
            FRadio(
              key: const Key('create-workspace-mode-ro'),
              label: const Text('只读'),
              description: const Text('不能修改文件'),
              value: _mode == MountMode.ro,
              onChange: (_) => setState(() => _mode = MountMode.ro),
            ),
            const SizedBox(height: 14),
            const InlineAlert(
              tone: Tone.warning,
              title: '.env 等文件不受 Nullkey 保护',
              body: 'Agent 能读取这个文件夹里的所有文件。',
            ),
          ],
          if (_error != null) _ErrorText(_error!),
        ],
      ),
    );
  }
}

// -----------------------------------------------------------------------------
// Launch options
// -----------------------------------------------------------------------------

Future<void> showLaunchOptionsDialog(
  BuildContext context,
  NullkeyStudioController c,
  WorkspaceRecord workspace,
) => showFDialog<void>(
  context: context,
  builder: (context, style, animation) => _LaunchOptionsDialog(
    controller: c,
    workspace: workspace,
    animation: animation,
  ),
);

class _LaunchOptionsDialog extends StatefulWidget {
  final NullkeyStudioController controller;
  final WorkspaceRecord workspace;
  final Animation<double> animation;

  const _LaunchOptionsDialog({
    required this.controller,
    required this.workspace,
    required this.animation,
  });

  @override
  State<_LaunchOptionsDialog> createState() => _LaunchOptionsDialogState();
}

class _LaunchOptionsDialogState extends State<_LaunchOptionsDialog> {
  late HarnessKind _harness;
  late MountMode _mode;
  late NetworkMode _network;
  bool _desktop = false;

  NullkeyStudioController get c => widget.controller;

  @override
  void initState() {
    super.initState();
    final d = c.launchDefaults(widget.workspace);
    _harness = d.harness;
    _mode = d.mode;
    _network = d.network;
  }

  void _launch() {
    Navigator.of(context).pop();
    c.launch(
      workspaceId: widget.workspace.id,
      harness: _harness,
      mode: _mode,
      network: _network,
      enableDesktop: _desktop,
    );
  }

  @override
  Widget build(BuildContext context) {
    final external = widget.workspace.kind == WorkspaceKind.external;

    return AppDialog(
      animation: widget.animation,
      title: '启动选项',
      description: '之后的启动会沿用这些设置。',
      width: 480,
      actions: [
        dialogButton('取消', () => Navigator.of(context).pop()),
        dialogButton(
          '启动',
          c.isHarnessInstalled(_harness) ? _launch : null,
          variant: .primary,
          key: const Key('launch-options-start'),
        ),
      ],
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const _FieldLabel('Agent'),
          for (final h in c.harnesses) ...[
            FRadio(
              key: Key('launch-option-harness-${h.kind.wireName}'),
              label: Text(
                h.version == null ? h.name : '${h.name} ${h.version}',
              ),
              description: c.isHarnessInstalled(h.kind)
                  ? null
                  : const Text('未安装'),
              enabled: c.isHarnessInstalled(h.kind),
              value: _harness == h.kind,
              onChange: (_) => setState(() => _harness = h.kind),
            ),
            const SizedBox(height: 8),
          ],
          const SizedBox(height: 10),
          const _FieldLabel('网络'),
          for (final mode in c.capabilities.networkModes) ...[
            FRadio(
              key: Key('launch-option-network-${mode.wireName}'),
              label: Text(mode.label),
              description: Text(mode.summary),
              value: _network == mode,
              onChange: (_) => setState(() => _network = mode),
            ),
            const SizedBox(height: 8),
          ],
          Text(
            NetworkMode.remoteGatewayDisclosure,
            style: context.secondaryStyle,
          ),
          if (external && c.capabilities.perLaunchMountMode) ...[
            const SizedBox(height: 18),
            const _FieldLabel('文件夹权限'),
            FRadio(
              key: const Key('launch-option-mode-rw'),
              label: const Text('读写'),
              description: const Text('直接修改原文件'),
              value: _mode == MountMode.rw,
              onChange: (_) => setState(() => _mode = MountMode.rw),
            ),
            const SizedBox(height: 8),
            FRadio(
              key: const Key('launch-option-mode-ro'),
              label: const Text('只读'),
              description: const Text('不能修改文件'),
              value: _mode == MountMode.ro,
              onChange: (_) => setState(() => _mode = MountMode.ro),
            ),
          ],
          if (c.capabilities.desktopAvailable) ...[
            const SizedBox(height: 18),
            FSwitch(
              key: const Key('launch-option-desktop'),
              label: const Text('同时打开虚拟桌面'),
              value: _desktop,
              onChange: (v) => setState(() => _desktop = v),
            ),
          ],
        ],
      ),
    );
  }
}

// -----------------------------------------------------------------------------
// Remove / delete workspace
// -----------------------------------------------------------------------------

Future<void> showRemoveWorkspaceDialog(
  BuildContext context,
  NullkeyStudioController c,
  WorkspaceRecord workspace,
) => showFDialog<void>(
  context: context,
  builder: (context, style, animation) => _RemoveWorkspaceDialog(
    controller: c,
    workspace: workspace,
    animation: animation,
  ),
);

class _RemoveWorkspaceDialog extends StatefulWidget {
  final NullkeyStudioController controller;
  final WorkspaceRecord workspace;
  final Animation<double> animation;

  const _RemoveWorkspaceDialog({
    required this.controller,
    required this.workspace,
    required this.animation,
  });

  @override
  State<_RemoveWorkspaceDialog> createState() => _RemoveWorkspaceDialogState();
}

class _RemoveWorkspaceDialogState extends State<_RemoveWorkspaceDialog> {
  final _confirm = TextEditingController();
  bool _busy = false;
  String? _error;

  @override
  void dispose() {
    _confirm.dispose();
    super.dispose();
  }

  bool get _managed => widget.workspace.kind == WorkspaceKind.managed;

  bool get _confirmed =>
      !_managed || _confirm.text.trim() == widget.workspace.name;

  Future<void> _submit() async {
    if (_busy || !_confirmed) {
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });
    final error = await widget.controller.removeWorkspace(
      workspaceId: widget.workspace.id,
      deleteFiles: _managed,
      confirmName: _managed ? _confirm.text : null,
    );
    if (!mounted) {
      return;
    }
    if (error == null) {
      Navigator.of(context).pop();
    } else {
      setState(() {
        _busy = false;
        _error = error;
      });
    }
  }

  @override
  Widget build(BuildContext context) => ListenableBuilder(
    listenable: widget.controller,
    builder: (context, _) => _build(context),
  );

  Widget _build(BuildContext context) {
    final c = widget.controller;
    final ws = widget.workspace;
    // The daemon refuses with 409 state_locked while a sandbox uses the
    // workspace; saying so up front saves typing the name for nothing.
    final inUse =
        c.sandboxForWorkspace(ws.id)?.status.isRunningOrStarting ?? false;
    final inUseAlert = inUse
        ? Padding(
            padding: const EdgeInsets.only(bottom: 14),
            child: InlineAlert(
              key: const Key('remove-workspace-in-use-alert'),
              tone: Tone.warning,
              title: '停止沙箱后才能${_managed ? '删除' : '移除'}',
              actions: [
                if (c.selectedWorkspaceId == ws.id)
                  SmallButton(label: '停止沙箱', onPress: c.stopSelectedSandbox),
              ],
            ),
          )
        : null;
    if (!_managed) {
      return AppDialog(
        animation: widget.animation,
        title: '从列表移除“${ws.name}”？',
        description: '文件夹里的文件不受影响。',
        actions: [
          dialogButton('取消', () => Navigator.of(context).pop()),
          dialogButton(
            '从列表移除',
            _busy || inUse ? null : _submit,
            variant: .primary,
            key: const Key('remove-workspace-submit'),
          ),
        ],
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            ?inUseAlert,
            PathBox(ws.normalizedPath),
            if (_error != null) _ErrorText(_error!),
          ],
        ),
      );
    }
    return AppDialog(
      animation: widget.animation,
      title: '删除“${ws.name}”？',
      description: '这个文件夹及其中的所有内容（包括 .git 和未提交的改动）将被永久删除，无法恢复。',
      actions: [
        dialogButton('取消', () => Navigator.of(context).pop()),
        dialogButton(
          '删除',
          _busy || inUse || !_confirmed ? null : _submit,
          variant: .destructive,
          key: const Key('remove-workspace-submit'),
        ),
      ],
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          ?inUseAlert,
          PathBox(ws.normalizedPath),
          const SizedBox(height: 14),
          FTextField(
            key: const Key('remove-workspace-confirm-field'),
            control: .managed(
              controller: _confirm,
              onChange: (_) => setState(() {}),
            ),
            label: Text('输入“${ws.name}”以确认'),
            autofocus: true,
            autocorrect: false,
            onSubmit: (_) => _submit(),
          ),
          if (_error != null) _ErrorText(_error!),
        ],
      ),
    );
  }
}

// -----------------------------------------------------------------------------
// Clear session data
// -----------------------------------------------------------------------------

Future<void> showResetSessionDataDialog(
  BuildContext context,
  NullkeyStudioController c, {
  required WorkspaceRecord workspace,
  required HarnessKind harness,
  required PersistentStateRecord state,
}) => showFDialog<void>(
  context: context,
  builder: (context, style, animation) => AppDialog(
    animation: animation,
    title: '清除会话数据？',
    description:
        '${harness.displayName} 在“${workspace.name}”中的配置、对话和历史'
        '（${state.formattedSize}）将被删除，下次启动时从头开始。文件不受影响。',
    actions: [
      dialogButton('取消', () => Navigator.of(context).pop()),
      dialogButton(
        '清除',
        () {
          Navigator.of(context).pop();
          c.resetSessionData(state.id);
        },
        variant: .destructive,
        key: const Key('reset-session-data-submit'),
      ),
    ],
  ),
);

// -----------------------------------------------------------------------------
// Connect an account
// -----------------------------------------------------------------------------

String credentialNoun(String provider) =>
    provider == 'github' ? '个人访问令牌' : 'API Key';

Future<void> showConnectProviderDialog(
  BuildContext context,
  NullkeyStudioController c, {
  required String provider,
  bool reconnect = false,
}) => showFDialog<void>(
  context: context,
  builder: (context, style, animation) => _ConnectProviderDialog(
    controller: c,
    provider: provider,
    reconnect: reconnect,
    animation: animation,
  ),
);

class _ConnectProviderDialog extends StatefulWidget {
  final NullkeyStudioController controller;
  final String provider;
  final bool reconnect;
  final Animation<double> animation;

  const _ConnectProviderDialog({
    required this.controller,
    required this.provider,
    required this.reconnect,
    required this.animation,
  });

  @override
  State<_ConnectProviderDialog> createState() => _ConnectProviderDialogState();
}

class _ConnectProviderDialogState extends State<_ConnectProviderDialog> {
  final _secret = TextEditingController();
  String? _flowId;
  String? _error;
  bool _saving = false;

  NullkeyStudioController get c => widget.controller;

  @override
  void dispose() {
    _secret.dispose();
    super.dispose();
  }

  Future<void> _startBrowser() async {
    setState(() => _error = null);
    final flow = await c.startOAuth(widget.provider);
    if (mounted && flow != null) {
      setState(() => _flowId = flow.flowId);
    }
  }

  Future<void> _saveKey() async {
    if (_saving) {
      return;
    }
    setState(() {
      _saving = true;
      _error = null;
    });
    final error = await c.addApiKey(
      provider: widget.provider,
      secret: _secret.text,
    );
    if (!mounted) {
      return;
    }
    setState(() {
      _saving = false;
      _error = error;
    });
  }

  @override
  Widget build(BuildContext context) {
    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        final account = providerAccountName(widget.provider);
        final connected = c.isProviderConnected(widget.provider);
        final flow = _flowId == null
            ? null
            : c.oauthFlows.where((f) => f.flowId == _flowId).firstOrNull;
        final noun = credentialNoun(widget.provider);
        final browser = c.capabilities.browserSignInProviders.contains(
          widget.provider,
        );

        return AppDialog(
          animation: widget.animation,
          title: '${widget.reconnect ? '重新连接' : '连接'}$account',
          description: '凭证不会进入沙箱。',
          actions: [
            dialogButton(
              connected ? '完成' : '取消',
              () => Navigator.of(context).pop(),
              variant: connected ? .primary : .outline,
              key: const Key('connect-provider-close'),
            ),
          ],
          child: connected
              ? InlineAlert(
                  tone: Tone.success,
                  title: '已连接',
                  body: c.credentialFor(widget.provider)?.accountHint,
                )
              : Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    if (browser) ...[
                      BrowserSignInStatus(
                        flow: flow,
                        account: account,
                        onStart: _startBrowser,
                        onCancel: () => c.cancelOAuth(_flowId!),
                      ),
                      const SizedBox(height: 18),
                      Row(
                        children: [
                          const Expanded(child: Hairline()),
                          Padding(
                            padding: const EdgeInsets.symmetric(horizontal: 10),
                            child: Text(
                              '或使用 $noun',
                              style: context.secondaryStyle,
                            ),
                          ),
                          const Expanded(child: Hairline()),
                        ],
                      ),
                      const SizedBox(height: 14),
                    ],
                    FTextField(
                      key: const Key('connect-provider-secret-field'),
                      control: .managed(controller: _secret),
                      label: Text(noun),
                      hint: widget.provider == 'github'
                          ? 'github_pat_…'
                          : 'sk-…',
                      obscureText: true,
                      autocorrect: false,
                      enableSuggestions: false,
                      onSubmit: (_) => _saveKey(),
                    ),
                    const SizedBox(height: 10),
                    Align(
                      alignment: Alignment.centerLeft,
                      child: SmallButton(
                        key: const Key('connect-provider-save-key'),
                        label: '保存',
                        onPress: _saving ? null : _saveKey,
                      ),
                    ),
                    if (_error != null) _ErrorText(_error!),
                  ],
                ),
        );
      },
    );
  }
}

/// Browser sign-in with its waiting, cancelled and failed states.
class BrowserSignInStatus extends StatelessWidget {
  final OAuthPendingFlow? flow;
  final String account;
  final VoidCallback onStart;
  final VoidCallback onCancel;
  final FButtonVariant startVariant;

  const BrowserSignInStatus({
    required this.flow,
    required this.account,
    required this.onStart,
    required this.onCancel,
    this.startVariant = .primary,
    super.key,
  });

  @override
  Widget build(BuildContext context) {
    final f = flow;
    final status = f?.status;
    if (status == OAuthFlowStatus.authorizing ||
        status == OAuthFlowStatus.cancelling) {
      final cancelling = status == OAuthFlowStatus.cancelling;
      return Semantics(
        liveRegion: true,
        child: Row(
          children: [
            const Spinner(),
            const SizedBox(width: 10),
            Expanded(
              child: Text(
                cancelling ? '正在取消…' : '请在浏览器中完成登录…',
                style: context.bodyStyle,
              ),
            ),
            if (!cancelling)
              SmallButton(
                key: const Key('oauth-cancel-button'),
                label: '取消',
                onPress: onCancel,
              ),
          ],
        ),
      );
    }
    final retry =
        status == OAuthFlowStatus.cancelled || status == OAuthFlowStatus.failed;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        if (retry) ...[
          Semantics(
            liveRegion: true,
            child: Text(
              status == OAuthFlowStatus.cancelled
                  ? '已取消登录'
                  : '登录失败：${f?.errorMessage ?? '请重试'}',
              style: context.bodyStyle.copyWith(
                color: status == OAuthFlowStatus.failed
                    ? context.colors.destructive
                    : context.colors.mutedForeground,
              ),
            ),
          ),
          const SizedBox(height: 8),
        ],
        FButton(
          key: const Key('oauth-start-button'),
          variant: startVariant,
          size: .sm,
          mainAxisSize: MainAxisSize.min,
          prefix: const Icon(FLucideIcons.logIn, size: 14),
          onPress: onStart,
          child: Text(retry ? '重新登录' : '用$account登录'),
        ),
      ],
    );
  }
}
