import Foundation

enum AppConfig {
    static let tokenDefaultsKey = "PINCER_BEARER_TOKEN"
    static let baseURLDefaultsKey = "PINCER_BASE_URL"

    static var baseURL: URL {
        if let raw = UserDefaults.standard.string(forKey: baseURLDefaultsKey),
           let url = URL(string: raw) {
            return url
        }
        return URL(string: "http://127.0.0.1:8080")!
    }

    static var bearerToken: String {
        UserDefaults.standard.string(forKey: tokenDefaultsKey) ?? ""
    }
}
