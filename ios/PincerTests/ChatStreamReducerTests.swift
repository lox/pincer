import XCTest
@testable import Pincer

final class ChatStreamReducerTests: XCTestCase {
    func testParseGatewayConnectionEventBuildsChatDelta() {
        let frame: [String: Any] = [
            "type": "event",
            "event": "chat",
            "seq": 7,
            "payload": [
                "runId": "run-123",
                "sessionKey": "agent:main:main",
                "state": "delta",
                "message": [
                    "role": "assistant",
                    "content": [
                        [
                            "type": "thinking",
                            "thinking": "Planning",
                        ],
                        [
                            "type": "text",
                            "text": "Hello there",
                        ],
                    ],
                ],
            ],
        ]

        let event = parseGatewayConnectionEvent(from: frame)

        XCTAssertEqual(
            event,
            .chat(
                GatewayChatEvent(
                    runID: "run-123",
                    sessionKey: "agent:main:main",
                    sequence: 7,
                    state: .delta,
                    message: GatewayChatMessage(
                        role: "assistant",
                        text: "Hello there",
                        thinkingText: "Planning",
                        command: false,
                        hasAttachments: false
                    ),
                    errorMessage: nil,
                    stopReason: nil
                )
            )
        )
    }

    func testApplyGatewayChatEventsBuildsDraftAndFinalMessage() {
        var state = ChatStreamState(
            messages: [
                Message(messageID: "msg-1", threadID: "agent:main:main", role: "user", content: "Ping", createdAt: "2026-04-04T00:00:00Z"),
            ]
        )

        applyGatewayConnectionEvent(
            .chat(
                GatewayChatEvent(
                    runID: "run-123",
                    sessionKey: "agent:main:main",
                    sequence: 1,
                    state: .delta,
                    message: GatewayChatMessage(
                        role: "assistant",
                        text: "Hel",
                        thinkingText: "",
                        command: false,
                        hasAttachments: false
                    ),
                    errorMessage: nil,
                    stopReason: nil
                )
            ),
            to: &state,
            currentThreadID: "agent:main:main",
            now: { "2026-04-04T00:00:10Z" }
        )

        XCTAssertEqual(state.activeRunID, "run-123")
        XCTAssertEqual(state.assistantDraftText, "Hel")
        XCTAssertFalse(state.needsSnapshotRefresh)

        applyGatewayConnectionEvent(
            .chat(
                GatewayChatEvent(
                    runID: "run-123",
                    sessionKey: "agent:main:main",
                    sequence: 2,
                    state: .final,
                    message: GatewayChatMessage(
                        role: "assistant",
                        text: "Hello there",
                        thinkingText: "",
                        command: false,
                        hasAttachments: false
                    ),
                    errorMessage: nil,
                    stopReason: "end_turn"
                )
            ),
            to: &state,
            currentThreadID: "agent:main:main",
            now: { "2026-04-04T00:00:11Z" }
        )

        XCTAssertNil(state.activeRunID)
        XCTAssertEqual(state.assistantDraftText, "")
        XCTAssertTrue(state.needsSnapshotRefresh)
        XCTAssertEqual(state.messages.last?.content, "Hello there")
        XCTAssertEqual(state.messages.last?.role, "assistant")
    }

    func testApplyGatewayChatEventsSuppressesSilentAssistantNoReplyMessages() {
        var state = ChatStreamState()

        applyGatewayConnectionEvent(
            .chat(
                GatewayChatEvent(
                    runID: "run-123",
                    sessionKey: "agent:main:main",
                    sequence: 1,
                    state: .delta,
                    message: GatewayChatMessage(
                        role: "assistant",
                        text: "NO_REPLY",
                        thinkingText: "",
                        command: false,
                        hasAttachments: false
                    ),
                    errorMessage: nil,
                    stopReason: nil
                )
            ),
            to: &state,
            currentThreadID: "agent:main:main"
        )

        XCTAssertEqual(state.assistantDraftText, "")

        applyGatewayConnectionEvent(
            .chat(
                GatewayChatEvent(
                    runID: "run-123",
                    sessionKey: "agent:main:main",
                    sequence: 2,
                    state: .final,
                    message: GatewayChatMessage(
                        role: "assistant",
                        text: "NO_REPLY",
                        thinkingText: "",
                        command: false,
                        hasAttachments: false
                    ),
                    errorMessage: nil,
                    stopReason: "end_turn"
                )
            ),
            to: &state,
            currentThreadID: "agent:main:main",
            now: { "2026-04-04T00:00:11Z" }
        )

        XCTAssertEqual(state.messages.count, 0)
    }

    func testApplyGatewayChatEventsStripTimestampPrefixFromAssistantOutput() {
        var state = ChatStreamState()

        applyGatewayConnectionEvent(
            .chat(
                GatewayChatEvent(
                    runID: "run-123",
                    sessionKey: "agent:main:main",
                    sequence: 2,
                    state: .final,
                    message: GatewayChatMessage(
                        role: "assistant",
                        text: "[Sat 2026-04-04 09:55 GMT+11] Hello there",
                        thinkingText: "",
                        command: false,
                        hasAttachments: false
                    ),
                    errorMessage: nil,
                    stopReason: "end_turn"
                )
            ),
            to: &state,
            currentThreadID: "agent:main:main",
            now: { "2026-04-04T00:00:11Z" }
        )

        XCTAssertEqual(state.messages.last?.content, "Hello there")
    }

    func testApplyGatewayAgentToolEventsTracksLiveToolCard() {
        var state = ChatStreamState(activeRunID: "run-123")

        applyGatewayConnectionEvent(
            .agent(
                GatewayAgentEvent(
                    runID: "run-123",
                    sessionKey: "agent:main:main",
                    sequence: 3,
                    stream: "tool",
                    tool: GatewayToolEvent(
                        phase: "start",
                        toolCallID: "tool-1",
                        name: "exec",
                        argsPreview: "{\"cmd\":\"pwd\"}",
                        outputPreview: nil,
                        isError: false
                    ),
                    lifecyclePhase: nil
                )
            ),
            to: &state,
            currentThreadID: "agent:main:main"
        )

        XCTAssertEqual(state.latestToolCalls.count, 1)
        XCTAssertEqual(state.latestToolCalls[0].toolName, "exec")
        XCTAssertEqual(state.latestToolCalls[0].state, .running)
        XCTAssertEqual(state.latestToolCalls[0].argsPreview, "{\"cmd\":\"pwd\"}")

        applyGatewayConnectionEvent(
            .agent(
                GatewayAgentEvent(
                    runID: "run-123",
                    sessionKey: "agent:main:main",
                    sequence: 4,
                    stream: "tool",
                    tool: GatewayToolEvent(
                        phase: "result",
                        toolCallID: "tool-1",
                        name: "exec",
                        argsPreview: nil,
                        outputPreview: "/tmp/workspace",
                        isError: false
                    ),
                    lifecyclePhase: nil
                )
            ),
            to: &state,
            currentThreadID: "agent:main:main"
        )

        XCTAssertEqual(state.latestToolCalls[0].state, .succeeded)
        XCTAssertEqual(state.latestToolCalls[0].executions.first?.stdout, "/tmp/workspace")
        XCTAssertEqual(state.latestToolCalls[0].executions.first?.isStreaming, false)
    }

    func testGapMarksSnapshotRefresh() {
        var state = ChatStreamState()

        applyGatewayConnectionEvent(
            .gap(
                GatewayGapEvent(
                    expected: 9,
                    received: 11,
                    stateVersion: GatewayStateVersion(presence: 4, health: 2)
                )
            ),
            to: &state,
            currentThreadID: "agent:main:main"
        )

        XCTAssertTrue(state.needsSnapshotRefresh)
        XCTAssertEqual(state.connectionNotice, "Gateway event gap detected. Refreshing chat…")
    }

    func testApplyGatewayChatEventsSuppressesHeartbeatMaintenanceRun() {
        var state = ChatStreamState()

        applyGatewayConnectionEvent(
            .chat(
                GatewayChatEvent(
                    runID: "heartbeat-run",
                    sessionKey: "agent:main:main",
                    sequence: 10,
                    state: .final,
                    message: GatewayChatMessage(
                        role: "user",
                        text: heartbeatMaintenancePrompt,
                        thinkingText: "",
                        command: false,
                        hasAttachments: false
                    ),
                    errorMessage: nil,
                    stopReason: nil
                )
            ),
            to: &state,
            currentThreadID: "agent:main:main",
            now: { "2026-04-04T04:21:00Z" }
        )

        applyGatewayConnectionEvent(
            .agent(
                GatewayAgentEvent(
                    runID: "heartbeat-run",
                    sessionKey: "agent:main:main",
                    sequence: 11,
                    stream: "tool",
                    tool: GatewayToolEvent(
                        phase: "start",
                        toolCallID: "heartbeat-tool",
                        name: "read",
                        argsPreview: "{\"path\":\"/Users/lachlan/.openclaw/workspace/HEARTBEAT.md\"}",
                        outputPreview: nil,
                        isError: false
                    ),
                    lifecyclePhase: nil
                )
            ),
            to: &state,
            currentThreadID: "agent:main:main"
        )

        applyGatewayConnectionEvent(
            .chat(
                GatewayChatEvent(
                    runID: "heartbeat-run",
                    sessionKey: "agent:main:main",
                    sequence: 12,
                    state: .final,
                    message: GatewayChatMessage(
                        role: "assistant",
                        text: "HEARTBEAT_OK",
                        thinkingText: "",
                        command: false,
                        hasAttachments: false
                    ),
                    errorMessage: nil,
                    stopReason: "end_turn"
                )
            ),
            to: &state,
            currentThreadID: "agent:main:main",
            now: { "2026-04-04T04:21:01Z" }
        )

        XCTAssertTrue(state.messages.isEmpty)
        XCTAssertTrue(state.timelineItems.isEmpty)
        XCTAssertTrue(state.latestToolCalls.isEmpty)
        XCTAssertNil(state.activeRunID)
        XCTAssertFalse(state.needsSnapshotRefresh)

        applyGatewayConnectionEvent(
            .chat(
                GatewayChatEvent(
                    runID: "normal-run",
                    sessionKey: "agent:main:main",
                    sequence: 13,
                    state: .final,
                    message: GatewayChatMessage(
                        role: "assistant",
                        text: "Visible reply",
                        thinkingText: "",
                        command: false,
                        hasAttachments: false
                    ),
                    errorMessage: nil,
                    stopReason: "end_turn"
                )
            ),
            to: &state,
            currentThreadID: "agent:main:main",
            now: { "2026-04-04T04:21:02Z" }
        )

        XCTAssertEqual(state.messages.map(\.content), ["Visible reply"])
    }
}

private let heartbeatMaintenancePrompt = """
Read HEARTBEAT.md if it exists (workspace context). Follow it strictly. Do not infer or repeat old tasks from prior chats. If nothing needs attention, reply HEARTBEAT_OK. When reading HEARTBEAT.md, use workspace file /Users/lachlan/.openclaw/workspace/HEARTBEAT.md (exact case). Do not read docs/heartbeat.md.
Current time: Saturday, April 4th, 2026 — 3:21 PM (Australia/Melbourne) / 2026-04-04 04:21 UTC
"""
