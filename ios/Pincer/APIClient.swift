import Foundation

enum APIError: Error {
    case invalidResponse
    case httpStatus(Int)
    case unauthorized
}

actor APIClient {
    private let baseURL: URL
    private var token: String
    private let session: URLSession
    private let decoder: JSONDecoder
    private let encoder: JSONEncoder

    init(baseURL: URL, token: String, session: URLSession = .shared) {
        self.baseURL = baseURL
        self.token = token
        self.session = session
        self.decoder = JSONDecoder()
        self.encoder = JSONEncoder()
    }

    func ensurePaired(deviceName: String = "Pincer iOS") async throws {
        if !token.isEmpty {
            return
        }

        let codeRequest = try makeRequest(path: "/v1/pairing/code", method: "POST", body: Optional<SendMessageRequest>.none, includeAuth: false)
        let codeResponse: PairingCodeResponse = try await send(codeRequest)

        let bindBody = PairingBindRequest(code: codeResponse.code, deviceName: deviceName)
        let bindRequest = try makeRequest(path: "/v1/pairing/bind", method: "POST", body: bindBody, includeAuth: false)
        let bindResponse: PairingBindResponse = try await send(bindRequest)

        token = bindResponse.token
        UserDefaults.standard.set(bindResponse.token, forKey: AppConfig.tokenDefaultsKey)
    }

    func createThread() async throws -> String {
        try await ensurePaired()
        let request = try makeRequest(path: "/v1/chat/threads", method: "POST", body: Optional<SendMessageRequest>.none)
        let response: ThreadResponse = try await send(request)
        return response.threadID
    }

    func sendMessage(threadID: String, content: String) async throws {
        try await ensurePaired()
        let body = SendMessageRequest(content: content)
        let request = try makeRequest(path: "/v1/chat/threads/\(threadID)/messages", method: "POST", body: body)
        let _: EmptyResponse = try await send(request)
    }

    func fetchMessages(threadID: String) async throws -> [Message] {
        try await ensurePaired()
        let request = try makeRequest(path: "/v1/chat/threads/\(threadID)/messages", method: "GET", body: Optional<SendMessageRequest>.none)
        let response: MessagesResponse = try await send(request)
        return response.items
    }

    func fetchApprovals(status: String = "pending") async throws -> [Approval] {
        try await ensurePaired()
        let request = try makeRequest(path: "/v1/approvals?status=\(status)", method: "GET", body: Optional<SendMessageRequest>.none)
        let response: ApprovalsResponse = try await send(request)
        return response.items
    }

    func approve(actionID: String) async throws {
        try await ensurePaired()
        let request = try makeRequest(path: "/v1/approvals/\(actionID)/approve", method: "POST", body: Optional<SendMessageRequest>.none)
        let _: EmptyResponse = try await send(request)
    }

    func fetchDevices() async throws -> [Device] {
        try await ensurePaired()
        let request = try makeRequest(path: "/v1/devices", method: "GET", body: Optional<SendMessageRequest>.none)
        let response: DevicesResponse = try await send(request)
        return response.items
    }

    func revokeDevice(deviceID: String) async throws {
        try await ensurePaired()
        let request = try makeRequest(path: "/v1/devices/\(deviceID)/revoke", method: "POST", body: Optional<SendMessageRequest>.none)
        let _: EmptyResponse = try await send(request)
    }

    private func makeRequest<T: Encodable>(path: String, method: String, body: T?, includeAuth: Bool = true) throws -> URLRequest {
        guard let url = URL(string: path, relativeTo: baseURL) else {
            throw APIError.invalidResponse
        }
        var request = URLRequest(url: url)
        request.httpMethod = method
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if includeAuth, !token.isEmpty {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        if let body {
            request.httpBody = try encoder.encode(body)
        }
        return request
    }

    private func send<T: Decodable>(_ request: URLRequest) async throws -> T {
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw APIError.invalidResponse
        }
        guard (200...299).contains(http.statusCode) else {
            if http.statusCode == 401 {
                token = ""
                UserDefaults.standard.removeObject(forKey: AppConfig.tokenDefaultsKey)
                throw APIError.unauthorized
            }
            throw APIError.httpStatus(http.statusCode)
        }

        if T.self == EmptyResponse.self {
            return EmptyResponse() as! T
        }
        return try decoder.decode(T.self, from: data)
    }
}

private struct EmptyResponse: Decodable {}
