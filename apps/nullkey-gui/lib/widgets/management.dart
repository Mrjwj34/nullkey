import 'package:flutter/widgets.dart';
import 'package:forui/forui.dart';

import '../models/nullkey_models.dart';
import '../state/nullkey_studio_controller.dart';
import 'common.dart';
import 'dialogs.dart';

enum ManagementSection {
  credentials('凭证'),
  mcp('MCP'),
  activity('活动');

  final String label;
  const ManagementSection(this.label);
}

/// Credentials, MCP servers and the activity log share one window: they
/// configure every workspace rather than being places users work in.
Future<void> showManagementDialog(
  BuildContext context,
  NullkeyStudioController c, {
  ManagementSection initial = ManagementSection.credentials,
}) => showFDialog<void>(
  context: context,
  builder: (context, style, animation) =>
      _ManagementDialog(controller: c, initial: initial, animation: animation),
);

class _ManagementDialog extends StatefulWidget {
  final NullkeyStudioController controller;
  final ManagementSection initial;
  final Animation<double> animation;

  const _ManagementDialog({
    required this.controller,
    required this.initial,
    required this.animation,
  });

  @override
  State<_ManagementDialog> createState() => _ManagementDialogState();
}

class _ManagementDialogState extends State<_ManagementDialog> {
  late ManagementSection _section = widget.initial;

  @override
  Widget build(BuildContext context) {
    final c = widget.controller;
    final size = MediaQuery.sizeOf(context);
    return FDialog(
      animation: widget.animation,
      semanticsLabel: '管理',
      constraints: const BoxConstraints(minWidth: 720, maxWidth: 720),
      builder: (context, style) => SizedBox(
        height: (size.height - 120).clamp(360.0, 600.0),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(20, 14, 12, 12),
              child: Row(
                children: [
                  Text(
                    '管理',
                    style: context.typo.md.copyWith(
                      fontWeight: FontWeight.w600,
                      color: context.colors.foreground,
                    ),
                  ),
                  const SizedBox(width: 16),
                  SegmentedControl<ManagementSection>(
                    semanticsLabel: '管理内容',
                    selected: _section,
                    onChanged: (s) => setState(() => _section = s),
                    segments: [
                      for (final s in ManagementSection.values)
                        Segment(
                          key: Key('management-tab-${s.name}'),
                          value: s,
                          label: s.label,
                        ),
                    ],
                  ),
                  const Spacer(),
                  ToolButton(
                    key: const Key('management-close'),
                    icon: FLucideIcons.x,
                    tooltip: '关闭',
                    onPress: () => Navigator.of(context).pop(),
                  ),
                ],
              ),
            ),
            const Hairline(),
            Expanded(
              child: ListenableBuilder(
                listenable: c,
                builder: (context, _) => switch (_section) {
                  ManagementSection.credentials => _CredentialsSection(
                    controller: c,
                  ),
                  ManagementSection.mcp => _McpSection(controller: c),
                  ManagementSection.activity => _ActivitySection(controller: c),
                },
              ),
            ),
          ],
        ),
      ),
    );
  }
}

({String label, Color color}) credentialStatus(
  BuildContext context,
  CredentialStatus s,
) => switch (s) {
  CredentialStatus.active => (label: '有效', color: context.appColors.success),
  CredentialStatus.expired => (label: '已过期', color: context.appColors.warning),
  CredentialStatus.refreshFailed => (
    label: '刷新失败',
    color: context.appColors.warning,
  ),
  CredentialStatus.revoked => (
    label: '已撤销',
    color: context.colors.mutedForeground,
  ),
};

class _CredentialsSection extends StatefulWidget {
  final NullkeyStudioController controller;

  const _CredentialsSection({required this.controller});

  @override
  State<_CredentialsSection> createState() => _CredentialsSectionState();
}

class _CredentialsSectionState extends State<_CredentialsSection> {
  bool _showImport = false;
  final Set<String> _picked = {};
  String? _importSummary;

  NullkeyStudioController get c => widget.controller;

  Future<void> _import() async {
    final result = await c.importHostCredentials(_picked.toList());
    if (!mounted || result == null) {
      return;
    }
    setState(() {
      _picked.clear();
      _importSummary = [
        if (result.imported.isNotEmpty) '已导入 ${result.imported.length} 项。',
        for (final s in result.skipped) '跳过 1 项：${s.reason}',
      ].join(' ');
    });
  }

  @override
  Widget build(BuildContext context) {
    final creds = c.credentials;
    final candidates = c.importableCredentials;
    return ListView(
      padding: const EdgeInsets.all(20),
      children: [
        if (creds.isEmpty)
          Text('没有凭证', style: context.secondaryStyle)
        else
          GroupedList(
            children: [
              for (final cred in creds)
                _CredentialRow(controller: c, credential: cred),
            ],
          ),
        const SizedBox(height: 12),
        Wrap(
          spacing: 8,
          runSpacing: 8,
          children: [
            SmallButton(
              key: const Key('credentials-add'),
              icon: FLucideIcons.plus,
              label: '添加 API Key…',
              onPress: () => showAddCredentialDialog(context, c),
            ),
            SmallButton(
              key: const Key('credentials-import'),
              icon: FLucideIcons.download,
              label: _showImport ? '收起' : '从本机导入…',
              onPress: () => setState(() => _showImport = !_showImport),
            ),
          ],
        ),
        if (_showImport) ...[
          const SizedBox(height: 16),
          Text('在这台 Mac 上找到的登录', style: context.strongStyle),
          const SizedBox(height: 8),
          GroupedList(
            children: [
              for (final cand in candidates)
                Padding(
                  padding: const EdgeInsets.fromLTRB(14, 10, 14, 10),
                  child: FCheckbox(
                    key: Key('import-candidate-${cand.id}'),
                    label: Text(
                      '${providerAccountName(cand.provider)} · ${cand.sourcePathHint}',
                    ),
                    description: Text(
                      [
                        if (cand.accountHint.isNotEmpty) cand.accountHint,
                        if (!cand.valid) '已失效',
                        if (cand.alreadyImported) '已导入',
                      ].join(' · '),
                    ),
                    enabled: cand.valid && !cand.alreadyImported,
                    value: _picked.contains(cand.id),
                    onChange: (v) => setState(() {
                      if (v) {
                        _picked.add(cand.id);
                      } else {
                        _picked.remove(cand.id);
                      }
                    }),
                  ),
                ),
            ],
          ),
          const SizedBox(height: 10),
          Row(
            children: [
              SmallButton(
                key: const Key('import-selected'),
                variant: .primary,
                label: '导入（${_picked.length}）',
                onPress: _picked.isEmpty ? null : _import,
              ),
              if (_importSummary != null) ...[
                const SizedBox(width: 12),
                Expanded(
                  child: Text(_importSummary!, style: context.secondaryStyle),
                ),
              ],
            ],
          ),
        ],
      ],
    );
  }
}

class _CredentialRow extends StatelessWidget {
  final NullkeyStudioController controller;
  final CredentialMetadata credential;

  const _CredentialRow({required this.controller, required this.credential});

  @override
  Widget build(BuildContext context) {
    final cred = credential;
    final status = credentialStatus(context, cred.status);
    final expires = cred.expiresAt;
    return Padding(
      key: Key('credential-${cred.id}'),
      padding: const EdgeInsets.fromLTRB(14, 10, 10, 10),
      child: Row(
        children: [
          Icon(
            cred.kind == CredentialKind.oauth
                ? FLucideIcons.userRound
                : FLucideIcons.keyRound,
            size: 16,
            color: context.colors.mutedForeground,
          ),
          const SizedBox(width: 12),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  cred.name,
                  style: context.bodyStyle.copyWith(
                    fontWeight: FontWeight.w500,
                  ),
                ),
                Text(
                  '${cred.accountHint} · ${cred.kind.label}',
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: context.secondaryStyle,
                ),
              ],
            ),
          ),
          const SizedBox(width: 12),
          Column(
            crossAxisAlignment: CrossAxisAlignment.end,
            children: [
              Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  StatusDot(color: status.color, size: 7),
                  const SizedBox(width: 6),
                  Text(status.label, style: context.secondaryStyle),
                ],
              ),
              if (expires != null && cred.status == CredentialStatus.active)
                Text(
                  '${_untilLabel(expires)}过期',
                  style: context.secondaryStyle,
                ),
            ],
          ),
          const SizedBox(width: 10),
          SmallButton(
            key: Key('credential-revoke-${cred.id}'),
            variant: .ghost,
            label: '撤销',
            onPress: cred.status == CredentialStatus.revoked
                ? null
                : () => controller.revokeCredential(cred.id),
          ),
        ],
      ),
    );
  }

  static String _untilLabel(DateTime at) {
    final d = at.difference(DateTime.now().toUtc());
    if (d.inHours < 1) {
      return '${d.inMinutes.clamp(0, 59)} 分钟后';
    }
    if (d.inHours < 48) {
      return '${d.inHours} 小时后';
    }
    return '${d.inDays} 天后';
  }
}

class _McpSection extends StatelessWidget {
  final NullkeyStudioController controller;

  const _McpSection({required this.controller});

  @override
  Widget build(BuildContext context) {
    final servers = controller.mcpServers;
    return ListView(
      padding: const EdgeInsets.all(20),
      children: [
        if (servers.isEmpty)
          Text('没有 MCP 服务', style: context.secondaryStyle)
        else
          GroupedList(
            children: [
              for (final s in servers)
                Padding(
                  key: Key('mcp-${s.id}'),
                  padding: const EdgeInsets.fromLTRB(14, 10, 14, 10),
                  child: Row(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Padding(
                        padding: const EdgeInsets.only(top: 2),
                        child: Icon(
                          FLucideIcons.plug,
                          size: 16,
                          color: context.colors.mutedForeground,
                        ),
                      ),
                      const SizedBox(width: 12),
                      Expanded(
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Text(
                              s.name,
                              style: context.bodyStyle.copyWith(
                                fontWeight: FontWeight.w500,
                              ),
                            ),
                            Text(
                              '${s.description} · ${s.toolsCount} 个工具',
                              style: context.secondaryStyle,
                            ),
                          ],
                        ),
                      ),
                      Row(
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          StatusDot(
                            color: s.health == HealthState.healthy
                                ? context.appColors.success
                                : context.appColors.warning,
                            size: 7,
                          ),
                          const SizedBox(width: 6),
                          Text(
                            s.health == HealthState.healthy
                                ? '正常'
                                : s.health.label,
                            style: context.secondaryStyle,
                          ),
                        ],
                      ),
                    ],
                  ),
                ),
            ],
          ),
      ],
    );
  }
}

IconData _eventIcon(String type) => switch (type) {
  'network.blocked' => FLucideIcons.ban,
  'gateway.model' || 'gateway.git' => FLucideIcons.arrowUpRight,
  'mcp.call' => FLucideIcons.plug,
  'sandbox.failed' => FLucideIcons.triangleAlert,
  _ when type.startsWith('sandbox.') => FLucideIcons.box,
  _ when type.startsWith('credential.') => FLucideIcons.keyRound,
  _ when type.startsWith('secret.') => FLucideIcons.shieldCheck,
  _ when type.startsWith('workspace.') => FLucideIcons.folder,
  _ when type.startsWith('service.') => FLucideIcons.globe,
  _ when type.startsWith('oauth.') => FLucideIcons.userRound,
  _ when type.startsWith('job.') => FLucideIcons.download,
  _ when type.startsWith('state.') => FLucideIcons.database,
  _ => FLucideIcons.activity,
};

class _ActivitySection extends StatelessWidget {
  final NullkeyStudioController controller;

  const _ActivitySection({required this.controller});

  @override
  Widget build(BuildContext context) {
    final c = controller;
    final events = c.auditEvents;
    return ListView(
      padding: const EdgeInsets.all(20),
      children: [
        GroupedList(
          children: [
            for (final e in events)
              Padding(
                key: Key('activity-${e.id}'),
                padding: const EdgeInsets.fromLTRB(14, 9, 14, 9),
                child: Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    SizedBox(
                      width: 44,
                      child: Text(
                        clockTime(e.timestamp),
                        style: context.mono(
                          size: 11.5,
                          color: context.colors.mutedForeground,
                        ),
                      ),
                    ),
                    Padding(
                      padding: const EdgeInsets.only(top: 2),
                      child: Icon(
                        _eventIcon(e.type),
                        size: 14,
                        color: e.isNetworkBlock
                            ? context.appColors.warning
                            : context.colors.mutedForeground,
                      ),
                    ),
                    const SizedBox(width: 10),
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(e.message, style: context.bodyStyle),
                          if (e.host != null)
                            Text(
                              [
                                '${e.method ?? ''} ${e.host}${e.path ?? ''}'
                                    .trim(),
                                if (e.status != null) '${e.status}',
                                if (e.latencyMs != null) '${e.latencyMs} ms',
                              ].join(' · '),
                              style: context.mono(
                                size: 11.5,
                                color: context.colors.mutedForeground,
                              ),
                            ),
                          if (e.sandboxId != null)
                            Text(
                              c.api.getSandbox(e.sandboxId!)?.name ??
                                  e.sandboxId!,
                              style: context.secondaryStyle,
                            ),
                        ],
                      ),
                    ),
                    if (e.isNetworkBlock)
                      Text(
                        '已拦截',
                        style: context.secondaryStyle.copyWith(
                          color: context.appColors.warning,
                          fontWeight: FontWeight.w600,
                        ),
                      ),
                  ],
                ),
              ),
          ],
        ),
      ],
    );
  }
}

// -----------------------------------------------------------------------------
// Add an API key
// -----------------------------------------------------------------------------

const _keyProviderInfo = {
  'openai': ('OpenAI', 'Codex、OpenCode'),
  'anthropic': ('Anthropic', 'Claude Code、OpenCode'),
  'google': ('Google', 'Gemini CLI'),
  'github': ('GitHub', '仓库和 Pull Request'),
};

Future<void> showAddCredentialDialog(
  BuildContext context,
  NullkeyStudioController c,
) => showFDialog<void>(
  context: context,
  builder: (context, style, animation) =>
      _AddCredentialDialog(controller: c, animation: animation),
);

class _AddCredentialDialog extends StatefulWidget {
  final NullkeyStudioController controller;
  final Animation<double> animation;

  const _AddCredentialDialog({
    required this.controller,
    required this.animation,
  });

  @override
  State<_AddCredentialDialog> createState() => _AddCredentialDialogState();
}

class _AddCredentialDialogState extends State<_AddCredentialDialog> {
  String _provider = 'openai';
  final _secret = TextEditingController();
  String? _error;
  bool _busy = false;

  @override
  void dispose() {
    _secret.dispose();
    super.dispose();
  }

  Future<void> _save() async {
    if (_busy) {
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
    });
    final error = await widget.controller.addApiKey(
      provider: _provider,
      secret: _secret.text,
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
  Widget build(BuildContext context) => AppDialog(
    animation: widget.animation,
    title: '添加 API Key',
    description: '保存在钥匙串中，不会进入沙箱。',
    width: 420,
    actions: [
      dialogButton('取消', () => Navigator.of(context).pop()),
      dialogButton(
        '保存',
        _busy ? null : _save,
        variant: .primary,
        key: const Key('add-credential-submit'),
      ),
    ],
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        for (final id in widget.controller.capabilities.apiKeyProviders)
          if (_keyProviderInfo[id] case (final name, final usedBy)) ...[
            FRadio(
              key: Key('add-credential-provider-$id'),
              label: Text(name),
              description: Text(usedBy),
              value: _provider == id,
              onChange: (_) => setState(() => _provider = id),
            ),
            const SizedBox(height: 8),
          ],
        const SizedBox(height: 6),
        FTextField(
          key: const Key('add-credential-secret-field'),
          control: .managed(controller: _secret),
          label: Text(credentialNoun(_provider)),
          obscureText: true,
          autocorrect: false,
          enableSuggestions: false,
          onSubmit: (_) => _save(),
        ),
        if (_error != null)
          Padding(
            padding: const EdgeInsets.only(top: 10),
            child: Text(
              _error!,
              style: context.bodyStyle.copyWith(
                color: context.colors.destructive,
              ),
            ),
          ),
      ],
    ),
  );
}

// -----------------------------------------------------------------------------
// Keyboard shortcuts
// -----------------------------------------------------------------------------

const keyboardShortcuts = [
  ('转到工作区列表', ['⌘', '⇧', 'M']),
  ('选择工作区', ['↑', '↓']),
  ('打开工作区', ['Return']),
  ('添加工作区', ['⌘', 'N']),
  ('切换标签页', ['⌘', '1–4']),
  ('锁定', ['⌘', 'L']),
  ('键盘快捷键', ['⌘', '/']),
];

Future<void> showShortcutsDialog(BuildContext context) => showFDialog<void>(
  context: context,
  builder: (context, style, animation) => AppDialog(
    animation: animation,
    title: '键盘快捷键',
    width: 460,
    actions: [
      dialogButton('完成', () => Navigator.of(context).pop(), variant: .primary),
    ],
    child: GroupedList(
      children: [
        for (final (label, keys) in keyboardShortcuts)
          Padding(
            padding: const EdgeInsets.fromLTRB(14, 9, 12, 9),
            child: Row(
              children: [
                Expanded(child: Text(label, style: context.bodyStyle)),
                for (final k in keys) ...[const SizedBox(width: 4), KeyCap(k)],
              ],
            ),
          ),
      ],
    ),
  ),
);

// -----------------------------------------------------------------------------
// Developer scenarios (Core Mock)
// -----------------------------------------------------------------------------

Future<void> showDeveloperScenariosDialog(
  BuildContext context,
  NullkeyStudioController c,
) => showFDialog<void>(
  context: context,
  builder: (context, style, animation) =>
      _DeveloperScenariosDialog(controller: c, animation: animation),
);

class _DeveloperScenariosDialog extends StatefulWidget {
  final NullkeyStudioController controller;
  final Animation<double> animation;

  const _DeveloperScenariosDialog({
    required this.controller,
    required this.animation,
  });

  @override
  State<_DeveloperScenariosDialog> createState() =>
      _DeveloperScenariosDialogState();
}

class _DeveloperScenariosDialogState extends State<_DeveloperScenariosDialog> {
  NullkeyStudioController get c => widget.controller;

  @override
  Widget build(BuildContext context) {
    final running = c.selectedSandbox?.status == SandboxStatus.running;

    Widget scenario(String key, String title, VoidCallback? run) => Padding(
      padding: const EdgeInsets.fromLTRB(14, 7, 10, 7),
      child: Row(
        children: [
          Expanded(child: Text(title, style: context.bodyStyle)),
          const SizedBox(width: 10),
          SmallButton(
            key: Key('scenario-$key'),
            label: '运行',
            onPress: run == null
                ? null
                : () {
                    Navigator.of(context).pop();
                    run();
                  },
          ),
        ],
      ),
    );

    Widget toggle(
      String key,
      String title,
      bool value,
      ValueChanged<bool> set,
    ) => Padding(
      padding: const EdgeInsets.fromLTRB(14, 10, 14, 10),
      child: FSwitch(
        key: Key('scenario-$key'),
        label: Text(title),
        value: value,
        onChange: (v) => setState(() => set(v)),
      ),
    );

    Widget group(String title, List<Widget> rows) => Padding(
      padding: const EdgeInsets.only(bottom: 16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Padding(
            padding: const EdgeInsets.only(bottom: 6),
            child: Text(title, style: context.strongStyle),
          ),
          GroupedList(children: rows),
        ],
      ),
    );

    return AppDialog(
      animation: widget.animation,
      title: '开发者场景',
      description: '数据来自内置模拟引擎，不会触及真实文件和账号。部分场景需要先启动沙箱。',
      width: 560,
      actions: [
        dialogButton(
          '完成',
          () => Navigator.of(context).pop(),
          variant: .primary,
        ),
      ],
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          group('终端', [
            scenario(
              'reconnect',
              '暂时断线后重新连接',
              running ? c.drillTransientTerminalReconnect : null,
            ),
            scenario(
              'replaced',
              '终端在另一个窗口打开',
              running ? c.drillTerminalReplacedByAnotherClient : null,
            ),
            scenario(
              'waiting-ready',
              '等待终端就绪',
              running
                  ? () => c.attachTerminalToSelectedSandbox(
                      holdAtWaitingForReadyFrame: true,
                    )
                  : null,
            ),
            scenario(
              'daemon-restart',
              'nullkeyd 重新启动',
              c.drillDaemonRestartInterruption,
            ),
          ]),
          group('网络和占用', [
            scenario(
              'network-denied',
              '拒绝一次出网请求',
              running ? c.drillOutboundNetworkDenied : null,
            ),
            scenario('state-locked', '会话数据被占用', c.drillStateLockedConflict),
          ]),
          group('凭证', [
            scenario(
              'credential-expired',
              'ChatGPT 账号失效',
              c.drillCredentialExpired,
            ),
            scenario('late-callback', '取消登录后浏览器才回调', c.drillLateOAuthCallback),
            scenario('scan-fails', '下次凭证检查失败', c.drillNextScanFails),
          ]),
          group('开关', [
            toggle(
              'passkey-cancel',
              '下次系统确认被取消',
              c.passkey.simulateNextCancellation,
              (v) => c.passkey.simulateNextCancellation = v,
            ),
            if (c.mock case final mock?) ...[
              toggle(
                'oauth-race',
                '取消登录时浏览器刚好已完成',
                mock.simulateOAuthCompleteBeforeCancel,
                (v) => mock.simulateOAuthCompleteBeforeCancel = v,
              ),
              toggle(
                'job-race',
                '取消安装时刚好已装完',
                mock.simulateJobCompleteBeforeCancel,
                (v) => mock.simulateJobCompleteBeforeCancel = v,
              ),
            ],
            toggle(
              'desktop-available',
              '这台 Mac 支持虚拟桌面',
              c.capabilities.desktopAvailable,
              c.toggleDesktopCapability,
            ),
          ]),
          group('数据', [scenario('first-run', '回到首次使用', c.resetToFirstRun)]),
        ],
      ),
    );
  }
}
