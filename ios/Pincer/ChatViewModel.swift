import Foundation

@MainActor
final class ChatViewModel: ObservableObject {
    @Published var threadID: String?
    @Published var messages: [Message] = []
    @Published var input: String = ""
    @Published var errorText: String?
    @Published var isBusy = false

    private let client: APIClient

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
            errorText = "Failed to initialize chat."
        }
    }

    func refresh() async throws {
        guard let threadID else { return }
        messages = try await client.fetchMessages(threadID: threadID)
    }

    func send() async {
        let content = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !content.isEmpty, let threadID else { return }

        isBusy = true
        defer { isBusy = false }
        do {
            try await client.sendMessage(threadID: threadID, content: content)
            input = ""
            try await refresh()
        } catch {
            errorText = "Failed to send message."
        }
    }
}
