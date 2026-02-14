import SwiftUI
import UIKit

private enum A11y {
    static let screenChat = "screen_chat"
    static let screenApprovals = "screen_approvals"
    static let screenSchedule = "screen_schedule"
    static let screenJobs = "screen_jobs"
    static let screenSettings = "screen_settings"

    static let tabChat = "tab_chat"
    static let tabApprovals = "tab_approvals"
    static let tabSchedule = "tab_schedule"
    static let tabJobs = "tab_jobs"
    static let tabSettings = "tab_settings"

    static let chatInput = "chat_input"
    static let chatSendButton = "chat_send_button"

    static let approvalsHeading = "approvals_heading"
    static let approvalsApproveFirst = "approval_approve_first"
}

struct ContentView: View {
    @StateObject private var approvalsStore: ApprovalsStore
    @StateObject private var chatModel: ChatViewModel
    @StateObject private var approvalsModel: ApprovalsViewModel
    @StateObject private var settingsModel: SettingsViewModel

    init(client: APIClient) {
        let approvalsStore = ApprovalsStore(client: client)
        _approvalsStore = StateObject(wrappedValue: approvalsStore)
        _chatModel = StateObject(wrappedValue: ChatViewModel(client: client, approvalsStore: approvalsStore))
        _approvalsModel = StateObject(wrappedValue: ApprovalsViewModel(approvalsStore: approvalsStore))
        _settingsModel = StateObject(wrappedValue: SettingsViewModel(client: client))
    }

    var body: some View {
        TabView {
            ChatView(model: chatModel)
                .accessibilityIdentifier(A11y.screenChat)
                .tabItem {
                    Label("Chat", systemImage: "message")
                        .accessibilityIdentifier(A11y.tabChat)
                }

            ApprovalsView(
                model: approvalsModel,
                onApproveSuccess: {
                    await chatModel.refreshAfterApproval()
                }
            )
                .accessibilityIdentifier(A11y.screenApprovals)
                .tabItem {
                    Label("Approvals", systemImage: "checkmark.shield")
                        .accessibilityIdentifier(A11y.tabApprovals)
                }
                .badge(approvalsStore.pendingApprovals.count)

            ScheduleView()
                .accessibilityIdentifier(A11y.screenSchedule)
                .tabItem {
                    Label("Schedule", systemImage: "calendar")
                        .accessibilityIdentifier(A11y.tabSchedule)
                }

            JobsView()
                .accessibilityIdentifier(A11y.screenJobs)
                .tabItem {
                    Label("Jobs", systemImage: "briefcase")
                        .accessibilityIdentifier(A11y.tabJobs)
                }

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
    }
}

private struct ChatView: View {
    @ObservedObject var model: ChatViewModel
    private let chatBottomAnchorID = "chat_bottom_anchor"

    var body: some View {
        NavigationStack {
            ZStack {
                PincerPageBackground()

                VStack(spacing: 10) {
                    ScrollViewReader { reader in
                        ScrollView(showsIndicators: false) {
                            LazyVStack(alignment: .leading, spacing: 10) {
                                if model.messages.isEmpty {
                                    EmptyChatCard()
                                }

                                ForEach(model.messages) { message in
                                    ChatMessageRow(message: message)
                                        .id(message.id)
                                }

                                if !model.inlineApprovals.isEmpty || !model.approvedInlineIndicators.isEmpty {
                                    InlineApprovalsSection(
                                        approvals: model.inlineApprovals,
                                        approvedIndicators: model.approvedInlineIndicators,
                                        approvingActionIDs: model.approvingActionIDs,
                                        onApprove: { actionID in
                                            Task { await model.approveInline(actionID) }
                                        }
                                    )
                                }

                                Color.clear
                                    .frame(height: 1)
                                    .id(chatBottomAnchorID)
                            }
                            .padding(.horizontal, 16)
                            .padding(.top, 10)
                            .padding(.bottom, 6)
                        }
                        .onChange(of: model.messages.count) { _, _ in
                            scrollToBottom(reader)
                        }
                        .onChange(of: model.inlineApprovals.count) { _, newCount in
                            if newCount > 0 {
                                scrollToBottom(reader)
                            }
                        }
                        .onChange(of: model.approvedInlineIndicators.count) { _, newCount in
                            if newCount > 0 {
                                scrollToBottom(reader)
                            }
                        }
                    }

                    ChatComposer(
                        text: $model.input,
                        isBusy: model.isBusy,
                        onSend: { Task { await model.send() } }
                    )
                    .padding(.horizontal, 16)
                    .padding(.bottom, 8)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottom)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            .navigationTitle("Chat")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbarBackground(PincerPalette.page, for: .navigationBar)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Image(systemName: "chevron.left")
                        .foregroundStyle(PincerPalette.textPrimary)
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Image(systemName: "ellipsis")
                        .foregroundStyle(PincerPalette.textPrimary)
                }
            }
            .task {
                await model.bootstrapIfNeeded()
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

    private func scrollToBottom(_ reader: ScrollViewProxy) {
        withAnimation(.easeOut(duration: 0.22)) {
            reader.scrollTo(chatBottomAnchorID, anchor: .bottom)
        }
    }
}

private struct ApprovalsView: View {
    @ObservedObject var model: ApprovalsViewModel
    let onApproveSuccess: () async -> Void

    var body: some View {
        NavigationStack {
            ZStack {
                PincerPageBackground()

                ScrollView(showsIndicators: false) {
                    VStack(alignment: .leading, spacing: 12) {
                        Text("Pending Actions")
                            .font(.system(.title3, design: .rounded).weight(.semibold))
                            .foregroundStyle(PincerPalette.textPrimary)
                            .padding(.horizontal, 16)
                            .padding(.top, 8)
                            .accessibilityIdentifier(A11y.approvalsHeading)

                        if model.approvals.isEmpty {
                            EmptyApprovalsCard()
                                .padding(.horizontal, 16)
                        } else {
                            ForEach(Array(model.approvals.enumerated()), id: \.element.id) { index, item in
                                ApprovalCard(
                                    item: item,
                                    isBusy: model.isBusy,
                                    approveIdentifier: index == 0 ? A11y.approvalsApproveFirst : "approval_approve_\(item.actionID)",
                                    onApprove: {
                                        Task {
                                            await model.approve(item.actionID, onSuccess: onApproveSuccess)
                                        }
                                    }
                                )
                                .padding(.horizontal, 16)
                            }
                        }
                    }
                    .padding(.bottom, 16)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            .navigationTitle("Approvals")
            .navigationBarTitleDisplayMode(.large)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbarBackground(PincerPalette.page, for: .navigationBar)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Image(systemName: "ellipsis")
                        .foregroundStyle(PincerPalette.textPrimary)
                }
            }
            .task {
                await model.refresh()
            }
            .refreshable {
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

private struct ScheduleView: View {
    private let items: [ScheduleItem] = [
        ScheduleItem(
            title: "Market Report Analysis",
            cadence: "Daily, 8:00 AM",
            next: "Next: Tomorrow 8:00 AM"
        ),
        ScheduleItem(
            title: "Research: New Tech Trends",
            cadence: "Every Friday, 2:00 PM",
            next: "Next: Friday 2:00 PM"
        )
    ]

    var body: some View {
        NavigationStack {
            ZStack {
                PincerPageBackground()

                ScrollView(showsIndicators: false) {
                    VStack(spacing: 12) {
                        ForEach(items) { item in
                            ScheduleCard(item: item)
                        }
                    }
                    .padding(.horizontal, 16)
                    .padding(.top, 10)
                    .padding(.bottom, 16)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            .navigationTitle("Schedule")
            .navigationBarTitleDisplayMode(.large)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbarBackground(PincerPalette.page, for: .navigationBar)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Image(systemName: "plus")
                        .foregroundStyle(PincerPalette.textPrimary)
                }
            }
        }
    }
}

private struct JobsView: View {
    private let items: [JobItem] = [
        JobItem(
            title: "Analyzing Market Data",
            status: "In Progress",
            detail: "Researching latest trends and forecasts",
            footer: "Started Today, 7:30 AM",
            isDone: false
        ),
        JobItem(
            title: "Website Summarizer",
            status: "Completed",
            detail: "Summary of the article is ready",
            footer: "Finished Today, 6:15 AM",
            isDone: true
        )
    ]

    var body: some View {
        NavigationStack {
            ZStack {
                PincerPageBackground()

                ScrollView(showsIndicators: false) {
                    VStack(spacing: 12) {
                        ForEach(items) { item in
                            JobCard(item: item)
                        }
                    }
                    .padding(.horizontal, 16)
                    .padding(.top, 10)
                    .padding(.bottom, 16)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            .navigationTitle("Jobs")
            .navigationBarTitleDisplayMode(.large)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbarBackground(PincerPalette.page, for: .navigationBar)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Image(systemName: "ellipsis")
                        .foregroundStyle(PincerPalette.textPrimary)
                }
            }
        }
    }
}

private enum PincerPalette {
    static let page = Color(red: 0.95, green: 0.96, blue: 0.98)
    static let card = Color.white
    static let border = Color.black.opacity(0.06)
    static let shadow = Color.black.opacity(0.06)

    static let textPrimary = Color(red: 0.11, green: 0.15, blue: 0.24)
    static let textSecondary = Color(red: 0.36, green: 0.40, blue: 0.49)
    static let textTertiary = Color(red: 0.54, green: 0.58, blue: 0.67)

    static let accent = Color(red: 0.12, green: 0.45, blue: 0.95)
    static let accentSoft = Color(red: 0.90, green: 0.95, blue: 1.00)
    static let success = Color(red: 0.34, green: 0.60, blue: 0.39)
    static let warning = Color(red: 0.78, green: 0.47, blue: 0.11)
    static let danger = Color(red: 0.77, green: 0.24, blue: 0.24)

    static let terminalBackground = Color(red: 0.06, green: 0.07, blue: 0.09)
    static let terminalBorder = Color.white.opacity(0.14)
    static let terminalText = Color(red: 0.85, green: 0.89, blue: 0.95)
    static let terminalPrompt = Color(red: 0.50, green: 0.88, blue: 0.56)
    static let terminalMuted = Color(red: 0.52, green: 0.57, blue: 0.64)
}

private struct PincerPageBackground: View {
    var body: some View {
        PincerPalette.page
            .ignoresSafeArea()
    }
}

private struct EmptyChatCard: View {
    var body: some View {
        EmptyView()
    }
}

private struct ChatMessageRow: View {
    let message: Message

    private var isUser: Bool { message.role.lowercased() == "user" }
    private var parsedBashExecution: ParsedBashExecutionMessage? {
        guard message.role.lowercased() == "system" else { return nil }
        return parseBashExecutionSystemMessage(message.content)
    }
    private var parsedApprovalStatus: ParsedApprovalStatusMessage? {
        guard message.role.lowercased() == "system" else { return nil }
        return parseApprovalStatusSystemMessage(message.content)
    }

    var body: some View {
        if let parsedApprovalStatus {
            ApprovalStatusSystemRow(
                parsed: parsedApprovalStatus,
                timestamp: shortTimestamp(from: message.createdAt),
                copyText: copyPayload
            )
        } else {
            HStack {
                if isUser { Spacer(minLength: 58) }

                VStack(alignment: .leading, spacing: 6) {
                    if !isUser {
                        Text(roleTitle)
                            .font(.system(.caption, design: .rounded).weight(.semibold))
                            .foregroundStyle(PincerPalette.textTertiary)
                    }

                    if let parsedBashExecution {
                        BashExecutionMessageCard(parsed: parsedBashExecution)
                    } else {
                        Text(message.content)
                            .font(.system(.body, design: .rounded))
                            .foregroundStyle(isUser ? Color.white : PincerPalette.textPrimary)
                    }

                    HStack(spacing: 8) {
                        if !isUser {
                            Text(shortTimestamp(from: message.createdAt))
                                .font(.system(size: 11, weight: .medium, design: .rounded))
                                .foregroundStyle(PincerPalette.textTertiary)
                        }

                        Spacer()

                        CopyIconButton(
                            copyText: copyPayload,
                            tint: isUser ? Color.white.opacity(0.88) : PincerPalette.textTertiary
                        )
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .background(isUser ? PincerPalette.accent : PincerPalette.card)
                .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
                .overlay(
                    RoundedRectangle(cornerRadius: 12, style: .continuous)
                        .stroke(isUser ? Color.clear : PincerPalette.border, lineWidth: 1)
                )
                .shadow(color: PincerPalette.shadow, radius: isUser ? 0 : 6, x: 0, y: 2)

                if !isUser { Spacer(minLength: 58) }
            }
        }
    }

    private var roleTitle: String {
        if parsedBashExecution != nil {
            return "Bash Result"
        }

        switch message.role.lowercased() {
        case "assistant":
            return "Assistant"
        case "system":
            return "System"
        default:
            return "Message"
        }
    }

    private var copyPayload: String {
        if let parsedBashExecution {
            return parsedBashExecution.output
        }
        if let parsedApprovalStatus {
            let tool = parsedApprovalStatus.tool?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            let displayTool = tool.isEmpty ? "Action" : tool
            return "Approval: \(displayTool) \u{2705}"
        }
        return message.content
    }
}

private struct BashExecutionMessageCard: View {
    let parsed: ParsedBashExecutionMessage

    private var statusColor: Color {
        if parsed.timedOut {
            return PincerPalette.warning
        }
        if parsed.exitCode == 0 {
            return PincerPalette.success
        }
        return PincerPalette.danger
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("$ \(parsed.command)")
                .foregroundStyle(PincerPalette.terminalPrompt)

            if let cwd = parsed.cwd, !cwd.isEmpty {
                Text("# cwd: \(cwd)")
                    .foregroundStyle(PincerPalette.terminalMuted)
            }

            Text(parsed.output)
                .foregroundStyle(PincerPalette.terminalText)

            Text(resultLine)
                .foregroundStyle(statusColor)

            if parsed.truncated {
                Text("result: output truncated")
                    .foregroundStyle(PincerPalette.terminalMuted)
            }
        }
        .font(.system(.subheadline, design: .monospaced))
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        .background(PincerPalette.terminalBackground)
        .overlay(
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .stroke(PincerPalette.terminalBorder, lineWidth: 1)
        )
        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        .textSelection(.enabled)
    }

    private var resultLine: String {
        if parsed.timedOut {
            return "result: timed out (\(parsed.durationMillis)ms)"
        }
        return "result: exit \(parsed.exitCode) (\(parsed.durationMillis)ms)"
    }
}

private struct ApprovalStatusSystemRow: View {
    let parsed: ParsedApprovalStatusMessage
    let timestamp: String
    let copyText: String

    var body: some View {
        ZStack(alignment: .bottomTrailing) {
            VStack(spacing: 2) {
                Text("Approval: \(displayTool) \u{2705}")
                    .font(.system(.footnote, design: .rounded).weight(.semibold))
                    .foregroundStyle(PincerPalette.textSecondary)

                Text(timestamp)
                    .font(.system(size: 11, weight: .medium, design: .rounded))
                    .foregroundStyle(PincerPalette.textTertiary)
            }
            .frame(maxWidth: .infinity, alignment: .center)

            CopyIconButton(copyText: copyText, tint: PincerPalette.textTertiary)
                .padding(.trailing, 2)
        }
        .padding(.vertical, 2)
    }

    private var displayTool: String {
        let tool = parsed.tool?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if tool.isEmpty {
            return "Action"
        }
        return tool
    }
}

private struct CopyIconButton: View {
    let copyText: String
    let tint: Color

    var body: some View {
        Button(action: {
            UIPasteboard.general.string = copyText
        }) {
            Image(systemName: "doc.on.doc")
                .font(.system(size: 11, weight: .semibold))
                .foregroundStyle(tint)
                .frame(width: 18, height: 18)
                .padding(2)
        }
        .buttonStyle(.plain)
    }
}

private struct ChatComposer: View {
    @Binding var text: String
    let isBusy: Bool
    let onSend: () -> Void

    private var canSend: Bool {
        !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && !isBusy
    }

    var body: some View {
        HStack(spacing: 8) {
            TextField("Message...", text: $text)
                .font(.system(.body, design: .rounded))
                .foregroundStyle(PincerPalette.textPrimary)
                .padding(.horizontal, 14)
                .padding(.vertical, 10)
                .background(PincerPalette.card)
                .clipShape(Capsule())
                .overlay(
                    Capsule()
                        .stroke(PincerPalette.border, lineWidth: 1)
                )
                .submitLabel(.send)
                .onSubmit {
                    if canSend {
                        onSend()
                    }
                }
                .accessibilityIdentifier(A11y.chatInput)

            Button(action: onSend) {
                Image(systemName: canSend ? "paperplane.fill" : "mic.fill")
                    .font(.system(size: 16, weight: .bold))
                    .frame(width: 36, height: 36)
                    .background(canSend ? PincerPalette.accent : PincerPalette.card)
                    .foregroundStyle(canSend ? Color.white : PincerPalette.textSecondary)
                    .clipShape(Circle())
                    .overlay(
                        Circle()
                            .stroke(PincerPalette.border, lineWidth: canSend ? 0 : 1)
                    )
            }
            .disabled(!canSend)
            .accessibilityIdentifier(A11y.chatSendButton)
        }
        .padding(6)
        .background(PincerPalette.page)
        .clipShape(Capsule())
    }
}

private struct EmptyApprovalsCard: View {
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("No pending actions")
                .font(.system(.headline, design: .rounded).weight(.semibold))
                .foregroundStyle(PincerPalette.textPrimary)

            Text("New external actions from Chat will show up here for explicit approval.")
                .font(.system(.subheadline, design: .rounded))
                .foregroundStyle(PincerPalette.textSecondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .cardSurface()
    }
}

private struct InlineApprovalsSection: View {
    let approvals: [Approval]
    let approvedIndicators: [ApprovedInlineIndicator]
    let approvingActionIDs: Set<String>
    let onApprove: (String) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Actions in this chat")
                .font(.system(.footnote, design: .rounded).weight(.semibold))
                .foregroundStyle(PincerPalette.textSecondary)

            ForEach(approvals) { item in
                HStack(alignment: .top, spacing: 10) {
                    VStack(alignment: .leading, spacing: 4) {
                        Text(prettyToolName(item.tool))
                            .font(.system(.subheadline, design: .rounded).weight(.semibold))
                            .foregroundStyle(PincerPalette.textPrimary)

                        Text("Risk: \(item.riskClass.capitalized)")
                            .font(.system(.caption, design: .rounded))
                            .foregroundStyle(PincerPalette.textSecondary)
                    }

                    Spacer()

                    Button(action: { onApprove(item.actionID) }) {
                        Text(approvingActionIDs.contains(item.actionID) ? "Approving..." : "Approve")
                            .font(.system(.caption, design: .rounded).weight(.semibold))
                            .foregroundStyle(PincerPalette.accent)
                            .padding(.horizontal, 10)
                            .padding(.vertical, 6)
                            .background(PincerPalette.accentSoft)
                            .clipShape(Capsule())
                    }
                    .disabled(approvingActionIDs.contains(item.actionID))
                    .accessibilityIdentifier("chat_inline_approve_\(item.actionID)")
                }
                .padding(.vertical, 2)
            }

            ForEach(approvedIndicators) { item in
                HStack(alignment: .top, spacing: 10) {
                    Image(systemName: "checkmark.circle.fill")
                        .font(.system(size: 16, weight: .semibold))
                        .foregroundStyle(PincerPalette.success)

                    VStack(alignment: .leading, spacing: 4) {
                        Text(prettyToolName(item.tool))
                            .font(.system(.subheadline, design: .rounded).weight(.semibold))
                            .foregroundStyle(PincerPalette.textPrimary)

                        Text("Approved. Waiting for execution...")
                            .font(.system(.caption, design: .rounded))
                            .foregroundStyle(PincerPalette.textSecondary)
                    }

                    Spacer()
                }
                .padding(.vertical, 2)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .cardSurface()
    }
}

private struct ApprovalCard: View {
    let item: Approval
    let isBusy: Bool
    let approveIdentifier: String
    let onApprove: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(prettyToolName(item.tool))
                .font(.system(.title3, design: .rounded).weight(.semibold))
                .foregroundStyle(PincerPalette.textPrimary)
                .accessibilityIdentifier("approval_card_\(item.actionID)")

            Text("Risk: \(item.riskClass.capitalized)")
                .font(.system(.subheadline, design: .rounded))
                .foregroundStyle(PincerPalette.textSecondary)

            Text("Today, \(shortTimestamp(from: item.createdAt))")
                .font(.system(.subheadline, design: .rounded))
                .foregroundStyle(PincerPalette.textSecondary)

            Divider()

            HStack(spacing: 10) {
                Button(action: onApprove) {
                    Text(isBusy ? "Approving..." : "Approve")
                        .font(.system(.title3, design: .rounded).weight(.semibold))
                        .foregroundStyle(PincerPalette.accent)
                }
                .disabled(isBusy)
                .accessibilityIdentifier(approveIdentifier)

                Text("|")
                    .foregroundStyle(PincerPalette.textTertiary)

                Button(action: {}) {
                    Text("View")
                        .font(.system(.title3, design: .rounded))
                        .foregroundStyle(PincerPalette.accent)
                }

                Spacer()

                Image(systemName: "chevron.right")
                    .foregroundStyle(PincerPalette.textTertiary)
            }
        }
        .cardSurface()
    }
}

private struct SettingsView: View {
    @ObservedObject var model: SettingsViewModel
    @State private var pendingRevokeDevice: Device?

    var body: some View {
        NavigationStack {
            ZStack {
                PincerPageBackground()

                ScrollView(showsIndicators: false) {
                    VStack(alignment: .leading, spacing: 12) {
                        Text("Paired Devices")
                            .font(.system(.title3, design: .rounded).weight(.semibold))
                            .foregroundStyle(PincerPalette.textPrimary)
                            .padding(.horizontal, 16)
                            .padding(.top, 8)

                        if model.devices.isEmpty {
                            Text("No devices found.")
                                .font(.system(.subheadline, design: .rounded))
                                .foregroundStyle(PincerPalette.textSecondary)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .cardSurface()
                                .padding(.horizontal, 16)
                        } else {
                            ForEach(model.devices) { device in
                                DeviceCard(
                                    device: device,
                                    isBusy: model.isBusy,
                                    onRevoke: { pendingRevokeDevice = device }
                                )
                                .padding(.horizontal, 16)
                            }
                        }
                    }
                    .padding(.bottom, 16)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.large)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbarBackground(PincerPalette.page, for: .navigationBar)
            .task {
                await model.refresh()
            }
            .refreshable {
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
            .alert("Revoke Device?", isPresented: Binding(
                get: { pendingRevokeDevice != nil },
                set: { if !$0 { pendingRevokeDevice = nil } }
            )) {
                Button("Cancel", role: .cancel) {}
                Button("Revoke", role: .destructive) {
                    guard let device = pendingRevokeDevice else { return }
                    pendingRevokeDevice = nil
                    Task { await model.revoke(device.deviceID) }
                }
            } message: {
                if let device = pendingRevokeDevice {
                    if device.isCurrent {
                        Text("This will revoke your current session and you will be paired again automatically.")
                    } else {
                        Text("This device will lose access immediately.")
                    }
                }
            }
        }
    }
}

private struct DeviceCard: View {
    let device: Device
    let isBusy: Bool
    let onRevoke: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(device.name)
                .font(.system(.title3, design: .rounded).weight(.semibold))
                .foregroundStyle(PincerPalette.textPrimary)

            Text("Device ID: \(device.deviceID)")
                .font(.system(.footnote, design: .rounded))
                .foregroundStyle(PincerPalette.textSecondary)
                .lineLimit(1)
                .truncationMode(.middle)

            Text("Paired: \(shortTimestamp(from: device.createdAt))")
                .font(.system(.subheadline, design: .rounded))
                .foregroundStyle(PincerPalette.textSecondary)

            if device.isRevoked {
                Text("Revoked")
                    .font(.system(.subheadline, design: .rounded).weight(.semibold))
                    .foregroundStyle(PincerPalette.textTertiary)
            } else {
                if device.isCurrent {
                    Text("This device")
                        .font(.system(.footnote, design: .rounded).weight(.semibold))
                        .foregroundStyle(PincerPalette.accent)
                }
                Button(action: onRevoke) {
                    Text(isBusy ? "Revoking..." : "Revoke")
                        .font(.system(.subheadline, design: .rounded).weight(.semibold))
                        .foregroundStyle(.red)
                }
                .disabled(isBusy)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .cardSurface()
    }
}

private struct ScheduleItem: Identifiable {
    let id = UUID()
    let title: String
    let cadence: String
    let next: String
}

private struct ScheduleCard: View {
    let item: ScheduleItem

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(item.title)
                .font(.system(.title3, design: .rounded).weight(.semibold))
                .foregroundStyle(PincerPalette.textPrimary)

            Text(item.cadence)
                .font(.system(.title3, design: .rounded))
                .foregroundStyle(PincerPalette.textSecondary)

            Text(item.next)
                .font(.system(.subheadline, design: .rounded))
                .foregroundStyle(PincerPalette.textSecondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .cardSurface()
    }
}

private struct JobItem: Identifiable {
    let id = UUID()
    let title: String
    let status: String
    let detail: String
    let footer: String
    let isDone: Bool
}

private struct JobCard: View {
    let item: JobItem

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: item.isDone ? "checkmark.circle.fill" : "circle")
                .font(.system(size: 20, weight: .semibold))
                .foregroundStyle(item.isDone ? PincerPalette.success : PincerPalette.textTertiary)

            VStack(alignment: .leading, spacing: 4) {
                Text(item.title)
                    .font(.system(.title3, design: .rounded).weight(.semibold))
                    .foregroundStyle(PincerPalette.textPrimary)

                Text(item.status)
                    .font(.system(.title3, design: .rounded))
                    .foregroundStyle(PincerPalette.textSecondary)

                Text(item.detail)
                    .font(.system(.subheadline, design: .rounded))
                    .foregroundStyle(PincerPalette.textSecondary)

                Text(item.footer)
                    .font(.system(.subheadline, design: .rounded))
                    .foregroundStyle(PincerPalette.textSecondary)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .cardSurface()
    }
}

private struct CardSurfaceModifier: ViewModifier {
    func body(content: Content) -> some View {
        content
            .padding(14)
            .background(PincerPalette.card)
            .overlay(
                RoundedRectangle(cornerRadius: 12, style: .continuous)
                    .stroke(PincerPalette.border, lineWidth: 1)
            )
            .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
            .shadow(color: PincerPalette.shadow, radius: 8, x: 0, y: 2)
    }
}

private extension View {
    func cardSurface() -> some View {
        modifier(CardSurfaceModifier())
    }
}

private struct ParsedBashExecutionMessage {
    let command: String
    let exitCode: Int
    let durationMillis: Int
    let cwd: String?
    let output: String
    let timedOut: Bool
    let truncated: Bool
}

private struct ParsedApprovalStatusMessage {
    let actionID: String
    let tool: String?
    let status: String?
}

private func parseApprovalStatusSystemMessage(_ content: String) -> ParsedApprovalStatusMessage? {
    let lines = content.components(separatedBy: "\n")
    guard let headline = lines.first else { return nil }

    let prefix = "Action "
    let suffix = " approved."
    guard headline.hasPrefix(prefix), headline.hasSuffix(suffix) else { return nil }

    let actionID = String(
        headline
            .dropFirst(prefix.count)
            .dropLast(suffix.count)
    ).trimmingCharacters(in: .whitespaces)
    guard !actionID.isEmpty else { return nil }

    var tool: String?
    var status: String?
    for line in lines.dropFirst() {
        if line.hasPrefix("Tool: ") {
            tool = String(line.dropFirst("Tool: ".count)).trimmingCharacters(in: .whitespaces)
        } else if line.hasPrefix("Status: ") {
            status = String(line.dropFirst("Status: ".count)).trimmingCharacters(in: .whitespaces)
        }
    }

    return ParsedApprovalStatusMessage(actionID: actionID, tool: tool, status: status)
}

private func parseBashExecutionSystemMessage(_ content: String) -> ParsedBashExecutionMessage? {
    let lines = content.components(separatedBy: "\n")
    guard lines.count >= 4 else { return nil }
    guard lines[0].hasPrefix("Action "), lines[0].hasSuffix(" executed.") else { return nil }
    guard lines[1].hasPrefix("Command: ") else { return nil }
    guard lines[2].hasPrefix("Exit code: ") else { return nil }

    let command = String(lines[1].dropFirst("Command: ".count)).trimmingCharacters(in: .whitespaces)
    let exitRaw = String(lines[2].dropFirst("Exit code: ".count)).trimmingCharacters(in: .whitespaces)
    guard let exitCode = Int(exitRaw) else { return nil }

    var durationMillis = 0
    var cwd: String?
    var timedOut = false
    var truncated = false
    var outputStart = -1

    for (idx, line) in lines.enumerated() {
        if line == "Output:" {
            outputStart = idx + 1
            break
        }
        if line.hasPrefix("Duration: ") {
            let durationValue = line
                .dropFirst("Duration: ".count)
                .replacingOccurrences(of: "ms", with: "")
                .trimmingCharacters(in: .whitespaces)
            if let parsed = Int(durationValue) {
                durationMillis = parsed
            }
        } else if line.hasPrefix("CWD: ") {
            cwd = String(line.dropFirst("CWD: ".count)).trimmingCharacters(in: .whitespaces)
        } else if line == "Timed out: true" {
            timedOut = true
        } else if line == "Output truncated: true" {
            truncated = true
        }
    }

    guard outputStart >= 0, outputStart <= lines.count else { return nil }
    let outputLines: [String]
    if outputStart >= lines.count {
        outputLines = []
    } else {
        outputLines = Array(lines[outputStart...])
    }
    let output = outputLines.joined(separator: "\n")

    return ParsedBashExecutionMessage(
        command: command,
        exitCode: exitCode,
        durationMillis: durationMillis,
        cwd: cwd,
        output: output,
        timedOut: timedOut,
        truncated: truncated
    )
}

private func prettyToolName(_ raw: String) -> String {
    raw
        .replacingOccurrences(of: "_", with: " ")
        .replacingOccurrences(of: "demo external notify", with: "Send External Follow-up")
        .capitalized
}

private func shortTimestamp(from iso: String) -> String {
    let parserWithFraction = ISO8601DateFormatter()
    parserWithFraction.formatOptions = [.withInternetDateTime, .withFractionalSeconds]

    let parser = ISO8601DateFormatter()

    guard let date = parserWithFraction.date(from: iso) ?? parser.date(from: iso) else {
        return iso
    }

    let out = DateFormatter()
    out.dateFormat = "h:mm a"
    return out.string(from: date)
}
