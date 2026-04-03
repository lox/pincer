import XCTest
@testable import Pincer

final class AppConfigTests: XCTestCase {
    func testBaseURLStringUsesEnvironmentOverride() {
        let defaultsKey = AppConfig.baseURLDefaultsKey
        let envKey = AppConfig.baseURLEnvironmentKey

        let defaults = UserDefaults.standard
        let originalDefaultsValue = defaults.string(forKey: defaultsKey)
        defer {
            if let value = originalDefaultsValue {
                defaults.set(value, forKey: defaultsKey)
            } else {
                defaults.removeObject(forKey: defaultsKey)
            }
        }

        let originalEnvValue = ProcessInfo.processInfo.environment[envKey]
        defer {
            if let value = originalEnvValue {
                setenv(envKey, value, 1)
            } else {
                unsetenv(envKey)
            }
        }

        defaults.set("ws://defaults.example:18789", forKey: defaultsKey)
        setenv(envKey, "wss://env.example:443", 1)

        XCTAssertEqual(AppConfig.baseURLString, "wss://env.example:443")
    }

    func testParseBaseURLAcceptsWebSocketSchemes() {
        XCTAssertEqual(AppConfig.parseBaseURL("ws://127.0.0.1:18789")?.absoluteString, "ws://127.0.0.1:18789")
        XCTAssertEqual(AppConfig.parseBaseURL("wss://gateway.example")?.absoluteString, "wss://gateway.example")
    }

    func testPrimarySessionFallsBackToMain() {
        let defaults = UserDefaults.standard
        let key = AppConfig.primarySessionDefaultsKey
        let original = defaults.string(forKey: key)
        defer {
            if let original {
                defaults.set(original, forKey: key)
            } else {
                defaults.removeObject(forKey: key)
            }
        }

        defaults.removeObject(forKey: key)
        XCTAssertEqual(AppConfig.primarySessionKey, "main")
    }
}
