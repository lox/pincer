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

    func testParseGatewayConnectionEventBuildsExecApprovalRequested() {
        let event = parseGatewayConnectionEvent(
            from: [
                "type": "event",
                "event": "exec.approval.requested",
                "payload": [
                    "id": "approval-123",
                    "createdAtMs": 1_775_259_600_000,
                    "expiresAtMs": 1_775_259_660_000,
                    "request": [
                        "command": "pwd",
                        "commandPreview": "pwd",
                        "security": "allowlist",
                        "allowedDecisions": ["allow-once", "deny"],
                        "sessionKey": "agent:main:main",
                    ],
                ],
            ]
        )

        XCTAssertEqual(
            event,
            .approvalRequested(
                GatewayPendingApproval(
                    kind: .exec,
                    id: "approval-123",
                    tool: "Exec approval",
                    summary: "pwd",
                    commandPreview: "pwd",
                    riskClass: "allowlist",
                    allowedDecisions: ["allow-once", "deny"],
                    sessionKey: "agent:main:main",
                    createdAtMS: 1_775_259_600_000,
                    expiresAtMS: 1_775_259_660_000
                )
            )
        )
    }
}
