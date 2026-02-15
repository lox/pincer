import Foundation
import Connect
import SwiftProtobuf

enum APIError: Error {
    case invalidResponse
    case rpc(String)
    case unauthorized
}

struct ThreadMessagesSnapshot {
    let messages: [Message]
    let lastSequence: UInt64
}

actor APIClient {
    private var baseURL: URL
    private var token: String
    private var authClient: Pincer_Protocol_V1_AuthServiceClient
    private var threadsClient: Pincer_Protocol_V1_ThreadsServiceClient
    private var turnsClient: Pincer_Protocol_V1_TurnsServiceClient
    private var eventsClient: Pincer_Protocol_V1_EventsServiceClient
    private var approvalsClient: Pincer_Protocol_V1_ApprovalsServiceClient
    private var devicesClient: Pincer_Protocol_V1_DevicesServiceClient

    private static let timestampFormatter: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter
    }()

    init(baseURL: URL, token: String) {
        self.baseURL = baseURL
        self.token = token

        let transport = Self.makeTransport(baseURL: baseURL)
        self.authClient = Pincer_Protocol_V1_AuthServiceClient(client: transport)
        self.threadsClient = Pincer_Protocol_V1_ThreadsServiceClient(client: transport)
        self.turnsClient = Pincer_Protocol_V1_TurnsServiceClient(client: transport)
        self.eventsClient = Pincer_Protocol_V1_EventsServiceClient(client: transport)
        self.approvalsClient = Pincer_Protocol_V1_ApprovalsServiceClient(client: transport)
        self.devicesClient = Pincer_Protocol_V1_DevicesServiceClient(client: transport)

        AppConfig.setBaseURL(baseURL)
    }

    func setBaseURL(_ baseURL: URL) {
        guard self.baseURL.absoluteString != baseURL.absoluteString else { return }

        self.baseURL = baseURL
        AppConfig.setBaseURL(baseURL)

        let transport = Self.makeTransport(baseURL: baseURL)
        self.authClient = Pincer_Protocol_V1_AuthServiceClient(client: transport)
        self.threadsClient = Pincer_Protocol_V1_ThreadsServiceClient(client: transport)
        self.turnsClient = Pincer_Protocol_V1_TurnsServiceClient(client: transport)
        self.eventsClient = Pincer_Protocol_V1_EventsServiceClient(client: transport)
        self.approvalsClient = Pincer_Protocol_V1_ApprovalsServiceClient(client: transport)
        self.devicesClient = Pincer_Protocol_V1_DevicesServiceClient(client: transport)

        clearToken()
    }

    func ensurePaired(force: Bool = false, deviceName: String = "Pincer iOS") async throws {
        if !force, !token.isEmpty {
            return
        }

        let codeResponse = await authClient.createPairingCode(
            request: .init(),
            headers: [:]
        )
        let codeMessage = try responseMessage(codeResponse)

        var bindRequest = Pincer_Protocol_V1_BindPairingCodeRequest()
        bindRequest.code = codeMessage.code
        bindRequest.deviceName = deviceName
        let bindResponse = await authClient.bindPairingCode(
            request: bindRequest,
            headers: [:]
        )
        let bindMessage = try responseMessage(bindResponse)

        token = bindMessage.token
        UserDefaults.standard.set(bindMessage.token, forKey: AppConfig.tokenDefaultsKey)
    }

    func createThread() async throws -> String {
        try await withAuthorizedRetry {
            let response = await threadsClient.createThread(
                request: .init(),
                headers: authHeaders()
            )
            let message = try responseMessage(response)
            return message.threadID
        }
    }

    func sendMessage(threadID: String, content: String) async throws {
        try await withAuthorizedRetry {
            var request = Pincer_Protocol_V1_SendTurnRequest()
            request.threadID = threadID
            request.userText = content
            request.triggerType = .chatMessage

            let response = await turnsClient.sendTurn(
                request: request,
                headers: authHeaders()
            )
            _ = try responseMessage(response) as Pincer_Protocol_V1_SendTurnResponse
        }
    }

    func fetchMessages(threadID: String) async throws -> [Message] {
        let snapshot = try await fetchMessagesSnapshot(threadID: threadID)
        return snapshot.messages
    }

    func fetchMessagesSnapshot(threadID: String) async throws -> ThreadMessagesSnapshot {
        try await withAuthorizedRetry {
            var request = Pincer_Protocol_V1_ListThreadMessagesRequest()
            request.threadID = threadID

            let response = await threadsClient.listThreadMessages(
                request: request,
                headers: authHeaders()
            )
            let message = try responseMessage(response)
            let items = message.items.map { item in
                Message(
                    messageID: item.messageID,
                    threadID: threadID,
                    role: item.role,
                    content: item.content,
                    createdAt: timestampString(item.createdAt, hasValue: item.hasCreatedAt)
                )
            }
            return ThreadMessagesSnapshot(messages: items, lastSequence: message.lastSequence)
        }
    }

    func startTurnStream(
        threadID: String,
        content: String,
        clientMessageID: String,
        resumeFromSequence: UInt64,
        onEvent: @escaping (Pincer_Protocol_V1_ThreadEvent) async -> Void
    ) async throws {
        try await withAuthorizedRetry {
            var request = Pincer_Protocol_V1_StartTurnRequest()
            request.threadID = threadID
            request.userText = content
            request.clientMessageID = clientMessageID
            request.triggerType = .chatMessage
            request.reasoningVisibility = .reasoningSummary
            request.resumeFromSequence = resumeFromSequence

            let stream = turnsClient.startTurn(headers: authHeaders())
            try await consumeThreadEventStream(
                stream: stream,
                request: request,
                onEvent: onEvent
            )
        }
    }

    func watchThreadStream(
        threadID: String,
        fromSequence: UInt64,
        onEvent: @escaping (Pincer_Protocol_V1_ThreadEvent) async -> Void
    ) async throws {
        try await withAuthorizedRetry {
            var request = Pincer_Protocol_V1_WatchThreadRequest()
            request.threadID = threadID
            request.fromSequence = fromSequence

            let stream = eventsClient.watchThread(headers: authHeaders())
            try await consumeThreadEventStream(
                stream: stream,
                request: request,
                onEvent: onEvent
            )
        }
    }

    func fetchApprovals(status: String = "pending") async throws -> [Approval] {
        try await withAuthorizedRetry {
            var request = Pincer_Protocol_V1_ListApprovalsRequest()
            request.status = actionStatus(status)

            let response = await approvalsClient.listApprovals(
                request: request,
                headers: authHeaders()
            )
            let message = try responseMessage(response)
            return message.items.map { item in
                let deterministicSummary = item.deterministicSummary.trimmingCharacters(in: .whitespacesAndNewlines)
                let preview = item.hasPreview ? item.preview : nil
                return Approval(
                    actionID: item.actionID,
                    source: item.source,
                    sourceID: item.sourceID,
                    tool: item.tool,
                    status: actionStatusName(item.status),
                    riskClass: riskClassName(item.riskClass),
                    deterministicSummary: deterministicSummary,
                    commandPreview: approvalCommandPreview(tool: item.tool, preview: preview),
                    commandTimeoutMS: approvalCommandTimeoutMS(tool: item.tool, preview: preview),
                    createdAt: timestampString(item.createdAt, hasValue: item.hasCreatedAt),
                    expiresAt: timestampString(item.expiresAt, hasValue: item.hasExpiresAt)
                )
            }
        }
    }

    func approve(actionID: String) async throws {
        try await withAuthorizedRetry {
            var request = Pincer_Protocol_V1_ApproveActionRequest()
            request.actionID = actionID
            let response = await approvalsClient.approveAction(
                request: request,
                headers: authHeaders()
            )
            _ = try responseMessage(response) as Pincer_Protocol_V1_ApproveActionResponse
        }
    }

    func fetchDevices() async throws -> [Device] {
        try await withAuthorizedRetry {
            let response = await devicesClient.listDevices(
                request: .init(),
                headers: authHeaders()
            )
            let message = try responseMessage(response)
            return message.items.map { item in
                Device(
                    deviceID: item.deviceID,
                    name: item.name,
                    revokedAt: timestampString(item.revokedAt, hasValue: item.hasRevokedAt),
                    createdAt: timestampString(item.createdAt, hasValue: item.hasCreatedAt),
                    isCurrent: item.isCurrent
                )
            }
        }
    }

    func revokeDevice(deviceID: String) async throws {
        try await withAuthorizedRetry {
            var request = Pincer_Protocol_V1_RevokeDeviceRequest()
            request.deviceID = deviceID
            let response = await devicesClient.revokeDevice(
                request: request,
                headers: authHeaders()
            )
            _ = try responseMessage(response) as Pincer_Protocol_V1_RevokeDeviceResponse
        }
    }

    private func withAuthorizedRetry<T>(_ operation: () async throws -> T) async throws -> T {
        try await ensurePaired()
        do {
            return try await operation()
        } catch APIError.unauthorized {
            try await ensurePaired(force: true)
            return try await operation()
        }
    }

    private func consumeThreadEventStream<Request: SwiftProtobuf.Message>(
        stream: any Connect.ServerOnlyAsyncStreamInterface<Request, Pincer_Protocol_V1_ThreadEvent>,
        request: Request,
        onEvent: @escaping (Pincer_Protocol_V1_ThreadEvent) async -> Void
    ) async throws {
        do {
            try stream.send(request)
        } catch {
            throw streamAPIError(code: nil, error: error)
        }

        var sawCompletion = false
        for await result in stream.results() {
            switch result {
            case .headers:
                continue
            case .message(let event):
                await onEvent(event)
            case .complete(let code, let error, _):
                sawCompletion = true
                if code == .ok {
                    return
                }
                throw streamAPIError(code: code, error: error)
            }
        }

        if !sawCompletion {
            throw APIError.invalidResponse
        }
    }

    private func streamAPIError(code: Connect.Code?, error: Error?) -> APIError {
        if code == .unauthenticated {
            clearToken()
            return .unauthorized
        }
        if let connectError = error as? Connect.ConnectError {
            if connectError.code == .unauthenticated {
                clearToken()
                return .unauthorized
            }
            return .rpc(connectError.code.name)
        }
        if let code {
            return .rpc(code.name)
        }
        return .invalidResponse
    }

    private static func makeTransport(baseURL: URL) -> ProtocolClient {
        let host = baseURL.absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        return ProtocolClient(config: .init(
            host: host,
            networkProtocol: .connect,
            codec: JSONCodec()
        ))
    }

    private func authHeaders() -> Connect.Headers {
        var headers: Connect.Headers = [:]
        if !token.isEmpty {
            headers["Authorization"] = ["Bearer \(token)"]
        }
        return headers
    }

    private func responseMessage<T: SwiftProtobuf.Message>(_ response: ResponseMessage<T>) throws -> T {
        if let message = response.message {
            return message
        }

        if let rpcError = response.error {
            if rpcError.code == .unauthenticated {
                clearToken()
                throw APIError.unauthorized
            }
            throw APIError.rpc(rpcError.code.name)
        }

        if response.code == .unauthenticated {
            clearToken()
            throw APIError.unauthorized
        }

        if response.code != .ok {
            throw APIError.rpc(response.code.name)
        }

        throw APIError.invalidResponse
    }

    private func timestampString(_ timestamp: SwiftProtobuf.Google_Protobuf_Timestamp, hasValue: Bool) -> String {
        guard hasValue else {
            return ""
        }

        let seconds = TimeInterval(timestamp.seconds)
        let nanos = TimeInterval(timestamp.nanos) / 1_000_000_000
        let date = Date(timeIntervalSince1970: seconds + nanos)
        return Self.timestampFormatter.string(from: date)
    }

    private func actionStatus(_ value: String) -> Pincer_Protocol_V1_ActionStatus {
        switch value.uppercased() {
        case "PENDING":
            return .pending
        case "APPROVED":
            return .approved
        case "REJECTED":
            return .rejected
        case "EXECUTED":
            return .executed
        default:
            return .unspecified
        }
    }

    private func actionStatusName(_ value: Pincer_Protocol_V1_ActionStatus) -> String {
        switch value {
        case .pending:
            return "PENDING"
        case .approved:
            return "APPROVED"
        case .rejected:
            return "REJECTED"
        case .executed:
            return "EXECUTED"
        case .unspecified, .UNRECOGNIZED:
            return "UNSPECIFIED"
        }
    }

    private func riskClassName(_ value: Pincer_Protocol_V1_RiskClass) -> String {
        switch value {
        case .read:
            return "READ"
        case .write:
            return "WRITE"
        case .exfiltration:
            return "EXFILTRATION"
        case .destructive:
            return "DESTRUCTIVE"
        case .high:
            return "HIGH"
        case .unspecified, .UNRECOGNIZED:
            return "UNSPECIFIED"
        }
    }

    private func clearToken() {
        token = ""
        UserDefaults.standard.removeObject(forKey: AppConfig.tokenDefaultsKey)
    }
}
