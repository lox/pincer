import XCTest
import SwiftProtobuf
@testable import Pincer

final class ApprovalDetailFormattingTests: XCTestCase {
    func testExtractsRunBashCommandFromPreviewRootField() {
        var preview = Google_Protobuf_Struct()
        preview.fields["command"] = Google_Protobuf_Value(stringValue: "pwd")

        let command = approvalCommandPreview(tool: "run_bash", preview: preview)

        XCTAssertEqual(command, "pwd")
    }

    func testExtractsRunBashCommandFromNestedArgsField() {
        var args = Google_Protobuf_Struct()
        args.fields["command"] = Google_Protobuf_Value(stringValue: "ls -la")
        var preview = Google_Protobuf_Struct()
        preview.fields["args"] = Google_Protobuf_Value(structValue: args)

        let command = approvalCommandPreview(tool: "run_bash", preview: preview)

        XCTAssertEqual(command, "ls -la")
    }

    func testExtractsRunBashTimeoutFromPreviewRootField() {
        var preview = Google_Protobuf_Struct()
        preview.fields["timeout_ms"] = Google_Protobuf_Value(numberValue: 12_000)

        let timeoutMS = approvalCommandTimeoutMS(tool: "run_bash", preview: preview)

        XCTAssertEqual(timeoutMS, 12_000)
    }

    func testExtractsRunBashTimeoutFromNestedArgsField() {
        var args = Google_Protobuf_Struct()
        args.fields["timeout_ms"] = Google_Protobuf_Value(numberValue: 90_000)
        var preview = Google_Protobuf_Struct()
        preview.fields["args"] = Google_Protobuf_Value(structValue: args)

        let timeoutMS = approvalCommandTimeoutMS(tool: "run_bash", preview: preview)

        XCTAssertEqual(timeoutMS, 90_000)
    }

    func testIgnoresPreviewForNonBashTools() {
        var preview = Google_Protobuf_Struct()
        preview.fields["command"] = Google_Protobuf_Value(stringValue: "pwd")

        let command = approvalCommandPreview(tool: "demo_external_notify", preview: preview)
        let timeoutMS = approvalCommandTimeoutMS(tool: "demo_external_notify", preview: preview)

        XCTAssertEqual(command, "")
        XCTAssertNil(timeoutMS)
    }
}
