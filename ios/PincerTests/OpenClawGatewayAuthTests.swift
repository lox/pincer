import CryptoKit
import XCTest
@testable import Pincer

final class OpenClawGatewayAuthTests: XCTestCase {
    func testBuildDeviceAuthPayloadV3MatchesOpenClawFormat() {
        let payload = buildDeviceAuthPayloadV3(
            deviceID: "device-123",
            clientID: "openclaw-ios",
            clientMode: "ui",
            role: "operator",
            scopes: ["operator.read", "operator.write"],
            signedAtMS: 1_717_171_717_000,
            token: "shared-token",
            nonce: "nonce-abc",
            platform: "iOS",
            deviceFamily: "Simulator"
        )

        XCTAssertEqual(
            payload,
            "v3|device-123|openclaw-ios|ui|operator|operator.read,operator.write|1717171717000|shared-token|nonce-abc|ios|simulator"
        )
    }

    func testSelectGatewayConnectAuthPrefersGatewayToken() {
        let selection = selectGatewayConnectAuth(
            bearerToken: "gateway-token",
            storedDeviceToken: "device-token"
        )

        XCTAssertEqual(
            selection,
            GatewayConnectAuthSelection(
                token: "gateway-token",
                deviceToken: nil,
                signatureToken: "gateway-token"
            )
        )
    }

    func testSelectGatewayConnectAuthFallsBackToStoredDeviceToken() {
        let selection = selectGatewayConnectAuth(
            bearerToken: "   ",
            storedDeviceToken: "device-token"
        )

        XCTAssertEqual(
            selection,
            GatewayConnectAuthSelection(
                token: "device-token",
                deviceToken: "device-token",
                signatureToken: "device-token"
            )
        )
    }

    func testLoadOrCreateGatewayDeviceIdentityReturnsStoredIdentity() throws {
        let secureStore = InMemorySecureStore()
        let store = GatewayCredentialStore(
            defaults: InMemoryDefaultsStore(),
            secureStore: secureStore,
            tokenKey: AppConfig.tokenDefaultsKey,
            deviceIdentityKey: AppConfig.deviceIdentityDefaultsKey,
            deviceTokenKeyPrefix: AppConfig.deviceTokenDefaultsKeyPrefix
        )

        let first = try GatewayDeviceIdentity.loadOrCreate(from: store)
        let second = try GatewayDeviceIdentity.loadOrCreate(from: store)

        XCTAssertEqual(second.deviceID, first.deviceID)
        XCTAssertEqual(second.publicKeyBase64URL, first.publicKeyBase64URL)
        XCTAssertNotNil(secureStore.storage[AppConfig.deviceIdentityDefaultsKey])
    }

    func testDeviceTokenRecordIsScopedByDeviceAndRole() {
        let store = GatewayCredentialStore(
            defaults: InMemoryDefaultsStore(),
            secureStore: InMemorySecureStore(),
            tokenKey: AppConfig.tokenDefaultsKey,
            deviceIdentityKey: AppConfig.deviceIdentityDefaultsKey,
            deviceTokenKeyPrefix: AppConfig.deviceTokenDefaultsKeyPrefix
        )
        let record = GatewayDeviceTokenRecord(
            deviceID: "device-a",
            role: "operator",
            token: "token-a",
            scopes: ["operator.read"],
            updatedAtMS: 1234
        )

        store.setDeviceToken(record)

        XCTAssertEqual(store.deviceToken(deviceID: "device-a", role: "operator")?.token, "token-a")
        XCTAssertNil(store.deviceToken(deviceID: "device-b", role: "operator"))
        XCTAssertNil(store.deviceToken(deviceID: "device-a", role: "node"))
    }

    func testGatewayDeviceIdentityUsesSha256OfRawPublicKeyAsDeviceID() throws {
        let rawPrivateKey = Data(repeating: 7, count: 32)
        let privateKey = try Curve25519.Signing.PrivateKey(rawRepresentation: rawPrivateKey)
        let identity = try GatewayDeviceIdentity(privateKey: privateKey)
        let expected = SHA256.hash(data: privateKey.publicKey.rawRepresentation)
            .map { String(format: "%02x", $0) }
            .joined()

        XCTAssertEqual(identity.deviceID, expected)
    }
}
