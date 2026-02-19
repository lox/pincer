import Foundation

struct ThreadEventReducerState {
    var messages: [Message]
    var lastSequence: UInt64

    var turnActivities: [String: TurnActivity] = [:]
    var turnOrder: [String] = []
    var activeTurnID: String?

    fileprivate var seenEventIDs: Set<String>
    fileprivate var assistantDraftMessageIDByTurnID: [String: String]
    fileprivate var thinkingMessageIDByTurnID: [String: String]
    fileprivate var toolMessageIDByExecutionID: [String: String]
    fileprivate var toolCallIDByExecutionID: [String: String]
    fileprivate var toolCallIDByActionID: [String: String]

    init(messages: [Message] = [], lastSequence: UInt64 = 0) {
        self.messages = messages
        self.lastSequence = lastSequence
        self.seenEventIDs = []
        self.assistantDraftMessageIDByTurnID = [:]
        self.thinkingMessageIDByTurnID = [:]
        self.toolMessageIDByExecutionID = [:]
        self.toolCallIDByExecutionID = [:]
        self.toolCallIDByActionID = [:]
        self.turnActivities = [:]
        self.turnOrder = []
        self.activeTurnID = nil
    }
}

struct ThreadEventReducerEffect {
    var reachedTurnTerminal: Bool = false
    var shouldRefreshApprovals: Bool = false
    var shouldResyncMessages: Bool = false
    var receivedProgressSignal: Bool = false
    /// Set when a tool execution finishes but the turn is still running — re-show "Thinking..." bubble.
    var shouldResumeAwaitingProgress: Bool = false
    var turnFailureMessage: String?
}

enum ThreadEventReducer {
    private static let maxSeenEventIDs = 8_192

    private static let timestampFormatter: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter
    }()

    static func apply(
        _ event: Pincer_Protocol_V1_ThreadEvent,
        state: inout ThreadEventReducerState,
        fallbackThreadID: String
    ) -> ThreadEventReducerEffect {
        var effect = ThreadEventReducerEffect()

        let eventID = event.eventID.trimmingCharacters(in: .whitespacesAndNewlines)
        if !eventID.isEmpty {
            if state.seenEventIDs.contains(eventID) {
                return effect
            }
            state.seenEventIDs.insert(eventID)
            if state.seenEventIDs.count > maxSeenEventIDs {
                state.seenEventIDs.removeAll(keepingCapacity: true)
                state.seenEventIDs.insert(eventID)
            }
        }

        if event.sequence > state.lastSequence {
            state.lastSequence = event.sequence
        }

        let threadID = resolvedThreadID(eventThreadID: event.threadID, fallbackThreadID: fallbackThreadID)
        guard !threadID.isEmpty else {
            return effect
        }

        let createdAt = timestampString(for: event)

        let eventDate = dateFromEvent(event)

        switch event.payload {
        case .turnStarted:
            let turnID = event.turnID
            if !turnID.isEmpty {
                var activity = TurnActivity(turnID: turnID)
                activity.startedAt = eventDate
                state.turnActivities[turnID] = activity
                if !state.turnOrder.contains(turnID) {
                    state.turnOrder.append(turnID)
                }
                state.activeTurnID = turnID
            }

        case .assistantThinkingDelta(let delta):
            effect.receivedProgressSignal = true
            let key = turnScopedKey(turnID: event.turnID, fallback: delta.segmentID)
            let messageID = state.thinkingMessageIDByTurnID[key] ?? "stream_thinking_\(key)"
            state.thinkingMessageIDByTurnID[key] = messageID

            var text = delta.delta
            if text.isEmpty && delta.redacted {
                text = "..."
            }
            appendMessageContent(
                messageID: messageID,
                role: "thinking",
                threadID: threadID,
                createdAt: createdAt,
                delta: text,
                state: &state
            )

            let activityTurnID = ensureActiveTurn(event.turnID, eventDate: eventDate, state: &state)
            if !activityTurnID.isEmpty {
                state.turnActivities[activityTurnID]?.thinking.text += delta.delta
                state.turnActivities[activityTurnID]?.thinking.isStreaming = true
            }

        case .assistantTextDelta(let delta):
            effect.receivedProgressSignal = true
            let key = turnScopedKey(turnID: event.turnID, fallback: delta.segmentID)
            let messageID = state.assistantDraftMessageIDByTurnID[key] ?? "stream_assistant_\(key)"
            state.assistantDraftMessageIDByTurnID[key] = messageID
            appendMessageContent(
                messageID: messageID,
                role: "assistant",
                threadID: threadID,
                createdAt: createdAt,
                delta: delta.delta,
                state: &state
            )

        case .assistantMessageCommitted(let committed):
            effect.receivedProgressSignal = true
            let key = turnScopedKey(turnID: event.turnID, fallback: committed.messageID)
            let existingID = state.assistantDraftMessageIDByTurnID[key] ?? "stream_assistant_\(key)"
            let finalMessageID = committed.messageID.isEmpty ? existingID : committed.messageID

            let fallbackContent: String
            if let index = messageIndex(for: existingID, in: state.messages) {
                fallbackContent = state.messages[index].content
            } else {
                fallbackContent = ""
            }
            let finalContent = committed.fullText.isEmpty ? fallbackContent : committed.fullText

            setMessage(
                existingID: existingID,
                finalMessageID: finalMessageID,
                role: "assistant",
                threadID: threadID,
                createdAt: createdAt,
                content: finalContent,
                state: &state
            )
            state.assistantDraftMessageIDByTurnID[key] = finalMessageID

            let commitTurnID = event.turnID.trimmingCharacters(in: .whitespacesAndNewlines)
            if !commitTurnID.isEmpty {
                state.turnActivities[commitTurnID]?.assistantMessageID = finalMessageID
            }

        case .toolCallPlanned(let planned):
            effect.receivedProgressSignal = true
            let activityTurnID = ensureActiveTurn(event.turnID, eventDate: eventDate, state: &state)
            if !activityTurnID.isEmpty {
                if state.turnActivities[activityTurnID]?.firstToolCallAt == nil {
                    state.turnActivities[activityTurnID]?.firstToolCallAt = eventDate
                }
                let argsPreview = extractArgsPreview(from: planned)
                let toolCall = ToolCallActivity(
                    toolCallID: planned.toolCallID,
                    toolName: planned.toolName,
                    displayLabel: planned.toolName,
                    argsPreview: argsPreview,
                    actionID: nil,
                    state: .planned,
                    executions: []
                )
                upsertToolCall(toolCall, inTurn: activityTurnID, state: &state)
            }

        case .toolExecutionStarted(let started):
            effect.receivedProgressSignal = true
            let key = started.executionID.isEmpty ? event.eventID : started.executionID
            let messageID = state.toolMessageIDByExecutionID[key] ?? "stream_tool_\(key)"
            state.toolMessageIDByExecutionID[key] = messageID

            let command = started.displayCommand.trimmingCharacters(in: .whitespacesAndNewlines)
            let fallbackCommand = started.toolName.trimmingCharacters(in: .whitespacesAndNewlines)
            let display = !command.isEmpty ? command : fallbackCommand
            let content = display.isEmpty ? "" : "$ \(display)\n"

            setMessage(
                existingID: messageID,
                finalMessageID: messageID,
                role: "tool",
                threadID: threadID,
                createdAt: createdAt,
                content: content,
                state: &state
            )

            state.toolCallIDByExecutionID[started.executionID] = started.toolCallID
            if let (matchedTurnID, tcIdx) = findToolCall(toolCallID: started.toolCallID, preferredTurnID: event.turnID, state: state) {
                state.turnActivities[matchedTurnID]?.toolCalls[tcIdx].executions.append(
                    ToolExecutionState(executionID: started.executionID)
                )
                state.turnActivities[matchedTurnID]?.toolCalls[tcIdx].state = .running
                let displayCommand = started.displayCommand.trimmingCharacters(in: .whitespacesAndNewlines)
                if !displayCommand.isEmpty {
                    state.turnActivities[matchedTurnID]?.toolCalls[tcIdx].displayLabel = displayCommand
                }
            }

        case .toolExecutionOutputDelta(let delta):
            effect.receivedProgressSignal = true
            let key = delta.executionID.isEmpty ? event.eventID : delta.executionID
            let messageID = state.toolMessageIDByExecutionID[key] ?? "stream_tool_\(key)"
            state.toolMessageIDByExecutionID[key] = messageID
            let chunk = decodedChunk(delta)
            appendMessageContent(
                messageID: messageID,
                role: "tool",
                threadID: threadID,
                createdAt: createdAt,
                delta: chunk,
                state: &state
            )

            if let toolCallID = state.toolCallIDByExecutionID[delta.executionID],
               let (matchedTurnID, tcIdx) = findToolCall(toolCallID: toolCallID, preferredTurnID: event.turnID, state: state),
               let exIdx = state.turnActivities[matchedTurnID]?.toolCalls[tcIdx].executions.firstIndex(where: { $0.executionID == delta.executionID }) {
                state.turnActivities[matchedTurnID]?.toolCalls[tcIdx].executions[exIdx].stdout += chunk
            }

        case .toolExecutionFinished(let finished):
            effect.receivedProgressSignal = true
            let key = finished.executionID.isEmpty ? event.eventID : finished.executionID
            let messageID = state.toolMessageIDByExecutionID[key] ?? "stream_tool_\(key)"
            state.toolMessageIDByExecutionID[key] = messageID

            var suffix = ""
            if let index = messageIndex(for: messageID, in: state.messages),
               !state.messages[index].content.isEmpty,
               !state.messages[index].content.hasSuffix("\n") {
                suffix.append("\n")
            }
            if finished.timedOut {
                suffix.append("result: timed out (\(finished.durationMs)ms)")
            } else {
                suffix.append("result: exit \(finished.exitCode) (\(finished.durationMs)ms)")
            }
            if finished.truncated {
                suffix.append("\nresult: output truncated")
            }

            appendMessageContent(
                messageID: messageID,
                role: "tool",
                threadID: threadID,
                createdAt: createdAt,
                delta: suffix,
                state: &state
            )

            if let toolCallID = state.toolCallIDByExecutionID[finished.executionID],
               let (matchedTurnID, tcIdx) = findToolCall(toolCallID: toolCallID, preferredTurnID: event.turnID, state: state),
               let exIdx = state.turnActivities[matchedTurnID]?.toolCalls[tcIdx].executions.firstIndex(where: { $0.executionID == finished.executionID }) {
                state.turnActivities[matchedTurnID]?.toolCalls[tcIdx].executions[exIdx].exitCode = finished.exitCode
                state.turnActivities[matchedTurnID]?.toolCalls[tcIdx].executions[exIdx].durationMs = finished.durationMs
                state.turnActivities[matchedTurnID]?.toolCalls[tcIdx].executions[exIdx].isStreaming = false
                state.turnActivities[matchedTurnID]?.toolCalls[tcIdx].executions[exIdx].truncated = finished.truncated
                state.turnActivities[matchedTurnID]?.toolCalls[tcIdx].state = finished.exitCode == 0 ? .succeeded : .failed
            }

            effect.shouldResumeAwaitingProgress = true

        case .proposedActionCreated(let created):
            effect.shouldRefreshApprovals = true

            let activityTurnID = ensureActiveTurn(event.turnID, eventDate: eventDate, state: &state)
            // Match by toolCallID == actionID (backend reuses toolCallID as actionID),
            // falling back to tool name match for older event streams.
            if !activityTurnID.isEmpty,
               let tcIdx = state.turnActivities[activityTurnID]?.toolCalls.firstIndex(where: {
                   $0.toolCallID == created.actionID
               }) ?? state.turnActivities[activityTurnID]?.toolCalls.firstIndex(where: {
                   $0.toolName == created.tool && $0.actionID == nil
               }) {
                state.turnActivities[activityTurnID]?.toolCalls[tcIdx].actionID = created.actionID
                state.turnActivities[activityTurnID]?.toolCalls[tcIdx].state = .waitingApproval
                state.toolCallIDByActionID[created.actionID] = state.turnActivities[activityTurnID]?.toolCalls[tcIdx].toolCallID
                let summary = created.deterministicSummary.trimmingCharacters(in: .whitespacesAndNewlines)
                if !summary.isEmpty {
                    state.turnActivities[activityTurnID]?.toolCalls[tcIdx].argsPreview = summary
                }
            }

        case .proposedActionStatusChanged(let statusChanged):
            effect.shouldRefreshApprovals = true
            if let statusMessage = actionStatusMessage(
                for: statusChanged.status,
                actionID: statusChanged.actionID,
                reason: statusChanged.reason
            ) {
                let systemID = eventID.isEmpty ? "stream_status_\(UUID().uuidString.lowercased())" : "stream_status_\(eventID)"
                setMessage(
                    existingID: systemID,
                    finalMessageID: systemID,
                    role: "system",
                    threadID: threadID,
                    createdAt: createdAt,
                    content: statusMessage,
                    state: &state
                )
            }

            if let toolCallID = state.toolCallIDByActionID[statusChanged.actionID] {
                for turnID in state.turnActivities.keys {
                    if let tcIdx = state.turnActivities[turnID]?.toolCalls.firstIndex(where: { $0.toolCallID == toolCallID }) {
                        switch statusChanged.status {
                        case .approved:
                            // Only transition if still waiting — avoids overwriting .running/.succeeded
                            // when approval event arrives after execution has already started.
                            if case .waitingApproval = state.turnActivities[turnID]?.toolCalls[tcIdx].state {
                                state.turnActivities[turnID]?.toolCalls[tcIdx].state = .planned
                            }
                        case .rejected:
                            state.turnActivities[turnID]?.toolCalls[tcIdx].state = .rejected(reason: statusChanged.reason)
                        default:
                            break
                        }
                        break
                    }
                }
            }

        case .turnFailed(let turnFailed):
            effect.reachedTurnTerminal = true
            effect.turnFailureMessage = turnFailed.message.trimmingCharacters(in: .whitespacesAndNewlines)
            let systemID = eventID.isEmpty ? "stream_turn_failed_\(UUID().uuidString.lowercased())" : "stream_turn_failed_\(eventID)"
            setMessage(
                existingID: systemID,
                finalMessageID: systemID,
                role: "system",
                threadID: threadID,
                createdAt: createdAt,
                content: turnFailedContent(turnFailed),
                state: &state
            )

            if let activeTurnID = state.activeTurnID {
                state.turnActivities[activeTurnID]?.status = .failed(message: turnFailed.message)
                state.turnActivities[activeTurnID]?.thinking.isStreaming = false
                state.turnActivities[activeTurnID]?.endedAt = eventDate
                // Fail any non-terminal tool calls
                if let toolCalls = state.turnActivities[activeTurnID]?.toolCalls {
                    for (idx, tc) in toolCalls.enumerated() {
                        switch tc.state {
                        case .planned, .waitingApproval, .running:
                            state.turnActivities[activeTurnID]?.toolCalls[idx].state = .failed
                        default:
                            break
                        }
                    }
                }
                state.activeTurnID = nil
            }

        case .turnPaused:
            // Turn is paused awaiting approval — not terminal, but stop the spinner.
            if let activeTurnID = state.activeTurnID {
                state.turnActivities[activeTurnID]?.status = .paused
                state.turnActivities[activeTurnID]?.thinking.isStreaming = false
            }

        case .turnResumed:
            // Turn resumes after all actions resolved — restart the spinner.
            // Use ensureActiveTurn so replay from snapshot correctly recovers the turn.
            let resumedTurnID = ensureActiveTurn(event.turnID, eventDate: eventDate, state: &state)
            if !resumedTurnID.isEmpty {
                state.turnActivities[resumedTurnID]?.status = .running
            }

            effect.shouldResumeAwaitingProgress = true

            // Clear draft message mappings so the next assistant response creates
            // a new message instead of overwriting the previous step's message.
            let resumeKey = turnScopedKey(turnID: event.turnID, fallback: "")
            if !resumeKey.isEmpty {
                state.assistantDraftMessageIDByTurnID.removeValue(forKey: resumeKey)
                state.thinkingMessageIDByTurnID.removeValue(forKey: resumeKey)
            }

        case .turnCompleted:
            effect.reachedTurnTerminal = true

            if let activeTurnID = state.activeTurnID {
                state.turnActivities[activeTurnID]?.status = .completed
                state.turnActivities[activeTurnID]?.thinking.isStreaming = false
                state.turnActivities[activeTurnID]?.endedAt = eventDate
                state.activeTurnID = nil
            }

        case .streamGap:
            effect.shouldResyncMessages = true

        default:
            break
        }

        return effect
    }

    private static func messageIndex(for messageID: String, in messages: [Message]) -> Int? {
        messages.firstIndex { $0.messageID == messageID }
    }

    private static func setMessage(
        existingID: String,
        finalMessageID: String,
        role: String,
        threadID: String,
        createdAt: String,
        content: String,
        state: inout ThreadEventReducerState
    ) {
        if let index = messageIndex(for: existingID, in: state.messages) {
            let existing = state.messages[index]
            state.messages[index] = Message(
                messageID: finalMessageID,
                threadID: existing.threadID,
                role: role,
                content: content,
                createdAt: existing.createdAt.isEmpty ? createdAt : existing.createdAt
            )
            return
        }

        if existingID != finalMessageID, let index = messageIndex(for: finalMessageID, in: state.messages) {
            let existing = state.messages[index]
            state.messages[index] = Message(
                messageID: finalMessageID,
                threadID: existing.threadID,
                role: role,
                content: content,
                createdAt: existing.createdAt.isEmpty ? createdAt : existing.createdAt
            )
            return
        }

        state.messages.append(Message(
            messageID: finalMessageID,
            threadID: threadID,
            role: role,
            content: content,
            createdAt: createdAt
        ))
    }

    private static func appendMessageContent(
        messageID: String,
        role: String,
        threadID: String,
        createdAt: String,
        delta: String,
        state: inout ThreadEventReducerState
    ) {
        guard !delta.isEmpty else {
            return
        }

        if let index = messageIndex(for: messageID, in: state.messages) {
            let existing = state.messages[index]
            state.messages[index] = Message(
                messageID: existing.messageID,
                threadID: existing.threadID,
                role: role,
                content: existing.content + delta,
                createdAt: existing.createdAt.isEmpty ? createdAt : existing.createdAt
            )
            return
        }

        state.messages.append(Message(
            messageID: messageID,
            threadID: threadID,
            role: role,
            content: delta,
            createdAt: createdAt
        ))
    }

    private static func resolvedThreadID(eventThreadID: String, fallbackThreadID: String) -> String {
        let threadID = eventThreadID.trimmingCharacters(in: .whitespacesAndNewlines)
        if !threadID.isEmpty {
            return threadID
        }
        return fallbackThreadID.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private static func turnScopedKey(turnID: String, fallback: String) -> String {
        let primary = turnID.trimmingCharacters(in: .whitespacesAndNewlines)
        if !primary.isEmpty {
            return primary
        }
        let backup = fallback.trimmingCharacters(in: .whitespacesAndNewlines)
        if !backup.isEmpty {
            return backup
        }
        return "global"
    }

    private static func findToolCall(toolCallID: String, preferredTurnID: String, state: ThreadEventReducerState) -> (turnID: String, index: Int)? {
        let preferred = preferredTurnID.trimmingCharacters(in: .whitespacesAndNewlines)
        if !preferred.isEmpty,
           let tcIdx = state.turnActivities[preferred]?.toolCalls.firstIndex(where: { $0.toolCallID == toolCallID }) {
            return (preferred, tcIdx)
        }
        for turnID in state.turnActivities.keys {
            if let tcIdx = state.turnActivities[turnID]?.toolCalls.firstIndex(where: { $0.toolCallID == toolCallID }) {
                return (turnID, tcIdx)
            }
        }
        return nil
    }

    private static func dateFromEvent(_ event: Pincer_Protocol_V1_ThreadEvent) -> Date? {
        guard event.hasOccurredAt else { return nil }
        let seconds = TimeInterval(event.occurredAt.seconds)
        let nanos = TimeInterval(event.occurredAt.nanos) / 1_000_000_000
        return Date(timeIntervalSince1970: seconds + nanos)
    }

    private static func timestampString(for event: Pincer_Protocol_V1_ThreadEvent) -> String {
        guard event.hasOccurredAt else {
            return ""
        }

        let seconds = TimeInterval(event.occurredAt.seconds)
        let nanos = TimeInterval(event.occurredAt.nanos) / 1_000_000_000
        let date = Date(timeIntervalSince1970: seconds + nanos)
        return timestampFormatter.string(from: date)
    }

    private static func decodedChunk(_ delta: Pincer_Protocol_V1_ToolExecutionOutputDelta) -> String {
        guard !delta.chunk.isEmpty else {
            return ""
        }
        if delta.utf8 {
            return String(decoding: delta.chunk, as: UTF8.self)
        }
        return "[binary output: \(delta.chunk.count) bytes]"
    }

    private static func actionStatusMessage(
        for status: Pincer_Protocol_V1_ActionStatus,
        actionID: String,
        reason: String
    ) -> String? {
        switch status {
        case .approved:
            return "Action \(actionID) approved."
        case .rejected:
            let trimmedReason = reason.trimmingCharacters(in: .whitespacesAndNewlines)
            if trimmedReason.isEmpty {
                return "Action \(actionID) rejected."
            }
            return "Action \(actionID) rejected (\(trimmedReason))."
        default:
            return nil
        }
    }

    private static func turnFailedContent(_ turnFailed: Pincer_Protocol_V1_TurnFailed) -> String {
        let code = turnFailed.code.trimmingCharacters(in: .whitespacesAndNewlines)
        let message = turnFailed.message.trimmingCharacters(in: .whitespacesAndNewlines)

        if code.isEmpty && message.isEmpty {
            return "Turn failed."
        }
        if code.isEmpty {
            return "Turn failed: \(message)"
        }
        if message.isEmpty {
            return "Turn failed (\(code))."
        }
        return "Turn failed (\(code)): \(message)"
    }

    static func orderedActivities(from state: ThreadEventReducerState) -> [TurnActivity] {
        state.turnOrder.compactMap { state.turnActivities[$0] }
    }

    private static func ensureActiveTurn(_ turnID: String, eventDate: Date?, state: inout ThreadEventReducerState) -> String {
        let id = turnID.trimmingCharacters(in: .whitespacesAndNewlines)
        if !id.isEmpty {
            if state.turnActivities[id] == nil {
                var activity = TurnActivity(turnID: id)
                activity.startedAt = eventDate
                state.turnActivities[id] = activity
                if !state.turnOrder.contains(id) {
                    state.turnOrder.append(id)
                }
            }
            state.activeTurnID = id
            return id
        }
        return state.activeTurnID ?? ""
    }

    private static func upsertToolCall(_ toolCall: ToolCallActivity, inTurn turnID: String, state: inout ThreadEventReducerState) {
        guard var turn = state.turnActivities[turnID] else { return }
        if let idx = turn.toolCalls.firstIndex(where: { $0.toolCallID == toolCall.toolCallID }) {
            turn.toolCalls[idx] = toolCall
        } else {
            turn.toolCalls.append(toolCall)
        }
        state.turnActivities[turnID] = turn
    }

    private static func extractArgsPreview(from planned: Pincer_Protocol_V1_ToolCallPlanned) -> String? {
        guard planned.hasArgs else { return nil }
        let fields = planned.args.fields
        guard !fields.isEmpty else { return nil }
        for (_, value) in fields {
            if case .stringValue(let s) = value.kind, !s.isEmpty {
                return s
            }
        }
        return planned.toolName
    }
}
