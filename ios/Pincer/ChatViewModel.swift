import Foundation

@MainActor
final class ChatViewModel: ObservableObject {
    @Published var threads: [ThreadSummary] = []
    @Published var threadID: String?
    @Published var threadTitle: String = ""
    @Published var messages: [Message] = []
    @Published var input: String = ""
    @Published var errorText: String?
    @Published var isBusy = false

    private let client: APIClient

    init(client: APIClient) {
        self.client = client
    }

    func bootstrapIfNeeded() async {
        await refreshThreads()
        if let existing = threads.first {
            await loadThread(existing.threadID, title: existing.displayTitle)
        }
    }

    func refreshThreads() async {
        do {
            threads = try await client.listThreads()
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to load sessions.")
        }
    }

    func startNewThread() async {
        isBusy = true
        defer { isBusy = false }

        do {
            let id = try await client.createThread()
            await refreshThreads()
            if let created = threads.first(where: { $0.threadID == id }) ?? threads.first {
                await loadThread(created.threadID, title: created.displayTitle)
            }
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to create session.")
        }
    }

    func loadThread(_ id: String, title: String = "") async {
        isBusy = true
        defer { isBusy = false }

        do {
            let snapshot = try await client.fetchMessagesSnapshot(threadID: id)
            threadID = id
            threadTitle = title.isEmpty ? inferredTitle(for: id) : title
            messages = snapshot.messages
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to load session.")
        }
    }

    func deleteCurrentThread() async {
        guard let threadID else { return }
        isBusy = true
        defer { isBusy = false }

        do {
            try await client.deleteThread(threadID: threadID)
            self.threadID = nil
            self.threadTitle = ""
            self.messages = []
            await refreshThreads()
            if let existing = threads.first {
                await loadThread(existing.threadID, title: existing.displayTitle)
            }
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to delete session.")
        }
    }

    func send() async {
        let content = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !content.isEmpty, let threadID else { return }

        isBusy = true
        defer { isBusy = false }

        do {
            try await client.appendUserMessage(threadID: threadID, content: content)
            try await client.appendLocalAssistantPlaceholder(threadID: threadID)
            input = ""
            let snapshot = try await client.fetchMessagesSnapshot(threadID: threadID)
            messages = snapshot.messages
            await refreshThreads()
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to send message.")
        }
    }

    private func inferredTitle(for threadID: String) -> String {
        threads.first(where: { $0.threadID == threadID })?.displayTitle ?? "Session"
    }
}
