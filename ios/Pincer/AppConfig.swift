import Foundation
import Security

protocol DefaultsStoring {
    func string(forKey defaultName: String) -> String?
    func set(_ value: Any?, forKey defaultName: String)
    func removeObject(forKey defaultName: String)
}

extension UserDefaults: DefaultsStoring {}

protocol SecureStringStoring {
    func string(forKey key: String) -> String?
    func setString(_ value: String, forKey key: String)
    func removeValue(forKey key: String)
}

struct GatewayDeviceIdentityRecord: Codable, Equatable {
    let deviceID: String
    let privateKeyBase64: String
}

struct GatewayDeviceTokenRecord: Codable, Equatable {
    let deviceID: String
    let role: String
    let token: String
    let scopes: [String]
    let updatedAtMS: Int64
}

struct GatewayCredentialStore {
    let defaults: DefaultsStoring
    let secureStore: SecureStringStoring
    let tokenKey: String
    let deviceIdentityKey: String
    let deviceTokenKeyPrefix: String

    func bearerToken() -> String {
        if let token = normalizedString(secureStore.string(forKey: tokenKey)) {
            return token
        }

        guard let legacyToken = normalizedString(defaults.string(forKey: tokenKey)) else {
            return ""
        }

        secureStore.setString(legacyToken, forKey: tokenKey)
        defaults.removeObject(forKey: tokenKey)
        return legacyToken
    }

    func setBearerToken(_ token: String) {
        defaults.removeObject(forKey: tokenKey)

        guard let normalizedToken = normalizedString(token) else {
            secureStore.removeValue(forKey: tokenKey)
            return
        }

        secureStore.setString(normalizedToken, forKey: tokenKey)
    }

    func deviceIdentity() -> GatewayDeviceIdentityRecord? {
        loadCodable(forKey: deviceIdentityKey, as: GatewayDeviceIdentityRecord.self)
    }

    func setDeviceIdentity(_ identity: GatewayDeviceIdentityRecord?) {
        storeCodable(identity, forKey: deviceIdentityKey)
    }

    func deviceToken(deviceID: String, role: String) -> GatewayDeviceTokenRecord? {
        guard let record = loadCodable(
            forKey: deviceTokenStorageKey(for: role),
            as: GatewayDeviceTokenRecord.self
        ) else {
            return nil
        }

        guard record.deviceID == deviceID else {
            return nil
        }

        return record
    }

    func setDeviceToken(_ record: GatewayDeviceTokenRecord) {
        storeCodable(record, forKey: deviceTokenStorageKey(for: record.role))
    }

    func clearDeviceToken(role: String) {
        secureStore.removeValue(forKey: deviceTokenStorageKey(for: role))
    }

    private func normalizedString(_ value: String?) -> String? {
        let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? nil : trimmed
    }

    private func normalizedRole(_ role: String) -> String {
        normalizedString(role)?.lowercased() ?? "operator"
    }

    private func deviceTokenStorageKey(for role: String) -> String {
        "\(deviceTokenKeyPrefix)_\(normalizedRole(role).uppercased())"
    }

    private func loadCodable<T: Decodable>(forKey key: String, as type: T.Type) -> T? {
        guard let raw = secureStore.string(forKey: key),
              let data = raw.data(using: .utf8) else {
            return nil
        }

        return try? JSONDecoder().decode(type, from: data)
    }

    private func storeCodable<T: Encodable>(_ value: T?, forKey key: String) {
        guard let value else {
            secureStore.removeValue(forKey: key)
            return
        }

        guard let data = try? JSONEncoder().encode(value),
              let raw = String(data: data, encoding: .utf8) else {
            return
        }

        secureStore.setString(raw, forKey: key)
    }
}

final class KeychainStringStore: SecureStringStoring {
    private let service: String

    init(service: String = "com.lox.pincer.gateway") {
        self.service = service
    }

    func string(forKey key: String) -> String? {
        var query = baseQuery(forKey: key)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne

        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        guard status == errSecSuccess,
              let data = result as? Data,
              let value = String(data: data, encoding: .utf8) else {
            return nil
        }
        return value
    }

    func setString(_ value: String, forKey key: String) {
        let data = Data(value.utf8)
        let query = baseQuery(forKey: key)
        let attributes: [String: Any] = [
            kSecValueData as String: data,
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
        ]

        let updateStatus = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if updateStatus == errSecSuccess {
            return
        }

        guard updateStatus == errSecItemNotFound else {
            return
        }

        var addQuery = query
        addQuery[kSecValueData as String] = data
        addQuery[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        SecItemAdd(addQuery as CFDictionary, nil)
    }

    func removeValue(forKey key: String) {
        SecItemDelete(baseQuery(forKey: key) as CFDictionary)
    }

    private func baseQuery(forKey key: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
        ]
    }
}

enum AppConfig {
    static let tokenDefaultsKey = "OPENCLAW_GATEWAY_TOKEN"
    static let baseURLDefaultsKey = "OPENCLAW_GATEWAY_URL"
    static let baseURLEnvironmentKey = "OPENCLAW_IOS_GATEWAY_URL"
    static let uiTestModeEnvironmentKey = "OPENCLAW_UI_TEST_MODE"
    static let primarySessionDefaultsKey = "OPENCLAW_PRIMARY_SESSION_KEY"
    static let deviceIdentityDefaultsKey = "OPENCLAW_GATEWAY_DEVICE_IDENTITY"
    static let deviceTokenDefaultsKeyPrefix = "OPENCLAW_GATEWAY_DEVICE_TOKEN"
    static let defaultBaseURL = URL(string: "ws://127.0.0.1:18789")!
    static let defaultPrimarySessionKey = "main"
    static var credentialStore = GatewayCredentialStore(
        defaults: UserDefaults.standard,
        secureStore: KeychainStringStore(),
        tokenKey: tokenDefaultsKey,
        deviceIdentityKey: deviceIdentityDefaultsKey,
        deviceTokenKeyPrefix: deviceTokenDefaultsKeyPrefix
    )

    static var baseURL: URL {
        URL(string: baseURLString) ?? defaultBaseURL
    }

    static var baseURLString: String {
        if let raw = ProcessInfo.processInfo.environment[baseURLEnvironmentKey],
           let parsed = parseBaseURL(raw) {
            return parsed.absoluteString
        }

        if let raw = ProcessInfo.processInfo.environment[baseURLDefaultsKey],
           let parsed = parseBaseURL(raw) {
            return parsed.absoluteString
        }

        if let raw = UserDefaults.standard.string(forKey: baseURLDefaultsKey),
           let parsed = parseBaseURL(raw) {
            return parsed.absoluteString
        }

        return defaultBaseURL.absoluteString
    }

    static var bearerToken: String {
        credentialStore.bearerToken()
    }

    static var isUITestMode: Bool {
        let raw = ProcessInfo.processInfo.environment[uiTestModeEnvironmentKey]?
            .trimmingCharacters(in: .whitespacesAndNewlines)
            .lowercased() ?? ""
        return raw == "1" || raw == "true" || raw == "yes"
    }

    static var primarySessionKey: String {
        let stored = UserDefaults.standard.string(forKey: primarySessionDefaultsKey)?
            .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return stored.isEmpty ? defaultPrimarySessionKey : stored
    }

    static var controlUIURL: URL? {
        guard var components = URLComponents(url: baseURL, resolvingAgainstBaseURL: false) else {
            return nil
        }

        switch components.scheme?.lowercased() {
        case "ws":
            components.scheme = "http"
        case "wss":
            components.scheme = "https"
        default:
            break
        }

        return components.url
    }

    static func parseBaseURL(_ raw: String) -> URL? {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }

        let candidate = trimmed.contains("://") ? trimmed : "ws://\(trimmed)"
        guard let url = URL(string: candidate),
              let scheme = url.scheme?.lowercased(),
              ["http", "https", "ws", "wss"].contains(scheme),
              url.host != nil else {
            return nil
        }

        return url
    }

    static func setBaseURL(_ url: URL) {
        UserDefaults.standard.set(url.absoluteString, forKey: baseURLDefaultsKey)
    }

    static func setBearerToken(_ token: String) {
        credentialStore.setBearerToken(token)
    }

    static func setPrimarySessionKey(_ sessionKey: String) {
        let trimmed = sessionKey.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty {
            UserDefaults.standard.removeObject(forKey: primarySessionDefaultsKey)
            return
        }
        UserDefaults.standard.set(trimmed, forKey: primarySessionDefaultsKey)
    }
}
