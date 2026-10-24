import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:nullkey_gui/models/nullkey_models.dart';
import 'package:nullkey_gui/services/daemon_backend.dart';
import 'package:nullkey_gui/services/passkey_service.dart';
import 'package:nullkey_gui/state/nullkey_studio_controller.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  const channel = MethodChannel(PasskeySecurityService.channelName);
  final messenger =
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;
  late PasskeySecurityService service;

  setUp(() {
    service = PasskeySecurityService(useNativeBridge: true);
  });
  tearDown(() => messenger.setMockMethodCallHandler(channel, null));

  final denied = throwsA(isA<NullkeyApiException>());

  test('live controller requires native interactive authentication', () async {
    final api = DaemonBackend(
      baseUrl: Uri.parse('http://127.0.0.1:1'),
      token: '',
    );
    final controller = NullkeyStudioController(
      api: api,
      initiallyUnlocked: false,
    );
    addTearDown(api.dispose);
    addTearDown(controller.dispose);
    expect(controller.passkey.useNativeBridge, isTrue);
    expect(controller.passkey.interactiveBiometrics, isTrue);
    expect(await controller.unlockStudioWithPasskey(), isNull);
    expect(controller.studioUnlocked, isFalse);
  });

  test('missing native bridge never grants mock authentication', () async {
    final capabilities = await service.queryCapabilities();
    expect(capabilities.deviceOwnerAuthAvailable, isFalse);
    expect(capabilities.enrolled, isFalse);
    expect(capabilities.registration, isNull);
    await expectLater(
      service.assertPasskey(scope: PasskeyScope.vaultUnlock),
      denied,
    );
    await expectLater(service.enrollPasskey(), denied);
    await expectLater(service.revokePasskey(), denied);
  });

  test('null native responses cannot authorize an operation', () async {
    messenger.setMockMethodCallHandler(channel, (_) async => null);
    await expectLater(
      service.assertPasskey(scope: PasskeyScope.credentialMutation),
      denied,
    );
    await expectLater(service.enrollPasskey(), denied);
  });

  test('cancelled system prompt fails closed', () async {
    messenger.setMockMethodCallHandler(channel, (_) async {
      throw PlatformException(code: 'passkey_user_cancelled', message: '已取消验证');
    });
    await expectLater(
      service.assertPasskey(scope: PasskeyScope.destructiveStorage),
      denied,
    );
  });

  test(
    'assertion requires verification, matching challenge and native fields',
    () async {
      for (final mutation in [
        'unverified',
        'challenge',
        'signature',
        'rpId',
        'scope',
      ]) {
        messenger.setMockMethodCallHandler(channel, (call) async {
          final result = _assertion(call);
          switch (mutation) {
            case 'unverified':
              result['userVerified'] = false;
            case 'challenge':
              result['challenge'] = 'stale-challenge';
            case 'signature':
              result.remove('signatureBase64');
            case 'rpId':
              result['rpId'] = 'different.local';
            case 'scope':
              result['scope'] = 'different.operation';
          }
          return result;
        });
        await expectLater(
          service.assertPasskey(scope: PasskeyScope.vaultUnlock),
          denied,
          reason: mutation,
        );
      }
    },
  );

  test('verified native response preserves software key provenance', () async {
    messenger.setMockMethodCallHandler(channel, (call) async {
      expect((call.arguments as Map)['interactive'], isTrue);
      return _assertion(call);
    });
    final assertion = await service.assertPasskey(
      scope: PasskeyScope.vaultUnlock,
    );
    expect(assertion.userVerified, isTrue);
    expect(assertion.signatureBase64, 'dGVzdC1zaWduYXR1cmU=');
    expect(assertion.backedBySecureEnclave, isFalse);
  });
}

Map<String, Object?> _assertion(MethodCall call) {
  final args = call.arguments as Map;
  return {
    'credentialId': 'native-test',
    'rpId': args['rpId'],
    'challenge': args['challenge'],
    'scope': args['scope'],
    'authMethod': 'macOS Device Owner Auth',
    'clientDataJSONBase64': 'e30=',
    'authenticatorDataBase64': 'dGVzdC1kYXRh',
    'signatureBase64': 'dGVzdC1zaWduYXR1cmU=',
    'signCount': 2,
    'userVerified': true,
    'backedBySecureEnclave': false,
  };
}
