import 'dart:async';
import 'dart:io';

import 'package:forui/forui.dart';
import 'package:material_ui/material_ui.dart';

import 'services/core_mock_backend.dart';
import 'services/daemon_backend.dart';
import 'services/daemon_connection.dart';
import 'services/daemon_launcher.dart';
import 'services/nullkey_backend.dart';
import 'state/nullkey_studio_controller.dart';
import 'theme/theme.dart';
import 'widgets/app_shell.dart';

void main() {
  // The controller queries native authentication before runApp builds the UI.
  WidgetsFlutterBinding.ensureInitialized();
  if (_setting('NULLKEY_DEMO', const String.fromEnvironment('NULLKEY_DEMO')) !=
      '1') {
    // The GUI waits for the token file and reconnects on its own, so launching
    // the daemon does not block the first frame.
    final launchEnvironment = Map<String, String>.of(Platform.environment);
    final definedToken = _setting(
      'NULLKEY_DAEMON_TOKEN',
      const String.fromEnvironment('NULLKEY_DAEMON_TOKEN'),
    );
    final definedUrl = _setting(
      'NULLKEY_DAEMON_URL',
      const String.fromEnvironment('NULLKEY_DAEMON_URL'),
    );
    if (definedToken != null) {
      launchEnvironment['NULLKEY_DAEMON_TOKEN'] = definedToken;
    }
    if (definedUrl != null) {
      launchEnvironment['NULLKEY_DAEMON_URL'] = definedUrl;
    }
    unawaited(DaemonLauncher(environment: launchEnvironment).ensureRunning());
  }
  runApp(NullkeyApp(controller: NullkeyStudioController(api: _backend())));
}

/// Connects to nullkeyd using `NULLKEY_DAEMON_URL`/`NULLKEY_DAEMON_TOKEN`
/// when given (`--dart-define` or environment), otherwise the token the
/// daemon wrote to `<NULLKEY_HOME>/daemon-token`. A missing token keeps the
/// app disconnected until the daemon starts. Demos must be selected explicitly.
NullkeyBackend _backend() {
  if (_setting('NULLKEY_DEMO', const String.fromEnvironment('NULLKEY_DEMO')) ==
      '1') {
    return CoreMockBackend(simulatedLatency: const Duration(milliseconds: 450));
  }
  final explicitToken = _setting(
    'NULLKEY_DAEMON_TOKEN',
    const String.fromEnvironment('NULLKEY_DAEMON_TOKEN'),
  );
  final connection = resolveDaemonConnection(
    url: _setting(
      'NULLKEY_DAEMON_URL',
      const String.fromEnvironment('NULLKEY_DAEMON_URL'),
    ),
    token: explicitToken,
    fileToken: readDaemonToken(),
  );
  return DaemonBackend(
    baseUrl: connection?.baseUrl ?? Uri.parse(defaultDaemonUrl),
    token: connection?.token ?? '',
    tokenLoader: explicitToken == null ? readDaemonToken : null,
  );
}

/// A `--dart-define` wins over the process environment. Empty values mean
/// "not set".
String? _setting(String name, String defined) {
  final value = defined.isNotEmpty ? defined : Platform.environment[name];
  return value == null || value.trim().isEmpty ? null : value.trim();
}

class NullkeyApp extends StatefulWidget {
  final NullkeyStudioController? controller;

  const NullkeyApp({this.controller, super.key});

  @override
  State<NullkeyApp> createState() => _NullkeyAppState();
}

class _NullkeyAppState extends State<NullkeyApp> {
  late final NullkeyStudioController _controller =
      widget.controller ?? NullkeyStudioController();

  static final Map<(Brightness, bool), FThemeData> _themes = {};

  static FThemeData _theme(
    Brightness brightness, {
    required bool highContrast,
  }) => _themes.putIfAbsent((
    brightness,
    highContrast,
  ), () => buildTheme(brightness, highContrast: highContrast));

  @override
  void dispose() {
    if (widget.controller == null) {
      _controller.dispose();
    }
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => MaterialApp(
    title: 'Nullkey',
    debugShowCheckedModeBanner: false,
    localizationsDelegates: FLocalizations.localizationsDelegates,
    supportedLocales: FLocalizations.supportedLocales,
    theme: lightTheme.toApproximateMaterialTheme(),
    darkTheme: darkTheme.toApproximateMaterialTheme(),
    builder: (context, child) {
      final media = MediaQuery.of(context);
      return FTheme(
        data: _theme(
          media.platformBrightness,
          highContrast: media.highContrast,
        ),
        child: FTooltipGroup(child: child!),
      );
    },
    home: AppShell(controller: _controller),
  );
}
