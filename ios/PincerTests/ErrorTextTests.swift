import XCTest
@testable import Pincer

final class ErrorTextTests: XCTestCase {
    func testSuppressesDeadlineExceededLiveStreamError() {
        XCTAssertFalse(shouldShowLiveStreamError(APIError.rpc("deadline_exceeded")))
    }

    func testSuppressesCanceledLiveStreamError() {
        XCTAssertFalse(shouldShowLiveStreamError(APIError.rpc("canceled")))
    }

    func testShowsOtherLiveStreamErrors() {
        XCTAssertTrue(shouldShowLiveStreamError(APIError.rpc("unavailable")))
        XCTAssertTrue(shouldShowLiveStreamError(APIError.unauthorized))
    }
}
