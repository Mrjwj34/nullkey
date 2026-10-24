import 'dart:async';

import '../models/nullkey_models.dart';

/// What the GUI needs from Nullkey Core.
///
/// Reads return the latest known state synchronously so widgets can build
/// from them; [changes] fires whenever that state moves without a call from
/// the GUI. Every call that talks to Core is asynchronous and fails with a
/// [NullkeyApiException] whose message can be shown to the user as is.
///
/// Two implementations exist: `DaemonBackend` talks to a running `nullkeyd`,
/// `CoreMockBackend` is an in-memory stand-in for demos and tests.
abstract class NullkeyBackend {
  DaemonCapabilities get capabilities;

  /// Why Core cannot be reached, in words for the user; null while
  /// connected. Reads then show the last state Core reported.
  String? get connectionProblem;

  /// Completes once the first snapshot from Core arrived, or the first
  /// attempt to get one failed ([connectionProblem] then says why).
  Future<void> get loaded;

  Stream<void> get changes;

  void dispose();

  // Harnesses & install jobs -------------------------------------------------

  List<HarnessInfo> listHarnesses();
  HarnessInfo? getHarness(HarnessKind kind);

  /// Jobs this GUI started or observed, newest first.
  List<JobInfo> listJobs();
  JobInfo? getJob(String id);

  Future<JobInfo> startHarnessInstall(
    HarnessKind kind, {
    bool isUpdate = false,
  });

  /// Moves the job to `cancelling`; it reports `cancelled` after cleanup.
  /// Fails with `conflict` when the job already finished.
  Future<JobInfo> requestCancelJob(String jobId);

  // Credentials & sign-in ----------------------------------------------------

  List<CredentialMetadata> listCredentials();
  List<ImportableCredentialCandidate> listImportableCredentials();

  /// Browser sign-ins this GUI started, newest first.
  List<OAuthPendingFlow> listOAuthFlows();

  Future<CredentialMetadata> createCredential({
    required String name,
    required String provider,
    required CredentialKind kind,
    required String secretValue,
  });

  Future<void> deleteCredential(String id);

  Future<ImportCredentialsResult> importHostCredentials(
    List<String> candidateIds,
  );

  Future<OAuthPendingFlow> startOAuthFlow({required String provider});

  /// Fails with `conflict` when the browser already finished; the account
  /// is then connected.
  Future<OAuthPendingFlow> cancelOAuthFlow(String flowId);

  List<McpServerRecord> listMcpServers();

  // Workspaces & session data -------------------------------------------------

  List<WorkspaceRecord> listWorkspaces();

  Future<WorkspaceRecord> createWorkspace({
    required String name,
    required WorkspaceKind kind,
    String? hostPath,
    MountMode defaultMode = MountMode.rw,
  });

  Future<void> deleteWorkspace(
    String id, {
    bool deleteFiles = false,
    String? confirmName,
  });

  List<PersistentStateRecord> listStates();
  Future<PersistentStateRecord> resetState(String stateId);

  // Sandboxes -----------------------------------------------------------------

  /// Newest first.
  List<SandboxRecord> listSandboxes();
  SandboxRecord? getSandbox(String id);

  /// Fails with `state_locked` (details carry `sandbox_id`, and `state_id`
  /// when session data is the contested resource) when another sandbox
  /// holds the workspace or session data.
  Future<SandboxRecord> createSandbox({
    required HarnessKind harness,
    required String workspaceId,
    required MountMode workspaceMode,
    String? stateId,
    NetworkMode networkMode = NetworkMode.filtered,
    bool enableDesktop = false,
  });

  /// Resolves once the sandbox left its starting states, with its record
  /// then, or null when it no longer exists.
  Future<SandboxRecord?> waitUntilRunning(String sandboxId);

  Future<SandboxRecord> stopSandbox(String sandboxId);

  Future<TerminalTicket> createTerminalTicket(String sandboxId);

  /// Opens the WebSocket for [ticket]. Input is accepted only after
  /// [TerminalConnection.ready] resolves.
  Future<TerminalConnection> connectTerminal(TerminalTicket ticket);

  /// Services [sandboxId] declared, as last loaded by [refreshServices].
  List<SandboxServiceRecord> listServices(String sandboxId);
  Future<void> refreshServices(String sandboxId);

  /// A single-use address that opens [serviceId] in the browser.
  Future<Uri> openService(String sandboxId, String serviceId);

  DesktopStatusRecord getDesktopStatus(String sandboxId);
  Future<DesktopStatusRecord> setDesktopEnabled(String sandboxId, bool enabled);

  /// Files the agent changed in the sandbox's workspace, as last loaded by
  /// [refreshArtifacts].
  List<ArtifactRecord> listArtifacts(String sandboxId);
  Future<void> refreshArtifacts(String sandboxId);

  /// Checks only what Nullkey generated; a failed check throws, it never
  /// reads as zero findings.
  Future<SecretScanResult> scanSandboxSecrets(String sandboxId);

  // Activity -----------------------------------------------------------------

  /// Newest first.
  List<AuditEventRecord> listAuditEvents();
  Future<void> refreshAuditEvents();
}

/// Something the sandbox's PTY reported after `ready`.
sealed class TerminalEvent {
  const TerminalEvent();
}

/// Raw PTY output, including escape sequences.
class TerminalOutput extends TerminalEvent {
  final String data;
  const TerminalOutput(this.data);
}

/// The agent process ended. The connection closes afterwards.
class TerminalExited extends TerminalEvent {
  final int code;
  const TerminalExited(this.code);
}

/// Another client attached to the same PTY. This client must not
/// reconnect on its own.
class TerminalReplaced extends TerminalEvent {
  const TerminalReplaced();
}

/// The connection ended without [TerminalExited] or [TerminalReplaced]:
/// the network dropped or Core went away. The PTY may still be running.
class TerminalDropped extends TerminalEvent {
  final String? reason;
  const TerminalDropped([this.reason]);
}

/// One WebSocket attached to a sandbox's PTY.
abstract class TerminalConnection {
  /// Resolves with the PTY's `terminal_id` once input is accepted.
  Future<String> get ready;

  /// Ends with the connection. Events are buffered until listened to.
  Stream<TerminalEvent> get events;

  void sendInput(String data);
  void resize(int cols, int rows);

  /// Detaches without stopping the PTY. No [TerminalDropped] follows.
  Future<void> close();
}
