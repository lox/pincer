import XCTest
@testable import Pincer

final class APIClientGatewayMappingTests: XCTestCase {
    func testMapGatewaySessionsPayloadKeepsPrimarySessionNamedMain() throws {
        let payload: [String: Any] = [
            "sessions": [
                [
                    "key": "agent:main:later",
                    "displayName": "Later",
                    "updatedAt": 1_717_171_718_000,
                    "startedAt": 1_717_171_700_000,
                ],
                [
                    "key": "agent:main:main",
                    "displayName": "Main",
                    "derivedTitle": "Debug OpenClaw pairing",
                    "updatedAt": 1_717_171_719_000,
                    "startedAt": 1_717_171_699_000,
                ],
            ],
        ]

        let threads = try mapGatewaySessionsPayload(payload)

        XCTAssertEqual(threads.map(\.threadID), ["agent:main:main", "agent:main:later"])
        XCTAssertEqual(threads.map(\.title), ["Main", "Later"])
        XCTAssertEqual(threads.first?.createdAt, "2024-05-31T16:08:19Z")
        XCTAssertEqual(threads.first?.updatedAt, "2024-05-31T16:08:39Z")
    }

    func testMapGatewaySessionsPayloadKeepsPrimarySessionNamedMainWhenGatewayUsesHeartbeatLabel() throws {
        let payload: [String: Any] = [
            "sessions": [
                [
                    "key": "agent:main:main",
                    "displayName": "heartbeat",
                    "label": "heartbeat",
                    "updatedAt": 1_775_255_508_455,
                    "startedAt": 1_775_251_983_010,
                ],
            ],
        ]

        let threads = try mapGatewaySessionsPayload(payload)

        XCTAssertEqual(threads.map(\.title), ["Main"])
    }

    func testMapGatewaySessionsPayloadStripsControlUIMetadataFromDerivedTitle() throws {
        let payload: [String: Any] = [
            "sessions": [
                [
                    "key": "agent:main:control-ui",
                    "derivedTitle": """
                    Sender (untrusted metadata):
                    ```json
                    {
                      "label": "openclaw-control-ui",
                      "id": "openclaw-control-ui"
                    }
                    ```

                    [Sat 2026-04-04 08:33 GMT+11] What can you do?
                    """,
                    "updatedAt": 1_717_171_719_000,
                    "startedAt": 1_717_171_699_000,
                ],
            ],
        ]

        let threads = try mapGatewaySessionsPayload(payload)

        XCTAssertEqual(threads.map(\.title), ["What can you do?"])
    }

    func testMapGatewayChatHistoryPayloadExtractsTextAndMetadata() throws {
        let payload: [String: Any] = [
            "messages": [
                [
                    "role": "user",
                    "content": "hello",
                    "timestamp": 1_717_171_717_000,
                    "__openclaw": [
                        "id": "msg-user",
                        "seq": 1,
                    ],
                ],
                [
                    "role": "assistant",
                    "content": [
                        [
                            "type": "text",
                            "text": "Hi there",
                        ],
                        [
                            "type": "text",
                            "text": "How can I help?",
                        ],
                    ],
                    "timestamp": 1_717_171_718_000,
                    "__openclaw": [
                        "id": "msg-assistant",
                        "seq": 2,
                    ],
                ],
                [
                    "role": "system",
                    "content": [
                        [
                            "type": "image",
                            "mimeType": "image/png",
                        ],
                    ],
                    "timestamp": 1_717_171_719_000,
                ],
            ],
        ]

        let snapshot = try mapGatewayChatHistoryPayload(payload, threadID: "main")

        XCTAssertEqual(snapshot.lastSequence, 2)
        XCTAssertEqual(snapshot.messages.map(\.messageID), ["msg-user", "msg-assistant", "msg_3"])
        XCTAssertEqual(snapshot.messages.map(\.role), ["user", "assistant", "system"])
        XCTAssertEqual(snapshot.messages.map(\.content), [
            "hello",
            "Hi there\n\nHow can I help?",
            "[Attachment]",
        ])
        XCTAssertEqual(snapshot.messages[0].threadID, "main")
        XCTAssertEqual(snapshot.messages[0].createdAt, "2024-05-31T16:08:37Z")
    }

    func testMapGatewayChatHistoryPayloadStripsControlUIUntrustedMetadataWrapper() throws {
        let payload: [String: Any] = [
            "messages": [
                [
                    "role": "user",
                    "content": """
                    Sender (untrusted metadata):
                    ```json
                    {
                      "label": "Pincer (openclaw-ios)",
                      "id": "openclaw-ios"
                    }
                    ```

                    [Sat 2026-04-04 09:55 GMT+11] Say hello world
                    """,
                    "timestamp": 1_775_256_000_000,
                ],
            ],
        ]

        let snapshot = try mapGatewayChatHistoryPayload(payload, threadID: "main")

        XCTAssertEqual(snapshot.messages.map(\.content), ["Say hello world"])
    }

    func testMapGatewayChatHistoryPayloadStripsChannelPrefixForNonAssistantMessages() throws {
        let payload: [String: Any] = [
            "messages": [
                [
                    "role": "system",
                    "content": "[WebChat 2026-04-04 10:18] Session reset",
                    "timestamp": 1_775_256_001_000,
                ],
            ],
        ]

        let snapshot = try mapGatewayChatHistoryPayload(payload, threadID: "main")

        XCTAssertEqual(snapshot.messages.map(\.content), ["Session reset"])
    }

    func testMapGatewayChatHistoryPayloadFiltersAssistantNoReplyMessages() throws {
        let payload: [String: Any] = [
            "messages": [
                [
                    "role": "user",
                    "content": "Ping",
                    "timestamp": 1_775_256_000_000,
                ],
                [
                    "role": "assistant",
                    "content": "NO_REPLY",
                    "timestamp": 1_775_256_001_000,
                ],
                [
                    "role": "assistant",
                    "content": "hello world",
                    "timestamp": 1_775_256_002_000,
                ],
            ],
        ]

        let snapshot = try mapGatewayChatHistoryPayload(payload, threadID: "main")

        XCTAssertEqual(snapshot.messages.map(\.role), ["user", "assistant"])
        XCTAssertEqual(snapshot.messages.map(\.content), ["Ping", "hello world"])
    }

    func testMapGatewayChatHistoryPayloadFiltersToolExecutionArtifactsFromTranscript() throws {
        let payload: [String: Any] = [
            "messages": [
                [
                    "role": "user",
                    "content": """
                    Sender (untrusted metadata):
                    ```json
                    {
                      "label": "openclaw-control-ui",
                      "id": "openclaw-control-ui"
                    }
                    ```

                    [Sat 2026-04-04 08:33 GMT+11] What can you do?
                    """,
                    "timestamp": 1_775_251_983_010,
                ],
                [
                    "role": "assistant",
                    "content": [
                        [
                            "type": "toolCall",
                            "id": "call-1",
                            "name": "read",
                            "arguments": [
                                "path": "/Users/lachlan/.openclaw/workspace/SOUL.md",
                            ],
                        ],
                        [
                            "type": "toolCall",
                            "id": "call-2",
                            "name": "read",
                            "arguments": [
                                "path": "/Users/lachlan/.openclaw/workspace/USER.md",
                            ],
                        ],
                    ],
                    "timestamp": 1_775_251_989_727,
                ],
                [
                    "role": "toolResult",
                    "content": [
                        [
                            "type": "text",
                            "text": "# SOUL.md - Who You Are",
                        ],
                    ],
                    "timestamp": 1_775_251_989_722,
                ],
                [
                    "role": "assistant",
                    "content": [
                        [
                            "type": "text",
                            "text": "[[reply_to_current]] I can do a fair bit.",
                        ],
                    ],
                    "timestamp": 1_775_251_998_216,
                ],
            ],
        ]

        let snapshot = try mapGatewayChatHistoryPayload(payload, threadID: "main")

        XCTAssertEqual(snapshot.messages.map(\.role), ["user", "assistant"])
        XCTAssertEqual(snapshot.messages.map(\.content), [
            "What can you do?",
            "I can do a fair bit.",
        ])
    }

    func testMapGatewayChatHistoryPayloadRejectsMissingMessagesArray() {
        XCTAssertThrowsError(try mapGatewayChatHistoryPayload([:], threadID: "main"))
    }
}
