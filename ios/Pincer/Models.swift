import Foundation

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
