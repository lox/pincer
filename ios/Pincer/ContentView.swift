import SwiftUI
import Textual

private enum A11y {
    static let screenChat = "screen_chat"
    static let screenApprovals = "screen_approvals"
    static let screenSettings = "screen_settings"
    static let tabChat = "tab_chat"
    static let tabApprovals = "tab_approvals"
    static let tabSettings = "tab_settings"
    static let gatewayURLInput = "gateway_url_input"
    static let gatewaySaveButton = "gateway_save_button"
    static let gatewayResetButton = "gateway_reset_button"
    static let messageInput = "message_input"
    static let messageSendButton = "message_send_button"
    static let messageStopButton = "message_stop_button"
    static let chatSessionsButton = "chat_sessions_button"
}

struct ContentView: View {
    @Environment(\.scenePhase) private var scenePhase
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
            ChatRootView(model: chatModel)
                .accessibilityIdentifier(A11y.screenChat)
                .tabItem {
                    Label("Chat", systemImage: "bubble.left.and.text.bubble.right")
                        .accessibilityIdentifier(A11y.tabChat)
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
        .task {
            await approvalsModel.start()
        }
        .onChange(of: scenePhase) { _, newPhase in
            Task {
                switch newPhase {
                case .active:
                    await chatModel.setGatewayConnectionActive(true)
                case .background:
                    await chatModel.setGatewayConnectionActive(false)
                case .inactive:
                    break
                @unknown default:
                    break
                }
            }
        }
    }
}

private struct ChatRootView: View {
    @ObservedObject var model: ChatViewModel
    @State private var isShowingSessions = false

    var body: some View {
        NavigationStack {
            Group {
                if model.currentThreadSummary != nil {
                    ChatConversationView(model: model)
                } else if model.isBusy {
                    VStack {
                        Spacer()
                        ProgressView("Loading chat")
                            .tint(PincerPalette.accent)
                        Spacer()
                    }
                } else {
                    VStack(alignment: .leading, spacing: 10) {
                        Text("No active chat")
                            .font(.title3.weight(.semibold))
                            .foregroundStyle(PincerPalette.textPrimary)
                        Text("Pincer opens straight into the primary OpenClaw conversation. Extra sessions only show up when the Gateway exposes them.")
                            .foregroundStyle(PincerPalette.textSecondary)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                    .padding(16)
                }
            }
            .background(PincerPalette.page)
            .navigationTitle(model.currentThreadDisplayTitle)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                if model.showsSessionSwitcher {
                    ToolbarItem(placement: .topBarLeading) {
                        Button {
                            isShowingSessions = true
                        } label: {
                            Image(systemName: "rectangle.stack")
                        }
                        .accessibilityIdentifier(A11y.chatSessionsButton)
                    }
                }

                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        Task { await model.refreshCurrentThread() }
                    } label: {
                        Image(systemName: "arrow.clockwise")
                    }
                }
            }
            .task {
                await model.bootstrapIfNeeded()
            }
            .sheet(isPresented: $isShowingSessions) {
                SessionSwitcherView(model: model)
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

private struct SessionSwitcherView: View {
    @Environment(\.dismiss) private var dismiss
    @ObservedObject var model: ChatViewModel

    var body: some View {
        NavigationStack {
            List {
                Section {
                    ForEach(model.threads) { thread in
                        Button {
                            Task {
                                await model.loadThread(thread.threadID, title: thread.displayTitle)
                                dismiss()
                            }
                        } label: {
                            SessionSwitcherRow(
                                thread: thread,
                                isSelected: model.threadID == thread.threadID
                            )
                        }
                        .buttonStyle(.plain)
                        .swipeActions {
                            if !sessionKeyMatchesPrimary(thread.threadID) {
                                Button("Delete", role: .destructive) {
                                    Task {
                                        await model.deleteThread(thread.threadID)
                                        if !model.showsSessionSwitcher {
                                            dismiss()
                                        }
                                    }
                                }
                            }
                        }
                    }
                } header: {
                    Text("OpenClaw Sessions")
                } footer: {
                    Text("Main stays pinned as the default chat. Extra sessions are available here when OpenClaw exposes them.")
                }
            }
            .scrollContentBackground(.hidden)
            .background(PincerPalette.page)
            .navigationTitle("Sessions")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") {
                        dismiss()
                    }
                }

                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        Task {
                            await model.startNewThread()
                            dismiss()
                        }
                    } label: {
                        Image(systemName: "plus")
                    }
                }
            }
        }
    }
}

private struct SessionSwitcherRow: View {
    let thread: ThreadSummary
    let isSelected: Bool

    var body: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 4) {
                HStack {
                    Text(thread.displayTitle)
                        .font(.headline)
                        .foregroundStyle(PincerPalette.textPrimary)

                    if sessionKeyMatchesPrimary(thread.threadID) {
                        Text("Main")
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

            Spacer()

            if isSelected {
                Image(systemName: "checkmark.circle.fill")
                    .foregroundStyle(PincerPalette.accent)
            }
        }
        .padding(.vertical, 4)
    }
}

private struct ChatConversationView: View {
    @ObservedObject var model: ChatViewModel

    var body: some View {
        VStack(spacing: 0) {
            if let connectionNotice = model.connectionNotice {
                HStack(spacing: 10) {
                    Image(systemName: model.canAbortCurrentRun ? "bolt.horizontal.circle.fill" : "wifi.exclamationmark")
                        .foregroundStyle(model.canAbortCurrentRun ? PincerPalette.accent : PincerPalette.warning)
                    Text(connectionNotice)
                        .font(.footnote.weight(.medium))
                        .foregroundStyle(PincerPalette.textPrimary)
                    Spacer()
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 12)
                .background(PincerPalette.card)
            }

            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 12) {
                        ForEach(model.timelineItems) { item in
                            TimelineItemView(item: item)
                                .id(item.id)
                        }

                        if !model.liveToolCalls.isEmpty || model.liveAssistantDraft != nil {
                            RunActivityView(
                                toolCalls: model.liveToolCalls,
                                assistantDraft: model.liveAssistantDraft
                            )
                            .id("run_activity")
                        }
                    }
                    .padding(16)
                }
                .background(PincerPalette.page)
                .onChange(of: model.timelineItems.count) { _, _ in
                    let lastID = model.liveAssistantDraft?.id ?? model.timelineItems.last?.id ?? "run_activity"
                    withAnimation(.easeOut(duration: 0.2)) {
                        proxy.scrollTo(lastID, anchor: .bottom)
                    }
                }
                .onChange(of: model.liveToolCalls.count) { _, _ in
                    withAnimation(.easeOut(duration: 0.2)) {
                        proxy.scrollTo("run_activity", anchor: .bottom)
                    }
                }
                .onChange(of: model.liveAssistantDraft?.content ?? "") { _, _ in
                    withAnimation(.easeOut(duration: 0.2)) {
                        proxy.scrollTo(model.liveAssistantDraft?.id ?? "run_activity", anchor: .bottom)
                    }
                }
            }

            composer
        }
        .background(PincerPalette.page)
    }

    private var composer: some View {
        HStack(alignment: .bottom, spacing: 12) {
            TextField("Message \(model.currentThreadDisplayTitle)", text: $model.input, axis: .vertical)
                .textFieldStyle(.plain)
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .background(PincerPalette.card)
                .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
                .accessibilityIdentifier(A11y.messageInput)

            if model.canAbortCurrentRun {
                Button(model.isStopping ? "Stopping…" : "Stop") {
                    Task { await model.abortCurrentRun() }
                }
                .buttonStyle(.borderedProminent)
                .tint(PincerPalette.warning)
                .disabled(model.isStopping)
                .accessibilityIdentifier(A11y.messageStopButton)
            } else {
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
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .background(PincerPalette.surface)
    }
}

private struct TimelineItemView: View {
    let item: ChatTimelineItem

    var body: some View {
        switch item {
        case .message(let message):
            MessageBubble(message: message)
        case .toolActivity(let toolCall):
            HistoricalToolActivityCard(toolCall: toolCall)
        case .approval(let approval):
            ApprovalTimelineCard(approval: approval)
        }
    }
}

private struct MessageBubble: View {
    let message: Message
    var isStreaming = false

    var body: some View {
        VStack(alignment: alignment, spacing: 6) {
            Text(roleLabel)
                .font(.caption.weight(.semibold))
                .foregroundStyle(PincerPalette.textSecondary)

            messageBody
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
            return isStreaming ? "Assistant Live" : "Assistant"
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

    @ViewBuilder
    private var messageBody: some View {
        if message.role == "user" {
            Text(message.content)
                .font(.body)
                .foregroundStyle(PincerPalette.textPrimary)
                .textSelection(.enabled)
        } else {
            MarkdownMessageText(
                message.content,
                font: .body,
                foregroundStyle: PincerPalette.textPrimary
            )
        }
    }
}

private struct HistoricalToolActivityCard: View {
    let toolCall: ToolCallActivity

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: iconName)
                .foregroundStyle(iconColor)

            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 8) {
                    Text(toolCall.displayLabel)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(PincerPalette.textPrimary)

                    Text(stateLabel)
                        .font(.caption.weight(.medium))
                        .foregroundStyle(stateColor)
                }

                if let summary = toolCall.argsPreview, !summary.isEmpty {
                    Text(summary)
                        .font(.footnote)
                        .foregroundStyle(PincerPalette.textSecondary)
                }

                if let errorText = toolCall.executions.first?.stderr, !errorText.isEmpty {
                    Text(errorText)
                        .font(.caption)
                        .foregroundStyle(PincerPalette.danger)
                }
            }

            Spacer()
        }
        .padding(12)
        .background(PincerPalette.card)
        .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
    }

    private var stateLabel: String {
        switch toolCall.state {
        case .planned:
            return "Queued"
        case .waitingApproval:
            return "Waiting Approval"
        case .running:
            return "Running"
        case .succeeded:
            return "Completed"
        case .failed:
            return "Failed"
        case .rejected:
            return "Rejected"
        }
    }

    private var iconName: String {
        switch toolCall.state {
        case .failed, .rejected:
            return "bolt.slash.circle.fill"
        default:
            return "bolt.circle.fill"
        }
    }

    private var iconColor: Color {
        switch toolCall.state {
        case .failed, .rejected:
            return PincerPalette.danger
        case .succeeded:
            return PincerPalette.success
        case .waitingApproval:
            return PincerPalette.warning
        case .planned, .running:
            return PincerPalette.accent
        }
    }

    private var stateColor: Color {
        switch toolCall.state {
        case .planned:
            return PincerPalette.textSecondary
        case .waitingApproval:
            return PincerPalette.warning
        case .running:
            return PincerPalette.accent
        case .succeeded:
            return PincerPalette.success
        case .failed, .rejected:
            return PincerPalette.danger
        }
    }
}

private struct RunActivityView: View {
    let toolCalls: [ToolCallActivity]
    let assistantDraft: Message?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            if !toolCalls.isEmpty {
                Text("Live Activity")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(PincerPalette.textSecondary)

                ForEach(toolCalls) { toolCall in
                    ToolActivityCard(toolCall: toolCall)
                }
            }

            if let assistantDraft {
                MessageBubble(message: assistantDraft, isStreaming: true)
                    .id(assistantDraft.id)
            }
        }
    }
}

private struct ApprovalTimelineCard: View {
    let approval: Approval

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(approval.tool)
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(PincerPalette.textPrimary)
            Text(approval.deterministicSummary)
                .font(.footnote)
                .foregroundStyle(PincerPalette.textSecondary)
        }
        .cardSurface()
    }
}

private struct ToolActivityCard: View {
    let toolCall: ToolCallActivity

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top, spacing: 12) {
                Image(systemName: "hammer.circle.fill")
                    .foregroundStyle(PincerPalette.accent)

                VStack(alignment: .leading, spacing: 4) {
                    Text(toolCall.displayLabel)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(PincerPalette.textPrimary)
                    Text(stateLabel)
                        .font(.caption.weight(.medium))
                        .foregroundStyle(stateColor)
                }

                Spacer()
            }

            if let argsPreview = toolCall.argsPreview, !argsPreview.isEmpty {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Input")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(PincerPalette.textSecondary)
                    Text(argsPreview)
                        .font(.footnote.monospaced())
                        .foregroundStyle(PincerPalette.textPrimary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(10)
                        .background(PincerPalette.page)
                        .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
                }
            }

            if let outputPreview = toolCall.executions.first?.stdout, !outputPreview.isEmpty {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Output")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(PincerPalette.textSecondary)
                    Text(outputPreview)
                        .font(.footnote.monospaced())
                        .foregroundStyle(PincerPalette.textPrimary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(10)
                        .background(PincerPalette.page)
                        .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
                }
            }
        }
        .cardSurface()
    }

    private var stateLabel: String {
        switch toolCall.state {
        case .planned:
            return "Queued"
        case .waitingApproval:
            return "Waiting Approval"
        case .running:
            return "Running"
        case .succeeded:
            return "Completed"
        case .failed:
            return "Failed"
        case .rejected:
            return "Rejected"
        }
    }

    private var stateColor: Color {
        switch toolCall.state {
        case .planned:
            return PincerPalette.textSecondary
        case .waitingApproval:
            return PincerPalette.warning
        case .running:
            return PincerPalette.accent
        case .succeeded:
            return PincerPalette.success
        case .failed, .rejected:
            return PincerPalette.danger
        }
    }
}

private struct MarkdownMessageText: View {
    let text: String
    let font: Font
    let foregroundColor: Color

    init(_ text: String, font: Font, foregroundStyle: Color) {
        self.text = text
        self.font = font
        self.foregroundColor = foregroundStyle
    }

    var body: some View {
        StructuredText(markdown: text)
            .font(font)
            .foregroundStyle(foregroundColor)
            .textual.textSelection(.enabled)
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
                            Text("Pincer shows the OpenClaw approvals it has observed on this live Gateway connection since launch.")
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
                                HStack(spacing: 10) {
                                    if approval.allowedDecisions.contains("allow-once") {
                                        Button("Allow once") {
                                            Task { await model.resolve(approval.actionID, decision: "allow-once") }
                                        }
                                        .buttonStyle(.borderedProminent)
                                    }

                                    if approval.allowedDecisions.contains("allow-always") {
                                        Button("Always allow") {
                                            Task { await model.resolve(approval.actionID, decision: "allow-always") }
                                        }
                                        .buttonStyle(.bordered)
                                    }

                                    if approval.allowedDecisions.contains("deny") {
                                        Button("Deny", role: .destructive) {
                                            Task { await model.resolve(approval.actionID, decision: "deny") }
                                        }
                                        .buttonStyle(.bordered)
                                    }
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
                await model.start()
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

            SecureField("Required on first connect", text: $model.gatewayToken)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .background(PincerPalette.page)
                .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))

            Text("After the first authenticated connect, Pincer stores the issued device token in Keychain and can reconnect without retyping the shared token.")
                .font(.footnote)
                .foregroundStyle(PincerPalette.textSecondary)
                .fixedSize(horizontal: false, vertical: true)

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
