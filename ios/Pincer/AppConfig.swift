import Foundation

enum AppConfig {
    static let tokenDefaultsKey = "PINCER_BEARER_TOKEN"
    static let baseURLDefaultsKey = "PINCER_BASE_URL"
    static let defaultBaseURL = URL(string: "http://127.0.0.1:8080")!

    static var baseURL: URL {
        URL(string: baseURLString) ?? defaultBaseURL
    }

    static var baseURLString: String {
        if let raw = UserDefaults.standard.string(forKey: baseURLDefaultsKey),
           let parsed = parseBaseURL(raw) {
            return parsed.absoluteString
        }
        return defaultBaseURL.absoluteString
    }

    static var bearerToken: String {
        UserDefaults.standard.string(forKey: tokenDefaultsKey) ?? ""
    }

    static func parseBaseURL(_ raw: String) -> URL? {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }

        let candidate = trimmed.contains("://") ? trimmed : "http://\(trimmed)"
        guard let url = URL(string: candidate),
              let scheme = url.scheme?.lowercased(),
              (scheme == "http" || scheme == "https"),
              url.host != nil else {
            return nil
        }

        return url
    }

    static func setBaseURL(_ url: URL) {
        UserDefaults.standard.set(url.absoluteString, forKey: baseURLDefaultsKey)
    }
}
