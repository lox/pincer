import Foundation

enum AppConfig {
    static let tokenDefaultsKey = "PINCER_BEARER_TOKEN"

    // Update baseURL for your local environment.
    static let baseURL = URL(string: "http://127.0.0.1:8080")!
    static var bearerToken: String {
        UserDefaults.standard.string(forKey: tokenDefaultsKey) ?? ""
    }
}
