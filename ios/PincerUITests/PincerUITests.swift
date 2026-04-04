import XCTest

final class PincerUITests: XCTestCase {
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        app = XCUIApplication()
        app.launchEnvironment["OPENCLAW_UI_TEST_MODE"] = "1"
        app.launch()
    }

    func testChatScreenAppears() {
        let chatScreen = app.otherElements["screen_chat"]
        XCTAssertTrue(chatScreen.waitForExistence(timeout: 15), "Chat screen did not appear")
        XCTAssertTrue(app.textFields["message_input"].waitForExistence(timeout: 10), "Message composer did not appear")
        XCTAssertTrue(app.navigationBars.staticTexts["Main"].waitForExistence(timeout: 10), "Primary chat title was not visible")
    }

    func testSettingsTabOpensGatewaySettings() {
        let chatScreen = app.otherElements["screen_chat"]
        XCTAssertTrue(chatScreen.waitForExistence(timeout: 15), "Chat screen did not appear")

        let settingsTab = app.tabBars.buttons["Settings"].firstMatch
        XCTAssertTrue(settingsTab.waitForExistence(timeout: 10), "Settings tab not found")
        settingsTab.tap()

        let settingsScreen = app.otherElements["screen_settings"]
        XCTAssertTrue(settingsScreen.waitForExistence(timeout: 10), "Settings screen did not appear")
        XCTAssertTrue(app.textFields["gateway_url_input"].waitForExistence(timeout: 10), "Gateway URL input not found")
    }
}
