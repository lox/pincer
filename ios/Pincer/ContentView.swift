import QuickLook
import SwiftUI
import Textual
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

    static let settingsBackendURLInput = "settings_backend_url_input"
    static let settingsBackendSaveButton = "settings_backend_save_button"
    static let settingsBackendResetButton = "settings_backend_reset_button"
}

struct ContentView: View {
    @StateObject private var approvalsStore: ApprovalsStore
    @StateObject private var chatModel: ChatViewModel
    @StateObject private var approvalsModel: ApprovalsViewModel
    @StateObject private var settingsModel: SettingsViewModel
    private let client: APIClient

    init(client: APIClient) {
        self.client = client
        let approvalsStore = ApprovalsStore(client: client)
        _approvalsStore = StateObject(wrappedValue: approvalsStore)
        _chatModel = StateObject(wrappedValue: ChatViewModel(client: client, approvalsStore: approvalsStore))
        _approvalsModel = StateObject(wrappedValue: ApprovalsViewModel(approvalsStore: approvalsStore))
        _settingsModel = StateObject(wrappedValue: SettingsViewModel(client: client))
    }

    var body: some View {
        TabView {
            ChatNavigationView(client: client, model: chatModel)
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

private struct ChatNavigationView: View {
    let client: APIClient
    @ObservedObject var model: ChatViewModel
    @State private var threads: [ThreadSummary] = []
    @State private var isLoadingThreads = false
    @State private var threadListError: String?
    @State private var path = NavigationPath()

    var body: some View {
        NavigationStack(path: $path) {
            ThreadListView(
                threads: threads,
                isLoading: isLoadingThreads,
                errorText: threadListError,
                onDismissError: { threadListError = nil },
                onSelect: { thread in
                    Task {
                        await model.loadThread(thread.threadID, title: thread.displayTitle)
                        path.append("chat")
                    }
                },
                onNewChat: {
                    Task {
                        await model.startNewThread()
                        path.append("chat")
                    }
                },
                onDelete: { thread in
                    Task { await deleteThread(thread.threadID) }
                },
                onRefresh: { await loadThreads() }
            )
            .navigationDestination(for: String.self) { _ in
                ChatDetailView(
                    model: model,
                    onNewChat: {
                        Task {
                            await model.startNewThread()
                        }
                    },
                    onDelete: {
                        guard let tid = model.threadID else { return }
                        path.removeLast(path.count)
                        Task { await deleteThread(tid) }
                    }
                )
            }
            .onChange(of: path.count) { oldCount, newCount in
                if newCount == 0 && oldCount > 0 {
                    Task { await loadThreads() }
                }
            }
        }
    }

    private func loadThreads() async {
        isLoadingThreads = true
        defer { isLoadingThreads = false }
        do {
            threads = try await client.listThreads()
            threadListError = nil
        } catch {
            threadListError = userFacingErrorMessage(error, fallback: "Failed to load threads.")
        }
    }

    private func deleteThread(_ threadID: String) async {
        do {
            try await client.deleteThread(threadID: threadID)
            threads.removeAll { $0.threadID == threadID }
        } catch {
            threadListError = userFacingErrorMessage(error, fallback: "Failed to delete thread.")
        }
    }
}

private struct ThreadListView: View {
    let threads: [ThreadSummary]
    let isLoading: Bool
    let errorText: String?
    let onDismissError: () -> Void
    let onSelect: (ThreadSummary) -> Void
    let onNewChat: () -> Void
    let onDelete: (ThreadSummary) -> Void
    let onRefresh: () async -> Void

    var body: some View {
        Group {
            if threads.isEmpty && !isLoading {
                VStack(spacing: 12) {
                    Image(systemName: "message")
                        .font(.system(size: 36))
                        .foregroundStyle(PincerPalette.textTertiary)
                    Text("No conversations yet")
                        .font(.system(.body, design: .rounded))
                        .foregroundStyle(PincerPalette.textSecondary)
                    Button(action: onNewChat) {
                        Text("Start a conversation")
                            .font(.system(.subheadline, design: .rounded).weight(.semibold))
                            .foregroundStyle(PincerPalette.accent)
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                ScrollView(showsIndicators: false) {
                    VStack(spacing: 10) {
                        ForEach(threads) { thread in
                            ThreadRow(thread: thread, onTap: { onSelect(thread) })
                                .contextMenu {
                                    Button(role: .destructive) {
                                        onDelete(thread)
                                    } label: {
                                        Label("Delete", systemImage: "trash")
                                    }
                                    Button {
                                        UIPasteboard.general.string = thread.threadID
                                    } label: {
                                        Label("Copy Thread ID", systemImage: "doc.on.doc")
                                    }
                                }
                        }
                    }
                    .padding(.horizontal, 16)
                    .padding(.top, 10)
                    .padding(.bottom, 16)
                }
            }
        }
        .background(PincerPalette.page)
        .navigationTitle("Chat")
        .navigationBarTitleDisplayMode(.large)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button(action: onNewChat) {
                    Image(systemName: "square.and.pencil")
                        .foregroundStyle(PincerPalette.accent)
                }
            }
        }
        .task { await onRefresh() }
        .refreshable { await onRefresh() }
        .alert("Error", isPresented: Binding(
            get: { errorText != nil },
            set: { if !$0 { onDismissError() } }
        )) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(errorText ?? "Unknown error")
        }
    }
}

private struct ThreadRow: View {
    let thread: ThreadSummary
    let onTap: () -> Void

    var body: some View {
        Button(action: onTap) {
            VStack(alignment: .leading, spacing: 4) {
                Text(thread.displayTitle)
                    .font(.system(.body, design: .rounded).weight(.medium))
                    .foregroundStyle(PincerPalette.textPrimary)
                    .lineLimit(2)

                HStack(spacing: 8) {
                    if thread.messageCount > 0 {
                        Text("\(thread.messageCount) messages")
                            .font(.system(.caption, design: .rounded))
                            .foregroundStyle(PincerPalette.textTertiary)
                    }
                    if !thread.updatedAt.isEmpty {
                        Text(relativeTimestamp(from: thread.updatedAt))
                            .font(.system(.caption, design: .rounded))
                            .foregroundStyle(PincerPalette.textTertiary)
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .cardSurface()
        }
        .buttonStyle(.plain)
    }
}

private struct ChatDetailView: View {
    @ObservedObject var model: ChatViewModel
    let onNewChat: () -> Void
    let onDelete: () -> Void
    @State private var previewURL: URL?
    @State private var isDownloadingAttachment = false
    private let chatBottomAnchorID = "chat_bottom_anchor"

    var body: some View {
        ZStack {
            PincerPageBackground()

            VStack(spacing: 10) {
                ScrollViewReader { reader in
                    ScrollView(showsIndicators: false) {
                        VStack(alignment: .leading, spacing: 10) {
                            if model.timelineItems.isEmpty {
                                EmptyChatCard()
                            }

                            ForEach(model.timelineItems) { item in
                                switch item {
                                case .message(let message):
                                    ChatMessageRow(message: message)
                                        .id(item.id)
                                case .approval(let approval):
                                    InlineApprovalRow(
                                        approval: approval,
                                        isApproving: model.approvingActionIDs.contains(approval.actionID),
                                        onApprove: {
                                            Task { await model.approveInline(approval.actionID) }
                                        }
                                    )
                                    .id(item.id)
                                }
                            }

                            if model.inlineApprovals.count > 1 {
                                Button(action: {
                                    Task { await model.approveAllInline() }
                                }) {
                                    Text("Approve All (\(model.inlineApprovals.count))")
                                        .font(.system(.caption, design: .rounded).weight(.semibold))
                                        .foregroundStyle(PincerPalette.accent)
                                        .padding(.horizontal, 12)
                                        .padding(.vertical, 6)
                                        .background(PincerPalette.accentSoft)
                                        .clipShape(Capsule())
                                }
                                .frame(maxWidth: .infinity, alignment: .trailing)
                            }

                            if model.isAwaitingAssistantProgress {
                                AssistantProcessingRow()
                                    .id("assistant_processing_row")
                            }

                            Color.clear
                                .frame(height: 1)
                                .id(chatBottomAnchorID)
                        }
                        .padding(.horizontal, 16)
                        .padding(.top, 10)
                        .padding(.bottom, 6)
                    }
                    .scrollDismissesKeyboard(.interactively)
                    .onChange(of: model.timelineItems.count) { _, _ in
                        scrollToBottom(reader)
                    }
                    .onChange(of: model.isAwaitingAssistantProgress) { _, isAwaiting in
                        if isAwaiting {
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
        .navigationTitle(model.threadTitle.isEmpty ? "Chat" : model.threadTitle)
        .navigationBarTitleDisplayMode(.inline)

        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    Button(action: onNewChat) {
                        Label("New Chat", systemImage: "square.and.pencil")
                    }
                    Button {
                        if let tid = model.threadID {
                            UIPasteboard.general.string = tid
                        }
                    } label: {
                        Label("Copy Thread ID", systemImage: "doc.on.doc")
                    }
                    Divider()
                    Button(role: .destructive, action: onDelete) {
                        Label("Delete Thread", systemImage: "trash")
                    }
                } label: {
                    Image(systemName: "ellipsis")
                        .foregroundStyle(PincerPalette.textPrimary)
                }
            }
        }
        .alert("Error", isPresented: Binding(
            get: { model.errorText != nil },
            set: { if !$0 { model.errorText = nil } }
        )) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(model.errorText ?? "Unknown error")
        }
        .environment(\.openURL, OpenURLAction { url in
            if url.path == "/proxy/gmail/attachment" || url.path.hasPrefix("/proxy/gmail/attachment") {
                Task { await downloadAndPreviewAttachment(url) }
                return .handled
            }
            return .systemAction
        })
        .quickLookPreview($previewURL)
    }

    private func downloadAndPreviewAttachment(_ url: URL) async {
        guard !isDownloadingAttachment else { return }
        isDownloadingAttachment = true
        defer { isDownloadingAttachment = false }

        var components = URLComponents(url: AppConfig.baseURL, resolvingAgainstBaseURL: false)!
        components.path = url.path
        components.query = url.query
        guard let fullURL = components.url else { return }

        var request = URLRequest(url: fullURL)
        let token = AppConfig.bearerToken
        if !token.isEmpty {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }

        do {
            let (data, response) = try await URLSession.shared.data(for: request)
            guard let httpResponse = response as? HTTPURLResponse,
                  (200..<300).contains(httpResponse.statusCode) else { return }

            let queryItems = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems
            let filename = queryItems?.first(where: { $0.name == "filename" })?.value ?? "attachment"

            let tempDir = FileManager.default.temporaryDirectory
                .appendingPathComponent("pincer_attachments", isDirectory: true)
            try FileManager.default.createDirectory(at: tempDir, withIntermediateDirectories: true)
            let fileURL = tempDir.appendingPathComponent(filename)
            try data.write(to: fileURL)

            await MainActor.run {
                previewURL = fileURL
            }
        } catch {
            // Download failed silently
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
                .scrollDismissesKeyboard(.interactively)
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
    private var isSystem: Bool { message.role.lowercased() == "system" }
    private var isThinking: Bool { message.role.lowercased() == "thinking" }
    private var isTool: Bool { message.role.lowercased() == "tool" }

    var body: some View {
        if isSystem {
            Text(message.content)
                .font(.system(.caption, design: .rounded).weight(.medium))
                .foregroundStyle(PincerPalette.textTertiary)
                .frame(maxWidth: .infinity, alignment: .center)
                .padding(.vertical, 2)
        } else if isThinking {
            HStack {
                FallbackThinkingRow(text: message.content)
                Spacer(minLength: 58)
            }
        } else if isTool {
            HStack {
                FallbackToolOutputRow(content: message.content)
                Spacer(minLength: 58)
            }
        } else {
            HStack {
                if isUser { Spacer(minLength: 58) }

                VStack(alignment: .leading, spacing: 6) {
                    if !isUser {
                        Text("Assistant")
                            .font(.system(.caption, design: .rounded).weight(.semibold))
                            .foregroundStyle(PincerPalette.textTertiary)
                    }

                    if isUser {
                        Text(message.content)
                            .font(.system(.body, design: .rounded))
                            .foregroundStyle(Color.white)
                    } else {
                        MarkdownMessageText(
                            message.content,
                            font: .system(.body, design: .rounded),
                            foregroundStyle: PincerPalette.textPrimary
                        )
                    }

                    HStack(spacing: 8) {
                        if !isUser {
                            Text(shortTimestamp(from: message.createdAt))
                                .font(.system(size: 11, weight: .medium, design: .rounded))
                                .foregroundStyle(PincerPalette.textTertiary)
                        }

                        Spacer()

                        CopyIconButton(
                            copyText: message.content,
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
}

private struct FallbackThinkingRow: View {
    let text: String
    @State private var isExpanded = false

    var body: some View {
        DisclosureGroup(isExpanded: $isExpanded) {
            MarkdownMessageText(
                text,
                font: .system(.caption, design: .rounded),
                foregroundStyle: PincerPalette.textTertiary
            )
            .padding(.top, 2)
        } label: {
            HStack(spacing: 4) {
                Image(systemName: "checkmark")
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundStyle(PincerPalette.success)
                Text("Thinking")
                    .font(.system(.caption, design: .rounded).weight(.medium))
                    .foregroundStyle(PincerPalette.textSecondary)
            }
        }
        .padding(10)
        .background(PincerPalette.card)
        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .stroke(PincerPalette.border, lineWidth: 1)
        )
    }
}

private struct FallbackToolOutputRow: View {
    let content: String

    var body: some View {
        let parsed = parseToolExecutionStreamingContent(content)
        if parsed.isBashCommand {
            BashToolOutputCard(parsed: parsed)
        } else {
            ReadToolOutputCard(parsed: parsed)
        }
    }
}

/// Expanded terminal-style card for run_bash commands.
private struct BashToolOutputCard: View {
    let parsed: ParsedToolExecutionStreamingContent

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Image(systemName: parsed.result != nil ? "checkmark" : "play.fill")
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundStyle(parsed.result != nil ? PincerPalette.success : PincerPalette.accent)

                Text(parsed.command ?? "Tool Output")
                    .font(.system(.caption, design: .monospaced).weight(.medium))
                    .foregroundStyle(PincerPalette.textPrimary)
                    .lineLimit(1)
            }

            if !parsed.output.isEmpty {
                Text(parsed.output)
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundStyle(PincerPalette.terminalText)
                    .lineLimit(6)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(6)
                    .background(PincerPalette.terminalBackground)
                    .clipShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
            }

            if let result = parsed.result {
                Text(result.line)
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundStyle(resultColor(result))
            }
        }
        .padding(10)
        .background(PincerPalette.card)
        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .stroke(PincerPalette.border, lineWidth: 1)
        )
    }

    private func resultColor(_ result: ToolExecutionResult) -> Color {
        switch result {
        case .exit(let code, _): return code == 0 ? PincerPalette.success : PincerPalette.danger
        case .timedOut: return PincerPalette.warning
        }
    }
}

/// Compact collapsed card for READ tools (gmail_search, web_search, etc.).
private struct ReadToolOutputCard: View {
    let parsed: ParsedToolExecutionStreamingContent
    @State private var isExpanded = false

    private var isDone: Bool { parsed.result != nil }
    private var isError: Bool {
        if case .exit(let code, _) = parsed.result { return code != 0 }
        if case .timedOut = parsed.result { return true }
        return false
    }

    var body: some View {
        DisclosureGroup(isExpanded: $isExpanded) {
            if !parsed.output.isEmpty {
                Text(parsed.output)
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundStyle(PincerPalette.terminalText)
                    .lineLimit(20)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(6)
                    .background(PincerPalette.terminalBackground)
                    .clipShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
                    .padding(.top, 2)
            }
        } label: {
            HStack(spacing: 6) {
                if isDone {
                    Image(systemName: isError ? "xmark" : "checkmark")
                        .font(.system(size: 10, weight: .semibold))
                        .foregroundStyle(isError ? PincerPalette.danger : PincerPalette.success)
                } else {
                    ProgressView()
                        .controlSize(.mini)
                }
                Text(parsed.readToolSummary)
                    .font(.system(.caption, design: .rounded).weight(.medium))
                    .foregroundStyle(PincerPalette.textSecondary)
                    .lineLimit(1)
                if let result = parsed.result {
                    Spacer()
                    Text(durationLabel(result))
                        .font(.system(.caption2, design: .rounded))
                        .foregroundStyle(PincerPalette.textTertiary)
                }
            }
        }
        .padding(10)
        .background(PincerPalette.card)
        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .stroke(PincerPalette.border, lineWidth: 1)
        )
    }

    private func durationLabel(_ result: ToolExecutionResult) -> String {
        switch result {
        case .exit(_, let ms): return formatDuration(ms)
        case .timedOut(let ms): return "timed out (\(formatDuration(ms)))"
        }
    }

    private func formatDuration(_ ms: Int) -> String {
        if ms >= 1000 {
            let seconds = Double(ms) / 1000.0
            return String(format: "%.1fs", seconds)
        }
        return "\(ms)ms"
    }
}

private struct AssistantProcessingRow: View {
    @State private var phase: CGFloat = 0

    var body: some View {
        HStack {
            HStack(spacing: 5) {
                ForEach(0..<3) { index in
                    Circle()
                        .fill(PincerPalette.textTertiary)
                        .frame(width: 6, height: 6)
                        .opacity(dotOpacity(for: index))
                        .scaleEffect(dotScale(for: index))
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)
            .background(PincerPalette.card)
            .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 16, style: .continuous)
                    .stroke(PincerPalette.border, lineWidth: 1)
            )

            Spacer()
        }
        .onAppear {
            withAnimation(.linear(duration: 1.2).repeatForever(autoreverses: false)) {
                phase = 1
            }
        }
    }

    private func dotOpacity(for index: Int) -> Double {
        let offset = Double(index) / 3.0
        let wave = sin((Double(phase) - offset) * .pi * 2)
        return 0.3 + 0.7 * max(0, wave)
    }

    private func dotScale(for index: Int) -> CGFloat {
        let offset = Double(index) / 3.0
        let wave = sin((Double(phase) - offset) * .pi * 2)
        return 0.7 + 0.3 * CGFloat(max(0, wave))
    }
}

private struct InlineApprovalRow: View {
    let approval: Approval
    let isApproving: Bool
    let onApprove: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: "clock")
                .font(.system(size: 10, weight: .semibold))
                .foregroundStyle(PincerPalette.warning)
                .frame(width: 14)

            Text(approval.tool)
                .font(.system(.caption, design: .monospaced).weight(.medium))
                .foregroundStyle(PincerPalette.textPrimary)

            if !approval.deterministicSummary.isEmpty {
                Text(approval.deterministicSummary)
                    .font(.system(.caption, design: .rounded))
                    .foregroundStyle(PincerPalette.textTertiary)
                    .lineLimit(1)
            }

            Spacer()

            Button(action: onApprove) {
                Text(isApproving ? "Approving..." : "Approve")
                    .font(.system(.caption2, design: .rounded).weight(.semibold))
                    .foregroundStyle(PincerPalette.accent)
                    .padding(.horizontal, 8)
                    .padding(.vertical, 4)
                    .background(PincerPalette.accentSoft)
                    .clipShape(Capsule())
            }
            .disabled(isApproving)
            .accessibilityIdentifier("inline_approve_\(approval.actionID)")
        }
        .padding(10)
        .background(PincerPalette.card)
        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .stroke(PincerPalette.border, lineWidth: 1)
        )
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

    private var backendBaseURL: URL { AppConfig.baseURL }

    var body: some View {
        StructuredText(markdown: Self.escapeLeadingListMarkers(text), baseURL: backendBaseURL)
            .font(font)
            .foregroundStyle(foregroundColor)
            .textual.textSelection(.enabled)
            .textual.imageAttachmentLoader(.image(relativeTo: backendBaseURL))
    }

    /// Escape leading ordered list markers so CommonMark doesn't
    /// swallow them as block-level markup (e.g. "4." → empty list item).
    private static func escapeLeadingListMarkers(_ input: String) -> String {
        input.replacing(
            /(?m)^(\d{1,9})([.)])/
        ) { match in
            "\(match.1)\\\(match.2)"
        }
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
    @FocusState private var isInputFocused: Bool

    private var canSend: Bool {
        !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && !isBusy
    }

    var body: some View {
        HStack(spacing: 8) {
            TextField("Message...", text: $text)
                .font(.system(.body, design: .rounded))
                .foregroundStyle(PincerPalette.textPrimary)
                .focused($isInputFocused)
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
        .toolbar {
            ToolbarItemGroup(placement: .keyboard) {
                Spacer()
                Button("Done") {
                    isInputFocused = false
                }
            }
        }
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

            if !item.commandPreview.isEmpty {
                Text("$ \(item.commandPreview)")
                    .font(.system(.subheadline, design: .monospaced))
                    .foregroundStyle(PincerPalette.textSecondary)
                if let timeoutSummary = approvalTimeoutSummary(item.commandTimeoutMS) {
                    Text(timeoutSummary)
                        .font(.system(.subheadline, design: .rounded))
                        .foregroundStyle(PincerPalette.textSecondary)
                }
            } else if !item.deterministicSummary.isEmpty {
                Text(item.deterministicSummary)
                    .font(.system(.subheadline, design: .rounded))
                    .foregroundStyle(PincerPalette.textSecondary)
            }

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
                    .contentShape(Rectangle())
                    .onTapGesture {
                        dismissKeyboard()
                    }

                ScrollView(showsIndicators: false) {
                    VStack(alignment: .leading, spacing: 12) {
                        Text("Backend")
                            .font(.system(.title3, design: .rounded).weight(.semibold))
                            .foregroundStyle(PincerPalette.textPrimary)
                            .padding(.horizontal, 16)
                            .padding(.top, 8)

                        BackendConfigCard(
                            backendURL: $model.backendURL,
                            isBusy: model.isBusy,
                            isChecking: model.isCheckingBackend,
                            checks: model.backendChecks,
                            onCheck: {
                                Task { await model.checkBackend() }
                            },
                            onSave: {
                                Task { await model.saveBackendURL() }
                            },
                            onReset: {
                                Task { await model.resetBackendURL() }
                            }
                        )
                        .padding(.horizontal, 16)

                        Text("Pairing")
                            .font(.system(.title3, design: .rounded).weight(.semibold))
                            .foregroundStyle(PincerPalette.textPrimary)
                            .padding(.horizontal, 16)

                        PairingCard(model: model)
                            .padding(.horizontal, 16)

                        Text("Paired Devices")
                            .font(.system(.title3, design: .rounded).weight(.semibold))
                            .foregroundStyle(PincerPalette.textPrimary)
                            .padding(.horizontal, 16)

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
                .scrollDismissesKeyboard(.interactively)
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

private func dismissKeyboard() {
    UIApplication.shared.sendAction(
        #selector(UIResponder.resignFirstResponder),
        to: nil,
        from: nil,
        for: nil
    )
}

private struct BackendConfigCard: View {
    @Binding var backendURL: String
    let isBusy: Bool
    let isChecking: Bool
    let checks: [BackendCheckItem]
    let onCheck: () -> Void
    let onSave: () -> Void
    let onReset: () -> Void
    @FocusState private var isAddressFocused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Address")
                .font(.system(.subheadline, design: .rounded).weight(.semibold))
                .foregroundStyle(PincerPalette.textPrimary)

            TextField("http://192.168.1.50:8080", text: $backendURL)
                .font(.system(.body, design: .rounded))
                .foregroundStyle(PincerPalette.textPrimary)
                .textInputAutocapitalization(.never)
                .keyboardType(.URL)
                .autocorrectionDisabled()
                .submitLabel(.done)
                .focused($isAddressFocused)
                .onSubmit {
                    isAddressFocused = false
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .background(PincerPalette.page)
                .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
                .accessibilityIdentifier(A11y.settingsBackendURLInput)

            Text("Use your Mac's LAN URL for physical iPhone installs.")
                .font(.system(.footnote, design: .rounded))
                .foregroundStyle(PincerPalette.textSecondary)

            HStack(spacing: 10) {
                Button(action: {
                    isAddressFocused = false
                    onSave()
                }) {
                    Text(isBusy ? "Saving..." : "Save")
                        .font(.system(.subheadline, design: .rounded).weight(.semibold))
                        .foregroundStyle(PincerPalette.accent)
                }
                .disabled(isBusy || isChecking)
                .accessibilityIdentifier(A11y.settingsBackendSaveButton)

                Button(action: {
                    isAddressFocused = false
                    onCheck()
                }) {
                    Text(isChecking ? "Checking..." : "Check")
                        .font(.system(.subheadline, design: .rounded).weight(.semibold))
                        .foregroundStyle(PincerPalette.textSecondary)
                }
                .disabled(isBusy || isChecking)

                Button(action: {
                    isAddressFocused = false
                    onReset()
                }) {
                    Text("Reset")
                        .font(.system(.subheadline, design: .rounded).weight(.semibold))
                        .foregroundStyle(PincerPalette.textSecondary)
                }
                .disabled(isBusy || isChecking)
                .accessibilityIdentifier(A11y.settingsBackendResetButton)
            }

            if !checks.isEmpty {
                Divider()

                VStack(alignment: .leading, spacing: 8) {
                    Text("Checks")
                        .font(.system(.footnote, design: .rounded).weight(.semibold))
                        .foregroundStyle(PincerPalette.textSecondary)

                    ForEach(checks) { item in
                        BackendCheckRow(item: item)
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .cardSurface()
        .toolbar {
            ToolbarItemGroup(placement: .keyboard) {
                Spacer()
                Button("Done") {
                    isAddressFocused = false
                }
            }
        }
    }
}

private struct BackendCheckRow: View {
    let item: BackendCheckItem

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            statusView
                .frame(width: 18)

            VStack(alignment: .leading, spacing: 2) {
                Text(item.title)
                    .font(.system(.subheadline, design: .rounded).weight(.semibold))
                    .foregroundStyle(PincerPalette.textPrimary)

                if !item.detail.isEmpty {
                    Text(item.detail)
                        .font(.system(.footnote, design: .rounded))
                        .foregroundStyle(PincerPalette.textSecondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }

    @ViewBuilder
    private var statusView: some View {
        switch item.status {
        case .idle:
            Image(systemName: "circle")
                .font(.system(size: 14, weight: .semibold))
                .foregroundStyle(PincerPalette.textTertiary)
        case .running:
            ProgressView()
                .controlSize(.small)
                .tint(PincerPalette.textSecondary)
        case .ok:
            Image(systemName: "checkmark.circle.fill")
                .font(.system(size: 16, weight: .semibold))
                .foregroundStyle(PincerPalette.success)
        case .warning:
            Image(systemName: "exclamationmark.triangle.fill")
                .font(.system(size: 16, weight: .semibold))
                .foregroundStyle(PincerPalette.warning)
        case .error:
            Image(systemName: "xmark.octagon.fill")
                .font(.system(size: 16, weight: .semibold))
                .foregroundStyle(PincerPalette.danger)
        }
    }
}

private struct PairingCard: View {
    @ObservedObject var model: SettingsViewModel
    @FocusState private var isCodeFocused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Add another device")
                .font(.system(.subheadline, design: .rounded).weight(.semibold))
                .foregroundStyle(PincerPalette.textPrimary)

            Text("Generate a code on this device, then enter it on the new device.")
                .font(.system(.footnote, design: .rounded))
                .foregroundStyle(PincerPalette.textSecondary)

            Button(action: { Task { await model.generatePairingCode() } }) {
                Text(model.isGeneratingCode ? "Generating..." : "Generate Pairing Code")
                    .font(.system(.subheadline, design: .rounded).weight(.semibold))
                    .foregroundStyle(PincerPalette.accent)
            }
            .disabled(model.isGeneratingCode || model.isBindingCode)

            if let code = model.generatedPairingCode {
                HStack(spacing: 8) {
                    Text(code)
                        .font(.system(.title, design: .monospaced).weight(.bold))
                        .foregroundStyle(PincerPalette.textPrimary)
                        .textSelection(.enabled)

                    Button(action: {
                        UIPasteboard.general.string = code
                    }) {
                        Image(systemName: "doc.on.doc")
                            .font(.system(size: 16, weight: .semibold))
                            .foregroundStyle(PincerPalette.accent)
                    }
                }

                Text("Enter this code on the new device. Expires in 10 minutes.")
                    .font(.system(.footnote, design: .rounded))
                    .foregroundStyle(PincerPalette.textSecondary)
            }

            Divider()

            Text("Pair this device")
                .font(.system(.subheadline, design: .rounded).weight(.semibold))
                .foregroundStyle(PincerPalette.textPrimary)

            TextField("Enter pairing code", text: $model.manualPairingCode)
                .font(.system(.body, design: .monospaced))
                .foregroundStyle(PincerPalette.textPrimary)
                .keyboardType(.numberPad)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .focused($isCodeFocused)
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .background(PincerPalette.page)
                .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))

            Button(action: {
                isCodeFocused = false
                Task { await model.bindManualCode() }
            }) {
                Text(model.isBindingCode ? "Pairing..." : "Pair")
                    .font(.system(.subheadline, design: .rounded).weight(.semibold))
                    .foregroundStyle(PincerPalette.accent)
            }
            .disabled(model.isBindingCode || model.manualPairingCode.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .cardSurface()
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

private func prettyToolName(_ raw: String) -> String {
    raw
        .replacingOccurrences(of: "_", with: " ")
        .replacingOccurrences(of: "demo external notify", with: "Send External Follow-up")
        .capitalized
}

private func approvalTimeoutSummary(_ timeoutMS: Int64?) -> String? {
    guard let timeoutMS, timeoutMS > 0 else {
        return nil
    }
    if timeoutMS % 1000 == 0 {
        return "Timeout: \(timeoutMS / 1000)s"
    }
    return "Timeout: \(timeoutMS)ms"
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

private func relativeTimestamp(from iso: String) -> String {
    let parserWithFraction = ISO8601DateFormatter()
    parserWithFraction.formatOptions = [.withInternetDateTime, .withFractionalSeconds]

    let parser = ISO8601DateFormatter()

    guard let date = parserWithFraction.date(from: iso) ?? parser.date(from: iso) else {
        return iso
    }

    let formatter = RelativeDateTimeFormatter()
    formatter.unitsStyle = .abbreviated
    return formatter.localizedString(for: date, relativeTo: Date())
}
