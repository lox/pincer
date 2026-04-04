import XCTest
@testable import Pincer

@MainActor
final class ChatViewModelTests: XCTestCase {
    func testBootstrapReplaysGapAfterInitialSnapshotToRecoverChatHistory() async throws {
        let thread = ThreadSummary(
            threadID: "agent:main:main",
            title: "Main",
            createdAt: "2026-04-04T00:00:00Z",
            updatedAt: "2026-04-04T00:00:00Z",
            messageCount: 1
        )
        let recoveredMessage = Message(
            messageID: "msg-1",
            threadID: thread.threadID,
            role: "assistant",
            content: "Recovered after gap",
            createdAt: "2026-04-04T00:00:01Z"
        )
        let client = TestChatClient(
            threads: [thread],
            snapshots: [
                ThreadMessagesSnapshot(messages: [], timelineItems: [], lastSequence: 1),
                ThreadMessagesSnapshot(
                    messages: [recoveredMessage],
                    timelineItems: [.message(recoveredMessage)],
                    lastSequence: 2
                ),
            ]
        )
        let model = ChatViewModel(client: client)

        client.emitGapDuringFirstFetch = true

        await model.bootstrapIfNeeded()
        await fulfillment(of: [client.secondSnapshotExpectation], timeout: 2.0)

        XCTAssertEqual(client.fetchMessagesSnapshotCallCount, 2)
        XCTAssertEqual(model.messages.map(\.content), ["Recovered after gap"])
        XCTAssertNil(model.connectionNotice)
    }

    func testRefreshCurrentThreadClearsTransientConnectionNoticeAfterSuccessfulLoad() async throws {
        let thread = ThreadSummary(
            threadID: "agent:main:main",
            title: "Main",
            createdAt: "2026-04-04T00:00:00Z",
            updatedAt: "2026-04-04T00:00:00Z",
            messageCount: 1
        )
        let message = Message(
            messageID: "msg-1",
            threadID: thread.threadID,
            role: "assistant",
            content: "Loaded",
            createdAt: "2026-04-04T00:00:01Z"
        )
        let client = TestChatClient(
            threads: [thread],
            snapshots: [
                ThreadMessagesSnapshot(
                    messages: [message],
                    timelineItems: [.message(message)],
                    lastSequence: 1
                ),
                ThreadMessagesSnapshot(
                    messages: [message],
                    timelineItems: [.message(message)],
                    lastSequence: 1
                ),
                ThreadMessagesSnapshot(
                    messages: [message],
                    timelineItems: [.message(message)],
                    lastSequence: 1
                ),
            ]
        )
        let model = ChatViewModel(client: client)

        await model.bootstrapIfNeeded()
        client.emit(.gap(GatewayGapEvent(expected: 2, received: 4, stateVersion: nil)))
        await Task.yield()

        XCTAssertEqual(model.connectionNotice, "Gateway event gap detected. Refreshing chat…")

        await model.refreshCurrentThread()

        XCTAssertNil(model.connectionNotice)
        XCTAssertEqual(model.messages.map(\.content), ["Loaded"])
    }
}

private final class TestChatClient: ChatClientProtocol {
    private let threadsValue: [ThreadSummary]
    private var snapshots: [ThreadMessagesSnapshot]
    private let eventsStream: AsyncStream<GatewayConnectionEvent>
    private let eventsContinuation: AsyncStream<GatewayConnectionEvent>.Continuation

    var emitGapDuringFirstFetch = false
    var fetchMessagesSnapshotCallCount = 0
    let secondSnapshotExpectation: XCTestExpectation

    init(threads: [ThreadSummary], snapshots: [ThreadMessagesSnapshot]) {
        self.threadsValue = threads
        self.snapshots = snapshots
        self.secondSnapshotExpectation = XCTestExpectation(description: "second snapshot requested")

        var continuation: AsyncStream<GatewayConnectionEvent>.Continuation?
        self.eventsStream = AsyncStream { streamContinuation in
            continuation = streamContinuation
        }
        self.eventsContinuation = continuation!
    }

    func createThread() async throws -> String {
        fatalError("unused")
    }

    func listThreads() async throws -> [ThreadSummary] {
        threadsValue
    }

    func deleteThread(threadID: String) async throws {}

    func fetchMessagesSnapshot(threadID: String) async throws -> ThreadMessagesSnapshot {
        fetchMessagesSnapshotCallCount += 1

        if fetchMessagesSnapshotCallCount == 1, emitGapDuringFirstFetch {
            eventsContinuation.yield(
                .gap(
                    GatewayGapEvent(
                        expected: 2,
                        received: 4,
                        stateVersion: GatewayStateVersion(presence: 1, health: 1)
                    )
                )
            )
            await Task.yield()
        }

        if fetchMessagesSnapshotCallCount == 2 {
            secondSnapshotExpectation.fulfill()
        }

        return snapshots.removeFirst()
    }

    func sendMessage(threadID: String, content: String) async throws -> GatewayChatSendReceipt {
        fatalError("unused")
    }

    func abortMessageRun(threadID: String, runID: String?) async throws {}

    func gatewayEvents() async -> AsyncStream<GatewayConnectionEvent> {
        eventsStream
    }

    func startLiveGatewayConnection() async {}

    func emit(_ event: GatewayConnectionEvent) {
        eventsContinuation.yield(event)
    }
}
