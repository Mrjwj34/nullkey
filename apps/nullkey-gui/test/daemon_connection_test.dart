import 'package:flutter_test/flutter_test.dart';
import 'package:nullkey_gui/services/daemon_connection.dart';

void main() {
  test('explicit url and token win', () {
    final connection = resolveDaemonConnection(
      url: 'http://127.0.0.1:50000',
      token: 'explicit',
      fileToken: 'from-file',
    );
    expect(connection!.baseUrl, Uri.parse('http://127.0.0.1:50000'));
    expect(connection.token, 'explicit');
  });

  test('token file is used with the default url', () {
    final connection = resolveDaemonConnection(fileToken: ' from-file\n');
    expect(connection!.baseUrl, Uri.parse(defaultDaemonUrl));
    expect(connection.token, 'from-file');
  });

  test('url without a token waits for a daemon', () {
    expect(resolveDaemonConnection(url: defaultDaemonUrl)!.token, isEmpty);
  });

  test('blank values are ignored', () {
    expect(
      resolveDaemonConnection(token: '   ', fileToken: '\n')!.token,
      isEmpty,
    );
  });

  test('malformed url is rejected', () {
    expect(resolveDaemonConnection(url: 'not a url', token: 'tok'), isNull);
  });

  test('token path follows NULLKEY_HOME when set', () {
    expect(
      daemonTokenPath(
        environment: {'NULLKEY_HOME': '/tmp/nk', 'HOME': '/Users/x'},
      ),
      '/tmp/nk/daemon-token',
    );
  });

  test('token path defaults to the daemon home under Application Support', () {
    expect(
      daemonTokenPath(environment: {'HOME': '/Users/x'}),
      '/Users/x/Library/Application Support/Nullkey/daemon-token',
    );
  });

  test('token path is unknown without a home', () {
    expect(daemonTokenPath(environment: {}), isNull);
  });

  test('missing token file reads as null', () {
    expect(
      readDaemonToken(environment: {'NULLKEY_HOME': '/tmp/nk-does-not-exist'}),
      isNull,
    );
  });
}
