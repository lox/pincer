import Foundation

@MainActor
final class SettingsViewModel: ObservableObject {
    @Published var devices: [Device] = []
    @Published var errorText: String?
    @Published var isBusy = false

    private let client: APIClient

    init(client: APIClient) {
        self.client = client
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
}
