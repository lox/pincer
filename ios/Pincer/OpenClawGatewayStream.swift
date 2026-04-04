import Foundation

struct GatewayStateVersion: Sendable, Equatable {
    let presence: Int
    let health: Int
}

struct GatewayGapEvent: Sendable, Equatable {
    let expected: UInt64
    let received: UInt64
    let stateVersion: GatewayStateVersion?
}

struct GatewayChatMessage: Sendable, Equatable {
    let role: String
    let text: String
    let thinkingText: String
    let command: Bool
    let hasAttachments: Bool
}

enum GatewayChatEventState: String, Sendable, Equatable {
    case delta
    case final
    case aborted
    case error
}

struct GatewayChatEvent: Sendable, Equatable {
    let runID: String
    let sessionKey: String
    let sequence: UInt64
    let state: GatewayChatEventState
    let message: GatewayChatMessage?
    let errorMessage: String?
    let stopReason: String?
}

struct GatewayToolEvent: Sendable, Equatable {
    let phase: String
    let toolCallID: String
    let name: String
    let argsPreview: String?
    let outputPreview: String?
    let isError: Bool
}

struct GatewayAgentEvent: Sendable, Equatable {
    let runID: String
    let sessionKey: String?
    let sequence: UInt64
    let stream: String
    let tool: GatewayToolEvent?
    let lifecyclePhase: String?
}

enum GatewayConnectionEvent: Sendable, Equatable {
    case connected
    case reconnecting
    case disconnected(reason: String)
    case gap(GatewayGapEvent)
    case presence(GatewayStateVersion)
    case health(GatewayStateVersion)
    case chat(GatewayChatEvent)
    case agent(GatewayAgentEvent)
}

struct ChatStreamState: Equatable {
    var messages: [Message] = []
    var timelineItems: [ChatTimelineItem] = []
    var activeRunID: String?
    var suppressedRunIDs: Set<String> = []
    var assistantDraftText: String = ""
    var assistantThinkingText: String = ""
    var latestToolCalls: [ToolCallActivity] = []
    var connectionNotice: String?
    var needsSnapshotRefresh = false
}

func applyGatewayConnectionEvent(
    _ event: GatewayConnectionEvent,
    to state: inout ChatStreamState,
    currentThreadID: String,
    now: () -> String = gatewayCurrentTimestampString
) {
    switch event {
    case .connected:
        state.connectionNotice = nil
        if !state.timelineItems.isEmpty || !state.assistantDraftText.isEmpty || !state.latestToolCalls.isEmpty {
            state.needsSnapshotRefresh = true
        }
    case .reconnecting:
        state.connectionNotice = "Reconnecting to OpenClaw…"
    case .disconnected(let reason):
        state.connectionNotice = reason.isEmpty ? "Gateway disconnected." : "Gateway disconnected: \(reason)"
    case .gap:
        state.connectionNotice = "Gateway event gap detected. Refreshing chat…"
        state.needsSnapshotRefresh = true
    case .presence, .health:
        break
    case .chat(let chatEvent):
        guard sessionKeyMatchesPrimary(chatEvent.sessionKey, primarySessionKey: currentThreadID) ||
                chatEvent.sessionKey == currentThreadID else {
            return
        }
        applyGatewayChatEvent(chatEvent, to: &state, threadID: currentThreadID, now: now)
    case .agent(let agentEvent):
        guard matchesGatewaySession(agentEvent.sessionKey, currentThreadID: currentThreadID) else {
            return
        }
        applyGatewayAgentEvent(agentEvent, to: &state)
    }
}

func parseGatewayConnectionEvent(from frame: [String: Any]) -> GatewayConnectionEvent? {
    guard isGatewayEventFrame(frame),
          let eventName = gatewayStreamTrimmedString(frame["event"] as? String)?.lowercased() else {
        return nil
    }

    let sequence = gatewayStreamUInt64(frame["seq"])
    let stateVersion = gatewayStreamStateVersion(from: frame["stateVersion"])

    switch eventName {
    case "chat":
        guard let payload = frame["payload"] as? [String: Any],
              let runID = gatewayStreamTrimmedString(payload["runId"] as? String),
              let sessionKey = gatewayStreamTrimmedString(payload["sessionKey"] as? String),
              let rawState = gatewayStreamTrimmedString(payload["state"] as? String)?.lowercased(),
              let state = GatewayChatEventState(rawValue: rawState) else {
            return nil
        }

        return .chat(
            GatewayChatEvent(
                runID: runID,
                sessionKey: sessionKey,
                sequence: sequence ?? 0,
                state: state,
                message: parseGatewayChatMessage(from: payload["message"]),
                errorMessage: gatewayStreamTrimmedString(payload["errorMessage"] as? String),
                stopReason: gatewayStreamTrimmedString(payload["stopReason"] as? String)
            )
        )
    case "agent":
        guard let payload = frame["payload"] as? [String: Any],
              let runID = gatewayStreamTrimmedString(payload["runId"] as? String),
              let stream = gatewayStreamTrimmedString(payload["stream"] as? String)?.lowercased() else {
            return nil
        }

        let data = payload["data"] as? [String: Any]
        let tool: GatewayToolEvent?
        if stream == "tool" {
            tool = parseGatewayToolEvent(from: data)
        } else {
            tool = nil
        }

        let lifecyclePhase = stream == "lifecycle" ? gatewayStreamTrimmedString(data?["phase"] as? String) : nil

        return .agent(
            GatewayAgentEvent(
                runID: runID,
                sessionKey: gatewayStreamTrimmedString(payload["sessionKey"] as? String),
                sequence: sequence ?? 0,
                stream: stream,
                tool: tool,
                lifecyclePhase: lifecyclePhase
            )
        )
    case "presence":
        guard let stateVersion else {
            return nil
        }
        return .presence(stateVersion)
    case "health":
        guard let stateVersion else {
            return nil
        }
        return .health(stateVersion)
    default:
        return nil
    }
}

func parseGatewayChatMessage(from raw: Any?) -> GatewayChatMessage? {
    guard let message = raw as? [String: Any] else {
        return nil
    }

    let contentSummary = gatewayStreamContentSummary(from: message["content"])
    let fallbackText = gatewayStreamTrimmedString(message["text"] as? String) ?? ""
    let combinedText = [contentSummary.text, fallbackText]
        .compactMap(gatewayStreamTrimmedString)
        .joined(separator: "\n\n")

    return GatewayChatMessage(
        role: gatewayStreamTrimmedString(message["role"] as? String)?.lowercased() ?? "assistant",
        text: combinedText,
        thinkingText: contentSummary.thinking,
        command: message["command"] as? Bool ?? false,
        hasAttachments: contentSummary.hasAttachments
    )
}

func gatewayChatRenderableText(_ message: GatewayChatMessage, includeThinking: Bool = false) -> String {
    if let content = gatewayChatRenderableContent(message, includeThinking: includeThinking) {
        return content
    }

    if message.hasAttachments {
        return "[Attachment]"
    }

    return "(no output)"
}

private func gatewayChatRenderableContent(_ message: GatewayChatMessage, includeThinking: Bool = false) -> String? {
    let thinking = includeThinking ? gatewayStreamTrimmedString(message.thinkingText) : nil
    let text = sanitizeGatewayRenderableText(message.text, role: message.role)
    let combined = [thinking, text]
        .compactMap { $0 }
        .joined(separator: "\n\n")

    return gatewayStreamTrimmedString(combined)
}

func gatewayToolPreview(from raw: Any?) -> String? {
    if let string = gatewayStreamTrimmedString(raw as? String) {
        return string
    }

    if let object = raw as? [String: Any] {
        if let contentSummary = gatewayStreamTrimmedString(gatewayStreamContentSummary(from: object["content"]).text) {
            return contentSummary
        }

        if let stdout = gatewayStreamTrimmedString(object["stdout"] as? String) {
            return stdout
        }

        if let stderr = gatewayStreamTrimmedString(object["stderr"] as? String) {
            return stderr
        }
    }

    return gatewayCompactJSONString(from: raw)
}

private func applyGatewayChatEvent(
    _ event: GatewayChatEvent,
    to state: inout ChatStreamState,
    threadID: String,
    now: () -> String
) {
    state.connectionNotice = nil

    if let message = event.message,
       let content = gatewayChatRenderableContent(message),
       isGatewayHeartbeatMaintenancePrompt(content, role: message.role) {
        state.suppressedRunIDs.insert(event.runID)
        clearGatewayDraft(for: event.runID, state: &state)
        return
    }

    if let message = event.message,
       let content = gatewayChatRenderableContent(message),
       isGatewayHeartbeatMaintenanceReply(content, role: message.role) {
        state.suppressedRunIDs.remove(event.runID)
        clearGatewayDraft(for: event.runID, state: &state)
        return
    }

    if state.suppressedRunIDs.contains(event.runID) {
        if event.state == .final || event.state == .aborted || event.state == .error {
            state.suppressedRunIDs.remove(event.runID)
            clearGatewayDraft(for: event.runID, state: &state)
        }
        return
    }

    if state.activeRunID != event.runID, event.state == .delta {
        state.activeRunID = event.runID
    }

    switch event.state {
    case .delta:
        state.activeRunID = event.runID
        if let message = event.message {
            state.assistantDraftText = gatewayChatRenderableContent(message) ?? ""
            state.assistantThinkingText = gatewayStreamTrimmedString(message.thinkingText) ?? ""
        }
    case .final:
        let finalizedMessage = resolvedFinalGatewayMessage(
            from: event,
            existingDraft: state.assistantDraftText,
            threadID: threadID,
            now: now
        )
        if let finalizedMessage {
            state.messages.append(finalizedMessage)
            state.timelineItems.append(.message(finalizedMessage))
        }
        state.activeRunID = nil
        state.assistantDraftText = ""
        state.assistantThinkingText = ""
        state.needsSnapshotRefresh = true
    case .aborted:
        if let partial = resolvedAbortedGatewayMessage(
            runID: event.runID,
            threadID: threadID,
            existingDraft: state.assistantDraftText,
            now: now
        ) {
            state.messages.append(partial)
            state.timelineItems.append(.message(partial))
        }
        state.activeRunID = nil
        state.assistantDraftText = ""
        state.assistantThinkingText = ""
        state.connectionNotice = "Run aborted."
        state.needsSnapshotRefresh = true
    case .error:
        if let partial = resolvedAbortedGatewayMessage(
            runID: event.runID,
            threadID: threadID,
            existingDraft: state.assistantDraftText,
            now: now
        ) {
            state.messages.append(partial)
            state.timelineItems.append(.message(partial))
        }
        if let errorMessage = gatewayStreamTrimmedString(event.errorMessage) {
            let systemMessage = Message(
                messageID: "system_\(event.runID)",
                threadID: threadID,
                role: "system",
                content: errorMessage,
                createdAt: now()
            )
            state.messages.append(systemMessage)
            state.timelineItems.append(.message(systemMessage))
        }
        state.activeRunID = nil
        state.assistantDraftText = ""
        state.assistantThinkingText = ""
        state.connectionNotice = "Run failed."
        state.needsSnapshotRefresh = true
    }
}

private func applyGatewayAgentEvent(_ event: GatewayAgentEvent, to state: inout ChatStreamState) {
    if state.suppressedRunIDs.contains(event.runID) {
        if event.stream == "lifecycle",
           let phase = event.lifecyclePhase?.lowercased(),
           phase == "end" || phase == "error" {
            state.suppressedRunIDs.remove(event.runID)
            clearGatewayDraft(for: event.runID, state: &state)
        }
        return
    }

    guard state.activeRunID == nil || state.activeRunID == event.runID else {
        return
    }

    if let tool = event.tool {
        upsertGatewayToolEvent(tool, into: &state.latestToolCalls)
        return
    }

    guard event.stream == "lifecycle" else {
        return
    }

    switch event.lifecyclePhase?.lowercased() {
    case "start":
        state.activeRunID = event.runID
    case "error":
        state.connectionNotice = "Run failed."
        state.needsSnapshotRefresh = true
    case "end":
        state.needsSnapshotRefresh = true
    default:
        break
    }
}

private func clearGatewayDraft(for runID: String, state: inout ChatStreamState) {
    guard state.activeRunID == runID else {
        return
    }

    state.activeRunID = nil
    state.assistantDraftText = ""
    state.assistantThinkingText = ""
}

private func upsertGatewayToolEvent(_ event: GatewayToolEvent, into toolCalls: inout [ToolCallActivity]) {
    let displayLabel = gatewayStreamTrimmedString(event.name) ?? "Tool"
    let output = gatewayToolPreview(from: event.outputPreview)
    if let index = toolCalls.firstIndex(where: { $0.toolCallID == event.toolCallID }) {
        toolCalls[index].toolName = displayLabel
        toolCalls[index].displayLabel = displayLabel
        toolCalls[index].argsPreview = event.argsPreview ?? toolCalls[index].argsPreview
        toolCalls[index].state = gatewayToolState(phase: event.phase, isError: event.isError)

        if toolCalls[index].executions.isEmpty {
            toolCalls[index].executions = [ToolExecutionState(executionID: event.toolCallID)]
        }

        if let output {
            toolCalls[index].executions[0].stdout = output
            toolCalls[index].executions[0].stderr = event.isError ? output : ""
        }
        toolCalls[index].executions[0].isStreaming = event.phase.lowercased() != "result"
        return
    }

    var execution = ToolExecutionState(executionID: event.toolCallID)
    if let output {
        execution.stdout = output
        execution.stderr = event.isError ? output : ""
    }
    execution.isStreaming = event.phase.lowercased() != "result"

    toolCalls.append(
        ToolCallActivity(
            toolCallID: event.toolCallID,
            toolName: displayLabel,
            displayLabel: displayLabel,
            argsPreview: event.argsPreview,
            actionID: nil,
            state: gatewayToolState(phase: event.phase, isError: event.isError),
            executions: [execution]
        )
    )
}

private func gatewayToolState(phase: String, isError: Bool) -> ToolCallState {
    if isError {
        return .failed
    }

    switch phase.lowercased() {
    case "start", "update":
        return .running
    case "result":
        return .succeeded
    default:
        return .planned
    }
}

private func resolvedFinalGatewayMessage(
    from event: GatewayChatEvent,
    existingDraft: String,
    threadID: String,
    now: () -> String
) -> Message? {
    if let message = event.message, !message.command {
        guard let content = gatewayChatRenderableContent(message) else {
            return nil
        }

        return Message(
            messageID: "run_\(event.runID)",
            threadID: threadID,
            role: message.role,
            content: content,
            createdAt: now()
        )
    }

    guard let content = gatewayStreamTrimmedString(existingDraft) else {
        return nil
    }

    return Message(
        messageID: "run_\(event.runID)",
        threadID: threadID,
        role: "assistant",
        content: content,
        createdAt: now()
    )
}

private func resolvedAbortedGatewayMessage(
    runID: String,
    threadID: String,
    existingDraft: String,
    now: () -> String
) -> Message? {
    guard let content = gatewayStreamTrimmedString(existingDraft) else {
        return nil
    }

    return Message(
        messageID: "partial_\(runID)",
        threadID: threadID,
        role: "assistant",
        content: content,
        createdAt: now()
    )
}

private func parseGatewayToolEvent(from raw: [String: Any]?) -> GatewayToolEvent? {
    guard let raw,
          let phase = gatewayStreamTrimmedString(raw["phase"] as? String),
          let toolCallID = gatewayStreamTrimmedString(raw["toolCallId"] as? String) else {
        return nil
    }

    return GatewayToolEvent(
        phase: phase,
        toolCallID: toolCallID,
        name: gatewayStreamTrimmedString(raw["name"] as? String) ?? "tool",
        argsPreview: gatewayCompactJSONString(from: raw["args"]),
        outputPreview: gatewayToolPreview(from: raw["partialResult"] ?? raw["result"]),
        isError: raw["isError"] as? Bool ?? false
    )
}

private func matchesGatewaySession(_ sessionKey: String?, currentThreadID: String) -> Bool {
    guard let sessionKey = gatewayStreamTrimmedString(sessionKey) else {
        return false
    }

    return sessionKey == currentThreadID ||
        sessionKeyMatchesPrimary(sessionKey, primarySessionKey: currentThreadID)
}

private func gatewayStreamContentSummary(from raw: Any?) -> (text: String, thinking: String, hasAttachments: Bool) {
    if let text = gatewayStreamTrimmedString(raw as? String) {
        return (text, "", false)
    }

    guard let blocks = raw as? [Any] else {
        return ("", "", false)
    }

    var textParts: [String] = []
    var thinkingParts: [String] = []
    var hasAttachments = false

    for block in blocks {
        guard let entry = block as? [String: Any],
              let type = gatewayStreamTrimmedString(entry["type"] as? String)?.lowercased() else {
            continue
        }

        switch type {
        case "text":
            if let text = gatewayStreamTrimmedString(entry["text"] as? String) {
                textParts.append(text)
            }
        case "thinking":
            if let thinking = gatewayStreamTrimmedString(entry["thinking"] as? String) {
                thinkingParts.append(thinking)
            }
        default:
            hasAttachments = true
        }
    }

    return (textParts.joined(separator: "\n\n"), thinkingParts.joined(separator: "\n\n"), hasAttachments)
}

private func gatewayCompactJSONString(from raw: Any?) -> String? {
    guard let raw else {
        return nil
    }

    if let string = gatewayStreamTrimmedString(raw as? String) {
        return string
    }

    guard JSONSerialization.isValidJSONObject(raw),
          let data = try? JSONSerialization.data(withJSONObject: raw, options: [.sortedKeys]),
          let text = String(data: data, encoding: .utf8) else {
        return nil
    }

    return gatewayStreamTrimmedString(text)
}

func gatewayStreamStateVersion(from raw: Any?) -> GatewayStateVersion? {
    guard let object = raw as? [String: Any],
          let presence = gatewayStreamInt(object["presence"]),
          let health = gatewayStreamInt(object["health"]) else {
        return nil
    }

    return GatewayStateVersion(presence: presence, health: health)
}

func gatewayStreamUInt64(_ raw: Any?) -> UInt64? {
    switch raw {
    case let number as NSNumber:
        return number.uint64Value
    case let value as UInt64:
        return value
    case let value as Int:
        return value >= 0 ? UInt64(value) : nil
    default:
        return nil
    }
}

private func gatewayStreamInt(_ raw: Any?) -> Int? {
    switch raw {
    case let number as NSNumber:
        return number.intValue
    case let value as Int:
        return value
    default:
        return nil
    }
}

private func gatewayStreamTrimmedString(_ value: String?) -> String? {
    let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    return trimmed.isEmpty ? nil : trimmed
}

private func gatewayCurrentTimestampString() -> String {
    ISO8601DateFormatter().string(from: Date())
}
