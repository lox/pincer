import Foundation
import Combine

struct ApprovedInlineIndicator: Identifiable, Equatable {
    let actionID: String
    let tool: String
    let approvedAt: Date

    var id: String { actionID }
}

@MainActor
final class ChatViewModel: ObservableObject {
    @Published var threadID: String?
    @Published var messages: [Message] = []
    @Published private(set) var inlineApprovals: [Approval] = []
    @Published private(set) var approvedInlineIndicators: [ApprovedInlineIndicator] = []
    @Published private(set) var approvingActionIDs: Set<String> = []
    @Published var input: String = ""
    @Published var errorText: String?
    @Published var isBusy = false

    private let client: APIClient
    private let approvalsStore: ApprovalsStore
    private var cancellables: Set<AnyCancellable> = []
    private static let createdAtFormatter = ISO8601DateFormatter()

    init(client: APIClient, approvalsStore: ApprovalsStore) {
        self.client = client
        self.approvalsStore = approvalsStore
        bindStore()
    }

    func bootstrapIfNeeded() async {
        guard threadID == nil else { return }
        isBusy = true
        defer { isBusy = false }
        do {
            let id = try await client.createThread()
            threadID = id
            try await refresh()
            syncInlineApprovals()
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to initialize chat.")
        }
    }

    func refresh() async throws {
        guard let threadID else { return }
        messages = try await client.fetchMessages(threadID: threadID)
        await approvalsStore.refreshPendingWithoutBusyState()
        syncInlineApprovals()
        cleanupApprovedInlineIndicators()
    }

    func send() async {
        let content = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !content.isEmpty, let threadID else { return }

        let optimisticID = "local_\(UUID().uuidString.lowercased())"
        let optimisticMessage = Message(
            messageID: optimisticID,
            threadID: threadID,
            role: "user",
            content: content,
            createdAt: Self.createdAtFormatter.string(from: Date())
        )

        messages.append(optimisticMessage)
        input = ""
        isBusy = true
        defer { isBusy = false }
        do {
            try await client.sendMessage(threadID: threadID, content: content)
            try await refresh()
        } catch {
            messages.removeAll { $0.messageID == optimisticID }
            input = content
            errorText = userFacingErrorMessage(error, fallback: "Failed to send message.")
        }
    }

    func approveInline(_ actionID: String) async {
        guard threadID != nil else { return }
        guard !approvingActionIDs.contains(actionID) else { return }
        let approvedItem = inlineApprovals.first { $0.actionID == actionID }

        let approved = await approvalsStore.approve(actionID)
        if approved {
            inlineApprovals.removeAll { $0.actionID == actionID }
            if let approvedItem {
                upsertApprovedIndicator(from: approvedItem)
            }
            await refreshAfterApproval()
        } else {
            errorText = approvalsStore.errorText
            syncInlineApprovals()
        }
    }

    func refreshAfterApproval() async {
        guard threadID != nil else { return }

        // Approve flips to APPROVED first; execution is picked up asynchronously by the
        // backend worker. Retry a few times so chat shows the status update without
        // requiring manual tab switching or pull-to-refresh.
        for attempt in 0..<5 {
            do {
                try await refresh()
            } catch {
                if attempt == 0 {
                    errorText = userFacingErrorMessage(error, fallback: "Failed to refresh chat.")
                }
            }

            if attempt < 4 {
                try? await Task.sleep(nanoseconds: 350_000_000)
            }
        }
    }

    private func bindStore() {
        approvalsStore.$pendingApprovals
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in
                self?.syncInlineApprovals()
                self?.cleanupApprovedInlineIndicators()
            }
            .store(in: &cancellables)

        approvalsStore.$approvingActionIDs
            .receive(on: RunLoop.main)
            .sink { [weak self] actionIDs in
                self?.approvingActionIDs = actionIDs
            }
            .store(in: &cancellables)
    }

    private func syncInlineApprovals() {
        inlineApprovals = approvalsStore.pendingApprovals(forThreadID: threadID)
    }

    private func upsertApprovedIndicator(from approval: Approval) {
        if approvedInlineIndicators.contains(where: { $0.actionID == approval.actionID }) {
            return
        }
        approvedInlineIndicators.append(ApprovedInlineIndicator(
            actionID: approval.actionID,
            tool: approval.tool,
            approvedAt: Date()
        ))
    }

    private func cleanupApprovedInlineIndicators() {
        approvedInlineIndicators.removeAll { indicator in
            hasApprovalMessage(for: indicator.actionID) || hasExecutionMessage(for: indicator.actionID)
        }
    }

    private func hasApprovalMessage(for actionID: String) -> Bool {
        let marker = "Action \(actionID) approved."
        return messages.contains { message in
            message.role == "system" && message.content.contains(marker)
        }
    }

    private func hasExecutionMessage(for actionID: String) -> Bool {
        let marker = "Action \(actionID) executed."
        return messages.contains { message in
            message.role == "system" && message.content.contains(marker)
        }
    }
}
