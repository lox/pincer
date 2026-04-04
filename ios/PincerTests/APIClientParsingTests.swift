import XCTest
@testable import Pincer

final class APIClientParsingTests: XCTestCase {
    func testExtractConnectChallengeNonceAcceptsEventFrameType() {
        let nonce = extractConnectChallengeNonce(
            from: [
                "type": "event",
                "event": "connect.challenge",
                "payload": [
                    "nonce": "nonce-123",
                    "ts": 1_775_254_277_116,
                ],
            ]
        )

        XCTAssertEqual(nonce, "nonce-123")
    }

    func testExtractConnectChallengeNonceRejectsBlankNonce() {
        let nonce = extractConnectChallengeNonce(
            from: [
                "type": "event",
                "event": "connect.challenge",
                "payload": [
                    "nonce": "   ",
                ],
            ]
        )

        XCTAssertNil(nonce)
    }
}
