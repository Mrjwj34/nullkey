import 'dart:io';

/// Where the GUI should connect and with which token.
class DaemonConnection {
  final Uri baseUrl;
  final String token;

  const DaemonConnection({required this.baseUrl, required this.token});
}

/// Default control-plane address; the daemon only ever binds loopback.
const defaultDaemonUrl = 'http://127.0.0.1:49152';

/// Resolves the connection without touching the filesystem, so the precedence
/// rules stay testable: an explicit url/token pair wins, otherwise the token
/// the daemon wrote for this host is used with [defaultDaemonUrl]. Returns null
/// when nothing is configured, which keeps the app on Core Mock.
DaemonConnection? resolveDaemonConnection({
  String? url,
  String? token,
  String? fileToken,
}) {
  final explicitUrl = _clean(url);
  final explicitToken = _clean(token);
  final discovered = _clean(fileToken);
  final effectiveToken = explicitToken ?? discovered;
  if (effectiveToken == null) {
    return null;
  }
  final target = Uri.tryParse(explicitUrl ?? defaultDaemonUrl);
  if (target == null || !target.hasScheme || target.host.isEmpty) {
    return null;
  }
  return DaemonConnection(baseUrl: target, token: effectiveToken);
}

/// Path of the token file the daemon writes when `NULLKEY_DAEMON_TOKEN` is not
/// set. Mirrors the daemon's own home resolution.
String? daemonTokenPath({Map<String, String>? environment}) {
  final env = environment ?? Platform.environment;
  final home = _clean(env['NULLKEY_HOME']);
  if (home != null) {
    return '$home/daemon-token';
  }
  final userHome = _clean(env['HOME']);
  if (userHome == null) {
    return null;
  }
  return '$userHome/Library/Application Support/Nullkey/daemon-token';
}

/// Reads the daemon token file, returning null when it is missing or empty.
String? readDaemonToken({Map<String, String>? environment}) {
  final path = daemonTokenPath(environment: environment);
  if (path == null) {
    return null;
  }
  try {
    return _clean(File(path).readAsStringSync());
  } on FileSystemException {
    return null;
  }
}

String? _clean(String? value) {
  if (value == null) {
    return null;
  }
  final trimmed = value.trim();
  return trimmed.isEmpty ? null : trimmed;
}
