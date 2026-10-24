import 'dart:io';

import 'package:forui/forui.dart';
import 'package:material_ui/material_ui.dart';

import 'services/core_mock_backend.dart';
import 'services/daemon_backend.dart';
import 'services/daemon_connection.dart';
import 'services/nullkey_backend.dart';
import 'state/nullkey_studio_controller.dart';
import 'theme/theme.dart';
import 'widgets/app_shell.dart';

void main() =>
    runApp(NullkeyApp(controller: NullkeyStudioController(api: _backend())));

/// Connects to nullkeyd using `NULLKEY_DAEMON_URL`/`NULLKEY_DAEMON_TOKEN`
/// when given (`--dart-define` or environment), otherwise the token the
/// daemon wrote to `<NULLKEY_HOME>/daemon-token`. With no token at all the app
/// runs on Core Mock, paced like a real daemon so launch stages, installs and
/// browser sign-in stay visible.
NullkeyBackend _backend() {
  String? setting(String name, String defined) {
    final value = defined.isNotEmpty ? defined : Platform.environment[name];
    return value == null || value.trim().isEmpty ? null : value.trim();
  }

  final connection = resolveDaemonConnection(
    url: setting(
      'NULLKEY_DAEMON_URL',
      const String.fromEnvironment('NULLKEY_DAEMON_URL'),
    ),
    token: setting(
      'NULLKEY_DAEMON_TOKEN',
      const String.fromEnvironment('NULLKEY_DAEMON_TOKEN'),
    ),
    fileToken: readDaemonToken(),
  );
  if (connection == null) {
    return CoreMockBackend(simulatedLatency: const Duration(milliseconds: 450));
  }
  return DaemonBackend(baseUrl: connection.baseUrl, token: connection.token);
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
