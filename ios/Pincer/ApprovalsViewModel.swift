import Foundation
import Combine

@MainActor
final class ApprovalsViewModel: ObservableObject {
    @Published var approvals: [Approval] = []
    @Published var errorText: String?
    @Published var isBusy = false

    private let approvalsStore: ApprovalsStore
    private var cancellables: Set<AnyCancellable> = []

    init(approvalsStore: ApprovalsStore) {
        self.approvalsStore = approvalsStore
        bindStore()
    }

    func refresh() async {
        await approvalsStore.refreshPending()
    }

    func approve(_ actionID: String, onSuccess: @escaping () async -> Void = {}) async {
        let approved = await approvalsStore.approve(actionID)
        if approved {
            await onSuccess()
        }
    }

    private func bindStore() {
        approvalsStore.$pendingApprovals
            .receive(on: RunLoop.main)
            .sink { [weak self] approvals in
                self?.approvals = approvals
            }
            .store(in: &cancellables)

        approvalsStore.$errorText
            .receive(on: RunLoop.main)
            .sink { [weak self] errorText in
                self?.errorText = errorText
            }
            .store(in: &cancellables)

        approvalsStore.$isBusy
            .receive(on: RunLoop.main)
            .sink { [weak self] isBusy in
                self?.isBusy = isBusy
            }
            .store(in: &cancellables)
    }
}
