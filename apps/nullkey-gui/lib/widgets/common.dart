import 'package:flutter/foundation.dart';
import 'package:flutter/widgets.dart';
import 'package:forui/forui.dart';

import '../models/nullkey_models.dart';
import '../state/nullkey_studio_controller.dart';
import '../theme/theme.dart';

/// Height of the transparent macOS title bar the window content extends
/// under. The traffic lights and window title live there, and clicks in it
/// move the window, so nothing interactive is placed inside it.
double titlebarInset(BuildContext context) =>
    defaultTargetPlatform == TargetPlatform.macOS ? 28 : 0;

extension UiContext on BuildContext {
  FColors get colors => theme.colors;
  AppColors get appColors => theme.colors.app;
  FTypeface get typo => theme.typography.body;

  TextStyle get bodyStyle => typo.sm.copyWith(color: colors.foreground);
  TextStyle get secondaryStyle =>
      typo.xs.copyWith(color: colors.mutedForeground);
  TextStyle get strongStyle =>
      typo.sm.copyWith(color: colors.foreground, fontWeight: FontWeight.w600);

  TextStyle mono({double size = 12, Color? color}) => TextStyle(
    fontFamily: monoFontFamily,
    fontFamilyFallback: monoFontFallback,
    fontSize: size,
    height: 1.5,
    color: color ?? colors.foreground,
  );
}

/// `/Users/name/…` shown as `~/…`, as Finder and Terminal do.
String abbreviateHome(String path) {
  final match = RegExp(r'^/Users/[^/]+').firstMatch(path);
  return match == null ? path : '~${path.substring(match.end)}';
}

String relativeTime(DateTime time, {DateTime? now}) {
  final diff = (now ?? DateTime.now().toUtc()).difference(time.toUtc());
  if (diff.inSeconds < 60) {
    return '刚刚';
  }
  if (diff.inMinutes < 60) {
    return '${diff.inMinutes} 分钟前';
  }
  if (diff.inHours < 24) {
    return '${diff.inHours} 小时前';
  }
  return '${diff.inDays} 天前';
}

String clockTime(DateTime time) {
  final local = time.toLocal();
  String two(int v) => v.toString().padLeft(2, '0');
  return '${two(local.hour)}:${two(local.minute)}';
}

/// How a workspace's sandbox is described in the sidebar and session header.
/// The color is always paired with [label].
({String label, Color color}) sandboxStatus(
  BuildContext context,
  NullkeyStudioController c,
  String workspaceId,
) {
  final colors = context.colors;
  final app = context.appColors;
  if (c.isLaunching(workspaceId)) {
    return (label: '正在启动', color: colors.primary);
  }
  final sbx = c.sandboxForWorkspace(workspaceId);
  if (sbx == null) {
    return (label: '未启动', color: colors.mutedForeground);
  }
  return switch (sbx.status) {
    SandboxStatus.running => (label: '运行中', color: app.success),
    SandboxStatus.creating ||
    SandboxStatus.starting => (label: '正在启动', color: colors.primary),
    SandboxStatus.stopping => (label: '正在停止', color: colors.mutedForeground),
    SandboxStatus.failed => (label: '启动失败', color: colors.destructive),
    SandboxStatus.stopped =>
      isInterrupted(sbx)
          ? (label: '已中断', color: app.warning)
          : (label: '已停止', color: colors.mutedForeground),
  };
}

bool isInterrupted(SandboxRecord sbx) =>
    sbx.status == SandboxStatus.stopped && sbx.stopReason == 'daemon_restarted';

class StatusDot extends StatelessWidget {
  final Color color;
  final double size;

  const StatusDot({required this.color, this.size = 8, super.key});

  @override
  Widget build(BuildContext context) => Container(
    width: size,
    height: size,
    decoration: BoxDecoration(color: color, shape: BoxShape.circle),
  );
}

enum Tone { neutral, info, success, warning, danger }

Color toneColor(BuildContext context, Tone tone) => switch (tone) {
  Tone.neutral => context.colors.mutedForeground,
  Tone.info => context.colors.primary,
  Tone.success => context.appColors.success,
  Tone.warning => context.appColors.warning,
  Tone.danger => context.colors.destructive,
};

IconData toneIcon(Tone tone) => switch (tone) {
  Tone.neutral || Tone.info => FLucideIcons.info,
  Tone.success => FLucideIcons.circleCheck,
  Tone.warning => FLucideIcons.triangleAlert,
  Tone.danger => FLucideIcons.circleAlert,
};

/// A message attached to the place it is about, with the next step as a
/// trailing button. Announced to VoiceOver when it appears.
class InlineAlert extends StatelessWidget {
  final Tone tone;
  final String title;
  final String? body;
  final Widget? details;
  final List<Widget> actions;
  final VoidCallback? onDismiss;
  final IconData? icon;

  const InlineAlert({
    required this.tone,
    required this.title,
    this.body,
    this.details,
    this.actions = const [],
    this.onDismiss,
    this.icon,
    super.key,
  });

  @override
  Widget build(BuildContext context) {
    final accent = toneColor(context, tone);
    final colors = context.colors;
    final highContrast = MediaQuery.highContrastOf(context);
    return Semantics(
      container: true,
      liveRegion: true,
      child: Container(
        decoration: BoxDecoration(
          color: Color.alphaBlend(
            accent.withValues(alpha: tone == Tone.neutral ? 0.05 : 0.07),
            colors.background,
          ),
          border: Border.all(
            color: accent.withValues(alpha: highContrast ? 0.9 : 0.3),
          ),
          borderRadius: BorderRadius.circular(8),
        ),
        padding: EdgeInsets.fromLTRB(12, 8, onDismiss == null ? 10 : 4, 8),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Row(
              children: [
                Icon(icon ?? toneIcon(tone), size: 15, color: accent),
                const SizedBox(width: 10),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(title, style: context.strongStyle),
                      if (body != null)
                        Text(
                          body!,
                          style: context.bodyStyle.copyWith(
                            color: colors.mutedForeground,
                          ),
                        ),
                    ],
                  ),
                ),
                for (final action in actions) ...[
                  const SizedBox(width: 8),
                  action,
                ],
                if (onDismiss != null) ...[
                  const SizedBox(width: 4),
                  ToolButton(
                    icon: FLucideIcons.x,
                    tooltip: '关闭',
                    onPress: onDismiss,
                  ),
                ],
              ],
            ),
            if (details != null)
              Padding(
                padding: const EdgeInsets.only(left: 25, top: 6),
                child: details!,
              ),
          ],
        ),
      ),
    );
  }
}

/// Icon-only button. [tooltip] is also its VoiceOver label.
class ToolButton extends StatelessWidget {
  final IconData icon;
  final String tooltip;
  final VoidCallback? onPress;
  final FocusNode? focusNode;

  const ToolButton({
    required this.icon,
    required this.tooltip,
    required this.onPress,
    this.focusNode,
    super.key,
  });

  @override
  Widget build(BuildContext context) => FTooltip(
    tipBuilder: (context, _) => Text(tooltip),
    child: FButton.icon(
      variant: .ghost,
      size: .sm,
      semanticsLabel: tooltip,
      focusNode: focusNode,
      onPress: onPress,
      child: Icon(icon, size: 15),
    ),
  );
}

/// A small text button for secondary actions inside alerts and panels.
class SmallButton extends StatelessWidget {
  final String label;
  final VoidCallback? onPress;
  final FButtonVariant variant;
  final IconData? icon;

  const SmallButton({
    required this.label,
    required this.onPress,
    this.variant = .outline,
    this.icon,
    super.key,
  });

  @override
  Widget build(BuildContext context) => FButton(
    variant: variant,
    size: .sm,
    mainAxisSize: MainAxisSize.min,
    onPress: onPress,
    prefix: icon == null ? null : Icon(icon, size: 14),
    child: Text(label),
  );
}

@immutable
class MenuAction {
  final String label;
  final IconData? icon;
  final VoidCallback? onSelect;
  final bool destructive;
  final String? shortcut;
  final Key? key;

  const MenuAction({
    required this.label,
    required this.onSelect,
    this.icon,
    this.destructive = false,
    this.shortcut,
    this.key,
  });
}

/// A pull-down menu. Choosing an item closes the menu before running it.
class PullDownMenu extends StatelessWidget {
  final List<List<MenuAction>> groups;
  final Widget Function(BuildContext context, VoidCallback toggle) builder;
  final AlignmentGeometry menuAnchor;
  final AlignmentGeometry childAnchor;

  const PullDownMenu({
    required this.groups,
    required this.builder,
    this.menuAnchor = Alignment.topRight,
    this.childAnchor = Alignment.bottomRight,
    super.key,
  });

  @override
  Widget build(BuildContext context) => FPopoverMenu(
    menuAnchor: menuAnchor,
    childAnchor: childAnchor,
    menuBuilder: (context, controller, _) => [
      for (final group in groups)
        FItemGroup(
          children: [
            for (final action in group)
              FItem(
                key: action.key,
                variant: action.destructive ? .destructive : .primary,
                enabled: action.onSelect != null,
                prefix: action.icon == null ? null : Icon(action.icon),
                title: Text(action.label),
                suffix: action.shortcut == null
                    ? null
                    : Text(action.shortcut!, style: context.secondaryStyle),
                onPress: () {
                  controller.hide();
                  action.onSelect?.call();
                },
              ),
          ],
        ),
    ],
    builder: (context, controller, _) =>
        builder(context, () => controller.toggle()),
  );
}

@immutable
class Segment<T> {
  final T value;
  final String label;
  final String? count;
  final Key? key;

  const Segment({
    required this.value,
    required this.label,
    this.count,
    this.key,
  });
}

/// macOS-style segmented control for switching between views of one thing.
class SegmentedControl<T> extends StatelessWidget {
  final List<Segment<T>> segments;
  final T selected;
  final ValueChanged<T> onChanged;
  final String semanticsLabel;

  const SegmentedControl({
    required this.segments,
    required this.selected,
    required this.onChanged,
    required this.semanticsLabel,
    super.key,
  });

  @override
  Widget build(BuildContext context) {
    final colors = context.colors;
    final dark = colors.brightness == Brightness.dark;
    return Semantics(
      label: semanticsLabel,
      container: true,
      child: Container(
        padding: const EdgeInsets.all(2),
        decoration: BoxDecoration(
          color: colors.secondary,
          borderRadius: BorderRadius.circular(7),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            for (final s in segments)
              FTappable.static(
                key: s.key,
                selected: s.value == selected,
                semanticsLabel: s.count == null
                    ? s.label
                    : '${s.label}，${s.count}',
                semanticsInMutuallyExclusiveGroup: true,
                onPress: () => onChanged(s.value),
                builder: (context, states, _) {
                  final isSelected = s.value == selected;
                  final hovered = states.contains(FTappableVariant.hovered);
                  return Container(
                    padding: const EdgeInsets.symmetric(
                      horizontal: 12,
                      vertical: 3,
                    ),
                    decoration: BoxDecoration(
                      color: isSelected
                          ? (dark ? const Color(0xFF5A5A5F) : colors.background)
                          : hovered
                          ? colors.foreground.withValues(alpha: 0.05)
                          : null,
                      borderRadius: BorderRadius.circular(5),
                      boxShadow: isSelected && !dark
                          ? const [
                              BoxShadow(
                                color: Color(0x1F000000),
                                blurRadius: 1.5,
                                offset: Offset(0, 0.5),
                              ),
                            ]
                          : null,
                    ),
                    child: Row(
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        Text(
                          s.label,
                          style: context.typo.sm.copyWith(
                            color: isSelected
                                ? colors.foreground
                                : colors.mutedForeground,
                            fontWeight: isSelected
                                ? FontWeight.w600
                                : FontWeight.w500,
                          ),
                        ),
                        if (s.count != null) ...[
                          const SizedBox(width: 5),
                          Text(
                            s.count!,
                            style: context.typo.xs.copyWith(
                              color: colors.mutedForeground,
                              fontFeatures: const [
                                FontFeature.tabularFigures(),
                              ],
                            ),
                          ),
                        ],
                      ],
                    ),
                  );
                },
              ),
          ],
        ),
      ),
    );
  }
}

/// Centered message for a pane with nothing to show yet.
class EmptyState extends StatelessWidget {
  final IconData icon;
  final String title;
  final String? body;
  final List<Widget> actions;

  const EmptyState({
    required this.icon,
    required this.title,
    this.body,
    this.actions = const [],
    super.key,
  });

  @override
  Widget build(BuildContext context) => Center(
    child: SingleChildScrollView(
      padding: const EdgeInsets.all(32),
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 420),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(icon, size: 28, color: context.colors.mutedForeground),
            const SizedBox(height: 12),
            Text(
              title,
              textAlign: TextAlign.center,
              style: context.typo.md.copyWith(
                fontWeight: FontWeight.w600,
                color: context.colors.foreground,
              ),
            ),
            if (body != null) ...[
              const SizedBox(height: 6),
              Text(
                body!,
                textAlign: TextAlign.center,
                style: context.bodyStyle.copyWith(
                  color: context.colors.mutedForeground,
                ),
              ),
            ],
            if (actions.isNotEmpty) ...[
              const SizedBox(height: 18),
              Wrap(
                spacing: 8,
                runSpacing: 8,
                alignment: WrapAlignment.center,
                children: actions,
              ),
            ],
          ],
        ),
      ),
    ),
  );
}

/// Keyboard key cap used in shortcut help and menus.
class KeyCap extends StatelessWidget {
  final String label;

  const KeyCap(this.label, {super.key});

  @override
  Widget build(BuildContext context) => Container(
    padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
    decoration: BoxDecoration(
      color: context.colors.secondary,
      borderRadius: BorderRadius.circular(4),
      border: Border.all(color: context.colors.border),
    ),
    child: Text(label, style: context.secondaryStyle),
  );
}

/// A bordered group of rows, like a grouped list in System Settings.
class GroupedList extends StatelessWidget {
  final List<Widget> children;

  const GroupedList({required this.children, super.key});

  @override
  Widget build(BuildContext context) {
    final colors = context.colors;
    return Container(
      decoration: BoxDecoration(
        color: colors.card,
        border: Border.all(color: colors.border),
        borderRadius: BorderRadius.circular(8),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          for (final (i, child) in children.indexed) ...[
            if (i > 0) Container(height: 1, color: colors.border),
            child,
          ],
        ],
      ),
    );
  }
}

/// Thin separator line in the theme's border color.
class Hairline extends StatelessWidget {
  final bool vertical;

  const Hairline({this.vertical = false, super.key});

  @override
  Widget build(BuildContext context) => vertical
      ? Container(width: 1, color: context.colors.border)
      : Container(height: 1, color: context.colors.border);
}

/// Spinner that becomes a static icon when the system asks for reduced
/// motion.
class Spinner extends StatelessWidget {
  final double size;

  const Spinner({this.size = 14, super.key});

  @override
  Widget build(BuildContext context) {
    if (MediaQuery.disableAnimationsOf(context)) {
      return Icon(
        FLucideIcons.loaderCircle,
        size: size,
        color: context.colors.mutedForeground,
      );
    }
    return SizedBox(
      width: size,
      height: size,
      child: IconTheme(
        data: IconThemeData(size: size, color: context.colors.mutedForeground),
        child: const FCircularProgress(),
      ),
    );
  }
}
