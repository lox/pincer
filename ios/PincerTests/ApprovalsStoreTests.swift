import XCTest
@testable import Pincer

@MainActor
final class ApprovalsStoreTests: XCTestCase {
    func testStartLoadsPendingApprovalsFromClient() async {
        let approval = Approval(
            actionID: "approval-1",
            source: "exec",
            sourceID: "agent:main:main",
            tool: "Exec approval",
            status: "PENDING",
            riskClass: "allowlist",
            deterministicSummary: "pwd",
            commandPreview: "pwd",
            commandTimeoutMS: nil,
            createdAt: "2026-04-04T00:00:00Z",
            expiresAt: "2026-04-04T00:01:00Z",
            allowedDecisions: ["allow-once", "deny"]
        )
        let client = TestApprovalsClient(approvals: [approval])
        let store = ApprovalsStore(client: client)

        await store.start()

        XCTAssertEqual(client.startLiveGatewayConnectionCallCount, 1)
        XCTAssertEqual(store.pendingApprovals, [approval])
    }

    func testResolvePassesDecisionAndRemovesApproval() async {
        let approval = Approval(
            actionID: "approval-1",
            source: "exec",
            sourceID: "agent:main:main",
            tool: "Exec approval",
            status: "PENDING",
            riskClass: "allowlist",
            deterministicSummary: "pwd",
            commandPreview: "pwd",
            commandTimeoutMS: nil,
            createdAt: "2026-04-04T00:00:00Z",
            expiresAt: "2026-04-04T00:01:00Z",
            allowedDecisions: ["allow-once", "allow-always", "deny"]
        )
        let client = TestApprovalsClient(approvals: [approval])
        let store = ApprovalsStore(client: client)

        await store.start()
        let resolved = await store.resolve(approval.actionID, decision: "allow-always")

        XCTAssertTrue(resolved)
        XCTAssertEqual(client.resolveCalls, [ResolvedApprovalCall(actionID: "approval-1", decision: "allow-always")])
        XCTAssertTrue(store.pendingApprovals.isEmpty)
    }

    func testApprovalEventsRefreshPendingApprovals() async {
        let first = Approval(
            actionID: "approval-1",
            source: "exec",
            sourceID: "agent:main:main",
            tool: "Exec approval",
            status: "PENDING",
            riskClass: "allowlist",
            deterministicSummary: "pwd",
            commandPreview: "pwd",
            commandTimeoutMS: nil,
            createdAt: "2026-04-04T00:00:00Z",
            expiresAt: "2026-04-04T00:01:00Z",
            allowedDecisions: ["allow-once", "deny"]
        )
        let second = Approval(
            actionID: "plugin:approval-2",
            source: "plugin",
            sourceID: "agent:main:main",
            tool: "Plugin approval",
            status: "PENDING",
            riskClass: "warning",
            deterministicSummary: "Needs access to external API",
            commandPreview: "Needs access to external API",
            commandTimeoutMS: nil,
            createdAt: "2026-04-04T00:02:00Z",
            expiresAt: "2026-04-04T00:03:00Z",
            allowedDecisions: ["allow-once", "allow-always", "deny"]
        )
        let client = TestApprovalsClient(approvals: [first])
        let store = ApprovalsStore(client: client)

        await store.start()
        client.approvals = [first, second]
        client.emit(
            .approvalRequested(
                GatewayPendingApproval(
                    kind: .plugin,
                    id: "plugin:approval-2",
                    tool: "Plugin approval",
                    summary: "Needs access to external API",
                    commandPreview: "Needs access to external API",
                    riskClass: "warning",
                    allowedDecisions: ["allow-once", "allow-always", "deny"],
                    sessionKey: "agent:main:main",
                    createdAtMS: 1_775_260_000_000,
                    expiresAtMS: 1_775_260_060_000
                )
            )
        )
        await fulfillment(of: [client.secondFetchExpectation], timeout: 2.0)

        XCTAssertEqual(store.pendingApprovals, [first, second])
    }
}

private struct ResolvedApprovalCall: Equatable {
    let actionID: String
    let decision: String
}

private final class TestApprovalsClient: ApprovalsClientProtocol {
    var approvals: [Approval]
    var resolveCalls: [ResolvedApprovalCall] = []
    var startLiveGatewayConnectionCallCount = 0
    let secondFetchExpectation: XCTestExpectation

    private let eventsStream: AsyncStream<GatewayConnectionEvent>
    private let eventsContinuation: AsyncStream<GatewayConnectionEvent>.Continuation
    private var fetchCallCount = 0

    init(approvals: [Approval]) {
        self.approvals = approvals
        self.secondFetchExpectation = XCTestExpectation(description: "second approvals fetch")

        var continuation: AsyncStream<GatewayConnectionEvent>.Continuation?
        self.eventsStream = AsyncStream { streamContinuation in
            continuation = streamContinuation
        }
        self.eventsContinuation = continuation!
    }

    func fetchApprovals(status: String) async throws -> [Approval] {
        fetchCallCount += 1
        if fetchCallCount == 2 {
            secondFetchExpectation.fulfill()
        }
        return approvals
    }

    func resolveApproval(actionID: String, decision: String) async throws {
        resolveCalls.append(ResolvedApprovalCall(actionID: actionID, decision: decision))
    }

    func gatewayEvents() async -> AsyncStream<GatewayConnectionEvent> {
        eventsStream
    }

    func startLiveGatewayConnection() async {
        startLiveGatewayConnectionCallCount += 1
    }

    func emit(_ event: GatewayConnectionEvent) {
        eventsContinuation.yield(event)
    }
}
