import Foundation

struct ThreadResponse: Codable {
    let threadID: String

    enum CodingKeys: String, CodingKey {
        case threadID = "thread_id"
    }
}

struct Message: Codable, Identifiable {
    let messageID: String
    let threadID: String
    let role: String
    let content: String
    let createdAt: String

    var id: String { messageID }

    enum CodingKeys: String, CodingKey {
        case messageID = "message_id"
        case threadID = "thread_id"
        case role
        case content
        case createdAt = "created_at"
    }
}

struct MessagesResponse: Codable {
    let items: [Message]
}

struct SendMessageRequest: Codable {
    let content: String
}

struct Approval: Codable, Identifiable {
    let actionID: String
    let source: String
    let sourceID: String
    let tool: String
    let status: String
    let riskClass: String
    let createdAt: String
    let expiresAt: String

    var id: String { actionID }

    enum CodingKeys: String, CodingKey {
        case actionID = "action_id"
        case source
        case sourceID = "source_id"
        case tool
        case status
        case riskClass = "risk_class"
        case createdAt = "created_at"
        case expiresAt = "expires_at"
    }
}

struct ApprovalsResponse: Codable {
    let items: [Approval]
}
