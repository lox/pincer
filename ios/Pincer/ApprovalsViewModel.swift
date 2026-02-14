import Foundation

@MainActor
final class ApprovalsViewModel: ObservableObject {
    @Published var approvals: [Approval] = []
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
            approvals = try await client.fetchApprovals(status: "pending")
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to load approvals.")
        }
    }

    func approve(_ actionID: String, onSuccess: @escaping () async -> Void = {}) async {
        isBusy = true
        defer { isBusy = false }
        do {
            try await client.approve(actionID: actionID)
            approvals = approvals.filter { $0.actionID != actionID }
            await onSuccess()
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to approve action.")
        }
    }
}
