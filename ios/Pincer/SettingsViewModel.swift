import Foundation

@MainActor
final class SettingsViewModel: ObservableObject {
    @Published var devices: [Device] = []
    @Published var backendURL: String
    @Published var errorText: String?
    @Published var isBusy = false

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
}
