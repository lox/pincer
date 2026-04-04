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

    func testBearerTokenMigratesFromDefaultsIntoSecureStore() {
        let defaults = InMemoryDefaultsStore()
        defaults.storage[AppConfig.tokenDefaultsKey] = "shared-token"
        let secureStore = InMemorySecureStore()
        let store = GatewayCredentialStore(
            defaults: defaults,
            secureStore: secureStore,
            tokenKey: AppConfig.tokenDefaultsKey,
            deviceIdentityKey: AppConfig.deviceIdentityDefaultsKey,
            deviceTokenKeyPrefix: AppConfig.deviceTokenDefaultsKeyPrefix
        )

        XCTAssertEqual(store.bearerToken(), "shared-token")
        XCTAssertNil(defaults.storage[AppConfig.tokenDefaultsKey])
        XCTAssertEqual(secureStore.storage[AppConfig.tokenDefaultsKey], "shared-token")
    }

    func testSetBearerTokenRemovesDefaultsAndSecureStoreWhenBlank() {
        let defaults = InMemoryDefaultsStore()
        defaults.storage[AppConfig.tokenDefaultsKey] = "stale"
        let secureStore = InMemorySecureStore()
        secureStore.storage[AppConfig.tokenDefaultsKey] = "secret"
        let store = GatewayCredentialStore(
            defaults: defaults,
            secureStore: secureStore,
            tokenKey: AppConfig.tokenDefaultsKey,
            deviceIdentityKey: AppConfig.deviceIdentityDefaultsKey,
            deviceTokenKeyPrefix: AppConfig.deviceTokenDefaultsKeyPrefix
        )

        store.setBearerToken("   ")

        XCTAssertNil(defaults.storage[AppConfig.tokenDefaultsKey])
        XCTAssertNil(secureStore.storage[AppConfig.tokenDefaultsKey])
    }

    func testSessionSwitcherStaysHiddenWhenOnlyPrimarySessionExists() {
        let threads = [
            ThreadSummary(
                threadID: "agent:main:main",
                title: "Main",
                createdAt: "",
                updatedAt: "",
                messageCount: 1
            ),
        ]

        XCTAssertFalse(shouldShowSessionSwitcher(for: threads))
    }

    func testSessionSwitcherAppearsWhenSecondarySessionExists() {
        let threads = [
            ThreadSummary(
                threadID: "agent:main:main",
                title: "Main",
                createdAt: "",
                updatedAt: "",
                messageCount: 1
            ),
            ThreadSummary(
                threadID: "agent:main:ops",
                title: "Ops",
                createdAt: "",
                updatedAt: "",
                messageCount: 4
            ),
        ]

        XCTAssertTrue(shouldShowSessionSwitcher(for: threads))
    }
}
