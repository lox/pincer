import Foundation

@MainActor
final class ApprovalsStore: ObservableObject {
    @Published private(set) var pendingApprovals: [Approval] = []
    @Published private(set) var approvingActionIDs: Set<String> = []
    @Published var errorText: String?
    @Published var isBusy = false

    private let client: APIClient

    init(client: APIClient) {
        self.client = client
    }

    func refreshPending() async {
        isBusy = true
        defer { isBusy = false }
        await refreshPendingWithoutBusyState()
    }

    func refreshPendingWithoutBusyState() async {
        do {
            pendingApprovals = try await client.fetchApprovals(status: "pending")
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to load approvals.")
        }
    }

    func approve(_ actionID: String) async -> Bool {
        guard !approvingActionIDs.contains(actionID) else { return false }

        isBusy = true
        approvingActionIDs.insert(actionID)
        defer {
            approvingActionIDs.remove(actionID)
            isBusy = false
        }

        do {
            try await client.approve(actionID: actionID)
            pendingApprovals.removeAll { $0.actionID == actionID }
            return true
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to approve action.")
            return false
        }
    }

    func pendingApprovals(forThreadID threadID: String?) -> [Approval] {
        guard let threadID, !threadID.isEmpty else { return [] }
        return pendingApprovals.filter { approval in
            approval.source == "chat" && approval.sourceID == threadID
        }
    }
}
