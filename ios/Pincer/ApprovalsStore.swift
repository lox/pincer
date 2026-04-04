import Foundation

protocol ApprovalsClientProtocol: AnyObject {
    func fetchApprovals(status: String) async throws -> [Approval]
    func resolveApproval(actionID: String, decision: String) async throws
    func gatewayEvents() async -> AsyncStream<GatewayConnectionEvent>
    func startLiveGatewayConnection() async
}

@MainActor
final class ApprovalsStore: ObservableObject {
    @Published private(set) var pendingApprovals: [Approval] = []
    @Published private(set) var approvingActionIDs: Set<String> = []
    @Published var errorText: String?
    @Published var isBusy = false

    private let client: any ApprovalsClientProtocol
    private var gatewayEventsTask: Task<Void, Never>?

    init(client: any ApprovalsClientProtocol) {
        self.client = client
    }

    deinit {
        gatewayEventsTask?.cancel()
    }

    func start() async {
        await ensureGatewayEventsStarted()
        await refreshPendingWithoutBusyState()
    }

    func refreshPending() async {
        await ensureGatewayEventsStarted()
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

    func resolve(_ actionID: String, decision: String) async -> Bool {
        guard !approvingActionIDs.contains(actionID) else { return false }

        isBusy = true
        approvingActionIDs.insert(actionID)
        defer {
            approvingActionIDs.remove(actionID)
            isBusy = false
        }

        do {
            try await client.resolveApproval(actionID: actionID, decision: decision)
            pendingApprovals.removeAll { $0.actionID == actionID }
            return true
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to resolve approval.")
            return false
        }
    }

    func pendingApprovals(forThreadID threadID: String?) -> [Approval] {
        guard let threadID, !threadID.isEmpty else { return [] }
        return pendingApprovals.filter { approval in
            approval.sourceID == threadID
        }
    }

    private func ensureGatewayEventsStarted() async {
        if gatewayEventsTask == nil {
            let events = await client.gatewayEvents()
            gatewayEventsTask = Task { [weak self] in
                guard let self else { return }

                for await event in events {
                    guard !Task.isCancelled else {
                        return
                    }

                    switch event {
                    case .connected, .approvalRequested, .approvalResolved:
                        await self.refreshPendingWithoutBusyState()
                    default:
                        break
                    }
                }
            }
        }

        await client.startLiveGatewayConnection()
    }
}

extension APIClient: ApprovalsClientProtocol {}
