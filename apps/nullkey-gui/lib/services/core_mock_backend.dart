import 'dart:async';

import '../models/nullkey_models.dart';
import 'nullkey_backend.dart';

/// Data the Core Mock engine starts from.
enum MockSeed {
  /// Codex connected and installed, three workspaces, one running sandbox.
  returningUser,

  /// Nothing connected, installed or created yet.
  firstRun,
}

/// An in-memory stand-in for `nullkeyd`, for demos and tests.
///
/// It follows `docs/08-api-contract.md`: the same error codes, lock rules
/// and job/OAuth state machines a real daemon has. The `simulate*` members
/// reproduce situations that are hard to trigger on demand against a real
/// daemon (a restart, a race between cancel and completion).
class CoreMockBackend implements NullkeyBackend {
  static const Map<HarnessKind, String> _pinnedVersions = {
    HarnessKind.codex: '0.118.0',
    HarnessKind.claudeCode: '1.0.29',
    HarnessKind.geminiCli: '0.9.1',
    HarnessKind.opencode: '0.6.2',
  };

  /// Pacing of the engine. Zero settles every call immediately (tests). A
  /// positive value makes sandboxes take time to start, installs report
  /// progress and browser sign-in complete after a while, so the GUI can be
  /// reviewed with the same intermediate states a real daemon produces.
  final Duration simulatedLatency;

  DaemonCapabilities _capabilities;
  final List<HarnessInfo> _harnesses = [];
  final Map<String, JobInfo> _jobs = {};
  final Map<String, int> _cancellingTicks = {};
  final List<CredentialMetadata> _credentials = [];
  final List<ImportableCredentialCandidate> _importableCandidates = [];
  final Map<String, OAuthPendingFlow> _oauthFlows = {};
  final List<WorkspaceRecord> _workspaces = [];
  final List<PersistentStateRecord> _states = [];
  final List<McpServerRecord> _mcpServers = [];
  final List<SandboxRecord> _sandboxes = [];
  final Map<String, List<SandboxServiceRecord>> _servicesBySandbox = {};
  final Map<String, DesktopStatusRecord> _desktopBySandbox = {};
  final Map<String, List<ArtifactRecord>> _artifactsBySandbox = {};
  final List<AuditEventRecord> _auditEvents = [];
  final Map<String, String> _openTickets = {};
  final Map<String, _MockTerminalConnection> _terminals = {};
  final Set<String> _attachedOnce = {};
  final Set<String> _seededTranscripts = {};

  final StreamController<void> _changes = StreamController<void>.broadcast();
  Timer? _jobTicker;
  final Map<String, Timer> _oauthCallbackTimers = {};
  Completer<void>? _heldTerminalReady;

  int _idCounter = 100;
  bool simulateNextScanFailure = false;
  bool simulateJobCompleteBeforeCancel = false;
  bool simulateOAuthCompleteBeforeCancel = false;

  /// The next terminal connection stays open without sending `ready` until
  /// [releaseHeldTerminalReady].
  bool holdNextTerminalReady = false;

  CoreMockBackend({
    this.simulatedLatency = Duration.zero,
    MockSeed seed = MockSeed.returningUser,
  }) : _capabilities = const DaemonCapabilities(
         version: '0.4.2-macos-arm64',
         os: 'darwin',
         arch: 'arm64',
         sandboxBackend: 'macOS Seatbelt',
         desktopAvailable: true,
         oauthImportAvailable: true,
         networkModes: NetworkMode.values,
         connectionMode: DaemonConnectionMode.contractEngine,
         endpointUrl: 'core-mock://local',
         browserSignInProviders: {'openai', 'anthropic', 'github'},
         apiKeyProviders: ['openai', 'anthropic', 'google', 'github'],
         perLaunchMountMode: true,
       ) {
    _seed(seed);
  }

  @override
  DaemonCapabilities get capabilities => _capabilities;

  @override
  String? get connectionProblem => null;

  @override
  Future<void> get loaded => Future<void>.value();

  @override
  Stream<void> get changes => _changes.stream;

  @override
  void dispose() {
    _jobTicker?.cancel();
    for (final timer in _oauthCallbackTimers.values) {
      timer.cancel();
    }
    _oauthCallbackTimers.clear();
    for (final conn in _terminals.values.toList()) {
      conn.close();
    }
    _changes.close();
  }

  /// Clears every resource and starts over from [MockSeed.firstRun].
  void resetToFirstRun() {
    _jobTicker?.cancel();
    _jobTicker = null;
    for (final timer in _oauthCallbackTimers.values) {
      timer.cancel();
    }
    _oauthCallbackTimers.clear();
    for (final conn in _terminals.values.toList()) {
      conn.close();
    }
    _terminals.clear();
    _attachedOnce.clear();
    _seededTranscripts.clear();
    _openTickets.clear();
    _harnesses.clear();
    _jobs.clear();
    _cancellingTicks.clear();
    _credentials.clear();
    _importableCandidates.clear();
    _oauthFlows.clear();
    _workspaces.clear();
    _states.clear();
    _mcpServers.clear();
    _sandboxes.clear();
    _servicesBySandbox.clear();
    _desktopBySandbox.clear();
    _artifactsBySandbox.clear();
    _auditEvents.clear();
    _seed(MockSeed.firstRun);
    _emit();
  }

  void _emit() {
    if (!_changes.isClosed) {
      _changes.add(null);
    }
  }

  String _nextId(String prefix) => '$prefix-${++_idCounter}';

  static NullkeyApiException _notFound(String message) =>
      NullkeyApiException(statusCode: 404, code: 'not_found', message: message);

  // ---------------------------------------------------------------------------
  // Seed data
  // ---------------------------------------------------------------------------

  void _seed(MockSeed seed) {
    final now = DateTime.now().toUtc();
    final returning = seed == MockSeed.returningUser;

    HarnessInfo harness(
      HarnessKind kind, {
      required String executable,
      required List<String> providers,
      required String description,
      HarnessStatus status = HarnessStatus.notInstalled,
      String? version,
    }) => HarnessInfo(
      kind: kind,
      name: kind.displayName,
      status: status,
      version: version,
      executable: executable,
      requiredProviders: providers,
      description: description,
    );

    _harnesses.addAll([
      harness(
        HarnessKind.codex,
        executable: '/cache/harnesses/codex/0.118.0/bin/codex',
        providers: const ['openai'],
        description: 'OpenAI 的终端编码 Agent。',
        status: returning
            ? HarnessStatus.installed
            : HarnessStatus.notInstalled,
        version: returning ? '0.118.0' : null,
      ),
      harness(
        HarnessKind.claudeCode,
        executable: '/cache/harnesses/claude/1.0.29/bin/claude',
        providers: const ['anthropic'],
        description: 'Anthropic 的终端编码 Agent。',
        status: returning
            ? HarnessStatus.installed
            : HarnessStatus.notInstalled,
        version: returning ? '1.0.29' : null,
      ),
      harness(
        HarnessKind.geminiCli,
        executable: '/cache/harnesses/gemini/0.8.4/bin/gemini',
        providers: const ['google'],
        description: 'Google 的终端编码 Agent。',
        status: returning
            ? HarnessStatus.updateAvailable
            : HarnessStatus.notInstalled,
        version: returning ? '0.8.4' : null,
      ),
      harness(
        HarnessKind.opencode,
        executable: '/cache/harnesses/opencode/0.6.2/bin/opencode',
        providers: const ['openai', 'anthropic'],
        description: '可切换多家模型的终端 Agent。',
      ),
    ]);

    _importableCandidates.addAll([
      ImportableCredentialCandidate(
        id: 'cand-codex-auth-json',
        provider: 'openai',
        kind: CredentialKind.oauth,
        sourcePathHint: '~/.codex/auth.json',
        accountHint: 'corcton@qiniu.com',
        valid: true,
        alreadyImported: returning,
      ),
      const ImportableCredentialCandidate(
        id: 'cand-claude-oauth-token',
        provider: 'anthropic',
        kind: CredentialKind.oauth,
        sourcePathHint: '钥匙串中的 Claude Code 登录',
        accountHint: 'corcton@qiniu.com',
        valid: true,
      ),
      ImportableCredentialCandidate(
        id: 'cand-gh-hosts-yml',
        provider: 'github',
        kind: CredentialKind.pat,
        sourcePathHint: '~/.config/gh/hosts.yml',
        accountHint: 'github.com/corcton',
        valid: true,
        alreadyImported: returning,
      ),
      const ImportableCredentialCandidate(
        id: 'cand-expired-gemini',
        provider: 'google',
        kind: CredentialKind.oauth,
        sourcePathHint: '~/.gemini/oauth_creds.json',
        accountHint: 'expired-session@example.com',
        valid: false,
      ),
    ]);

    if (!returning) {
      return;
    }

    _credentials.addAll([
      CredentialMetadata(
        id: 'cred-openai-oauth',
        name: 'ChatGPT 账号',
        provider: 'openai',
        kind: CredentialKind.oauth,
        source: CredentialSource.hostOauth,
        status: CredentialStatus.active,
        scopes: const ['model.request'],
        accountHint: 'corcton@qiniu.com',
        expiresAt: now.add(const Duration(hours: 18)),
        createdAt: now.subtract(const Duration(days: 5)),
        updatedAt: now.subtract(const Duration(minutes: 14)),
      ),
      CredentialMetadata(
        id: 'cred-anthropic-key',
        name: 'Anthropic API Key',
        provider: 'anthropic',
        kind: CredentialKind.apiKey,
        source: CredentialSource.userInput,
        status: CredentialStatus.active,
        scopes: const ['messages.write'],
        accountHint: '以 9xQ2 结尾',
        createdAt: now.subtract(const Duration(days: 12)),
        updatedAt: now.subtract(const Duration(days: 2)),
      ),
      CredentialMetadata(
        id: 'cred-github-pat',
        name: 'GitHub',
        provider: 'github',
        kind: CredentialKind.pat,
        source: CredentialSource.importedHostAuth,
        status: CredentialStatus.active,
        scopes: const ['repo:read', 'pull_request:write'],
        accountHint: 'github.com/corcton',
        expiresAt: now.add(const Duration(days: 24)),
        createdAt: now.subtract(const Duration(days: 7)),
        updatedAt: now.subtract(const Duration(hours: 6)),
      ),
    ]);

    _workspaces.addAll([
      WorkspaceRecord(
        id: 'ws-nullkey-core',
        name: 'nullkey-runtime',
        kind: WorkspaceKind.managed,
        hostPath: '/Users/corcton/Library/Application Support/Nullkey/workspaces/nullkey-runtime',
        defaultMode: MountMode.rw,
        createdAt: now.subtract(const Duration(days: 9)),
        lastUsedAt: now.subtract(const Duration(minutes: 4)),
        gitBranch: 'main',
      ),
      WorkspaceRecord(
        id: 'ws-ext-gateway',
        name: 'qiniu-edge-proxy',
        kind: WorkspaceKind.external,
        hostPath: '/Users/corcton/Work/qiniu/edge-proxy/',
        defaultMode: MountMode.rw,
        createdAt: now.subtract(const Duration(days: 4)),
        lastUsedAt: now.subtract(const Duration(hours: 2)),
        gitBranch: 'main',
        hasProjectEnvWarning: true,
      ),
      WorkspaceRecord(
        id: 'ws-ext-audit-ro',
        name: 'security-policies-ro',
        kind: WorkspaceKind.external,
        hostPath: '/Users/corcton/Work/qiniu/security-policies',
        defaultMode: MountMode.ro,
        createdAt: now.subtract(const Duration(days: 15)),
        lastUsedAt: now.subtract(const Duration(days: 1)),
        gitBranch: 'release/2026-q1',
      ),
    ]);

    _states.addAll([
      PersistentStateRecord(
        id: 'state-codex-main',
        name: 'nullkey-runtime · Codex',
        sizeBytes: 642 * 1024 * 1024,
        lockedBySandboxId: 'sbx-codex-primary',
        createdAt: now.subtract(const Duration(days: 9)),
        updatedAt: now.subtract(const Duration(minutes: 2)),
        summary: 'Codex 的配置、会话和历史',
      ),
      PersistentStateRecord(
        id: 'state-clean-room',
        name: 'qiniu-edge-proxy · Claude Code',
        sizeBytes: 184 * 1024 * 1024,
        createdAt: now.subtract(const Duration(days: 3)),
        updatedAt: now.subtract(const Duration(hours: 5)),
        summary: 'Claude Code 的配置、会话和历史',
      ),
    ]);

    _mcpServers.addAll(const [
      McpServerRecord(
        id: 'mcp-github-gateway',
        name: 'GitHub',
        transport: 'http',
        endpointSummary: '由 Nullkey 代为调用 GitHub API',
        credentialIds: ['cred-github-pat'],
        health: HealthState.healthy,
        toolsCount: 14,
        description: '读取仓库、Issue 和 Pull Request',
      ),
      McpServerRecord(
        id: 'mcp-filesystem-scoped',
        name: '工作区索引',
        transport: 'stdio',
        endpointSummary: '只能读取当前工作区',
        credentialIds: [],
        health: HealthState.healthy,
        toolsCount: 6,
        description: '为 Agent 提供代码符号搜索',
      ),
    ]);

    final primary = _workspaces[0];
    final secondary = _workspaces[1];

    _sandboxes.addAll([
      SandboxRecord(
        id: 'sbx-codex-primary',
        name: 'nullkey-runtime · Codex',
        status: SandboxStatus.running,
        harness: HarnessKind.codex,
        workspaceId: primary.id,
        workspaceMode: MountMode.rw,
        stateId: 'state-codex-main',
        networkMode: NetworkMode.servicesOnly,
        enableDesktop: false,
        createdAt: now.subtract(const Duration(minutes: 32)),
        startedAt: now.subtract(const Duration(minutes: 31)),
      ),
      SandboxRecord(
        id: 'sbx-edge-filtered',
        name: 'qiniu-edge-proxy · Claude Code',
        status: SandboxStatus.stopped,
        harness: HarnessKind.claudeCode,
        workspaceId: secondary.id,
        workspaceMode: MountMode.rw,
        stateId: 'state-clean-room',
        networkMode: NetworkMode.filtered,
        enableDesktop: false,
        createdAt: now.subtract(const Duration(hours: 3)),
        startedAt: now.subtract(const Duration(hours: 3)),
        stoppedAt: now.subtract(const Duration(hours: 2)),
        stopReason: 'user_stopped',
      ),
    ]);
    _seededTranscripts.add('sbx-codex-primary');

    _servicesBySandbox['sbx-codex-primary'] = const [
      SandboxServiceRecord(
        id: 'svc-preview',
        sandboxId: 'sbx-codex-primary',
        name: '网页预览',
        status: ServiceStatus.ready,
        websocket: true,
      ),
      SandboxServiceRecord(
        id: 'svc-docs',
        sandboxId: 'sbx-codex-primary',
        name: 'API 文档',
        status: ServiceStatus.starting,
        websocket: false,
      ),
    ];

    _desktopBySandbox['sbx-codex-primary'] = const DesktopStatusRecord(
      enabled: false,
      status: 'stopped',
      resolution: '1440×900',
      fps: 60,
    );

    _artifactsBySandbox['sbx-codex-primary'] = [
      ArtifactRecord(
        id: 'art-diff-01',
        sandboxId: 'sbx-codex-primary',
        relativePath: 'internal/gateway/egress.go',
        kind: 'diff',
        sizeBytes: 6840,
        summary: '拒绝白名单外的出网请求，并记录被拒绝的地址',
        updatedAt: now.subtract(const Duration(minutes: 6)),
        previewSnippet: '''
@@ -42,6 +42,11 @@ func (p *Proxy) EnforceDomainPolicy(sandboxID, host string) error {
     mode := p.policy.Mode(sandboxID)
+    if !p.policy.Allows(sandboxID, host) {
+        p.audit.NetworkBlocked(sandboxID, host, mode)
+        return ErrNetworkDenied
+    }
-    log.Printf("allow %s", host)
     return nil
 }''',
      ),
      ArtifactRecord(
        id: 'art-test-02',
        sandboxId: 'sbx-codex-primary',
        relativePath: 'internal/gateway/egress_test.go',
        kind: 'diff',
        sizeBytes: 2190,
        summary: '新增被拒绝地址的测试',
        updatedAt: now.subtract(const Duration(minutes: 5)),
        previewSnippet: '''
@@ -0,0 +1,14 @@
+func TestDeniedHostIsAudited(t *testing.T) {
+    p := newTestProxy(t, ModeFiltered)
+    err := p.EnforceDomainPolicy("sbx-1", "evil.example")
+    if !errors.Is(err, ErrNetworkDenied) {
+        t.Fatalf("want ErrNetworkDenied, got %v", err)
+    }
+    if got := p.audit.Last().Host; got != "evil.example" {
+        t.Fatalf("audited host = %q", got)
+    }
+}''',
      ),
      ArtifactRecord(
        id: 'art-notes-03',
        sandboxId: 'sbx-codex-primary',
        relativePath: 'docs/egress-notes.md',
        kind: 'file',
        sizeBytes: 3420,
        summary: '本次改动的说明',
        updatedAt: now.subtract(const Duration(minutes: 1)),
        previewSnippet: '''
# 出网策略

- filtered 模式只放行白名单域名。
- 被拒绝的请求会写入活动记录，包含目标地址和当时的网络模式。
- 不会因为请求被拒绝而自动切换到 open。''',
      ),
    ];

    _auditEvents.addAll([
      AuditEventRecord(
        id: 'aud-seed-1',
        timestamp: now.subtract(const Duration(minutes: 12)),
        type: 'gateway.model',
        sandboxId: 'sbx-codex-primary',
        credentialId: 'cred-openai-oauth',
        provider: 'openai',
        method: 'POST',
        host: 'api.openai.com',
        path: '/v1/responses',
        status: 200,
        latencyMs: 384,
        message: '网关代为附加 ChatGPT 账号凭证',
      ),
      AuditEventRecord(
        id: 'aud-seed-2',
        timestamp: now.subtract(const Duration(minutes: 9)),
        type: 'mcp.call',
        sandboxId: 'sbx-codex-primary',
        credentialId: 'cred-github-pat',
        provider: 'github',
        method: 'POST',
        host: 'api.github.com',
        path: '/graphql',
        status: 200,
        latencyMs: 142,
        message: 'GitHub MCP 读取 Pull Request',
      ),
      AuditEventRecord(
        id: 'aud-seed-3',
        timestamp: now.subtract(const Duration(minutes: 31)),
        type: 'sandbox.started',
        sandboxId: 'sbx-codex-primary',
        message: '启动沙箱 nullkey-runtime · Codex',
      ),
    ]);
  }

  void setDesktopAvailableCapability(bool available) {
    _capabilities = _capabilities.copyWith(desktopAvailable: available);
    _emit();
  }

  // ---------------------------------------------------------------------------
  // Harnesses & Jobs
  // ---------------------------------------------------------------------------

  @override
  List<HarnessInfo> listHarnesses() => List.unmodifiable(_harnesses);

  @override
  HarnessInfo? getHarness(HarnessKind kind) =>
      _harnesses.where((h) => h.kind == kind).firstOrNull;

  @override
  List<JobInfo> listJobs() {
    final list = _jobs.values.toList()
      ..sort((a, b) => b.createdAt.compareTo(a.createdAt));
    return List.unmodifiable(list);
  }

  @override
  JobInfo? getJob(String id) => _jobs[id];

  @override
  Future<JobInfo> startHarnessInstall(
    HarnessKind kind, {
    bool isUpdate = false,
  }) async {
    final existing = getHarness(kind);
    final activeJob = existing?.activeJobId == null
        ? null
        : _jobs[existing!.activeJobId];
    if (activeJob != null && activeJob.status.isActive) {
      return activeJob;
    }

    final now = DateTime.now().toUtc();
    final jobId = _nextId('job');
    final job = JobInfo(
      id: jobId,
      kind: isUpdate ? JobKind.harnessUpdate : JobKind.harnessInstall,
      status: JobStatus.running,
      progress: 0.05,
      message: _installStage(kind, 0.05),
      targetId: kind.wireName,
      createdAt: now,
      updatedAt: now,
    );
    _jobs[jobId] = job;

    final idx = _harnesses.indexWhere((h) => h.kind == kind);
    if (idx != -1) {
      _harnesses[idx] = _harnesses[idx].copyWith(
        status: HarnessStatus.installing,
        activeJobId: jobId,
      );
    }

    _recordAudit(
      type: 'job.created',
      message: '${isUpdate ? '更新' : '安装'} ${kind.displayName}',
    );
    _ensureJobTicker();
    return job;
  }

  String _installStage(HarnessKind kind, double progress) {
    final version = _pinnedVersions[kind] ?? '';
    if (progress < 0.5) {
      return '正在下载 ${kind.displayName} $version';
    }
    if (progress < 0.8) {
      return '正在校验文件';
    }
    return '正在安装';
  }

  /// Moves a running job forward; at 1.0 it succeeds.
  JobInfo advanceJobProgress(String jobId, {double? toProgress}) {
    final existing = _jobs[jobId];
    if (existing == null) {
      throw _notFound('找不到这个任务。');
    }
    if (existing.status != JobStatus.running) {
      return existing;
    }

    final nextProgress = (toProgress ?? (existing.progress + 0.35)).clamp(
      0.0,
      1.0,
    );
    if (nextProgress >= 1.0) {
      final completed = existing.copyWith(
        status: JobStatus.succeeded,
        progress: 1.0,
        message: '已安装',
        updatedAt: DateTime.now().toUtc(),
      );
      _jobs[jobId] = completed;
      _finalizeHarnessForJob(completed, succeeded: true);
      _emit();
      return completed;
    }

    final kind = HarnessKind.values.firstWhere(
      (k) => k.wireName == existing.targetId,
      orElse: () => HarnessKind.codex,
    );
    final updated = existing.copyWith(
      progress: nextProgress,
      message: _installStage(kind, nextProgress),
      updatedAt: DateTime.now().toUtc(),
    );
    _jobs[jobId] = updated;
    _emit();
    return updated;
  }

  @override
  Future<JobInfo> requestCancelJob(String jobId) async {
    final existing = _jobs[jobId];
    if (existing == null) {
      throw _notFound('找不到这个任务。');
    }

    if (simulateJobCompleteBeforeCancel) {
      simulateJobCompleteBeforeCancel = false;
      final completed = existing.copyWith(
        status: JobStatus.succeeded,
        progress: 1.0,
        message: '已安装',
        updatedAt: DateTime.now().toUtc(),
      );
      _jobs[jobId] = completed;
      _finalizeHarnessForJob(completed, succeeded: true);
      throw const NullkeyApiException(
        statusCode: 409,
        code: 'conflict',
        message: '取消前安装已经完成。',
      );
    }

    if (existing.status == JobStatus.cancelling) {
      return existing;
    }
    if (!existing.status.canRequestCancel) {
      throw NullkeyApiException(
        statusCode: 409,
        code: 'conflict',
        message: '任务已经${existing.status.label}，不能再取消。',
      );
    }

    final cancelling = existing.copyWith(
      status: JobStatus.cancelling,
      message: '正在删除未完成的文件',
      updatedAt: DateTime.now().toUtc(),
    );
    _jobs[jobId] = cancelling;
    _cancellingTicks[jobId] = 0;
    _recordAudit(type: 'job.cancelling', message: '请求取消任务 $jobId');
    _ensureJobTicker();
    return cancelling;
  }

  /// Finishes the cleanup of a job in `cancelling`.
  JobInfo finalizeCancelJob(String jobId) {
    final existing = _jobs[jobId];
    if (existing == null) {
      throw _notFound('找不到这个任务。');
    }
    if (!existing.status.isActive) {
      return existing;
    }

    final cancelled = existing.copyWith(
      status: JobStatus.cancelled,
      message: existing.kind == JobKind.harnessUpdate
          ? '已取消更新，原版本仍可使用'
          : '已取消安装，未留下文件',
      updatedAt: DateTime.now().toUtc(),
    );
    _jobs[jobId] = cancelled;
    _cancellingTicks.remove(jobId);
    _finalizeHarnessForJob(cancelled, succeeded: false);
    _recordAudit(type: 'job.cancelled', message: '任务 $jobId 已取消');
    _emit();
    return cancelled;
  }

  void _ensureJobTicker() {
    if (simulatedLatency == Duration.zero || _jobTicker != null) {
      return;
    }
    _jobTicker = Timer.periodic(simulatedLatency, (_) => _tickJobs());
  }

  void _tickJobs() {
    var anyActive = false;
    for (final job in _jobs.values.toList()) {
      if (job.status == JobStatus.running) {
        advanceJobProgress(job.id, toProgress: job.progress + 0.09);
        anyActive = true;
      } else if (job.status == JobStatus.cancelling) {
        final ticks = (_cancellingTicks[job.id] ?? 0) + 1;
        _cancellingTicks[job.id] = ticks;
        if (ticks >= 3) {
          finalizeCancelJob(job.id);
        } else {
          anyActive = true;
        }
      }
    }
    if (!anyActive) {
      _jobTicker?.cancel();
      _jobTicker = null;
    }
    _emit();
  }

  void _finalizeHarnessForJob(JobInfo job, {required bool succeeded}) {
    final idx = _harnesses.indexWhere((h) => h.kind.wireName == job.targetId);
    if (idx == -1) {
      return;
    }
    final current = _harnesses[idx];
    if (succeeded) {
      _harnesses[idx] = current.copyWith(
        status: HarnessStatus.installed,
        version: _pinnedVersions[current.kind] ?? current.version,
        clearActiveJob: true,
      );
      return;
    }
    final isUpdate = job.kind == JobKind.harnessUpdate;
    _harnesses[idx] = HarnessInfo(
      kind: current.kind,
      name: current.name,
      status: isUpdate
          ? HarnessStatus.updateAvailable
          : HarnessStatus.notInstalled,
      version: isUpdate ? current.version : null,
      executable: current.executable,
      requiredProviders: current.requiredProviders,
      description: current.description,
    );
  }

  // ---------------------------------------------------------------------------
  // Credentials & OAuth
  // ---------------------------------------------------------------------------

  @override
  List<CredentialMetadata> listCredentials() => List.unmodifiable(_credentials);

  @override
  List<ImportableCredentialCandidate> listImportableCredentials() =>
      List.unmodifiable(_importableCandidates);

  @override
  List<OAuthPendingFlow> listOAuthFlows() {
    final list = _oauthFlows.values.toList()
      ..sort((a, b) => b.startedAt.compareTo(a.startedAt));
    return List.unmodifiable(list);
  }

  @override
  Future<CredentialMetadata> createCredential({
    required String name,
    required String provider,
    required CredentialKind kind,
    required String secretValue,
  }) async {
    if (name.trim().isEmpty ||
        provider.trim().isEmpty ||
        secretValue.trim().isEmpty) {
      throw const NullkeyApiException(
        statusCode: 400,
        code: 'invalid_request',
        message: '请填写名称、服务和密钥。',
      );
    }

    final trimmedSecret = secretValue.trim();
    final suffix = trimmedSecret.length >= 4
        ? trimmedSecret.substring(trimmedSecret.length - 4)
        : '';
    final now = DateTime.now().toUtc();
    final metadata = CredentialMetadata(
      id: _nextId('cred'),
      name: name.trim(),
      provider: provider.trim().toLowerCase(),
      kind: kind,
      source: CredentialSource.userInput,
      status: CredentialStatus.active,
      scopes: const [],
      accountHint: '以 $suffix 结尾',
      createdAt: now,
      updatedAt: now,
    );
    _credentials.insert(0, metadata);
    _recordAudit(
      type: 'credential.created',
      credentialId: metadata.id,
      provider: metadata.provider,
      message: '添加凭证 ${metadata.name}',
    );
    return metadata;
  }

  @override
  Future<void> deleteCredential(String id) async {
    final idx = _credentials.indexWhere((c) => c.id == id);
    if (idx == -1) {
      throw _notFound('找不到这个凭证，可能已被撤销。');
    }
    final removed = _credentials.removeAt(idx);
    _recordAudit(
      type: 'credential.deleted',
      credentialId: removed.id,
      provider: removed.provider,
      message: '撤销凭证 ${removed.name}',
    );
  }

  @override
  Future<ImportCredentialsResult> importHostCredentials(
    List<String> candidateIds,
  ) async {
    final imported = <CredentialMetadata>[];
    final skipped = <ImportSkippedItem>[];
    final now = DateTime.now().toUtc();

    for (final candidateId in candidateIds) {
      final idx = _importableCandidates.indexWhere((c) => c.id == candidateId);
      if (idx == -1) {
        skipped.add(ImportSkippedItem(id: candidateId, reason: '没有找到'));
        continue;
      }
      final candidate = _importableCandidates[idx];
      if (!candidate.valid) {
        skipped.add(
          ImportSkippedItem(
            id: candidate.id,
            reason: '${candidate.sourcePathHint} 里的登录已过期',
          ),
        );
        continue;
      }
      if (candidate.alreadyImported) {
        skipped.add(ImportSkippedItem(id: candidate.id, reason: '之前已经导入'));
        continue;
      }

      final created = _upsertProviderCredential(
        provider: candidate.provider,
        kind: candidate.kind,
        source: CredentialSource.importedHostAuth,
        accountHint: candidate.accountHint,
        expiresAt: now.add(const Duration(days: 14)),
      );
      _importableCandidates[idx] = candidate.copyWith(alreadyImported: true);
      imported.add(created);
      _recordAudit(
        type: 'credential.imported',
        credentialId: created.id,
        provider: created.provider,
        message: '从 ${candidate.sourcePathHint} 导入登录',
      );
    }

    return ImportCredentialsResult(imported: imported, skipped: skipped);
  }

  /// Signing in again replaces the provider's previous login instead of
  /// piling up duplicates; this is what "reconnect" means to the user.
  CredentialMetadata _upsertProviderCredential({
    required String provider,
    required CredentialKind kind,
    required CredentialSource source,
    required String accountHint,
    DateTime? expiresAt,
  }) {
    final now = DateTime.now().toUtc();
    final idx = _credentials.indexWhere(
      (c) => c.provider == provider && c.kind == kind,
    );
    final cred = CredentialMetadata(
      id: idx == -1 ? _nextId('cred') : _credentials[idx].id,
      name: idx == -1 ? providerAccountName(provider) : _credentials[idx].name,
      provider: provider,
      kind: kind,
      source: source,
      status: CredentialStatus.active,
      scopes: const ['model.request'],
      accountHint: accountHint,
      expiresAt: expiresAt,
      createdAt: idx == -1 ? now : _credentials[idx].createdAt,
      updatedAt: now,
    );
    if (idx == -1) {
      _credentials.insert(0, cred);
    } else {
      _credentials[idx] = cred;
    }
    return cred;
  }

  /// A newer flow for the same provider invalidates the older one so only
  /// the latest browser window can finish sign-in.
  @override
  Future<OAuthPendingFlow> startOAuthFlow({required String provider}) async {
    final normalizedProvider = provider.trim().toLowerCase();
    for (final entry in _oauthFlows.entries.toList()) {
      if (entry.value.provider == normalizedProvider &&
          entry.value.status == OAuthFlowStatus.authorizing) {
        _oauthFlows[entry.key] = entry.value.copyWith(
          status: OAuthFlowStatus.cancelled,
          errorMessage: '已被新的登录请求替代',
        );
        _oauthCallbackTimers.remove(entry.key)?.cancel();
      }
    }

    final flowId = _nextId('oauth-flow');
    final stateToken = 'st_${DateTime.now().microsecondsSinceEpoch}';
    final flow = OAuthPendingFlow(
      flowId: flowId,
      provider: normalizedProvider,
      authUrl:
          'https://auth.$normalizedProvider.com/oauth/authorize?client_id=nullkey-macos&state=$stateToken&code_challenge_method=S256',
      state: stateToken,
      status: OAuthFlowStatus.authorizing,
      startedAt: DateTime.now().toUtc(),
    );
    _oauthFlows[flowId] = flow;
    _recordAudit(
      type: 'oauth.started',
      provider: normalizedProvider,
      message: '在浏览器中登录 ${providerAccountName(normalizedProvider)}',
    );

    if (simulatedLatency > Duration.zero) {
      // Stands in for the user finishing sign-in in the browser.
      _oauthCallbackTimers[flowId] = Timer(simulatedLatency * 8, () {
        _oauthCallbackTimers.remove(flowId);
        if (_oauthFlows[flowId]?.status == OAuthFlowStatus.authorizing) {
          completeOAuthFlowCallback(flowId);
          _emit();
        }
      });
    }
    return flow;
  }

  /// Invalidates `state` so a late browser callback cannot save a
  /// credential.
  @override
  Future<OAuthPendingFlow> cancelOAuthFlow(String flowId) async {
    final existing = _oauthFlows[flowId];
    if (existing == null) {
      throw _notFound('找不到这次登录。');
    }

    if (simulateOAuthCompleteBeforeCancel) {
      simulateOAuthCompleteBeforeCancel = false;
      final completed = completeOAuthFlowCallback(flowId);
      throw NullkeyApiException(
        statusCode: 409,
        code: 'conflict',
        message: '取消前浏览器已完成登录，账号已连接。',
        details: {'credential_id': completed.credentialId},
      );
    }

    if (existing.status == OAuthFlowStatus.succeeded) {
      throw NullkeyApiException(
        statusCode: 409,
        code: 'conflict',
        message: '登录已经完成，账号已连接。',
        details: {'credential_id': existing.credentialId},
      );
    }

    _oauthCallbackTimers.remove(flowId)?.cancel();
    final cancelled = existing.copyWith(
      status: OAuthFlowStatus.cancelled,
      errorMessage: '已取消',
    );
    _oauthFlows[flowId] = cancelled;
    _recordAudit(
      type: 'oauth.cancelled',
      provider: existing.provider,
      message: '取消登录 ${providerAccountName(existing.provider)}',
    );
    return cancelled;
  }

  /// The browser returning to the loopback callback. Rejected once the flow
  /// was cancelled.
  OAuthPendingFlow completeOAuthFlowCallback(String flowId) {
    final existing = _oauthFlows[flowId];
    if (existing == null) {
      throw _notFound('找不到这次登录。');
    }

    if (existing.status == OAuthFlowStatus.cancelled) {
      _recordAudit(
        type: 'oauth.late_callback_rejected',
        provider: existing.provider,
        status: 409,
        message: '拒绝已取消登录的浏览器回调，没有保存凭证',
      );
      throw const NullkeyApiException(
        statusCode: 409,
        code: 'conflict',
        message: '这次登录已取消，浏览器返回的结果没有保存。',
      );
    }
    if (existing.status == OAuthFlowStatus.succeeded) {
      return existing;
    }

    final cred = _upsertProviderCredential(
      provider: existing.provider,
      kind: CredentialKind.oauth,
      source: CredentialSource.hostOauth,
      accountHint: 'corcton@qiniu.com',
      expiresAt: DateTime.now().toUtc().add(const Duration(hours: 24)),
    );
    final succeeded = existing.copyWith(
      status: OAuthFlowStatus.succeeded,
      credentialId: cred.id,
    );
    _oauthFlows[flowId] = succeeded;
    _recordAudit(
      type: 'oauth.succeeded',
      credentialId: cred.id,
      provider: existing.provider,
      status: 200,
      message: '已连接 ${cred.name}',
    );
    return succeeded;
  }

  @override
  List<McpServerRecord> listMcpServers() => List.unmodifiable(_mcpServers);

  // ---------------------------------------------------------------------------
  // Workspaces & States
  // ---------------------------------------------------------------------------

  @override
  List<WorkspaceRecord> listWorkspaces() => List.unmodifiable(_workspaces);

  @override
  Future<WorkspaceRecord> createWorkspace({
    required String name,
    required WorkspaceKind kind,
    String? hostPath,
    MountMode defaultMode = MountMode.rw,
  }) async {
    final trimmedName = name.trim();
    if (trimmedName.isEmpty) {
      throw const NullkeyApiException(
        statusCode: 400,
        code: 'invalid_request',
        message: '请输入工作区名称。',
      );
    }
    if (_workspaces.any((w) => w.name == trimmedName)) {
      throw NullkeyApiException(
        statusCode: 409,
        code: 'conflict',
        message: '已经有名为“$trimmedName”的工作区。',
      );
    }

    final String resolvedPath;
    if (kind == WorkspaceKind.managed) {
      resolvedPath =
          '/Users/corcton/Library/Application Support/Nullkey/workspaces/$trimmedName';
    } else {
      final rawPath = (hostPath ?? '').trim();
      if (rawPath.isEmpty || !rawPath.startsWith('/')) {
        throw const NullkeyApiException(
          statusCode: 400,
          code: 'invalid_request',
          message: '请填写以 / 开头的完整路径。',
        );
      }
      if (rawPath.split('/').contains('..')) {
        throw const NullkeyApiException(
          statusCode: 400,
          code: 'invalid_request',
          message: '路径不能包含 ..。',
        );
      }
      resolvedPath = rawPath;
    }

    final now = DateTime.now().toUtc();
    final ws = WorkspaceRecord(
      id: _nextId('ws'),
      name: trimmedName,
      kind: kind,
      hostPath: resolvedPath,
      defaultMode: defaultMode,
      createdAt: now,
      lastUsedAt: now,
      hasProjectEnvWarning: kind == WorkspaceKind.external,
    );
    _workspaces.insert(0, ws);
    _recordAudit(
      type: 'workspace.created',
      message: '添加工作区 ${ws.name}（${ws.defaultMode.label}）',
    );
    return ws;
  }

  /// External folders can only be removed from the list; managed folders
  /// need the exact name.
  @override
  Future<void> deleteWorkspace(
    String id, {
    bool deleteFiles = false,
    String? confirmName,
  }) async {
    final idx = _workspaces.indexWhere((w) => w.id == id);
    if (idx == -1) {
      throw _notFound('找不到这个工作区。');
    }
    final ws = _workspaces[idx];

    final activeSandbox = _sandboxes
        .where((s) => s.workspaceId == id && s.status.isRunningOrStarting)
        .firstOrNull;
    if (activeSandbox != null) {
      throw NullkeyApiException(
        statusCode: 409,
        code: 'state_locked',
        message: '“${ws.name}”正在被沙箱使用，请先停止沙箱。',
        details: {'sandbox_id': activeSandbox.id, 'workspace_id': id},
      );
    }

    if (ws.kind == WorkspaceKind.external && deleteFiles) {
      throw const NullkeyApiException(
        statusCode: 400,
        code: 'invalid_request',
        message: '已有文件夹不会被删除，只能从列表移除。',
      );
    }

    if (ws.kind == WorkspaceKind.managed &&
        deleteFiles &&
        confirmName?.trim() != ws.name) {
      throw NullkeyApiException(
        statusCode: 400,
        code: 'invalid_request',
        message: '名称不一致。请输入“${ws.name}”确认删除。',
      );
    }

    _workspaces.removeAt(idx);
    _recordAudit(
      type: 'workspace.deleted',
      message: deleteFiles
          ? '删除工作区 ${ws.name} 及目录 ${ws.normalizedPath}'
          : '从列表移除工作区 ${ws.name}，文件保留',
    );
  }

  @override
  List<PersistentStateRecord> listStates() => List.unmodifiable(_states);

  PersistentStateRecord _createState({
    required String name,
    required String summary,
  }) {
    final now = DateTime.now().toUtc();
    final record = PersistentStateRecord(
      id: _nextId('state'),
      name: name,
      sizeBytes: 4 * 1024 * 1024,
      createdAt: now,
      updatedAt: now,
      summary: summary,
    );
    _states.insert(0, record);
    return record;
  }

  @override
  Future<PersistentStateRecord> resetState(String stateId) async {
    final idx = _states.indexWhere((s) => s.id == stateId);
    if (idx == -1) {
      throw _notFound('找不到这份会话数据。');
    }
    final current = _states[idx];
    if (current.lockedBySandboxId != null) {
      throw NullkeyApiException(
        statusCode: 409,
        code: 'state_locked',
        message: '会话数据正在被使用，请先停止沙箱。',
        details: {
          'sandbox_id': current.lockedBySandboxId,
          'state_id': current.id,
        },
      );
    }

    final reset = current.copyWith(
      sizeBytes: 1024 * 1024,
      updatedAt: DateTime.now().toUtc(),
    );
    _states[idx] = reset;
    _recordAudit(type: 'state.reset', message: '清除会话数据 ${reset.name}');
    return reset;
  }

  // ---------------------------------------------------------------------------
  // Sandboxes & Terminal
  // ---------------------------------------------------------------------------

  @override
  List<SandboxRecord> listSandboxes() => List.unmodifiable(_sandboxes);

  @override
  SandboxRecord? getSandbox(String id) =>
      _sandboxes.where((s) => s.id == id).firstOrNull;

  @override
  Future<SandboxRecord> createSandbox({
    required HarnessKind harness,
    required String workspaceId,
    required MountMode workspaceMode,
    String? stateId,
    NetworkMode networkMode = NetworkMode.filtered,
    bool enableDesktop = false,
  }) async {
    final ws = _workspaces.where((w) => w.id == workspaceId).firstOrNull;
    if (ws == null) {
      throw _notFound('找不到这个工作区。');
    }

    final info = getHarness(harness);
    if (harness != HarnessKind.custom &&
        (info == null ||
            info.status == HarnessStatus.notInstalled ||
            info.status == HarnessStatus.installing)) {
      throw NullkeyApiException(
        statusCode: 409,
        code: 'harness_not_installed',
        message: '${harness.displayName} 还没有安装。',
        details: {'harness_id': harness.wireName},
      );
    }

    final providers = info?.requiredProviders ?? const <String>[];
    if (providers.isNotEmpty &&
        !_credentials.any(
          (c) =>
              providers.contains(c.provider) &&
              c.status == CredentialStatus.active,
        )) {
      final provider = providers.first;
      throw NullkeyApiException(
        statusCode: 401,
        code: 'reauth_required',
        message: '${providerAccountName(provider)}的登录已失效，需要重新连接。',
        details: {'provider': provider},
      );
    }

    final rwHolder = workspaceMode == MountMode.rw
        ? _sandboxes
              .where(
                (s) =>
                    s.workspaceId == workspaceId &&
                    s.workspaceMode == MountMode.rw &&
                    s.status.isRunningOrStarting,
              )
              .firstOrNull
        : null;
    if (rwHolder != null) {
      throw NullkeyApiException(
        statusCode: 409,
        code: 'state_locked',
        message: '“${ws.name}”正在被另一个沙箱以读写方式使用。',
        details: {'sandbox_id': rwHolder.id, 'workspace_id': workspaceId},
      );
    }

    if (stateId != null) {
      final state = _states.where((s) => s.id == stateId).firstOrNull;
      if (state == null) {
        throw _notFound('找不到这份会话数据。');
      }
      if (state.lockedBySandboxId != null) {
        throw NullkeyApiException(
          statusCode: 409,
          code: 'state_locked',
          message: '会话数据“${state.name}”正在被另一个沙箱使用。',
          details: {
            'sandbox_id': state.lockedBySandboxId,
            'state_id': state.id,
          },
        );
      }
    }

    final now = DateTime.now().toUtc();
    final sandboxId = _nextId('sbx');

    // Without an explicit state the daemon gives the sandbox fresh session
    // data, so a later start of the same workspace can continue from it.
    final effectiveStateId =
        stateId ??
        _createState(
          name: '${ws.name} · ${harness.displayName}',
          summary: '${harness.displayName} 的配置、会话和历史',
        ).id;
    final stateIdx = _states.indexWhere((s) => s.id == effectiveStateId);
    _states[stateIdx] = _states[stateIdx].copyWith(
      lockedBySandboxId: sandboxId,
      updatedAt: now,
    );
    final wsIdx = _workspaces.indexWhere((w) => w.id == workspaceId);
    _workspaces[wsIdx] = ws.copyWith(lastUsedAt: now);

    final record = SandboxRecord(
      id: sandboxId,
      name: '${ws.name} · ${harness.displayName}',
      status: SandboxStatus.starting,
      harness: harness,
      workspaceId: ws.id,
      workspaceMode: workspaceMode,
      stateId: effectiveStateId,
      networkMode: networkMode,
      enableDesktop: enableDesktop && _capabilities.desktopAvailable,
      createdAt: now,
      startedAt: now,
    );

    _sandboxes.insert(0, record);
    _servicesBySandbox[sandboxId] = [];
    _desktopBySandbox[sandboxId] = DesktopStatusRecord(
      enabled: record.enableDesktop,
      status: record.enableDesktop ? 'running' : 'stopped',
      viewerUrl: record.enableDesktop
          ? 'http://127.0.0.1:49153/desktop/$sandboxId'
          : null,
      resolution: '1440×900',
      fps: 60,
    );
    _artifactsBySandbox[sandboxId] = [];

    _recordAudit(
      type: 'sandbox.started',
      sandboxId: sandboxId,
      message: '启动沙箱 ${record.name}（${networkMode.label}）',
    );
    return record;
  }

  @override
  Future<SandboxRecord?> waitUntilRunning(String sandboxId) async {
    if (simulatedLatency > Duration.zero) {
      await Future<void>.delayed(simulatedLatency * 2);
    }
    final idx = _sandboxes.indexWhere((s) => s.id == sandboxId);
    if (idx == -1) {
      return null;
    }
    if (_sandboxes[idx].status == SandboxStatus.starting) {
      _sandboxes[idx] = _sandboxes[idx].copyWith(status: SandboxStatus.running);
    }
    return _sandboxes[idx];
  }

  @override
  Future<SandboxRecord> stopSandbox(String sandboxId) async =>
      _stop(sandboxId, reason: 'user_stopped');

  SandboxRecord _stop(String sandboxId, {required String reason}) {
    final idx = _sandboxes.indexWhere((s) => s.id == sandboxId);
    if (idx == -1) {
      throw _notFound('找不到这个沙箱。');
    }
    final current = _sandboxes[idx];
    final now = DateTime.now().toUtc();

    if (current.stateId != null) {
      final sIdx = _states.indexWhere((st) => st.id == current.stateId);
      if (sIdx != -1 && _states[sIdx].lockedBySandboxId == sandboxId) {
        _states[sIdx] = _states[sIdx].copyWith(clearLock: true, updatedAt: now);
      }
    }

    final stopped = current.copyWith(
      status: SandboxStatus.stopped,
      stoppedAt: now,
      stopReason: reason,
    );
    _sandboxes[idx] = stopped;
    _servicesBySandbox[sandboxId] = [];
    _terminals.remove(sandboxId)?.drop('沙箱已停止');
    _recordAudit(
      type: 'sandbox.stopped',
      sandboxId: sandboxId,
      message: reason == 'daemon_restarted'
          ? 'Nullkey 重启，沙箱 ${stopped.name} 已中断'
          : '停止沙箱 ${stopped.name}，工作区和会话数据已保留',
    );
    return stopped;
  }

  @override
  Future<TerminalTicket> createTerminalTicket(String sandboxId) async {
    final sbx = getSandbox(sandboxId);
    if (sbx == null) {
      throw _notFound('找不到这个沙箱。');
    }
    if (sbx.status != SandboxStatus.running) {
      throw NullkeyApiException(
        statusCode: 409,
        code: sbx.status == SandboxStatus.stopped
            ? 'conflict'
            : 'terminal_not_ready',
        message: sbx.status == SandboxStatus.stopped
            ? '沙箱已停止。'
            : '终端还没准备好，请稍后再试。',
      );
    }
    final now = DateTime.now().toUtc();
    final ticket =
        'tkt_${sandboxId}_${now.microsecondsSinceEpoch}_${++_idCounter}';
    _openTickets[ticket] = sandboxId;
    return TerminalTicket(
      wsUrl: 'ws://127.0.0.1:49153/terminal',
      ticket: ticket,
      expiresAt: now.add(const Duration(seconds: 60)),
    );
  }

  @override
  Future<TerminalConnection> connectTerminal(TerminalTicket ticket) async {
    final sandboxId = _openTickets.remove(ticket.ticket);
    if (sandboxId == null || DateTime.now().toUtc().isAfter(ticket.expiresAt)) {
      throw const NullkeyApiException(
        statusCode: 401,
        code: 'ticket_invalid',
        message: '终端连接已失效，请重新连接。',
      );
    }
    final sbx = getSandbox(sandboxId)!;

    final Future<void> gate;
    if (holdNextTerminalReady) {
      holdNextTerminalReady = false;
      gate = (_heldTerminalReady = Completer<void>()).future;
    } else if (simulatedLatency > Duration.zero) {
      gate = Future<void>.delayed(simulatedLatency);
    } else {
      gate = Future<void>.value();
    }

    final firstAttach = _attachedOnce.add(sandboxId);
    final greeting = !firstAttach
        ? ''
        : _seededTranscripts.remove(sandboxId)
        ? _seedTranscript(sbx)
        : _banner(sbx);
    final conn = _MockTerminalConnection(
      sandbox: sbx,
      terminalId: 'term-$sandboxId',
      greeting: greeting,
      gate: gate,
    );
    // A new attach replaces the previous writable connection.
    _terminals.remove(sandboxId)?.replace();
    _terminals[sandboxId] = conn;
    return conn;
  }

  /// Lets a connection held by [holdNextTerminalReady] send `ready`.
  void releaseHeldTerminalReady() {
    final held = _heldTerminalReady;
    _heldTerminalReady = null;
    if (held != null && !held.isCompleted) {
      held.complete();
    }
  }

  String _banner(SandboxRecord sbx) {
    final version = getHarness(sbx.harness)?.version ?? '';
    return '\x1b[1m>_ ${sbx.harness.displayName} $version\x1b[0m\r\n'
        '\x1b[2m   directory: /workspace\r\n'
        '   network:   ${sbx.networkMode.wireName}\x1b[0m\r\n\r\n';
  }

  String _seedTranscript(SandboxRecord sbx) => [
    _banner(sbx),
    '\x1b[1m› 检查 internal/gateway 的出网策略，补上被拒绝地址的记录\x1b[0m\r\n',
    '\r\n',
    '• 读取 internal/gateway/egress.go\r\n',
    '• 修改 internal/gateway/egress.go  \x1b[32m+4\x1b[0m \x1b[31m−1\x1b[0m\r\n',
    '• 新增 internal/gateway/egress_test.go  \x1b[32m+10\x1b[0m\r\n',
    '• 运行 go test ./internal/gateway/...\r\n',
    '\x1b[32m    ok   nullkey/internal/gateway   0.412s\x1b[0m\r\n',
    '\r\n',
    '白名单外的请求现在会被拒绝，并记录目标地址和当时的网络模式。\r\n',
    '改动在「产物」里可以查看。\r\n',
    '\r\n',
  ].join();

  // ---------------------------------------------------------------------------
  // Services, Desktop, Artifacts & Credential check
  // ---------------------------------------------------------------------------

  @override
  List<SandboxServiceRecord> listServices(String sandboxId) =>
      List.unmodifiable(_servicesBySandbox[sandboxId] ?? const []);

  @override
  Future<void> refreshServices(String sandboxId) async {}

  @override
  Future<Uri> openService(String sandboxId, String serviceId) async {
    final service = listServices(sandboxId)
        .where((s) => s.id == serviceId)
        .firstOrNull;
    if (service == null) {
      throw const NullkeyApiException(
        statusCode: 404,
        code: 'service_not_found',
        message: '沙箱没有声明这个服务。',
      );
    }
    return Uri.parse(
      'http://127.0.0.1:49153/tunnel?ticket=mock-${++_idCounter}',
    );
  }

  @override
  DesktopStatusRecord getDesktopStatus(String sandboxId) =>
      _desktopBySandbox[sandboxId] ??
      const DesktopStatusRecord(
        enabled: false,
        status: 'stopped',
        resolution: '1440×900',
        fps: 60,
      );

  @override
  Future<DesktopStatusRecord> setDesktopEnabled(
    String sandboxId,
    bool enabled,
  ) async {
    if (enabled && !_capabilities.desktopAvailable) {
      throw const NullkeyApiException(
        statusCode: 501,
        code: 'desktop_unavailable',
        message: '这台 Mac 没有启用虚拟桌面。',
      );
    }
    final next = DesktopStatusRecord(
      enabled: enabled,
      status: enabled ? 'running' : 'stopped',
      viewerUrl: enabled ? 'http://127.0.0.1:49153/desktop/$sandboxId' : null,
      resolution: '1440×900',
      fps: 60,
    );
    _desktopBySandbox[sandboxId] = next;
    _recordAudit(
      type: enabled ? 'desktop.enabled' : 'desktop.disabled',
      sandboxId: sandboxId,
      message: enabled ? '打开虚拟桌面' : '关闭虚拟桌面',
    );
    return next;
  }

  @override
  List<ArtifactRecord> listArtifacts(String sandboxId) =>
      List.unmodifiable(_artifactsBySandbox[sandboxId] ?? const []);

  @override
  Future<void> refreshArtifacts(String sandboxId) async {}

  @override
  Future<SecretScanResult> scanSandboxSecrets(String sandboxId) async {
    if (simulateNextScanFailure) {
      simulateNextScanFailure = false;
      throw const NullkeyApiException(
        statusCode: 500,
        code: 'internal',
        message: '无法读取沙箱的进程环境。',
      );
    }

    SecretScanCheck check(String name) => SecretScanCheck(
      name: name,
      clean: true,
      matches: 0,
      targetSummary: scanCheckLabel(name),
    );
    final result = SecretScanResult(
      clean: true,
      scope: 'generated_runtime',
      excluded: const ['workspace', 'user_imports', 'user_input'],
      checks: [
        check('generated_config'),
        check('state_files'),
        check('sandbox_env'),
      ],
      scannedAt: DateTime.now().toUtc(),
    );
    _recordAudit(
      type: 'secret.scan',
      sandboxId: sandboxId,
      message: result.headline,
    );
    return result;
  }

  // ---------------------------------------------------------------------------
  // Activity
  // ---------------------------------------------------------------------------

  @override
  List<AuditEventRecord> listAuditEvents() => List.unmodifiable(_auditEvents);

  @override
  Future<void> refreshAuditEvents() async {}

  void _recordAudit({
    required String type,
    required String message,
    String? sandboxId,
    String? credentialId,
    String? provider,
    String? host,
    int? status,
  }) {
    _auditEvents.insert(
      0,
      AuditEventRecord(
        id: _nextId('aud'),
        timestamp: DateTime.now().toUtc(),
        type: type,
        sandboxId: sandboxId,
        credentialId: credentialId,
        provider: provider,
        host: host,
        status: status,
        message: message,
      ),
    );
  }

  // ---------------------------------------------------------------------------
  // Scenarios
  // ---------------------------------------------------------------------------

  /// The egress proxy refused a connection. The network mode is never
  /// widened as a side effect.
  void simulateOutboundNetworkBlock({
    required String sandboxId,
    required String blockedHost,
  }) {
    final mode = getSandbox(sandboxId)?.networkMode ?? NetworkMode.servicesOnly;
    _recordAudit(
      type: 'network.blocked',
      sandboxId: sandboxId,
      host: blockedHost,
      status: 403,
      message: '拒绝访问 $blockedHost（${mode.label}）',
    );
    _emit();
  }

  /// `nullkeyd` restarted: running sandboxes lost their PTY, are reported as
  /// stopped and their locks are released. Workspace and state stay intact.
  void simulateDaemonRestartRecovery() {
    for (final sbx in _sandboxes.toList()) {
      if (sbx.status.isRunningOrStarting) {
        _stop(sbx.id, reason: 'daemon_restarted');
      }
    }
    _emit();
  }

  /// Another client attached to the sandbox's PTY.
  void simulateTerminalReplaced(String sandboxId) {
    _terminals.remove(sandboxId)?.replace();
  }

  /// The provider's login expired or was revoked upstream.
  void simulateCredentialExpired(String provider) {
    for (var i = 0; i < _credentials.length; i++) {
      if (_credentials[i].provider == provider) {
        _credentials[i] = _credentials[i].copyWith(
          status: CredentialStatus.expired,
          updatedAt: DateTime.now().toUtc(),
        );
      }
    }
    _recordAudit(
      type: 'credential.expired',
      provider: provider,
      message: '${providerAccountName(provider)}的登录已过期',
    );
    _emit();
  }
}

/// A line-buffered stand-in for an agent's PTY: it echoes what is typed and
/// answers `env`, so the terminal can be exercised without a real process.
class _MockTerminalConnection implements TerminalConnection {
  final SandboxRecord sandbox;
  final String terminalId;

  final _ready = Completer<String>();
  final _events = StreamController<TerminalEvent>();
  String _line = '';
  bool _ended = false;

  static const _prompt = '\x1b[1m›\x1b[0m ';

  _MockTerminalConnection({
    required this.sandbox,
    required this.terminalId,
    required String greeting,
    required Future<void> gate,
  }) {
    _ready.future.ignore();
    gate.then((_) {
      if (_ended) {
        return;
      }
      _ready.complete(terminalId);
      _events.add(TerminalOutput('$greeting$_prompt'));
    });
  }

  @override
  Future<String> get ready => _ready.future;

  @override
  Stream<TerminalEvent> get events => _events.stream;

  @override
  void sendInput(String data) {
    if (_ended || !_ready.isCompleted) {
      return;
    }
    // Arrow keys and other escape sequences have nothing to act on here.
    if (data.startsWith('\x1b')) {
      return;
    }
    final out = StringBuffer();
    for (final rune in data.runes) {
      final ch = String.fromCharCode(rune);
      if ((ch == '\r' || ch == '\n') && _line.trim() == 'exit') {
        _events.add(TerminalOutput('$out\r\n'));
        _end(const TerminalExited(0));
        return;
      } else if (ch == '\r' || ch == '\n') {
        out
          ..write('\r\n')
          ..write(_respond(_line.trim()))
          ..write(_prompt);
        _line = '';
      } else if (rune == 0x7f || rune == 0x08) {
        if (_line.isNotEmpty) {
          final runes = _line.runes.toList()..removeLast();
          _line = String.fromCharCodes(runes);
          out.write('\b \b');
        }
      } else if (rune >= 0x20) {
        _line += ch;
        out.write(ch);
      }
    }
    if (out.isNotEmpty) {
      _events.add(TerminalOutput(out.toString()));
    }
  }

  String _respond(String line) {
    if (line.isEmpty) {
      return '';
    }
    if (line == 'env' || line.startsWith('printenv')) {
      return [
        'HOME=/state/home',
        'NULLKEY_SANDBOX_ID=${sandbox.id}',
        'NULLKEY_TOKEN=nk_••••••••',
        'HTTPS_PROXY=http://127.0.0.1:49154',
        'PWD=/workspace',
        '',
      ].join('\r\n');
    }
    return '\x1b[2m（Core Mock：这里没有真实的 Agent，输入不会被执行）\x1b[0m\r\n';
  }

  @override
  void resize(int cols, int rows) {}

  void _end(TerminalEvent? event) {
    if (_ended) {
      return;
    }
    _ended = true;
    if (!_ready.isCompleted) {
      _ready.completeError(
        const NullkeyApiException(
          statusCode: 0,
          code: 'terminal_not_ready',
          message: '终端没有就绪就断开了。',
        ),
      );
    }
    if (event != null) {
      _events.add(event);
    }
    _events.close();
  }

  void replace() => _end(const TerminalReplaced());

  void drop(String reason) => _end(TerminalDropped(reason));

  @override
  Future<void> close() async => _end(null);
}
