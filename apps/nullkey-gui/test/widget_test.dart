import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:forui/forui.dart';
import 'package:material_ui/material_ui.dart';
import 'package:nullkey_gui/main.dart';
import 'package:nullkey_gui/models/nullkey_models.dart';
import 'package:nullkey_gui/services/core_mock_backend.dart';
import 'package:nullkey_gui/state/nullkey_studio_controller.dart';
import 'package:nullkey_gui/theme/theme.dart';
import 'package:nullkey_gui/widgets/app_shell.dart';

NullkeyStudioController _controller({MockSeed seed = MockSeed.returningUser}) {
  final api = CoreMockBackend(seed: seed);
  final controller = NullkeyStudioController(api: api);
  // Tear-downs run last-in first-out: the controller lets go of the API
  // before the API closes its change stream.
  addTearDown(api.dispose);
  addTearDown(controller.dispose);
  return controller;
}

Future<NullkeyStudioController> _pumpApp(
  WidgetTester tester, {
  MockSeed seed = MockSeed.returningUser,
}) async {
  tester.view.physicalSize = const Size(1440, 900);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  final controller = _controller(seed: seed);
  await tester.pumpWidget(NullkeyApp(controller: controller));
  await tester.pumpAndSettle();
  return controller;
}

Finder _key(String key) => find.byKey(Key(key));

VoidCallback? _onPress(WidgetTester tester, String key) =>
    tester.widget<FButton>(_key(key)).onPress;

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  group('theme', () {
    test('uses the macOS system font', () {
      for (final brightness in Brightness.values) {
        final theme = buildTheme(brightness);
        expect(theme.typography.body.fontFamily, 'CupertinoSystemText');
        expect(theme.typography.display.fontFamily, 'CupertinoSystemDisplay');
        expect(theme.typography.body.sm.fontSize, 13);
      }
    });

    test('increase contrast strengthens borders and secondary text', () {
      for (final brightness in Brightness.values) {
        final normal = buildTheme(brightness).colors;
        final strong = buildTheme(brightness, highContrast: true).colors;
        expect(strong.border, isNot(normal.border));
        expect(strong.mutedForeground, isNot(normal.mutedForeground));
      }
    });

    testWidgets('uses Lucide icons', (tester) async {
      await tester.pumpWidget(
        MaterialApp(
          home: Builder(builder: (context) => lightTheme.icons.search(context)),
        ),
      );
      expect(find.byIcon(FLucideIcons.search), findsOneWidget);
    });

    testWidgets('follows the system appearance', (tester) async {
      tester.platformDispatcher.platformBrightnessTestValue = Brightness.light;
      addTearDown(tester.platformDispatcher.clearPlatformBrightnessTestValue);
      await _pumpApp(tester);
      Brightness current() =>
          tester.element(find.byType(AppShell)).theme.colors.brightness;
      expect(current(), Brightness.light);

      tester.platformDispatcher.platformBrightnessTestValue = Brightness.dark;
      await tester.pumpAndSettle();
      expect(current(), Brightness.dark);
    });
  });

  group('navigation', () {
    testWidgets('a returning user lands on the most recent workspace', (
      tester,
    ) async {
      final c = await _pumpApp(tester);
      expect(c.surface, AppSurface.workspace);
      expect(c.selectedWorkspaceId, 'ws-nullkey-core');
      expect(
        tester.widget<Text>(_key('session-header-workspace')).data,
        'nullkey-runtime',
      );
      expect(
        tester.widget<Text>(_key('session-header-network')).data,
        NetworkMode.servicesOnly.label,
      );
      expect(c.terminalPhase, TerminalConnectionPhase.ready);
      expect(_key('terminal-view'), findsOneWidget);
    });

    testWidgets(
      'first run starts Codex only after connect, install and workspace',
      (tester) async {
        final c = await _pumpApp(tester, seed: MockSeed.firstRun);
        expect(c.surface, AppSurface.setup);
        expect(_onPress(tester, 'setup-launch-button'), isNull);

        await tester.tap(_key('setup-import-codex'));
        await tester.pumpAndSettle();
        expect(c.codexConnected, isTrue);
        expect(_onPress(tester, 'setup-launch-button'), isNull);

        await tester.tap(_key('setup-install-button'));
        await tester.pump();
        c.advanceJob(c.jobFor(HarnessKind.codex)!.id, toProgress: 1);
        await tester.pumpAndSettle();
        expect(c.codexInstalled, isTrue);
        expect(_onPress(tester, 'setup-launch-button'), isNull);

        expect(
          await c.createWorkspace(
            name: 'demo',
            kind: WorkspaceKind.managed,
            open: false,
          ),
          isNull,
        );
        await tester.pumpAndSettle();
        expect(_onPress(tester, 'setup-launch-button'), isNotNull);

        await tester.tap(_key('setup-launch-button'));
        await tester.pumpAndSettle();
        expect(c.surface, AppSurface.workspace);
        expect(c.activeTab, SessionTab.terminal);
        expect(c.selectedSandbox!.networkMode, NetworkMode.filtered);
        expect(c.terminalPhase, TerminalConnectionPhase.ready);
      },
    );

    testWidgets('Cmd+Shift+M moves keyboard focus to the workspace list', (
      tester,
    ) async {
      await _pumpApp(tester);
      await tester.sendKeyDownEvent(LogicalKeyboardKey.metaLeft);
      await tester.sendKeyDownEvent(LogicalKeyboardKey.shiftLeft);
      await tester.sendKeyEvent(LogicalKeyboardKey.keyM);
      await tester.sendKeyUpEvent(LogicalKeyboardKey.shiftLeft);
      await tester.sendKeyUpEvent(LogicalKeyboardKey.metaLeft);
      await tester.pumpAndSettle();
      expect(_key('focus-escaped-to-sidebar-badge'), findsOneWidget);
    });
  });

  group('sign-in and install', () {
    test('a cancelled browser sign-in rejects the late callback', () async {
      final c = _controller(seed: MockSeed.firstRun);
      final flow = await c.startOAuth('openai');
      expect(flow!.status, OAuthFlowStatus.authorizing);

      await c.cancelOAuth(flow.flowId);
      expect(c.latestOAuthFlow('openai')!.status, OAuthFlowStatus.cancelled);

      c.completeOAuthCallback(flow.flowId);
      expect(c.notice!.isError, isTrue);
      expect(c.codexConnected, isFalse);
    });

    test(
      'cancelling after the browser finished shows the account connected',
      () async {
        final c = _controller(seed: MockSeed.firstRun);
        final flow = await c.startOAuth('openai');
        c.mock!.simulateOAuthCompleteBeforeCancel = true;

        await c.cancelOAuth(flow!.flowId);
        expect(c.latestOAuthFlow('openai')!.status, OAuthFlowStatus.succeeded);
        expect(c.codexConnected, isTrue);
        expect(c.notice!.isError, isFalse);
        expect(c.notice!.message, contains('已连接'));
      },
    );

    test('an install that finished before cancel stays installed', () async {
      final c = _controller(seed: MockSeed.firstRun);
      final job = (await c.startHarnessInstall(HarnessKind.codex))!;
      c.mock!.simulateJobCompleteBeforeCancel = true;

      await c.requestCancelJob(job.id);
      expect(c.api.getJob(job.id)!.status, JobStatus.succeeded);
      expect(c.codexInstalled, isTrue);
      expect(c.notice!.isError, isFalse);
    });

    testWidgets('cancelling an install shows cleanup, then cancelled', (
      tester,
    ) async {
      final c = await _pumpApp(tester, seed: MockSeed.firstRun);
      await tester.tap(_key('setup-install-button'));
      await tester.pump();
      await tester.tap(_key('setup-install-cancel'));
      // The cleanup spinner repeats, so settle by single frames.
      await tester.pump();
      final job = c.jobFor(HarnessKind.codex)!;
      expect(job.status, JobStatus.cancelling);
      expect(_key('setup-install-cancelling'), findsOneWidget);

      c.finalizeCancelJob(job.id);
      await tester.pumpAndSettle();
      expect(_key('setup-install-cancelled'), findsOneWidget);
      expect(c.codexInstalled, isFalse);
    });

    testWidgets('an expired login is reconnected in place', (tester) async {
      final c = await _pumpApp(tester);
      c.drillCredentialExpired();
      await tester.pumpAndSettle();
      expect(c.surface, AppSurface.workspace);
      expect(_key('reauth-alert'), findsOneWidget);
      expect(_key('reauth-reconnect-button'), findsOneWidget);
    });

    testWidgets('a new workspace asks to connect instead of claiming expiry', (
      tester,
    ) async {
      final c = await _pumpApp(tester, seed: MockSeed.firstRun);
      expect(
        await c.createWorkspace(name: 'first', kind: WorkspaceKind.managed),
        isNull,
      );
      await tester.pumpAndSettle();
      expect(find.text('尚未连接ChatGPT 账号'), findsOneWidget);
      expect(find.text('ChatGPT 账号登录已失效'), findsNothing);
      expect(find.text('连接…'), findsOneWidget);
    });
  });

  group('terminal', () {
    testWidgets('input is not sent before the terminal is ready', (
      tester,
    ) async {
      final c = await _pumpApp(tester);
      c.attachTerminalToSelectedSandbox(holdAtWaitingForReadyFrame: true);
      // The connecting spinner never settles; step frames instead.
      await tester.pump();
      await tester.pump();
      expect(c.terminalPhase, TerminalConnectionPhase.connectingWs);
      expect(c.sendTerminalInput('env'), isFalse);

      await tester.tap(_key('terminal-complete-ready-frame-button'));
      await tester.pumpAndSettle();
      expect(c.terminalPhase, TerminalConnectionPhase.ready);
      expect(c.sendTerminalInput('env'), isTrue);
    });

    testWidgets('keys typed in the terminal reach the agent', (tester) async {
      final c = await _pumpApp(tester);
      final screen = c.selectedTerminal!;
      await tester.tap(_key('terminal-view'));
      // Outlasts the emulator's double-tap window.
      await tester.pump(const Duration(milliseconds: 400));
      final before = screen.buffer.getText();
      await tester.sendKeyEvent(LogicalKeyboardKey.enter);
      await tester.pumpAndSettle();
      expect(screen.buffer.getText(), isNot(before));
    });

    testWidgets(
      'when the agent exits, the terminal says so and offers a stop',
      (tester) async {
        final c = await _pumpApp(tester);
        expect(c.sendTerminalInput('exit\r'), isTrue);
        await tester.pumpAndSettle();
        expect(c.terminalPhase, TerminalConnectionPhase.exited);
        expect(c.terminalExitCode, 0);
        expect(_key('terminal-exited-alert'), findsOneWidget);
        expect(c.sendTerminalInput('env\r'), isFalse);

        await tester.tap(_key('terminal-exited-stop-button'));
        await tester.pumpAndSettle();
        expect(c.selectedSandbox!.status, SandboxStatus.stopped);
      },
    );

    testWidgets(
      'reconnecting returns to the same terminal and reports the gap',
      (tester) async {
        final c = await _pumpApp(tester);
        final before = c.activeTerminalTicket!;
        final screen = c.selectedTerminal!;
        c.drillTransientTerminalReconnect();
        await tester.pumpAndSettle();
        final after = c.activeTerminalTicket!;
        expect(after.ticket, isNot(before.ticket));
        // Core keeps no scrollback: the same emulator stays on screen and
        // marks where output may be missing.
        expect(c.selectedTerminal, same(screen));
        expect(screen.buffer.getText(), contains('已重新连接'));
        expect(_key('terminal-reconnected-gap-alert'), findsOneWidget);
      },
    );

    testWidgets('another window taking over offers to continue here', (
      tester,
    ) async {
      final c = await _pumpApp(tester);
      c.drillTerminalReplacedByAnotherClient();
      await tester.pumpAndSettle();
      expect(c.terminalPhase, TerminalConnectionPhase.replaced);

      await tester.tap(_key('terminal-reclaim-button'));
      await tester.pumpAndSettle();
      expect(c.terminalPhase, TerminalConnectionPhase.ready);
    });

    testWidgets('closing the terminal does not stop the sandbox', (
      tester,
    ) async {
      final c = await _pumpApp(tester);
      await tester.tap(_key('session-more-button'));
      await tester.pumpAndSettle();
      await tester.tap(_key('menu-detach-terminal'));
      await tester.pumpAndSettle();
      expect(c.terminalPhase, TerminalConnectionPhase.closed);
      expect(c.selectedSandbox!.status, SandboxStatus.running);

      await tester.tap(_key('terminal-reconnect-button'));
      await tester.pumpAndSettle();
      expect(c.terminalPhase, TerminalConnectionPhase.ready);
    });

    testWidgets(
      'after a daemon restart, one button relaunches the same setup',
      (tester) async {
        final c = await _pumpApp(tester);
        final before = c.selectedSandbox!;
        c.drillDaemonRestartInterruption();
        await tester.pumpAndSettle();
        expect(c.selectedSandbox!.status, SandboxStatus.stopped);
        expect(_key('daemon-interrupted-panel'), findsOneWidget);
        expect(_key('session-start-button'), findsNothing);

        await tester.tap(_key('daemon-restart-relaunch-button'));
        await tester.pumpAndSettle();
        final after = c.selectedSandbox!;
        expect(after.id, isNot(before.id));
        expect(after.status, SandboxStatus.running);
        expect(after.networkMode, before.networkMode);
        expect(after.stateId, before.stateId);
        expect(c.terminalPhase, TerminalConnectionPhase.ready);
      },
    );
  });

  group('conflicts and safety', () {
    testWidgets('occupied session data names the holder and never stops it', (
      tester,
    ) async {
      final c = await _pumpApp(tester);
      await c.drillStateLockedConflict();
      await tester.pumpAndSettle();
      expect(c.selectedWorkspaceId, 'ws-ext-gateway');
      expect(_key('state-locked-conflict-alert'), findsOneWidget);
      expect(
        find.descendant(
          of: _key('state-locked-conflict-alert'),
          matching: find.textContaining('nullkey-runtime · Codex'),
        ),
        findsWidgets,
      );
      expect(
        c.api.getSandbox('sbx-codex-primary')!.status,
        SandboxStatus.running,
      );

      await tester.tap(_key('state-locked-jump-button'));
      await tester.pumpAndSettle();
      expect(c.selectedWorkspaceId, 'ws-nullkey-core');
      expect(c.selectedSandbox!.status, SandboxStatus.running);
    });

    testWidgets(
      'a blocked request names the target and keeps the network mode',
      (tester) async {
        final c = await _pumpApp(tester);
        c.drillOutboundNetworkDenied(host: 'evil.telemetry.io');
        await tester.pumpAndSettle();
        expect(_key('network-denied-alert'), findsOneWidget);
        expect(find.textContaining('evil.telemetry.io'), findsOneWidget);
        expect(
          find.textContaining(NetworkMode.servicesOnly.label),
          findsWidgets,
        );
        expect(c.selectedSandbox!.networkMode, NetworkMode.servicesOnly);
      },
    );

    testWidgets('a failed secret scan never reads as clean', (tester) async {
      final c = await _pumpApp(tester);
      final result = await c.runSecretScanOnSelectedSandbox();
      await tester.pumpAndSettle();
      expect(result!.scope, 'generated_runtime');
      expect(_key('secret-scan-result-alert'), findsOneWidget);
      expect(find.text(result.headline), findsOneWidget);

      c.drillNextScanFails();
      expect(await c.runSecretScanOnSelectedSandbox(), isNull);
      await tester.pumpAndSettle();
      expect(_key('secret-scan-result-alert'), findsNothing);
      expect(_key('secret-scan-error-alert'), findsOneWidget);
      expect(find.textContaining('不代表没有发现凭证'), findsOneWidget);
    });

    testWidgets('the desktop tab follows the daemon capability', (
      tester,
    ) async {
      final c = await _pumpApp(tester);
      expect(_key('session-tab-desktop'), findsOneWidget);
      c.toggleDesktopCapability(false);
      await tester.pumpAndSettle();
      expect(_key('session-tab-desktop'), findsNothing);
      expect(c.availableTabs, isNot(contains(SessionTab.desktop)));
    });

    testWidgets('a workspace in use cannot be deleted from its dialog', (
      tester,
    ) async {
      final c = await _pumpApp(tester);
      await tester.tap(_key('session-more-button'));
      await tester.pumpAndSettle();
      await tester.tap(_key('menu-remove-workspace'));
      await tester.pumpAndSettle();
      expect(_key('remove-workspace-in-use-alert'), findsOneWidget);
      expect(
        find.textContaining(c.selectedWorkspace!.normalizedPath),
        findsOneWidget,
      );
      expect(_onPress(tester, 'remove-workspace-submit'), isNull);
    });

    test('deletion rules: external folders are never deleted', () async {
      final c = _controller();
      expect(
        await c.removeWorkspace(
          workspaceId: 'ws-ext-audit-ro',
          deleteFiles: true,
          confirmName: 'security-policies-ro',
        ),
        isNotNull,
      );
      expect(c.workspaceById('ws-ext-audit-ro'), isNotNull);

      expect(
        await c.removeWorkspace(
          workspaceId: 'ws-ext-audit-ro',
          deleteFiles: false,
        ),
        isNull,
      );
      expect(c.workspaceById('ws-ext-audit-ro'), isNull);
    });

    test(
      'deletion rules: a managed workspace needs a stop and its name',
      () async {
        final c = _controller();
        Future<String?> delete(String name) => c.removeWorkspace(
          workspaceId: 'ws-nullkey-core',
          deleteFiles: true,
          confirmName: name,
        );

        expect(await delete('nullkey-runtime'), isNotNull);
        await c.stopSelectedSandbox();
        expect(await delete('wrong-name'), isNotNull);
        expect(c.workspaceById('ws-nullkey-core'), isNotNull);
        expect(await delete('nullkey-runtime'), isNull);
        expect(c.workspaceById('ws-nullkey-core'), isNull);
      },
    );
  });

  group('lock', () {
    testWidgets('unlocking needs the system confirmation to succeed', (
      tester,
    ) async {
      final c = await _pumpApp(tester);
      c.lockStudio();
      await tester.pumpAndSettle();
      expect(_key('locked-gate-unlock-button'), findsOneWidget);
      expect(_key('session-header-workspace'), findsNothing);

      c.passkey.simulateNextCancellation = true;
      await tester.tap(_key('locked-gate-unlock-button'));
      await tester.pumpAndSettle();
      expect(c.studioUnlocked, isFalse);
      expect(_key('locked-gate-message'), findsOneWidget);

      await tester.tap(_key('locked-gate-unlock-button'));
      await tester.pumpAndSettle();
      expect(c.studioUnlocked, isTrue);
      expect(_key('session-header-workspace'), findsOneWidget);
    });
  });
}
