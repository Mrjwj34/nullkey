import 'package:flutter/widgets.dart';
import 'package:forui/forui.dart';
import 'package:material_ui/material_ui.dart'
    show SelectableText, SelectionArea;

import '../models/nullkey_models.dart';
import '../state/nullkey_studio_controller.dart';
import 'common.dart';

// -----------------------------------------------------------------------------
// Artifacts
// -----------------------------------------------------------------------------

({int added, int removed}) diffStat(ArtifactRecord a) {
  var added = 0;
  var removed = 0;
  for (final line in a.previewSnippet.split('\n')) {
    if (line.startsWith('+') && !line.startsWith('+++')) {
      added++;
    } else if (line.startsWith('-') && !line.startsWith('---')) {
      removed++;
    }
  }
  return (added: added, removed: removed);
}

/// Files the agent changed or produced, reviewed read-only.
class ArtifactsPane extends StatefulWidget {
  final NullkeyStudioController controller;

  const ArtifactsPane({required this.controller, super.key});

  @override
  State<ArtifactsPane> createState() => _ArtifactsPaneState();
}

class _ArtifactsPaneState extends State<ArtifactsPane> {
  String? _selectedId;

  @override
  Widget build(BuildContext context) {
    final artifacts = widget.controller.activeArtifacts;
    if (artifacts.isEmpty) {
      return const EmptyState(
        icon: FLucideIcons.fileDiff,
        title: '没有产物',
        body: 'Agent 修改的文件会显示在这里。',
      );
    }
    final selected =
        artifacts.where((a) => a.id == _selectedId).firstOrNull ??
        artifacts.first;
    return Row(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        SizedBox(
          width: 290,
          child: ListView(
            padding: const EdgeInsets.all(8),
            children: [
              for (final a in artifacts)
                _ArtifactRow(
                  artifact: a,
                  selected: a.id == selected.id,
                  onPress: () => setState(() => _selectedId = a.id),
                ),
            ],
          ),
        ),
        const Hairline(vertical: true),
        Expanded(child: _ArtifactPreview(artifact: selected)),
      ],
    );
  }
}

class _ArtifactRow extends StatelessWidget {
  final ArtifactRecord artifact;
  final bool selected;
  final VoidCallback onPress;

  const _ArtifactRow({
    required this.artifact,
    required this.selected,
    required this.onPress,
  });

  @override
  Widget build(BuildContext context) {
    final a = artifact;
    final colors = context.colors;
    final slash = a.relativePath.lastIndexOf('/');
    final fileName = a.relativePath.substring(slash + 1);
    final dir = slash < 0 ? '' : a.relativePath.substring(0, slash);
    final stat = diffStat(a);
    final isDiff = a.kind == 'diff';
    return FTappable.static(
      key: Key('artifact-${a.id}'),
      selected: selected,
      semanticsLabel:
          '${a.relativePath}，${a.summary}${isDiff ? '，新增 ${stat.added} 行，删除 ${stat.removed} 行' : ''}',
      onPress: onPress,
      builder: (context, states, _) => Container(
        margin: const EdgeInsets.only(bottom: 2),
        padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
        decoration: BoxDecoration(
          color: selected
              ? colors.secondary
              : states.contains(FTappableVariant.hovered)
              ? colors.muted
              : null,
          borderRadius: BorderRadius.circular(7),
        ),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Padding(
              padding: const EdgeInsets.only(top: 2),
              child: Icon(
                isDiff ? FLucideIcons.fileDiff : FLucideIcons.fileText,
                size: 15,
                color: colors.mutedForeground,
              ),
            ),
            const SizedBox(width: 8),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(
                    children: [
                      Expanded(
                        child: Text(
                          fileName,
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: context.bodyStyle.copyWith(
                            fontWeight: FontWeight.w500,
                          ),
                        ),
                      ),
                      if (isDiff) ...[
                        Text(
                          '+${stat.added}',
                          style: context.mono(
                            size: 11,
                            color: context.appColors.success,
                          ),
                        ),
                        const SizedBox(width: 4),
                        Text(
                          '−${stat.removed}',
                          style: context.mono(
                            size: 11,
                            color: colors.destructive,
                          ),
                        ),
                      ],
                    ],
                  ),
                  Text(
                    dir.isEmpty ? a.summary : dir,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: context.secondaryStyle,
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _ArtifactPreview extends StatelessWidget {
  final ArtifactRecord artifact;

  const _ArtifactPreview({required this.artifact});

  @override
  Widget build(BuildContext context) {
    final a = artifact;
    final colors = context.colors;
    final app = context.appColors;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(20, 12, 20, 12),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              SelectableText(
                a.relativePath,
                style: context
                    .mono(size: 12.5)
                    .copyWith(fontWeight: FontWeight.w600),
              ),
              const SizedBox(height: 2),
              Text(
                [
                  a.summary,
                  if (a.updatedAt case final at?) relativeTime(at),
                ].join(' · '),
                style: context.secondaryStyle,
              ),
            ],
          ),
        ),
        const Hairline(),
        Expanded(
          child: ColoredBox(
            color: app.terminal,
            child: SelectionArea(
              child: ListView(
                padding: const EdgeInsets.symmetric(vertical: 10),
                children: [
                  for (final line in a.previewSnippet.split('\n'))
                    Container(
                      color: a.kind != 'diff'
                          ? null
                          : line.startsWith('+')
                          ? app.success.withValues(alpha: 0.10)
                          : line.startsWith('-')
                          ? colors.destructive.withValues(alpha: 0.10)
                          : null,
                      padding: const EdgeInsets.symmetric(horizontal: 20),
                      child: Text(
                        line.isEmpty ? ' ' : line,
                        style: context.mono(
                          color: a.kind == 'diff' && line.startsWith('@@')
                              ? app.terminalMuted
                              : app.terminalForeground,
                        ),
                      ),
                    ),
                ],
              ),
            ),
          ),
        ),
      ],
    );
  }
}

// -----------------------------------------------------------------------------
// Services
// -----------------------------------------------------------------------------

({String label, Color color}) serviceStatusLabel(
  BuildContext context,
  ServiceStatus s,
) => switch (s) {
  ServiceStatus.ready => (label: '正常', color: context.appColors.success),
  ServiceStatus.starting => (label: '正在启动', color: context.colors.primary),
  ServiceStatus.error => (label: '无响应', color: context.colors.destructive),
};

/// Services the workspace declares in `.nullkey/services.json`. Each opens
/// in the browser through a single-use link.
class ServicesPane extends StatelessWidget {
  final NullkeyStudioController controller;

  const ServicesPane({required this.controller, super.key});

  @override
  Widget build(BuildContext context) {
    final c = controller;
    final services = c.activeServices;
    if (services.isEmpty) {
      return const EmptyState(
        icon: FLucideIcons.globe,
        title: '没有服务',
        body: '在工作区的 .nullkey/services.json 中声明服务，沙箱启动时会一起运行。',
      );
    }
    return ListView(
      padding: const EdgeInsets.fromLTRB(20, 16, 20, 20),
      children: [
        GroupedList(
          children: [
            for (final s in services) _ServiceRow(controller: c, service: s),
          ],
        ),
        const SizedBox(height: 10),
        Text('只有这台 Mac 能打开，每次打开都会生成新的链接。', style: context.secondaryStyle),
      ],
    );
  }
}

class _ServiceRow extends StatelessWidget {
  final NullkeyStudioController controller;
  final SandboxServiceRecord service;

  const _ServiceRow({required this.controller, required this.service});

  @override
  Widget build(BuildContext context) {
    final s = service;
    final status = serviceStatusLabel(context, s.status);
    return Padding(
      key: Key('service-${s.id}'),
      padding: const EdgeInsets.fromLTRB(14, 10, 10, 10),
      child: Row(
        children: [
          Expanded(
            child: Text(
              s.name,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: context.bodyStyle.copyWith(fontWeight: FontWeight.w500),
            ),
          ),
          StatusDot(color: status.color, size: 7),
          const SizedBox(width: 6),
          Text(status.label, style: context.secondaryStyle),
          const SizedBox(width: 14),
          SmallButton(
            key: Key('service-open-${s.id}'),
            icon: FLucideIcons.externalLink,
            label: '在浏览器中打开',
            onPress: s.status == ServiceStatus.ready
                ? () => controller.openService(s.id)
                : null,
          ),
        ],
      ),
    );
  }
}
// -----------------------------------------------------------------------------
// Desktop
// -----------------------------------------------------------------------------

class DesktopPane extends StatelessWidget {
  final NullkeyStudioController controller;

  const DesktopPane({required this.controller, super.key});

  @override
  Widget build(BuildContext context) {
    final c = controller;
    final desktop = c.activeDesktopStatus;
    if (desktop == null || !desktop.enabled) {
      return EmptyState(
        icon: FLucideIcons.monitor,
        title: '虚拟桌面已关闭',
        body: '用于查看沙箱里的图形界面应用。',
        actions: [
          FButton(
            key: const Key('desktop-enable-button'),
            size: .sm,
            mainAxisSize: MainAxisSize.min,
            onPress: () => c.setDesktopEnabled(true),
            child: const Text('打开'),
          ),
        ],
      );
    }
    final mock =
        c.capabilities.connectionMode == DaemonConnectionMode.contractEngine;
    return Padding(
      padding: const EdgeInsets.fromLTRB(20, 14, 20, 20),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              Text(
                '${desktop.resolution} · ${desktop.fps} fps',
                style: context.secondaryStyle,
              ),
              const Spacer(),
              SmallButton(
                key: const Key('desktop-disable-button'),
                label: '关闭',
                onPress: () => c.setDesktopEnabled(false),
              ),
            ],
          ),
          const SizedBox(height: 12),
          Expanded(
            child: Center(
              child: AspectRatio(
                aspectRatio: 16 / 10,
                child: Container(
                  decoration: BoxDecoration(
                    color: const Color(0xFF1C1C1E),
                    borderRadius: BorderRadius.circular(8),
                    border: Border.all(color: context.colors.border),
                  ),
                  alignment: Alignment.center,
                  child: Text(
                    mock ? '模拟数据，无画面' : '正在连接…',
                    style: context.bodyStyle.copyWith(
                      color: const Color(0xFF8E8E93),
                    ),
                  ),
                ),
              ),
            ),
          ),
        ],
      ),
    );
  }
}
