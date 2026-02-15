import Foundation

struct ThreadEventReducerState {
    var messages: [Message]
    var lastSequence: UInt64

    fileprivate var seenEventIDs: Set<String>
    fileprivate var assistantDraftMessageIDByTurnID: [String: String]
    fileprivate var thinkingMessageIDByTurnID: [String: String]
    fileprivate var toolMessageIDByExecutionID: [String: String]

    init(messages: [Message] = [], lastSequence: UInt64 = 0) {
        self.messages = messages
        self.lastSequence = lastSequence
        self.seenEventIDs = []
        self.assistantDraftMessageIDByTurnID = [:]
        self.thinkingMessageIDByTurnID = [:]
        self.toolMessageIDByExecutionID = [:]
    }
}

struct ThreadEventReducerEffect {
    var reachedTurnTerminal: Bool = false
    var shouldRefreshApprovals: Bool = false
    var shouldResyncMessages: Bool = false
    var receivedProgressSignal: Bool = false
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

        switch event.payload {
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

        case .toolExecutionOutputDelta(let delta):
            effect.receivedProgressSignal = true
            let key = delta.executionID.isEmpty ? event.eventID : delta.executionID
            let messageID = state.toolMessageIDByExecutionID[key] ?? "stream_tool_\(key)"
            state.toolMessageIDByExecutionID[key] = messageID
            appendMessageContent(
                messageID: messageID,
                role: "tool",
                threadID: threadID,
                createdAt: createdAt,
                delta: decodedChunk(delta),
                state: &state
            )

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

        case .proposedActionCreated:
            effect.shouldRefreshApprovals = true

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

        case .turnCompleted:
            effect.reachedTurnTerminal = true

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
}
