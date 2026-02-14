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

struct PairingCodeResponse: Codable {
    let code: String
    let expiresAt: String

    enum CodingKeys: String, CodingKey {
        case code
        case expiresAt = "expires_at"
    }
}

struct PairingBindRequest: Codable {
    let code: String
    let deviceName: String

    enum CodingKeys: String, CodingKey {
        case code
        case deviceName = "device_name"
    }
}

struct PairingBindResponse: Codable {
	let deviceID: String
	let token: String
	let expiresAt: String

    enum CodingKeys: String, CodingKey {
        case deviceID = "device_id"
        case token
		case expiresAt = "expires_at"
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
		case deviceID = "device_id"
		case name
		case revokedAt = "revoked_at"
		case createdAt = "created_at"
		case isCurrent = "is_current"
	}
}

struct DevicesResponse: Codable {
	let items: [Device]
}
