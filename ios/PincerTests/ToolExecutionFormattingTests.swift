import XCTest
@testable import Pincer

final class ToolExecutionFormattingTests: XCTestCase {
    func testParseStructuredToolExecutionOutput() {
        let parsed = parseToolExecutionStreamingContent(
            "$ pwd\n/tmp/pincer\nresult: exit 0 (25ms)"
        )

        XCTAssertEqual(parsed.command, "pwd")
        XCTAssertEqual(parsed.cwd, nil)
        XCTAssertEqual(parsed.output, "/tmp/pincer")
        XCTAssertEqual(parsed.result, .exit(code: 0, durationMs: 25))
        XCTAssertFalse(parsed.truncated)
        XCTAssertTrue(parsed.isStructured)
    }

    func testParseStructuredToolExecutionWithCwdTimeoutAndTruncation() {
        let parsed = parseToolExecutionStreamingContent(
            "$ ls\n# cwd: /tmp\nstdout\nresult: timed out (1000ms)\nresult: output truncated"
        )

        XCTAssertEqual(parsed.command, "ls")
        XCTAssertEqual(parsed.cwd, "/tmp")
        XCTAssertEqual(parsed.output, "stdout")
        XCTAssertEqual(parsed.result, .timedOut(durationMs: 1000))
        XCTAssertTrue(parsed.truncated)
        XCTAssertTrue(parsed.isStructured)
    }

    func testParseUnstructuredToolExecutionOutputFallsBackToRawText() {
        let parsed = parseToolExecutionStreamingContent("plain output only")

        XCTAssertNil(parsed.command)
        XCTAssertNil(parsed.cwd)
        XCTAssertEqual(parsed.output, "plain output only")
        XCTAssertNil(parsed.result)
        XCTAssertFalse(parsed.truncated)
        XCTAssertFalse(parsed.isStructured)
    }
}
