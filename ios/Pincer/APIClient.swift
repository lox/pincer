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

struct GatewayChatSendReceipt: Sendable, Equatable {
    let runID: String
    let status: String
}

func extractConnectChallengeNonce(from frame: [String: Any]) -> String? {
    guard isGatewayEventFrame(frame),
          frame["event"] as? String == "connect.challenge",
          let payload = frame["payload"] as? [String: Any] else {
        return nil
    }

    let nonce = (payload["nonce"] as? String)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    return nonce.isEmpty ? nil : nonce
}

func isGatewayEventFrame(_ frame: [String: Any]) -> Bool {
    guard let type = frame["type"] as? String else {
        return false
    }

    switch type.lowercased() {
    case "evt", "event":
        return true
    default:
        return false
    }
}

func mapGatewaySessionsPayload(_ payload: Any?) throws -> [ThreadSummary] {
    guard let object = payload as? [String: Any],
          let rows = object["sessions"] as? [Any] else {
        throw APIError.invalidResponse
    }

    let primarySessionKey = AppConfig.primarySessionKey

    return rows.compactMap { raw in
        guard let row = raw as? [String: Any],
              let threadID = gatewayTrimmedString(row["key"] as? String) else {
            return nil
        }

        let isPrimarySession = sessionKeyMatchesPrimary(
            threadID,
            primarySessionKey: primarySessionKey
        )
        let displayName = sanitizedGatewaySessionTitle(row["displayName"] as? String)
        let label = sanitizedGatewaySessionTitle(row["label"] as? String)
        let derivedTitle = sanitizedGatewaySessionTitle(row["derivedTitle"] as? String)
        let titleCandidates = isPrimarySession ? [
            fallbackTitle(for: threadID, primarySessionKey: primarySessionKey),
            displayName,
            label,
            derivedTitle,
        ] : [
            displayName,
            label,
            derivedTitle,
            fallbackTitle(for: threadID, primarySessionKey: primarySessionKey),
        ]

        let title = firstNonEmptyGatewayString(titleCandidates) ?? threadID

        return ThreadSummary(
            threadID: threadID,
            title: title,
            createdAt: gatewayISO8601String(from: row["startedAt"]) ?? "",
            updatedAt: gatewayISO8601String(from: row["updatedAt"]) ?? "",
            messageCount: 0
        )
    }
    .sorted { $0.updatedAt > $1.updatedAt }
}

func mapGatewayChatHistoryPayload(_ payload: Any?, threadID: String) throws -> ThreadMessagesSnapshot {
    guard let object = payload as? [String: Any],
          let rows = object["messages"] as? [Any] else {
        throw APIError.invalidResponse
    }

    var lastSequence: UInt64 = 0
    let messages = rows.enumerated().compactMap { index, raw -> Message? in
        guard let row = raw as? [String: Any] else {
            return nil
        }

        if let seq = gatewayMessageSequence(from: row) {
            lastSequence = max(lastSequence, seq)
        }

        let role = gatewayTrimmedString(row["role"] as? String)?.lowercased() ?? "system"
        guard !gatewayShouldOmitHistoryMessage(row, role: role) else {
            return nil
        }
        guard let content = sanitizeGatewayRenderableText(gatewayMessageText(from: row), role: role) else {
            return nil
        }

        return Message(
            messageID: gatewayMessageID(from: row) ?? "msg_\(index + 1)",
            threadID: threadID,
            role: role,
            content: content,
            createdAt: gatewayISO8601String(from: row["timestamp"]) ?? ""
        )
    }

    return ThreadMessagesSnapshot(messages: messages, lastSequence: lastSequence)
}

private func gatewayTrimmedString(_ value: String?) -> String? {
    let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    return trimmed.isEmpty ? nil : trimmed
}

private func firstNonEmptyGatewayString(_ values: [String?]) -> String? {
    values.compactMap(gatewayTrimmedString).first
}

private func sanitizedGatewaySessionTitle(_ value: String?) -> String? {
    guard var title = sanitizeGatewayRenderableText(value ?? "", role: "user")
            ?? sanitizeGatewayRenderableText(value ?? "", role: "system") else {
        return nil
    }

    title = title.replacingOccurrences(
        of: #"\s+"#,
        with: " ",
        options: .regularExpression
    )

    return gatewayTrimmedString(title)
}

private func fallbackTitle(for threadID: String, primarySessionKey: String) -> String {
    if sessionKeyMatchesPrimary(threadID, primarySessionKey: primarySessionKey) {
        return "Main"
    }

    return threadID
}

private func gatewayISO8601String(from value: Any?) -> String? {
    let timestamp: TimeInterval?
    switch value {
    case let number as NSNumber:
        let raw = number.doubleValue
        timestamp = raw > 10_000_000_000 ? raw / 1_000 : raw
    case let string as String:
        if let numeric = Double(string) {
            timestamp = numeric > 10_000_000_000 ? numeric / 1_000 : numeric
        } else if let date = ISO8601DateFormatter().date(from: string) {
            timestamp = date.timeIntervalSince1970
        } else {
            timestamp = nil
        }
    default:
        timestamp = nil
    }

    guard let timestamp else {
        return nil
    }

    return ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: timestamp))
}

private func gatewayMessageSequence(from row: [String: Any]) -> UInt64? {
    guard let meta = row["__openclaw"] as? [String: Any] else {
        return nil
    }

    switch meta["seq"] {
    case let value as NSNumber:
        return value.uint64Value
    case let value as UInt64:
        return value
    case let value as Int:
        return value >= 0 ? UInt64(value) : nil
    default:
        return nil
    }
}

private func gatewayMessageID(from row: [String: Any]) -> String? {
    if let id = gatewayTrimmedString(row["id"] as? String) {
        return id
    }

    guard let meta = row["__openclaw"] as? [String: Any] else {
        return nil
    }
    return gatewayTrimmedString(meta["id"] as? String)
}

private func gatewayMessageText(from row: [String: Any]) -> String {
    if let content = gatewayTrimmedString(row["content"] as? String) {
        return content
    }

    if let blocks = gatewayMessageBlocks(from: row) {
        let textBlocks = blocks.compactMap { item -> String? in
            guard gatewayTrimmedString(item["type"] as? String) == "text" else {
                return nil
            }
            return gatewayTrimmedString(item["text"] as? String)
        }

        if !textBlocks.isEmpty {
            return textBlocks.joined(separator: "\n\n")
        }

        if !blocks.isEmpty {
            return "[Attachment]"
        }
    }

    if let text = gatewayTrimmedString(row["text"] as? String) {
        return text
    }

    return "[Message]"
}

private func gatewayMessageBlocks(from row: [String: Any]) -> [[String: Any]]? {
    guard let blocks = row["content"] as? [Any] else {
        return nil
    }

    let mapped = blocks.compactMap { $0 as? [String: Any] }
    return mapped.isEmpty ? nil : mapped
}

private func gatewayShouldOmitHistoryMessage(_ row: [String: Any], role: String) -> Bool {
    switch role {
    case "toolresult", "tool_result":
        return true
    case "assistant":
        guard let blocks = gatewayMessageBlocks(from: row) else {
            return false
        }

        let blockTypes = blocks.compactMap { gatewayTrimmedString($0["type"] as? String)?.lowercased() }
        return !blockTypes.isEmpty && blockTypes.allSatisfy { $0 == "toolcall" || $0 == "tool_call" }
    default:
        return false
    }
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

private struct GatewayProbeFailure: Error {
    let code: String
    let detail: String
}

private struct GatewayConnectResult {
    let authRole: String
}

actor APIClient {
    private var baseURL: URL
    private var token: String
    private let defaults = UserDefaults.standard
    private let isUITestMode: Bool
    private let liveConnection: OpenClawGatewayConnection?

    private static let timestampFormatter: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter
    }()

    init(baseURL: URL, token: String) {
        self.baseURL = baseURL
        self.token = token
        self.isUITestMode = AppConfig.isUITestMode
        self.liveConnection = AppConfig.isUITestMode ? nil : OpenClawGatewayConnection(
            baseURL: baseURL,
            gatewayToken: token
        )
        Self.seedDefaultStateIfNeeded(defaults: defaults)
    }

    func setBaseURL(_ baseURL: URL) async {
        self.baseURL = baseURL
        AppConfig.setBaseURL(baseURL)
        await liveConnection?.configure(baseURL: baseURL, gatewayToken: token)
    }

    func setGatewayToken(_ token: String) async {
        self.token = token.trimmingCharacters(in: .whitespacesAndNewlines)
        AppConfig.setBearerToken(self.token)
        await liveConnection?.configure(baseURL: baseURL, gatewayToken: self.token)
    }

    func setPrimarySessionKey(_ sessionKey: String) {
        AppConfig.setPrimarySessionKey(sessionKey)
    }

    func createThread() async throws -> String {
        if isUITestMode {
            return createLocalThread()
        }

        let payload = try await authenticatedGatewayRequestPayload(
            method: "sessions.create",
            params: [
                "label": "New Session",
            ]
        )

        guard let object = payload as? [String: Any],
              let threadID = trimmedOrNil(object["key"] as? String) else {
            throw APIError.invalidResponse
        }

        return threadID
    }

    func listThreads() async throws -> [ThreadSummary] {
        if isUITestMode {
            return listLocalThreads()
        }

        let payload = try await authenticatedGatewayRequestPayload(
            method: "sessions.list",
            params: [
                "limit": 100,
                "includeDerivedTitles": true,
                "includeLastMessage": true,
                "includeGlobal": false,
                "includeUnknown": false,
            ]
        )

        return try mapGatewaySessionsPayload(payload)
    }

    func deleteThread(threadID: String) async throws {
        if isUITestMode {
            try deleteLocalThread(threadID: threadID)
            return
        }

        _ = try await authenticatedGatewayRequestPayload(
            method: "sessions.delete",
            params: [
                "key": threadID,
            ]
        )
    }

    func fetchMessagesSnapshot(threadID: String) async throws -> ThreadMessagesSnapshot {
        if isUITestMode {
            return fetchLocalMessagesSnapshot(threadID: threadID)
        }

        let payload = try await authenticatedGatewayRequestPayload(
            method: "chat.history",
            params: [
                "sessionKey": threadID,
                "limit": 200,
            ]
        )

        return try mapGatewayChatHistoryPayload(payload, threadID: threadID)
    }

    func sendMessage(threadID: String, content: String) async throws -> GatewayChatSendReceipt {
        if isUITestMode {
            sendLocalMessage(threadID: threadID, content: content)
            return GatewayChatSendReceipt(runID: UUID().uuidString.lowercased(), status: "completed")
        }

        let payload = try await authenticatedGatewayRequestPayload(
            method: "chat.send",
            params: [
                "sessionKey": threadID,
                "message": content,
                "idempotencyKey": UUID().uuidString.lowercased(),
            ]
        )

        guard let object = payload as? [String: Any],
              let runID = trimmedOrNil(object["runId"] as? String),
              let status = trimmedOrNil(object["status"] as? String) else {
            throw APIError.invalidResponse
        }

        return GatewayChatSendReceipt(runID: runID, status: status)
    }

    func abortMessageRun(threadID: String, runID: String? = nil) async throws {
        if isUITestMode {
            return
        }

        var params: [String: Any] = [
            "sessionKey": threadID,
        ]
        if let runID = trimmedOrNil(runID) {
            params["runId"] = runID
        }

        _ = try await authenticatedGatewayRequestPayload(
            method: "chat.abort",
            params: params
        )
    }

    func gatewayEvents() async -> AsyncStream<GatewayConnectionEvent> {
        guard !isUITestMode, let liveConnection else {
            return AsyncStream { continuation in
                continuation.finish()
            }
        }

        return await liveConnection.subscribe()
    }

    func startLiveGatewayConnection() async {
        guard !isUITestMode else {
            return
        }

        await liveConnection?.start()
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

    func probeGatewayAuth(baseURL: URL, gatewayToken overrideToken: String? = nil) async -> GatewayProbeResult {
        await authenticatedGatewayProbe(
            baseURL: baseURL,
            gatewayToken: overrideToken ?? token
        )
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

    private func authenticatedGatewayProbe(baseURL: URL, gatewayToken: String) async -> GatewayProbeResult {
        do {
            let detail = try await authenticatedSessionProbeDetail(
                baseURL: baseURL,
                gatewayToken: gatewayToken
            )
            return GatewayProbeResult(code: "ok", detail: detail)
        } catch let error as GatewayProbeFailure {
            return GatewayProbeResult(code: error.code, detail: error.detail)
        } catch {
            if let urlError = error as? URLError {
                return GatewayProbeResult(code: urlError.code.rawValue.description, detail: urlError.localizedDescription)
            }
            return GatewayProbeResult(code: "auth_failed", detail: error.localizedDescription)
        }
    }

    private func authenticatedSessionProbeDetail(baseURL: URL, gatewayToken: String) async throws -> String {
        guard let websocketURL = websocketURL(from: baseURL) else {
            throw GatewayProbeFailure(code: "invalid_url", detail: "Enter a valid ws:// or wss:// Gateway URL.")
        }

        let session = URLSession(configuration: .ephemeral)
        let task = session.webSocketTask(with: websocketURL)
        task.resume()

        defer {
            task.cancel(with: .goingAway, reason: nil)
            session.invalidateAndCancel()
        }

        guard let challengeFrame = try await receiveJSONObject(from: task) else {
            throw GatewayProbeFailure(code: "invalid_response", detail: "Gateway challenge was not valid JSON.")
        }

        guard let nonce = extractConnectChallengeNonce(from: challengeFrame) else {
            throw GatewayProbeFailure(code: "challenge_missing", detail: "Gateway did not send a usable connect.challenge nonce.")
        }

        let connect = try await performAuthenticatedConnect(
            on: task,
            nonce: nonce,
            gatewayToken: gatewayToken
        )

        let sessionsRequestID = UUID().uuidString.lowercased()
        try sendJSONObject(
            [
                "type": "req",
                "id": sessionsRequestID,
                "method": "sessions.list",
                "params": [:],
            ],
            on: task
        )

        let sessionsPayload = try await awaitResponsePayload(
            for: sessionsRequestID,
            on: task
        )
        let sessionCount = ((sessionsPayload as? [String: Any])?["sessions"] as? [Any])?.count

        if let sessionCount {
            return "Authenticated as \(connect.authRole). sessions.list returned \(sessionCount) session(s)."
        }
        return "Authenticated as \(connect.authRole). sessions.list returned successfully."
    }

    private func authenticatedGatewayRequestPayload(
        method: String,
        params: [String: Any]
    ) async throws -> Any? {
        if let liveConnection {
            return try await liveConnection.request(method: method, params: params).value
        }

        return try await authenticatedGatewayRequestPayload(
            baseURL: baseURL,
            gatewayToken: token,
            method: method,
            params: params
        )
    }

    private func authenticatedGatewayRequestPayload(
        baseURL: URL,
        gatewayToken: String,
        method: String,
        params: [String: Any]
    ) async throws -> Any? {
        guard let websocketURL = websocketURL(from: baseURL) else {
            throw GatewayProbeFailure(code: "invalid_url", detail: "Enter a valid ws:// or wss:// Gateway URL.")
        }

        let session = URLSession(configuration: .ephemeral)
        let task = session.webSocketTask(with: websocketURL)
        task.resume()

        defer {
            task.cancel(with: .goingAway, reason: nil)
            session.invalidateAndCancel()
        }

        guard let challengeFrame = try await receiveJSONObject(from: task) else {
            throw GatewayProbeFailure(code: "invalid_response", detail: "Gateway challenge was not valid JSON.")
        }

        guard let nonce = extractConnectChallengeNonce(from: challengeFrame) else {
            throw GatewayProbeFailure(code: "challenge_missing", detail: "Gateway did not send a usable connect.challenge nonce.")
        }

        _ = try await performAuthenticatedConnect(
            on: task,
            nonce: nonce,
            gatewayToken: gatewayToken
        )

        let requestID = UUID().uuidString.lowercased()
        try sendJSONObject(
            [
                "type": "req",
                "id": requestID,
                "method": method,
                "params": params,
            ],
            on: task
        )

        return try await awaitResponsePayload(
            for: requestID,
            on: task
        )
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

    private func performAuthenticatedConnect(
        on task: URLSessionWebSocketTask,
        nonce: String,
        gatewayToken: String
    ) async throws -> GatewayConnectResult {
        let credentialStore = AppConfig.credentialStore
        let identity = try GatewayDeviceIdentity.loadOrCreate(from: credentialStore)
        let storedDeviceToken = credentialStore.deviceToken(
            deviceID: identity.deviceID,
            role: GatewayClientProfile.role
        )?.token
        let authSelection = selectGatewayConnectAuth(
            bearerToken: gatewayToken,
            storedDeviceToken: storedDeviceToken
        )
        let device = try identity.makeConnectDevice(
            nonce: nonce,
            role: GatewayClientProfile.role,
            scopes: GatewayClientProfile.scopes,
            token: authSelection.signatureToken
        )
        let connectRequestID = UUID().uuidString.lowercased()
        var connectParams: [String: Any] = [
            "minProtocol": 3,
            "maxProtocol": 3,
            "client": [
                "id": GatewayClientProfile.clientID,
                "displayName": GatewayClientProfile.displayName,
                "version": GatewayClientProfile.clientVersion,
                "platform": GatewayClientProfile.platform,
                "deviceFamily": GatewayClientProfile.deviceFamily,
                "mode": GatewayClientProfile.clientMode,
            ],
            "caps": [],
            "role": GatewayClientProfile.role,
            "scopes": GatewayClientProfile.scopes,
            "device": device.jsonObject,
        ]

        var connectAuth: [String: Any] = [:]
        if let token = authSelection.token {
            connectAuth["token"] = token
        }
        if let deviceToken = authSelection.deviceToken {
            connectAuth["deviceToken"] = deviceToken
        }
        if !connectAuth.isEmpty {
            connectParams["auth"] = connectAuth
        }

        try sendJSONObject(
            [
                "type": "req",
                "id": connectRequestID,
                "method": "connect",
                "params": connectParams,
            ],
            on: task
        )

        let connectPayload = try await awaitResponsePayload(
            for: connectRequestID,
            on: task
        )

        guard let hello = connectPayload as? [String: Any],
              hello["type"] as? String == "hello-ok" else {
            throw GatewayProbeFailure(code: "invalid_response", detail: "Gateway connect response was not hello-ok.")
        }

        let authRole = storeDeviceTokenIfPresent(
            from: hello["auth"] as? [String: Any],
            deviceID: identity.deviceID
        ) ?? GatewayClientProfile.role

        return GatewayConnectResult(authRole: authRole)
    }

    private func storeDeviceTokenIfPresent(from auth: [String: Any]?, deviceID: String) -> String? {
        guard let auth,
              let deviceToken = trimmedOrNil(auth["deviceToken"] as? String) else {
            return nil
        }

        let role = trimmedOrNil(auth["role"] as? String) ?? GatewayClientProfile.role
        let scopes = (auth["scopes"] as? [String]) ?? []
        AppConfig.credentialStore.setDeviceToken(
            GatewayDeviceTokenRecord(
                deviceID: deviceID,
                role: role,
                token: deviceToken,
                scopes: scopes,
                updatedAtMS: Int64(Date().timeIntervalSince1970 * 1_000)
            )
        )
        return role
    }

    private func awaitResponsePayload(
        for requestID: String,
        on task: URLSessionWebSocketTask
    ) async throws -> Any? {
        while true {
            guard let frame = try await receiveJSONObject(from: task) else {
                throw GatewayProbeFailure(code: "invalid_response", detail: "Gateway returned a non-JSON frame.")
            }

            if isGatewayEventFrame(frame) {
                continue
            }

            guard frame["type"] as? String == "res",
                  frame["id"] as? String == requestID else {
                continue
            }

            if let ok = frame["ok"] as? Bool, ok {
                return frame["payload"]
            }

            let error = frame["error"] as? [String: Any]
            let detailCode = trimmedOrNil(error?["details"].flatMap { ($0 as? [String: Any])?["code"] as? String })
            let requestIDDetail = trimmedOrNil(error?["details"].flatMap { ($0 as? [String: Any])?["requestId"] as? String })
            let reason = trimmedOrNil(error?["details"].flatMap { ($0 as? [String: Any])?["reason"] as? String })
            let message = trimmedOrNil(error?["message"] as? String) ?? "Gateway request failed."

            var detail = message
            if let detailCode, detailCode == "PAIRING_REQUIRED" {
                if let requestIDDetail {
                    detail = "Pairing required. Approve request \(requestIDDetail) in OpenClaw, then check again."
                } else {
                    detail = "Pairing required. Approve the device in OpenClaw, then check again."
                }
                throw GatewayProbeFailure(code: "pairing_required", detail: detail)
            }

            if let detailCode {
                detail += " (\(detailCode))"
            }
            if let reason {
                detail += ": \(reason)"
            }

            throw GatewayProbeFailure(
                code: detailCode ?? trimmedOrNil(error?["code"] as? String) ?? "request_failed",
                detail: detail
            )
        }
    }

    private func receiveJSONObject(
        from task: URLSessionWebSocketTask,
        timeoutNanoseconds: UInt64 = 5_000_000_000
    ) async throws -> [String: Any]? {
        let message = try await receiveMessage(
            from: task,
            timeoutNanoseconds: timeoutNanoseconds
        )

        let data: Data
        switch message {
        case .string(let text):
            data = Data(text.utf8)
        case .data(let binary):
            data = binary
        @unknown default:
            return nil
        }

        guard let object = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return nil
        }
        return object
    }

    private func sendJSONObject(_ object: [String: Any], on task: URLSessionWebSocketTask) throws {
        let data = try JSONSerialization.data(withJSONObject: object)
        guard let text = String(data: data, encoding: .utf8) else {
            throw GatewayProbeFailure(code: "invalid_request", detail: "Failed to encode Gateway request.")
        }
        task.send(.string(text)) { error in
            if let error {
                NSLog("Pincer Gateway send failed: %@", error.localizedDescription)
            }
        }
    }

    private func receiveMessage(
        from task: URLSessionWebSocketTask,
        timeoutNanoseconds: UInt64
    ) async throws -> URLSessionWebSocketTask.Message {
        try await withThrowingTaskGroup(of: URLSessionWebSocketTask.Message.self) { group in
            group.addTask {
                try await task.receive()
            }
            group.addTask {
                try await Task.sleep(nanoseconds: timeoutNanoseconds)
                throw URLError(.timedOut)
            }

            guard let first = try await group.next() else {
                throw APIError.invalidResponse
            }
            group.cancelAll()
            return first
        }
    }

    private func trimmedOrNil(_ value: String?) -> String? {
        let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? nil : trimmed
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

    private func createLocalThread() -> String {
        let now = Self.nowString()
        let threadID = "session_\(UUID().uuidString.lowercased())"
        var threads = loadThreads()
        threads.insert(
            StoredThread(
                threadID: threadID,
                title: "New Session",
                createdAt: now,
                updatedAt: now
            ),
            at: 0
        )
        saveThreads(threads)
        saveMessages([], for: threadID)
        return threadID
    }

    private func listLocalThreads() -> [ThreadSummary] {
        loadThreads()
            .map { thread in
                ThreadSummary(
                    threadID: thread.threadID,
                    title: thread.title,
                    createdAt: thread.createdAt,
                    updatedAt: thread.updatedAt,
                    messageCount: loadMessages(for: thread.threadID).count
                )
            }
            .sorted { $0.updatedAt > $1.updatedAt }
    }

    private func deleteLocalThread(threadID: String) throws {
        guard !sessionKeyMatchesPrimary(threadID) else {
            throw APIError.rpc("Cannot delete the primary session.")
        }

        let remaining = loadThreads().filter { $0.threadID != threadID }
        saveThreads(remaining)
        defaults.removeObject(forKey: messagesKey(for: threadID))
    }

    private func fetchLocalMessagesSnapshot(threadID: String) -> ThreadMessagesSnapshot {
        let messages = loadMessages(for: threadID)
        return ThreadMessagesSnapshot(messages: messages, lastSequence: UInt64(messages.count))
    }

    private func sendLocalMessage(threadID: String, content: String) {
        let now = Self.nowString()
        var messages = loadMessages(for: threadID)
        messages.append(
            Message(
                messageID: "msg_\(UUID().uuidString.lowercased())",
                threadID: threadID,
                role: "user",
                content: content,
                createdAt: now
            )
        )
        messages.append(
            Message(
                messageID: "msg_\(UUID().uuidString.lowercased())",
                threadID: threadID,
                role: "assistant",
                content: "UI test mode is using deterministic local session data instead of a live OpenClaw Gateway.",
                createdAt: now
            )
        )
        saveMessages(messages, for: threadID)
        touchThread(threadID: threadID, updatedAt: now)
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
