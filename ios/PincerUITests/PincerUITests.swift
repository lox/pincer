import XCTest

final class PincerUITests: XCTestCase {
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        app = XCUIApplication()
        app.launch()
    }

    func testSessionsScreenAppears() {
        let sessionsScreen = app.otherElements["screen_sessions"]
        XCTAssertTrue(sessionsScreen.waitForExistence(timeout: 15), "Sessions screen did not appear")
        XCTAssertTrue(app.staticTexts["Main"].waitForExistence(timeout: 10), "Primary session was not visible")
    }

    func testSettingsTabOpensGatewaySettings() {
        let sessionsScreen = app.otherElements["screen_sessions"]
        XCTAssertTrue(sessionsScreen.waitForExistence(timeout: 15), "Sessions screen did not appear")

        let settingsTab = app.tabBars.buttons["Settings"].firstMatch
        XCTAssertTrue(settingsTab.waitForExistence(timeout: 10), "Settings tab not found")
        settingsTab.tap()

        let settingsScreen = app.otherElements["screen_settings"]
        XCTAssertTrue(settingsScreen.waitForExistence(timeout: 10), "Settings screen did not appear")
        XCTAssertTrue(app.textFields["gateway_url_input"].waitForExistence(timeout: 10), "Gateway URL input not found")
    }
}
