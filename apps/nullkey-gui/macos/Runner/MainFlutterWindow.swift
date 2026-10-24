import Cocoa
import CryptoKit
import FlutterMacOS
import LocalAuthentication

class MainFlutterWindow: NSWindow {
  private var passkeyChannel: FlutterMethodChannel?

  override func awakeFromNib() {
    let flutterViewController = FlutterViewController()
    let windowFrame = self.frame
    self.contentViewController = flutterViewController
    // The Flutter UI reserves the top 28pt (titlebarInset in common.dart) for
    // the traffic lights, so the sidebar can run to the top edge.
    self.titlebarAppearsTransparent = true
    self.titleVisibility = .hidden
    self.styleMask.insert(.fullSizeContentView)
    self.minSize = NSSize(width: 1080, height: 720)
    let initialFrame = NSRect(
      x: windowFrame.origin.x,
      y: windowFrame.origin.y,
      width: max(windowFrame.size.width, 1360),
      height: max(windowFrame.size.height, 860)
    )
    self.setFrame(initialFrame, display: true)

    RegisterGeneratedPlugins(registry: flutterViewController)
    registerPasskeyBridge(messenger: flutterViewController.engine.binaryMessenger)

    super.awakeFromNib()
  }

  private func registerPasskeyBridge(messenger: FlutterBinaryMessenger) {
    let channel = FlutterMethodChannel(
      name: "dev.nullkey.gui/passkey",
      binaryMessenger: messenger
    )
    channel.setMethodCallHandler { [weak self] call, result in
      self?.handlePasskeyCall(call: call, result: result)
    }
    self.passkeyChannel = channel
  }

  private func handlePasskeyCall(call: FlutterMethodCall, result: @escaping FlutterResult) {
    let args = call.arguments as? [String: Any] ?? [:]
    let rpId = (args["rpId"] as? String) ?? "local.nullkey.vault"

    switch call.method {
    case "capabilities":
      result(queryCapabilities(rpId: rpId))

    case "enroll":
      let accountName = (args["accountName"] as? String) ?? "nullkey-local-operator"
      let reason = (args["reason"] as? String) ?? "启用 Nullkey 本机验证"
      let interactive = (args["interactive"] as? Bool) ?? true
      authenticateIfNeeded(reason: reason, interactive: interactive) { authOk, authMethod, errorMsg in
        guard authOk else {
          result(
            FlutterError(
              code: "passkey_user_cancelled",
              message: errorMsg ?? "Passkey 生物识别/系统鉴权已取消",
              details: nil
            )
          )
          return
        }
        do {
          let record = try PasskeyVaultStore.shared.enroll(
            rpId: rpId,
            accountName: accountName,
            authMethod: authMethod
          )
          result(record)
        } catch {
          result(
            FlutterError(
              code: "passkey_enroll_failed",
              message: error.localizedDescription,
              details: nil
            )
          )
        }
      }

    case "assert":
      let challenge = (args["challenge"] as? String) ?? UUID().uuidString
      let scope = (args["scope"] as? String) ?? "vault.unlock"
      let reason = (args["reason"] as? String) ?? "验证身份以授权 Nullkey 敏感操作"
      let interactive = (args["interactive"] as? Bool) ?? true
      authenticateIfNeeded(reason: reason, interactive: interactive) { authOk, authMethod, errorMsg in
        guard authOk else {
          result(
            FlutterError(
              code: "passkey_user_cancelled",
              message: errorMsg ?? "Passkey 授权未通过",
              details: nil
            )
          )
          return
        }
        do {
          let assertion = try PasskeyVaultStore.shared.signAssertion(
            rpId: rpId,
            challenge: challenge,
            scope: scope,
            authMethod: authMethod
          )
          result(assertion)
        } catch {
          result(
            FlutterError(
              code: "passkey_assert_failed",
              message: error.localizedDescription,
              details: nil
            )
          )
        }
      }

    case "revoke":
      PasskeyVaultStore.shared.revoke()
      result(["revoked": true])

    default:
      result(FlutterMethodNotImplemented)
    }
  }

  private func queryCapabilities(rpId: String) -> [String: Any] {
    let context = LAContext()
    var bioError: NSError?
    let biometryAvailable = context.canEvaluatePolicy(
      .deviceOwnerAuthenticationWithBiometrics,
      error: &bioError
    )
    var ownerError: NSError?
    let ownerAuthAvailable = context.canEvaluatePolicy(
      .deviceOwnerAuthentication,
      error: &ownerError
    )

    let biometryType: String
    switch context.biometryType {
    case .touchID:
      biometryType = "Touch ID"
    case .faceID:
      biometryType = "Face ID"
    case .opticID:
      biometryType = "Optic ID"
    case .none:
      biometryType = ownerAuthAvailable ? "macOS Passcode / Watch" : "Unavailable"
    @unknown default:
      biometryType = "Touch ID"
    }

    let snapshot = PasskeyVaultStore.shared.currentRegistration(rpId: rpId)
    return [
      "rpId": rpId,
      "biometryAvailable": biometryAvailable,
      "deviceOwnerAuthAvailable": ownerAuthAvailable,
      "secureEnclaveAvailable": SecureEnclave.isAvailable,
      "biometryType": biometryType,
      "enrolled": snapshot != nil,
      "registration": snapshot as Any,
    ]
  }

  private func authenticateIfNeeded(
    reason: String,
    interactive: Bool,
    completion: @escaping (Bool, String, String?) -> Void
  ) {
    if !interactive {
      completion(false, "macOS Device Owner Auth", "本机验证必须通过系统认证。")
      return
    }
    let context = LAContext()
    context.localizedCancelTitle = "取消授权"
    var error: NSError?
    if context.canEvaluatePolicy(.deviceOwnerAuthentication, error: &error) {
      let methodLabel = context.biometryType == .touchID ? "Touch ID" : "macOS Device Owner Auth"
      context.evaluatePolicy(.deviceOwnerAuthentication, localizedReason: reason) { success, evalError in
        DispatchQueue.main.async {
          if success {
            completion(true, methodLabel, nil)
          } else {
            completion(false, methodLabel, evalError?.localizedDescription)
          }
        }
      }
    } else {
      completion(false, "macOS Device Owner Auth", error?.localizedDescription ?? "macOS 系统验证不可用。")
    }
  }
}

private final class PasskeyVaultStore {
  static let shared = PasskeyVaultStore()

  private let defaults = UserDefaults.standard
  private let privateKeySeedKey = "dev.nullkey.gui.passkey.p256.raw"
  private let credentialIdKey = "dev.nullkey.gui.passkey.credentialId"
  private let accountNameKey = "dev.nullkey.gui.passkey.accountName"
  private let createdAtKey = "dev.nullkey.gui.passkey.createdAt"
  private let signCountKey = "dev.nullkey.gui.passkey.signCount"

  // This software key signs local GUI receipts; it is not a Secure Enclave
  // or WebAuthn credential and is not used to authenticate daemon requests.
  private func loadOrCreatePrivateKey() -> P256.Signing.PrivateKey {
    if let rawData = defaults.data(forKey: privateKeySeedKey),
       let existing = try? P256.Signing.PrivateKey(rawRepresentation: rawData) {
      return existing
    }
    let generated = P256.Signing.PrivateKey()
    defaults.set(generated.rawRepresentation, forKey: privateKeySeedKey)
    return generated
  }

  func currentRegistration(rpId: String) -> [String: Any]? {
    guard let credentialId = defaults.string(forKey: credentialIdKey) else {
      return nil
    }
    let key = loadOrCreatePrivateKey()
    let spkiBase64 = key.publicKey.derRepresentation.base64EncodedString()
    let accountName = defaults.string(forKey: accountNameKey) ?? "nullkey-local-operator"
    let createdAt = defaults.string(forKey: createdAtKey) ?? ISO8601DateFormatter().string(from: Date())
    let signCount = defaults.integer(forKey: signCountKey)
    return [
      "credentialId": credentialId,
      "rpId": rpId,
      "accountName": accountName,
      "publicKeySpkiBase64": spkiBase64,
      "authenticatorAttachment": "platform",
      "backedBySecureEnclave": false,
      "createdAt": createdAt,
      "signCount": signCount,
    ]
  }

  func enroll(rpId: String, accountName: String, authMethod: String) throws -> [String: Any] {
    let key = loadOrCreatePrivateKey()
    let pubDigest = SHA256.hash(data: key.publicKey.rawRepresentation)
    let digestBytes = Array(pubDigest.prefix(12))
    let hexId = digestBytes.map { String(format: "%02x", $0) }.joined()
    let credentialId = "nk-passkey-\(hexId)"
    let nowIso = ISO8601DateFormatter().string(from: Date())

    defaults.set(credentialId, forKey: credentialIdKey)
    defaults.set(accountName, forKey: accountNameKey)
    defaults.set(nowIso, forKey: createdAtKey)
    if defaults.object(forKey: signCountKey) == nil {
      defaults.set(1, forKey: signCountKey)
    }

    return [
      "credentialId": credentialId,
      "rpId": rpId,
      "accountName": accountName,
      "publicKeySpkiBase64": key.publicKey.derRepresentation.base64EncodedString(),
      "authenticatorAttachment": "platform",
      "backedBySecureEnclave": false,
      "authMethod": authMethod,
      "createdAt": nowIso,
      "signCount": defaults.integer(forKey: signCountKey),
    ]
  }

  func signAssertion(
    rpId: String,
    challenge: String,
    scope: String,
    authMethod: String
  ) throws -> [String: Any] {
    if defaults.string(forKey: credentialIdKey) == nil {
      _ = try enroll(rpId: rpId, accountName: "nullkey-local-operator", authMethod: authMethod)
    }
    let key = loadOrCreatePrivateKey()
    let credentialId = defaults.string(forKey: credentialIdKey) ?? "nk-passkey-default"
    let nextCount = defaults.integer(forKey: signCountKey) + 1
    defaults.set(nextCount, forKey: signCountKey)

    let clientDataDict: [String: String] = [
      "type": "webauthn.get",
      "challenge": challenge,
      "origin": "https://\(rpId)",
      "scope": scope,
    ]
    let clientDataJSON = try JSONSerialization.data(withJSONObject: clientDataDict, options: [.sortedKeys])
    let clientDataHash = SHA256.hash(data: clientDataJSON)

    let rpIdHash = SHA256.hash(data: Data(rpId.utf8))
    var authenticatorData = Data(rpIdHash)
    // Flags: UP (0x01) | UV (0x04) = 0x05
    authenticatorData.append(0x05)
    var bigEndianCounter = UInt32(nextCount).bigEndian
    withUnsafeBytes(of: &bigEndianCounter) { authenticatorData.append(contentsOf: $0) }

    var signedPayload = Data()
    signedPayload.append(authenticatorData)
    signedPayload.append(contentsOf: clientDataHash)

    let signature = try key.signature(for: signedPayload)
    let verifiedAt = ISO8601DateFormatter().string(from: Date())

    return [
      "credentialId": credentialId,
      "rpId": rpId,
      "scope": scope,
      "challenge": challenge,
      "authMethod": authMethod,
      "clientDataJSONBase64": clientDataJSON.base64EncodedString(),
      "authenticatorDataBase64": authenticatorData.base64EncodedString(),
      "signatureBase64": signature.derRepresentation.base64EncodedString(),
      "signCount": nextCount,
      "userVerified": true,
      "backedBySecureEnclave": false,
      "verifiedAt": verifiedAt,
    ]
  }

  func revoke() {
    defaults.removeObject(forKey: credentialIdKey)
    defaults.removeObject(forKey: accountNameKey)
    defaults.removeObject(forKey: createdAtKey)
    defaults.removeObject(forKey: signCountKey)
  }
}

