import Foundation
import SwiftProtobuf

func approvalCommandPreview(tool: String, preview: Google_Protobuf_Struct?) -> String {
    guard tool.trimmingCharacters(in: .whitespacesAndNewlines) == "run_bash", let preview else {
        return ""
    }

    if let command = structStringField(preview, key: "command"), !command.isEmpty {
        return command
    }

    if let nestedArgs = structStructField(preview, key: "args"),
       let command = structStringField(nestedArgs, key: "command"),
       !command.isEmpty {
        return command
    }

    return ""
}

func approvalCommandTimeoutMS(tool: String, preview: Google_Protobuf_Struct?) -> Int64? {
    guard tool.trimmingCharacters(in: .whitespacesAndNewlines) == "run_bash", let preview else {
        return nil
    }

    if let timeoutMS = structInt64Field(preview, key: "timeout_ms"), timeoutMS > 0 {
        return timeoutMS
    }

    if let nestedArgs = structStructField(preview, key: "args"),
       let timeoutMS = structInt64Field(nestedArgs, key: "timeout_ms"),
       timeoutMS > 0 {
        return timeoutMS
    }

    return nil
}

private func structStringField(_ value: Google_Protobuf_Struct, key: String) -> String? {
    guard let field = value.fields[key] else {
        return nil
    }
    switch field.kind {
    case .stringValue(let raw):
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    default:
        return nil
    }
}

private func structStructField(_ value: Google_Protobuf_Struct, key: String) -> Google_Protobuf_Struct? {
    guard let field = value.fields[key] else {
        return nil
    }
    switch field.kind {
    case .structValue(let nested):
        return nested
    default:
        return nil
    }
}

private func structInt64Field(_ value: Google_Protobuf_Struct, key: String) -> Int64? {
    guard let field = value.fields[key] else {
        return nil
    }

    switch field.kind {
    case .numberValue(let raw):
        guard raw.isFinite else {
            return nil
        }
        let truncated = raw.rounded(.towardZero)
        guard truncated > 0, truncated <= Double(Int64.max) else {
            return nil
        }
        return Int64(truncated)
    default:
        return nil
    }
}
