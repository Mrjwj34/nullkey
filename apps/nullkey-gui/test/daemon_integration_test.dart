import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:nullkey_gui/models/nullkey_models.dart';
import 'package:nullkey_gui/services/daemon_backend.dart';
import 'package:nullkey_gui/services/nullkey_backend.dart';

Future<void> _until(bool Function() predicate) async {
  final deadline = DateTime.now().add(const Duration(seconds: 10));
  while (!predicate()) {
    if (DateTime.now().isAfter(deadline)) {
      fail('Timed out waiting for daemon state');
    }
    await Future<void>.delayed(const Duration(milliseconds: 25));
  }
}

void main() {
  final executable = Platform.environment['NULLKEY_TEST_DAEMON'];

  test(
    'real daemon: installation, credentials, workspace, terminal and restart',
    () async {
      final root = await Directory.systemTemp.createTemp(
        'nullkey-gui-integration-',
      );
      final home = Directory('${root.path}/daemon')..createSync();
      final bin = Directory('${root.path}/bin')..createSync();
      final imports = Directory('${root.path}/host-import')..createSync();
      final reservations = <ServerSocket>[];
      for (var i = 0; i < 3; i++) {
        reservations.add(
          await ServerSocket.bind(InternetAddress.loopbackIPv4, 0),
        );
      }
      final ports = reservations.map((s) => s.port).toList();
      for (final socket in reservations) {
        await socket.close();
      }
      final daemon = await Process.start(
        executable!,
        [],
        environment: {
          'PATH': '${bin.path}:/usr/bin:/bin',
          'HOME': root.path,
          'TMPDIR': root.path,
          'NULLKEY_HOME': home.path,
          'NULLKEY_HOST_IMPORT_ROOT': imports.path,
          'NULLKEY_SECRET_BACKEND': 'file',
          'NULLKEY_LISTEN': '127.0.0.1:${ports[0]}',
          'NULLKEY_GATEWAY_LISTEN': '127.0.0.1:${ports[1]}',
          'NULLKEY_PROXY_LISTEN': '127.0.0.1:${ports[2]}',
        },
        includeParentEnvironment: false,
      );
      final stdoutDone = daemon.stdout.drain<void>();
      final stderrDone = daemon.stderr.drain<void>();
      addTearDown(() async {
        daemon.kill(ProcessSignal.sigterm);
        await daemon.exitCode.timeout(const Duration(seconds: 10));
        await Future.wait([stdoutDone, stderrDone]);
        await root.delete(recursive: true);
      });
      final tokenFile = File('${home.path}/daemon-token');
      await _until(() => tokenFile.existsSync() && tokenFile.lengthSync() > 0);
      final backend = DaemonBackend(
        baseUrl: Uri.parse('http://127.0.0.1:${ports[0]}'),
        token: tokenFile.readAsStringSync().trim(),
      );
      addTearDown(backend.dispose);
      await backend.loaded;
      await _until(() => backend.connectionProblem == null);

      expect(
        backend.getHarness(HarnessKind.codex)!.status,
        HarnessStatus.notInstalled,
      );
      final missing = await backend.startHarnessInstall(HarnessKind.codex);
      await _until(
        () => backend.getJob(missing.id)?.status == JobStatus.failed,
      );
      await _until(
        () =>
            backend.getHarness(HarnessKind.codex)!.status !=
            HarnessStatus.installing,
      );
      expect(
        backend.getHarness(HarnessKind.codex)!.status,
        HarnessStatus.notInstalled,
      );
      expect(backend.getJob(missing.id)!.error, contains('宿主机安装'));
      final fakeCodex = File('${bin.path}/codex')
        ..writeAsStringSync('#!/bin/sh\nexit 0\n');
      final chmod = await Process.run('/bin/chmod', ['700', fakeCodex.path]);
      expect(chmod.exitCode, 0);
      final installed = await backend.startHarnessInstall(HarnessKind.codex);
      await _until(
        () => backend.getJob(installed.id)?.status == JobStatus.succeeded,
      );
      await _until(
        () =>
            backend.getHarness(HarnessKind.codex)!.status ==
            HarnessStatus.installed,
      );

      final credential = await backend.createCredential(
        name: 'integration fake key',
        provider: 'openai',
        kind: CredentialKind.apiKey,
        secretValue: 'sk-test-integration-not-a-real-key',
      );
      expect(credential.status, CredentialStatus.active);
      await backend.deleteCredential(credential.id);
      expect(backend.listCredentials(), isEmpty);

      final workspace = await backend.createWorkspace(
        name: 'integration',
        kind: WorkspaceKind.managed,
      );
      final sandbox = await backend.createSandbox(
        harness: HarnessKind.custom,
        workspaceId: workspace.id,
        workspaceMode: MountMode.rw,
      );
      expect(sandbox.status, SandboxStatus.running);
      final terminal = await backend.connectTerminal(
        await backend.createTerminalTicket(sandbox.id),
      );
      addTearDown(terminal.close);
      final output = StringBuffer();
      final subscription = terminal.events.listen((event) {
        if (event is TerminalOutput) output.write(event.data);
      });
      addTearDown(subscription.cancel);
      await terminal.ready;
      terminal.sendInput(
        "printf 'integration artifact\\n' > result.txt; printf 'GUI_%s\\n' done\n",
      );
      await _until(() => output.toString().contains('GUI_done'));
      await backend.refreshArtifacts(sandbox.id);
      final artifact = backend.listArtifacts(sandbox.id).single;
      expect(artifact.relativePath, 'result.txt');
      expect(artifact.previewSnippet, contains('integration artifact'));
      final scan = await backend.scanSandboxSecrets(sandbox.id);
      expect(scan.clean, isTrue);
      expect(scan.scope, 'generated_runtime');
      expect(scan.excluded, contains('workspace'));

      // Closing the GUI connection must not terminate the shell.
      await terminal.close();
      final resumed = await backend.connectTerminal(
        await backend.createTerminalTicket(sandbox.id),
      );
      addTearDown(resumed.close);
      final resumedOutput = StringBuffer();
      final resumedSubscription = resumed.events.listen((event) {
        if (event is TerminalOutput) resumedOutput.write(event.data);
      });
      addTearDown(resumedSubscription.cancel);
      await resumed.ready;
      resumed.sendInput("cat result.txt; printf 'RESUMED_%s\\n' done\n");
      await _until(() => resumedOutput.toString().contains('RESUMED_done'));
      expect(resumedOutput.toString(), contains('integration artifact'));
      await backend.stopSandbox(sandbox.id);
      expect(backend.getSandbox(sandbox.id)!.status, SandboxStatus.stopped);
      final restarted = await backend.createSandbox(
        harness: HarnessKind.custom,
        workspaceId: workspace.id,
        workspaceMode: MountMode.rw,
        stateId: sandbox.stateId,
      );
      expect(restarted.stateId, sandbox.stateId);
      await backend.refreshArtifacts(restarted.id);
      expect(
        backend.listArtifacts(restarted.id).single.previewSnippet,
        contains('integration artifact'),
      );
      await backend.stopSandbox(restarted.id);
      await backend.deleteWorkspace(
        workspace.id,
        deleteFiles: true,
        confirmName: workspace.name,
      );
      expect(backend.listWorkspaces(), isEmpty);
    },
    skip: executable == null
        ? 'Set NULLKEY_TEST_DAEMON to a built nullkeyd (CI runs this).'
        : false,
    timeout: const Timeout(Duration(seconds: 60)),
  );
}
