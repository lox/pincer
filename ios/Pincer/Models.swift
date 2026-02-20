import Foundation

struct ThreadSummary: Identifiable {
    let threadID: String
    let title: String
    let createdAt: String
    let updatedAt: String
    let messageCount: Int

    var id: String { threadID }

    var displayTitle: String {
        title.isEmpty ? "New conversation" : title
    }
}

struct Message: Codable, Identifiable {
    let messageID: String
    let threadID: String
    let role: String
    let content: String
    let createdAt: String

    var id: String { messageID }
}

struct Approval: Codable, Identifiable {
    let actionID: String
    let source: String
    let sourceID: String
    let tool: String
    let status: String
    let riskClass: String
    let deterministicSummary: String
    let commandPreview: String
    let commandTimeoutMS: Int64?
    let createdAt: String
    let expiresAt: String

    var id: String { actionID }
}

struct Device: Codable, Identifiable {
    let deviceID: String
    let name: String
    let revokedAt: String
    let createdAt: String
    let isCurrent: Bool

    var id: String { deviceID }
    var isRevoked: Bool { !revokedAt.isEmpty }
}

// MARK: - Activity Model

enum TurnStatus {
    case running
    case paused
    case completed
    case failed(message: String)
}

struct ThinkingState {
    var text: String = ""
    var isStreaming: Bool = false
}

struct ToolCallActivity: Identifiable {
    let toolCallID: String
    var toolName: String
    var displayLabel: String
    var argsPreview: String?
    var actionID: String?
    var state: ToolCallState
    var executions: [ToolExecutionState]

    var id: String { toolCallID }
}

enum ToolCallState {
    case planned
    case waitingApproval
    case running
    case succeeded
    case failed
    case rejected(reason: String)
}

struct ToolExecutionState: Identifiable {
    let executionID: String
    var stdout: String = ""
    var stderr: String = ""
    var exitCode: Int32?
    var durationMs: UInt64?
    var isStreaming: Bool = true
    var truncated: Bool = false

    var id: String { executionID }
}

struct TurnActivity: Identifiable {
    let turnID: String
    var status: TurnStatus = .running
    var thinking: ThinkingState = ThinkingState()
    var toolCalls: [ToolCallActivity] = []
    var startedAt: Date?
    var endedAt: Date?
    /// Set when the first ToolCallPlanned arrives.
    var firstToolCallAt: Date?
    var assistantMessageID: String?
    var id: String { turnID }
}

enum ChatTimelineItem: Identifiable {
    case message(Message)
    case approval(Approval)

    var id: String {
        switch self {
        case .message(let m): return "msg_\(m.id)"
        case .approval(let a): return "apv_\(a.id)"
        }
    }
}
