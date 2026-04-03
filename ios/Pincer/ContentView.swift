import SwiftUI

private enum A11y {
    static let screenSessions = "screen_sessions"
    static let screenApprovals = "screen_approvals"
    static let screenSettings = "screen_settings"
    static let tabSessions = "tab_sessions"
    static let tabApprovals = "tab_approvals"
    static let tabSettings = "tab_settings"
    static let gatewayURLInput = "gateway_url_input"
    static let gatewaySaveButton = "gateway_save_button"
    static let gatewayResetButton = "gateway_reset_button"
    static let messageInput = "message_input"
    static let messageSendButton = "message_send_button"
}

struct ContentView: View {
    @StateObject private var approvalsStore: ApprovalsStore
    @StateObject private var chatModel: ChatViewModel
    @StateObject private var approvalsModel: ApprovalsViewModel
    @StateObject private var settingsModel: SettingsViewModel

    init(client: APIClient) {
        let approvalsStore = ApprovalsStore(client: client)
        _approvalsStore = StateObject(wrappedValue: approvalsStore)
        _chatModel = StateObject(wrappedValue: ChatViewModel(client: client))
        _approvalsModel = StateObject(wrappedValue: ApprovalsViewModel(approvalsStore: approvalsStore))
        _settingsModel = StateObject(wrappedValue: SettingsViewModel(client: client))
    }

    var body: some View {
        TabView {
            SessionRootView(model: chatModel)
                .accessibilityIdentifier(A11y.screenSessions)
                .tabItem {
                    Label("Sessions", systemImage: "bubble.left.and.bubble.right")
                        .accessibilityIdentifier(A11y.tabSessions)
                }

            ApprovalsView(model: approvalsModel)
                .accessibilityIdentifier(A11y.screenApprovals)
                .tabItem {
                    Label("Approvals", systemImage: "checkmark.shield")
                        .accessibilityIdentifier(A11y.tabApprovals)
                }
                .badge(approvalsStore.pendingApprovals.count)

            SettingsView(model: settingsModel)
                .accessibilityIdentifier(A11y.screenSettings)
                .tabItem {
                    Label("Settings", systemImage: "gearshape")
                        .accessibilityIdentifier(A11y.tabSettings)
                }
        }
        .tint(PincerPalette.accent)
        .toolbarBackground(.visible, for: .tabBar)
        .toolbarBackground(PincerPalette.page, for: .tabBar)
        .background(PincerPalette.page)
        .ignoresSafeArea(.keyboard, edges: .bottom)
    }
}

private struct SessionRootView: View {
    @ObservedObject var model: ChatViewModel

    var body: some View {
        NavigationStack {
            List {
                Section {
                    ForEach(model.threads) { thread in
                        NavigationLink {
                            SessionDetailView(model: model, thread: thread)
                        } label: {
                            VStack(alignment: .leading, spacing: 4) {
                                HStack {
                                    Text(thread.displayTitle)
                                        .font(.headline)
                                        .foregroundStyle(PincerPalette.textPrimary)
                                    if thread.threadID == AppConfig.primarySessionKey {
                                        Text("Primary")
                                            .font(.caption.weight(.semibold))
                                            .foregroundStyle(PincerPalette.accent)
                                            .padding(.horizontal, 8)
                                            .padding(.vertical, 2)
                                            .background(PincerPalette.accent.opacity(0.14))
                                            .clipShape(Capsule())
                                    }
                                }

                                Text(thread.updatedAt.isEmpty ? "No activity yet" : relativeTimestamp(from: thread.updatedAt))
                                    .font(.subheadline)
                                    .foregroundStyle(PincerPalette.textSecondary)
                            }
                            .padding(.vertical, 4)
                        }
                    }
                    .onDelete { offsets in
                        guard let first = offsets.first, model.threads.indices.contains(first) else { return }
                        let thread = model.threads[first]
                        Task {
                            await model.loadThread(thread.threadID, title: thread.displayTitle)
                            await model.deleteCurrentThread()
                        }
                    }
                } header: {
                    Text("OpenClaw Sessions")
                } footer: {
                    Text("The app is now session-first. The direct OpenClaw Gateway client replaces the local shell next.")
                }
            }
            .scrollContentBackground(.hidden)
            .background(PincerPalette.page)
            .navigationTitle("Sessions")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        Task { await model.startNewThread() }
                    } label: {
                        Image(systemName: "plus")
                    }
                }
            }
            .task {
                await model.bootstrapIfNeeded()
            }
            .refreshable {
                await model.refreshThreads()
            }
            .alert("Error", isPresented: Binding(
                get: { model.errorText != nil },
                set: { if !$0 { model.errorText = nil } }
            )) {
                Button("OK", role: .cancel) {}
            } message: {
                Text(model.errorText ?? "Unknown error")
            }
        }
    }
}

private struct SessionDetailView: View {
    @ObservedObject var model: ChatViewModel
    let thread: ThreadSummary

    var body: some View {
        VStack(spacing: 0) {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 12) {
                        ForEach(model.messages) { message in
                            MessageBubble(message: message)
                                .id(message.id)
                        }
                    }
                    .padding(16)
                }
                .background(PincerPalette.page)
                .onChange(of: model.messages.count) { _, _ in
                    guard let lastID = model.messages.last?.id else { return }
                    withAnimation(.easeOut(duration: 0.2)) {
                        proxy.scrollTo(lastID, anchor: .bottom)
                    }
                }
            }

            composer
        }
        .background(PincerPalette.page)
        .navigationTitle(thread.displayTitle)
        .navigationBarTitleDisplayMode(.inline)
        .task {
            await model.loadThread(thread.threadID, title: thread.displayTitle)
        }
    }

    private var composer: some View {
        HStack(alignment: .bottom, spacing: 12) {
            TextField("Message \(thread.displayTitle)", text: $model.input, axis: .vertical)
                .textFieldStyle(.plain)
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .background(PincerPalette.card)
                .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
                .accessibilityIdentifier(A11y.messageInput)

            Button {
                Task { await model.send() }
            } label: {
                Image(systemName: "arrow.up.circle.fill")
                    .font(.system(size: 28))
                    .foregroundStyle(model.input.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? PincerPalette.textTertiary : PincerPalette.accent)
            }
            .disabled(model.input.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || model.isBusy)
            .accessibilityIdentifier(A11y.messageSendButton)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .background(PincerPalette.surface)
    }
}

private struct MessageBubble: View {
    let message: Message

    var body: some View {
        VStack(alignment: alignment, spacing: 6) {
            Text(roleLabel)
                .font(.caption.weight(.semibold))
                .foregroundStyle(PincerPalette.textSecondary)

            Text(message.content)
                .font(.body)
                .foregroundStyle(PincerPalette.textPrimary)
                .frame(maxWidth: .infinity, alignment: bubbleAlignment)
                .padding(12)
                .background(backgroundColor)
                .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))

            if !message.createdAt.isEmpty {
                Text(relativeTimestamp(from: message.createdAt))
                    .font(.caption2)
                    .foregroundStyle(PincerPalette.textTertiary)
            }
        }
        .frame(maxWidth: .infinity, alignment: bubbleAlignment)
    }

    private var roleLabel: String {
        switch message.role {
        case "assistant":
            return "Assistant"
        case "system":
            return "System"
        default:
            return "You"
        }
    }

    private var alignment: HorizontalAlignment {
        message.role == "user" ? .trailing : .leading
    }

    private var bubbleAlignment: Alignment {
        message.role == "user" ? .trailing : .leading
    }

    private var backgroundColor: Color {
        switch message.role {
        case "user":
            return PincerPalette.accent.opacity(0.18)
        case "system":
            return PincerPalette.warning.opacity(0.14)
        default:
            return PincerPalette.card
        }
    }
}

private struct ApprovalsView: View {
    @ObservedObject var model: ApprovalsViewModel

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    if model.approvals.isEmpty {
                        VStack(alignment: .leading, spacing: 8) {
                            Text("No open approvals")
                                .font(.title3.weight(.semibold))
                                .foregroundStyle(PincerPalette.textPrimary)
                            Text("This tab is reserved for OpenClaw exec/plugin approvals once the direct Gateway client is wired in.")
                                .foregroundStyle(PincerPalette.textSecondary)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .cardSurface()
                    } else {
                        ForEach(model.approvals) { approval in
                            VStack(alignment: .leading, spacing: 8) {
                                Text(approval.tool)
                                    .font(.headline)
                                    .foregroundStyle(PincerPalette.textPrimary)
                                Text(approval.deterministicSummary)
                                    .foregroundStyle(PincerPalette.textSecondary)
                                Button("Approve") {
                                    Task { await model.approve(approval.actionID) }
                                }
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .cardSurface()
                        }
                    }
                }
                .padding(16)
            }
            .scrollContentBackground(.hidden)
            .background(PincerPalette.page)
            .navigationTitle("Approvals")
            .task {
                await model.refresh()
            }
            .refreshable {
                await model.refresh()
            }
        }
    }
}

private struct SettingsView: View {
    @ObservedObject var model: SettingsViewModel

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    Text("Gateway")
                        .font(.title3.weight(.semibold))
                        .foregroundStyle(PincerPalette.textPrimary)
                        .padding(.horizontal, 16)
                        .padding(.top, 8)

                    GatewayCard(model: model)
                        .padding(.horizontal, 16)

                    Text("Control UI")
                        .font(.title3.weight(.semibold))
                        .foregroundStyle(PincerPalette.textPrimary)
                        .padding(.horizontal, 16)

                    VStack(alignment: .leading, spacing: 8) {
                        Text("The OpenClaw Control UI is served from the same Gateway.")
                            .foregroundStyle(PincerPalette.textSecondary)
                        if let controlURL = AppConfig.controlUIURL {
                            Text(controlURL.absoluteString)
                                .font(.footnote.monospaced())
                                .foregroundStyle(PincerPalette.textPrimary)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .cardSurface()
                    .padding(.horizontal, 16)
                }
                .padding(.bottom, 24)
            }
            .scrollDismissesKeyboard(.interactively)
            .background(PincerPalette.page)
            .navigationTitle("Settings")
            .task {
                await model.refresh()
            }
            .alert("Error", isPresented: Binding(
                get: { model.errorText != nil },
                set: { if !$0 { model.errorText = nil } }
            )) {
                Button("OK", role: .cancel) {}
            } message: {
                Text(model.errorText ?? "Unknown error")
            }
        }
    }
}

private struct GatewayCard: View {
    @ObservedObject var model: SettingsViewModel

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Gateway URL")
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(PincerPalette.textPrimary)

            TextField("ws://127.0.0.1:18789", text: $model.gatewayURL)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .background(PincerPalette.page)
                .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
                .accessibilityIdentifier(A11y.gatewayURLInput)

            Text("Gateway token or app password")
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(PincerPalette.textPrimary)

            SecureField("Optional for now", text: $model.gatewayToken)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .background(PincerPalette.page)
                .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))

            Text("Primary session key")
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(PincerPalette.textPrimary)

            TextField("main", text: $model.primarySessionKey)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .background(PincerPalette.page)
                .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))

            HStack(spacing: 12) {
                Button(model.isBusy ? "Saving..." : "Save") {
                    Task { await model.saveConnectionSettings() }
                }
                .disabled(model.isBusy || model.isCheckingGateway)
                .accessibilityIdentifier(A11y.gatewaySaveButton)

                Button(model.isCheckingGateway ? "Checking..." : "Check") {
                    Task { await model.checkGateway() }
                }
                .disabled(model.isBusy || model.isCheckingGateway)

                Button("Reset") {
                    Task { await model.resetConnectionSettings() }
                }
                .disabled(model.isBusy || model.isCheckingGateway)
                .accessibilityIdentifier(A11y.gatewayResetButton)
            }
            .font(.subheadline.weight(.semibold))
            .foregroundStyle(PincerPalette.accent)

            if !model.gatewayChecks.isEmpty {
                Divider()
                VStack(alignment: .leading, spacing: 8) {
                    ForEach(model.gatewayChecks) { item in
                        HStack(alignment: .top, spacing: 10) {
                            statusView(for: item.status)
                                .frame(width: 18)
                            VStack(alignment: .leading, spacing: 2) {
                                Text(item.title)
                                    .font(.subheadline.weight(.semibold))
                                    .foregroundStyle(PincerPalette.textPrimary)
                                if !item.detail.isEmpty {
                                    Text(item.detail)
                                        .font(.footnote)
                                        .foregroundStyle(PincerPalette.textSecondary)
                                        .fixedSize(horizontal: false, vertical: true)
                                }
                            }
                        }
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .cardSurface()
    }

    @ViewBuilder
    private func statusView(for status: GatewayCheckStatus) -> some View {
        switch status {
        case .idle:
            Image(systemName: "circle")
                .foregroundStyle(PincerPalette.textTertiary)
        case .running:
            ProgressView()
                .controlSize(.small)
        case .ok:
            Image(systemName: "checkmark.circle.fill")
                .foregroundStyle(PincerPalette.success)
        case .warning:
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(PincerPalette.warning)
        case .error:
            Image(systemName: "xmark.octagon.fill")
                .foregroundStyle(PincerPalette.danger)
        }
    }
}

private enum PincerPalette {
    static let page = Color(red: 0.97, green: 0.98, blue: 0.99)
    static let surface = Color.white
    static let card = Color(red: 0.93, green: 0.95, blue: 0.98)
    static let accent = Color(red: 0.06, green: 0.39, blue: 0.88)
    static let textPrimary = Color(red: 0.1, green: 0.14, blue: 0.2)
    static let textSecondary = Color(red: 0.34, green: 0.39, blue: 0.49)
    static let textTertiary = Color(red: 0.57, green: 0.62, blue: 0.71)
    static let success = Color(red: 0.12, green: 0.63, blue: 0.35)
    static let warning = Color(red: 0.83, green: 0.55, blue: 0.1)
    static let danger = Color(red: 0.82, green: 0.22, blue: 0.19)
}

private extension View {
    func cardSurface() -> some View {
        self
            .padding(16)
            .background(PincerPalette.surface)
            .clipShape(RoundedRectangle(cornerRadius: 18, style: .continuous))
            .shadow(color: .black.opacity(0.04), radius: 10, x: 0, y: 6)
    }
}

private func relativeTimestamp(from iso: String) -> String {
    let parserWithFraction = ISO8601DateFormatter()
    parserWithFraction.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    let parser = ISO8601DateFormatter()

    guard let date = parserWithFraction.date(from: iso) ?? parser.date(from: iso) else {
        return iso
    }

    let formatter = RelativeDateTimeFormatter()
    formatter.unitsStyle = .short
    return formatter.localizedString(for: date, relativeTo: Date())
}
