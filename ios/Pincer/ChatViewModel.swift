import Foundation

@MainActor
final class ChatViewModel: ObservableObject {
    @Published var threads: [ThreadSummary] = []
    @Published var threadID: String?
    @Published var threadTitle: String = ""
    @Published var messages: [Message] = []
    @Published var liveAssistantDraft: Message?
    @Published var liveToolCalls: [ToolCallActivity] = []
    @Published var connectionNotice: String?
    @Published var input: String = ""
    @Published var errorText: String?
    @Published var isBusy = false
    @Published var isStopping = false

    private let client: APIClient
    private var gatewayEventsTask: Task<Void, Never>?
    private var snapshotRefreshTask: Task<Void, Never>?
    private var streamState = ChatStreamState()

    init(client: APIClient) {
        self.client = client
    }

    deinit {
        gatewayEventsTask?.cancel()
        snapshotRefreshTask?.cancel()
    }

    func bootstrapIfNeeded() async {
        await ensureGatewayEventsStarted()

        if !threads.isEmpty, threadID != nil {
            return
        }

        await refreshCurrentThread()
    }

    func refreshThreads() async {
        do {
            threads = try await client.listThreads()
            if let currentThreadID = threadID,
               let current = threads.first(where: { $0.threadID == currentThreadID }) {
                threadTitle = current.displayTitle
            }
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to load sessions.")
        }
    }

    func startNewThread() async {
        await ensureGatewayEventsStarted()
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
        await ensureGatewayEventsStarted()
        isBusy = true
        defer { isBusy = false }

        do {
            let snapshot = try await client.fetchMessagesSnapshot(threadID: id)
            threadID = id
            threadTitle = title.isEmpty ? inferredTitle(for: id) : title
            streamState.messages = snapshot.messages
            streamState.activeRunID = nil
            streamState.assistantDraftText = ""
            streamState.assistantThinkingText = ""
            streamState.latestToolCalls = []
            streamState.needsSnapshotRefresh = false
            syncPublishedState()
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to load session.")
        }
    }

    func refreshCurrentThread() async {
        await ensureGatewayEventsStarted()
        await refreshThreads()

        if let currentThreadID = threadID,
           let current = threads.first(where: { $0.threadID == currentThreadID }) {
            await loadThread(current.threadID, title: current.displayTitle)
            return
        }

        if let existing = preferredThreadToOpen() {
            await loadThread(existing.threadID, title: existing.displayTitle)
            return
        }

        threadID = nil
        threadTitle = ""
        streamState = ChatStreamState()
        syncPublishedState()
    }

    func deleteCurrentThread() async {
        guard let threadID else { return }
        await deleteThread(threadID)
    }

    func deleteThread(_ id: String) async {
        isBusy = true
        defer { isBusy = false }

        do {
            try await client.deleteThread(threadID: id)
            if threadID == id {
                threadID = nil
                threadTitle = ""
                streamState = ChatStreamState()
                syncPublishedState()
            }
            await refreshThreads()
            if let existing = preferredThreadToOpen(),
               threadID == nil || !threads.contains(where: { $0.threadID == threadID }) {
                await loadThread(existing.threadID, title: existing.displayTitle)
            }
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to delete session.")
        }
    }

    func send() async {
        let content = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !content.isEmpty, let threadID else { return }

        await ensureGatewayEventsStarted()

        let optimisticMessage = Message(
            messageID: "optimistic_\(UUID().uuidString.lowercased())",
            threadID: threadID,
            role: "user",
            content: content,
            createdAt: ISO8601DateFormatter().string(from: Date())
        )
        let originalInput = input

        streamState.messages.append(optimisticMessage)
        streamState.activeRunID = nil
        streamState.assistantDraftText = ""
        streamState.assistantThinkingText = ""
        streamState.latestToolCalls = []
        streamState.connectionNotice = nil
        input = ""
        syncPublishedState()

        isBusy = true
        defer { isBusy = false }

        do {
            let receipt = try await client.sendMessage(threadID: threadID, content: content)
            streamState.activeRunID = receipt.runID
            syncPublishedState()
            Task {
                await self.refreshThreads()
            }
        } catch {
            input = originalInput
            streamState.messages.removeAll { $0.messageID == optimisticMessage.messageID }
            syncPublishedState()
            errorText = userFacingErrorMessage(error, fallback: "Failed to send message.")
        }
    }

    func abortCurrentRun() async {
        guard let threadID, canAbortCurrentRun else { return }

        isStopping = true
        defer { isStopping = false }

        do {
            try await client.abortMessageRun(threadID: threadID, runID: streamState.activeRunID)
            streamState.connectionNotice = "Stopping run…"
            syncPublishedState()
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to stop the current run.")
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

                    await self.handleGatewayEvent(event)
                }
            }
        }

        await client.startLiveGatewayConnection()
    }

    private func handleGatewayEvent(_ event: GatewayConnectionEvent) async {
        let currentThreadID = threadID ?? AppConfig.primarySessionKey
        applyGatewayConnectionEvent(
            event,
            to: &streamState,
            currentThreadID: currentThreadID
        )
        syncPublishedState()
        scheduleSnapshotRefreshIfNeeded()
    }

    private func scheduleSnapshotRefreshIfNeeded() {
        guard streamState.needsSnapshotRefresh,
              snapshotRefreshTask == nil,
              let currentThreadID = threadID else {
            return
        }

        streamState.needsSnapshotRefresh = false
        snapshotRefreshTask = Task { [weak self] in
            await self?.refreshThreadFromGateway(afterEventFor: currentThreadID)
        }
    }

    private func refreshThreadFromGateway(afterEventFor currentThreadID: String) async {
        defer { snapshotRefreshTask = nil }

        do {
            let snapshot = try await client.fetchMessagesSnapshot(threadID: currentThreadID)
            let latestThreads = try await client.listThreads()

            guard threadID == currentThreadID else {
                return
            }

            threads = latestThreads
            if let current = latestThreads.first(where: { $0.threadID == currentThreadID }) {
                threadTitle = current.displayTitle
            }
            streamState.messages = snapshot.messages
            syncPublishedState()
        } catch {
            guard shouldShowLiveStreamError(error) else {
                return
            }

            if threadID == currentThreadID {
                errorText = userFacingErrorMessage(error, fallback: "Failed to refresh session.")
            }
        }
    }

    private func preferredThreadToOpen() -> ThreadSummary? {
        threads.first(where: { sessionKeyMatchesPrimary($0.threadID) }) ?? threads.first
    }

    private func inferredTitle(for threadID: String) -> String {
        threads.first(where: { $0.threadID == threadID })?.displayTitle ?? "Session"
    }

    private func syncPublishedState() {
        messages = streamState.messages
        liveToolCalls = streamState.latestToolCalls
        connectionNotice = streamState.connectionNotice

        if let runID = streamState.activeRunID,
           let content = streamState.assistantDraftText.trimmingCharacters(in: .whitespacesAndNewlines).nilIfEmpty {
            liveAssistantDraft = Message(
                messageID: "draft_\(runID)",
                threadID: threadID ?? AppConfig.primarySessionKey,
                role: "assistant",
                content: content,
                createdAt: ""
            )
        } else {
            liveAssistantDraft = nil
        }
    }

    var currentThreadSummary: ThreadSummary? {
        if let threadID {
            return threads.first(where: { $0.threadID == threadID })
        }

        return preferredThreadToOpen()
    }

    var currentThreadDisplayTitle: String {
        if !threadTitle.isEmpty {
            return threadTitle
        }

        return currentThreadSummary?.displayTitle ?? "Chat"
    }

    var showsSessionSwitcher: Bool {
        shouldShowSessionSwitcher(for: threads)
    }

    var canAbortCurrentRun: Bool {
        streamState.activeRunID != nil || !streamState.assistantDraftText.isEmpty
    }
}

private extension String {
    var nilIfEmpty: String? {
        isEmpty ? nil : self
    }
}
