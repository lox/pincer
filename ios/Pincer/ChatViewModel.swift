import Foundation
import Combine

@MainActor
final class ChatViewModel: ObservableObject {
    @Published var threadID: String?
    @Published var messages: [Message] = []
    @Published var timelineItems: [ChatTimelineItem] = []
    @Published private(set) var inlineApprovals: [Approval] = []
    @Published private(set) var approvingActionIDs: Set<String> = []

    @Published private(set) var isAwaitingAssistantProgress = false

    @Published var input: String = ""
    @Published var errorText: String?
    @Published var isBusy = false

    private let client: APIClient
    private let approvalsStore: ApprovalsStore
    private var cancellables: Set<AnyCancellable> = []
    private var watchThreadTask: Task<Void, Never>?
    private var watchingThreadID: String?
    private var threadEventState = ThreadEventReducerState()
    private var staleTurnTimer: Task<Void, Never>?

    private static let createdAtFormatter = ISO8601DateFormatter()

    init(client: APIClient, approvalsStore: ApprovalsStore) {
        self.client = client
        self.approvalsStore = approvalsStore
        bindStore()
    }

    deinit {
        watchThreadTask?.cancel()
        staleTurnTimer?.cancel()
    }

    func bootstrapIfNeeded() async {
        if let threadID {
            startWatchThreadIfNeeded(for: threadID)
            return
        }

        isBusy = true
        defer { isBusy = false }

        do {
            let id = try await client.createThread()
            threadID = id
            try await refresh()
            syncInlineApprovals()
            startWatchThreadIfNeeded(for: id)
        } catch {
            errorText = userFacingErrorMessage(error, fallback: "Failed to initialize chat.")
        }
    }

    func refresh() async throws {
        guard let threadID else { return }

        let snapshot = try await client.fetchMessagesSnapshot(threadID: threadID)
        threadEventState = ThreadEventReducerState(
            messages: snapshot.messages,
            lastSequence: snapshot.lastSequence
        )
        rebuildTimeline()

        await approvalsStore.refreshPendingWithoutBusyState()
        syncInlineApprovals()
        startWatchThreadIfNeeded(for: threadID)
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

        appendLocalMessage(optimisticMessage)
        input = ""
        isBusy = true
        isAwaitingAssistantProgress = true
        defer {
            isBusy = false
            isAwaitingAssistantProgress = false
        }

        do {
            try await client.startTurnStream(
                threadID: threadID,
                content: content,
                clientMessageID: optimisticID,
                resumeFromSequence: threadEventState.lastSequence
            ) { [weak self] event in
                guard let self else { return }
                await self.applyThreadEvent(event)
            }
        } catch {
            removeMessage(messageID: optimisticID)
            input = content
            errorText = userFacingErrorMessage(error, fallback: "Failed to send message.")
        }
    }

    func approveInline(_ actionID: String) async {
        guard threadID != nil else { return }
        guard !approvingActionIDs.contains(actionID) else { return }

        let approved = await approvalsStore.approve(actionID)
        if approved {
            inlineApprovals.removeAll { $0.actionID == actionID }
            await refreshAfterApproval()
        } else {
            errorText = approvalsStore.errorText
            syncInlineApprovals()
        }
    }

    func approveAllInline() async {
        guard threadID != nil else { return }

        // Snapshot action IDs so we can iterate deterministically while local state mutates.
        let actionIDs = inlineApprovals.map(\.actionID)
        guard !actionIDs.isEmpty else { return }

        var encounteredFailure = false
        for actionID in actionIDs {
            guard !approvingActionIDs.contains(actionID) else { continue }

            let approved = await approvalsStore.approve(actionID)
            if approved {
                inlineApprovals.removeAll { $0.actionID == actionID }
            } else {
                encounteredFailure = true
                errorText = approvalsStore.errorText
            }
        }

        // Always refresh to pick up any external state transitions during batch approval.
        await refreshAfterApproval()

        if encounteredFailure, errorText == nil {
            errorText = "One or more approvals failed."
        }
    }

    func refreshAfterApproval() async {
        guard threadID != nil else { return }
        await approvalsStore.refreshPendingWithoutBusyState()
        syncInlineApprovals()
    }

    private func bindStore() {
        approvalsStore.$pendingApprovals
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in
                self?.syncInlineApprovals()
            }
            .store(in: &cancellables)

        approvalsStore.$approvingActionIDs
            .receive(on: RunLoop.main)
            .sink { [weak self] actionIDs in
                self?.approvingActionIDs = actionIDs
            }
            .store(in: &cancellables)
    }

    private func appendLocalMessage(_ message: Message) {
        threadEventState.messages.append(message)
        rebuildTimeline()
    }

    private func removeMessage(messageID: String) {
        threadEventState.messages.removeAll { $0.messageID == messageID }
        rebuildTimeline()
    }

    private func rebuildTimeline() {
        var items: [ChatTimelineItem] = threadEventState.messages.map { .message($0) }
        for approval in inlineApprovals {
            items.append(.approval(approval))
        }
        timelineItems = items
        messages = threadEventState.messages
    }

    private func startWatchThreadIfNeeded(for threadID: String) {
        if watchingThreadID == threadID, let watchThreadTask, !watchThreadTask.isCancelled {
            return
        }

        watchThreadTask?.cancel()
        watchingThreadID = threadID
        watchThreadTask = Task { [weak self] in
            guard let self else { return }
            await self.watchThreadLoop(threadID: threadID)
        }
    }

    private func watchThreadLoop(threadID: String) async {
        while !Task.isCancelled {
            let fromSequence = threadEventState.lastSequence
            do {
                try await client.watchThreadStream(
                    threadID: threadID,
                    fromSequence: fromSequence
                ) { [weak self] event in
                    guard let self else { return }
                    await self.applyThreadEvent(event)
                }

                if Task.isCancelled {
                    return
                }
                try? await Task.sleep(nanoseconds: 300_000_000)
            } catch {
                if Task.isCancelled {
                    return
                }
                if shouldShowLiveStreamError(error) {
                    errorText = userFacingErrorMessage(error, fallback: "Lost live thread stream. Reconnecting...")
                }
                try? await Task.sleep(nanoseconds: 1_000_000_000)
            }
        }
    }

    private func applyThreadEvent(_ event: Pincer_Protocol_V1_ThreadEvent) async {
        guard let threadID else { return }

        let effect = ThreadEventReducer.apply(
            event,
            state: &threadEventState,
            fallbackThreadID: threadID
        )

        rebuildTimeline()

        if effect.shouldRefreshApprovals {
            await approvalsStore.refreshPendingWithoutBusyState()
            syncInlineApprovals()
        }

        if effect.shouldResyncMessages {
            do {
                try await refreshMessagesOnly()
            } catch {
                errorText = userFacingErrorMessage(error, fallback: "Failed to resync chat messages.")
            }
        }

        if effect.receivedProgressSignal {
            isAwaitingAssistantProgress = false
            cancelStaleTurnTimer()
        }

        if effect.shouldResumeAwaitingProgress && !effect.reachedTurnTerminal {
            isAwaitingAssistantProgress = true
            resetStaleTurnTimer()
        }

        if effect.reachedTurnTerminal {
            isBusy = false
            isAwaitingAssistantProgress = false
            cancelStaleTurnTimer()
        }

        if let failure = effect.turnFailureMessage,
           !failure.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            errorText = failure
        }
    }

    private func resetStaleTurnTimer() {
        staleTurnTimer?.cancel()
        staleTurnTimer = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 90_000_000_000)
            guard !Task.isCancelled else { return }
            self?.errorText = "Response is taking longer than expected."
            self?.isAwaitingAssistantProgress = false
        }
    }

    private func cancelStaleTurnTimer() {
        staleTurnTimer?.cancel()
        staleTurnTimer = nil
    }

    private func refreshMessagesOnly() async throws {
        guard let threadID else { return }

        let snapshot = try await client.fetchMessagesSnapshot(threadID: threadID)
        threadEventState = ThreadEventReducerState(
            messages: snapshot.messages,
            lastSequence: snapshot.lastSequence
        )
        rebuildTimeline()
    }

    private func syncInlineApprovals() {
        inlineApprovals = approvalsStore.pendingApprovals(forThreadID: threadID)
        rebuildTimeline()
    }
}
