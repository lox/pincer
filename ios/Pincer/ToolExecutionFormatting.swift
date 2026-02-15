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
