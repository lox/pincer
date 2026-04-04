import Foundation

protocol ChatClientProtocol: AnyObject {
    func createThread() async throws -> String
    func listThreads() async throws -> [ThreadSummary]
    func deleteThread(threadID: String) async throws
    func fetchMessagesSnapshot(threadID: String) async throws -> ThreadMessagesSnapshot
    func sendMessage(threadID: String, content: String) async throws -> GatewayChatSendReceipt
    func abortMessageRun(threadID: String, runID: String?) async throws
    func gatewayEvents() async -> AsyncStream<GatewayConnectionEvent>
    func startLiveGatewayConnection() async
}

extension APIClient: ChatClientProtocol {}
