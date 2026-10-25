import 'dart:io';

import 'daemon_connection.dart';

typedef DaemonHealthProbe = Future<bool> Function(Uri baseUrl);
typedef DaemonStarter =
    Future<void> Function(
      String executable,
      List<String> arguments,
      Map<String, String> environment,
    );
typedef PathExists = bool Function(String path);

enum DaemonStartOutcome {
  disabled,
  external,
  healthy,
  missingExecutable,
  unsupported,
  started,
  failed,
}

/// Starts the daemon bundled next to the app when the GUI has nothing to talk
/// to. The daemon is detached: quitting the GUI must not stop running sandboxes.
///
/// The release DMG puts `nullkeyd` in `Nullkey.app/Contents/Resources`. In a
/// development checkout set `NULLKEY_DAEMON_PATH` to `bin/nullkeyd`, or keep
/// starting the daemon by hand.
class DaemonLauncher {
  final Map<String, String> environment;
  final String resolvedExecutable;
  final DaemonHealthProbe? healthProbe;
  final DaemonStarter? starter;
  final PathExists? pathExists;

  DaemonLauncher({
    Map<String, String>? environment,
    String? resolvedExecutable,
    this.healthProbe,
    this.starter,
    this.pathExists,
  }) : environment = environment ?? Platform.environment,
       resolvedExecutable =
           resolvedExecutable ?? Platform.resolvedExecutable;

  Future<DaemonStartOutcome> ensureRunning() async {
    if (_flag(environment['NULLKEY_DISABLE_AUTOSTART'])) {
      return DaemonStartOutcome.disabled;
    }
    // An explicit token means the operator owns the daemon. Never race them.
    if (_clean(environment['NULLKEY_DAEMON_TOKEN']) != null) {
      return DaemonStartOutcome.external;
    }

    final base = _baseUri();
    final probe = healthProbe ?? _probeHealth;
    if (await probe(base)) {
      return DaemonStartOutcome.healthy;
    }
    if (base.scheme != 'http' || !_isLoopback(base.host)) {
      return DaemonStartOutcome.unsupported;
    }

    final executable = _daemonExecutable();
    if (executable == null) {
      return DaemonStartOutcome.missingExecutable;
    }
    final childEnvironment = Map<String, String>.of(environment)
      // The bundled daemon must write <NULLKEY_HOME>/daemon-token; the GUI
      // discovers that file. A token in the environment would hide it.
      ..remove('NULLKEY_DAEMON_TOKEN')
      ..remove('NULLKEY_TOKEN_FILE');
    try {
      await (starter ?? _startDetached)(
        executable,
        ['--listen', _listenAddress(base)],
        childEnvironment,
      );
    } on Object {
      return DaemonStartOutcome.failed;
    }
    return DaemonStartOutcome.started;
  }

  Uri _baseUri() {
    final resolved = resolveDaemonConnection(
      url: environment['NULLKEY_DAEMON_URL'],
    );
    return resolved?.baseUrl ?? Uri.parse(defaultDaemonUrl);
  }

  String? _daemonExecutable() {
    final exists = pathExists ?? (path) => File(path).existsSync();
    final configured = _clean(environment['NULLKEY_DAEMON_PATH']);
    if (configured != null) {
      return exists(configured) ? configured : null;
    }
    final bundle = File(resolvedExecutable).parent.parent.path;
    final bundled = '$bundle/Resources/nullkeyd';
    return exists(bundled) ? bundled : null;
  }

  static Future<bool> _probeHealth(Uri base) async {
    final client = HttpClient()
      ..connectionTimeout = const Duration(seconds: 1);
    try {
      final uri = base.replace(
        path: '/api/v1/health',
        query: null,
        fragment: null,
      );
      final request = await client
          .getUrl(uri)
          .timeout(const Duration(seconds: 2));
      final response = await request.close().timeout(const Duration(seconds: 2));
      await response.drain<void>();
      return response.statusCode == 200;
    } on Object {
      return false;
    } finally {
      client.close(force: true);
    }
  }

  static Future<void> _startDetached(
    String executable,
    List<String> arguments,
    Map<String, String> environment,
  ) async {
    await Process.start(
      executable,
      arguments,
      mode: ProcessStartMode.detached,
      environment: environment,
      workingDirectory: _clean(environment['HOME']),
    );
  }

  static String _listenAddress(Uri base) {
    final host = base.host.contains(':') ? '[${base.host}]' : base.host;
    return '$host:${base.port}';
  }

  static bool _isLoopback(String host) =>
      host == 'localhost' || host == '127.0.0.1' || host == '::1';

  static bool _flag(String? value) {
    final normalized = value?.trim().toLowerCase();
    return normalized == '1' || normalized == 'true' || normalized == 'yes';
  }

  static String? _clean(String? value) {
    final trimmed = value?.trim();
    return trimmed == null || trimmed.isEmpty ? null : trimmed;
  }
}
