import Foundation

enum AppConfig {
    static let tokenDefaultsKey = "OPENCLAW_GATEWAY_TOKEN"
    static let baseURLDefaultsKey = "OPENCLAW_GATEWAY_URL"
    static let baseURLEnvironmentKey = "OPENCLAW_IOS_GATEWAY_URL"
    static let primarySessionDefaultsKey = "OPENCLAW_PRIMARY_SESSION_KEY"
    static let defaultBaseURL = URL(string: "ws://127.0.0.1:18789")!
    static let defaultPrimarySessionKey = "main"

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
        UserDefaults.standard.string(forKey: tokenDefaultsKey) ?? ""
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
        let trimmed = token.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty {
            UserDefaults.standard.removeObject(forKey: tokenDefaultsKey)
            return
        }
        UserDefaults.standard.set(trimmed, forKey: tokenDefaultsKey)
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
