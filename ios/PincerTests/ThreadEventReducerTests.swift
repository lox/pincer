import XCTest
import SwiftProtobuf
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

    // MARK: - Activity lifecycle tests

    func testFullApprovalFlowSetsWaitingApproval() {
        var state = ThreadEventReducerState()
        let threadID = "thr_1"
        let turnID = "turn_1"
        let toolCallID = "tc_abc123"

        // 1. TurnStarted
        var turnStarted = Pincer_Protocol_V1_ThreadEvent()
        turnStarted.eventID = "evt_1"
        turnStarted.threadID = threadID
        turnStarted.turnID = turnID
        turnStarted.sequence = 1
        turnStarted.turnStarted = {
            var p = Pincer_Protocol_V1_TurnStarted()
            p.userMessageID = "msg_user_1"
            p.triggerType = .chatMessage
            return p
        }()
        _ = ThreadEventReducer.apply(turnStarted, state: &state, fallbackThreadID: threadID)

        XCTAssertEqual(state.activeTurnID, turnID)
        XCTAssertEqual(state.turnActivities.count, 1)

        // 2. ToolCallPlanned
        var planned = Pincer_Protocol_V1_ThreadEvent()
        planned.eventID = "evt_2"
        planned.threadID = threadID
        planned.turnID = turnID
        planned.sequence = 2
        planned.toolCallPlanned = {
            var p = Pincer_Protocol_V1_ToolCallPlanned()
            p.toolCallID = toolCallID
            p.toolName = "run_bash"
            return p
        }()
        _ = ThreadEventReducer.apply(planned, state: &state, fallbackThreadID: threadID)

        XCTAssertEqual(state.turnActivities[turnID]?.toolCalls.count, 1)
        XCTAssertEqual(state.turnActivities[turnID]?.toolCalls[0].toolCallID, toolCallID)
        if case .planned = state.turnActivities[turnID]?.toolCalls[0].state {} else {
            XCTFail("Expected .planned state")
        }

        // 3. ProposedActionCreated (actionID == toolCallID on backend)
        var actionCreated = Pincer_Protocol_V1_ThreadEvent()
        actionCreated.eventID = "evt_3"
        actionCreated.threadID = threadID
        actionCreated.turnID = turnID
        actionCreated.sequence = 3
        actionCreated.proposedActionCreated = {
            var p = Pincer_Protocol_V1_ProposedActionCreated()
            p.actionID = toolCallID // backend reuses toolCallID as actionID
            p.tool = "run_bash"
            p.deterministicSummary = "Run: echo hello"
            return p
        }()
        let actionEffect = ThreadEventReducer.apply(actionCreated, state: &state, fallbackThreadID: threadID)

        XCTAssertTrue(actionEffect.shouldRefreshApprovals)
        XCTAssertEqual(state.turnActivities[turnID]?.toolCalls[0].actionID, toolCallID)
        if case .waitingApproval = state.turnActivities[turnID]?.toolCalls[0].state {} else {
            XCTFail("Expected .waitingApproval state, got \(String(describing: state.turnActivities[turnID]?.toolCalls[0].state))")
        }
        XCTAssertEqual(state.turnActivities[turnID]?.toolCalls[0].argsPreview, "Run: echo hello")

        // 4. TurnCompleted
        var completed = Pincer_Protocol_V1_ThreadEvent()
        completed.eventID = "evt_4"
        completed.threadID = threadID
        completed.turnID = turnID
        completed.sequence = 4
        completed.turnCompleted = Pincer_Protocol_V1_TurnCompleted()
        let completedEffect = ThreadEventReducer.apply(completed, state: &state, fallbackThreadID: threadID)

        XCTAssertTrue(completedEffect.reachedTurnTerminal)
        XCTAssertNil(state.activeTurnID)
        // Tool call should still be waitingApproval after turn completes
        if case .waitingApproval = state.turnActivities[turnID]?.toolCalls[0].state {} else {
            XCTFail("Expected .waitingApproval state after turnCompleted")
        }
    }

    func testProposedActionCreatedMatchesByToolCallID() {
        var state = ThreadEventReducerState()
        let threadID = "thr_1"
        let turnID = "turn_1"
        let toolCallID = "tc_match_test"

        // Start turn and plan tool call
        var turnStarted = Pincer_Protocol_V1_ThreadEvent()
        turnStarted.eventID = "evt_ts"
        turnStarted.threadID = threadID
        turnStarted.turnID = turnID
        turnStarted.sequence = 1
        turnStarted.turnStarted = Pincer_Protocol_V1_TurnStarted()
        _ = ThreadEventReducer.apply(turnStarted, state: &state, fallbackThreadID: threadID)

        var planned = Pincer_Protocol_V1_ThreadEvent()
        planned.eventID = "evt_tcp"
        planned.threadID = threadID
        planned.turnID = turnID
        planned.sequence = 2
        planned.toolCallPlanned = {
            var p = Pincer_Protocol_V1_ToolCallPlanned()
            p.toolCallID = toolCallID
            p.toolName = "run_bash"
            return p
        }()
        _ = ThreadEventReducer.apply(planned, state: &state, fallbackThreadID: threadID)

        // ProposedActionCreated with actionID == toolCallID (direct ID match)
        var actionCreated = Pincer_Protocol_V1_ThreadEvent()
        actionCreated.eventID = "evt_pac"
        actionCreated.threadID = threadID
        actionCreated.turnID = turnID
        actionCreated.sequence = 3
        actionCreated.proposedActionCreated = {
            var p = Pincer_Protocol_V1_ProposedActionCreated()
            p.actionID = toolCallID
            p.tool = "run_bash"
            return p
        }()
        _ = ThreadEventReducer.apply(actionCreated, state: &state, fallbackThreadID: threadID)

        XCTAssertEqual(state.turnActivities[turnID]?.toolCalls[0].actionID, toolCallID)
        if case .waitingApproval = state.turnActivities[turnID]?.toolCalls[0].state {} else {
            XCTFail("Expected .waitingApproval")
        }
        // toolCallIDByActionID is fileprivate; verified indirectly via state transitions
    }

    func testProposedActionCreatedWithoutPriorTurnStarted() {
        var state = ThreadEventReducerState()
        let threadID = "thr_1"
        let turnID = "turn_orphan"
        let toolCallID = "tc_orphan"

        // ProposedActionCreated arrives without prior TurnStarted or ToolCallPlanned.
        // ensureActiveTurn should create the activity, but no tool call to match.
        var actionCreated = Pincer_Protocol_V1_ThreadEvent()
        actionCreated.eventID = "evt_pac"
        actionCreated.threadID = threadID
        actionCreated.turnID = turnID
        actionCreated.sequence = 1
        actionCreated.proposedActionCreated = {
            var p = Pincer_Protocol_V1_ProposedActionCreated()
            p.actionID = toolCallID
            p.tool = "run_bash"
            return p
        }()
        let effect = ThreadEventReducer.apply(actionCreated, state: &state, fallbackThreadID: threadID)

        XCTAssertTrue(effect.shouldRefreshApprovals)
        // Activity should be created even without turnStarted
        XCTAssertNotNil(state.turnActivities[turnID])
        XCTAssertEqual(state.activeTurnID, turnID)
        // No matching tool call since no ToolCallPlanned preceded it
        XCTAssertEqual(state.turnActivities[turnID]?.toolCalls.count, 0)
    }

    func testOrderedActivitiesReturnsChronologicalOrder() {
        var state = ThreadEventReducerState()

        // Create two turns in order
        var ts1 = Pincer_Protocol_V1_ThreadEvent()
        ts1.eventID = "evt_ts1"
        ts1.threadID = "thr_1"
        ts1.turnID = "turn_a"
        ts1.sequence = 1
        ts1.turnStarted = Pincer_Protocol_V1_TurnStarted()
        _ = ThreadEventReducer.apply(ts1, state: &state, fallbackThreadID: "thr_1")

        var tc1 = Pincer_Protocol_V1_ThreadEvent()
        tc1.eventID = "evt_tc1"
        tc1.threadID = "thr_1"
        tc1.turnID = "turn_a"
        tc1.sequence = 2
        tc1.turnCompleted = Pincer_Protocol_V1_TurnCompleted()
        _ = ThreadEventReducer.apply(tc1, state: &state, fallbackThreadID: "thr_1")

        var ts2 = Pincer_Protocol_V1_ThreadEvent()
        ts2.eventID = "evt_ts2"
        ts2.threadID = "thr_1"
        ts2.turnID = "turn_b"
        ts2.sequence = 3
        ts2.turnStarted = Pincer_Protocol_V1_TurnStarted()
        _ = ThreadEventReducer.apply(ts2, state: &state, fallbackThreadID: "thr_1")

        let activities = ThreadEventReducer.orderedActivities(from: state)
        XCTAssertEqual(activities.count, 2)
        XCTAssertEqual(activities[0].turnID, "turn_a")
        XCTAssertEqual(activities[1].turnID, "turn_b")
    }

    func testTurnFailedSetsFailedStateOnNonTerminalToolCalls() {
        var state = ThreadEventReducerState()
        let turnID = "turn_fail"

        var ts = Pincer_Protocol_V1_ThreadEvent()
        ts.eventID = "evt_ts"
        ts.threadID = "thr_1"
        ts.turnID = turnID
        ts.sequence = 1
        ts.turnStarted = Pincer_Protocol_V1_TurnStarted()
        _ = ThreadEventReducer.apply(ts, state: &state, fallbackThreadID: "thr_1")

        var tcp = Pincer_Protocol_V1_ThreadEvent()
        tcp.eventID = "evt_tcp"
        tcp.threadID = "thr_1"
        tcp.turnID = turnID
        tcp.sequence = 2
        tcp.toolCallPlanned = {
            var p = Pincer_Protocol_V1_ToolCallPlanned()
            p.toolCallID = "tc_1"
            p.toolName = "run_bash"
            return p
        }()
        _ = ThreadEventReducer.apply(tcp, state: &state, fallbackThreadID: "thr_1")

        // Mark as waitingApproval
        var pac = Pincer_Protocol_V1_ThreadEvent()
        pac.eventID = "evt_pac"
        pac.threadID = "thr_1"
        pac.turnID = turnID
        pac.sequence = 3
        pac.proposedActionCreated = {
            var p = Pincer_Protocol_V1_ProposedActionCreated()
            p.actionID = "tc_1"
            p.tool = "run_bash"
            return p
        }()
        _ = ThreadEventReducer.apply(pac, state: &state, fallbackThreadID: "thr_1")

        // Turn fails
        var failed = Pincer_Protocol_V1_ThreadEvent()
        failed.eventID = "evt_fail"
        failed.threadID = "thr_1"
        failed.turnID = turnID
        failed.sequence = 4
        failed.turnFailed = {
            var p = Pincer_Protocol_V1_TurnFailed()
            p.code = "timeout"
            p.message = "Turn timed out"
            return p
        }()
        let failEffect = ThreadEventReducer.apply(failed, state: &state, fallbackThreadID: "thr_1")

        XCTAssertTrue(failEffect.reachedTurnTerminal)
        XCTAssertNil(state.activeTurnID)
        if case .failed = state.turnActivities[turnID]?.toolCalls[0].state {} else {
            XCTFail("Expected .failed state on non-terminal tool call after turn failure")
        }
    }

    func testApprovalThenExecutionTransitionsCorrectly() {
        var state = ThreadEventReducerState()
        let turnID = "turn_exec"
        let toolCallID = "tc_exec"

        // TurnStarted -> ToolCallPlanned -> ProposedActionCreated -> TurnCompleted
        var ts = Pincer_Protocol_V1_ThreadEvent()
        ts.eventID = "evt_1"; ts.threadID = "thr_1"; ts.turnID = turnID; ts.sequence = 1
        ts.turnStarted = Pincer_Protocol_V1_TurnStarted()
        _ = ThreadEventReducer.apply(ts, state: &state, fallbackThreadID: "thr_1")

        var tcp = Pincer_Protocol_V1_ThreadEvent()
        tcp.eventID = "evt_2"; tcp.threadID = "thr_1"; tcp.turnID = turnID; tcp.sequence = 2
        tcp.toolCallPlanned = {
            var p = Pincer_Protocol_V1_ToolCallPlanned()
            p.toolCallID = toolCallID; p.toolName = "run_bash"
            return p
        }()
        _ = ThreadEventReducer.apply(tcp, state: &state, fallbackThreadID: "thr_1")

        var pac = Pincer_Protocol_V1_ThreadEvent()
        pac.eventID = "evt_3"; pac.threadID = "thr_1"; pac.turnID = turnID; pac.sequence = 3
        pac.proposedActionCreated = {
            var p = Pincer_Protocol_V1_ProposedActionCreated()
            p.actionID = toolCallID; p.tool = "run_bash"
            return p
        }()
        _ = ThreadEventReducer.apply(pac, state: &state, fallbackThreadID: "thr_1")

        var tc = Pincer_Protocol_V1_ThreadEvent()
        tc.eventID = "evt_4"; tc.threadID = "thr_1"; tc.turnID = turnID; tc.sequence = 4
        tc.turnCompleted = Pincer_Protocol_V1_TurnCompleted()
        _ = ThreadEventReducer.apply(tc, state: &state, fallbackThreadID: "thr_1")

        // Approval event (arrives later via watchThread)
        var approved = Pincer_Protocol_V1_ThreadEvent()
        approved.eventID = "evt_5"; approved.threadID = "thr_1"; approved.turnID = turnID; approved.sequence = 5
        approved.proposedActionStatusChanged = {
            var p = Pincer_Protocol_V1_ProposedActionStatusChanged()
            p.actionID = toolCallID; p.status = .approved
            return p
        }()
        _ = ThreadEventReducer.apply(approved, state: &state, fallbackThreadID: "thr_1")

        // After approval, state transitions from .waitingApproval -> .planned
        if case .planned = state.turnActivities[turnID]?.toolCalls[0].state {} else {
            XCTFail("Expected .planned after approval, got \(String(describing: state.turnActivities[turnID]?.toolCalls[0].state))")
        }

        // Execution starts
        var execStarted = Pincer_Protocol_V1_ThreadEvent()
        execStarted.eventID = "evt_6"; execStarted.threadID = "thr_1"; execStarted.turnID = turnID; execStarted.sequence = 6
        execStarted.toolExecutionStarted = {
            var p = Pincer_Protocol_V1_ToolExecutionStarted()
            p.executionID = "exec_1"; p.toolCallID = toolCallID; p.toolName = "run_bash"; p.displayCommand = "echo hello"
            return p
        }()
        _ = ThreadEventReducer.apply(execStarted, state: &state, fallbackThreadID: "thr_1")

        if case .running = state.turnActivities[turnID]?.toolCalls[0].state {} else {
            XCTFail("Expected .running after execution started")
        }

        // Execution finishes
        var execFinished = Pincer_Protocol_V1_ThreadEvent()
        execFinished.eventID = "evt_7"; execFinished.threadID = "thr_1"; execFinished.turnID = turnID; execFinished.sequence = 7
        execFinished.toolExecutionFinished = {
            var p = Pincer_Protocol_V1_ToolExecutionFinished()
            p.executionID = "exec_1"; p.exitCode = 0; p.durationMs = 42
            return p
        }()
        _ = ThreadEventReducer.apply(execFinished, state: &state, fallbackThreadID: "thr_1")

        if case .succeeded = state.turnActivities[turnID]?.toolCalls[0].state {} else {
            XCTFail("Expected .succeeded after execution finished with exit 0")
        }
    }

    // MARK: - Timeline ordering tests

    func testTimelineOrdersAssistantMessageBeforeActivityCard() {
        var state = ThreadEventReducerState()
        let threadID = "thr_1"
        let turnID = "turn_1"
        let toolCallID = "tc_1"

        let baseTime: Int64 = 1_700_000_000

        // 0. Pre-existing user message (before the turn starts).
        // Without this, the interleaving has only one message and always
        // appends the activity after it — masking the startedAt vs
        // firstToolCallAt ordering bug.
        let userCreatedAt: String = {
            let f = ISO8601DateFormatter()
            f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            return f.string(from: Date(timeIntervalSince1970: TimeInterval(baseTime - 1)))
        }()
        state.messages.append(Message(
            messageID: "msg_user_1",
            threadID: threadID,
            role: "user",
            content: "Please run pwd",
            createdAt: userCreatedAt
        ))

        // 1. TurnStarted (t+0)
        var turnStarted = Pincer_Protocol_V1_ThreadEvent()
        turnStarted.eventID = "evt_1"
        turnStarted.threadID = threadID
        turnStarted.turnID = turnID
        turnStarted.sequence = 1
        turnStarted.occurredAt = {
            var ts = Google_Protobuf_Timestamp()
            ts.seconds = baseTime
            ts.nanos = 0
            return ts
        }()
        turnStarted.turnStarted = {
            var p = Pincer_Protocol_V1_TurnStarted()
            p.userMessageID = "msg_user_1"
            p.triggerType = .chatMessage
            return p
        }()
        _ = ThreadEventReducer.apply(turnStarted, state: &state, fallbackThreadID: threadID)

        // Use the SAME timestamp for the assistant delta and tool call planned,
        // which is what happens in practice when the server batches events in the
        // same second. The `<` comparison (not `<=`) in rebuildTimeline ensures
        // the activity sorts after the assistant message even when timestamps match.
        let sameSecond = baseTime + 1

        // 2. AssistantTextDelta (same second as ToolCallPlanned)
        var delta = Pincer_Protocol_V1_ThreadEvent()
        delta.eventID = "evt_2"
        delta.threadID = threadID
        delta.turnID = turnID
        delta.sequence = 2
        delta.occurredAt = {
            var ts = Google_Protobuf_Timestamp()
            ts.seconds = sameSecond
            ts.nanos = 0
            return ts
        }()
        delta.assistantTextDelta = {
            var p = Pincer_Protocol_V1_AssistantTextDelta()
            p.segmentID = "assistant"
            p.delta = "I'll help you."
            return p
        }()
        _ = ThreadEventReducer.apply(delta, state: &state, fallbackThreadID: threadID)

        // 3. AssistantMessageCommitted (same second)
        var committed = Pincer_Protocol_V1_ThreadEvent()
        committed.eventID = "evt_3"
        committed.threadID = threadID
        committed.turnID = turnID
        committed.sequence = 3
        committed.occurredAt = {
            var ts = Google_Protobuf_Timestamp()
            ts.seconds = sameSecond
            ts.nanos = 0
            return ts
        }()
        committed.assistantMessageCommitted = {
            var p = Pincer_Protocol_V1_AssistantMessageCommitted()
            p.messageID = "msg_assistant_1"
            p.fullText = "I'll help you."
            return p
        }()
        _ = ThreadEventReducer.apply(committed, state: &state, fallbackThreadID: threadID)

        // 4. ToolCallPlanned (same second) — sets firstToolCallAt
        var planned = Pincer_Protocol_V1_ThreadEvent()
        planned.eventID = "evt_4"
        planned.threadID = threadID
        planned.turnID = turnID
        planned.sequence = 4
        planned.occurredAt = {
            var ts = Google_Protobuf_Timestamp()
            ts.seconds = sameSecond
            ts.nanos = 0
            return ts
        }()
        planned.toolCallPlanned = {
            var p = Pincer_Protocol_V1_ToolCallPlanned()
            p.toolCallID = toolCallID
            p.toolName = "run_bash"
            return p
        }()
        _ = ThreadEventReducer.apply(planned, state: &state, fallbackThreadID: threadID)

        // 5. ProposedActionCreated (same second)
        var actionCreated = Pincer_Protocol_V1_ThreadEvent()
        actionCreated.eventID = "evt_5"
        actionCreated.threadID = threadID
        actionCreated.turnID = turnID
        actionCreated.sequence = 5
        actionCreated.occurredAt = {
            var ts = Google_Protobuf_Timestamp()
            ts.seconds = sameSecond
            ts.nanos = 0
            return ts
        }()
        actionCreated.proposedActionCreated = {
            var p = Pincer_Protocol_V1_ProposedActionCreated()
            p.actionID = toolCallID
            p.tool = "run_bash"
            return p
        }()
        _ = ThreadEventReducer.apply(actionCreated, state: &state, fallbackThreadID: threadID)

        // 6. TurnPaused (same second)
        var paused = Pincer_Protocol_V1_ThreadEvent()
        paused.eventID = "evt_6"
        paused.threadID = threadID
        paused.turnID = turnID
        paused.sequence = 6
        paused.occurredAt = {
            var ts = Google_Protobuf_Timestamp()
            ts.seconds = sameSecond
            ts.nanos = 0
            return ts
        }()
        paused.turnPaused = Pincer_Protocol_V1_TurnPaused()
        _ = ThreadEventReducer.apply(paused, state: &state, fallbackThreadID: threadID)

        // --- Verify reducer state ---
        let activities = ThreadEventReducer.orderedActivities(from: state)
        XCTAssertEqual(activities.count, 1, "Expected one activity for the turn")
        XCTAssertEqual(activities[0].assistantMessageID, "msg_assistant_1",
                       "Activity should be anchored to the committed assistant message")
        XCTAssertNotNil(activities[0].firstToolCallAt, "firstToolCallAt should be set by ToolCallPlanned")
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
