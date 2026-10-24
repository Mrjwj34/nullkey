import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:url_launcher/url_launcher.dart';
import 'package:xterm/xterm.dart';

import '../models/nullkey_models.dart';
import '../services/core_mock_backend.dart';
import '../services/nullkey_backend.dart';
import '../services/passkey_service.dart';

/// What fills the main area of the window.
enum AppSurface {
  /// First-run checklist: connect Codex, install it, pick a workspace, start.
  setup,

  /// The selected workspace and its current sandbox.
  workspace,
}

/// Views of the current sandbox (`docs/00-product.md` §7). Desktop is only
/// offered when the daemon reports `desktop_available`.
enum SessionTab {
  terminal('终端'),
  artifacts('产物'),
  services('服务'),
  desktop('桌面');

  final String label;
  const SessionTab(this.label);
}

/// Launch feedback required by `docs/10-interaction-contract.md` §2. The
/// stage is the one in progress; earlier stages are done.
enum LaunchStage {
  accepted('已收到启动请求'),
  preparing('正在准备沙箱'),
  connecting('正在连接终端'),
  ready('终端可以输入');

  final String label;
  const LaunchStage(this.label);
}

@immutable
class LaunchProgress {
  final String workspaceId;
  final String? sandboxId;
  final HarnessKind harness;
  final LaunchStage stage;

  const LaunchProgress({
    required this.workspaceId,
    required this.harness,
    required this.stage,
    this.sandboxId,
  });
}

/// The latest result of something the user did, shown once in the message
/// bar. [id] changes for every notice so repeated messages are announced.
@immutable
class StudioNotice {
  final int id;
  final String message;
  final bool isError;

  const StudioNotice({
    required this.id,
    required this.message,
    required this.isError,
  });
}

enum LockedResource { workspace, sessionData }

/// A workspace or session-data lock held by another sandbox (`409
/// state_locked`). Nullkey never stops the holder implicitly.
@immutable
class LockConflict {
  final String workspaceId;
  final String lockingSandboxId;
  final String lockingSandboxName;
  final LockedResource resource;

  const LockConflict({
    required this.workspaceId,
    required this.lockingSandboxId,
    required this.lockingSandboxName,
    required this.resource,
  });
}

@immutable
class ScanOutcome {
  final String sandboxId;
  final SecretScanResult? result;
  final String? error;

  const ScanOutcome({required this.sandboxId, this.result, this.error});
}

/// Client-side state of the terminal attached to one sandbox's PTY. The
/// emulator outlives connections, so what is on screen stays after a
/// disconnect; Core keeps no scrollback to replay.
class TerminalSession {
  final Terminal terminal = Terminal(maxLines: 10000);
  TerminalConnectionPhase phase = TerminalConnectionPhase.idle;
  TerminalTicket? ticket;
  TerminalConnection? connection;
  StreamSubscription<TerminalEvent>? _events;

  /// Whether any output has been shown, so a later attach is a reconnect.
  bool hasOutput = false;

  /// Set after a reconnect: output produced while disconnected is gone.
  bool historyGap = false;

  /// Developer scenario: the WebSocket is open but `ready` has not arrived.
  bool waitingForReadyFrame = false;
  String? error;
  int? exitCode;

  /// Bumped by every attach/detach so a stale attach cannot overwrite a
  /// newer one when its await resumes.
  int attempt = 0;

  /// Detaches from the PTY without stopping it.
  void endConnection() {
    _events?.cancel();
    _events = null;
    connection?.close();
    connection = null;
  }
}

/// State and actions behind the Nullkey macOS GUI.
class NullkeyStudioController extends ChangeNotifier {
  final NullkeyBackend api;
  final PasskeySecurityService passkey;
  final Future<bool> Function(Uri) _openUrl;
  final bool _ownsApi;
  late final StreamSubscription<void> _apiChanges;
  bool _disposed = false;

  late AppSurface _surface;
  bool _loading;

  /// Set once the user navigated, so the first snapshot from Core does not
  /// move them.
  bool _navigated = false;
  bool _unlocked = true;
  PasskeyCapabilities? _passkeyCapabilities;
  PasskeyAssertion? _lastPasskeyAssertion;

  String? _selectedWorkspaceId;
  SessionTab _activeTab = SessionTab.terminal;

  final Map<String, TerminalSession> _terminals = {};

  LaunchProgress? _launch;
  int _launchSerial = 0;

  StudioNotice? _notice;
  int _noticeSerial = 0;
  LockConflict? _conflict;
  AuditEventRecord? _networkDenied;
  ScanOutcome? _scan;

  /// Activity already accounted for; anything new that is a blocked
  /// connection raises the network alert.
  Set<String>? _seenAuditIds;

  NullkeyStudioController({
    NullkeyBackend? api,
    PasskeySecurityService? passkey,
    Future<bool> Function(Uri)? openUrl,
    bool initiallyUnlocked = true,
  }) : api = api ?? CoreMockBackend(),
       _ownsApi = api == null,
       passkey = passkey ?? PasskeySecurityService(),
       _openUrl = openUrl ?? launchUrl,
       _unlocked = initiallyUnlocked,
       _loading = api != null && api is! CoreMockBackend {
    _apiChanges = this.api.changes.listen((_) => _onBackendChanged());
    if (!_loading) {
      _seenAuditIds = {for (final e in auditEvents) e.id};
    }
    _openMostRecent();
    _initAsync();
  }

  Future<void> _initAsync() async {
    final passkeyCapabilities = passkey.queryCapabilities();
    await api.loaded;
    if (_disposed) {
      return;
    }
    _loading = false;
    _seenAuditIds ??= {for (final e in auditEvents) e.id};
    if (!_navigated) {
      _openMostRecent();
    }
    notifyListeners();
    _passkeyCapabilities = await passkeyCapabilities;
    if (!_disposed) {
      notifyListeners();
    }
  }

  @override
  void dispose() {
    _disposed = true;
    _launchSerial++;
    _apiChanges.cancel();
    for (final session in _terminals.values) {
      session.endConnection();
    }
    if (_ownsApi) {
      api.dispose();
    }
    super.dispose();
  }

  /// The in-memory engine when Core is mocked; developer scenarios need it.
  CoreMockBackend? get mock => switch (api) {
    final CoreMockBackend m => m,
    _ => null,
  };

  void _onBackendChanged() {
    if (_disposed) {
      return;
    }
    _noticeNetworkBlocks();
    _reconcileTerminals();
    _attachIfRunning();
    notifyListeners();
  }

  void _noticeNetworkBlocks() {
    final seen = _seenAuditIds;
    if (seen == null) {
      return;
    }
    for (final e in auditEvents) {
      if (seen.add(e.id) && e.isNetworkBlock && e.sandboxId != null) {
        _networkDenied ??= e;
      }
    }
  }

  /// A PTY ends with its sandbox. Interrupted by a Core restart reads
  /// differently from stopped on purpose.
  void _reconcileTerminals() {
    for (final MapEntry(key: sandboxId, value: session) in _terminals.entries) {
      final sbx = api.getSandbox(sandboxId);
      if (sbx != null && sbx.status.isRunningOrStarting) {
        continue;
      }
      final target = sbx?.stopReason == 'daemon_restarted'
          ? TerminalConnectionPhase.daemonInterrupted
          : TerminalConnectionPhase.closed;
      if (session.phase == TerminalConnectionPhase.idle ||
          session.phase == target) {
        continue;
      }
      session
        ..endConnection()
        ..attempt += 1
        ..phase = target
        ..historyGap = false
        ..waitingForReadyFrame = false;
    }
  }

  /// Returning users land on their most recent workspace, even when a login
  /// has expired (they reconnect in place). Without any workspace there is
  /// nothing to return to, so the checklist opens.
  void _openMostRecent() {
    final list = [...workspaces]
      ..sort((a, b) => b.lastUsedAt.compareTo(a.lastUsedAt));
    _selectedWorkspaceId = list.isEmpty ? null : list.first.id;
    _surface = list.isEmpty ? AppSurface.setup : AppSurface.workspace;
    _attachIfRunning();
    _refreshSessionData();
  }

  // ---------------------------------------------------------------------------
  // Read model
  // ---------------------------------------------------------------------------

  AppSurface get surface => _surface;

  /// True until Core's first snapshot arrived.
  bool get loading => _loading;

  /// Why Core cannot be reached right now, or null.
  String? get connectionProblem => api.connectionProblem;
  bool get studioUnlocked => _unlocked;
  DaemonCapabilities get capabilities => api.capabilities;
  PasskeyCapabilities? get passkeyCapabilities => _passkeyCapabilities;
  PasskeyAssertion? get lastPasskeyAssertion => _lastPasskeyAssertion;

  List<WorkspaceRecord> get workspaces => api.listWorkspaces();
  List<PersistentStateRecord> get states => api.listStates();
  List<SandboxRecord> get sandboxes => api.listSandboxes();
  List<HarnessInfo> get harnesses => api.listHarnesses();
  List<JobInfo> get jobs => api.listJobs();
  List<CredentialMetadata> get credentials => api.listCredentials();
  List<ImportableCredentialCandidate> get importableCredentials =>
      api.listImportableCredentials();
  List<OAuthPendingFlow> get oauthFlows => api.listOAuthFlows();
  List<McpServerRecord> get mcpServers => api.listMcpServers();
  List<AuditEventRecord> get auditEvents => api.listAuditEvents();

  String? get selectedWorkspaceId => _selectedWorkspaceId;

  WorkspaceRecord? get selectedWorkspace => _selectedWorkspaceId == null
      ? null
      : workspaceById(_selectedWorkspaceId!);

  WorkspaceRecord? workspaceById(String id) =>
      workspaces.where((w) => w.id == id).firstOrNull;

  /// The sandbox a workspace's session header describes: the active one if
  /// any, otherwise the most recent.
  SandboxRecord? sandboxForWorkspace(String workspaceId) {
    final list = sandboxes.where((s) => s.workspaceId == workspaceId).toList();
    return list.where((s) => s.status.isRunningOrStarting).firstOrNull ??
        list.firstOrNull;
  }

  SandboxRecord? get selectedSandbox => _selectedWorkspaceId == null
      ? null
      : sandboxForWorkspace(_selectedWorkspaceId!);

  SessionTab get activeTab => _activeTab;

  List<SessionTab> get availableTabs => [
    SessionTab.terminal,
    SessionTab.artifacts,
    SessionTab.services,
    if (capabilities.desktopAvailable) SessionTab.desktop,
  ];

  TerminalSession? get _selectedTerminal {
    final sbx = selectedSandbox;
    return sbx == null ? null : _terminals[sbx.id];
  }

  /// The emulator showing the selected sandbox's PTY.
  Terminal? get selectedTerminal => _selectedTerminal?.terminal;

  TerminalConnectionPhase get terminalPhase =>
      _selectedTerminal?.phase ?? TerminalConnectionPhase.idle;
  TerminalTicket? get activeTerminalTicket => _selectedTerminal?.ticket;
  bool get showReconnectedGapBanner => _selectedTerminal?.historyGap ?? false;
  bool get terminalWaitingForReadyFrame =>
      _selectedTerminal?.waitingForReadyFrame ?? false;
  String? get terminalError => _selectedTerminal?.error;
  int? get terminalExitCode => _selectedTerminal?.exitCode;

  /// Launch in progress for the selected workspace, if any.
  LaunchProgress? get launchProgress =>
      _launch?.workspaceId == _selectedWorkspaceId ? _launch : null;

  bool isLaunching(String workspaceId) => _launch?.workspaceId == workspaceId;

  StudioNotice? get notice => _notice;

  LockConflict? get lastStateLockedConflict =>
      _conflict?.workspaceId == _selectedWorkspaceId ? _conflict : null;

  AuditEventRecord? get lastNetworkDeniedBlock {
    final sbx = selectedSandbox;
    return sbx != null && _networkDenied?.sandboxId == sbx.id
        ? _networkDenied
        : null;
  }

  ScanOutcome? get lastScan {
    final sbx = selectedSandbox;
    return sbx != null && _scan?.sandboxId == sbx.id ? _scan : null;
  }

  SecretScanResult? get lastSecretScanResult => lastScan?.result;
  String? get lastSecretScanError => lastScan?.error;

  List<SandboxServiceRecord> get activeServices {
    final sbx = selectedSandbox;
    return sbx == null ? const [] : api.listServices(sbx.id);
  }

  List<ArtifactRecord> get activeArtifacts {
    final sbx = selectedSandbox;
    return sbx == null ? const [] : api.listArtifacts(sbx.id);
  }

  DesktopStatusRecord? get activeDesktopStatus {
    final sbx = selectedSandbox;
    return sbx == null ? null : api.getDesktopStatus(sbx.id);
  }

  HarnessInfo? harness(HarnessKind kind) => api.getHarness(kind);

  bool isHarnessInstalled(HarnessKind kind) {
    final status = harness(kind)?.status;
    return status == HarnessStatus.installed ||
        status == HarnessStatus.updateAvailable;
  }

  /// The install/update job shown for [kind]: the running one, otherwise the
  /// latest one so a cancellation or failure stays visible.
  JobInfo? jobFor(HarnessKind kind) =>
      jobs.where((j) => j.targetId == kind.wireName).firstOrNull;

  CredentialMetadata? credentialFor(String provider) {
    final list = credentials.where((c) => c.provider == provider).toList();
    return list.where((c) => c.status == CredentialStatus.active).firstOrNull ??
        list.firstOrNull;
  }

  bool isProviderConnected(String provider) =>
      credentialFor(provider)?.status == CredentialStatus.active;

  OAuthPendingFlow? latestOAuthFlow(String provider) =>
      oauthFlows.where((f) => f.provider == provider).firstOrNull;

  /// Provider whose login the selected workspace's agent needs but does not
  /// have, so the workspace can offer to reconnect in place.
  String? get missingProviderForSelected {
    final kind = selectedSandbox?.harness ?? HarnessKind.codex;
    final providers = harness(kind)?.requiredProviders ?? const <String>[];
    if (providers.isEmpty || providers.any(isProviderConnected)) {
      return null;
    }
    return providers.first;
  }

  bool get codexConnected => isProviderConnected('openai');
  bool get codexInstalled => isHarnessInstalled(HarnessKind.codex);
  bool get setupComplete =>
      codexConnected && codexInstalled && workspaces.isNotEmpty;

  /// What "Start" uses for [workspace]: the previous sandbox's settings when
  /// there is one, so returning users continue where they left off.
  ({HarnessKind harness, MountMode mode, NetworkMode network}) launchDefaults(
    WorkspaceRecord workspace,
  ) {
    final last = sandboxForWorkspace(workspace.id);
    if (last != null) {
      return (
        harness: last.harness,
        mode: last.workspaceMode,
        network: last.networkMode,
      );
    }
    return (
      harness: HarnessKind.codex,
      mode: workspace.defaultMode,
      network: NetworkMode.filtered,
    );
  }

  // ---------------------------------------------------------------------------
  // Navigation & lock
  // ---------------------------------------------------------------------------

  void openSetup() {
    _navigated = true;
    _surface = AppSurface.setup;
    _notice = null;
    notifyListeners();
  }

  void selectWorkspace(String workspaceId) {
    if (workspaceById(workspaceId) == null) {
      return;
    }
    _navigated = true;
    if (_selectedWorkspaceId != workspaceId) {
      _notice = null;
      if (_activeTab == SessionTab.desktop && !capabilities.desktopAvailable) {
        _activeTab = SessionTab.terminal;
      }
    }
    _selectedWorkspaceId = workspaceId;
    _surface = AppSurface.workspace;
    _attachIfRunning();
    _refreshSessionData();
    notifyListeners();
  }

  void setActiveTab(SessionTab tab) {
    if (!availableTabs.contains(tab)) {
      return;
    }
    _activeTab = tab;
    _refreshSessionData();
    notifyListeners();
  }

  /// Artifacts and services are not pushed by Core; they are loaded when
  /// the user looks at them.
  void _refreshSessionData() {
    final sbx = selectedSandbox;
    if (sbx == null) {
      return;
    }
    unawaited(api.refreshArtifacts(sbx.id).catchError((Object _) {}));
    if (sbx.status == SandboxStatus.running) {
      unawaited(api.refreshServices(sbx.id).catchError((Object _) {}));
    }
  }

  void lockStudio() {
    _unlocked = false;
    notifyListeners();
  }

  Future<PasskeyAssertion?> unlockStudioWithPasskey() async {
    try {
      final assertion = await _verify(PasskeyScope.vaultUnlock);
      _unlocked = true;
      notifyListeners();
      return assertion;
    } on NullkeyApiException catch (e) {
      _notify(e.message, error: true);
      return null;
    }
  }

  void dismissNotice() {
    _notice = null;
    notifyListeners();
  }

  void dismissConflict() {
    _conflict = null;
    notifyListeners();
  }

  void dismissNetworkDenied() {
    _networkDenied = null;
    notifyListeners();
  }

  void dismissScan() {
    _scan = null;
    notifyListeners();
  }

  void dismissHistoryGap() {
    final session = _selectedTerminal;
    if (session == null || !session.historyGap) {
      return;
    }
    session.historyGap = false;
    notifyListeners();
  }

  /// Reports the result of a purely local action, such as copying a link.
  void showMessage(String message) => _notify(message);

  Future<PasskeyAssertion> _verify(PasskeyScope scope, {String? reason}) async {
    final assertion = await passkey.assertPasskey(
      scope: scope,
      customReason: reason,
    );
    _lastPasskeyAssertion = assertion;
    return assertion;
  }

  void _notify(String message, {bool error = false}) {
    if (_disposed) {
      return;
    }
    _notice = StudioNotice(
      id: ++_noticeSerial,
      message: message,
      isError: error,
    );
    notifyListeners();
  }

  // ---------------------------------------------------------------------------
  // Credentials & sign-in
  // ---------------------------------------------------------------------------

  /// Returns an error message, or null once the key is stored.
  Future<String?> addApiKey({
    required String provider,
    required String secret,
    String? name,
  }) async {
    try {
      await _verify(PasskeyScope.credentialMutation, reason: '保存 API Key');
      final created = await api.createCredential(
        name: name ?? '${providerAccountName(provider)} API Key',
        provider: provider,
        kind: CredentialKind.apiKey,
        secretValue: secret,
      );
      _notify('已保存 ${created.name}');
      return null;
    } on NullkeyApiException catch (e) {
      notifyListeners();
      return e.message;
    }
  }

  Future<bool> revokeCredential(String credentialId) async {
    final cred = credentials.where((c) => c.id == credentialId).firstOrNull;
    try {
      await _verify(
        PasskeyScope.credentialMutation,
        reason: '撤销 ${cred?.name ?? '凭证'}',
      );
      await api.deleteCredential(credentialId);
      _notify('已撤销 ${cred?.name ?? '凭证'}');
      return true;
    } on NullkeyApiException catch (e) {
      _notify(e.message, error: true);
      return false;
    }
  }

  Future<ImportCredentialsResult?> importHostCredentials(
    List<String> candidateIds,
  ) async {
    try {
      await _verify(PasskeyScope.credentialMutation, reason: '从本机导入登录');
      final result = await api.importHostCredentials(candidateIds);
      final parts = [
        if (result.imported.isNotEmpty) '已导入 ${result.imported.length} 项',
        if (result.skipped.isNotEmpty)
          '跳过 ${result.skipped.length} 项（${result.skipped.map((s) => s.reason).join('；')}）',
      ];
      _notify(parts.join('，'), error: result.imported.isEmpty);
      return result;
    } on NullkeyApiException catch (e) {
      _notify(e.message, error: true);
      return null;
    }
  }

  /// Starts a browser sign-in and opens the provider's page.
  Future<OAuthPendingFlow?> startOAuth(String provider) async {
    try {
      await _verify(
        PasskeyScope.oauthBroker,
        reason: '登录${providerAccountName(provider)}',
      );
      final flow = await api.startOAuthFlow(provider: provider);
      // The mock's sign-in page does not exist; its flow completes by itself.
      if (mock == null && flow.authUrl.isNotEmpty) {
        final opened = await _openUrl(Uri.parse(flow.authUrl));
        if (!opened) {
          _notify('没能打开浏览器。', error: true);
        }
      }
      notifyListeners();
      return flow;
    } on NullkeyApiException catch (e) {
      _notify(e.message, error: true);
      return null;
    }
  }

  Future<void> cancelOAuth(String flowId) async {
    try {
      await api.cancelOAuthFlow(flowId);
      _notify('已取消登录');
    } on NullkeyApiException catch (e) {
      // A conflict means the browser finished first: the account is connected
      // and that is the real outcome to show.
      _notify(e.message, error: e.code != 'conflict');
    }
  }

  /// The browser returning to the loopback callback. Only reachable from
  /// developer scenarios and tests while Core is mocked.
  void completeOAuthCallback(String flowId) {
    final engine = mock;
    if (engine == null) {
      return;
    }
    try {
      final flow = engine.completeOAuthFlowCallback(flowId);
      _notify('已连接${providerAccountName(flow.provider)}');
    } on NullkeyApiException catch (e) {
      _notify(e.message, error: true);
    }
  }

  // ---------------------------------------------------------------------------
  // Harness install jobs
  // ---------------------------------------------------------------------------

  Future<JobInfo?> startHarnessInstall(
    HarnessKind kind, {
    bool isUpdate = false,
  }) async {
    try {
      final job = await api.startHarnessInstall(kind, isUpdate: isUpdate);
      notifyListeners();
      return job;
    } on NullkeyApiException catch (e) {
      _notify(e.message, error: true);
      return null;
    }
  }

  /// Developer scenario: moves a mocked install forward.
  void advanceJob(String jobId, {double? toProgress}) {
    mock?.advanceJobProgress(jobId, toProgress: toProgress);
    notifyListeners();
  }

  Future<void> requestCancelJob(String jobId) async {
    try {
      await api.requestCancelJob(jobId);
      notifyListeners();
    } on NullkeyApiException catch (e) {
      _notify(e.message, error: e.code != 'conflict');
    }
  }

  /// Developer scenario: finishes a mocked cancellation's cleanup.
  void finalizeCancelJob(String jobId) {
    mock?.finalizeCancelJob(jobId);
    notifyListeners();
  }

  // ---------------------------------------------------------------------------
  // Workspaces & session data
  // ---------------------------------------------------------------------------

  /// Returns an error message, or null once the workspace exists. The new
  /// workspace becomes the selection; [open] also switches to it.
  Future<String?> createWorkspace({
    required String name,
    required WorkspaceKind kind,
    String? hostPath,
    MountMode defaultMode = MountMode.rw,
    bool open = true,
  }) async {
    try {
      if (kind == WorkspaceKind.external && defaultMode == MountMode.rw) {
        await _verify(
          PasskeyScope.highRiskSandbox,
          reason: '允许 Agent 直接修改 ${hostPath ?? name}',
        );
      }
      final created = await api.createWorkspace(
        name: name,
        kind: kind,
        hostPath: hostPath,
        defaultMode: defaultMode,
      );
      _selectedWorkspaceId = created.id;
      if (open) {
        _navigated = true;
        _surface = AppSurface.workspace;
      }
      _notify('已添加工作区“${created.name}”');
      return null;
    } on NullkeyApiException catch (e) {
      notifyListeners();
      return e.message;
    }
  }

  /// Removes [workspaceId] from the list, and with [deleteFiles] also deletes
  /// a managed workspace's folder. Returns an error message, or null.
  Future<String?> removeWorkspace({
    required String workspaceId,
    required bool deleteFiles,
    String? confirmName,
  }) async {
    final ws = workspaceById(workspaceId);
    try {
      if (deleteFiles) {
        await _verify(
          PasskeyScope.destructiveStorage,
          reason: '删除 ${ws?.name ?? '工作区'} 的全部文件',
        );
      }
      await api.deleteWorkspace(
        workspaceId,
        deleteFiles: deleteFiles,
        confirmName: confirmName,
      );
      if (_selectedWorkspaceId == workspaceId) {
        _openMostRecent();
      }
      _notify(deleteFiles ? '已删除“${ws?.name}”' : '已从列表移除“${ws?.name}”');
      return null;
    } on NullkeyApiException catch (e) {
      notifyListeners();
      return e.message;
    }
  }

  /// Session data the workspace's sandboxes reuse for [kind].
  PersistentStateRecord? sessionDataFor(String workspaceId, HarnessKind kind) {
    final stateId = sandboxes
        .where(
          (s) =>
              s.workspaceId == workspaceId &&
              s.harness == kind &&
              s.stateId != null,
        )
        .firstOrNull
        ?.stateId;
    return stateId == null
        ? null
        : states.where((s) => s.id == stateId).firstOrNull;
  }

  Future<bool> resetSessionData(String stateId) async {
    try {
      await _verify(PasskeyScope.destructiveStorage, reason: '清除会话数据');
      await api.resetState(stateId);
      _notify('已清除会话数据');
      return true;
    } on NullkeyApiException catch (e) {
      _handleLockError(e, _selectedWorkspaceId);
      if (e.code == 'state_locked') {
        notifyListeners();
      } else {
        _notify(e.message, error: true);
      }
      return false;
    }
  }

  void _handleLockError(NullkeyApiException e, String? workspaceId) {
    final holder = e.lockingSandboxId;
    if (e.code == 'state_locked' && holder != null && workspaceId != null) {
      _conflict = LockConflict(
        workspaceId: workspaceId,
        lockingSandboxId: holder,
        lockingSandboxName: api.getSandbox(holder)?.name ?? holder,
        resource: e.details.containsKey('state_id')
            ? LockedResource.sessionData
            : LockedResource.workspace,
      );
    }
  }

  // ---------------------------------------------------------------------------
  // Sandboxes
  // ---------------------------------------------------------------------------

  /// Starts a sandbox and walks it through the launch stages until the
  /// terminal accepts input. Session data is reused per workspace and agent
  /// unless [stateId] picks one explicitly.
  Future<SandboxRecord?> launch({
    required String workspaceId,
    HarnessKind? harness,
    MountMode? mode,
    NetworkMode? network,
    String? stateId,
    bool enableDesktop = false,
  }) async {
    final ws = workspaceById(workspaceId);
    if (ws == null) {
      return null;
    }
    final defaults = launchDefaults(ws);
    final kind = harness ?? defaults.harness;
    final mountMode = mode ?? defaults.mode;
    final networkMode = network ?? defaults.network;

    final serial = ++_launchSerial;
    if (_conflict?.workspaceId == workspaceId) {
      _conflict = null;
    }
    _launch = LaunchProgress(
      workspaceId: workspaceId,
      harness: kind,
      stage: LaunchStage.accepted,
    );
    _navigated = true;
    _selectedWorkspaceId = workspaceId;
    _surface = AppSurface.workspace;
    _activeTab = SessionTab.terminal;
    _notice = null;
    notifyListeners();

    try {
      if ((ws.kind == WorkspaceKind.external && mountMode == MountMode.rw) ||
          networkMode == NetworkMode.open) {
        await _verify(
          PasskeyScope.highRiskSandbox,
          reason: networkMode == NetworkMode.open
              ? '允许沙箱访问开放网络'
              : '允许 Agent 直接修改 ${ws.normalizedPath}',
        );
        if (serial != _launchSerial) {
          return null;
        }
      }

      final created = await api.createSandbox(
        harness: kind,
        workspaceId: workspaceId,
        workspaceMode: mountMode,
        stateId: stateId ?? sessionDataFor(workspaceId, kind)?.id,
        networkMode: networkMode,
        enableDesktop: enableDesktop,
      );
      if (serial != _launchSerial) {
        return created;
      }
      _launch = LaunchProgress(
        workspaceId: workspaceId,
        sandboxId: created.id,
        harness: kind,
        stage: LaunchStage.preparing,
      );
      notifyListeners();

      final running = await api.waitUntilRunning(created.id);
      if (serial != _launchSerial) {
        return created;
      }
      if (running?.status != SandboxStatus.running) {
        _launch = null;
        notifyListeners();
        return created;
      }

      _launch = LaunchProgress(
        workspaceId: workspaceId,
        sandboxId: created.id,
        harness: kind,
        stage: LaunchStage.connecting,
      );
      notifyListeners();
      await _attach(created.id);
      if (serial == _launchSerial) {
        _launch = null;
        _refreshSessionData();
        notifyListeners();
      }
      return created;
    } on NullkeyApiException catch (e) {
      if (serial == _launchSerial) {
        _launch = null;
      }
      _handleLockError(e, workspaceId);
      // Lock conflicts, expired logins and failed starts get their own panel
      // or alert with the next step; repeating them in the message bar would
      // only add noise.
      final hasOwnAlert =
          _conflict?.workspaceId == workspaceId ||
          (e.code == 'reauth_required' && missingProviderForSelected != null) ||
          (e.code == 'runtime_unavailable' &&
              sandboxForWorkspace(workspaceId)?.status == SandboxStatus.failed);
      if (hasOwnAlert) {
        notifyListeners();
      } else {
        _notify(e.message, error: true);
      }
      return null;
    }
  }

  /// Starts the selected workspace again with its previous sandbox's
  /// workspace, session data and network mode.
  Future<SandboxRecord?> restartInterruptedSandboxWithSameConfig() async {
    final prev = selectedSandbox;
    if (prev == null) {
      return null;
    }
    return launch(
      workspaceId: prev.workspaceId,
      harness: prev.harness,
      mode: prev.workspaceMode,
      network: prev.networkMode,
      stateId: prev.stateId,
      enableDesktop: prev.enableDesktop,
    );
  }

  /// Opens the sandbox holding a lock, without stopping it.
  void jumpToLockingSandbox(String sandboxId) {
    final sbx = api.getSandbox(sandboxId);
    if (sbx == null) {
      return;
    }
    _conflict = null;
    _activeTab = SessionTab.terminal;
    selectWorkspace(sbx.workspaceId);
  }

  Future<void> stopSelectedSandbox() async {
    final sbx = selectedSandbox;
    if (sbx == null) {
      return;
    }
    if (_launch?.sandboxId == sbx.id) {
      _launchSerial++;
      _launch = null;
    }
    final session = _terminals[sbx.id];
    if (session != null) {
      session
        ..endConnection()
        ..attempt += 1
        ..phase = TerminalConnectionPhase.closed
        ..historyGap = false
        ..waitingForReadyFrame = false;
    }
    notifyListeners();
    try {
      await api.stopSandbox(sbx.id);
      _notify('沙箱已停止');
    } on NullkeyApiException catch (e) {
      _notify(e.message, error: true);
    }
  }

  void _attachIfRunning() {
    final sbx = selectedSandbox;
    if (sbx == null ||
        sbx.status != SandboxStatus.running ||
        isLaunching(sbx.workspaceId)) {
      return;
    }
    final session = _terminals[sbx.id];
    if (session == null || session.phase == TerminalConnectionPhase.idle) {
      unawaited(_attach(sbx.id));
    }
  }

  TerminalSession _session(String sandboxId) =>
      _terminals.putIfAbsent(sandboxId, () {
        final session = TerminalSession();
        session.terminal
          ..onOutput = (data) {
            if (session.phase.canSendInput) {
              session.connection?.sendInput(data);
            }
          }
          ..onResize = (cols, rows, _, _) {
            session.connection?.resize(cols, rows);
          };
        return session;
      });

  /// Requests a ticket, opens the WebSocket and waits for its `ready` frame.
  /// Input stays disabled until then.
  Future<void> _attach(
    String sandboxId, {
    bool reconnect = false,
    bool holdForReadyFrame = false,
  }) async {
    final session = _session(sandboxId);
    final attempt = ++session.attempt;
    session
      ..endConnection()
      ..phase = reconnect
          ? TerminalConnectionPhase.reconnecting
          : TerminalConnectionPhase.requestingTicket
      ..historyGap = false
      ..waitingForReadyFrame = false
      ..error = null
      ..exitCode = null;
    notifyListeners();

    bool current() => !_disposed && attempt == session.attempt;
    try {
      final ticket = await api.createTerminalTicket(sandboxId);
      if (!current()) {
        return;
      }
      session
        ..ticket = ticket
        ..phase = TerminalConnectionPhase.connectingWs;
      notifyListeners();
      if (holdForReadyFrame) {
        mock?.holdNextTerminalReady = true;
      }
      final connection = await api.connectTerminal(ticket);
      if (!current()) {
        connection.close();
        return;
      }
      session
        ..connection = connection
        .._events = connection.events.listen(
          (event) => _onTerminalEvent(sandboxId, session, attempt, event),
        );
      if (holdForReadyFrame) {
        session.waitingForReadyFrame = true;
        notifyListeners();
      }
      await connection.ready;
      if (!current()) {
        return;
      }
      final terminal = session.terminal;
      connection.resize(terminal.viewWidth, terminal.viewHeight);
      session
        ..phase = TerminalConnectionPhase.ready
        ..waitingForReadyFrame = false
        ..historyGap = session.hasOutput;
      if (session.hasOutput) {
        terminal.write('\r\n\x1b[2m── 已重新连接 · 断线期间的输出无法恢复 ──\x1b[0m\r\n');
      }
      notifyListeners();
    } on NullkeyApiException catch (e) {
      // A connection that ended before `ready` is reported by its events.
      if (!current() ||
          session.phase == TerminalConnectionPhase.replaced ||
          session.phase == TerminalConnectionPhase.closed) {
        return;
      }
      session
        ..endConnection()
        ..phase = TerminalConnectionPhase.error
        ..waitingForReadyFrame = false
        ..error = e.message;
      notifyListeners();
    }
  }

  void _onTerminalEvent(
    String sandboxId,
    TerminalSession session,
    int attempt,
    TerminalEvent event,
  ) {
    if (_disposed || attempt != session.attempt) {
      return;
    }
    switch (event) {
      case TerminalOutput(:final data):
        session
          ..hasOutput = true
          ..terminal.write(data);
        return;
      case TerminalExited(:final code):
        session
          ..phase = TerminalConnectionPhase.exited
          ..exitCode = code;
      case TerminalReplaced():
        session.phase = TerminalConnectionPhase.replaced;
      case TerminalDropped():
        session.phase = TerminalConnectionPhase.closed;
    }
    session
      ..connection = null
      ..waitingForReadyFrame = false
      ..historyGap = false;
    _reconcileTerminals();
    notifyListeners();
  }

  /// Developer scenario: the WebSocket is open but the `ready` frame has not
  /// arrived yet.
  void attachTerminalToSelectedSandbox({
    bool holdAtWaitingForReadyFrame = false,
  }) {
    final sbx = selectedSandbox;
    if (sbx == null) {
      return;
    }
    unawaited(_attach(sbx.id, holdForReadyFrame: holdAtWaitingForReadyFrame));
  }

  /// Developer scenario: lets a held `ready` frame through.
  void completeTerminalReadyFrame() => mock?.releaseHeldTerminalReady();

  /// Closes the terminal view only; the sandbox keeps running.
  void detachTerminal() {
    final session = _selectedTerminal;
    if (session == null) {
      return;
    }
    session
      ..endConnection()
      ..attempt += 1
      ..phase = TerminalConnectionPhase.closed
      ..historyGap = false
      ..waitingForReadyFrame = false;
    notifyListeners();
  }

  /// Reattaches to the same PTY. The TUI redraws; earlier output is not
  /// replayed.
  void reconnectTerminal() {
    final sbx = selectedSandbox;
    if (sbx == null) {
      return;
    }
    unawaited(_attach(sbx.id, reconnect: true));
  }

  /// Sends [data] to the selected PTY as typed, escape sequences included.
  /// Returns false when the terminal does not accept input yet.
  bool sendTerminalInput(String data) {
    final session = _selectedTerminal;
    if (session == null || !session.phase.canSendInput || data.isEmpty) {
      return false;
    }
    session.connection?.sendInput(data);
    return true;
  }

  // ---------------------------------------------------------------------------
  // Services, desktop & credential check
  // ---------------------------------------------------------------------------

  /// Opens a service the sandbox declared in the browser, through a
  /// single-use ticket.
  Future<void> openService(String serviceId) async {
    final sbx = selectedSandbox;
    if (sbx == null) {
      return;
    }
    try {
      final uri = await api.openService(sbx.id, serviceId);
      if (mock != null) {
        _notify('Core Mock 里的服务只是示例，没有可以打开的页面');
        return;
      }
      if (!await _openUrl(uri)) {
        _notify('没能打开浏览器。', error: true);
      }
    } on NullkeyApiException catch (e) {
      _notify(e.message, error: true);
    }
  }

  Future<void> setDesktopEnabled(bool enabled) async {
    final sbx = selectedSandbox;
    if (sbx == null) {
      return;
    }
    try {
      await api.setDesktopEnabled(sbx.id, enabled);
      _notify(enabled ? '虚拟桌面已打开' : '虚拟桌面已关闭');
    } on NullkeyApiException catch (e) {
      _notify(e.message, error: true);
    }
  }

  /// `POST /sandboxes/{id}/scan` on the selected sandbox.
  Future<SecretScanResult?> runSecretScanOnSelectedSandbox() async {
    final sbx = selectedSandbox;
    if (sbx == null) {
      return null;
    }
    try {
      final result = await api.scanSandboxSecrets(sbx.id);
      _scan = ScanOutcome(sandboxId: sbx.id, result: result);
      notifyListeners();
      return result;
    } on NullkeyApiException catch (e) {
      _scan = ScanOutcome(sandboxId: sbx.id, error: e.message);
      notifyListeners();
      return null;
    }
  }

  // ---------------------------------------------------------------------------
  // Developer scenarios (Core Mock only)
  // ---------------------------------------------------------------------------

  void drillTransientTerminalReconnect() => reconnectTerminal();

  void drillTerminalReplacedByAnotherClient() {
    final sbx = selectedSandbox;
    if (sbx != null) {
      mock?.simulateTerminalReplaced(sbx.id);
    }
  }

  void drillDaemonRestartInterruption() {
    _launchSerial++;
    _launch = null;
    mock?.simulateDaemonRestartRecovery();
    _onBackendChanged();
  }

  void drillOutboundNetworkDenied({String host = 'telemetry.example.net'}) {
    final sbx = selectedSandbox;
    if (sbx == null) {
      return;
    }
    mock?.simulateOutboundNetworkBlock(sandboxId: sbx.id, blockedHost: host);
    _onBackendChanged();
  }

  /// From another, idle workspace, starts a sandbox on session data that a
  /// running sandbox holds.
  Future<void> drillStateLockedConflict() async {
    final lockedState = states.where((s) => s.isLocked).firstOrNull;
    final holder = lockedState == null
        ? null
        : api.getSandbox(lockedState.lockedBySandboxId!);
    if (lockedState == null || holder == null) {
      return;
    }
    final target = workspaces
        .where(
          (w) =>
              w.id != holder.workspaceId &&
              sandboxForWorkspace(w.id)?.status.isRunningOrStarting != true,
        )
        .firstOrNull;
    if (target == null) {
      return;
    }
    await launch(
      workspaceId: target.id,
      harness: holder.harness,
      stateId: lockedState.id,
    );
  }

  void drillNextScanFails() {
    mock?.simulateNextScanFailure = true;
  }

  void drillCredentialExpired({String provider = 'openai'}) {
    mock?.simulateCredentialExpired(provider);
    notifyListeners();
  }

  /// Replays the browser callback of the most recently cancelled sign-in.
  void drillLateOAuthCallback() {
    final flow = oauthFlows
        .where((f) => f.status == OAuthFlowStatus.cancelled)
        .firstOrNull;
    if (flow == null) {
      _notify('没有已取消的登录可以模拟', error: true);
      return;
    }
    completeOAuthCallback(flow.flowId);
  }

  void toggleDesktopCapability(bool available) {
    mock?.setDesktopAvailableCapability(available);
    if (!available && _activeTab == SessionTab.desktop) {
      _activeTab = SessionTab.terminal;
    }
    notifyListeners();
  }

  void resetToFirstRun() {
    final engine = mock;
    if (engine == null) {
      return;
    }
    _launchSerial++;
    _launch = null;
    for (final session in _terminals.values) {
      session.endConnection();
    }
    _terminals.clear();
    _conflict = null;
    _networkDenied = null;
    _scan = null;
    _notice = null;
    _activeTab = SessionTab.terminal;
    engine.resetToFirstRun();
    _seenAuditIds = {for (final e in auditEvents) e.id};
    _openMostRecent();
    notifyListeners();
  }
}
