import Foundation

enum ToolExecutionResult: Equatable {
    case exit(code: Int, durationMs: Int)
    case timedOut(durationMs: Int)

    var line: String {
        switch self {
        case .exit(let code, let durationMs):
            return "result: exit \(code) (\(durationMs)ms)"
        case .timedOut(let durationMs):
            return "result: timed out (\(durationMs)ms)"
        }
    }
}

struct ParsedToolExecutionStreamingContent: Equatable {
    let command: String?
    let cwd: String?
    let output: String
    let result: ToolExecutionResult?
    let truncated: Bool

    var isStructured: Bool {
        command != nil || cwd != nil || result != nil || truncated
    }

    /// The tool name extracted from the command line (e.g. "gmail_search" from "gmail_search {...}").
    var toolName: String? {
        guard let command else { return nil }
        let name = command.prefix(while: { !$0.isWhitespace })
        return name.isEmpty ? nil : String(name)
    }

    /// Whether this is a terminal/bash command (shown expanded) vs a read tool (shown collapsed).
    var isBashCommand: Bool {
        guard let name = toolName else { return true }
        return name == "run_bash"
    }

    /// A human-readable one-line summary for non-bash tools, e.g. "gmail_search · from:emily@canopy..."
    var readToolSummary: String {
        guard let command else { return "Tool Output" }
        let name = toolName ?? command
        // Try to extract first string value from JSON args after the tool name.
        let argsStart = command.dropFirst(name.count).drop(while: { $0.isWhitespace })
        if let firstStringValue = extractFirstJSONStringValue(String(argsStart)) {
            let truncated = firstStringValue.count > 60 ? String(firstStringValue.prefix(57)) + "…" : firstStringValue
            return "\(humanToolName(name)) · \(truncated)"
        }
        return humanToolName(name)
    }
}

/// Map tool names to human-friendly labels.
private func humanToolName(_ name: String) -> String {
    switch name {
    case "gmail_search": return "Search Gmail"
    case "gmail_read": return "Read email"
    case "gmail_create_draft": return "Create draft"
    case "gmail_send_draft": return "Send draft"
    case "web_search": return "Web search"
    case "web_summarize": return "Summarize page"
    case "web_fetch": return "Fetch page"
    case "image_describe": return "Describe image"
    default: return name
    }
}

/// Extract the first string value from a JSON object string, e.g. `{"query": "foo"}` → "foo".
private func extractFirstJSONStringValue(_ json: String) -> String? {
    guard let data = json.data(using: .utf8),
          let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
        return nil
    }
    // Return first string value found.
    for (_, value) in obj {
        if let s = value as? String, !s.isEmpty {
            return s
        }
    }
    return nil
}

func parseToolExecutionStreamingContent(_ content: String) -> ParsedToolExecutionStreamingContent {
    var lines = content.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)

    var command: String?
    if let first = lines.first, first.hasPrefix("$ ") {
        command = String(first.dropFirst(2))
        lines.removeFirst()
    }

    var cwd: String?
    if let first = lines.first, first.hasPrefix("# cwd: ") {
        cwd = String(first.dropFirst("# cwd: ".count))
        lines.removeFirst()
    }

    var truncated = false
    if let last = lines.last, last == "result: output truncated" {
        truncated = true
        lines.removeLast()
    }

    var result: ToolExecutionResult?
    if let last = lines.last, let parsedResult = parseToolExecutionResultLine(last) {
        result = parsedResult
        lines.removeLast()
    }

    let output: String
    if command == nil, cwd == nil, result == nil, !truncated {
        output = content
    } else {
        output = lines.joined(separator: "\n")
    }

    return ParsedToolExecutionStreamingContent(
        command: command,
        cwd: cwd,
        output: output,
        result: result,
        truncated: truncated
    )
}

private func parseToolExecutionResultLine(_ line: String) -> ToolExecutionResult? {
    let timeoutPrefix = "result: timed out ("
    if line.hasPrefix(timeoutPrefix), line.hasSuffix("ms)") {
        let durationSlice = line.dropFirst(timeoutPrefix.count).dropLast(3)
        if let durationMs = Int(durationSlice) {
            return .timedOut(durationMs: durationMs)
        }
    }

    let exitPrefix = "result: exit "
    if line.hasPrefix(exitPrefix) {
        let payload = String(line.dropFirst(exitPrefix.count))
        guard let separator = payload.firstIndex(of: " ") else {
            return nil
        }

        let codeRaw = payload[..<separator]
        guard let code = Int(codeRaw) else {
            return nil
        }

        let durationRaw = payload[separator...].trimmingCharacters(in: .whitespaces)
        guard durationRaw.hasPrefix("("), durationRaw.hasSuffix("ms)") else {
            return nil
        }
        let msValue = durationRaw.dropFirst().dropLast(3)
        guard let durationMs = Int(msValue) else {
            return nil
        }

        return .exit(code: code, durationMs: durationMs)
    }

    return nil
}
