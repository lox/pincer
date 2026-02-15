import Foundation
import UIKit

enum BackendCheckStatus: Equatable {
    case idle
    case running
    case ok
    case warning
    case error
}

struct BackendCheckItem: Identifiable, Equatable {
    let id: String
    let title: String
    var status: BackendCheckStatus
    var detail: String

    init(id: String, title: String, status: BackendCheckStatus = .idle, detail: String = "") {
        self.id = id
        self.title = title
        self.status = status
        self.detail = detail
    }
}

@MainActor
final class SettingsViewModel: ObservableObject {
    @Published var devices: [Device] = []
    @Published var backendURL: String
    @Published var errorText: String?
    @Published var isBusy = false
    @Published var isCheckingBackend = false
    @Published var backendChecks: [BackendCheckItem] = []
    @Published var generatedPairingCode: String?
    @Published var isGeneratingCode = false
    @Published var manualPairingCode: String = ""
    @Published var isBindingCode = false

    private let client: APIClient

    init(client: APIClient) {
        self.client = client
        self.backendURL = AppConfig.baseURLString
    }

    func refresh() async {
        isBusy = true
        defer { isBusy = false }

        do {
            devices = try await client.fetchDevices()
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to load devices.")
        }
    }

    func revoke(_ deviceID: String) async {
        isBusy = true
        defer { isBusy = false }

        do {
            try await client.revokeDevice(deviceID: deviceID)
            do {
                devices = try await client.fetchDevices()
            } catch APIError.unauthorized {
                devices = try await client.fetchDevices()
            }
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to revoke device.")
        }
    }

    func saveBackendURL() async {
        guard let parsedURL = AppConfig.parseBaseURL(backendURL) else {
            errorText = "Enter a valid backend URL (for example, http://192.168.1.50:8080)."
            return
        }

        backendURL = parsedURL.absoluteString
        await client.setBaseURL(parsedURL)
        devices = []
        await refresh()
    }

    func resetBackendURL() async {
        backendURL = AppConfig.defaultBaseURL.absoluteString
        await client.setBaseURL(AppConfig.defaultBaseURL)
        devices = []
        await refresh()
    }

    func checkBackend() async {
        if isCheckingBackend {
            return
        }

        let raw = backendURL.trimmingCharacters(in: .whitespacesAndNewlines)
        isCheckingBackend = true
        backendChecks = [
            BackendCheckItem(id: "url", title: "URL format", status: .running),
            BackendCheckItem(id: "rpc", title: "RPC reachable", status: .idle),
            BackendCheckItem(id: "pairing", title: "Pairing available", status: .idle),
        ]
        defer { isCheckingBackend = false }

        guard let parsedURL = AppConfig.parseBaseURL(raw) else {
            setBackendCheck(id: "url", status: .error, detail: "Enter a valid URL (http://192.168.1.50:8080 or https://pincer.tailnet.ts.net).")
            setBackendCheck(id: "rpc", status: .idle, detail: "")
            setBackendCheck(id: "pairing", status: .idle, detail: "")
            return
        }

        setBackendCheck(id: "url", status: .ok, detail: parsedURL.absoluteString)

        setBackendCheck(id: "rpc", status: .running, detail: "")
        let rpcProbe = await client.probeBackendRPC(baseURL: parsedURL)
        if rpcProbe.code == "ok" || rpcProbe.code == "unauthenticated" {
            let detail = rpcProbe.code == "unauthenticated" ? "reachable (auth required)" : "reachable"
            setBackendCheck(id: "rpc", status: .ok, detail: detail)
        } else {
            setBackendCheck(
                id: "rpc",
                status: .error,
                detail: "failed (\(rpcProbe.code))\(rpcProbe.detail.isEmpty ? "" : ": ")\(rpcProbe.detail)"
            )
        }

        setBackendCheck(id: "pairing", status: .running, detail: "")
        let pairingProbe = await client.probePairingEndpoint(baseURL: parsedURL)
        if pairingProbe.code == "ok" {
            setBackendCheck(id: "pairing", status: .ok, detail: "create pairing code: ok")
        } else if pairingProbe.code == "unauthenticated" {
            setBackendCheck(id: "pairing", status: .ok, detail: "server already paired — authorize from existing device to add this one")
        } else {
            setBackendCheck(
                id: "pairing",
                status: .error,
                detail: "failed (\(pairingProbe.code))\(pairingProbe.detail.isEmpty ? "" : ": ")\(pairingProbe.detail)"
            )
        }
    }

    func generatePairingCode() async {
        isGeneratingCode = true
        defer { isGeneratingCode = false }

        do {
            let code = try await client.generatePairingCode()
            generatedPairingCode = code
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to generate pairing code.")
        }
    }

    func bindManualCode() async {
        let code = manualPairingCode.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !code.isEmpty else {
            errorText = "Enter a pairing code."
            return
        }

        isBindingCode = true
        defer { isBindingCode = false }

        do {
            try await client.manualBind(code: code, deviceName: UIDevice.current.name)
            manualPairingCode = ""
            await refresh()
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to bind pairing code.")
        }
    }

    private func setBackendCheck(id: String, status: BackendCheckStatus, detail: String) {
        guard let index = backendChecks.firstIndex(where: { $0.id == id }) else { return }
        backendChecks[index].status = status
        backendChecks[index].detail = detail
    }
}
