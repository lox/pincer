import XCTest
@testable import Pincer

final class ThreadEventReducerTests: XCTestCase {
    func testTurnStartedDoesNotCountAsVisibleProgressSignal() {
        var state = ThreadEventReducerState()

        var started = Pincer_Protocol_V1_ThreadEvent()
        started.eventID = "evt_turn_started"
        started.threadID = "thr_1"
        started.turnID = "turn_1"
        started.sequence = 1
        started.turnStarted = {
            var payload = Pincer_Protocol_V1_TurnStarted()
            payload.userMessageID = "msg_user_1"
            payload.triggerType = .chatMessage
            return payload
        }()

        let effect = ThreadEventReducer.apply(started, state: &state, fallbackThreadID: "thr_1")

        XCTAssertFalse(effect.reachedTurnTerminal)
        XCTAssertFalse(effect.receivedProgressSignal)
    }

    func testAssistantTextDeltaCountsAsVisibleProgressSignal() {
        var state = ThreadEventReducerState()

        var delta = Pincer_Protocol_V1_ThreadEvent()
        delta.eventID = "evt_assistant_delta"
        delta.threadID = "thr_1"
        delta.turnID = "turn_1"
        delta.sequence = 1
        delta.assistantTextDelta = {
            var payload = Pincer_Protocol_V1_AssistantTextDelta()
            payload.segmentID = "assistant"
            payload.delta = "Hello"
            return payload
        }()

        let effect = ThreadEventReducer.apply(delta, state: &state, fallbackThreadID: "thr_1")

        XCTAssertTrue(effect.receivedProgressSignal)
        XCTAssertEqual(state.messages.count, 1)
        XCTAssertEqual(state.messages[0].role, "assistant")
        XCTAssertEqual(state.messages[0].content, "Hello")
    }

    func testAssistantDeltaAndCommitBuildsSingleAssistantMessage() {
        var state = ThreadEventReducerState()

        var delta1 = Pincer_Protocol_V1_ThreadEvent()
        delta1.eventID = "evt_delta_1"
        delta1.threadID = "thr_1"
        delta1.turnID = "turn_1"
        delta1.sequence = 1
        delta1.assistantTextDelta = {
            var payload = Pincer_Protocol_V1_AssistantTextDelta()
            payload.segmentID = "assistant"
            payload.delta = "Hello"
            return payload
        }()

        _ = ThreadEventReducer.apply(delta1, state: &state, fallbackThreadID: "thr_1")

        var delta2 = Pincer_Protocol_V1_ThreadEvent()
        delta2.eventID = "evt_delta_2"
        delta2.threadID = "thr_1"
        delta2.turnID = "turn_1"
        delta2.sequence = 2
        delta2.assistantTextDelta = {
            var payload = Pincer_Protocol_V1_AssistantTextDelta()
            payload.segmentID = "assistant"
            payload.delta = " world"
            return payload
        }()

        _ = ThreadEventReducer.apply(delta2, state: &state, fallbackThreadID: "thr_1")

        var committed = Pincer_Protocol_V1_ThreadEvent()
        committed.eventID = "evt_commit"
        committed.threadID = "thr_1"
        committed.turnID = "turn_1"
        committed.sequence = 3
        committed.assistantMessageCommitted = {
            var payload = Pincer_Protocol_V1_AssistantMessageCommitted()
            payload.messageID = "msg_assistant_1"
            payload.fullText = "Hello world"
            return payload
        }()

        let commitEffect = ThreadEventReducer.apply(committed, state: &state, fallbackThreadID: "thr_1")

        XCTAssertFalse(commitEffect.reachedTurnTerminal)
        XCTAssertEqual(state.lastSequence, 3)
        XCTAssertEqual(state.messages.count, 1)
        XCTAssertEqual(state.messages[0].messageID, "msg_assistant_1")
        XCTAssertEqual(state.messages[0].role, "assistant")
        XCTAssertEqual(state.messages[0].content, "Hello world")
    }

    func testToolExecutionStreamingAppendsOutputAndResultLine() {
        var state = ThreadEventReducerState()

        var started = Pincer_Protocol_V1_ThreadEvent()
        started.eventID = "evt_tool_started"
        started.threadID = "thr_1"
        started.sequence = 1
        started.toolExecutionStarted = {
            var payload = Pincer_Protocol_V1_ToolExecutionStarted()
            payload.executionID = "exec_1"
            payload.toolName = "run_bash"
            payload.displayCommand = "pwd"
            return payload
        }()

        let startedEffect = ThreadEventReducer.apply(started, state: &state, fallbackThreadID: "thr_1")
        XCTAssertTrue(startedEffect.receivedProgressSignal)

        var output = Pincer_Protocol_V1_ThreadEvent()
        output.eventID = "evt_tool_output"
        output.threadID = "thr_1"
        output.sequence = 2
        output.toolExecutionOutputDelta = {
            var payload = Pincer_Protocol_V1_ToolExecutionOutputDelta()
            payload.executionID = "exec_1"
            payload.stream = .stdout
            payload.chunk = Data("/tmp/pincer\n".utf8)
            payload.utf8 = true
            return payload
        }()

        let outputEffect = ThreadEventReducer.apply(output, state: &state, fallbackThreadID: "thr_1")
        XCTAssertTrue(outputEffect.receivedProgressSignal)

        var finished = Pincer_Protocol_V1_ThreadEvent()
        finished.eventID = "evt_tool_finished"
        finished.threadID = "thr_1"
        finished.sequence = 3
        finished.toolExecutionFinished = {
            var payload = Pincer_Protocol_V1_ToolExecutionFinished()
            payload.executionID = "exec_1"
            payload.exitCode = 0
            payload.durationMs = 25
            return payload
        }()

        let finishedEffect = ThreadEventReducer.apply(finished, state: &state, fallbackThreadID: "thr_1")
        XCTAssertTrue(finishedEffect.receivedProgressSignal)

        XCTAssertEqual(state.messages.count, 1)
        XCTAssertEqual(state.messages[0].role, "tool")
        XCTAssertTrue(state.messages[0].content.contains("$ pwd"))
        XCTAssertTrue(state.messages[0].content.contains("/tmp/pincer"))
        XCTAssertTrue(state.messages[0].content.contains("result: exit 0 (25ms)"))
    }

    func testActionStatusChangedRequestsApprovalRefreshAndAddsSystemMarker() {
        var state = ThreadEventReducerState()

        var status = Pincer_Protocol_V1_ThreadEvent()
        status.eventID = "evt_status"
        status.threadID = "thr_1"
        status.sequence = 7
        status.proposedActionStatusChanged = {
            var payload = Pincer_Protocol_V1_ProposedActionStatusChanged()
            payload.actionID = "act_1"
            payload.status = .approved
            return payload
        }()

        let effect = ThreadEventReducer.apply(status, state: &state, fallbackThreadID: "thr_1")

        XCTAssertTrue(effect.shouldRefreshApprovals)
        XCTAssertFalse(effect.shouldResyncMessages)
        XCTAssertEqual(state.messages.count, 1)
        XCTAssertEqual(state.messages[0].role, "system")
        XCTAssertEqual(state.messages[0].content, "Action act_1 approved.")
    }

    func testStreamGapRequestsMessageResync() {
        var state = ThreadEventReducerState(messages: [], lastSequence: 10)

        var gap = Pincer_Protocol_V1_ThreadEvent()
        gap.eventID = "evt_gap"
        gap.threadID = "thr_1"
        gap.sequence = 11
        gap.streamGap = {
            var payload = Pincer_Protocol_V1_StreamGap()
            payload.requestedFromSequence = 2
            payload.nextAvailableSequence = 9
            return payload
        }()

        let effect = ThreadEventReducer.apply(gap, state: &state, fallbackThreadID: "thr_1")

        XCTAssertTrue(effect.shouldResyncMessages)
        XCTAssertEqual(state.lastSequence, 11)
    }

    func testDuplicateEventIDIsIgnored() {
        var state = ThreadEventReducerState()

        var event = Pincer_Protocol_V1_ThreadEvent()
        event.eventID = "evt_duplicate"
        event.threadID = "thr_1"
        event.turnID = "turn_1"
        event.sequence = 1
        event.assistantTextDelta = {
            var payload = Pincer_Protocol_V1_AssistantTextDelta()
            payload.delta = "Hello"
            return payload
        }()

        _ = ThreadEventReducer.apply(event, state: &state, fallbackThreadID: "thr_1")
        _ = ThreadEventReducer.apply(event, state: &state, fallbackThreadID: "thr_1")

        XCTAssertEqual(state.messages.count, 1)
        XCTAssertEqual(state.messages[0].content, "Hello")
    }

    func testExecutedStatusDoesNotAddSystemMessage() {
        var state = ThreadEventReducerState()

        var status = Pincer_Protocol_V1_ThreadEvent()
        status.eventID = "evt_executed"
        status.threadID = "thr_1"
        status.sequence = 8
        status.proposedActionStatusChanged = {
            var payload = Pincer_Protocol_V1_ProposedActionStatusChanged()
            payload.actionID = "act_1"
            payload.status = .executed
            return payload
        }()

        let effect = ThreadEventReducer.apply(status, state: &state, fallbackThreadID: "thr_1")

        XCTAssertTrue(effect.shouldRefreshApprovals)
        XCTAssertEqual(state.messages.count, 0)
    }
}
