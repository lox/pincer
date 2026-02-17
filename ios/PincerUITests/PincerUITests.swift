// PincerUITests — XCUITest E2E for the approval flow.
// Assumes a backend is already running externally.
// The app auto-pairs if no bearer token exists in UserDefaults,
// so we only need to set the base URL via launch environment.

import XCTest

final class PincerUITests: XCTestCase {
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false

        app = XCUIApplication()

        // The E2E wrapper script sets PINCER_BASE_URL in the simulator's
        // UserDefaults before running xcodebuild.  The app reads UserDefaults
        // as a fallback when no launch environment overrides are present
        // (see AppConfig.swift).  We intentionally do NOT set
        // PINCER_IOS_BASE_URL here because xcodebuild does not forward custom
        // env vars to the XCUITest runner process.
        app.launch()
    }

    func testApprovalFlow() throws {
        let chatScreen = app.otherElements["screen_chat"]
        XCTAssertTrue(chatScreen.waitForExistence(timeout: 15), "Chat screen did not appear")

        let chatInput = app.textFields["chat_input"]
        XCTAssertTrue(chatInput.waitForExistence(timeout: 10), "Chat input not found")
        chatInput.tap()
        chatInput.typeText("Please run bash command pwd")

        let sendButton = app.buttons["chat_send_button"]
        XCTAssertTrue(sendButton.waitForExistence(timeout: 5), "Send button not found")
        sendButton.tap()

        // Wait for the LLM to respond and propose an action.
        sleep(5)

        // Dismiss the keyboard — it covers the tab bar after sending.
        dismissKeyboard()

        let approvalsTab = app.tabBars.buttons["tab_approvals"]
        XCTAssertTrue(approvalsTab.waitForExistence(timeout: 10), "Approvals tab not found")
        approvalsTab.tap()

        let heading = app.staticTexts["approvals_heading"]
        XCTAssertTrue(heading.waitForExistence(timeout: 15), "Approvals heading did not appear")

        let approveButton = app.buttons["approval_approve_first"]
        XCTAssertTrue(approveButton.waitForExistence(timeout: 30),
                      "No pending approval appeared within timeout")
        approveButton.tap()

        // After approval the button should disappear or become disabled.
        let disappeared = approveButton.waitForNonExistence(timeout: 20)
        let resolved = disappeared || !approveButton.isEnabled
        XCTAssertTrue(resolved, "Approval did not resolve after approving")
    }

    func testAssistantMessageAppearsBeforeActivityCard() throws {
        let chatScreen = app.otherElements["screen_chat"]
        XCTAssertTrue(chatScreen.waitForExistence(timeout: 15), "Chat screen did not appear")

        let chatInput = app.textFields["chat_input"]
        XCTAssertTrue(chatInput.waitForExistence(timeout: 10), "Chat input not found")
        chatInput.tap()
        chatInput.typeText("Please run bash command pwd")

        let sendButton = app.buttons["chat_send_button"]
        XCTAssertTrue(sendButton.waitForExistence(timeout: 5), "Send button not found")
        sendButton.tap()

        // Wait for the LLM to respond with an assistant message and a tool proposal.
        let activityLabel = app.staticTexts["Activity"]
        XCTAssertTrue(activityLabel.waitForExistence(timeout: 30),
                      "Activity card did not appear within timeout")

        let assistantLabel = app.staticTexts["Assistant"]
        XCTAssertTrue(assistantLabel.waitForExistence(timeout: 5),
                      "Assistant message label did not appear")

        // The assistant message should appear above (smaller Y) the activity card.
        let assistantY = assistantLabel.frame.minY
        let activityY = activityLabel.frame.minY
        XCTAssertLessThan(assistantY, activityY,
                          "Assistant message (y=\(assistantY)) should appear before activity card (y=\(activityY)) in the scroll view")
    }

    private func dismissKeyboard() {
        // The keyboard toolbar has a "Done" button; tap it to dismiss.
        let doneButton = app.toolbars.buttons["Done"]
        if doneButton.waitForExistence(timeout: 3) {
            doneButton.tap()
            sleep(1)
            return
        }
        // Fallback: tap the navigation bar title area to resign focus.
        let navBar = app.navigationBars.firstMatch
        if navBar.exists {
            navBar.tap()
            sleep(1)
        }
    }
}

private extension XCUIElement {
    func waitForNonExistence(timeout: TimeInterval) -> Bool {
        let predicate = NSPredicate(format: "exists == false")
        let expectation = XCTNSPredicateExpectation(predicate: predicate, object: self)
        let result = XCTWaiter().wait(for: [expectation], timeout: timeout)
        return result == .completed
    }
}
