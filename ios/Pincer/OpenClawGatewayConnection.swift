import Foundation

struct GatewayJSONPayload: @unchecked Sendable {
    let value: Any?
}

actor OpenClawGatewayConnection {
    private struct PendingGatewayRequest {
        let method: String
        let continuation: CheckedContinuation<GatewayJSONPayload, Error>
        let timeoutTask: Task<Void, Never>?
    }

    private var baseURL: URL
    private var gatewayToken: String
    private var isStarted = false
    private var webSocketTask: URLSessionWebSocketTask?
    private var session: URLSession?
    private var receiveLoopTask: Task<Void, Never>?
    private var isReceiveLoopRunning = false
    private var reconnectTask: Task<Void, Never>?
    private var connectTask: Task<Void, Error>?
    private var subscribers: [UUID: AsyncStream<GatewayConnectionEvent>.Continuation] = [:]
    private var pendingRequests: [String: PendingGatewayRequest] = [:]
    private var pendingApprovals: [String: GatewayPendingApproval] = [:]
    private var lastSequence: UInt64?
    private var reconnectDelayNanoseconds: UInt64 = 1_000_000_000

    init(baseURL: URL, gatewayToken: String) {
        self.baseURL = baseURL
        self.gatewayToken = gatewayToken.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    func configure(baseURL: URL, gatewayToken: String) async {
        let normalizedToken = gatewayToken.trimmingCharacters(in: .whitespacesAndNewlines)
        let baseURLChanged = self.baseURL != baseURL
        let tokenChanged = self.gatewayToken != normalizedToken

        self.baseURL = baseURL
        self.gatewayToken = normalizedToken

        guard isStarted, baseURLChanged || tokenChanged else {
            return
        }

        await disconnect(reason: "Gateway settings changed.")
        scheduleReconnect()
    }

    func subscribe() -> AsyncStream<GatewayConnectionEvent> {
        AsyncStream { continuation in
            let subscriberID = UUID()
            subscribers[subscriberID] = continuation
            continuation.onTermination = { [weak self] _ in
                Task {
                    await self?.removeSubscriber(subscriberID)
                }
            }
        }
    }

    func start() async {
        isStarted = true
        _ = await connectIfNeeded()
    }

    func stop() async {
        isStarted = false
        reconnectTask?.cancel()
        reconnectTask = nil
        connectTask?.cancel()
        connectTask = nil
        await disconnect(reason: "Stopped.")
    }

    func request(method: String, params: [String: Any]) async throws -> GatewayJSONPayload {
        let connected = await connectIfNeeded()
        guard connected, let webSocketTask else {
            throw URLError(.cannotConnectToHost)
        }

        let requestID = UUID().uuidString.lowercased()
        let timeoutNanoseconds: UInt64 = 15_000_000_000

        return try await withCheckedThrowingContinuation { continuation in
            let timeoutTask = Task { [requestID] in
                try? await Task.sleep(nanoseconds: timeoutNanoseconds)
                self.failPendingRequest(
                    requestID,
                    error: URLError(.timedOut)
                )
            }

            pendingRequests[requestID] = PendingGatewayRequest(
                method: method,
                continuation: continuation,
                timeoutTask: timeoutTask
            )

            Task {
                do {
                    try await self.sendJSONObject(
                        [
                            "type": "req",
                            "id": requestID,
                            "method": method,
                            "params": params,
                        ],
                        on: webSocketTask
                    )
                } catch {
                    self.failPendingRequest(requestID, error: error)
                }
            }
        }
    }

    func pendingApprovalsSnapshot() -> [GatewayPendingApproval] {
        pendingApprovals.values.sorted { lhs, rhs in
            if lhs.createdAtMS == rhs.createdAtMS {
                return lhs.id < rhs.id
            }
            return lhs.createdAtMS < rhs.createdAtMS
        }
    }

    private func removeSubscriber(_ subscriberID: UUID) {
        subscribers.removeValue(forKey: subscriberID)
    }

    private func connectIfNeeded() async -> Bool {
        if webSocketTask?.closeCode == .invalid, isReceiveLoopRunning {
            return true
        }

        if let connectTask {
            do {
                try await connectTask.value
                return webSocketTask?.closeCode == .invalid
            } catch {
                return false
            }
        }

        let task = Task { try await self.establishConnection() }
        connectTask = task

        do {
            try await task.value
            connectTask = nil
            return true
        } catch {
            connectTask = nil
            broadcast(.disconnected(reason: gatewayDisconnectReason(for: error)))
            if isStarted {
                scheduleReconnect()
            }
            return false
        }
    }

    private func establishConnection() async throws {
        guard let webSocketURL = gatewayWebSocketURL(from: baseURL) else {
            throw URLError(.badURL)
        }

        let session = URLSession(configuration: .ephemeral)
        let task = session.webSocketTask(with: webSocketURL)
        task.resume()

        self.session = session
        self.webSocketTask = task
        self.lastSequence = nil

        guard let challengeFrame = try await receiveJSONObject(from: task, timeoutNanoseconds: 10_000_000_000) else {
            throw APIError.invalidResponse
        }

        guard let nonce = extractConnectChallengeNonce(from: challengeFrame) else {
            throw APIError.invalidResponse
        }

        try await sendConnectRequest(on: task, nonce: nonce)

        reconnectDelayNanoseconds = 1_000_000_000
        broadcast(.connected)

        receiveLoopTask?.cancel()
        isReceiveLoopRunning = true
        receiveLoopTask = Task { [weak self] in
            await self?.runReceiveLoop()
            await self?.receiveLoopDidEnd()
        }
    }

    private func runReceiveLoop() async {
        guard let task = webSocketTask else {
            return
        }

        while !Task.isCancelled {
            do {
                guard let frame = try await receiveJSONObject(from: task, timeoutNanoseconds: 30_000_000_000) else {
                    throw APIError.invalidResponse
                }

                if isGatewayEventFrame(frame) {
                    handleEventFrame(frame)
                    continue
                }

                if frame["type"] as? String == "res" {
                    handleResponseFrame(frame)
                }
            } catch {
                await disconnect(reason: gatewayDisconnectReason(for: error))
                if isStarted {
                    scheduleReconnect()
                }
                return
            }
        }
    }

    private func receiveLoopDidEnd() {
        isReceiveLoopRunning = false
        receiveLoopTask = nil
    }

    private func scheduleReconnect() {
        guard reconnectTask == nil, isStarted else {
            return
        }

        broadcast(.reconnecting)

        let delayNanoseconds = reconnectDelayNanoseconds
        reconnectDelayNanoseconds = min(reconnectDelayNanoseconds * 2, 30_000_000_000)

        reconnectTask = Task { [delayNanoseconds] in
            try? await Task.sleep(nanoseconds: delayNanoseconds)
            self.clearReconnectTask()
            _ = await self.connectIfNeeded()
        }
    }

    private func clearReconnectTask() {
        reconnectTask = nil
    }

    private func disconnect(reason: String) async {
        let error = URLError(.networkConnectionLost)
        let pending = pendingRequests.keys
        for requestID in pending {
            failPendingRequest(requestID, error: error)
        }
        pendingRequests.removeAll()

        receiveLoopTask?.cancel()
        receiveLoopTask = nil
        isReceiveLoopRunning = false

        webSocketTask?.cancel(with: .goingAway, reason: nil)
        webSocketTask = nil

        session?.invalidateAndCancel()
        session = nil
        lastSequence = nil

        broadcast(.disconnected(reason: reason))
    }

    private func sendConnectRequest(on task: URLSessionWebSocketTask, nonce: String) async throws {
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

        try await sendJSONObject(
            [
                "type": "req",
                "id": connectRequestID,
                "method": "connect",
                "params": connectParams,
            ],
            on: task
        )

        while true {
            guard let frame = try await receiveJSONObject(from: task, timeoutNanoseconds: 10_000_000_000) else {
                throw APIError.invalidResponse
            }

            if isGatewayEventFrame(frame) {
                continue
            }

            guard frame["type"] as? String == "res",
                  frame["id"] as? String == connectRequestID else {
                continue
            }

            if let ok = frame["ok"] as? Bool, ok,
               let hello = frame["payload"] as? [String: Any],
               hello["type"] as? String == "hello-ok" {
                storeDeviceTokenIfPresent(
                    from: hello["auth"] as? [String: Any],
                    deviceID: identity.deviceID
                )
                return
            }

            throw gatewayError(from: frame["error"] as? [String: Any])
        }
    }

    private func handleEventFrame(_ frame: [String: Any]) {
        if let sequence = gatewayStreamUInt64(frame["seq"]) {
            if let lastSequence, sequence > lastSequence + 1 {
                broadcast(
                    .gap(
                        GatewayGapEvent(
                            expected: lastSequence + 1,
                            received: sequence,
                            stateVersion: gatewayStreamStateVersion(from: frame["stateVersion"])
                        )
                    )
                )
            }
            lastSequence = sequence
        }

        guard let event = parseGatewayConnectionEvent(from: frame) else {
            return
        }

        applyPendingApprovalMutation(for: event)
        broadcast(event)
    }

    private func applyPendingApprovalMutation(for event: GatewayConnectionEvent) {
        switch event {
        case .approvalRequested(let approval):
            pendingApprovals[approval.id] = approval
        case .approvalResolved(let resolution):
            pendingApprovals.removeValue(forKey: resolution.id)
        default:
            break
        }
    }

    private func handleResponseFrame(_ frame: [String: Any]) {
        guard let requestID = frame["id"] as? String,
              let pending = pendingRequests.removeValue(forKey: requestID) else {
            return
        }

        pending.timeoutTask?.cancel()

        if let ok = frame["ok"] as? Bool, ok {
            pending.continuation.resume(returning: GatewayJSONPayload(value: frame["payload"]))
            return
        }

        pending.continuation.resume(throwing: gatewayError(from: frame["error"] as? [String: Any]))
    }

    private func failPendingRequest(_ requestID: String, error: Error) {
        guard let pending = pendingRequests.removeValue(forKey: requestID) else {
            return
        }

        pending.timeoutTask?.cancel()
        pending.continuation.resume(throwing: error)
    }

    private func broadcast(_ event: GatewayConnectionEvent) {
        for continuation in subscribers.values {
            continuation.yield(event)
        }
    }

    private func receiveJSONObject(
        from task: URLSessionWebSocketTask,
        timeoutNanoseconds: UInt64
    ) async throws -> [String: Any]? {
        let message = try await withThrowingTaskGroup(of: URLSessionWebSocketTask.Message.self) { group in
            group.addTask {
                try await task.receive()
            }
            group.addTask {
                try await Task.sleep(nanoseconds: timeoutNanoseconds)
                throw URLError(.timedOut)
            }

            guard let message = try await group.next() else {
                throw APIError.invalidResponse
            }

            group.cancelAll()
            return message
        }

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

    private func sendJSONObject(
        _ object: [String: Any],
        on task: URLSessionWebSocketTask
    ) async throws {
        let data = try JSONSerialization.data(withJSONObject: object)
        guard let text = String(data: data, encoding: .utf8) else {
            throw APIError.invalidResponse
        }

        try await task.send(.string(text))
    }

    private func storeDeviceTokenIfPresent(from auth: [String: Any]?, deviceID: String) {
        guard let auth,
              let deviceToken = gatewayConnectionTrimmedString(auth["deviceToken"] as? String) else {
            return
        }

        let role = gatewayConnectionTrimmedString(auth["role"] as? String) ?? GatewayClientProfile.role
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
    }
}

private func gatewayWebSocketURL(from url: URL) -> URL? {
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

private func gatewayError(from error: [String: Any]?) -> Error {
    let detailCode = gatewayConnectionTrimmedString(
        error?["details"].flatMap { ($0 as? [String: Any])?["code"] as? String }
    )
    let message = gatewayConnectionTrimmedString(error?["message"] as? String) ?? "Gateway request failed."

    if detailCode == "PAIRING_REQUIRED" || detailCode == "AUTH_UNAUTHORIZED" {
        return APIError.unauthorized
    }

    if let detailCode {
        return APIError.rpc(detailCode)
    }

    return APIError.rpc(message)
}

private func gatewayDisconnectReason(for error: Error) -> String {
    if let apiError = error as? APIError {
        switch apiError {
        case .unauthorized:
            return "unauthorized"
        case .rpc(let code):
            return code
        case .invalidResponse:
            return "invalid response"
        }
    }

    if let urlError = error as? URLError {
        return urlError.localizedDescription
    }

    return error.localizedDescription
}

private func gatewayConnectionTrimmedString(_ value: String?) -> String? {
    let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    return trimmed.isEmpty ? nil : trimmed
}
