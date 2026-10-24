import 'package:flutter/widgets.dart';
import 'package:forui/forui.dart';
import 'package:xterm/xterm.dart';

import '../models/nullkey_models.dart';
import '../state/nullkey_studio_controller.dart';
import '../theme/theme.dart';
import 'common.dart';

/// The sandbox's PTY in a terminal emulator. Keys reach the agent only once
/// the terminal reports ready; every other phase explains why typing does
/// nothing.
class TerminalPane extends StatelessWidget {
  final NullkeyStudioController controller;
  final FocusNode inputFocus;

  const TerminalPane({
    required this.controller,
    required this.inputFocus,
    super.key,
  });

  @override
  Widget build(BuildContext context) {
    final c = controller;
    final app = context.appColors;
    final phase = c.terminalPhase;
    final terminal = c.selectedTerminal;

    return ColoredBox(
      color: app.terminal,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          if (c.showReconnectedGapBanner)
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 16, 0),
              child: InlineAlert(
                key: const Key('terminal-reconnected-gap-alert'),
                tone: Tone.info,
                title: '已重新连接',
                body: '断线期间的输出无法恢复。',
                onDismiss: c.dismissHistoryGap,
              ),
            ),
          if (c.terminalWaitingForReadyFrame)
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 16, 0),
              child: InlineAlert(
                key: const Key('terminal-waiting-ready-alert'),
                tone: Tone.neutral,
                title: '正在等待终端就绪',
                actions: [
                  SmallButton(
                    key: const Key('terminal-complete-ready-frame-button'),
                    label: '模拟就绪',
                    onPress: c.completeTerminalReadyFrame,
                  ),
                ],
              ),
            ),
          Expanded(
            child: terminal == null
                ? const SizedBox.shrink()
                : Semantics(
                    container: true,
                    label: '终端：${phase.label}',
                    child: TerminalView(
                      terminal,
                      key: const Key('terminal-view'),
                      focusNode: inputFocus,
                      autofocus: true,
                      readOnly: !phase.canSendInput,
                      theme: _terminalTheme(context),
                      textStyle: const TerminalStyle(
                        fontSize: 12.5,
                        height: 1.25,
                        fontFamily: monoFontFamily,
                        fontFamilyFallback: monoFontFallback,
                      ),
                      padding: const EdgeInsets.fromLTRB(16, 12, 16, 12),
                      keyboardAppearance: context.colors.brightness,
                      cursorType: TerminalCursorType.verticalBar,
                    ),
                  ),
          ),
          _StatusBar(controller: c, phase: phase),
        ],
      ),
    );
  }
}

TerminalTheme _terminalTheme(BuildContext context) {
  final app = context.appColors;
  final colors = context.colors;
  final dark = colors.brightness == Brightness.dark;
  // ANSI colors tuned for the pane's background: the dark set follows the
  // system's dark-mode colors, the light set their darker, readable variants.
  final ansi = dark
      ? const [
          Color(0xFF3A3A3C), Color(0xFFFF6961), Color(0xFF30D158),
          Color(0xFFFFD60A), Color(0xFF409CFF), Color(0xFFDA8FFF),
          Color(0xFF64D2FF), Color(0xFFD1D1D6), //
          Color(0xFF8E8E93), Color(0xFFFF8A84), Color(0xFF66E28A),
          Color(0xFFFFE45C), Color(0xFF70B5FF), Color(0xFFE5B0FF),
          Color(0xFF9BE3FF), Color(0xFFFFFFFF),
        ]
      : const [
          Color(0xFF1D1D1F), Color(0xFFC4302B), Color(0xFF1E7B34),
          Color(0xFF8F5A00), Color(0xFF0040DD), Color(0xFF8944AB),
          Color(0xFF0B6E8E), Color(0xFFAEAEB2), //
          Color(0xFF6E6E73), Color(0xFFD70015), Color(0xFF248A3D),
          Color(0xFFA05A00), Color(0xFF0A5BD8), Color(0xFFA550C7),
          Color(0xFF0071A4), Color(0xFF8E8E93),
        ];
  return TerminalTheme(
    cursor: colors.primary,
    selection: colors.primary.withValues(alpha: 0.28),
    foreground: app.terminalForeground,
    background: app.terminal,
    black: ansi[0],
    red: ansi[1],
    green: ansi[2],
    yellow: ansi[3],
    blue: ansi[4],
    magenta: ansi[5],
    cyan: ansi[6],
    white: ansi[7],
    brightBlack: ansi[8],
    brightRed: ansi[9],
    brightGreen: ansi[10],
    brightYellow: ansi[11],
    brightBlue: ansi[12],
    brightMagenta: ansi[13],
    brightCyan: ansi[14],
    brightWhite: ansi[15],
    searchHitBackground: const Color(0xFFFFD60A),
    searchHitBackgroundCurrent: const Color(0xFFFF9F0A),
    searchHitForeground: const Color(0xFF1D1D1F),
  );
}

/// What the terminal cannot do right now, and how to get it back.
class _StatusBar extends StatelessWidget {
  final NullkeyStudioController controller;
  final TerminalConnectionPhase phase;

  const _StatusBar({required this.controller, required this.phase});

  @override
  Widget build(BuildContext context) {
    final c = controller;
    Widget blocked({
      required Key key,
      required Tone tone,
      required String title,
      required List<Widget> actions,
      String? body,
    }) => Padding(
      padding: const EdgeInsets.fromLTRB(16, 0, 16, 16),
      child: InlineAlert(
        key: key,
        tone: tone,
        title: title,
        body: body,
        actions: actions,
      ),
    );

    switch (phase) {
      case TerminalConnectionPhase.replaced:
        return blocked(
          key: const Key('terminal-replaced-alert'),
          tone: Tone.warning,
          title: '终端已在另一个窗口打开',
          body: '在这里继续会断开另一个窗口。',
          actions: [
            FButton(
              key: const Key('terminal-reclaim-button'),
              size: .sm,
              mainAxisSize: MainAxisSize.min,
              onPress: c.reconnectTerminal,
              child: const Text('在这里继续'),
            ),
          ],
        );
      case TerminalConnectionPhase.closed:
        return blocked(
          key: const Key('terminal-closed-alert'),
          tone: Tone.neutral,
          title: '终端已断开',
          body: '沙箱仍在运行。',
          actions: [
            FButton(
              key: const Key('terminal-reconnect-button'),
              size: .sm,
              mainAxisSize: MainAxisSize.min,
              onPress: c.reconnectTerminal,
              child: const Text('重新连接'),
            ),
          ],
        );
      case TerminalConnectionPhase.exited:
        final name = c.selectedSandbox?.harness.displayName ?? 'Agent';
        final code = c.terminalExitCode;
        return blocked(
          key: const Key('terminal-exited-alert'),
          tone: code == 0 ? Tone.neutral : Tone.warning,
          title: '$name 已退出',
          body: [
            if (code != null && code != 0) '退出码 $code。',
            '停止沙箱后可以重新启动，文件和会话会保留。',
          ].join(),
          actions: [
            FButton(
              key: const Key('terminal-exited-stop-button'),
              size: .sm,
              mainAxisSize: MainAxisSize.min,
              onPress: c.stopSelectedSandbox,
              child: const Text('停止沙箱'),
            ),
          ],
        );
      case TerminalConnectionPhase.error:
        return blocked(
          key: const Key('terminal-error-alert'),
          tone: Tone.danger,
          title: '无法连接终端',
          body: c.terminalError,
          actions: [
            FButton(
              key: const Key('terminal-retry-button'),
              size: .sm,
              mainAxisSize: MainAxisSize.min,
              onPress: c.reconnectTerminal,
              child: const Text('重试'),
            ),
          ],
        );
      case TerminalConnectionPhase.idle:
        return blocked(
          key: const Key('terminal-idle-alert'),
          tone: Tone.neutral,
          title: '终端未连接',
          actions: [
            FButton(
              size: .sm,
              mainAxisSize: MainAxisSize.min,
              onPress: c.attachTerminalToSelectedSandbox,
              child: const Text('连接'),
            ),
          ],
        );
      case TerminalConnectionPhase.requestingTicket ||
          TerminalConnectionPhase.connectingWs ||
          TerminalConnectionPhase.reconnecting:
        return Semantics(
          liveRegion: true,
          child: Padding(
            key: const Key('terminal-connecting'),
            padding: const EdgeInsets.fromLTRB(18, 0, 18, 14),
            child: Row(
              children: [
                const Spinner(),
                const SizedBox(width: 8),
                Text(
                  phase == TerminalConnectionPhase.reconnecting
                      ? '正在重新连接…'
                      : '正在连接终端…',
                  style: context.secondaryStyle,
                ),
              ],
            ),
          ),
        );
      case TerminalConnectionPhase.ready ||
          TerminalConnectionPhase.daemonInterrupted:
        return const SizedBox.shrink();
    }
  }
}
