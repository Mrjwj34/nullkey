import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math' as math;

import '../models/nullkey_models.dart';
import 'nullkey_backend.dart';

typedef _Json = Map<String, Object?>;

/// Builds the user-facing message for a failed call from Core's error
/// `code` and its (English) `message`. Returning null falls back to the
/// generic wording for the code.
typedef _Describe = String? Function(String code, String message);

enum _Collection {
  capabilities,
  harnesses,
  workspaces,
  states,
  sandboxes,
  credentials,
  importable,
  mcp,
  audit,
}

/// Talks to a running `nullkeyd` over its control API (`api/openapi.yaml`).
///
/// Core pushes changes over `GET /events`; each event refetches the
/// collections it touches, so every read is Core's own answer. Core has no
/// endpoint that lists jobs or browser sign-ins, so the ones this GUI
/// starts are tracked here and polled until they finish.
class DaemonBackend implements NullkeyBackend {
  static const Duration _requestTimeout = Duration(seconds: 20);

  /// Core sends a heartbeat every 15 s; silence for longer means the
  /// connection is gone even if the socket has not noticed.
  static const Duration _eventSilenceLimit = Duration(seconds: 40);

  static const String _unreachableMessage = '连不上 Nullkey。请确认 nullkeyd 正在运行。';
  static const String _unauthorizedMessage =
      'Nullkey 拒绝了这个应用的令牌。请检查 NULLKEY_DAEMON_TOKEN。';

  final Uri _api;
  final String _token;
  final String? Function()? _tokenLoader;
  final HttpClient _http = HttpClient()
    ..connectionTimeout = const Duration(seconds: 3);

  /// Separate client so the long-lived event stream never occupies a
  /// connection that requests are waiting for.
  final HttpClient _eventsHttp = HttpClient()
    ..connectionTimeout = const Duration(seconds: 3);

  final StreamController<void> _changes = StreamController<void>.broadcast();
  final Completer<void> _loaded = Completer<void>();
  bool _disposed = false;
  String? _connectionProblem;

  // Core's latest answers, as received.
  _Json _capabilitiesRaw = const {};
  String _version = '';
  List<_Json> _oauthProvidersRaw = const [];
  List<_Json> _harnessesRaw = const [];
  List<_Json> _workspacesRaw = const [];
  List<_Json> _statesRaw = const [];
  List<_Json> _sandboxesRaw = const [];
  List<_Json> _credentialsRaw = const [];
  List<_Json> _importableRaw = const [];
  List<_Json> _mcpRaw = const [];
  List<_Json> _mcpCatalogRaw = const [];
  List<_Json> _auditRaw = const [];

  // The GUI's view of them, rebuilt by [_rebuild] after every fetch.
  late DaemonCapabilities _capabilities;
  List<HarnessInfo> _harnesses = const [];
  List<WorkspaceRecord> _workspaces = const [];
  List<PersistentStateRecord> _states = const [];
  List<SandboxRecord> _sandboxes = const [];
  List<CredentialMetadata> _credentials = const [];
  List<ImportableCredentialCandidate> _importable = const [];
  List<McpServerRecord> _mcpServers = const [];
  List<AuditEventRecord> _audit = const [];

  // Work this GUI started.
  final Map<String, JobInfo> _jobs = {};
  final Map<String, Timer> _jobPollers = {};
  final Map<String, OAuthPendingFlow> _flows = {};
  final Map<String, Timer> _flowPollers = {};

  final Map<String, List<SandboxServiceRecord>> _services = {};
  final Map<String, List<ArtifactRecord>> _artifacts = {};
  final Set<_DaemonTerminalConnection> _terminals = {};

  final Set<_Collection> _pendingRefetch = {};
  Timer? _refetchTimer;

  /// [baseUrl] is the control API's origin, such as
  /// `http://127.0.0.1:49152`; `/api/v1` is added when missing.
  DaemonBackend({required Uri baseUrl, required this._token, this._tokenLoader})
    : _api = _apiRoot(baseUrl) {
    _capabilities = _mapCapabilities();
    unawaited(_runEvents());
  }

  static Uri _apiRoot(Uri base) {
    final path = base.path.endsWith('/')
        ? base.path.substring(0, base.path.length - 1)
        : base.path;
    return base.replace(
      path: path.endsWith('/api/v1') ? path : '$path/api/v1',
      query: null,
      fragment: null,
    );
  }

  @override
  DaemonCapabilities get capabilities => _capabilities;

  @override
  String? get connectionProblem => _connectionProblem;

  @override
  Future<void> get loaded => _loaded.future;

  @override
  Stream<void> get changes => _changes.stream;

  @override
  void dispose() {
    _disposed = true;
    _refetchTimer?.cancel();
    for (final timer in [..._jobPollers.values, ..._flowPollers.values]) {
      timer.cancel();
    }
    for (final terminal in _terminals.toList()) {
      terminal.close();
    }
    _http.close(force: true);
    _eventsHttp.close(force: true);
    _changes.close();
  }

  void _emit() {
    if (!_disposed) {
      _changes.add(null);
    }
  }

  void _setProblem(String? problem) {
    if (_connectionProblem == problem) {
      return;
    }
    _connectionProblem = problem;
    _capabilities = _mapCapabilities();
    _emit();
  }

  void _markLoaded() {
    if (!_loaded.isCompleted) {
      _loaded.complete();
    }
  }

  // ---------------------------------------------------------------------------
  // Transport
  // ---------------------------------------------------------------------------

  Uri _uri(String path, [Map<String, String>? query]) => _api.replace(
    path: '${_api.path}$path',
    queryParameters: query == null || query.isEmpty ? null : query,
  );

  static String _seg(String id) => Uri.encodeComponent(id);

  String _authToken() {
    final token =
        (_tokenLoader == null ? _token : _tokenLoader())?.trim() ?? '';
    if (token.isNotEmpty) return token;
    const message = '尚未连接 nullkeyd。请先启动 nullkeyd；应用会自动重试连接。';
    _setProblem(message);
    throw const NullkeyApiException(
      statusCode: 0,
      code: 'unreachable',
      message: message,
    );
  }

  Future<Object?> _call(
    String method,
    String path, {
    Map<String, String>? query,
    Object? body,
    _Describe? describe,
  }) async {
    final HttpClientResponse response;
    final String text;
    try {
      final token = _authToken();
      final request = await _http
          .openUrl(method, _uri(path, query))
          .timeout(_requestTimeout);
      request.headers
        ..set(HttpHeaders.authorizationHeader, 'Bearer $token')
        ..set(HttpHeaders.acceptHeader, 'application/json');
      if (body != null) {
        final bytes = utf8.encode(jsonEncode(body));
        request.headers.contentType = ContentType.json;
        request.contentLength = bytes.length;
        request.add(bytes);
      }
      response = await request.close().timeout(_requestTimeout);
      text = await response
          .transform(utf8.decoder)
          .join()
          .timeout(_requestTimeout);
    } on IOException {
      throw _unreachable();
    } on TimeoutException {
      throw _unreachable();
    }

    final decoded = text.isEmpty ? null : _tryDecode(text);
    if (response.statusCode < 400) {
      if (_connectionProblem == _unauthorizedMessage) {
        _setProblem(null);
      }
      return decoded;
    }

    final envelope = decoded is Map ? decoded['error'] : null;
    final code = envelope is Map ? envelope['code'] as String? : null;
    final message = envelope is Map ? envelope['message'] as String? : null;
    final details = envelope is Map && envelope['details'] is Map
        ? _Json.from(envelope['details'] as Map)
        : <String, Object?>{};
    if (response.statusCode == 401 &&
        (code == null || code == 'unauthorized')) {
      _setProblem(_unauthorizedMessage);
    }
    final resolvedCode = code ?? 'internal';
    final resolvedMessage = message ?? '';
    throw NullkeyApiException(
      statusCode: response.statusCode,
      code: resolvedCode,
      message:
          describe?.call(resolvedCode, resolvedMessage) ??
          describeCoreError(resolvedCode, resolvedMessage),
      details: details,
    );
  }

  NullkeyApiException _unreachable() {
    _setProblem(_unreachableMessage);
    return const NullkeyApiException(
      statusCode: 0,
      code: 'unreachable',
      message: _unreachableMessage,
    );
  }

  static Object? _tryDecode(String text) {
    try {
      return jsonDecode(text);
    } on FormatException {
      return null;
    }
  }

  Future<_Json> _object(
    String method,
    String path, {
    Object? body,
    Map<String, String>? query,
    _Describe? describe,
  }) async {
    final result = await _call(
      method,
      path,
      body: body,
      query: query,
      describe: describe,
    );
    return result is Map ? _Json.from(result) : <String, Object?>{};
  }

  /// Follows `next_cursor` so the GUI sees the whole collection.
  Future<List<_Json>> _list(
    String path, {
    Map<String, String> query = const {},
    int maxPages = 20,
  }) async {
    final items = <_Json>[];
    String? cursor;
    for (var page = 0; page < maxPages; page++) {
      final body = await _object(
        'GET',
        path,
        query: {...query, 'cursor': ?cursor},
      );
      final raw = body['items'];
      if (raw is List) {
        items.addAll(raw.whereType<Map>().map(_Json.from));
      }
      cursor = body['next_cursor'] as String?;
      if (cursor == null || cursor.isEmpty) {
        break;
      }
    }
    return items;
  }

  // ---------------------------------------------------------------------------
  // Event stream & refetching
  // ---------------------------------------------------------------------------

  Future<void> _runEvents() async {
    var backoff = const Duration(milliseconds: 500);
    while (!_disposed) {
      try {
        final token = _authToken();
        final request = await _eventsHttp
            .getUrl(_uri('/events'))
            .timeout(_requestTimeout);
        request.headers
          ..set(HttpHeaders.authorizationHeader, 'Bearer $token')
          ..set(HttpHeaders.acceptHeader, 'text/event-stream');
        final response = await request.close().timeout(_requestTimeout);
        if (response.statusCode != 200) {
          await response.drain<void>();
          if (response.statusCode == 401) {
            _setProblem(_unauthorizedMessage);
            backoff = const Duration(seconds: 5);
          } else {
            _setProblem(_unreachableMessage);
          }
        } else {
          // Anything that happened while disconnected was not pushed.
          await _refreshAll();
          _setProblem(null);
          backoff = const Duration(milliseconds: 500);
          await _consumeEvents(response);
          if (!_disposed) {
            _setProblem(_unreachableMessage);
          }
        }
      } on IOException {
        if (!_disposed) _setProblem(_unreachableMessage);
      } on TimeoutException {
        if (!_disposed) _setProblem(_unreachableMessage);
      } on NullkeyApiException {
        // [_call] already recorded why.
      }
      _markLoaded();
      if (_disposed) {
        return;
      }
      await Future<void>.delayed(backoff);
      backoff = Duration(
        milliseconds: math.min(backoff.inMilliseconds * 2, 5000),
      );
    }
  }

  Future<void> _consumeEvents(HttpClientResponse response) async {
    String? event;
    final data = StringBuffer();
    final lines = response
        .transform(utf8.decoder)
        .transform(const LineSplitter())
        .timeout(_eventSilenceLimit);
    await for (final line in lines) {
      if (_disposed) {
        return;
      }
      if (line.isEmpty) {
        if (event != null) {
          _onEvent(event, data.toString());
        }
        event = null;
        data.clear();
      } else if (line.startsWith(':')) {
        if (line.contains('heartbeat')) {
          _onHeartbeat();
        }
      } else if (line.startsWith('event:')) {
        event = line.substring(6).trim();
      } else if (line.startsWith('data:')) {
        data.write(line.substring(5).trimLeft());
      }
    }
  }

  void _onEvent(String type, String data) {
    final decoded = _tryDecode(data);
    final payload = decoded is Map ? _Json.from(decoded) : <String, Object?>{};
    final touched = <_Collection>{_Collection.audit};
    switch (type.split('.').first) {
      case 'credential':
        touched.add(_Collection.credentials);
      case 'oauth':
        touched.add(_Collection.credentials);
        final flowId = payload['flow_id'];
        if (flowId is String && _flows.containsKey(flowId)) {
          unawaited(_pollFlow(flowId));
        }
      case 'workspace':
        touched.add(_Collection.workspaces);
      case 'state':
        touched.add(_Collection.states);
      case 'sandbox':
        touched.addAll(const [
          _Collection.sandboxes,
          _Collection.workspaces,
          _Collection.states,
        ]);
      case 'job':
        final jobId = payload['job_id'];
        if (jobId is String && _jobs.containsKey(jobId)) {
          unawaited(_pollJob(jobId));
        }
        touched.add(_Collection.harnesses);
      case 'mcp':
        touched.add(_Collection.mcp);
      case 'service':
        final sandboxId = payload['sandbox_id'];
        if (sandboxId is String) {
          unawaited(refreshServices(sandboxId).catchError((_) {}));
        }
    }
    _scheduleRefetch(touched);
  }

  /// Activity such as blocked connections and gateway calls is recorded
  /// without an event, so it is picked up on Core's heartbeat.
  void _onHeartbeat() {
    _scheduleRefetch({_Collection.audit});
    for (final sandboxId in _services.keys.toList()) {
      if (getSandbox(sandboxId)?.status == SandboxStatus.running) {
        unawaited(refreshServices(sandboxId).catchError((_) {}));
      }
    }
  }

  void _scheduleRefetch(Set<_Collection> collections) {
    _pendingRefetch.addAll(collections);
    _refetchTimer ??= Timer(const Duration(milliseconds: 60), () {
      _refetchTimer = null;
      final batch = {..._pendingRefetch};
      _pendingRefetch.clear();
      unawaited(_refetch(batch).catchError((_) {}));
    });
  }

  Future<void> _refreshAll() async {
    await _refetch(_Collection.values.toSet());
    for (final jobId in _jobs.keys.toList()) {
      if (_jobs[jobId]!.status.isActive) {
        unawaited(_pollJob(jobId));
      }
    }
    for (final flowId in _flows.keys.toList()) {
      if (_flows[flowId]!.status == OAuthFlowStatus.authorizing) {
        unawaited(_pollFlow(flowId));
      }
    }
    _markLoaded();
  }

  Future<void> _refetch(Set<_Collection> collections) async {
    await Future.wait([
      for (final collection in collections) _fetch(collection),
    ]);
    if (_disposed) {
      return;
    }
    _rebuild();
    _emit();
  }

  Future<void> _fetch(_Collection collection) async {
    switch (collection) {
      case _Collection.capabilities:
        final results = await Future.wait([
          _object('GET', '/capabilities'),
          _object('GET', '/health'),
          _list('/oauth/providers'),
        ]);
        _capabilitiesRaw = results[0] as _Json;
        _version = (results[1] as _Json)['version'] as String? ?? '';
        _oauthProvidersRaw = results[2] as List<_Json>;
      case _Collection.harnesses:
        _harnessesRaw = await _list('/harnesses');
      case _Collection.workspaces:
        _workspacesRaw = await _list('/workspaces');
      case _Collection.states:
        _statesRaw = await _list('/states');
      case _Collection.sandboxes:
        _sandboxesRaw = await _list('/sandboxes');
      case _Collection.credentials:
        _credentialsRaw = await _list('/credentials');
      case _Collection.importable:
        _importableRaw = await _list('/oauth/importable');
      case _Collection.mcp:
        final results = await Future.wait([
          _list('/mcp/connections'),
          _list('/mcp/catalog'),
        ]);
        _mcpRaw = results[0];
        _mcpCatalogRaw = results[1];
      case _Collection.audit:
        _auditRaw = await _list(
          '/audit',
          query: const {'limit': '100'},
          maxPages: 1,
        );
    }
  }

  // ---------------------------------------------------------------------------
  // Mapping Core's resources to the GUI's
  // ---------------------------------------------------------------------------

  static DateTime? _time(Object? raw) =>
      raw is String ? DateTime.tryParse(raw)?.toUtc() : null;

  static final DateTime _epoch = DateTime.fromMillisecondsSinceEpoch(
    0,
    isUtc: true,
  );

  static HarnessKind? _harnessKind(Object? id) => switch (id) {
    'codex' => HarnessKind.codex,
    'claude' => HarnessKind.claudeCode,
    'shell' => HarnessKind.custom,
    _ => null,
  };

  static String _harnessId(HarnessKind kind) => switch (kind) {
    HarnessKind.codex => 'codex',
    HarnessKind.claudeCode => 'claude',
    HarnessKind.custom => 'shell',
    _ => kind.wireName,
  };

  /// The GUI groups ChatGPT sign-in (`codex`) and OpenAI API keys under
  /// one account; Core's gateway uses either for OpenAI.
  static String _guiProvider(String coreProvider) => switch (coreProvider) {
    'codex' => 'openai',
    'claude' => 'anthropic',
    _ => coreProvider,
  };

  static String _browserSignInProvider(String guiProvider) =>
      guiProvider == 'openai' ? 'codex' : guiProvider;

  DaemonCapabilities _mapCapabilities() {
    final modes = (_capabilitiesRaw['network_modes'] as List?) ?? const [];
    final supported = NetworkMode.values
        .where((m) => modes.contains(m.wireName))
        .toList();
    return DaemonCapabilities(
      version: _version,
      os: 'darwin',
      arch: '',
      sandboxBackend: 'macOS Seatbelt',
      desktopAvailable: _capabilitiesRaw['desktop_available'] == true,
      oauthImportAvailable: true,
      networkModes: supported.isEmpty ? NetworkMode.values : supported,
      connectionMode: DaemonConnectionMode.liveDaemon,
      endpointUrl: _api.toString(),
      browserSignInProviders: {
        for (final p in _oauthProvidersRaw)
          if (p['status'] == 'available') _guiProvider(p['id'] as String),
      },
      apiKeyProviders: const ['openai', 'anthropic', 'github'],
      perLaunchMountMode: false,
    );
  }

  void _rebuild() {
    _capabilities = _mapCapabilities();

    final workspacesById = {
      for (final w in _workspacesRaw) w['id'] as String: w,
    };
    final sandboxes = <SandboxRecord>[];
    for (final raw in _sandboxesRaw) {
      final record = _mapSandbox(raw, workspacesById);
      if (record != null) {
        sandboxes.add(record);
      }
    }
    sandboxes.sort((a, b) => b.createdAt.compareTo(a.createdAt));
    _sandboxes = List.unmodifiable(sandboxes);

    _workspaces = List.unmodifiable(_workspacesRaw.map(_mapWorkspace));
    _states = List.unmodifiable(_statesRaw.map(_mapState));
    _harnesses = List.unmodifiable(_mapHarnesses());
    _credentials = List.unmodifiable(_credentialsRaw.map(_mapCredential));
    _importable = List.unmodifiable(_mapImportable());
    _mcpServers = List.unmodifiable(_mcpRaw.map(_mapMcp));
    _audit = List.unmodifiable(_auditRaw.map(_mapAudit));
  }

  List<HarnessInfo> _mapHarnesses() {
    final out = <HarnessInfo>[];
    for (final raw in _harnessesRaw) {
      final kind = _harnessKind(raw['id']);
      if (kind == null) {
        continue;
      }
      final activeJob = _jobs.values
          .where((j) => j.targetId == kind.wireName && j.status.isActive)
          .firstOrNull;
      final version = raw['version'] as String?;
      out.add(
        HarnessInfo(
          kind: kind,
          name: kind.displayName,
          status: activeJob != null
              ? HarnessStatus.installing
              : raw['installed'] == true
              ? HarnessStatus.installed
              : HarnessStatus.notInstalled,
          // `latest` and `builtin` name no actual version.
          version:
              version == null || version == 'latest' || version == 'builtin'
              ? null
              : version,
          executable: '',
          requiredProviders: switch (kind) {
            HarnessKind.codex => const ['openai'],
            HarnessKind.claudeCode => const ['anthropic'],
            _ => const [],
          },
          description: switch (kind) {
            HarnessKind.codex => 'OpenAI 的终端编码 Agent。',
            HarnessKind.claudeCode => 'Anthropic 的终端编码 Agent。',
            _ => '沙箱里的命令行。',
          },
          activeJobId: activeJob?.id,
        ),
      );
    }
    const order = [
      HarnessKind.codex,
      HarnessKind.claudeCode,
      HarnessKind.custom,
    ];
    out.sort((a, b) => order.indexOf(a.kind).compareTo(order.indexOf(b.kind)));
    return out;
  }

  WorkspaceRecord _mapWorkspace(_Json raw) {
    final createdAt = _time(raw['created_at']) ?? _epoch;
    return WorkspaceRecord(
      id: raw['id'] as String,
      name: raw['name'] as String? ?? '',
      kind: raw['storage_kind'] == 'external'
          ? WorkspaceKind.external
          : WorkspaceKind.managed,
      hostPath: raw['root_path'] as String? ?? '',
      defaultMode: raw['default_mode'] == 'ro' ? MountMode.ro : MountMode.rw,
      createdAt: createdAt,
      lastUsedAt: _time(raw['last_used_at']) ?? createdAt,
    );
  }

  static SandboxStatus _sandboxStatus(Object? raw) => SandboxStatus.values
      .firstWhere((s) => s.wireName == raw, orElse: () => SandboxStatus.failed);

  SandboxRecord? _mapSandbox(_Json raw, Map<String, _Json> workspacesById) {
    final kind = _harnessKind(raw['harness_id']);
    if (kind == null) {
      return null;
    }
    final id = raw['id'] as String;
    final workspace = workspacesById[raw['workspace_id']];
    final status = _sandboxStatus(raw['status']);
    final createdAt =
        _time(raw['created_at']) ?? _time(raw['started_at']) ?? _epoch;
    return SandboxRecord(
      id: id,
      name: '${workspace?['name'] ?? '工作区'} · ${kind.displayName}',
      status: status,
      harness: kind,
      workspaceId: raw['workspace_id'] as String? ?? '',
      // Core mounts every sandbox with its workspace's default mode.
      workspaceMode: workspace?['default_mode'] == 'ro'
          ? MountMode.ro
          : MountMode.rw,
      stateId: raw['state_id'] as String?,
      networkMode: NetworkMode.fromWire(raw['network_mode'] as String?),
      enableDesktop: raw['runtime'] == 'desktop',
      createdAt: createdAt,
      startedAt: _time(raw['started_at']),
      stoppedAt: _time(raw['stopped_at']),
      stopReason: status == SandboxStatus.stopped ? _stopReason(id) : null,
      lastError: raw['last_error'] as String?,
    );
  }

  /// Core reports why a sandbox stopped only in its activity log.
  String _stopReason(String sandboxId) {
    final event = _auditRaw
        .where(
          (e) => e['type'] == 'sandbox.stopped' && e['sandbox_id'] == sandboxId,
        )
        .firstOrNull;
    final detail = event?['detail'];
    return detail is Map && detail['reason'] == 'daemon_restart'
        ? 'daemon_restarted'
        : 'user_stopped';
  }

  PersistentStateRecord _mapState(_Json raw) {
    final id = raw['id'] as String;
    final kind = _harnessKind(raw['harness_id']);
    final agent = kind?.displayName ?? raw['harness_id'] as String? ?? 'Agent';
    final users = _sandboxes.where((s) => s.stateId == id).toList();
    final holder = users.where((s) => s.status.isRunningOrStarting).firstOrNull;
    final latest = holder ?? users.firstOrNull;
    final createdAt = _time(raw['created_at']) ?? _epoch;
    return PersistentStateRecord(
      id: id,
      name: latest?.name ?? '$agent 会话数据',
      sizeBytes: (raw['size_bytes'] as num?)?.toInt() ?? 0,
      lockedBySandboxId: holder?.id,
      createdAt: createdAt,
      updatedAt: _time(raw['last_used_at']) ?? createdAt,
      summary: '$agent 的配置、会话和历史',
    );
  }

  static CredentialKind _credentialKind(Object? authType) => switch (authType) {
    'oauth' => CredentialKind.oauth,
    'pat' || 'basic' => CredentialKind.pat,
    'bearer' => CredentialKind.serviceToken,
    'header' => CredentialKind.customHeader,
    _ => CredentialKind.apiKey,
  };

  /// `sha256:<prefix>…<last four>` keeps only the tail worth showing.
  static String _accountHint(Object? fingerprint) {
    if (fingerprint is! String) {
      return '';
    }
    final tail = fingerprint.split('…').last;
    return tail.isEmpty || tail == fingerprint ? '' : '以 $tail 结尾';
  }

  CredentialMetadata _mapCredential(_Json raw) {
    final id = raw['id'] as String;
    final kind = _credentialKind(raw['auth_type']);
    final imported = _auditRaw.any(
      (e) =>
          e['type'] == 'credential.imported' &&
          (e['detail'] as Map?)?['credential_id'] == id,
    );
    final createdAt = _time(raw['created_at']) ?? _epoch;
    return CredentialMetadata(
      id: id,
      name: raw['label'] as String? ?? '',
      provider: _guiProvider(raw['provider_id'] as String? ?? ''),
      kind: kind,
      source: imported
          ? CredentialSource.importedHostAuth
          : kind == CredentialKind.oauth
          ? CredentialSource.hostOauth
          : CredentialSource.userInput,
      status: switch (raw['status']) {
        'expired' => CredentialStatus.expired,
        'revoked' => CredentialStatus.revoked,
        _ => CredentialStatus.active,
      },
      scopes: const [],
      accountHint: _accountHint(raw['fingerprint']),
      expiresAt: _time(raw['expires_at']),
      createdAt: createdAt,
      updatedAt: _time(raw['last_used_at']) ?? createdAt,
    );
  }

  List<ImportableCredentialCandidate> _mapImportable() {
    final connected = {
      for (final c in _credentialsRaw)
        if (c['status'] == 'connected') c['provider_id'] as String?,
    };
    final now = DateTime.now().toUtc();
    return [
      for (final raw in _importableRaw)
        if (raw['available'] == true)
          ImportableCredentialCandidate(
            id: '${raw['provider_id']}:${raw['source']}',
            provider: _guiProvider(raw['provider_id'] as String),
            kind: raw['provider_id'] == 'github'
                ? CredentialKind.pat
                : CredentialKind.oauth,
            sourcePathHint: '~/${raw['source']}',
            accountHint: providerAccountName(
              _guiProvider(raw['provider_id'] as String),
            ),
            valid: _time(raw['expires_at'])?.isAfter(now) ?? true,
            alreadyImported: connected.contains(raw['provider_id']),
          ),
    ];
  }

  McpServerRecord _mapMcp(_Json raw) {
    final serverId = raw['server_id'] as String? ?? '';
    final catalog = _mcpCatalogRaw
        .where((c) => c['id'] == serverId)
        .firstOrNull;
    final tools = (raw['tools'] as List?) ?? const [];
    final credentialId = raw['credential_id'] as String?;
    return McpServerRecord(
      id: raw['id'] as String,
      name: raw['label'] as String? ?? serverId,
      transport: 'http',
      endpointSummary: '由 Nullkey 代为调用',
      credentialIds: [?credentialId],
      health: raw['status'] == 'connected'
          ? HealthState.healthy
          : HealthState.failed,
      toolsCount: tools.length,
      description: catalog?['name'] as String? ?? serverId,
    );
  }

  AuditEventRecord _mapAudit(_Json raw) {
    final type = raw['type'] as String? ?? '';
    final rawDetail = raw['detail'];
    final detail = rawDetail is Map
        ? _Json.from(rawDetail)
        : <String, Object?>{};
    final coreProvider =
        (detail['provider'] ?? detail['provider_id']) as String?;
    final provider = coreProvider == null ? null : _guiProvider(coreProvider);
    final status = detail['status'];
    return AuditEventRecord(
      id: raw['id'] as String,
      timestamp: _time(raw['ts']) ?? _epoch,
      type: type,
      sandboxId: raw['sandbox_id'] as String?,
      credentialId: detail['credential_id'] as String?,
      provider: provider,
      host: detail['host'] as String?,
      status: status is num ? status.toInt() : null,
      message: _auditMessage(type, detail, provider),
    );
  }

  String _auditMessage(String type, _Json detail, String? provider) {
    String harnessName() =>
        _harnessKind(detail['harness_id'])?.displayName ??
        (detail['harness_id'] as String? ?? 'Agent');
    final account = provider == null ? '账号' : providerAccountName(provider);
    return switch (type) {
      'gateway.model' => '代为调用 $account 的模型',
      'gateway.git' => '代为访问 GitHub 仓库 ${detail['owner']}/${detail['repo']}',
      'network.blocked' => '拒绝访问 ${detail['host'] ?? '未知地址'}',
      'sandbox.started' =>
        '启动 ${harnessName()}（${NetworkMode.fromWire(detail['network_mode'] as String?).label}）',
      'sandbox.stopped' =>
        detail['reason'] == 'daemon_restart' ? 'Nullkey 重启，沙箱已停止' : '沙箱已停止',
      'sandbox.failed' => '沙箱没能启动',
      'sandbox.deleted' => '删除沙箱记录',
      'credential.created' => '添加$account',
      'credential.imported' => '从本机导入$account',
      'credential.deleted' => '撤销凭证',
      'oauth.started' => '开始在浏览器中登录',
      'oauth.succeeded' => '浏览器登录完成',
      'oauth.cancelled' => '取消浏览器登录',
      'oauth.failed' => '浏览器登录失败',
      'job.created' => '开始安装 ${harnessName()}',
      'job.succeeded' => '已安装 ${harnessName()}',
      'job.cancelling' || 'job.cancelled' => '取消安装',
      'job.failed' => '安装失败',
      'workspace.created' => '添加工作区',
      'workspace.deleted' => '移除工作区',
      'state.created' => '新建会话数据',
      'state.reset' => '清除会话数据',
      'state.deleted' => '删除会话数据',
      'service.started' => '服务 ${detail['service'] ?? ''} 已启动',
      'service.stopped' => '服务 ${detail['service'] ?? ''} 已停止',
      'service.exited' => '服务 ${detail['service'] ?? ''} 已退出',
      'service.restarted' => '服务 ${detail['service'] ?? ''} 已重启',
      'service.error' => '服务 ${detail['service'] ?? ''} 出错',
      'service.manifest_invalid' => '沙箱的服务清单无效',
      'mcp.created' => '连接 MCP 服务',
      'mcp.deleted' => '断开 MCP 服务',
      _ => type,
    };
  }

  // ---------------------------------------------------------------------------
  // Harnesses & install jobs
  // ---------------------------------------------------------------------------

  @override
  List<HarnessInfo> listHarnesses() => _harnesses;

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
    final active = getHarness(kind)?.activeJobId;
    if (active != null && _jobs[active]?.status.isActive == true) {
      return _jobs[active]!;
    }
    final accepted = await _object(
      'POST',
      '/harnesses/${_seg(_harnessId(kind))}/install',
      body: const {},
    );
    final raw = await _object(
      'GET',
      '/jobs/${_seg(accepted['job_id'] as String)}',
    );
    final now = DateTime.now().toUtc();
    final job = _mapJob(
      raw,
      kind: isUpdate ? JobKind.harnessUpdate : JobKind.harnessInstall,
      targetId: kind.wireName,
      createdAt: now,
    );
    _jobs[job.id] = job;
    _rebuild();
    _emit();
    _jobPollers[job.id]?.cancel();
    _jobPollers[job.id] = Timer.periodic(
      const Duration(milliseconds: 400),
      (_) => unawaited(_pollJob(job.id)),
    );
    return job;
  }

  JobInfo _mapJob(
    _Json raw, {
    required JobKind kind,
    required String? targetId,
    required DateTime createdAt,
  }) {
    final status = JobStatus.values.firstWhere(
      (s) => s.wireName == raw['status'],
      orElse: () => JobStatus.failed,
    );
    final error = raw['error'] as String?;
    return JobInfo(
      id: raw['id'] as String,
      kind: kind,
      status: status,
      progress: (raw['progress'] as num?)?.toDouble() ?? 0,
      message: switch (raw['message']) {
        _ when status == JobStatus.cancelling => '正在删除未完成的文件',
        'validating harness' => '正在检查',
        'staging harness' => '正在安装',
        'installed' => '已安装',
        'cancelled' =>
          kind == JobKind.harnessUpdate ? '已取消更新，原版本仍可使用' : '已取消安装，未留下文件',
        _ => status.label,
      },
      targetId: targetId,
      error: error == null
          ? null
          : error.startsWith('harness executable is unavailable')
          ? '请先在宿主机安装 Agent，再重新检查。Nullkey 会从 PATH 和常见安装位置（~/.local/bin、/opt/homebrew/bin 等）查找。'
          : '没能完成安装（$error）',
      createdAt: createdAt,
      updatedAt: DateTime.now().toUtc(),
    );
  }

  Future<void> _pollJob(String jobId) async {
    final known = _jobs[jobId];
    if (known == null || _disposed) {
      return;
    }
    final _Json raw;
    try {
      raw = await _object('GET', '/jobs/${_seg(jobId)}');
    } on NullkeyApiException {
      return;
    }
    final next = _mapJob(
      raw,
      kind: known.kind,
      targetId: known.targetId,
      createdAt: known.createdAt,
    );
    _jobs[jobId] = next;
    if (!next.status.isActive) {
      _jobPollers.remove(jobId)?.cancel();
      await _fetch(_Collection.harnesses).catchError((_) {});
    }
    _rebuild();
    _emit();
  }

  @override
  Future<JobInfo> requestCancelJob(String jobId) async {
    final known = _jobs[jobId];
    if (known == null) {
      throw const NullkeyApiException(
        statusCode: 404,
        code: 'not_found',
        message: '找不到这个任务。',
      );
    }
    try {
      final raw = await _object(
        'POST',
        '/jobs/${_seg(jobId)}/cancel',
        body: const {},
        describe: (code, message) => code == 'conflict' ? '取消前安装已经完成。' : null,
      );
      final next = _mapJob(
        raw,
        kind: known.kind,
        targetId: known.targetId,
        createdAt: known.createdAt,
      );
      _jobs[jobId] = next;
      _rebuild();
      _emit();
      return next;
    } on NullkeyApiException catch (e) {
      if (e.code == 'conflict') {
        await _pollJob(jobId);
      }
      rethrow;
    }
  }

  // ---------------------------------------------------------------------------
  // Credentials & sign-in
  // ---------------------------------------------------------------------------

  @override
  List<CredentialMetadata> listCredentials() => _credentials;

  @override
  List<ImportableCredentialCandidate> listImportableCredentials() =>
      _importable;

  @override
  List<OAuthPendingFlow> listOAuthFlows() {
    final list = _flows.values.toList()
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
    final authType = switch (kind) {
      CredentialKind.apiKey when provider == 'github' => 'pat',
      CredentialKind.apiKey => 'api_key',
      CredentialKind.pat => 'pat',
      CredentialKind.sessionToken || CredentialKind.serviceToken => 'bearer',
      CredentialKind.customHeader => 'header',
      CredentialKind.oauth => 'oauth',
    };
    final raw = await _object(
      'POST',
      '/credentials',
      body: {
        'provider_id': provider,
        'label': name.trim(),
        'auth_type': authType,
        'value': secretValue.trim(),
      },
    );
    await _refetch({_Collection.credentials, _Collection.audit});
    final id = raw['id'] as String?;
    return _credentials.where((c) => c.id == id).firstOrNull ??
        _mapCredential(raw);
  }

  @override
  Future<void> deleteCredential(String id) async {
    await _call(
      'DELETE',
      '/credentials/${_seg(id)}',
      describe: (code, _) => code == 'not_found' ? '找不到这个凭证，可能已被撤销。' : null,
    );
    await _refetch({_Collection.credentials, _Collection.audit});
  }

  @override
  Future<ImportCredentialsResult> importHostCredentials(
    List<String> candidateIds,
  ) async {
    final importedIds = <String>[];
    final skipped = <ImportSkippedItem>[];
    for (final candidateId in candidateIds) {
      final candidate = _importable
          .where((c) => c.id == candidateId)
          .firstOrNull;
      final separator = candidateId.indexOf(':');
      if (candidate == null || separator == -1) {
        skipped.add(ImportSkippedItem(id: candidateId, reason: '没有找到'));
        continue;
      }
      if (candidate.alreadyImported) {
        skipped.add(ImportSkippedItem(id: candidateId, reason: '之前已经导入'));
        continue;
      }
      try {
        final result = await _object(
          'POST',
          '/oauth/import',
          body: {
            'provider_id': candidateId.substring(0, separator),
            'source': candidateId.substring(separator + 1),
            'label': providerAccountName(candidate.provider),
          },
        );
        final credentialId = result['credential_id'] as String?;
        if (result['status'] == 'expired') {
          skipped.add(
            ImportSkippedItem(
              id: candidateId,
              reason: '${candidate.sourcePathHint} 里的登录已过期',
            ),
          );
        }
        if (credentialId != null) {
          importedIds.add(credentialId);
        }
      } on NullkeyApiException catch (e) {
        if (e.code == 'unreachable') {
          rethrow;
        }
        skipped.add(ImportSkippedItem(id: candidateId, reason: e.message));
      }
    }
    await _refetch({_Collection.credentials, _Collection.audit});
    return ImportCredentialsResult(
      imported: _credentials.where((c) => importedIds.contains(c.id)).toList(),
      skipped: skipped,
    );
  }

  @override
  Future<OAuthPendingFlow> startOAuthFlow({required String provider}) async {
    final raw = await _object(
      'POST',
      '/oauth/start',
      body: {
        'provider_id': _browserSignInProvider(provider),
        'label': providerAccountName(provider),
      },
    );
    final flow = OAuthPendingFlow(
      flowId: raw['flow_id'] as String,
      provider: provider,
      authUrl: raw['auth_url'] as String? ?? '',
      state: '',
      status: OAuthFlowStatus.authorizing,
      startedAt: DateTime.now().toUtc(),
    );
    _flows[flow.flowId] = flow;
    _flowPollers[flow.flowId]?.cancel();
    _flowPollers[flow.flowId] = Timer.periodic(
      const Duration(seconds: 2),
      (_) => unawaited(_pollFlow(flow.flowId)),
    );
    _emit();
    return flow;
  }

  OAuthPendingFlow _applyFlow(OAuthPendingFlow known, _Json raw) {
    final error = raw['error'] as String?;
    return switch (raw['status']) {
      'succeeded' => known.copyWith(
        status: OAuthFlowStatus.succeeded,
        credentialId: raw['credential_id'] as String?,
      ),
      'cancelled' => known.copyWith(
        status: OAuthFlowStatus.cancelled,
        errorMessage: '已取消',
      ),
      'expired' => known.copyWith(
        status: OAuthFlowStatus.failed,
        errorMessage: '登录等待太久已失效，请重新开始。',
      ),
      'failed' => known.copyWith(
        status: OAuthFlowStatus.failed,
        errorMessage: error == null ? '登录没有完成，请重试。' : '登录没有完成（$error）',
      ),
      _ => known,
    };
  }

  Future<void> _pollFlow(String flowId) async {
    final known = _flows[flowId];
    if (known == null || _disposed) {
      return;
    }
    final _Json raw;
    try {
      raw = await _object('GET', '/oauth/flows/${_seg(flowId)}');
    } on NullkeyApiException {
      return;
    }
    final next = _applyFlow(_flows[flowId] ?? known, raw);
    _flows[flowId] = next;
    if (next.status != OAuthFlowStatus.authorizing) {
      _flowPollers.remove(flowId)?.cancel();
      await _refetch({_Collection.credentials, _Collection.audit})
          .catchError((_) {});
      return;
    }
    _emit();
  }

  @override
  Future<OAuthPendingFlow> cancelOAuthFlow(String flowId) async {
    final known = _flows[flowId];
    if (known == null) {
      throw const NullkeyApiException(
        statusCode: 404,
        code: 'not_found',
        message: '找不到这次登录。',
      );
    }
    try {
      final raw = await _object(
        'POST',
        '/oauth/flows/${_seg(flowId)}/cancel',
        body: const {},
        describe: (code, _) => code == 'conflict' ? '取消前浏览器已完成登录，账号已连接。' : null,
      );
      final next = _applyFlow(known, raw);
      _flows[flowId] = next.status == OAuthFlowStatus.authorizing
          ? known.copyWith(
              status: OAuthFlowStatus.cancelled,
              errorMessage: '已取消',
            )
          : next;
      _flowPollers.remove(flowId)?.cancel();
      _emit();
      return _flows[flowId]!;
    } on NullkeyApiException catch (e) {
      if (e.code == 'conflict') {
        await _pollFlow(flowId);
      }
      rethrow;
    }
  }

  @override
  List<McpServerRecord> listMcpServers() => _mcpServers;

  // ---------------------------------------------------------------------------
  // Workspaces & session data
  // ---------------------------------------------------------------------------

  @override
  List<WorkspaceRecord> listWorkspaces() => _workspaces;

  @override
  Future<WorkspaceRecord> createWorkspace({
    required String name,
    required WorkspaceKind kind,
    String? hostPath,
    MountMode defaultMode = MountMode.rw,
  }) async {
    final path = hostPath?.trim() ?? '';
    if (kind == WorkspaceKind.external && path.isEmpty) {
      throw const NullkeyApiException(
        statusCode: 400,
        code: 'invalid_request',
        message: '请填写以 / 开头的完整路径。',
      );
    }
    final raw = await _object(
      'POST',
      '/workspaces',
      body: {
        'name': name.trim(),
        if (kind == WorkspaceKind.external) 'root_path': path,
        'default_mode': defaultMode.wireName,
      },
    );
    await _refetch({_Collection.workspaces, _Collection.audit});
    final id = raw['id'] as String?;
    return _workspaces.where((w) => w.id == id).firstOrNull ??
        _mapWorkspace(raw);
  }

  @override
  Future<void> deleteWorkspace(
    String id, {
    bool deleteFiles = false,
    String? confirmName,
  }) async {
    final workspace = _workspaces.where((w) => w.id == id).firstOrNull;
    try {
      await _call(
        'DELETE',
        '/workspaces/${_seg(id)}',
        query: {
          if (deleteFiles) 'delete_files': 'true',
          if (deleteFiles && confirmName != null)
            'confirm_name': confirmName.trim(),
        },
        describe: (code, _) => code == 'state_locked'
            ? '“${workspace?.name ?? '工作区'}”正在被沙箱使用，请先停止沙箱。'
            : null,
      );
    } on NullkeyApiException catch (e) {
      if (e.code == 'state_locked') {
        await _refetch({_Collection.workspaces, _Collection.sandboxes});
        throw _withLockHolder(e, workspaceId: id);
      }
      rethrow;
    }
    await _refetch({
      _Collection.workspaces,
      _Collection.sandboxes,
      _Collection.audit,
    });
  }

  @override
  List<PersistentStateRecord> listStates() => _states;

  @override
  Future<PersistentStateRecord> resetState(String stateId) async {
    try {
      await _call(
        'POST',
        '/states/${_seg(stateId)}/reset',
        body: const {'confirm': true},
        describe: (code, _) =>
            code == 'state_locked' ? '会话数据正在被使用，请先停止沙箱。' : null,
      );
    } on NullkeyApiException catch (e) {
      if (e.code == 'state_locked') {
        await _refetch({_Collection.sandboxes, _Collection.states});
        throw _withLockHolder(e, stateId: stateId);
      }
      rethrow;
    }
    await _refetch({_Collection.states, _Collection.audit});
    return _states.firstWhere((s) => s.id == stateId);
  }

  /// Core's `state_locked` does not say who holds the lock; the GUI needs
  /// it to offer "open that sandbox".
  NullkeyApiException _withLockHolder(
    NullkeyApiException e, {
    String? workspaceId,
    String? stateId,
  }) {
    if (e.lockingSandboxId != null) {
      return e;
    }
    final stateHolder = stateId == null
        ? null
        : _states.where((s) => s.id == stateId).firstOrNull?.lockedBySandboxId;
    final workspaceHolder = workspaceId == null
        ? null
        : (_workspacesRaw
                      .where((w) => w['id'] == workspaceId)
                      .firstOrNull?['locked_by']
                  as String?) ??
              _sandboxes
                  .where(
                    (s) =>
                        s.workspaceId == workspaceId &&
                        s.status.isRunningOrStarting,
                  )
                  .firstOrNull
                  ?.id;
    final holder = stateHolder ?? workspaceHolder;
    if (holder == null) {
      return e;
    }
    return NullkeyApiException(
      statusCode: e.statusCode,
      code: e.code,
      message: e.message,
      details: {
        ...e.details,
        'sandbox_id': holder,
        if (stateHolder != null) 'state_id': stateId,
        if (stateHolder == null) 'workspace_id': workspaceId,
      },
    );
  }

  // ---------------------------------------------------------------------------
  // Sandboxes & terminal
  // ---------------------------------------------------------------------------

  @override
  List<SandboxRecord> listSandboxes() => _sandboxes;

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
    final workspace = _workspaces.where((w) => w.id == workspaceId).firstOrNull;
    final _Json raw;
    try {
      raw = await _object(
        'POST',
        '/sandboxes',
        body: {
          'harness_id': _harnessId(harness),
          'workspace_id': workspaceId,
          'state_id': ?stateId,
          'runtime': enableDesktop ? 'desktop' : 'native',
          'network_mode': networkMode.wireName,
        },
        describe: (code, message) => switch (code) {
          'state_locked' when message.startsWith('workspace') =>
            '“${workspace?.name ?? '工作区'}”正在被另一个沙箱使用。',
          'state_locked' => '这份会话数据正在被另一个沙箱使用。',
          'harness_not_installed' when message.contains('binary') =>
            '这台 Mac 上找不到 ${_harnessId(harness)} 命令。先安装 ${harness.displayName}，再回来启动。',
          'harness_not_installed' => '${harness.displayName} 还没有安装。',
          'runtime_unavailable' => '沙箱没能启动。',
          _ => null,
        },
      );
    } on NullkeyApiException catch (e) {
      // A start that failed still leaves a sandbox record with the reason.
      await _refetch({
        _Collection.sandboxes,
        _Collection.workspaces,
        _Collection.states,
        _Collection.audit,
      }).catchError((_) {});
      if (e.code == 'state_locked') {
        throw _withLockHolder(e, workspaceId: workspaceId, stateId: stateId);
      }
      rethrow;
    }
    await _refetch({
      _Collection.sandboxes,
      _Collection.workspaces,
      _Collection.states,
      _Collection.audit,
    });
    final id = raw['id'] as String;
    return getSandbox(id) ??
        _mapSandbox(raw, {
          for (final w in _workspacesRaw) w['id'] as String: w,
        })!;
  }

  @override
  Future<SandboxRecord?> waitUntilRunning(String sandboxId) async {
    final deadline = DateTime.now().add(const Duration(seconds: 30));
    while (!_disposed) {
      final record = getSandbox(sandboxId);
      final settled =
          record == null ||
          (record.status != SandboxStatus.creating &&
              record.status != SandboxStatus.starting);
      if (settled || DateTime.now().isAfter(deadline)) {
        return record;
      }
      await Future<void>.delayed(const Duration(milliseconds: 300));
      await _refetch({_Collection.sandboxes});
    }
    return null;
  }

  @override
  Future<SandboxRecord> stopSandbox(String sandboxId) async {
    await _call(
      'POST',
      '/sandboxes/${_seg(sandboxId)}/stop',
      body: const {},
      describe: (code, _) => code == 'not_found' ? '找不到这个沙箱。' : null,
    );
    await _refetch({
      _Collection.sandboxes,
      _Collection.workspaces,
      _Collection.states,
      _Collection.audit,
    });
    _services.remove(sandboxId);
    return getSandbox(sandboxId)!;
  }

  @override
  Future<TerminalTicket> createTerminalTicket(String sandboxId) async {
    final raw = await _object(
      'POST',
      '/sandboxes/${_seg(sandboxId)}/terminal',
      body: const {},
      describe: (code, _) => switch (code) {
        'conflict' => '沙箱已停止。',
        'terminal_not_ready' => 'Agent 已经退出，终端不可用。停止沙箱后再启动一次。',
        _ => null,
      },
    );
    return TerminalTicket(
      wsUrl: raw['ws_url'] as String,
      ticket: raw['ticket'] as String,
      expiresAt:
          _time(raw['expires_at']) ??
          DateTime.now().toUtc().add(const Duration(seconds: 30)),
    );
  }

  @override
  Future<TerminalConnection> connectTerminal(TerminalTicket ticket) async {
    final WebSocket socket;
    try {
      socket = await WebSocket.connect(
        ticket.wsUrl,
        headers: {HttpHeaders.authorizationHeader: 'Bearer ${ticket.ticket}'},
      ).timeout(_requestTimeout);
    } on WebSocketException {
      // Core rejects a used or expired ticket before the upgrade.
      throw const NullkeyApiException(
        statusCode: 401,
        code: 'ticket_invalid',
        message: '终端连接已失效，请重新连接。',
      );
    } on IOException {
      throw _unreachable();
    } on TimeoutException {
      throw _unreachable();
    }
    socket.pingInterval = const Duration(seconds: 20);
    final connection = _DaemonTerminalConnection(
      socket,
      onClosed: _terminals.remove,
    );
    _terminals.add(connection);
    return connection;
  }

  @override
  List<SandboxServiceRecord> listServices(String sandboxId) =>
      _services[sandboxId] ?? const [];

  @override
  Future<void> refreshServices(String sandboxId) async {
    final items = await _list('/sandboxes/${_seg(sandboxId)}/services');
    _services[sandboxId] = List.unmodifiable([
      for (final raw in items)
        SandboxServiceRecord(
          id: raw['id'] as String,
          sandboxId: sandboxId,
          name: raw['name'] as String? ?? raw['id'] as String,
          status: ServiceStatus.fromWire(raw['status'] as String?),
          websocket: raw['websocket'] == true,
        ),
    ]);
    _emit();
  }

  @override
  Future<Uri> openService(String sandboxId, String serviceId) async {
    final raw = await _object(
      'POST',
      '/sandboxes/${_seg(sandboxId)}/services/${_seg(serviceId)}/ticket',
      body: const {},
      describe: (code, _) => switch (code) {
        'conflict' => '沙箱已停止。',
        'service_not_found' => '沙箱没有声明这个服务。',
        _ => null,
      },
    );
    final url = Uri.parse(raw['url'] as String);
    return url.replace(
      queryParameters: {
        ...url.queryParameters,
        'ticket': raw['ticket'] as String,
      },
    );
  }

  @override
  DesktopStatusRecord getDesktopStatus(String sandboxId) =>
      const DesktopStatusRecord(
        enabled: false,
        status: 'unavailable',
        resolution: '',
        fps: 0,
      );

  @override
  Future<DesktopStatusRecord> setDesktopEnabled(
    String sandboxId,
    bool enabled,
  ) async {
    if (!enabled) {
      return getDesktopStatus(sandboxId);
    }
    await _call(
      'POST',
      '/sandboxes/${_seg(sandboxId)}/desktop',
      body: const {},
    );
    return getDesktopStatus(sandboxId);
  }

  @override
  List<ArtifactRecord> listArtifacts(String sandboxId) =>
      _artifacts[sandboxId] ?? const [];

  /// Files whose content is fetched for a preview; the rest are listed only.
  static const int _maxPreviews = 24;
  static const int _previewLines = 200;

  @override
  Future<void> refreshArtifacts(String sandboxId) async {
    final workspaceId = getSandbox(sandboxId)?.workspaceId;
    if (workspaceId == null || workspaceId.isEmpty) {
      return;
    }
    final diff = await _object('GET', '/workspaces/${_seg(workspaceId)}/diff');
    final patch = diff['patch'] as String? ?? '';
    final files = ((diff['files'] as List?) ?? const []).whereType<String>();
    final sections = _splitPatch(patch);

    final records = <ArtifactRecord>[];
    final previews = <Future<void>>[];
    for (final path in files) {
      final section = sections[path];
      if (section != null) {
        records.add(_diffArtifact(sandboxId, workspaceId, path, section));
        continue;
      }
      final index = records.length;
      records.add(
        ArtifactRecord(
          id: '$workspaceId:$path',
          sandboxId: sandboxId,
          relativePath: path,
          kind: 'file',
          sizeBytes: 0,
          summary: '',
          previewSnippet: '',
        ),
      );
      if (previews.length < _maxPreviews) {
        previews.add(
          _filePreview(workspaceId, path).then((preview) {
            records[index] = ArtifactRecord(
              id: '$workspaceId:$path',
              sandboxId: sandboxId,
              relativePath: path,
              kind: 'file',
              sizeBytes: preview.size,
              summary: '',
              previewSnippet: preview.text,
            );
          }),
        );
      }
    }
    await Future.wait(previews);
    _artifacts[sandboxId] = List.unmodifiable(records);
    _emit();
  }

  static Map<String, String> _splitPatch(String patch) {
    final header = RegExp(r'^diff --git a/(.+?) b/(.+)$', multiLine: true);
    final matches = header.allMatches(patch).toList();
    return {
      for (var i = 0; i < matches.length; i++)
        matches[i].group(2)!: patch
            .substring(
              matches[i].start,
              i + 1 < matches.length ? matches[i + 1].start : patch.length,
            )
            .trimRight(),
    };
  }

  static ArtifactRecord _diffArtifact(
    String sandboxId,
    String workspaceId,
    String path,
    String section,
  ) {
    final lines = section.split('\n');
    final firstHunk = lines.indexWhere((l) => l.startsWith('@@'));
    final body = firstHunk == -1 ? lines : lines.sublist(firstHunk);
    var added = 0;
    var removed = 0;
    for (final line in body) {
      if (line.startsWith('+')) {
        added++;
      } else if (line.startsWith('-')) {
        removed++;
      }
    }
    return ArtifactRecord(
      id: '$workspaceId:$path',
      sandboxId: sandboxId,
      relativePath: path,
      kind: 'diff',
      sizeBytes: utf8.encode(section).length,
      summary: '+$added −$removed',
      previewSnippet: body.take(_previewLines).join('\n'),
    );
  }

  Future<({int size, String text})> _filePreview(
    String workspaceId,
    String path,
  ) async {
    try {
      final raw = await _object(
        'GET',
        '/workspaces/${_seg(workspaceId)}/file',
        query: {'path': path},
      );
      final content = raw['content'] as String? ?? '';
      if (raw['encoding'] == 'base64') {
        return (size: (content.length * 3) ~/ 4, text: '（二进制文件，无法预览）');
      }
      final lines = content.split('\n');
      final shown = lines.take(_previewLines).join('\n');
      return (
        size: utf8.encode(content).length,
        text: lines.length > _previewLines || raw['truncated'] == true
            ? '$shown\n…'
            : shown,
      );
    } on NullkeyApiException catch (e) {
      return (size: 0, text: '（${e.message}）');
    }
  }

  @override
  Future<SecretScanResult> scanSandboxSecrets(String sandboxId) async {
    final raw = await _object(
      'POST',
      '/sandboxes/${_seg(sandboxId)}/scan',
      body: const {},
      describe: (code, _) => code == 'internal' ? '没能完成检查，结果不代表没有凭证。' : null,
    );
    if (raw['ok'] != true) {
      throw const NullkeyApiException(
        statusCode: 500,
        code: 'internal',
        message: '没能完成检查，结果不代表没有凭证。',
      );
    }
    final checks = [
      for (final c in ((raw['checks'] as List?) ?? const []).whereType<Map>())
        SecretScanCheck(
          name: c['name'] as String? ?? '',
          clean: (c['hits'] as num? ?? 0) == 0,
          matches: (c['hits'] as num?)?.toInt() ?? 0,
          targetSummary: scanCheckLabel(c['name'] as String? ?? ''),
        ),
    ];
    unawaited(_refetch({_Collection.audit}).catchError((_) {}));
    return SecretScanResult(
      clean: (raw['hits'] as num? ?? 0) == 0,
      scope: raw['scope'] as String? ?? 'generated_runtime',
      excluded: ((raw['excluded'] as List?) ?? const [])
          .whereType<String>()
          .toList(),
      checks: checks,
      scannedAt: _time(raw['scanned_at']) ?? DateTime.now().toUtc(),
    );
  }

  // ---------------------------------------------------------------------------
  // Activity
  // ---------------------------------------------------------------------------

  @override
  List<AuditEventRecord> listAuditEvents() => _audit;

  @override
  Future<void> refreshAuditEvents() => _refetch({_Collection.audit});
}

/// Wording for Core's error codes and the messages Core is known to send.
/// Core's own messages are English and written for developers.
String describeCoreError(String code, String message) =>
    _knownCoreMessages[message] ??
    switch (code) {
      'unauthorized' => 'Nullkey 拒绝了这个应用的令牌。',
      'not_found' => '找不到这项内容，可能已被删除。',
      'conflict' => '状态已经变了，请刷新后再试。',
      'state_locked' => '正在被另一个沙箱使用。先停止那个沙箱。',
      'runtime_unavailable' => '沙箱没能启动。',
      'desktop_unavailable' => '这台 Mac 上的 Nullkey 还不支持虚拟桌面。',
      'service_not_found' => '沙箱没有声明这个服务。',
      'ticket_invalid' => '连接已失效，请重试。',
      'terminal_not_ready' => '终端还没准备好。',
      'reauth_required' => '登录已失效，需要重新连接。',
      'oauth_unavailable' => '这台 Mac 上的 Nullkey 还没有配置浏览器登录。',
      'oauth_failed' => '登录没有完成，请重试。',
      'oauth_state_invalid' => '这次登录已失效，请重新开始。',
      'keychain_unavailable' => '无法访问钥匙串，凭证没有保存。',
      'harness_not_installed' => 'Agent 还没有安装。',
      'mcp_unavailable' => 'MCP 服务没有响应。',
      'gateway_unavailable' => '上游服务暂时连不上。',
      'network_denied' => '网络规则拒绝了这次访问。',
      'not_implemented' => 'Nullkey 还不支持这个操作。',
      'invalid_request' => '请求的内容不完整或不正确。',
      _ => 'Nullkey 出了点问题，请稍后再试。',
    };

const Map<String, String> _knownCoreMessages = {
  'name is required': '请输入名称。',
  'name is too long': '名称太长了。',
  'root_path is invalid': '这个路径无效。请填写以 / 开头的完整路径。',
  'root_path must be an existing directory': '找不到这个文件夹。请填写一个已存在的文件夹。',
  'root_path must not be the filesystem root': '不能把磁盘根目录用作工作区。',
  "root_path must not be the user's home directory": '不能直接使用个人文件夹，请选择其中的项目文件夹。',
  'root_path points to a protected location': '这个位置受系统保护，不能用作工作区。',
  'a workspace with this root path already exists': '这个文件夹已经是一个工作区了。',
  'confirm_name must match the workspace name': '名称不一致，没有删除。',
  'delete_files is only allowed for managed workspaces': '已有文件夹不会被删除，只能从列表移除。',
  'OAuth callback port is already in use':
      '端口 1455 被占用。请先完成或取消 Codex CLI 的登录，再重试。',
  'another OAuth callback listener is already active':
      '已经有一个登录正在等待回调，请先完成或取消它。',
  'unknown provider_id': '还不支持这个服务。',
  'label is required': '请输入名称。',
  'auth_type is not supported by this provider': '这个服务不接受这种密钥。',
  'value is required': '请粘贴密钥。',
  'host login is not available': '本机上已经找不到这个登录。',
  'host login could not be parsed': '读不懂本机上的这个登录文件。',
  'provider does not support host import': '这个账号不能从本机导入。',
  'job has already completed': '任务已经结束。',
  'OAuth is not configured for this provider':
      '这台 Mac 上的 Nullkey 还没有配置这个账号的浏览器登录。',
  'OAuth flow has already completed': '登录已经完成。',
  'sandbox is not running': '沙箱已停止。',
  'harness is not installed': 'Agent 还没有安装。',
  'harness binary is not available':
      '这台 Mac 上找不到 Agent 的程序。安装后再启动，Nullkey 会从 PATH 和常见安装位置查找它。',
  'native runtime is not available on this host': '这台 Mac 不能运行沙箱。',
  'state harness_id does not match harness_id': '这份会话数据属于另一个 Agent。',
  'network_mode must be services_only, filtered, or open': '不支持这种网络设置。',
};

/// One WebSocket attached to a sandbox's PTY (`internal/terminal`).
class _DaemonTerminalConnection implements TerminalConnection {
  final WebSocket _socket;
  final void Function(_DaemonTerminalConnection) _onClosed;
  final Completer<String> _ready = Completer<String>();
  final StreamController<TerminalEvent> _events =
      StreamController<TerminalEvent>();

  /// Set once the connection's outcome is known, so it reports exactly one.
  bool _ended = false;
  bool _closedByUs = false;
  (int, int)? _pendingSize;

  _DaemonTerminalConnection(this._socket, {required this._onClosed}) {
    _ready.future.ignore();
    _socket.listen(
      _onMessage,
      onError: (Object _) => _finish(),
      onDone: _finish,
      cancelOnError: true,
    );
  }

  void _onMessage(Object? frame) {
    if (frame is! String) {
      return;
    }
    final decoded = DaemonBackend._tryDecode(frame);
    if (decoded is! Map) {
      return;
    }
    switch (decoded['type']) {
      case 'ready':
        if (!_ready.isCompleted) {
          _ready.complete(decoded['terminal_id'] as String? ?? '');
          final size = _pendingSize;
          if (size != null) {
            _send({'type': 'resize', 'cols': size.$1, 'rows': size.$2});
          }
        }
      case 'output':
        final data = decoded['data'];
        if (data is String && !_ended) {
          _events.add(TerminalOutput(data));
        }
      case 'exit':
        _end(TerminalExited((decoded['code'] as num?)?.toInt() ?? 0));
      case 'error':
        final error = decoded['error'];
        final code = error is Map ? error['code'] : null;
        _end(
          code == 'terminal_replaced'
              ? const TerminalReplaced()
              : TerminalDropped(
                  error is Map ? error['message'] as String? : null,
                ),
        );
    }
  }

  void _finish() {
    _end(_closedByUs ? null : const TerminalDropped());
    _onClosed(this);
  }

  void _end(TerminalEvent? event) {
    if (!_ready.isCompleted) {
      _ready.completeError(
        const NullkeyApiException(
          statusCode: 0,
          code: 'terminal_not_ready',
          message: '终端没有就绪就断开了。',
        ),
      );
    }
    if (_ended) {
      return;
    }
    _ended = true;
    if (event != null) {
      _events.add(event);
    }
    _events.close();
  }

  void _send(Map<String, Object?> message) {
    if (_socket.readyState == WebSocket.open) {
      _socket.add(jsonEncode(message));
    }
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
    _send({'type': 'input', 'data': data});
  }

  @override
  void resize(int cols, int rows) {
    if (_ended) {
      return;
    }
    if (!_ready.isCompleted) {
      _pendingSize = (cols, rows);
      return;
    }
    _send({'type': 'resize', 'cols': cols, 'rows': rows});
  }

  @override
  Future<void> close() async {
    _closedByUs = true;
    _end(null);
    await _socket.close(WebSocketStatus.normalClosure);
  }
}
