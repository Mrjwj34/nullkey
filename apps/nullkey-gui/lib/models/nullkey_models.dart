import 'package:flutter/foundation.dart';

/// Error envelope returned by `nullkeyd` or the local contract engine.
@immutable
class NullkeyApiException implements Exception {
  final int statusCode;
  final String code;
  final String message;
  final Map<String, Object?> details;

  const NullkeyApiException({
    required this.statusCode,
    required this.code,
    required this.message,
    this.details = const {},
  });

  String? get lockingSandboxId => details['sandbox_id'] as String?;
  String? get blockedHost => details['host'] as String?;

  @override
  String toString() => 'NullkeyApiException($statusCode $code): $message';
}

enum DaemonConnectionMode {
  liveDaemon('本机 nullkeyd'),
  contractEngine('Core Mock 引擎');

  final String label;
  const DaemonConnectionMode(this.label);
}

/// How an account is named to the user, for provider ids the GUI uses
/// (`openai` covers both ChatGPT sign-in and OpenAI API keys).
String providerAccountName(String provider) => switch (provider) {
  'openai' => 'ChatGPT 账号',
  'anthropic' => 'Claude 账号',
  'github' => 'GitHub',
  'google' => 'Google 账号',
  _ => provider,
};

enum NetworkMode {
  servicesOnly(
    wireName: 'services_only',
    label: '仅允许已连接服务',
    summary: '只能访问已连接的模型、MCP 和 Git 服务。',
    highRisk: false,
  ),
  filtered(
    wireName: 'filtered',
    label: '限定网站',
    summary: '另外允许包管理源、公开文档等常用网站。',
    highRisk: false,
  ),
  open(
    wireName: 'open',
    label: '开放网络',
    summary: '可以访问互联网，本机、局域网和云元数据地址除外。',
    highRisk: true,
  );

  final String wireName;
  final String label;
  final String summary;
  final bool highRisk;

  const NetworkMode({
    required this.wireName,
    required this.label,
    required this.summary,
    required this.highRisk,
  });

  /// Every mode still reaches remote services through the gateway
  /// (`docs/10-interaction-contract.md` §2); none of them is offline.
  static const String remoteGatewayDisclosure =
      '以上都不是离线模式：模型、MCP 和 Git 始终经 Nullkey 网关联网。';

  static NetworkMode fromWire(String? raw) => NetworkMode.values.firstWhere(
    (m) => m.wireName == raw,
    orElse: () => NetworkMode.servicesOnly,
  );
}

@immutable
class DaemonCapabilities {
  final String version;
  final String os;
  final String arch;
  final String sandboxBackend;
  final bool desktopAvailable;
  final bool oauthImportAvailable;
  final List<NetworkMode> networkModes;
  final DaemonConnectionMode connectionMode;
  final String endpointUrl;

  /// Providers whose browser sign-in the daemon can broker right now.
  final Set<String> browserSignInProviders;

  /// Providers that accept a pasted API key or token, in display order.
  final List<String> apiKeyProviders;

  /// Whether a sandbox can mount its workspace read-only or read-write
  /// independently of the workspace's default.
  final bool perLaunchMountMode;

  const DaemonCapabilities({
    required this.version,
    required this.os,
    required this.arch,
    required this.sandboxBackend,
    required this.desktopAvailable,
    required this.oauthImportAvailable,
    required this.networkModes,
    required this.connectionMode,
    required this.endpointUrl,
    required this.browserSignInProviders,
    required this.apiKeyProviders,
    required this.perLaunchMountMode,
  });

  DaemonCapabilities copyWith({bool? desktopAvailable}) => DaemonCapabilities(
    version: version,
    os: os,
    arch: arch,
    sandboxBackend: sandboxBackend,
    desktopAvailable: desktopAvailable ?? this.desktopAvailable,
    oauthImportAvailable: oauthImportAvailable,
    networkModes: networkModes,
    connectionMode: connectionMode,
    endpointUrl: endpointUrl,
    browserSignInProviders: browserSignInProviders,
    apiKeyProviders: apiKeyProviders,
    perLaunchMountMode: perLaunchMountMode,
  );
}

enum PasskeyScope {
  vaultUnlock(id: 'vault.unlock', title: '解锁 Nullkey', reason: '解锁 Nullkey'),
  credentialMutation(
    id: 'credential.mutate',
    title: '更改凭证',
    reason: '添加、导入或撤销凭证',
  ),
  oauthBroker(id: 'oauth.broker', title: '登录账号', reason: '在浏览器中登录账号'),
  highRiskSandbox(
    id: 'sandbox.high_risk',
    title: '放宽沙箱限制',
    reason: '允许沙箱修改已有文件夹或访问开放网络',
  ),
  destructiveStorage(
    id: 'storage.destructive',
    title: '删除数据',
    reason: '删除工作区文件或清除会话数据',
  );

  final String id;
  final String title;
  final String reason;

  const PasskeyScope({
    required this.id,
    required this.title,
    required this.reason,
  });
}

@immutable
class PasskeyRegistration {
  final String credentialId;
  final String rpId;
  final String accountName;
  final String publicKeySpkiBase64;
  final String authenticatorAttachment;
  final bool backedBySecureEnclave;
  final String authMethod;
  final String createdAt;
  final int signCount;

  const PasskeyRegistration({
    required this.credentialId,
    required this.rpId,
    required this.accountName,
    required this.publicKeySpkiBase64,
    required this.authenticatorAttachment,
    required this.backedBySecureEnclave,
    required this.authMethod,
    required this.createdAt,
    required this.signCount,
  });

  factory PasskeyRegistration.fromMap(
    Map<Object?, Object?> map,
  ) => PasskeyRegistration(
    credentialId: (map['credentialId'] as String?) ?? 'nk-passkey-enclave-01',
    rpId: (map['rpId'] as String?) ?? 'local.nullkey.vault',
    accountName: (map['accountName'] as String?) ?? 'nullkey-local-operator',
    publicKeySpkiBase64:
        (map['publicKeySpkiBase64'] as String?) ??
        'MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE7NullkeySecureEnclaveP256PubKey=',
    authenticatorAttachment:
        (map['authenticatorAttachment'] as String?) ?? 'platform',
    backedBySecureEnclave: (map['backedBySecureEnclave'] as bool?) ?? false,
    authMethod: (map['authMethod'] as String?) ?? 'macOS 系统验证',
    createdAt:
        (map['createdAt'] as String?) ?? DateTime.now().toIso8601String(),
    signCount: (map['signCount'] as num?)?.toInt() ?? 1,
  );
}

@immutable
class PasskeyAssertion {
  final String credentialId;
  final String rpId;
  final PasskeyScope scope;
  final String challenge;
  final String authMethod;
  final String clientDataJSONBase64;
  final String authenticatorDataBase64;
  final String signatureBase64;
  final int signCount;
  final bool userVerified;
  final bool backedBySecureEnclave;
  final DateTime verifiedAt;
  final int latencyMs;

  const PasskeyAssertion({
    required this.credentialId,
    required this.rpId,
    required this.scope,
    required this.challenge,
    required this.authMethod,
    required this.clientDataJSONBase64,
    required this.authenticatorDataBase64,
    required this.signatureBase64,
    required this.signCount,
    required this.userVerified,
    required this.backedBySecureEnclave,
    required this.verifiedAt,
    required this.latencyMs,
  });

  String get shortSignature {
    if (signatureBase64.length <= 22) {
      return signatureBase64;
    }
    return '${signatureBase64.substring(0, 12)}…${signatureBase64.substring(signatureBase64.length - 8)}';
  }
}

@immutable
class PasskeyCapabilities {
  final String rpId;
  final bool biometryAvailable;
  final bool deviceOwnerAuthAvailable;
  final bool secureEnclaveAvailable;
  final String biometryType;
  final bool enrolled;
  final PasskeyRegistration? registration;

  const PasskeyCapabilities({
    required this.rpId,
    required this.biometryAvailable,
    required this.deviceOwnerAuthAvailable,
    required this.secureEnclaveAvailable,
    required this.biometryType,
    required this.enrolled,
    this.registration,
  });
}

enum HarnessKind {
  codex('codex', 'Codex', 'codex'),
  claudeCode('claude_code', 'Claude Code', 'claude'),
  geminiCli('gemini_cli', 'Gemini CLI', 'gemini'),
  opencode('opencode', 'OpenCode', 'opencode'),
  custom('custom', 'Shell', 'zsh -l');

  final String wireName;
  final String displayName;
  final String defaultCommand;
  const HarnessKind(this.wireName, this.displayName, this.defaultCommand);
}

enum HarnessStatus {
  notInstalled('not_installed', '未安装'),
  installing('installing', '安装中'),
  installed('installed', '已就绪'),
  updateAvailable('update_available', '可更新'),
  broken('broken', '异常');

  final String wireName;
  final String label;
  const HarnessStatus(this.wireName, this.label);
}

@immutable
class HarnessInfo {
  final HarnessKind kind;
  final String name;
  final HarnessStatus status;
  final String? version;
  final String executable;
  final List<String> requiredProviders;
  final String? activeJobId;
  final String description;

  const HarnessInfo({
    required this.kind,
    required this.name,
    required this.status,
    required this.executable,
    required this.requiredProviders,
    required this.description,
    this.version,
    this.activeJobId,
  });

  HarnessInfo copyWith({
    HarnessStatus? status,
    String? version,
    String? activeJobId,
    bool clearActiveJob = false,
  }) => HarnessInfo(
    kind: kind,
    name: name,
    status: status ?? this.status,
    version: version ?? this.version,
    executable: executable,
    requiredProviders: requiredProviders,
    description: description,
    activeJobId: clearActiveJob ? null : (activeJobId ?? this.activeJobId),
  );
}

enum JobKind {
  harnessInstall('harness_install', '安装'),
  harnessUpdate('harness_update', '更新'),
  appInstall('app_install', '安装应用'),
  stateReset('state_reset', '清除会话数据'),
  imageWarmup('image_warmup', '准备运行环境');

  final String wireName;
  final String label;
  const JobKind(this.wireName, this.label);
}

enum JobStatus {
  queued('queued', '排队中'),
  running('running', '执行中'),
  cancelling('cancelling', '正在清理'),
  cancelled('cancelled', '已取消'),
  succeeded('succeeded', '已完成'),
  failed('failed', '失败');

  final String wireName;
  final String label;
  const JobStatus(this.wireName, this.label);

  bool get isActive =>
      this == JobStatus.queued ||
      this == JobStatus.running ||
      this == JobStatus.cancelling;

  bool get canRequestCancel =>
      this == JobStatus.queued || this == JobStatus.running;
}

@immutable
class JobInfo {
  final String id;
  final JobKind kind;
  final JobStatus status;
  final double progress;
  final String message;
  final String? targetId;
  final String? error;
  final DateTime createdAt;
  final DateTime updatedAt;

  const JobInfo({
    required this.id,
    required this.kind,
    required this.status,
    required this.progress,
    required this.message,
    required this.createdAt,
    required this.updatedAt,
    this.targetId,
    this.error,
  });

  JobInfo copyWith({
    JobStatus? status,
    double? progress,
    String? message,
    String? error,
    DateTime? updatedAt,
  }) => JobInfo(
    id: id,
    kind: kind,
    status: status ?? this.status,
    progress: progress ?? this.progress,
    message: message ?? this.message,
    targetId: targetId,
    error: error ?? this.error,
    createdAt: createdAt,
    updatedAt: updatedAt ?? this.updatedAt,
  );
}

enum CredentialKind {
  apiKey('api_key', 'API Key'),
  oauth('oauth', '账号登录'),
  sessionToken('session_token', '会话令牌'),
  pat('pat', '个人访问令牌'),
  serviceToken('service_token', '服务令牌'),
  customHeader('custom_header', '自定义请求头');

  final String wireName;
  final String label;
  const CredentialKind(this.wireName, this.label);
}

enum CredentialSource {
  userInput('user_input', '手动添加'),
  hostOauth('host_oauth', '浏览器登录'),
  importedHostAuth('imported_host_auth', '从本机导入');

  final String wireName;
  final String label;
  const CredentialSource(this.wireName, this.label);
}

enum CredentialStatus {
  active('active', '有效'),
  expired('expired', '已过期'),
  revoked('revoked', '已撤销'),
  refreshFailed('refresh_failed', '刷新失败');

  final String wireName;
  final String label;
  const CredentialStatus(this.wireName, this.label);
}

/// Metadata-only representation of a credential. Never contains secret plaintext.
@immutable
class CredentialMetadata {
  final String id;
  final String name;
  final String provider;
  final CredentialKind kind;
  final CredentialSource source;
  final CredentialStatus status;
  final List<String> scopes;
  final String accountHint;
  final DateTime? expiresAt;
  final DateTime createdAt;
  final DateTime updatedAt;

  const CredentialMetadata({
    required this.id,
    required this.name,
    required this.provider,
    required this.kind,
    required this.source,
    required this.status,
    required this.scopes,
    required this.accountHint,
    required this.createdAt,
    required this.updatedAt,
    this.expiresAt,
  });

  CredentialMetadata copyWith({
    String? name,
    CredentialStatus? status,
    DateTime? updatedAt,
  }) => CredentialMetadata(
    id: id,
    name: name ?? this.name,
    provider: provider,
    kind: kind,
    source: source,
    status: status ?? this.status,
    scopes: scopes,
    accountHint: accountHint,
    expiresAt: expiresAt,
    createdAt: createdAt,
    updatedAt: updatedAt ?? this.updatedAt,
  );
}

@immutable
class ImportableCredentialCandidate {
  final String id;
  final String provider;
  final CredentialKind kind;
  final String sourcePathHint;
  final String accountHint;
  final bool valid;
  final bool alreadyImported;

  const ImportableCredentialCandidate({
    required this.id,
    required this.provider,
    required this.kind,
    required this.sourcePathHint,
    required this.accountHint,
    required this.valid,
    this.alreadyImported = false,
  });

  ImportableCredentialCandidate copyWith({bool? alreadyImported}) =>
      ImportableCredentialCandidate(
        id: id,
        provider: provider,
        kind: kind,
        sourcePathHint: sourcePathHint,
        accountHint: accountHint,
        valid: valid,
        alreadyImported: alreadyImported ?? this.alreadyImported,
      );
}

@immutable
class ImportSkippedItem {
  final String id;
  final String reason;

  const ImportSkippedItem({required this.id, required this.reason});
}

@immutable
class ImportCredentialsResult {
  final List<CredentialMetadata> imported;
  final List<ImportSkippedItem> skipped;

  const ImportCredentialsResult({
    required this.imported,
    required this.skipped,
  });
}

enum OAuthFlowStatus {
  authorizing('authorizing', '等待浏览器授权'),
  cancelling('cancelling', '正在取消'),
  cancelled('cancelled', '已取消'),
  succeeded('succeeded', '已连接'),
  failed('failed', '授权失败');

  final String wireName;
  final String label;
  const OAuthFlowStatus(this.wireName, this.label);
}

@immutable
class OAuthPendingFlow {
  final String flowId;
  final String provider;
  final String authUrl;
  final String state;
  final OAuthFlowStatus status;
  final DateTime startedAt;
  final String? credentialId;
  final String? errorMessage;

  const OAuthPendingFlow({
    required this.flowId,
    required this.provider,
    required this.authUrl,
    required this.state,
    required this.status,
    required this.startedAt,
    this.credentialId,
    this.errorMessage,
  });

  OAuthPendingFlow copyWith({
    OAuthFlowStatus? status,
    String? credentialId,
    String? errorMessage,
  }) => OAuthPendingFlow(
    flowId: flowId,
    provider: provider,
    authUrl: authUrl,
    state: state,
    status: status ?? this.status,
    startedAt: startedAt,
    credentialId: credentialId ?? this.credentialId,
    errorMessage: errorMessage ?? this.errorMessage,
  );
}

enum WorkspaceKind {
  managed('managed', 'Nullkey 管理'),
  external('external', '已有文件夹');

  final String wireName;
  final String label;
  const WorkspaceKind(this.wireName, this.label);

  bool get allowsPhysicalDeletion => this == WorkspaceKind.managed;
}

enum MountMode {
  rw('rw', '读写'),
  ro('ro', '只读');

  final String wireName;
  final String label;
  const MountMode(this.wireName, this.label);
}

@immutable
class WorkspaceRecord {
  final String id;
  final String name;
  final WorkspaceKind kind;
  final String hostPath;
  final MountMode defaultMode;
  final DateTime createdAt;
  final DateTime lastUsedAt;
  final String gitBranch;
  final bool hasProjectEnvWarning;

  const WorkspaceRecord({
    required this.id,
    required this.name,
    required this.kind,
    required this.hostPath,
    required this.defaultMode,
    required this.createdAt,
    required this.lastUsedAt,
    this.gitBranch = 'main',
    this.hasProjectEnvWarning = false,
  });

  /// Normalized path display required by `docs/07-state-and-mounts.md`.
  String get normalizedPath {
    final trimmed = hostPath.trim();
    if (trimmed.length > 1 && trimmed.endsWith('/')) {
      return trimmed.substring(0, trimmed.length - 1);
    }
    return trimmed;
  }

  WorkspaceRecord copyWith({
    String? name,
    MountMode? defaultMode,
    DateTime? lastUsedAt,
    String? gitBranch,
    bool? hasProjectEnvWarning,
  }) => WorkspaceRecord(
    id: id,
    name: name ?? this.name,
    kind: kind,
    hostPath: hostPath,
    defaultMode: defaultMode ?? this.defaultMode,
    createdAt: createdAt,
    lastUsedAt: lastUsedAt ?? this.lastUsedAt,
    gitBranch: gitBranch ?? this.gitBranch,
    hasProjectEnvWarning: hasProjectEnvWarning ?? this.hasProjectEnvWarning,
  );
}

@immutable
class PersistentStateRecord {
  final String id;
  final String name;
  final int sizeBytes;
  final String? lockedBySandboxId;
  final DateTime createdAt;
  final DateTime updatedAt;
  final String summary;

  const PersistentStateRecord({
    required this.id,
    required this.name,
    required this.sizeBytes,
    required this.createdAt,
    required this.updatedAt,
    required this.summary,
    this.lockedBySandboxId,
  });

  bool get isLocked => lockedBySandboxId != null;

  String get sizeLabel => formattedSize;

  String get formattedSize {
    final mb = sizeBytes / (1024 * 1024);
    if (mb >= 1024) {
      return '${(mb / 1024).toStringAsFixed(2)} GB';
    }
    return '${mb.toStringAsFixed(1)} MB';
  }

  PersistentStateRecord copyWith({
    int? sizeBytes,
    String? lockedBySandboxId,
    bool clearLock = false,
    DateTime? updatedAt,
  }) => PersistentStateRecord(
    id: id,
    name: name,
    sizeBytes: sizeBytes ?? this.sizeBytes,
    lockedBySandboxId: clearLock
        ? null
        : (lockedBySandboxId ?? this.lockedBySandboxId),
    createdAt: createdAt,
    updatedAt: updatedAt ?? this.updatedAt,
    summary: summary,
  );
}

enum HealthState {
  healthy('healthy', '健康'),
  degraded('degraded', '降级'),
  stopped('stopped', '已停止'),
  failed('failed', '异常');

  final String wireName;
  final String label;
  const HealthState(this.wireName, this.label);
}

@immutable
class McpServerRecord {
  final String id;
  final String name;
  final String transport;
  final String endpointSummary;
  final List<String> credentialIds;
  final HealthState health;
  final int toolsCount;
  final String description;

  const McpServerRecord({
    required this.id,
    required this.name,
    required this.transport,
    required this.endpointSummary,
    required this.credentialIds,
    required this.health,
    required this.toolsCount,
    required this.description,
  });

  McpServerRecord copyWith({
    HealthState? health,
    List<String>? credentialIds,
  }) => McpServerRecord(
    id: id,
    name: name,
    transport: transport,
    endpointSummary: endpointSummary,
    credentialIds: credentialIds ?? this.credentialIds,
    health: health ?? this.health,
    toolsCount: toolsCount,
    description: description,
  );
}

enum SandboxStatus {
  creating('creating', '创建中'),
  starting('starting', '启动中'),
  running('running', '运行中'),
  stopping('stopping', '正在停止'),
  stopped('stopped', '已停止'),
  failed('failed', '启动失败');

  final String wireName;
  final String label;
  const SandboxStatus(this.wireName, this.label);

  bool get isRunningOrStarting =>
      this == SandboxStatus.creating ||
      this == SandboxStatus.starting ||
      this == SandboxStatus.running;
}

/// Terminal connection state machine defined in `docs/08-api-contract.md` & `docs/10-interaction-contract.md`.
enum TerminalConnectionPhase {
  idle('未连接'),
  requestingTicket('正在连接'),
  connectingWs('等待终端就绪'),
  ready('已连接'),
  reconnecting('正在重新连接'),
  replaced('已在其他窗口打开'),
  daemonInterrupted('已中断'),
  closed('已断开'),

  /// The agent process ended; its PTY is gone.
  exited('已退出'),
  error('连接失败');

  final String label;
  const TerminalConnectionPhase(this.label);

  bool get canSendInput => this == TerminalConnectionPhase.ready;

  String get wireState => switch (this) {
    TerminalConnectionPhase.idle => 'idle',
    TerminalConnectionPhase.requestingTicket => 'requesting_ticket',
    TerminalConnectionPhase.connectingWs => 'connecting_ws',
    TerminalConnectionPhase.ready => 'ready',
    TerminalConnectionPhase.reconnecting => 'reconnecting',
    TerminalConnectionPhase.replaced => 'replaced',
    TerminalConnectionPhase.daemonInterrupted => 'daemon_interrupted',
    TerminalConnectionPhase.closed => 'closed',
    TerminalConnectionPhase.exited => 'exited',
    TerminalConnectionPhase.error => 'error',
  };
}

/// A single-use ticket for attaching to a sandbox's PTY over WebSocket.
@immutable
class TerminalTicket {
  final String wsUrl;
  final String ticket;
  final DateTime expiresAt;

  const TerminalTicket({
    required this.wsUrl,
    required this.ticket,
    required this.expiresAt,
  });
}

enum ServiceStatus {
  starting('starting', '正在启动'),
  ready('ready', '正常'),
  error('error', '无响应');

  final String wireName;
  final String label;
  const ServiceStatus(this.wireName, this.label);

  static ServiceStatus fromWire(String? raw) => ServiceStatus.values.firstWhere(
    (s) => s.wireName == raw,
    orElse: () => ServiceStatus.error,
  );
}

/// A service the sandbox declared in its manifest. Opening it needs a
/// single-use ticket, so there is no long-lived link to copy.
@immutable
class SandboxServiceRecord {
  final String id;
  final String sandboxId;
  final String name;
  final ServiceStatus status;
  final bool websocket;

  const SandboxServiceRecord({
    required this.id,
    required this.sandboxId,
    required this.name,
    required this.status,
    required this.websocket,
  });
}

@immutable
class DesktopStatusRecord {
  final bool enabled;
  final String status;
  final String? viewerUrl;
  final String resolution;
  final int fps;

  const DesktopStatusRecord({
    required this.enabled,
    required this.status,
    required this.resolution,
    required this.fps,
    this.viewerUrl,
  });

  DesktopStatusRecord copyWith({
    bool? enabled,
    String? status,
    String? viewerUrl,
  }) => DesktopStatusRecord(
    enabled: enabled ?? this.enabled,
    status: status ?? this.status,
    viewerUrl: viewerUrl ?? this.viewerUrl,
    resolution: resolution,
    fps: fps,
  );
}

/// What a scan check covers, for the check names Core reports.
String scanCheckLabel(String name) => switch (name) {
  'generated_config' => 'Nullkey 生成的 Agent 配置',
  'state_files' => '会话数据中的文件',
  'sandbox_env' => '沙箱进程的环境变量',
  _ => name,
};

@immutable
class SecretScanCheck {
  final String name;
  final bool clean;
  final int matches;
  final String targetSummary;

  const SecretScanCheck({
    required this.name,
    required this.clean,
    required this.matches,
    required this.targetSummary,
  });
}

/// Secret scan result matching `api/openapi.yaml` (`SecretScanResult`) and `docs/10-interaction-contract.md`.
@immutable
class SecretScanResult {
  final bool clean;
  final String scope;
  final List<String> excluded;
  final List<SecretScanCheck> checks;
  final DateTime scannedAt;

  const SecretScanResult({
    required this.clean,
    required this.scope,
    required this.excluded,
    required this.checks,
    required this.scannedAt,
  });

  int get findingsCount => checks.fold<int>(0, (sum, c) => sum + c.matches);

  /// Exact wording required by `docs/10-interaction-contract.md`.
  String get headline => clean ? '生成配置/环境未发现注册凭证' : '生成配置/环境发现疑似注册凭证';
}

@immutable
class ArtifactRecord {
  final String id;
  final String sandboxId;
  final String relativePath;
  final String kind;
  final int sizeBytes;
  final String summary;

  /// When the file last changed, if Core reported it.
  final DateTime? updatedAt;
  final String previewSnippet;

  const ArtifactRecord({
    required this.id,
    required this.sandboxId,
    required this.relativePath,
    required this.kind,
    required this.sizeBytes,
    required this.summary,
    required this.previewSnippet,
    this.updatedAt,
  });

  String get name => relativePath;

  String get sizeLabel => '${(sizeBytes / 1024).toStringAsFixed(1)} KB';
}

@immutable
class SandboxRecord {
  final String id;
  final String name;
  final SandboxStatus status;
  final HarnessKind harness;
  final String workspaceId;
  final MountMode workspaceMode;
  final String? stateId;
  final NetworkMode networkMode;
  final bool enableDesktop;
  final DateTime createdAt;
  final DateTime? startedAt;
  final DateTime? stoppedAt;
  final String? stopReason;

  /// Why the sandbox failed to start, as Core reported it.
  final String? lastError;

  const SandboxRecord({
    required this.id,
    required this.name,
    required this.status,
    required this.harness,
    required this.workspaceId,
    required this.workspaceMode,
    required this.networkMode,
    required this.enableDesktop,
    required this.createdAt,
    this.stateId,
    this.startedAt,
    this.stoppedAt,
    this.stopReason,
    this.lastError,
  });

  SandboxRecord copyWith({
    SandboxStatus? status,
    DateTime? startedAt,
    DateTime? stoppedAt,
    String? stopReason,
  }) => SandboxRecord(
    id: id,
    name: name,
    status: status ?? this.status,
    harness: harness,
    workspaceId: workspaceId,
    workspaceMode: workspaceMode,
    stateId: stateId,
    networkMode: networkMode,
    enableDesktop: enableDesktop,
    createdAt: createdAt,
    startedAt: startedAt ?? this.startedAt,
    stoppedAt: stoppedAt ?? this.stoppedAt,
    stopReason: stopReason ?? this.stopReason,
    lastError: lastError,
  );
}

@immutable
class AuditEventRecord {
  final String id;
  final DateTime timestamp;
  final String type;
  final String? sandboxId;
  final String? credentialId;
  final String? provider;
  final String? method;
  final String? host;
  final String? path;
  final int? status;
  final int? latencyMs;
  final String message;

  const AuditEventRecord({
    required this.id,
    required this.timestamp,
    required this.type,
    required this.message,
    this.sandboxId,
    this.credentialId,
    this.provider,
    this.method,
    this.host,
    this.path,
    this.status,
    this.latencyMs,
  });

  bool get isNetworkBlock => type == 'network.blocked';
}
