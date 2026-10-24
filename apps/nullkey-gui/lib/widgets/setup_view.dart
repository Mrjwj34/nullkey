import 'package:flutter/widgets.dart';
import 'package:forui/forui.dart';

import '../models/nullkey_models.dart';
import '../state/nullkey_studio_controller.dart';
import 'common.dart';
import 'dialogs.dart';

/// First-run checklist: connect Codex, install it, choose a workspace, start.
/// GitHub and MCP are connected later, from management, when needed.
class SetupView extends StatefulWidget {
  final NullkeyStudioController controller;

  const SetupView({required this.controller, super.key});

  @override
  State<SetupView> createState() => _SetupViewState();
}

class _SetupViewState extends State<SetupView> {
  String? _chosenWorkspaceId;

  NullkeyStudioController get c => widget.controller;

  @override
  Widget build(BuildContext context) {
    final workspaces = c.workspaces;
    final chosen =
        workspaces.where((w) => w.id == _chosenWorkspaceId).firstOrNull ??
        c.selectedWorkspace ??
        workspaces.firstOrNull;
    final ready = c.codexConnected && c.codexInstalled && chosen != null;
    // Only the first unfinished step gets a primary button, so the eye lands
    // on what to do next. Later steps stay usable (installing can start
    // while signing in).
    final current = !c.codexConnected
        ? 1
        : !c.codexInstalled
        ? 2
        : 3;

    return SingleChildScrollView(
      key: const Key('setup-view'),
      padding: EdgeInsets.fromLTRB(40, titlebarInset(context) + 36, 40, 40),
      child: Center(
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 560),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Semantics(
                header: true,
                child: Text(
                  '开始使用 Nullkey',
                  style: context.typo.xl.copyWith(
                    fontWeight: FontWeight.w600,
                    color: context.colors.foreground,
                  ),
                ),
              ),
              const SizedBox(height: 4),
              Text(
                '在隔离的沙箱中运行编码 Agent。',
                style: context.bodyStyle.copyWith(
                  color: context.colors.mutedForeground,
                ),
              ),
              const SizedBox(height: 20),
              GroupedList(
                children: [
                  _ConnectStep(controller: c),
                  _InstallStep(controller: c, isCurrent: current == 2),
                  _WorkspaceStep(
                    controller: c,
                    isCurrent: current == 3,
                    chosen: chosen,
                    onChoose: (id) => setState(() => _chosenWorkspaceId = id),
                  ),
                ],
              ),
              const SizedBox(height: 16),
              Row(
                children: [
                  FButton(
                    key: const Key('setup-launch-button'),
                    mainAxisSize: MainAxisSize.min,
                    prefix: const Icon(FLucideIcons.play, size: 14),
                    onPress: ready
                        ? () => c.launch(workspaceId: chosen.id)
                        : null,
                    child: const Text('启动 Codex'),
                  ),
                  if (ready) ...[
                    const SizedBox(width: 12),
                    Text(
                      '网络：${NetworkMode.filtered.label}',
                      style: context.secondaryStyle,
                    ),
                  ],
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }
}

/// One checklist row: state, title, a short status line, and the step's
/// actions on the trailing edge.
class _Step extends StatelessWidget {
  final int number;
  final bool done;
  final String title;
  final Widget? status;
  final List<Widget> actions;
  final Widget? below;
  final Key? stepKey;

  const _Step({
    required this.number,
    required this.done,
    required this.title,
    this.status,
    this.actions = const [],
    this.below,
    this.stepKey,
  });

  @override
  Widget build(BuildContext context) {
    final colors = context.colors;
    return Semantics(
      key: stepKey,
      container: true,
      label: '第 $number 步，$title，${done ? '已完成' : '未完成'}',
      child: Padding(
        padding: const EdgeInsets.fromLTRB(14, 12, 12, 12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Row(
              children: [
                Container(
                  width: 20,
                  height: 20,
                  alignment: Alignment.center,
                  decoration: BoxDecoration(
                    shape: BoxShape.circle,
                    color: done ? context.appColors.success : null,
                    border: done
                        ? null
                        : Border.all(color: colors.border, width: 1.5),
                  ),
                  child: done
                      ? const Icon(
                          FLucideIcons.check,
                          size: 12,
                          color: Color(0xFFFFFFFF),
                        )
                      : Text(
                          '$number',
                          style: context.secondaryStyle.copyWith(
                            fontWeight: FontWeight.w600,
                          ),
                        ),
                ),
                const SizedBox(width: 12),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        title,
                        style: context.bodyStyle.copyWith(
                          fontWeight: FontWeight.w500,
                        ),
                      ),
                      if (status != null)
                        DefaultTextStyle.merge(
                          style: context.secondaryStyle,
                          child: status!,
                        ),
                    ],
                  ),
                ),
                for (final action in actions) ...[
                  const SizedBox(width: 6),
                  action,
                ],
              ],
            ),
            if (below != null)
              Padding(
                padding: const EdgeInsets.only(left: 32, top: 10),
                child: below,
              ),
          ],
        ),
      ),
    );
  }
}

class _ConnectStep extends StatelessWidget {
  final NullkeyStudioController controller;

  const _ConnectStep({required this.controller});

  @override
  Widget build(BuildContext context) {
    final c = controller;
    const title = '连接 Codex';
    if (c.codexConnected) {
      return _Step(
        stepKey: const Key('setup-step-connect'),
        number: 1,
        done: true,
        title: title,
        status: Text(c.credentialFor('openai')!.accountHint),
      );
    }
    final flow = c.latestOAuthFlow('openai');
    final status = flow?.status;
    if (status == OAuthFlowStatus.authorizing ||
        status == OAuthFlowStatus.cancelling) {
      final cancelling = status == OAuthFlowStatus.cancelling;
      return _Step(
        stepKey: const Key('setup-step-connect'),
        number: 1,
        done: false,
        title: title,
        status: Semantics(
          liveRegion: true,
          child: Row(
            children: [
              const Spinner(size: 11),
              const SizedBox(width: 6),
              Text(cancelling ? '正在取消…' : '请在浏览器中完成登录…'),
            ],
          ),
        ),
        actions: [
          if (!cancelling)
            SmallButton(
              key: const Key('oauth-cancel-button'),
              label: '取消',
              onPress: () => c.cancelOAuth(flow!.flowId),
            ),
        ],
      );
    }

    final candidate = c.importableCredentials
        .where((x) => x.provider == 'openai' && x.valid && !x.alreadyImported)
        .firstOrNull;
    final expired = c.credentialFor('openai') != null;
    final browser = c.capabilities.browserSignInProviders.contains('openai');
    void signIn() => c.startOAuth('openai');
    void useKey() => showConnectProviderDialog(context, c, provider: 'openai');

    final String? line = switch (status) {
      OAuthFlowStatus.cancelled => '已取消登录',
      OAuthFlowStatus.failed => '登录失败：${flow?.errorMessage ?? '请重试'}',
      _ when expired => 'ChatGPT 登录已失效',
      _ when candidate != null =>
        candidate.accountHint.isEmpty
            ? '在 ${candidate.sourcePathHint} 找到 Codex 登录'
            : '在 ${candidate.sourcePathHint} 找到 ${candidate.accountHint}',
      _ => null,
    };

    return _Step(
      stepKey: const Key('setup-step-connect'),
      number: 1,
      done: false,
      title: expired ? '重新连接 Codex' : title,
      status: line == null
          ? null
          : Semantics(liveRegion: true, child: Text(line)),
      actions: candidate != null
          ? [
              SmallButton(
                key: const Key('setup-import-codex'),
                variant: .primary,
                label: '导入',
                onPress: () => c.importHostCredentials([candidate.id]),
              ),
              PullDownMenu(
                groups: [
                  [
                    if (browser)
                      MenuAction(
                        key: const Key('oauth-start-button'),
                        label: '用 ChatGPT 账号登录',
                        icon: FLucideIcons.logIn,
                        onSelect: signIn,
                      ),
                    MenuAction(
                      key: const Key('setup-use-api-key'),
                      label: '使用 API Key…',
                      icon: FLucideIcons.keyRound,
                      onSelect: useKey,
                    ),
                  ],
                ],
                builder: (context, toggle) => SmallButton(
                  key: const Key('setup-other-sign-in'),
                  label: '其他方式',
                  onPress: toggle,
                ),
              ),
            ]
          : [
              if (browser)
                SmallButton(
                  key: const Key('oauth-start-button'),
                  variant: .primary,
                  icon: FLucideIcons.logIn,
                  label: '用 ChatGPT 登录',
                  onPress: signIn,
                ),
              SmallButton(
                key: const Key('setup-use-api-key'),
                variant: browser ? .outline : .primary,
                label: browser ? 'API Key…' : '使用 API Key…',
                onPress: useKey,
              ),
            ],
    );
  }
}

class _InstallStep extends StatelessWidget {
  final NullkeyStudioController controller;
  final bool isCurrent;

  const _InstallStep({required this.controller, required this.isCurrent});

  @override
  Widget build(BuildContext context) {
    final c = controller;
    const title = '安装 Codex';
    if (c.codexInstalled) {
      return _Step(
        stepKey: const Key('setup-step-install'),
        number: 2,
        done: true,
        title: title,
        status: Text(c.harness(HarnessKind.codex)?.version ?? '已安装'),
      );
    }
    final job = c.jobFor(HarnessKind.codex);
    final live =
        c.capabilities.connectionMode == DaemonConnectionMode.liveDaemon;
    SmallButton install(String label) => SmallButton(
      key: const Key('setup-install-button'),
      variant: isCurrent ? .primary : .outline,
      label: live ? '检查安装' : label,
      onPress: () => c.startHarnessInstall(HarnessKind.codex),
    );

    Widget step({
      Widget? status,
      List<Widget> actions = const [],
      Widget? below,
    }) => _Step(
      stepKey: const Key('setup-step-install'),
      number: 2,
      done: false,
      title: title,
      status: status == null
          ? null
          : Semantics(liveRegion: true, child: status),
      actions: actions,
      below:
          below ??
          (live
              ? const Text(
                  '请先在宿主机安装 Codex CLI，再检查安装。Nullkey 会从 nullkeyd 的 PATH 查找 codex。',
                )
              : null),
    );

    return switch (job?.status) {
      JobStatus.queued || JobStatus.running => step(
        status: Text('${job!.message} · ${(job.progress * 100).round()}%'),
        actions: [
          SmallButton(
            key: const Key('setup-install-cancel'),
            label: '取消',
            onPress: () => c.requestCancelJob(job.id),
          ),
        ],
        below: FDeterminateProgress(
          value: job.progress.clamp(0.0, 1.0),
          semanticsLabel: '安装进度',
        ),
      ),
      JobStatus.cancelling => step(
        status: const Row(
          key: Key('setup-install-cancelling'),
          children: [Spinner(size: 11), SizedBox(width: 6), Text('正在清理…')],
        ),
      ),
      JobStatus.cancelled => step(
        status: const Text('已取消', key: Key('setup-install-cancelled')),
        actions: [install('安装')],
      ),
      JobStatus.failed => step(
        status: Text(
          '安装失败：${job!.error ?? job.message}',
          style: TextStyle(color: context.colors.destructive),
        ),
        actions: [install('重试')],
      ),
      JobStatus.succeeded || null => step(actions: [install('安装')]),
    };
  }
}

class _WorkspaceStep extends StatelessWidget {
  final NullkeyStudioController controller;
  final bool isCurrent;
  final WorkspaceRecord? chosen;
  final ValueChanged<String> onChoose;

  const _WorkspaceStep({
    required this.controller,
    required this.isCurrent,
    required this.chosen,
    required this.onChoose,
  });

  @override
  Widget build(BuildContext context) {
    final c = controller;
    final workspaces = c.workspaces;
    final picked = chosen;
    final actions = [
      SmallButton(
        key: const Key('setup-new-workspace'),
        variant: picked == null && isCurrent ? .primary : .outline,
        label: '新建…',
        onPress: () =>
            showCreateWorkspaceDialog(context, c, openAfterCreate: false),
      ),
      SmallButton(
        key: const Key('setup-existing-workspace'),
        label: '选取文件夹…',
        onPress: () => showCreateWorkspaceDialog(
          context,
          c,
          initialKind: WorkspaceKind.external,
          openAfterCreate: false,
        ),
      ),
    ];
    return _Step(
      stepKey: const Key('setup-step-workspace'),
      number: 3,
      done: picked != null,
      title: '选择工作区',
      status: picked == null || workspaces.length > 1
          ? null
          : Text(picked.name),
      actions: actions,
      below: picked == null || workspaces.length < 2
          ? null
          : Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                for (final (i, ws) in workspaces.indexed) ...[
                  if (i > 0) const SizedBox(height: 8),
                  FRadio(
                    key: Key('setup-workspace-${ws.id}'),
                    label: Text(ws.name),
                    description: Text(abbreviateHome(ws.normalizedPath)),
                    value: ws.id == picked.id,
                    onChange: (_) => onChoose(ws.id),
                  ),
                ],
              ],
            ),
    );
  }
}
