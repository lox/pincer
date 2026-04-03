import Foundation

enum APIError: Error {
    case invalidResponse
    case rpc(String)
    case unauthorized
}

struct ThreadMessagesSnapshot {
    let messages: [Message]
    let lastSequence: UInt64
}

struct GatewayProbeResult: Sendable, Equatable {
    let code: String
    let detail: String
}

private struct StoredThread: Codable {
    let threadID: String
    var title: String
    let createdAt: String
    var updatedAt: String
}

private enum LocalStorage {
    static let threadsKey = "OPENCLAW_LOCAL_THREADS"
    static let approvalsKey = "OPENCLAW_LOCAL_APPROVALS"
}

actor APIClient {
    private var baseURL: URL
    private var token: String
    private let defaults = UserDefaults.standard

    private static let timestampFormatter: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter
    }()

    init(baseURL: URL, token: String) {
        self.baseURL = baseURL
        self.token = token
        Self.seedDefaultStateIfNeeded(defaults: defaults)
    }

    func setBaseURL(_ baseURL: URL) {
        self.baseURL = baseURL
        AppConfig.setBaseURL(baseURL)
    }

    func setGatewayToken(_ token: String) {
        self.token = token.trimmingCharacters(in: .whitespacesAndNewlines)
        AppConfig.setBearerToken(self.token)
    }

    func setPrimarySessionKey(_ sessionKey: String) {
        AppConfig.setPrimarySessionKey(sessionKey)
    }

    func createThread() async throws -> String {
        var threads = loadThreads()
        let now = Self.nowString()
        let title = "Session \(threads.count + 1)"
        let threadID = "sess_\(UUID().uuidString.lowercased())"
        threads.insert(
            StoredThread(
                threadID: threadID,
                title: title,
                createdAt: now,
                updatedAt: now
            ),
            at: 0
        )
        saveThreads(threads)
        saveMessages([], for: threadID)
        return threadID
    }

    func listThreads() async throws -> [ThreadSummary] {
        loadThreads()
            .sorted { $0.updatedAt > $1.updatedAt }
            .map { thread in
                ThreadSummary(
                    threadID: thread.threadID,
                    title: thread.title,
                    createdAt: thread.createdAt,
                    updatedAt: thread.updatedAt,
                    messageCount: loadMessages(for: thread.threadID).count
                )
            }
    }

    func deleteThread(threadID: String) async throws {
        var threads = loadThreads()
        threads.removeAll { $0.threadID == threadID }
        saveThreads(threads)
        defaults.removeObject(forKey: messagesKey(for: threadID))

        if threads.isEmpty {
            Self.seedDefaultStateIfNeeded(defaults: defaults)
        }
    }

    func fetchMessagesSnapshot(threadID: String) async throws -> ThreadMessagesSnapshot {
        ThreadMessagesSnapshot(messages: loadMessages(for: threadID), lastSequence: 0)
    }

    func appendUserMessage(threadID: String, content: String) async throws {
        var messages = loadMessages(for: threadID)
        let createdAt = Self.nowString()
        messages.append(
            Message(
                messageID: "msg_\(UUID().uuidString.lowercased())",
                threadID: threadID,
                role: "user",
                content: content,
                createdAt: createdAt
            )
        )
        saveMessages(messages, for: threadID)
        touchThread(threadID: threadID, updatedAt: createdAt)
    }

    func appendLocalAssistantPlaceholder(threadID: String) async throws {
        var messages = loadMessages(for: threadID)
        let createdAt = Self.nowString()
        messages.append(
            Message(
                messageID: "msg_\(UUID().uuidString.lowercased())",
                threadID: threadID,
                role: "assistant",
                content: """
                OpenClaw transport is the next implementation slice.

                This build keeps the app iOS-only, stores sessions locally, and saves your Gateway settings so the direct WebSocket client can replace this local shell cleanly.
                """,
                createdAt: createdAt
            )
        )
        saveMessages(messages, for: threadID)
        touchThread(threadID: threadID, updatedAt: createdAt)
    }

    func fetchApprovals(status: String = "pending") async throws -> [Approval] {
        let normalized = status.trimmingCharacters(in: .whitespacesAndNewlines).uppercased()
        return loadApprovals().filter { approval in
            normalized.isEmpty || approval.status.uppercased() == normalized
        }
    }

    func approve(actionID: String) async throws {
        var approvals = loadApprovals()
        guard let index = approvals.firstIndex(where: { $0.actionID == actionID }) else {
            return
        }

        let current = approvals[index]
        approvals[index] = Approval(
            actionID: current.actionID,
            source: current.source,
            sourceID: current.sourceID,
            tool: current.tool,
            status: "APPROVED",
            riskClass: current.riskClass,
            deterministicSummary: current.deterministicSummary,
            commandPreview: current.commandPreview,
            commandTimeoutMS: current.commandTimeoutMS,
            createdAt: current.createdAt,
            expiresAt: current.expiresAt
        )
        saveApprovals(approvals)
    }

    func probeGatewayConnection(baseURL: URL) async -> GatewayProbeResult {
        await probeGateway(baseURL: baseURL)
    }

    func probeGatewayAuth(baseURL: URL) async -> GatewayProbeResult {
        let result = await probeGateway(baseURL: baseURL)
        if result.code == "ok" {
            return GatewayProbeResult(
                code: "ok",
                detail: "gateway reachable; device-auth pairing still needs the direct OpenClaw client implementation"
            )
        }
        return result
    }

    private func probeGateway(baseURL: URL) async -> GatewayProbeResult {
        guard let websocketURL = websocketURL(from: baseURL) else {
            return GatewayProbeResult(code: "invalid_url", detail: "Enter a valid ws:// or wss:// Gateway URL.")
        }

        let session = URLSession(configuration: .ephemeral)
        let task = session.webSocketTask(with: websocketURL)
        task.resume()

        do {
            let message = try await withThrowingTaskGroup(of: URLSessionWebSocketTask.Message.self) { group in
                group.addTask {
                    try await task.receive()
                }
                group.addTask {
                    try await Task.sleep(nanoseconds: 5_000_000_000)
                    throw URLError(.timedOut)
                }
                guard let first = try await group.next() else {
                    throw APIError.invalidResponse
                }
                group.cancelAll()
                return first
            }

            task.cancel(with: .goingAway, reason: nil)

            switch message {
            case .string(let text):
                if text.contains("connect.challenge") || text.contains("hello-ok") {
                    return GatewayProbeResult(code: "ok", detail: "Gateway challenge received.")
                }
                return GatewayProbeResult(code: "ok", detail: "Gateway responded: \(trimmed(text))")
            case .data(let data):
                return GatewayProbeResult(code: "ok", detail: "Gateway responded with \(data.count) bytes.")
            @unknown default:
                return GatewayProbeResult(code: "ok", detail: "Gateway responded.")
            }
        } catch {
            task.cancel(with: .goingAway, reason: nil)
            if let urlError = error as? URLError {
                return GatewayProbeResult(code: urlError.code.rawValue.description, detail: urlError.localizedDescription)
            }
            return GatewayProbeResult(code: "connection_failed", detail: error.localizedDescription)
        }
    }

    private func websocketURL(from url: URL) -> URL? {
        guard var components = URLComponents(url: url, resolvingAgainstBaseURL: false) else {
            return nil
        }

        switch components.scheme?.lowercased() {
        case "http":
            components.scheme = "ws"
        case "https":
            components.scheme = "wss"
        case "ws", "wss":
            break
        default:
            return nil
        }

        return components.url
    }

    private static func seedDefaultStateIfNeeded(defaults: UserDefaults) {
        guard defaults.data(forKey: LocalStorage.threadsKey) == nil else { return }
        let now = Self.nowString()
        let main = StoredThread(
            threadID: AppConfig.primarySessionKey,
            title: "Main",
            createdAt: now,
            updatedAt: now
        )
        let messages = [
            Message(
                messageID: "msg_welcome",
                threadID: main.threadID,
                role: "system",
                content: "OpenClaw iOS shell initialized. Configure your Gateway in Settings, then use this session-first UI as the base for the direct WebSocket client.",
                createdAt: now
            )
        ]
        encode([main], forKey: LocalStorage.threadsKey, defaults: defaults)
        encode(messages, forKey: "OPENCLAW_LOCAL_MESSAGES_\(main.threadID)", defaults: defaults)
        encode([Approval](), forKey: LocalStorage.approvalsKey, defaults: defaults)
    }

    private func touchThread(threadID: String, updatedAt: String) {
        var threads = loadThreads()
        guard let index = threads.firstIndex(where: { $0.threadID == threadID }) else { return }
        threads[index].updatedAt = updatedAt
        saveThreads(threads)
    }

    private func loadThreads() -> [StoredThread] {
        decode([StoredThread].self, forKey: LocalStorage.threadsKey) ?? []
    }

    private func saveThreads(_ threads: [StoredThread]) {
        encode(threads, forKey: LocalStorage.threadsKey)
    }

    private func loadMessages(for threadID: String) -> [Message] {
        decode([Message].self, forKey: messagesKey(for: threadID)) ?? []
    }

    private func saveMessages(_ messages: [Message], for threadID: String) {
        encode(messages, forKey: messagesKey(for: threadID))
    }

    private func loadApprovals() -> [Approval] {
        decode([Approval].self, forKey: LocalStorage.approvalsKey) ?? []
    }

    private func saveApprovals(_ approvals: [Approval]) {
        encode(approvals, forKey: LocalStorage.approvalsKey)
    }

    private func messagesKey(for threadID: String) -> String {
        "OPENCLAW_LOCAL_MESSAGES_\(threadID)"
    }

    private func encode<T: Encodable>(_ value: T, forKey key: String) {
        Self.encode(value, forKey: key, defaults: defaults)
    }

    private func decode<T: Decodable>(_ type: T.Type, forKey key: String) -> T? {
        guard let data = defaults.data(forKey: key) else { return nil }
        return try? JSONDecoder().decode(type, from: data)
    }

    private static func encode<T: Encodable>(_ value: T, forKey key: String, defaults: UserDefaults) {
        guard let data = try? JSONEncoder().encode(value) else { return }
        defaults.set(data, forKey: key)
    }

    private static func nowString() -> String {
        timestampFormatter.string(from: Date())
    }

    private func trimmed(_ value: String) -> String {
        let compact = value.trimmingCharacters(in: .whitespacesAndNewlines)
        if compact.count <= 120 {
            return compact
        }
        return String(compact.prefix(117)) + "..."
    }
}
