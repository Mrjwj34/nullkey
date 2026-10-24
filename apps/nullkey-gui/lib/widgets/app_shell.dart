import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';
import 'package:forui/forui.dart';

import '../state/nullkey_studio_controller.dart';
import 'common.dart';
import 'dialogs.dart';
import 'management.dart';
import 'setup_view.dart';
import 'sidebar.dart';
import 'workspace_view.dart';

class _FocusWorkspaceListIntent extends Intent {
  const _FocusWorkspaceListIntent();
}

class _LockIntent extends Intent {
  const _LockIntent();
}

class _AddWorkspaceIntent extends Intent {
  const _AddWorkspaceIntent();
}

class _ShowShortcutsIntent extends Intent {
  const _ShowShortcutsIntent();
}

class _SelectTabIntent extends Intent {
  final int index;
  const _SelectTabIntent(this.index);
}

const double sidebarWidth = 248;

/// Window layout: sidebar, the selected workspace or setup, and the message
/// bar. Owns the app-wide keyboard shortcuts.
class AppShell extends StatefulWidget {
  final NullkeyStudioController controller;

  const AppShell({required this.controller, super.key});

  @override
  State<AppShell> createState() => _AppShellState();
}

class _AppShellState extends State<AppShell> {
  final _sidebar = GlobalKey<SidebarState>();
  final _terminalFocus = FocusNode(debugLabel: 'terminal-input');
  bool _focusMovedHint = false;

  NullkeyStudioController get c => widget.controller;

  @override
  void dispose() {
    _terminalFocus.dispose();
    super.dispose();
  }

  void _focusWorkspaceList() {
    _sidebar.currentState?.focusList();
    setState(() => _focusMovedHint = true);
  }

  void _onListFocusChange(bool hasFocus) {
    if (!hasFocus && _focusMovedHint) {
      setState(() => _focusMovedHint = false);
    }
  }

  void _selectTab(int index) {
    final tabs = c.availableTabs;
    if (c.surface == AppSurface.workspace &&
        c.selectedSandbox != null &&
        index < tabs.length) {
      c.setActiveTab(tabs[index]);
    }
  }

  @override
  Widget build(BuildContext context) => ListenableBuilder(
    listenable: c,
    builder: (context, _) {
      if (!c.studioUnlocked) {
        return LockView(controller: c);
      }
      return Shortcuts(
        shortcuts: <ShortcutActivator, Intent>{
          const SingleActivator(
            LogicalKeyboardKey.keyM,
            meta: true,
            shift: true,
          ): const _FocusWorkspaceListIntent(),
          const SingleActivator(
            LogicalKeyboardKey.keyM,
            control: true,
            shift: true,
          ): const _FocusWorkspaceListIntent(),
          const SingleActivator(LogicalKeyboardKey.keyL, meta: true):
              const _LockIntent(),
          const SingleActivator(LogicalKeyboardKey.keyN, meta: true):
              const _AddWorkspaceIntent(),
          const SingleActivator(LogicalKeyboardKey.slash, meta: true):
              const _ShowShortcutsIntent(),
          for (final (i, key) in const [
            LogicalKeyboardKey.digit1,
            LogicalKeyboardKey.digit2,
            LogicalKeyboardKey.digit3,
            LogicalKeyboardKey.digit4,
          ].indexed)
            SingleActivator(key, meta: true): _SelectTabIntent(i),
        },
        child: Actions(
          actions: <Type, Action<Intent>>{
            _FocusWorkspaceListIntent:
                CallbackAction<_FocusWorkspaceListIntent>(
                  onInvoke: (_) => _focusWorkspaceList(),
                ),
            _LockIntent: CallbackAction<_LockIntent>(
              onInvoke: (_) => c.lockStudio(),
            ),
            _AddWorkspaceIntent: CallbackAction<_AddWorkspaceIntent>(
              onInvoke: (_) => showCreateWorkspaceDialog(context, c),
            ),
            _ShowShortcutsIntent: CallbackAction<_ShowShortcutsIntent>(
              onInvoke: (_) => showShortcutsDialog(context),
            ),
            _SelectTabIntent: CallbackAction<_SelectTabIntent>(
              onInvoke: (intent) => _selectTab(intent.index),
            ),
          },
          child: Focus(
            autofocus: true,
            child: ColoredBox(
              color: context.colors.background,
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  SizedBox(
                    width: sidebarWidth,
                    child: Sidebar(
                      key: _sidebar,
                      controller: c,
                      showFocusMovedHint: _focusMovedHint,
                      onListFocusChange: _onListFocusChange,
                      onAddWorkspace: () =>
                          showCreateWorkspaceDialog(context, c),
                      onOpenManagement: () => showManagementDialog(context, c),
                      onShowShortcuts: () => showShortcutsDialog(context),
                      onOpenDeveloperScenarios: () =>
                          showDeveloperScenariosDialog(context, c),
                    ),
                  ),
                  const Hairline(vertical: true),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.stretch,
                      children: [
                        Expanded(child: _main(context)),
                        if (c.connectionProblem case final problem?)
                          ConnectionProblemBar(problem: problem),
                        NoticeBar(controller: c),
                      ],
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
      );
    },
  );

  Widget _main(BuildContext context) {
    if (c.loading) {
      return Center(
        key: const Key('connecting-view'),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Spinner(),
            const SizedBox(width: 10),
            Text('正在连接 Nullkey…', style: context.secondaryStyle),
          ],
        ),
      );
    }
    if (c.surface == AppSurface.setup) {
      return SetupView(controller: c);
    }
    if (c.selectedWorkspace == null) {
      return Padding(
        padding: EdgeInsets.only(top: titlebarInset(context)),
        child: EmptyState(
          icon: FLucideIcons.box,
          title: '没有工作区',
          body: '工作区是 Agent 能看到的文件夹。',
          actions: [
            FButton(
              size: .sm,
              mainAxisSize: MainAxisSize.min,
              onPress: () => showCreateWorkspaceDialog(context, c),
              child: const Text('添加工作区…'),
            ),
          ],
        ),
      );
    }
    return WorkspaceView(controller: c, terminalFocus: _terminalFocus);
  }
}

/// Shown while Core cannot be reached. What is on screen is the last state
/// Core reported; the connection is retried on its own.
class ConnectionProblemBar extends StatelessWidget {
  final String problem;

  const ConnectionProblemBar({required this.problem, super.key});

  @override
  Widget build(BuildContext context) {
    final colors = context.colors;
    return Container(
      key: const Key('connection-problem-bar'),
      decoration: BoxDecoration(
        color: context.appColors.warning.withValues(alpha: 0.08),
        border: Border(top: BorderSide(color: colors.border)),
      ),
      padding: const EdgeInsets.fromLTRB(20, 8, 20, 8),
      child: Row(
        children: [
          Icon(FLucideIcons.unplug, size: 14, color: context.appColors.warning),
          const SizedBox(width: 8),
          Expanded(
            child: Semantics(
              liveRegion: true,
              child: Text(problem, style: context.bodyStyle),
            ),
          ),
        ],
      ),
    );
  }
}

/// The latest result of something the user did. It stays until dismissed or
/// replaced, and VoiceOver reads it when it changes.
class NoticeBar extends StatelessWidget {
  final NullkeyStudioController controller;

  const NoticeBar({required this.controller, super.key});

  @override
  Widget build(BuildContext context) {
    final notice = controller.notice;
    if (notice == null) {
      return const SizedBox.shrink();
    }
    final colors = context.colors;
    final accent = notice.isError
        ? colors.destructive
        : context.appColors.success;
    return Container(
      key: const Key('notice-bar'),
      decoration: BoxDecoration(
        color: colors.background,
        border: Border(top: BorderSide(color: colors.border)),
      ),
      padding: const EdgeInsets.fromLTRB(20, 4, 8, 4),
      child: Row(
        children: [
          Icon(
            notice.isError
                ? FLucideIcons.circleAlert
                : FLucideIcons.circleCheck,
            size: 14,
            color: accent,
          ),
          const SizedBox(width: 8),
          Expanded(
            child: Semantics(
              liveRegion: true,
              child: Text(
                notice.message,
                key: ValueKey('notice-${notice.id}'),
                style: context.bodyStyle,
              ),
            ),
          ),
          ToolButton(
            key: const Key('notice-dismiss'),
            icon: FLucideIcons.x,
            tooltip: '关闭',
            onPress: controller.dismissNotice,
          ),
        ],
      ),
    );
  }
}

/// Shown instead of the window content while Nullkey is locked. Running
/// sandboxes are unaffected.
class LockView extends StatelessWidget {
  final NullkeyStudioController controller;

  const LockView({required this.controller, super.key});

  @override
  Widget build(BuildContext context) {
    final c = controller;
    final colors = context.colors;
    final biometric = c.passkeyCapabilities?.biometryAvailable ?? true;
    final notice = c.notice;
    return ColoredBox(
      color: context.appColors.sidebar,
      child: Center(
        child: SingleChildScrollView(
          padding: const EdgeInsets.all(32),
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 360),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                Icon(
                  FLucideIcons.lock,
                  size: 30,
                  color: colors.mutedForeground,
                ),
                const SizedBox(height: 14),
                Semantics(
                  header: true,
                  child: Text(
                    'Nullkey 已锁定',
                    style: context.typo.xl.copyWith(
                      fontWeight: FontWeight.w600,
                      color: colors.foreground,
                    ),
                  ),
                ),
                const SizedBox(height: 6),
                Text(
                  '运行中的沙箱不受影响。',
                  textAlign: TextAlign.center,
                  style: context.bodyStyle.copyWith(
                    color: colors.mutedForeground,
                  ),
                ),
                const SizedBox(height: 20),
                FButton(
                  key: const Key('locked-gate-unlock-button'),
                  mainAxisSize: MainAxisSize.min,
                  autofocus: true,
                  prefix: const Icon(FLucideIcons.fingerprint, size: 15),
                  onPress: c.unlockStudioWithPasskey,
                  child: Text(biometric ? '用 Touch ID 解锁' : '用密码解锁'),
                ),
                if (notice != null && notice.isError) ...[
                  const SizedBox(height: 14),
                  Semantics(
                    liveRegion: true,
                    child: Text(
                      notice.message,
                      key: const Key('locked-gate-message'),
                      textAlign: TextAlign.center,
                      style: context.secondaryStyle,
                    ),
                  ),
                ],
              ],
            ),
          ),
        ),
      ),
    );
  }
}
