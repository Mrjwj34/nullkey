import 'package:flutter/widgets.dart';
import 'package:forui/forui.dart';

import '../models/nullkey_models.dart';
import '../state/nullkey_studio_controller.dart';
import 'common.dart';

/// Workspace list plus the single entry into management.
class Sidebar extends StatefulWidget {
  final NullkeyStudioController controller;

  /// True right after Cmd+Shift+M moved focus here, until focus leaves.
  final bool showFocusMovedHint;
  final ValueChanged<bool> onListFocusChange;
  final VoidCallback onAddWorkspace;
  final VoidCallback onOpenManagement;
  final VoidCallback onShowShortcuts;
  final VoidCallback onOpenDeveloperScenarios;

  const Sidebar({
    required this.controller,
    required this.showFocusMovedHint,
    required this.onListFocusChange,
    required this.onAddWorkspace,
    required this.onOpenManagement,
    required this.onShowShortcuts,
    required this.onOpenDeveloperScenarios,
    super.key,
  });

  @override
  State<Sidebar> createState() => SidebarState();
}

class SidebarState extends State<Sidebar> {
  final Map<String, FocusNode> _rowFocus = {};
  final FocusNode _setupFocus = FocusNode(debugLabel: 'sidebar-setup');

  NullkeyStudioController get c => widget.controller;

  /// Moves keyboard focus to the selected workspace row (or the first row).
  void focusList() {
    final selected = c.selectedWorkspaceId;
    final node =
        (selected == null ? null : _rowFocus[selected]) ??
        _rowFocus.values.firstOrNull ??
        _setupFocus;
    node.requestFocus();
  }

  FocusNode _focusFor(String id) =>
      _rowFocus.putIfAbsent(id, () => FocusNode(debugLabel: 'sidebar-$id'));

  @override
  void dispose() {
    for (final node in _rowFocus.values) {
      node.dispose();
    }
    _setupFocus.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final workspaces = c.workspaces;
    final ids = workspaces.map((w) => w.id).toSet();
    for (final stale
        in _rowFocus.keys.where((k) => !ids.contains(k)).toList()) {
      _rowFocus.remove(stale)!.dispose();
    }
    final showSetup = !c.setupComplete || c.surface == AppSurface.setup;
    final isMock =
        c.capabilities.connectionMode == DaemonConnectionMode.contractEngine;

    return ColoredBox(
      color: context.appColors.sidebar,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          SizedBox(height: titlebarInset(context) + 10),
          Expanded(
            child: Focus(
              canRequestFocus: false,
              skipTraversal: true,
              onFocusChange: widget.onListFocusChange,
              child: FocusTraversalGroup(
                child: ListView(
                  padding: const EdgeInsets.fromLTRB(8, 0, 8, 12),
                  children: [
                    if (showSetup) ...[
                      _SetupRow(
                        controller: c,
                        focusNode: _setupFocus,
                        selected: c.surface == AppSurface.setup,
                      ),
                      const SizedBox(height: 12),
                    ],
                    _SectionHeader(
                      showFocusMovedHint: widget.showFocusMovedHint,
                      onAdd: widget.onAddWorkspace,
                    ),
                    for (final ws in workspaces)
                      _WorkspaceRow(
                        controller: c,
                        workspace: ws,
                        focusNode: _focusFor(ws.id),
                        selected:
                            c.surface == AppSurface.workspace &&
                            c.selectedWorkspaceId == ws.id,
                      ),
                  ],
                ),
              ),
            ),
          ),
          const Hairline(),
          Padding(
            padding: const EdgeInsets.fromLTRB(8, 6, 8, 8),
            child: Row(
              children: [
                Expanded(
                  child: _SidebarTile(
                    key: const Key('sidebar-management'),
                    selected: false,
                    semanticsLabel: '管理',
                    onPress: widget.onOpenManagement,
                    child: Row(
                      children: [
                        Icon(
                          FLucideIcons.settings2,
                          size: 16,
                          color: context.colors.mutedForeground,
                        ),
                        const SizedBox(width: 8),
                        Text('管理', style: context.bodyStyle),
                      ],
                    ),
                  ),
                ),
                if (isMock)
                  ToolButton(
                    key: const Key('sidebar-developer'),
                    icon: FLucideIcons.flaskConical,
                    tooltip: '模拟数据',
                    onPress: widget.onOpenDeveloperScenarios,
                  ),
                ToolButton(
                  key: const Key('sidebar-shortcuts'),
                  icon: FLucideIcons.keyboard,
                  tooltip: '键盘快捷键（⌘/）',
                  onPress: widget.onShowShortcuts,
                ),
                ToolButton(
                  key: const Key('sidebar-lock'),
                  icon: FLucideIcons.lock,
                  tooltip: '锁定（⌘L）',
                  onPress: c.lockStudio,
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

class _SectionHeader extends StatelessWidget {
  final bool showFocusMovedHint;
  final VoidCallback onAdd;

  const _SectionHeader({required this.showFocusMovedHint, required this.onAdd});

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.fromLTRB(8, 0, 0, 2),
    child: Row(
      children: [
        Text(
          '工作区',
          style: context.secondaryStyle.copyWith(fontWeight: FontWeight.w600),
        ),
        if (showFocusMovedHint) ...[
          const SizedBox(width: 8),
          Flexible(
            child: Semantics(
              liveRegion: true,
              child: Text(
                '键盘焦点在这里',
                key: const Key('focus-escaped-to-sidebar-badge'),
                overflow: TextOverflow.ellipsis,
                style: context.secondaryStyle.copyWith(
                  color: context.colors.primary,
                ),
              ),
            ),
          ),
        ],
        const Spacer(),
        ToolButton(
          key: const Key('sidebar-add-workspace'),
          icon: FLucideIcons.plus,
          tooltip: '添加工作区（⌘N）',
          onPress: onAdd,
        ),
      ],
    ),
  );
}

class _SidebarTile extends StatelessWidget {
  final bool selected;
  final FocusNode? focusNode;
  final VoidCallback onPress;
  final String semanticsLabel;
  final Widget child;

  const _SidebarTile({
    required this.selected,
    required this.onPress,
    required this.semanticsLabel,
    required this.child,
    this.focusNode,
    super.key,
  });

  @override
  Widget build(BuildContext context) {
    final app = context.appColors;
    return FTappable.static(
      focusNode: focusNode,
      selected: selected,
      semanticsLabel: semanticsLabel,
      onPress: onPress,
      builder: (context, states, _) {
        final hovered = states.contains(FTappableVariant.hovered);
        return Container(
          height: 30,
          padding: const EdgeInsets.symmetric(horizontal: 8),
          decoration: BoxDecoration(
            color: selected
                ? app.sidebarSelection
                : hovered
                ? app.sidebarSelection.withValues(alpha: 0.5)
                : null,
            borderRadius: BorderRadius.circular(7),
          ),
          child: child,
        );
      },
    );
  }
}

/// Trailing mark for a workspace row. Running shows a dot, transitions a
/// spinner, and problems an icon, so the state never depends on color alone;
/// a sandbox that is simply not running shows nothing.
Widget? _statusMark(
  BuildContext context,
  NullkeyStudioController c,
  String id,
) {
  if (c.isLaunching(id)) {
    return const Spinner(size: 12);
  }
  final sbx = c.sandboxForWorkspace(id);
  if (sbx == null) {
    return null;
  }
  return switch (sbx.status) {
    SandboxStatus.running => StatusDot(
      color: context.appColors.success,
      size: 7,
    ),
    SandboxStatus.creating ||
    SandboxStatus.starting ||
    SandboxStatus.stopping => const Spinner(size: 12),
    SandboxStatus.failed => Icon(
      FLucideIcons.circleAlert,
      size: 14,
      color: context.colors.destructive,
    ),
    SandboxStatus.stopped =>
      isInterrupted(sbx)
          ? Icon(
              FLucideIcons.triangleAlert,
              size: 14,
              color: context.appColors.warning,
            )
          : null,
  };
}

class _WorkspaceRow extends StatelessWidget {
  final NullkeyStudioController controller;
  final WorkspaceRecord workspace;
  final FocusNode focusNode;
  final bool selected;

  const _WorkspaceRow({
    required this.controller,
    required this.workspace,
    required this.focusNode,
    required this.selected,
  });

  @override
  Widget build(BuildContext context) {
    final c = controller;
    final ws = workspace;
    final status = sandboxStatus(context, c, ws.id);
    final sbx = c.sandboxForWorkspace(ws.id);
    final harness = sbx != null && sbx.status.isRunningOrStarting
        ? '，${sbx.harness.displayName}'
        : '';
    final mark = _statusMark(context, c, ws.id);

    return Padding(
      padding: const EdgeInsets.only(bottom: 2),
      child: _SidebarTile(
        key: Key('sidebar-workspace-${ws.id}'),
        focusNode: focusNode,
        selected: selected,
        semanticsLabel: '${ws.name}，${status.label}$harness',
        onPress: () => c.selectWorkspace(ws.id),
        child: Row(
          children: [
            Icon(
              ws.kind == WorkspaceKind.managed
                  ? FLucideIcons.box
                  : FLucideIcons.folder,
              size: 16,
              color: context.colors.mutedForeground,
            ),
            const SizedBox(width: 8),
            Expanded(
              child: Text(
                ws.name,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: context.bodyStyle,
              ),
            ),
            if (mark != null) ...[const SizedBox(width: 8), mark],
          ],
        ),
      ),
    );
  }
}

class _SetupRow extends StatelessWidget {
  final NullkeyStudioController controller;
  final FocusNode focusNode;
  final bool selected;

  const _SetupRow({
    required this.controller,
    required this.focusNode,
    required this.selected,
  });

  @override
  Widget build(BuildContext context) {
    final c = controller;
    final done = [
      c.codexConnected,
      c.codexInstalled,
      c.workspaces.isNotEmpty,
    ].where((d) => d).length;
    return _SidebarTile(
      key: const Key('sidebar-setup'),
      focusNode: focusNode,
      selected: selected,
      semanticsLabel: '开始使用，已完成 $done/3',
      onPress: c.openSetup,
      child: Row(
        children: [
          Icon(
            FLucideIcons.listChecks,
            size: 16,
            color: context.colors.primary,
          ),
          const SizedBox(width: 8),
          Expanded(child: Text('开始使用', style: context.bodyStyle)),
          Text(
            '$done/3',
            style: context.secondaryStyle.copyWith(
              fontFeatures: const [FontFeature.tabularFigures()],
            ),
          ),
        ],
      ),
    );
  }
}
