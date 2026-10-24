import 'dart:convert';
import 'dart:math';

import 'package:flutter/services.dart';

import '../models/nullkey_models.dart';

/// Local macOS authentication for sensitive GUI actions. Native failures never
/// fall back to the deterministic demo/test implementation. The native receipt
/// uses a software P-256 key; it is not a WebAuthn or Secure Enclave credential.
class PasskeySecurityService {
  static const String channelName = 'dev.nullkey.gui/passkey';
  static const String defaultRpId = 'local.nullkey.vault';

  final MethodChannel _channel;
  final bool interactiveBiometrics;
  final bool useNativeBridge;

  PasskeyRegistration? _cachedRegistration;
  int _fallbackSignCounter = 1;

  /// Allows contract drills or tests to simulate a user cancelling Touch ID / Passkey prompt.
  bool simulateNextCancellation = false;

  PasskeySecurityService({
    MethodChannel? channel,
    this.interactiveBiometrics = true,
    this.useNativeBridge = false,
  }) : _channel = channel ?? const MethodChannel(channelName) {
    if (useNativeBridge) return;
    _cachedRegistration = const PasskeyRegistration(
      credentialId: 'nk-passkey-se-9f4a2c81b03e',
      rpId: defaultRpId,
      accountName: 'corcton@macbook-pro.local',
      publicKeySpkiBase64: 'MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE8xNullkeyAppleSecureEnclaveP256Key9f4a2c81b03e==',
      authenticatorAttachment: 'platform',
      backedBySecureEnclave: true,
      authMethod: 'Touch ID · Secure Enclave',
      createdAt: '2026-04-15T08:00:00Z',
      signCount: 1,
    );
  }

  Future<PasskeyCapabilities> queryCapabilities({
    String rpId = defaultRpId,
  }) async {
    if (useNativeBridge) {
      try {
        final raw = await _channel.invokeMapMethod<Object?, Object?>(
          'capabilities',
          {'rpId': rpId},
        );
        if (raw != null) {
          final regMap = raw['registration'] as Map<Object?, Object?>?;
          final reg = regMap != null
              ? PasskeyRegistration.fromMap(regMap)
              : null;
          if (reg != null) {
            _cachedRegistration = reg;
          }
          return PasskeyCapabilities(
            rpId: (raw['rpId'] as String?) ?? rpId,
            biometryAvailable: (raw['biometryAvailable'] as bool?) ?? false,
            deviceOwnerAuthAvailable:
                (raw['deviceOwnerAuthAvailable'] as bool?) ?? false,
            secureEnclaveAvailable:
                (raw['secureEnclaveAvailable'] as bool?) ?? false,
            biometryType: (raw['biometryType'] as String?) ?? '不可用',
            enrolled: ((raw['enrolled'] as bool?) ?? false) || reg != null,
            registration: reg,
          );
        }
      } on MissingPluginException {
        // Report unavailable; never substitute demo capabilities.
      } on PlatformException {
        // Report unavailable; never substitute demo capabilities.
      }
      return PasskeyCapabilities(
        rpId: rpId,
        biometryAvailable: false,
        deviceOwnerAuthAvailable: false,
        secureEnclaveAvailable: false,
        biometryType: '不可用',
        enrolled: false,
      );
    }

    return PasskeyCapabilities(
      rpId: rpId,
      biometryAvailable: true,
      deviceOwnerAuthAvailable: true,
      secureEnclaveAvailable: true,
      biometryType: 'Touch ID · Secure Enclave',
      enrolled: _cachedRegistration != null,
      registration: _cachedRegistration,
    );
  }

  Future<PasskeyRegistration> enrollPasskey({
    String rpId = defaultRpId,
    String accountName = 'corcton@macbook-pro.local',
    String reason = 'Enroll Nullkey Passkey',
    bool? interactive,
  }) async {
    if (simulateNextCancellation) {
      simulateNextCancellation = false;
      throw const NullkeyApiException(
        statusCode: 401,
        code: 'passkey_user_cancelled',
        message: '已取消验证，没有做任何更改。',
      );
    }

    if (useNativeBridge) {
      try {
        final raw = await _channel.invokeMapMethod<Object?, Object?>('enroll', {
          'rpId': rpId,
          'accountName': accountName,
          'reason': reason,
          'interactive': interactive ?? interactiveBiometrics,
        });
        if (raw != null &&
            raw['rpId'] == rpId &&
            _hasText(raw, 'credentialId') &&
            _hasText(raw, 'publicKeySpkiBase64')) {
          final reg = PasskeyRegistration.fromMap(raw);
          _cachedRegistration = reg;
          return reg;
        }
      } on PlatformException catch (e) {
        throw NullkeyApiException(
          statusCode: 401,
          code: e.code,
          message: e.message ?? '已取消验证，没有做任何更改。',
        );
      } on MissingPluginException {
        throw _nativeUnavailable;
      }
      throw _nativeUnavailable;
    }

    final nowIso = DateTime.now().toUtc().toIso8601String();
    final registration = PasskeyRegistration(
      credentialId: 'nk-passkey-se-9f4a2c81b03e',
      rpId: rpId,
      accountName: accountName,
      publicKeySpkiBase64: 'MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE8xNullkeyAppleSecureEnclaveP256Key9f4a2c81b03e==',
      authenticatorAttachment: 'platform',
      backedBySecureEnclave: true,
      authMethod: 'Touch ID · Secure Enclave',
      createdAt: nowIso,
      signCount: _fallbackSignCounter,
    );
    _cachedRegistration = registration;
    return registration;
  }

  Future<PasskeyAssertion> assertPasskey({
    required PasskeyScope scope,
    String rpId = defaultRpId,
    String? customReason,
    bool? interactive,
  }) async {
    final sw = Stopwatch()..start();
    if (simulateNextCancellation) {
      simulateNextCancellation = false;
      throw NullkeyApiException(
        statusCode: 401,
        code: 'passkey_user_cancelled',
        message: '已取消验证，没有做任何更改。',
      );
    }

    final challenge = _generateChallenge(scope.id);
    final reason = customReason ?? scope.reason;

    if (useNativeBridge) {
      try {
        final raw = await _channel.invokeMapMethod<Object?, Object?>('assert', {
          'rpId': rpId,
          'challenge': challenge,
          'scope': scope.id,
          'reason': reason,
          'interactive': interactive ?? interactiveBiometrics,
        });
        if (raw == null ||
            raw['userVerified'] != true ||
            raw['rpId'] != rpId ||
            raw['scope'] != scope.id ||
            raw['challenge'] != challenge ||
            !_hasText(raw, 'credentialId') ||
            !_hasText(raw, 'clientDataJSONBase64') ||
            !_hasText(raw, 'authenticatorDataBase64') ||
            !_hasText(raw, 'signatureBase64') ||
            raw['signCount'] is! num) {
          throw _nativeUnavailable;
        }
        return PasskeyAssertion(
          credentialId: raw['credentialId'] as String,
          rpId: rpId,
          scope: scope,
          challenge: challenge,
          authMethod: (raw['authMethod'] as String?) ?? 'macOS 系统验证',
          clientDataJSONBase64: raw['clientDataJSONBase64'] as String,
          authenticatorDataBase64: raw['authenticatorDataBase64'] as String,
          signatureBase64: raw['signatureBase64'] as String,
          signCount: (raw['signCount'] as num).toInt(),
          userVerified: true,
          backedBySecureEnclave: raw['backedBySecureEnclave'] == true,
          verifiedAt: DateTime.now().toUtc(),
          latencyMs: sw.elapsedMilliseconds,
        );
      } on PlatformException catch (e) {
        throw NullkeyApiException(
          statusCode: 401,
          code: e.code,
          message: e.message ?? '已取消验证，没有做任何更改。',
        );
      } on MissingPluginException {
        throw _nativeUnavailable;
      }
    }

    sw.stop();
    final nextCount = ++_fallbackSignCounter;
    _cachedRegistration = PasskeyRegistration(
      credentialId:
          _cachedRegistration?.credentialId ?? 'nk-passkey-se-9f4a2c81b03e',
      rpId: rpId,
      accountName:
          _cachedRegistration?.accountName ?? 'corcton@macbook-pro.local',
      publicKeySpkiBase64: _cachedRegistration?.publicKeySpkiBase64 ?? 'MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE8xNullkeyAppleSecureEnclaveP256Key9f4a2c81b03e==',
      authenticatorAttachment: 'platform',
      backedBySecureEnclave: true,
      authMethod: 'Touch ID · Secure Enclave',
      createdAt:
          _cachedRegistration?.createdAt ??
          DateTime.now().toUtc().toIso8601String(),
      signCount: nextCount,
    );
    final clientDataB64 = _encodeClientData(
      rpId: rpId,
      challenge: challenge,
      scope: scope,
    );
    final authDataB64 = base64Encode(
      utf8.encode('rp:$rpId|flags:0x05|cnt:$nextCount'),
    );
    final sigB64 = base64Encode(
      utf8.encode(
        'MEUCIQDs9NullkeyEnclaveSig-${scope.id}-$nextCount-${challenge.substring(0, 8)}',
      ),
    );

    return PasskeyAssertion(
      credentialId: _cachedRegistration!.credentialId,
      rpId: rpId,
      scope: scope,
      challenge: challenge,
      authMethod: 'Touch ID · Secure Enclave',
      clientDataJSONBase64: clientDataB64,
      authenticatorDataBase64: authDataB64,
      signatureBase64: sigB64,
      signCount: nextCount,
      userVerified: true,
      backedBySecureEnclave: true,
      verifiedAt: DateTime.now().toUtc(),
      latencyMs: max(11, sw.elapsedMilliseconds),
    );
  }

  Future<void> revokePasskey() async {
    if (useNativeBridge) {
      try {
        await _channel.invokeMethod<Object?>('revoke');
      } on MissingPluginException {
        throw _nativeUnavailable;
      } on PlatformException {
        throw _nativeUnavailable;
      }
    }
    _cachedRegistration = null;
  }

  static const _nativeUnavailable = NullkeyApiException(
    statusCode: 401,
    code: 'passkey_unavailable',
    message: '本机验证未完成，操作已停止。请确认 macOS 系统验证可用后重试。',
  );

  static bool _hasText(Map<Object?, Object?> raw, String key) =>
      raw[key] is String && (raw[key] as String).isNotEmpty;

  String _generateChallenge(String scopeId) {
    final micros = DateTime.now().microsecondsSinceEpoch;
    final raw = 'nullkey:$scopeId:$micros:$_fallbackSignCounter';
    return base64UrlEncode(utf8.encode(raw)).replaceAll('=', '');
  }

  String _encodeClientData({
    required String rpId,
    required String challenge,
    required PasskeyScope scope,
  }) {
    final payload = jsonEncode({
      'type': 'webauthn.get',
      'challenge': challenge,
      'origin': 'https://$rpId',
      'scope': scope.id,
    });
    return base64Encode(utf8.encode(payload));
  }
}
