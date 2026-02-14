import Foundation

@MainActor
final class ChatViewModel: ObservableObject {
    @Published var threadID: String?
    @Published var messages: [Message] = []
    @Published private(set) var inlineApprovals: [Approval] = []
    @Published private(set) var approvingActionIDs: Set<String> = []
    @Published var input: String = ""
    @Published var errorText: String?
    @Published var isBusy = false

    private let client: APIClient
    private static let createdAtFormatter = ISO8601DateFormatter()

    init(client: APIClient) {
        self.client = client
    }

    func bootstrapIfNeeded() async {
        guard threadID == nil else { return }
        isBusy = true
        defer { isBusy = false }
        do {
            let id = try await client.createThread()
            threadID = id
            try await refresh()
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to initialize chat.")
        }
    }

    func refresh() async throws {
        guard let threadID else { return }
        messages = try await client.fetchMessages(threadID: threadID)
        await refreshInlineApprovals(threadID: threadID)
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
        guard !approvingActionIDs.contains(actionID), let threadID else { return }
        approvingActionIDs.insert(actionID)
        defer { approvingActionIDs.remove(actionID) }

        do {
            try await client.approve(actionID: actionID)
            inlineApprovals.removeAll { $0.actionID == actionID }

            // Pull latest messages to surface the eventual execution status update.
            try await refresh()
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to approve action.")
            await refreshInlineApprovals(threadID: threadID)
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

    private func refreshInlineApprovals(threadID: String) async {
        do {
            let pending = try await client.fetchApprovals(status: "pending")
            inlineApprovals = pending.filter { approval in
                approval.source == "chat" && approval.sourceID == threadID
            }
        } catch {
            inlineApprovals = []
        }
    }
}
