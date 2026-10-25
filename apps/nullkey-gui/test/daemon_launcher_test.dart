import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:nullkey_gui/services/daemon_launcher.dart';

void main() {
  const app = '/Applications/Nullkey.app/Contents/MacOS/Nullkey';
  const bundled = '/Applications/Nullkey.app/Contents/Resources/nullkeyd';

  group('DaemonLauncher', () {
    test('stays out of the way when autostart is disabled', () async {
      final outcome = await DaemonLauncher(
        environment: const {'NULLKEY_DISABLE_AUTOSTART': '1'},
        resolvedExecutable: app,
        healthProbe: (_) async => fail('must not probe'),
      ).ensureRunning();
      expect(outcome, DaemonStartOutcome.disabled);
    });

    test('never competes with an explicitly configured daemon', () async {
      final outcome = await DaemonLauncher(
        environment: const {'NULLKEY_DAEMON_TOKEN': 'test-token'},
        resolvedExecutable: app,
        healthProbe: (_) async => fail('must not probe'),
      ).ensureRunning();
      expect(outcome, DaemonStartOutcome.external);
    });

    test('leaves a healthy daemon alone', () async {
      var started = false;
      final outcome = await DaemonLauncher(
        environment: const {},
        resolvedExecutable: app,
        healthProbe: (_) async => true,
        starter: (_, _, _) async {
          started = true;
        },
      ).ensureRunning();
      expect(outcome, DaemonStartOutcome.healthy);
      expect(started, isFalse);
    });

    test('reports a missing bundled daemon instead of guessing', () async {
      final outcome = await DaemonLauncher(
        environment: const {},
        resolvedExecutable: app,
        healthProbe: (_) async => false,
        pathExists: (_) => false,
      ).ensureRunning();
      expect(outcome, DaemonStartOutcome.missingExecutable);
    });

    test('starts the bundled daemon with a clean token environment', () async {
      String? executable;
      List<String>? arguments;
      Map<String, String>? environment;
      final outcome = await DaemonLauncher(
        environment: const {
          'NULLKEY_HOME': '/tmp/nullkey-test',
          'NULLKEY_TOKEN_FILE': '/tmp/nullkey-token',
        },
        resolvedExecutable: app,
        healthProbe: (_) async => false,
        pathExists: (path) => path == bundled,
        starter: (exe, args, env) async {
          executable = exe;
          arguments = args;
          environment = env;
        },
      ).ensureRunning();

      expect(outcome, DaemonStartOutcome.started);
      expect(executable, bundled);
      expect(arguments, ['--listen', '127.0.0.1:49152']);
      expect(environment!['NULLKEY_HOME'], '/tmp/nullkey-test');
      expect(environment!.containsKey('NULLKEY_TOKEN_FILE'), isFalse);
      expect(environment!.containsKey('NULLKEY_DAEMON_TOKEN'), isFalse);
    });

    test('uses the configured URL and daemon path', () async {
      List<String>? arguments;
      final outcome = await DaemonLauncher(
        environment: const {
          'NULLKEY_DAEMON_URL': 'http://127.0.0.1:50000',
          'NULLKEY_DAEMON_PATH': '/opt/nullkeyd',
        },
        resolvedExecutable: app,
        healthProbe: (uri) async {
          expect(uri.host, '127.0.0.1');
          expect(uri.port, 50000);
          return false;
        },
        pathExists: (path) => path == '/opt/nullkeyd',
        starter: (_, args, _) async {
          arguments = args;
        },
      ).ensureRunning();

      expect(outcome, DaemonStartOutcome.started);
      expect(arguments, ['--listen', '127.0.0.1:50000']);
    });

    test('refuses to start a daemon for a non-loopback URL', () async {
      final outcome = await DaemonLauncher(
        environment: const {'NULLKEY_DAEMON_URL': 'https://example.test'},
        resolvedExecutable: app,
        healthProbe: (_) async => false,
        pathExists: (_) => true,
      ).ensureRunning();
      expect(outcome, DaemonStartOutcome.unsupported);
    });

    test('reports a failed spawn so the UI can keep waiting', () async {
      final outcome = await DaemonLauncher(
        environment: const {},
        resolvedExecutable: app,
        healthProbe: (_) async => false,
        pathExists: (path) => path == bundled,
        starter: (_, _, _) async => throw ProcessException('nullkeyd', const []),
      ).ensureRunning();
      expect(outcome, DaemonStartOutcome.failed);
    });
  });
}
