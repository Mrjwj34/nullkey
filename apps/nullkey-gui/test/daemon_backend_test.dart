import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:nullkey_gui/models/nullkey_models.dart';
import 'package:nullkey_gui/services/daemon_backend.dart';

/// A stand-in for nullkeyd's control API: lists come from [collections],
/// `/events` stays open so tests can push events.
class _FakeDaemon {
  static const token = 'test-token-0123456789';

  final HttpServer _server;
  final Map<String, List<Map<String, Object?>>> collections = {};
  final List<HttpResponse> _eventStreams = [];
  final _eventStreamOpened = StreamController<void>.broadcast();

  _FakeDaemon._(this._server) {
    _server.listen(_handle);
  }

  static Future<_FakeDaemon> start() async =>
      _FakeDaemon._(await HttpServer.bind(InternetAddress.loopbackIPv4, 0));

  Uri get url => Uri.parse('http://127.0.0.1:${_server.port}');

  Future<void> get eventStreamOpened => _eventStreams.isNotEmpty
      ? Future.value()
      : _eventStreamOpened.stream.first;

  void push(String type, Map<String, Object?> data) {
    for (final response in _eventStreams) {
      response.write('event: $type\ndata: ${jsonEncode(data)}\n\n');
      unawaited(response.flush());
    }
  }

  Future<void> close() async {
    for (final response in _eventStreams) {
      await response.close();
    }
    await _server.close(force: true);
  }

  Future<void> _handle(HttpRequest request) async {
    final path = request.uri.path.replaceFirst('/api/v1', '');
    final response = request.response;
    if (request.headers.value(HttpHeaders.authorizationHeader) !=
        'Bearer $token') {
      return _json(response, 401, {
        'error': {'code': 'unauthorized', 'message': 'invalid token'},
      });
    }
    if (path == '/events') {
      // dart:io holds headers back until the first bytes, and buffered
      // output would hold events back until the buffer fills.
      response
        ..bufferOutput = false
        ..statusCode = 200
        ..headers.contentType = ContentType('text', 'event-stream')
        ..write(': connected\n\n');
      await response.flush();
      _eventStreams.add(response);
      _eventStreamOpened.add(null);
      return;
    }
    if (request.method == 'POST' && path == '/workspaces') {
      return _json(response, 409, {
        'error': {
          'code': 'conflict',
          'message': 'a workspace with this root path already exists',
        },
      });
    }
    return switch (path) {
      '/capabilities' => _json(response, 200, {}),
      '/health' => _json(response, 200, {'status': 'ok', 'version': 'test'}),
      _ => _json(response, 200, {
        'items': collections[path] ?? const [],
        'next_cursor': null,
      }),
    };
  }

  static Future<void> _json(
    HttpResponse response,
    int status,
    Object body,
  ) async {
    response
      ..statusCode = status
      ..headers.contentType = ContentType.json
      ..write(jsonEncode(body));
    await response.close();
  }
}

Map<String, Object?> _workspace(String id, String name) => {
  'id': id,
  'name': name,
  'storage_kind': 'managed',
  'root_path': '/tmp/$name',
  'default_mode': 'rw',
  'created_at': '2026-01-01T00:00:00Z',
};

void main() {
  late _FakeDaemon daemon;

  setUp(() async => daemon = await _FakeDaemon.start());
  tearDown(() => daemon.close());

  DaemonBackend connect({String token = _FakeDaemon.token}) {
    final backend = DaemonBackend(baseUrl: daemon.url, token: token);
    addTearDown(backend.dispose);
    return backend;
  }

  test('loads what Core reports', () async {
    daemon.collections['/workspaces'] = [_workspace('ws_1', 'demo')];
    final backend = connect();
    await backend.loaded;
    expect(backend.connectionProblem, isNull);
    expect(backend.listWorkspaces().single.name, 'demo');
    expect(
      backend.capabilities.connectionMode,
      DaemonConnectionMode.liveDaemon,
    );
  });

  test('a rejected token is reported as a connection problem', () async {
    final backend = connect(token: 'wrong-token-0123456789');
    await backend.loaded;
    expect(backend.connectionProblem, contains('NULLKEY_DAEMON_TOKEN'));
    expect(backend.listWorkspaces(), isEmpty);
  });

  test('an unreachable daemon is reported, not treated as empty', () async {
    final url = daemon.url;
    await daemon.close();
    final backend = DaemonBackend(baseUrl: url, token: _FakeDaemon.token);
    addTearDown(backend.dispose);
    await backend.loaded;
    expect(backend.connectionProblem, contains('nullkeyd'));
    // Restart a server so tearDown has something to close.
    daemon = await _FakeDaemon.start();
  });

  test('Core error messages reach the user in their words', () async {
    final backend = connect();
    await backend.loaded;
    await expectLater(
      backend.createWorkspace(name: 'demo', kind: WorkspaceKind.managed),
      throwsA(
        isA<NullkeyApiException>()
            .having((e) => e.code, 'code', 'conflict')
            .having((e) => e.message, 'message', '这个文件夹已经是一个工作区了。'),
      ),
    );
  });

  test('an event refetches the collection it touches', () async {
    final backend = connect();
    await backend.loaded;
    await daemon.eventStreamOpened;
    expect(backend.listWorkspaces(), isEmpty);

    daemon.collections['/workspaces'] = [_workspace('ws_2', 'later')];
    final changed = backend.changes.firstWhere(
      (_) => backend.listWorkspaces().isNotEmpty,
    );
    daemon.push('workspace.created', {'workspace_id': 'ws_2'});
    await changed.timeout(const Duration(seconds: 5));
    expect(backend.listWorkspaces().single.id, 'ws_2');
  });
}
