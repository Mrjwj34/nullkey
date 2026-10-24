import 'package:flutter/widgets.dart';
import 'package:forui/forui.dart';

import '../models/nullkey_models.dart';
import '../state/nullkey_studio_controller.dart';
import 'common.dart';
import 'dialogs.dart';
import 'session_panes.dart';
import 'terminal_pane.dart';

/// The selected workspace: what runs in it, and its terminal, artifacts,
/// services and desktop.
class WorkspaceView extends StatelessWidget {
  final NullkeyStudioController controller;
  final FocusNode terminalFocus;

  const WorkspaceView({
    required this.controller,
    required this.terminalFocus,
    super.key,
  });

  @override
  Widget build(BuildContext context) {
    final c = controller;
    final ws = c.selectedWorkspace!;
    final sbx = c.selectedSandbox;
    final launch = c.launchProgress;
    final alerts = _alerts(context, c, sbx);
    // The stopped, interrupted and first-launch panels carry their own start
    // button; showing a second one in the toolbar would compete with it.
    final bodyOffersStart =
        launch == null &&
        (sbx == null ||
            (c.activeTab == SessionTab.terminal &&
                !sbx.status.isRunningOrStarting));

    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _SessionHeader(
          controller: c,
          workspace: ws,
          sandbox: sbx,
          showTabs: sbx != null && launch == null,
          showStart: !bodyOffersStart,
        ),
        const Hairline(),
        if (alerts.isNotEmpty)
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 10, 16, 10),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                for (final (i, alert) in alerts.indexed) ...[
                  if (i > 0) const SizedBox(height: 8),
                  alert,
                ],
              ],
            ),
          ),
        Expanded(child: _body(context, c, ws, sbx, launch)),
      ],
    );
  }

  Widget _body(
    BuildContext context,
    NullkeyStudioController c,
    WorkspaceRecord ws,
    SandboxRecord? sbx,
    LaunchProgress? launch,
  ) {
    if (launch != null) {
      return LaunchProgressView(progress: launch);
    }
    if (sbx == null) {
      return _FirstLaunchPanel(controller: c, workspace: ws);
    }
    final active = sbx.status.isRunningOrStarting;
    const notRunning = EmptyState(icon: FLucideIcons.square, title: '沙箱未运行');
    switch (c.activeTab) {
      case SessionTab.terminal:
        if (active) {
          return TerminalPane(controller: c, inputFocus: terminalFocus);
        }
        if (isInterrupted(sbx)) {
          return EmptyState(
            key: const Key('daemon-interrupted-panel'),
            icon: FLucideIcons.triangleAlert,
            title: '沙箱已中断',
            body: 'Nullkey 重启时命令未完成。文件和会话已保留。',
            actions: [
              FButton(
                key: const Key('daemon-restart-relaunch-button'),
                size: .sm,
                mainAxisSize: MainAxisSize.min,
                prefix: const Icon(FLucideIcons.rotateCcw, size: 14),
                onPress: c.restartInterruptedSandboxWithSameConfig,
                child: const Text('以相同设置重新启动'),
              ),
            ],
          );
        }
        final failed = sbx.status == SandboxStatus.failed;
        return EmptyState(
          key: Key(failed ? 'sandbox-failed-panel' : 'sandbox-stopped-panel'),
          icon: failed ? FLucideIcons.circleAlert : FLucideIcons.square,
          title: failed ? '${sbx.harness.displayName} 没能启动' : '沙箱已停止',
          body: '文件和会话已保留。',
          actions: [
            FButton(
              size: .sm,
              mainAxisSize: MainAxisSize.min,
              prefix: const Icon(FLucideIcons.play, size: 14),
              onPress: () => c.launch(workspaceId: ws.id),
              child: Text('启动 ${sbx.harness.displayName}'),
            ),
            SmallButton(
              label: '启动选项…',
              onPress: () => showLaunchOptionsDialog(context, c, ws),
            ),
          ],
        );
      case SessionTab.artifacts:
        return ArtifactsPane(key: ValueKey(sbx.id), controller: c);
      case SessionTab.services:
        return active ? ServicesPane(controller: c) : notRunning;
      case SessionTab.desktop:
        return active ? DesktopPane(controller: c) : notRunning;
    }
  }

  List<Widget> _alerts(
    BuildContext context,
    NullkeyStudioController c,
    SandboxRecord? sbx,
  ) => [
    if (c.missingProviderForSelected case final provider?)
      InlineAlert(
        key: const Key('reauth-alert'),
        tone: Tone.warning,
        icon: FLucideIcons.keyRound,
        title: '${providerAccountName(provider)}登录已失效',
        body: '重新连接后可继续，文件和会话不受影响。',
        actions: [
          FButton(
            key: const Key('reauth-reconnect-button'),
            size: .sm,
            mainAxisSize: MainAxisSize.min,
            onPress: () => showConnectProviderDialog(
              context,
              c,
              provider: provider,
              reconnect: true,
            ),
            child: const Text('重新连接…'),
          ),
        ],
      ),
    if (c.lastStateLockedConflict case final conflict?)
      InlineAlert(
        key: const Key('state-locked-conflict-alert'),
        tone: Tone.warning,
        icon: FLucideIcons.lock,
        title: '无法启动沙箱',
        body: switch (conflict.resource) {
          LockedResource.sessionData =>
            '会话数据正被“${conflict.lockingSandboxName}”使用。停止它后再启动。',
          LockedResource.workspace =>
            '工作区正被“${conflict.lockingSandboxName}”读写。停止它后再启动。',
        },
        onDismiss: c.dismissConflict,
        actions: [
          SmallButton(
            key: const Key('state-locked-jump-button'),
            label: '打开“${conflict.lockingSandboxName}”',
            onPress: () => c.jumpToLockingSandbox(conflict.lockingSandboxId),
          ),
        ],
      ),
    if (c.lastNetworkDeniedBlock case final block?)
      InlineAlert(
        key: const Key('network-denied-alert'),
        tone: Tone.warning,
        icon: FLucideIcons.ban,
        title: '已阻止访问 ${block.host ?? '未知地址'}',
        body:
            '网络：${(sbx?.networkMode ?? NetworkMode.servicesOnly).label}。'
            '如需访问，停止后在启动选项中更改网络。',
        onDismiss: c.dismissNetworkDenied,
      ),
    if (c.lastScan case final scan?) _ScanAlert(controller: c, scan: scan),
  ];
}

String _excludedLabel(String key) => switch (key) {
  'workspace' => '工作区文件',
  'user_imports' => '导入的配置',
  'user_input' => '手动输入',
  _ => key,
};

class _ScanAlert extends StatelessWidget {
  final NullkeyStudioController controller;
  final ScanOutcome scan;

  const _ScanAlert({required this.controller, required this.scan});

  @override
  Widget build(BuildContext context) {
    final c = controller;
    final result = scan.result;
    if (result == null) {
      return InlineAlert(
        key: const Key('secret-scan-error-alert'),
        tone: Tone.danger,
        title: '凭证检查未完成',
        body: '${scan.error ?? ''}这不代表没有发现凭证。',
        onDismiss: c.dismissScan,
        actions: [
          SmallButton(label: '重新检查', onPress: c.runSecretScanOnSelectedSandbox),
        ],
      );
    }
    final hits = result.checks.where((check) => !check.clean);
    return InlineAlert(
      key: const Key('secret-scan-result-alert'),
      tone: result.clean ? Tone.success : Tone.warning,
      icon: result.clean ? FLucideIcons.shieldCheck : FLucideIcons.shieldAlert,
      title: result.headline,
      body:
          '未检查${result.excluded.map(_excludedLabel).join('、')} · '
          '${clockTime(result.scannedAt)}',
      onDismiss: c.dismissScan,
      details: hits.isEmpty
          ? null
          : Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                for (final check in hits)
                  Text(
                    '${check.targetSummary}：${check.matches} 处',
                    style: context.secondaryStyle,
                  ),
              ],
            ),
    );
  }
}

/// Title and status in the title bar row, then tabs and actions in a
/// toolbar row below it.
class _SessionHeader extends StatelessWidget {
  final NullkeyStudioController controller;
  final WorkspaceRecord workspace;
  final SandboxRecord? sandbox;
  final bool showTabs;
  final bool showStart;

  const _SessionHeader({
    required this.controller,
    required this.workspace,
    required this.sandbox,
    required this.showTabs,
    required this.showStart,
  });

  @override
  Widget build(BuildContext context) {
    final c = controller;
    final ws = workspace;
    final sbx = sandbox;
    final colors = context.colors;
    final defaults = c.launchDefaults(ws);
    final harness = sbx?.harness ?? defaults.harness;
    final network = sbx?.networkMode ?? defaults.network;
    final mode = sbx?.workspaceMode ?? defaults.mode;
    final version = c.harness(harness)?.version;
    final status = sandboxStatus(context, c, ws.id);
    final inset = titlebarInset(context);
    final muted = context.secondaryStyle;
    const dot = Text(' · ');

    return Padding(
      padding: EdgeInsets.fromLTRB(20, inset > 0 ? 3 : 12, 12, 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          SizedBox(
            height: inset > 0 ? inset - 3 : null,
            child: DefaultTextStyle.merge(
              style: muted,
              child: Row(
                children: [
                  Flexible(
                    child: Semantics(
                      header: true,
                      child: Text(
                        ws.name,
                        key: const Key('session-header-workspace'),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: context.typo.md.copyWith(
                          fontWeight: FontWeight.w600,
                          color: colors.foreground,
                        ),
                      ),
                    ),
                  ),
                  const SizedBox(width: 10),
                  Semantics(
                    liveRegion: true,
                    label: '状态：${status.label}',
                    excludeSemantics: true,
                    child: Row(
                      key: const Key('session-header-status'),
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        StatusDot(color: status.color, size: 7),
                        const SizedBox(width: 5),
                        Text(status.label),
                      ],
                    ),
                  ),
                  dot,
                  Text(
                    version == null
                        ? harness.displayName
                        : '${harness.displayName} $version',
                    key: const Key('session-header-harness'),
                  ),
                  dot,
                  Text(network.label, key: const Key('session-header-network')),
                  if (ws.kind == WorkspaceKind.external) ...[
                    dot,
                    Text(mode == MountMode.rw ? '直接修改原文件' : '只读'),
                  ],
                ],
              ),
            ),
          ),
          SizedBox(height: inset > 0 ? 4 : 8),
          ConstrainedBox(
            constraints: const BoxConstraints(minHeight: 32),
            child: Row(
              children: [
                if (showTabs)
                  SegmentedControl<SessionTab>(
                    semanticsLabel: '会话视图',
                    selected: c.activeTab,
                    onChanged: c.setActiveTab,
                    segments: [
                      for (final tab in c.availableTabs)
                        Segment(
                          key: Key('session-tab-${tab.name}'),
                          value: tab,
                          label: tab.label,
                          count: switch (tab) {
                            SessionTab.artifacts =>
                              c.activeArtifacts.isEmpty
                                  ? null
                                  : '${c.activeArtifacts.length}',
                            SessionTab.services =>
                              c.activeServices.isEmpty
                                  ? null
                                  : '${c.activeServices.length}',
                            _ => null,
                          },
                        ),
                    ],
                  ),
                const Spacer(),
                ..._actions(context, c, ws, sbx, harness),
              ],
            ),
          ),
        ],
      ),
    );
  }

  List<Widget> _actions(
    BuildContext context,
    NullkeyStudioController c,
    WorkspaceRecord ws,
    SandboxRecord? sbx,
    HarnessKind harness,
  ) {
    final launching = c.isLaunching(ws.id);
    final running = sbx != null && sbx.status.isRunningOrStarting;
    final interrupted = sbx != null && isInterrupted(sbx);
    final sessionData = c.sessionDataFor(ws.id, harness);

    final primary = running || launching
        ? FButton(
            key: const Key('session-stop-button'),
            variant: .outline,
            size: .sm,
            mainAxisSize: MainAxisSize.min,
            prefix: const Icon(FLucideIcons.square, size: 13),
            onPress: running ? c.stopSelectedSandbox : null,
            child: const Text('停止'),
          )
        : FButton(
            key: const Key('session-start-button'),
            size: .sm,
            mainAxisSize: MainAxisSize.min,
            prefix: const Icon(FLucideIcons.play, size: 13),
            onPress: interrupted
                ? c.restartInterruptedSandboxWithSameConfig
                : () => c.launch(workspaceId: ws.id),
            child: Text('启动 ${harness.displayName}'),
          );

    return [
      if (running || launching || showStart) ...[
        primary,
        const SizedBox(width: 4),
      ],
      PullDownMenu(
        groups: [
          [
            MenuAction(
              key: const Key('menu-launch-options'),
              label: '启动选项…',
              icon: FLucideIcons.slidersHorizontal,
              onSelect: running || launching
                  ? null
                  : () => showLaunchOptionsDialog(context, c, ws),
            ),
            MenuAction(
              key: const Key('menu-secret-scan'),
              label: '检查凭证',
              icon: FLucideIcons.shieldCheck,
              onSelect: running ? c.runSecretScanOnSelectedSandbox : null,
            ),
            MenuAction(
              key: const Key('menu-detach-terminal'),
              label: '断开终端',
              icon: FLucideIcons.unplug,
              onSelect:
                  running && c.terminalPhase == TerminalConnectionPhase.ready
                  ? c.detachTerminal
                  : null,
            ),
          ],
          [
            MenuAction(
              key: const Key('menu-reset-session-data'),
              label: '清除会话数据…',
              icon: FLucideIcons.eraser,
              onSelect: sessionData == null || running || launching
                  ? null
                  : () => showResetSessionDataDialog(
                      context,
                      c,
                      workspace: ws,
                      harness: harness,
                      state: sessionData,
                    ),
            ),
            MenuAction(
              key: const Key('menu-remove-workspace'),
              label: ws.kind == WorkspaceKind.managed ? '删除工作区…' : '从列表移除…',
              icon: ws.kind == WorkspaceKind.managed
                  ? FLucideIcons.trash2
                  : FLucideIcons.listX,
              destructive: ws.kind == WorkspaceKind.managed,
              onSelect: () => showRemoveWorkspaceDialog(context, c, ws),
            ),
          ],
        ],
        builder: (context, toggle) => ToolButton(
          key: const Key('session-more-button'),
          icon: FLucideIcons.ellipsis,
          tooltip: '更多',
          onPress: toggle,
        ),
      ),
    ];
  }
}

/// Launch feedback: each stage the daemon reports, in order. Input is only
/// enabled after the last one.
class LaunchProgressView extends StatelessWidget {
  final LaunchProgress progress;

  const LaunchProgressView({required this.progress, super.key});

  @override
  Widget build(BuildContext context) {
    final colors = context.colors;
    final current = progress.stage.index;
    return Center(
      key: const Key('launch-progress'),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            '正在启动 ${progress.harness.displayName}',
            style: context.typo.md.copyWith(
              fontWeight: FontWeight.w600,
              color: colors.foreground,
            ),
          ),
          const SizedBox(height: 14),
          for (final stage in LaunchStage.values)
            Padding(
              padding: const EdgeInsets.only(bottom: 8),
              child: Semantics(
                liveRegion: stage.index == current,
                child: Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    SizedBox(
                      width: 16,
                      child: stage.index < current
                          ? Icon(
                              FLucideIcons.circleCheck,
                              size: 15,
                              color: context.appColors.success,
                            )
                          : stage.index == current
                          ? const Spinner()
                          : Icon(
                              FLucideIcons.circle,
                              size: 15,
                              color: colors.border,
                            ),
                    ),
                    const SizedBox(width: 10),
                    Text(
                      stage.label,
                      style: context.bodyStyle.copyWith(
                        color: stage.index > current
                            ? colors.mutedForeground
                            : colors.foreground,
                      ),
                    ),
                  ],
                ),
              ),
            ),
        ],
      ),
    );
  }
}

class _FirstLaunchPanel extends StatelessWidget {
  final NullkeyStudioController controller;
  final WorkspaceRecord workspace;

  const _FirstLaunchPanel({required this.controller, required this.workspace});

  @override
  Widget build(BuildContext context) {
    final c = controller;
    final ws = workspace;
    final d = c.launchDefaults(ws);
    final installed = c.isHarnessInstalled(d.harness);
    final name = d.harness.displayName;

    return EmptyState(
      key: const Key('first-launch-panel'),
      icon: FLucideIcons.box,
      title: '在沙箱中运行 $name',
      body: [
        d.network.label,
        abbreviateHome(ws.normalizedPath),
        if (ws.kind == WorkspaceKind.external)
          d.mode == MountMode.rw ? '直接修改原文件' : '只读',
      ].join(' · '),
      actions: [
        if (installed)
          FButton(
            key: const Key('first-launch-start-button'),
            size: .sm,
            mainAxisSize: MainAxisSize.min,
            prefix: const Icon(FLucideIcons.play, size: 13),
            onPress: () => c.launch(workspaceId: ws.id),
            child: Text('启动 $name'),
          )
        else
          FButton(
            size: .sm,
            mainAxisSize: MainAxisSize.min,
            onPress: c.openSetup,
            child: Text('安装 $name…'),
          ),
        SmallButton(
          label: '启动选项…',
          onPress: () => showLaunchOptionsDialog(context, c, ws),
        ),
      ],
    );
  }
}
