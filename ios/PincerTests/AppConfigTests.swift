import XCTest
@testable import Pincer

final class AppConfigTests: XCTestCase {
    func testBaseURLStringUsesEnvironmentOverride() {
        let defaultsKey = AppConfig.baseURLDefaultsKey
        let envKey = "PINCER_IOS_BASE_URL"

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

        defaults.set("http://defaults.example:8080", forKey: defaultsKey)
        setenv(envKey, "https://env.example:8443", 1)

        XCTAssertEqual(AppConfig.baseURLString, "https://env.example:8443")
    }
}
