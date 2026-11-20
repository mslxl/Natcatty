import XCTest

/// Drives the real on-screen keyboard of the iPad simulator against the
/// natcatty_smoke terminal diagnostic. Unit tests can feed the terminal's
/// text-input client directly, but only UIKit decides when the keyboard is
/// presented, what an input method composes, and what `insertText` delivers
/// for Return; these tests read the bytes the terminal actually wrote.
final class SoftKeyboardUITests: XCTestCase {
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        app = XCUIApplication(bundleIdentifier: "com.mslxl.natcatty")
        app.launch()
        let terminal = app.descendants(matching: .any)["Terminal"]
        XCTAssertTrue(terminal.waitForExistence(timeout: 60), "the terminal did not appear")
        terminal.tap()
        XCTAssertTrue(waitForKeyboard(), "the keyboard did not appear on the first tap")
    }

    private var keyboard: XCUIElement { app.keyboards.element }

    /// The keyboard element can exist before its view is presented, so wait
    /// for a frame with height: acting on the not-yet-shown input view makes
    /// its accessibility actions fail and leaves it hidden. A dismissal can
    /// leave the element at zero height or remove it entirely.
    private func waitForKeyboardGone(timeout: TimeInterval = 10) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if !app.keyboards.element.exists || app.keyboards.element.frame.height == 0 { return true }
            usleep(200_000)
        }
        return false
    }

    @discardableResult
    private func waitForKeyboard(timeout: TimeInterval = 15) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if app.keyboards.element.exists && app.keyboards.element.frame.height > 0 { return true }
            usleep(200_000)
        }
        return false
    }

    private func key(_ label: String) -> XCUIElement {
        keyboard.keys.matching(NSPredicate(format: "label == %@", label)).firstMatch
    }

    private func label(_ element: XCUIElement, is expected: String, timeout: TimeInterval = 10) {
        expectation(for: NSPredicate(format: "label == %@", expected), evaluatedWith: element)
        waitForExpectations(timeout: timeout)
    }

    private func expectInput(_ hex: String, timeout: TimeInterval = 10) {
        let e = app.staticTexts["input-bytes: " + hex]
        XCTAssertTrue(e.waitForExistence(timeout: timeout), "shell received \(currentInput()), want \(hex)")
        print("SoftKeyboardUITests: shell received \(hex)")
    }

    private func currentInput() -> String {
        let e = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "input-bytes: ")).firstMatch
        return e.exists ? String(e.label.dropFirst("input-bytes: ".count)) : "<none>"
    }

    /// Cycles the globe key until the wanted keyboard shows. The pinyin one
    /// has a full-width comma key; the English one has a plain q and no comma.
    @discardableResult
    private func switchKeyboard(pinyin: Bool) -> Bool {
        for _ in 0..<4 {
            let comma = keyboard.keys.matching(NSPredicate(format: "label == %@", "，")).count
            let q = keyboard.keys.matching(NSPredicate(format: "label == %@", "q")).count
            if pinyin && comma > 0 { return true }
            if !pinyin && comma == 0 && q > 0 { return true }
            let next = app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "Next keyboard")).firstMatch
            guard next.exists else { break }
            next.tap()
            sleep(1)
            if app.buttons["Continue"].exists { app.buttons["Continue"].tap(); sleep(1) }
            if !app.keyboards.element.exists {
                app.descendants(matching: .any)["Terminal"].tap()
                _ = app.keyboards.element.waitForExistence(timeout: 5)
            }
        }
        return false
    }

    /// Criterion 1: the first tap presents the keyboard, the iPad's own
    /// dismiss key hides it, and a second tap brings it back. The dismissal
    /// tickles a MyGo bug where the surface resize that follows the
    /// keyboard's exit re-focused the terminal and raised the keyboard
    /// again; this test is what catches it.
    func testKeyboardComesBackAfterDismissal() {
        XCTAssertTrue(keyboard.exists)
        let hide = app.buttons["Hide keyboard"]
        XCTAssertTrue(hide.waitForExistence(timeout: 5), "the iPad keyboard has no dismiss key")
        hide.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
        XCTAssertTrue(waitForKeyboardGone(), "the keyboard did not hide")
        // The surface resize that follows the keyboard's exit arrives after
        // the animation; a moment's stay-down also proves the dismissal
        // itself sticks rather than only being observed mid-animation.
        sleep(1)
        XCTAssertFalse(keyboard.exists && keyboard.frame.height > 0,
                       "the keyboard came back without a tap")
        app.descendants(matching: .any)["Terminal"].tap()
        XCTAssertTrue(waitForKeyboard(), "the second tap did not bring the keyboard back")
        print("SoftKeyboardUITests: the keyboard hid with its own key and the next tap re-showed it")
    }

    /// Criterion 2, Latin path: keys typed on the software keyboard arrive
    /// as typed and Return reaches the shell as CR, not the LF that UIKit's
    /// `insertText` delivers for a newline.
    func testSoftKeyboardTypesAndReturnSendsCarriageReturn() {
        XCTAssertTrue(switchKeyboard(pinyin: false), "the English keyboard is not available")
        key("a").tap()
        key("b").tap()
        key("c").tap()
        app.buttons["Return"].tap()
        expectInput("6162630d")
    }

    /// Criterion 2, IME path: a Pinyin composition is marked text in the
    /// terminal, the shell sees nothing until a candidate is committed, the
    /// candidate reaches it once, and deleting inside the composition edits
    /// only the marked text. The committed candidate is one CJK character,
    /// three UTF-8 bytes and so six hex digits.
    func testPinyinCompositionCommitsOnceAndEditsLocally() {
        XCTAssertTrue(switchKeyboard(pinyin: true), "the Pinyin keyboard is not enabled")
        expectInput("")
        key("n").tap()
        key("i").tap()
        label(app.buttons["Return"], is: "确认")
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "pinyin composition shown in the terminal"
        shot.lifetime = .keepAlways
        add(shot)
        sleep(1)
        XCTAssertEqual(currentInput(), "", "the composition reached the shell before it was committed")
        key("space").tap()
        let committed = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@ AND label != %@", "input-bytes: ", "input-bytes: ")).firstMatch
        XCTAssertTrue(committed.waitForExistence(timeout: 10), "the committed candidate never reached the shell")
        let hex = String(committed.label.dropFirst("input-bytes: ".count))
        XCTAssertEqual(hex.count, 6, "one CJK character reaches the shell, got \(hex)")
        print("SoftKeyboardUITests: composition stayed marked, then committed once as \(hex)")
        sleep(1)
        XCTAssertTrue(app.staticTexts["input-bytes: " + hex].exists, "the commit was sent more than once")
        key("n").tap()
        key("i").tap()
        label(app.buttons["Return"], is: "确认")
        app.keys["delete"].tap()
        app.keys["delete"].tap()
        label(app.buttons["Return"], is: "换行")
        sleep(1)
        XCTAssertTrue(app.staticTexts["input-bytes: " + hex].exists, "deleting the composition reached the shell")
        print("SoftKeyboardUITests: delete edited only the marked text; nothing reached the shell")
        app.buttons["Return"].tap()
        expectInput(hex + "0d")
    }
}
