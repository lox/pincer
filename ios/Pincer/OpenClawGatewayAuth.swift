import CryptoKit
import Foundation

struct GatewayConnectAuthSelection: Equatable {
    let token: String?
    let deviceToken: String?
    let signatureToken: String?
}

struct GatewayClientProfile {
    static let clientID = "openclaw-ios"
    static let clientMode = "ui"
    static let role = "operator"
    static let scopes = [
        "operator.read",
        "operator.write",
        "operator.approvals",
    ]
    static let displayName = "Pincer"

    static var clientVersion: String {
        let shortVersion = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String
        let buildVersion = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String

        if let shortVersion = normalized(shortVersion),
           let buildVersion = normalized(buildVersion) {
            return "\(shortVersion) (\(buildVersion))"
        }

        return normalized(shortVersion) ?? normalized(buildVersion) ?? "0.1.0"
    }

    static var platform: String {
        "ios"
    }

    static var deviceFamily: String {
        #if targetEnvironment(simulator)
        "simulator"
        #else
        "iphone"
        #endif
    }

    private static func normalized(_ value: String?) -> String? {
        let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? nil : trimmed
    }
}

func selectGatewayConnectAuth(bearerToken: String, storedDeviceToken: String?) -> GatewayConnectAuthSelection {
    let trimmedBearerToken = trimmedOrNil(bearerToken)
    let trimmedDeviceToken = trimmedOrNil(storedDeviceToken)

    if let trimmedBearerToken {
        return GatewayConnectAuthSelection(
            token: trimmedBearerToken,
            deviceToken: nil,
            signatureToken: trimmedBearerToken
        )
    }

    if let trimmedDeviceToken {
        return GatewayConnectAuthSelection(
            token: trimmedDeviceToken,
            deviceToken: trimmedDeviceToken,
            signatureToken: trimmedDeviceToken
        )
    }

    return GatewayConnectAuthSelection(token: nil, deviceToken: nil, signatureToken: nil)
}

func buildDeviceAuthPayloadV3(
    deviceID: String,
    clientID: String,
    clientMode: String,
    role: String,
    scopes: [String],
    signedAtMS: Int64,
    token: String?,
    nonce: String,
    platform: String,
    deviceFamily: String?
) -> String {
    [
        "v3",
        deviceID,
        clientID,
        clientMode,
        role,
        scopes.joined(separator: ","),
        String(signedAtMS),
        token ?? "",
        nonce,
        normalizeDeviceMetadataForAuth(platform),
        normalizeDeviceMetadataForAuth(deviceFamily),
    ]
    .joined(separator: "|")
}

struct GatewayConnectDevice {
    let id: String
    let publicKey: String
    let signature: String
    let signedAt: Int64
    let nonce: String

    var jsonObject: [String: Any] {
        [
            "id": id,
            "publicKey": publicKey,
            "signature": signature,
            "signedAt": signedAt,
            "nonce": nonce,
        ]
    }
}

struct GatewayDeviceIdentity {
    let deviceID: String
    private let privateKey: Curve25519.Signing.PrivateKey

    init(privateKey: Curve25519.Signing.PrivateKey) throws {
        self.privateKey = privateKey
        self.deviceID = sha256Hex(privateKey.publicKey.rawRepresentation)
    }

    static func loadOrCreate(from store: GatewayCredentialStore) throws -> GatewayDeviceIdentity {
        if let record = store.deviceIdentity(),
           let rawPrivateKey = Data(base64Encoded: record.privateKeyBase64),
           let privateKey = try? Curve25519.Signing.PrivateKey(rawRepresentation: rawPrivateKey) {
            let identity = try GatewayDeviceIdentity(privateKey: privateKey)
            if identity.deviceID != record.deviceID {
                store.setDeviceIdentity(identity.record)
            }
            return identity
        }

        let identity = try GatewayDeviceIdentity(privateKey: Curve25519.Signing.PrivateKey())
        store.setDeviceIdentity(identity.record)
        return identity
    }

    var publicKeyBase64URL: String {
        base64URLEncoded(privateKey.publicKey.rawRepresentation)
    }

    var record: GatewayDeviceIdentityRecord {
        GatewayDeviceIdentityRecord(
            deviceID: deviceID,
            privateKeyBase64: privateKey.rawRepresentation.base64EncodedString()
        )
    }

    func makeConnectDevice(
        nonce: String,
        role: String,
        scopes: [String],
        token: String?
    ) throws -> GatewayConnectDevice {
        let signedAtMS = Int64(Date().timeIntervalSince1970 * 1_000)
        let payload = buildDeviceAuthPayloadV3(
            deviceID: deviceID,
            clientID: GatewayClientProfile.clientID,
            clientMode: GatewayClientProfile.clientMode,
            role: role,
            scopes: scopes,
            signedAtMS: signedAtMS,
            token: token,
            nonce: nonce,
            platform: GatewayClientProfile.platform,
            deviceFamily: GatewayClientProfile.deviceFamily
        )
        let signature = try privateKey.signature(for: Data(payload.utf8))

        return GatewayConnectDevice(
            id: deviceID,
            publicKey: publicKeyBase64URL,
            signature: base64URLEncoded(signature),
            signedAt: signedAtMS,
            nonce: nonce
        )
    }
}

private func trimmedOrNil(_ value: String?) -> String? {
    let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    return trimmed.isEmpty ? nil : trimmed
}

private func normalizeDeviceMetadataForAuth(_ value: String?) -> String {
    trimmedOrNil(value)?.lowercased() ?? ""
}

private func base64URLEncoded(_ data: Data) -> String {
    data.base64EncodedString()
        .replacingOccurrences(of: "+", with: "-")
        .replacingOccurrences(of: "/", with: "_")
        .replacingOccurrences(of: "=", with: "")
}

private func sha256Hex(_ data: Data) -> String {
    SHA256.hash(data: data)
        .map { String(format: "%02x", $0) }
        .joined()
}
