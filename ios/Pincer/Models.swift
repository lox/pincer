import Foundation

struct EmptyRequest: Codable {}

struct ThreadResponse: Codable {
    let threadID: String

    enum CodingKeys: String, CodingKey {
        case threadID = "threadId"
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
        case messageID = "messageId"
        case threadID = "threadId"
        case role
        case content
        case createdAt
    }

    init(messageID: String, threadID: String, role: String, content: String, createdAt: String) {
        self.messageID = messageID
        self.threadID = threadID
        self.role = role
        self.content = content
        self.createdAt = createdAt
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        messageID = try container.decode(String.self, forKey: .messageID)
        threadID = (try? container.decode(String.self, forKey: .threadID)) ?? ""
        role = try container.decode(String.self, forKey: .role)
        content = try container.decode(String.self, forKey: .content)
        createdAt = try container.decode(String.self, forKey: .createdAt)
    }
}

struct MessagesResponse: Codable {
    let items: [Message]

    enum CodingKeys: String, CodingKey {
        case items
    }

    init(items: [Message]) {
        self.items = items
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        items = try container.decodeIfPresent([Message].self, forKey: .items) ?? []
    }
}

struct SendTurnRequest: Codable {
    let threadID: String
    let userText: String
    let triggerType: String

    enum CodingKeys: String, CodingKey {
        case threadID = "threadId"
        case userText
        case triggerType
    }
}

struct SendTurnResponse: Codable {
    let turnID: String
    let assistantMessage: String
    let actionID: String

    enum CodingKeys: String, CodingKey {
        case turnID = "turnId"
        case assistantMessage
        case actionID = "actionId"
    }

    init(turnID: String, assistantMessage: String, actionID: String) {
        self.turnID = turnID
        self.assistantMessage = assistantMessage
        self.actionID = actionID
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        turnID = try container.decodeIfPresent(String.self, forKey: .turnID) ?? ""
        assistantMessage = try container.decodeIfPresent(String.self, forKey: .assistantMessage) ?? ""
        actionID = try container.decodeIfPresent(String.self, forKey: .actionID) ?? ""
    }
}

struct ListThreadMessagesRequest: Codable {
    let threadID: String

    enum CodingKeys: String, CodingKey {
        case threadID = "threadId"
    }
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
        case actionID = "actionId"
        case source
        case sourceID = "sourceId"
        case tool
        case status
        case riskClass
        case createdAt
        case expiresAt
    }
}

struct ListApprovalsRequest: Codable {
    let status: String
}

struct ApprovalsResponse: Codable {
    let items: [Approval]

    enum CodingKeys: String, CodingKey {
        case items
    }

    init(items: [Approval]) {
        self.items = items
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        items = try container.decodeIfPresent([Approval].self, forKey: .items) ?? []
    }
}

struct ApproveActionRequest: Codable {
    let actionID: String

    enum CodingKeys: String, CodingKey {
        case actionID = "actionId"
    }
}

struct PairingCodeResponse: Codable {
    let code: String
    let expiresAt: String
}

struct PairingBindRequest: Codable {
    let code: String
    let deviceName: String
}

struct PairingBindResponse: Codable {
    let deviceID: String
    let token: String
    let expiresAt: String

    enum CodingKeys: String, CodingKey {
        case deviceID = "deviceId"
        case token
        case expiresAt
    }
}

struct Device: Codable, Identifiable {
    let deviceID: String
    let name: String
    let revokedAt: String
    let createdAt: String
    let isCurrent: Bool

    var id: String { deviceID }
    var isRevoked: Bool { !revokedAt.isEmpty }

    enum CodingKeys: String, CodingKey {
        case deviceID = "deviceId"
        case name
        case revokedAt
        case createdAt
        case isCurrent
    }
}

struct DevicesResponse: Codable {
    let items: [Device]

    enum CodingKeys: String, CodingKey {
        case items
    }

    init(items: [Device]) {
        self.items = items
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        items = try container.decodeIfPresent([Device].self, forKey: .items) ?? []
    }
}

struct RevokeDeviceRequest: Codable {
    let deviceID: String

    enum CodingKeys: String, CodingKey {
        case deviceID = "deviceId"
    }
}
