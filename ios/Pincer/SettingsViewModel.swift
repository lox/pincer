import Foundation

enum GatewayCheckStatus: Equatable {
    case idle
    case running
    case ok
    case warning
    case error
}

struct GatewayCheckItem: Identifiable, Equatable {
    let id: String
    let title: String
    var status: GatewayCheckStatus
    var detail: String

    init(id: String, title: String, status: GatewayCheckStatus = .idle, detail: String = "") {
        self.id = id
        self.title = title
        self.status = status
        self.detail = detail
    }
}

@MainActor
final class SettingsViewModel: ObservableObject {
    @Published var gatewayURL: String
    @Published var gatewayToken: String
    @Published var primarySessionKey: String
    @Published var errorText: String?
    @Published var isBusy = false
    @Published var isCheckingGateway = false
    @Published var gatewayChecks: [GatewayCheckItem] = []

    private let client: APIClient

    init(client: APIClient) {
        self.client = client
        self.gatewayURL = AppConfig.baseURLString
        self.gatewayToken = AppConfig.bearerToken
        self.primarySessionKey = AppConfig.primarySessionKey
    }

    func refresh() async {
        gatewayURL = AppConfig.baseURLString
        gatewayToken = AppConfig.bearerToken
        primarySessionKey = AppConfig.primarySessionKey
    }

    func saveConnectionSettings() async {
        guard let parsedURL = AppConfig.parseBaseURL(gatewayURL) else {
            errorText = "Enter a valid Gateway URL, for example ws://127.0.0.1:18789."
            return
        }

        let trimmedSession = primarySessionKey.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmedSession.isEmpty else {
            errorText = "Enter a primary session key."
            return
        }

        isBusy = true
        defer { isBusy = false }

        gatewayURL = parsedURL.absoluteString
        primarySessionKey = trimmedSession
        await client.setBaseURL(parsedURL)
        await client.setGatewayToken(gatewayToken)
        await client.setPrimarySessionKey(trimmedSession)
    }

    func resetConnectionSettings() async {
        isBusy = true
        defer { isBusy = false }

        gatewayURL = AppConfig.defaultBaseURL.absoluteString
        gatewayToken = ""
        primarySessionKey = AppConfig.defaultPrimarySessionKey
        await client.setBaseURL(AppConfig.defaultBaseURL)
        await client.setGatewayToken("")
        await client.setPrimarySessionKey(AppConfig.defaultPrimarySessionKey)
    }

    func checkGateway() async {
        if isCheckingGateway {
            return
        }

        let raw = gatewayURL.trimmingCharacters(in: .whitespacesAndNewlines)
        isCheckingGateway = true
        gatewayChecks = [
            GatewayCheckItem(id: "url", title: "Gateway URL", status: .running),
            GatewayCheckItem(id: "ws", title: "Gateway WebSocket", status: .idle),
            GatewayCheckItem(id: "auth", title: "Gateway Auth", status: .idle),
        ]
        defer { isCheckingGateway = false }

        guard let parsedURL = AppConfig.parseBaseURL(raw) else {
            setGatewayCheck(id: "url", status: .error, detail: "Use ws://127.0.0.1:18789 or your wss:// Gateway URL.")
            return
        }

        setGatewayCheck(id: "url", status: .ok, detail: parsedURL.absoluteString)

        setGatewayCheck(id: "ws", status: .running, detail: "")
        let gatewayProbe = await client.probeGatewayConnection(baseURL: parsedURL)
        if gatewayProbe.code == "ok" {
            setGatewayCheck(id: "ws", status: .ok, detail: gatewayProbe.detail)
        } else {
            setGatewayCheck(
                id: "ws",
                status: .error,
                detail: "failed (\(gatewayProbe.code))\(gatewayProbe.detail.isEmpty ? "" : ": \(gatewayProbe.detail)")"
            )
        }

        setGatewayCheck(id: "auth", status: .running, detail: "")
        let authProbe = await client.probeGatewayAuth(baseURL: parsedURL)
        if authProbe.code == "ok" {
            setGatewayCheck(id: "auth", status: .warning, detail: authProbe.detail)
        } else {
            setGatewayCheck(id: "auth", status: .error, detail: authProbe.detail)
        }
    }

    private func setGatewayCheck(id: String, status: GatewayCheckStatus, detail: String) {
        guard let index = gatewayChecks.firstIndex(where: { $0.id == id }) else { return }
        gatewayChecks[index].status = status
        gatewayChecks[index].detail = detail
    }
}
