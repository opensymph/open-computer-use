import AppKit
import ApplicationServices
import CoreGraphics
import Foundation
import XCTest
@testable import OpenComputerUseKit

final class WindowManagementTests: XCTestCase {
    // MARK: - window argument parsing (pure)

    func testWindowArgumentObjectForm() throws {
        let ref = try WindowArgumentParsing.optional([
            "window": ["app": "  Safari  ", "id": 5, "title": "Example"],
        ])

        XCTAssertEqual(ref, WindowRef(app: "Safari", id: 5, title: "Example"))
    }

    func testWindowArgumentObjectFormOmitsMissingTitle() throws {
        let ref = try WindowArgumentParsing.optional([
            "window": ["app": "Safari", "id": 5],
        ])

        XCTAssertEqual(ref, WindowRef(app: "Safari", id: 5, title: nil))
    }

    func testWindowArgumentFlatAliasWithoutWindowKey() throws {
        let ref = try WindowArgumentParsing.optional(["window_id": 7])

        XCTAssertEqual(ref, WindowRef(app: "", id: 7, title: nil))
    }

    func testWindowArgumentFlatAliasStringID() throws {
        let ref = try WindowArgumentParsing.optional(["window_id": "42"])

        XCTAssertEqual(ref, WindowRef(app: "", id: 42, title: nil))
    }

    func testWindowArgumentFlatAliasZeroIsAbsent() throws {
        let ref = try WindowArgumentParsing.optional(["window_id": 0])
        let negative = try WindowArgumentParsing.optional(["window_id": -2])

        XCTAssertNil(ref)
        XCTAssertNil(negative)
    }

    func testWindowArgumentObjectTakesPrecedenceOverAlias() throws {
        let ref = try WindowArgumentParsing.optional([
            "window": ["app": "Safari", "id": 5],
            "window_id": 9,
        ])

        XCTAssertEqual(ref?.id, 5)
    }

    func testWindowArgumentNonObjectIsRejected() {
        XCTAssertThrowsError(try WindowArgumentParsing.optional(["window": "Safari"])) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "window must be an object with an integer id"
            )
        }
    }

    func testWindowArgumentMissingIDIsRejected() {
        XCTAssertThrowsError(try WindowArgumentParsing.optional(["window": ["app": "Safari"]])) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "window.id must be an integer >= 0"
            )
        }
    }

    func testWindowArgumentNonIntegerIDIsRejected() {
        XCTAssertThrowsError(try WindowArgumentParsing.optional(["window": ["id": 1.5]])) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "window id must be an integer >= 0"
            )
        }
        XCTAssertThrowsError(try WindowArgumentParsing.optional(["window_id": "abc"])) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "window id must be an integer >= 0"
            )
        }
    }

    func testWindowArgumentNegativeObjectIDIsRejected() {
        XCTAssertThrowsError(try WindowArgumentParsing.optional(["window": ["id": -3]])) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "window.id must be an integer >= 0"
            )
        }
    }

    func testWindowArgumentBooleanIDIsTreatedAsMissing() {
        XCTAssertThrowsError(try WindowArgumentParsing.optional(["window": ["id": true]])) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "window.id must be an integer >= 0"
            )
        }
    }

    func testWindowRefJSONObjectOmitsEmptyTitle() {
        let withoutTitle = WindowRef(app: "Safari", id: 5, title: nil).jsonObject
        XCTAssertEqual(withoutTitle.count, 2)
        XCTAssertEqual(withoutTitle["app"] as? String, "Safari")
        XCTAssertEqual(withoutTitle["id"] as? Int, 5)
        XCTAssertNil(withoutTitle["title"])

        let withTitle = WindowRef(app: "Safari", id: 5, title: "Tab").jsonObject
        XCTAssertEqual(withTitle.count, 3)
        XCTAssertEqual(withTitle["app"] as? String, "Safari")
        XCTAssertEqual(withTitle["id"] as? Int, 5)
        XCTAssertEqual(withTitle["title"] as? String, "Tab")
    }

    // MARK: - resolve with synthetic entries (pure)

    private func syntheticEntry(id: CGWindowID, pid: pid_t) -> CGWindowEntry {
        CGWindowEntry(
            windowID: id,
            ownerPID: pid,
            layer: 0,
            bounds: CGRect(x: 10, y: 20, width: 300, height: 200),
            title: "Synthetic",
            frontToBackIndex: 0
        )
    }

    private func syntheticApp(name: String, bundleIdentifier: String?, pid: pid_t) -> RunningAppDescriptor {
        RunningAppDescriptor(
            name: name,
            bundleIdentifier: bundleIdentifier,
            pid: pid,
            runningApplication: NSRunningApplication.current
        )
    }

    func testResolveMissingWindowIsStale() {
        let entry = syntheticEntry(id: 10, pid: 1234)
        let app = syntheticApp(name: "Example", bundleIdentifier: "com.example.app", pid: 1234)

        XCTAssertThrowsError(try WindowDirectory.resolve(id: 99, entries: [entry], runningApps: [app])) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "staleWindowHandle(99): the window is no longer open; re-observe with list_windows."
            )
        }
    }

    func testResolveDeadProcessIsStale() {
        let entry = syntheticEntry(id: 10, pid: 1234)

        XCTAssertThrowsError(try WindowDirectory.resolve(id: 10, entries: [entry], runningApps: [])) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "staleWindowHandle(10): the owning process is no longer running; re-observe with list_windows."
            )
        }
    }

    func testResolveBlockedAppIsDenied() {
        let entry = syntheticEntry(id: 10, pid: 1234)
        let app = syntheticApp(name: "1Password", bundleIdentifier: "com.1password.1password", pid: 1234)

        XCTAssertThrowsError(try WindowDirectory.resolve(id: 10, entries: [entry], runningApps: [app])) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "Computer Use is not allowed to use the app 'com.1password.1password' for safety reasons."
            )
        }
    }

    func testResolveSuccessCarriesLiveReference() throws {
        let entry = syntheticEntry(id: 10, pid: 1234)
        let app = syntheticApp(name: "Example", bundleIdentifier: "com.example.app", pid: 1234)

        let resolved = try WindowDirectory.resolve(
            id: 10,
            entries: [entry],
            runningApps: [app],
            identity: { _, _ in .indeterminate }
        )

        XCTAssertEqual(resolved.ref, WindowRef(app: "Example", id: 10, title: "Synthetic"))
        XCTAssertEqual(resolved.pid, 1234)
        XCTAssertEqual(resolved.bundleIdentifier, "com.example.app")
        XCTAssertEqual(resolved.entry.bounds, entry.bounds)
    }

    func testResolveLingeringEntryWithoutAXIdentityIsStale() {
        let entry = syntheticEntry(id: 10, pid: 1234)
        let app = syntheticApp(name: "Example", bundleIdentifier: "com.example.app", pid: 1234)

        // A CG entry that lingers in the full window list after close has no
        // live accessibility identity: the AX-identity check proves absence
        // and the official stale error applies.
        XCTAssertThrowsError(try WindowDirectory.resolve(
            id: 10,
            entries: [entry],
            runningApps: [app],
            identity: { _, _ in .absent }
        )) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "staleWindowHandle(10): the window is no longer open; re-observe with list_windows."
            )
        }
    }

    func testResolveConfirmedAXIdentitySucceeds() throws {
        let entry = syntheticEntry(id: 10, pid: 1234)
        let app = syntheticApp(name: "Example", bundleIdentifier: "com.example.app", pid: 1234)

        let resolved = try WindowDirectory.resolve(
            id: 10,
            entries: [entry],
            runningApps: [app],
            identity: { _, _ in .confirmed }
        )

        XCTAssertEqual(resolved.ref, WindowRef(app: "Example", id: 10, title: "Synthetic"))
    }

    func testResolveIndeterminateIdentityIsAccepted() throws {
        let entry = syntheticEntry(id: 10, pid: 1234)
        let app = syntheticApp(name: "Example", bundleIdentifier: "com.example.app", pid: 1234)

        // Documented guarantee: a window whose identity cannot be verified
        // (no private mapping symbol, no AX window list, partial mapping
        // failure) is never rejected by the identity check.
        let resolved = try WindowDirectory.resolve(
            id: 10,
            entries: [entry],
            runningApps: [app],
            identity: { _, _ in .indeterminate }
        )

        XCTAssertEqual(resolved.ref, WindowRef(app: "Example", id: 10, title: "Synthetic"))
    }

    func testAmbiguousWindowMessageFormatIsPinned() {
        // The explicit same-bounds ambiguity error is part of the tool
        // contract; pin its exact format (id, count, app, recovery hint).
        XCTAssertEqual(
            WindowDirectory.ambiguousWindowMessage(id: 42, app: "Example", count: 2),
            "ambiguousWindow(42): 2 windows of Example share the same bounds; "
                + "the accessibility tree cannot be matched to this window. "
                + "Move or resize one of them and re-observe with list_windows."
        )
    }

    // MARK: - screenshot id lifecycle (pure)

    func testWindowScreenshotIDFormatUsesGlobalCounter() {
        let service = ComputerUseService()
        let bounds = CGRect(x: 0, y: 0, width: 100, height: 100)

        let first = service.issueWindowScreenshotID(
            windowID: 42,
            hasImage: true,
            bounds: bounds,
            screenshotPixelSize: CGSize(width: 200, height: 200)
        )
        let second = service.issueWindowScreenshotID(
            windowID: 7,
            hasImage: true,
            bounds: bounds,
            screenshotPixelSize: nil
        )

        XCTAssertEqual(first, "shot-42-1")
        XCTAssertEqual(second, "shot-7-2")
    }

    func testCheckScreenshotIDRequiresWindowTargeting() {
        let service = ComputerUseService()

        XCTAssertThrowsError(try service.checkScreenshotID("shot-1-1", windowID: nil)) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "screenshotId requires window targeting; pass window from get_window_state and re-observe."
            )
        }
    }

    func testCheckScreenshotIDAcceptsEmptyAndValidIDs() throws {
        let service = ComputerUseService()
        let bounds = CGRect(x: 0, y: 0, width: 100, height: 100)

        try service.checkScreenshotID(nil, windowID: 1)
        try service.checkScreenshotID("  ", windowID: 1)

        let issued = service.issueWindowScreenshotID(
            windowID: 1,
            hasImage: true,
            bounds: bounds,
            screenshotPixelSize: nil
        )
        try service.checkScreenshotID(issued, windowID: 1)
    }

    func testCheckScreenshotIDRejectsStaleIDs() throws {
        let service = ComputerUseService()
        let bounds = CGRect(x: 0, y: 0, width: 100, height: 100)
        let issued = service.issueWindowScreenshotID(
            windowID: 1,
            hasImage: true,
            bounds: bounds,
            screenshotPixelSize: nil
        )

        XCTAssertThrowsError(try service.checkScreenshotID(issued, windowID: 2)) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "stale screenshot id; re-observe with get_window_state before retrying."
            )
        }
        XCTAssertThrowsError(try service.checkScreenshotID("shot-1-999", windowID: 1)) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "stale screenshot id; re-observe with get_window_state before retrying."
            )
        }
    }

    func testInvalidateWindowScreenshotDropsTheBinding() throws {
        let service = ComputerUseService()
        let bounds = CGRect(x: 0, y: 0, width: 100, height: 100)
        let issued = service.issueWindowScreenshotID(
            windowID: 1,
            hasImage: true,
            bounds: bounds,
            screenshotPixelSize: CGSize(width: 200, height: 200)
        )

        service.invalidateWindowScreenshot(for: 1)

        XCTAssertThrowsError(try service.checkScreenshotID(issued, windowID: 1)) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "stale screenshot id; re-observe with get_window_state before retrying."
            )
        }
        XCTAssertThrowsError(try service.checkCoordinateGate(windowID: 1, x: 5, y: 5, currentBounds: nil)) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "call get_window_state before issuing coordinate input"
            )
        }
    }

    // MARK: - coordinate gate (pure)

    func testCoordinateGateRejectsNeverObservedWindow() {
        let service = ComputerUseService()

        XCTAssertThrowsError(try service.checkCoordinateGate(windowID: 1, x: 5, y: 5, currentBounds: nil)) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "call get_window_state before issuing coordinate input"
            )
        }
    }

    func testCoordinateGateRejectsObservationWithoutScreenshot() {
        let service = ComputerUseService()
        service.recordWindowObservationWithoutImage(
            windowID: 1,
            bounds: CGRect(x: 0, y: 0, width: 100, height: 100)
        )

        XCTAssertThrowsError(try service.checkCoordinateGate(windowID: 1, x: 5, y: 5, currentBounds: nil)) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "call get_window_state with include_screenshot before issuing coordinate input"
            )
        }
    }

    func testCoordinateGateRejectsMovedWindow() {
        let service = ComputerUseService()
        let bounds = CGRect(x: 0, y: 0, width: 100, height: 100)
        _ = service.issueWindowScreenshotID(
            windowID: 1,
            hasImage: true,
            bounds: bounds,
            screenshotPixelSize: CGSize(width: 200, height: 200)
        )

        XCTAssertThrowsError(try service.checkCoordinateGate(
            windowID: 1,
            x: 5,
            y: 5,
            currentBounds: bounds.offsetBy(dx: 25, dy: 0)
        )) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "window bounds changed; call get_window_state before continuing"
            )
        }
    }

    func testCoordinateGateAllowsBoundsWithinTolerance() throws {
        let service = ComputerUseService()
        let bounds = CGRect(x: 0, y: 0, width: 100, height: 100)
        _ = service.issueWindowScreenshotID(
            windowID: 1,
            hasImage: true,
            bounds: bounds,
            screenshotPixelSize: CGSize(width: 200, height: 200)
        )

        try service.checkCoordinateGate(
            windowID: 1,
            x: 5,
            y: 5,
            currentBounds: bounds.offsetBy(dx: 1.5, dy: -1)
        )
    }

    func testCoordinateGateRejectsPointsOutsideScreenshot() {
        let service = ComputerUseService()
        let bounds = CGRect(x: 0, y: 0, width: 100, height: 100)
        _ = service.issueWindowScreenshotID(
            windowID: 1,
            hasImage: true,
            bounds: bounds,
            screenshotPixelSize: CGSize(width: 200, height: 200)
        )

        XCTAssertThrowsError(try service.checkCoordinateGate(windowID: 1, x: 200, y: 0, currentBounds: bounds)) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "(200, 0) is outside screenshot bounds"
            )
        }
        XCTAssertThrowsError(try service.checkCoordinateGate(windowID: 1, x: 0, y: -1, currentBounds: bounds)) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "(0, -1) is outside screenshot bounds"
            )
        }
        // The error message reports the int-cast coordinates.
        XCTAssertThrowsError(try service.checkCoordinateGate(windowID: 1, x: 199.6, y: 200.4, currentBounds: bounds)) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "(199, 200) is outside screenshot bounds"
            )
        }
    }

    func testCoordinateGateAcceptsInsidePoints() throws {
        let service = ComputerUseService()
        let bounds = CGRect(x: 0, y: 0, width: 100, height: 100)
        _ = service.issueWindowScreenshotID(
            windowID: 1,
            hasImage: true,
            bounds: bounds,
            screenshotPixelSize: CGSize(width: 200, height: 200)
        )

        try service.checkCoordinateGate(windowID: 1, x: 0, y: 0, currentBounds: bounds)
        try service.checkCoordinateGate(windowID: 1, x: 199.9, y: 199.9, currentBounds: bounds)
    }

    // MARK: - dispatcher routing (no live windows required)

    func testDispatcherWindow2ToolsAreNoLongerStubbed() {
        let dispatcher = ComputerUseToolDispatcher()
        let result = dispatcher.callToolAsResult(name: "list_windows", arguments: [:])

        XCTAssertFalse(result.isError, "list_windows must not error on macOS: \(result.primaryText ?? "")")
        XCTAssertNil(result.primaryText?.range(of: "not supported yet on macOS"))
        XCTAssertTrue(result.primaryText?.hasPrefix("[") == true, "list_windows must return a JSON array")
    }

    func testDispatcherGetWindowRequiresWindowID() {
        let dispatcher = ComputerUseToolDispatcher()
        let result = dispatcher.callToolAsResult(name: "get_window", arguments: [:])

        XCTAssertTrue(result.isError)
        XCTAssertEqual(result.primaryText, "Missing required argument: window.id")
    }

    func testDispatcherGetWindowStateRequiresWindow() {
        let dispatcher = ComputerUseToolDispatcher()
        let result = dispatcher.callToolAsResult(name: "get_window_state", arguments: [:])

        XCTAssertTrue(result.isError)
        XCTAssertEqual(result.primaryText, "Missing required argument: window")
    }

    func testDispatcherActivateWindowRequiresWindowID() {
        let dispatcher = ComputerUseToolDispatcher()
        let result = dispatcher.callToolAsResult(name: "activate_window", arguments: [:])

        XCTAssertTrue(result.isError)
        XCTAssertEqual(result.primaryText, "Missing required argument: window.id")
    }

    func testDispatcherGetWindowStateRejectsBothFlagsFalse() {
        let dispatcher = ComputerUseToolDispatcher()
        let result = dispatcher.callToolAsResult(name: "get_window_state", arguments: [
            "window": ["id": 1],
            "include_screenshot": false,
            "include_text": false,
        ])

        XCTAssertTrue(result.isError)
        XCTAssertEqual(
            result.primaryText,
            "get_window_state must request include_text, include_screenshot, or both"
        )
    }

    func testDispatcherGetWindowStateRejectsNonBooleanFlags() {
        let dispatcher = ComputerUseToolDispatcher()

        let screenshotResult = dispatcher.callToolAsResult(name: "get_window_state", arguments: [
            "window": ["id": 1],
            "include_screenshot": "yes",
        ])
        XCTAssertTrue(screenshotResult.isError)
        XCTAssertEqual(screenshotResult.primaryText, "include_screenshot must be a boolean")

        let textResult = dispatcher.callToolAsResult(name: "get_window_state", arguments: [
            "window": ["id": 1],
            "include_text": 1,
        ])
        XCTAssertTrue(textResult.isError)
        XCTAssertEqual(textResult.primaryText, "include_text must be a boolean")
    }

    func testDispatcherActionToolsRequireWindowOrApp() {
        let dispatcher = ComputerUseToolDispatcher()
        let expected = "Missing required argument: provide either window (from list_windows/get_window_state) or app"

        for name in ["click", "perform_secondary_action", "scroll", "drag", "type_text", "press_key", "set_value"] {
            let result = dispatcher.callToolAsResult(name: name, arguments: [:])
            XCTAssertTrue(result.isError, "\(name) must fail without a target")
            XCTAssertEqual(result.primaryText, expected, "\(name) must use the unified missing-argument error")
        }
    }

    func testDispatcherLegacyClickRejectsScreenshotIDWithoutWindow() {
        let dispatcher = ComputerUseToolDispatcher()
        let result = dispatcher.callToolAsResult(name: "click", arguments: [
            "app": "OpenComputerUseFixture",
            "screenshotId": "shot-1-1",
        ])

        XCTAssertTrue(result.isError)
        XCTAssertEqual(
            result.primaryText,
            "screenshotId requires window targeting; pass window from get_window_state and re-observe."
        )
    }

    func testDispatcherLegacyDragRejectsScreenshotIDWithoutWindow() {
        let dispatcher = ComputerUseToolDispatcher()
        let result = dispatcher.callToolAsResult(name: "drag", arguments: [
            "app": "OpenComputerUseFixture",
            "from_x": 0.0,
            "from_y": 0.0,
            "to_x": 1.0,
            "to_y": 1.0,
            "screenshotId": "shot-1-1",
        ])

        XCTAssertTrue(result.isError)
        XCTAssertEqual(
            result.primaryText,
            "screenshotId requires window targeting; pass window from get_window_state and re-observe."
        )
    }

    func testDispatcherRejectsRightDoubleClickForBothTargets() {
        let dispatcher = ComputerUseToolDispatcher()
        let expected = "right double click is not supported"

        let appResult = dispatcher.callToolAsResult(name: "click", arguments: [
            "app": "OpenComputerUseFixture",
            "mouse_button": "right",
            "click_count": 2,
        ])
        XCTAssertTrue(appResult.isError)
        XCTAssertEqual(appResult.primaryText, expected)

        let windowResult = dispatcher.callToolAsResult(name: "click", arguments: [
            "window": ["id": 1],
            "mouse_button": "r",
            "click_count": 2,
        ])
        XCTAssertTrue(windowResult.isError)
        XCTAssertEqual(windowResult.primaryText, expected)
    }

    func testDispatcherScrollCoordinateModeValidatesArguments() {
        let dispatcher = ComputerUseToolDispatcher()

        let missingXY = dispatcher.callToolAsResult(name: "scroll", arguments: [
            "window": ["id": 1],
            "scrollY": 10.0,
        ])
        XCTAssertTrue(missingXY.isError)
        XCTAssertEqual(
            missingXY.primaryText,
            "coordinate scroll requires both x and y (window-relative)"
        )

        let zeroDelta = dispatcher.callToolAsResult(name: "scroll", arguments: [
            "window": ["id": 1],
            "x": 5.0,
            "y": 5.0,
            "scrollX": 0.0,
            "scrollY": 0.0,
        ])
        XCTAssertTrue(zeroDelta.isError)
        XCTAssertEqual(
            zeroDelta.primaryText,
            "coordinate scroll requires a non-zero scrollX or scrollY pixel delta"
        )
    }

    // MARK: - Live fixture tests (require a GUI session and the built fixture)

    private func fixtureBinaryURL() -> URL? {
        var candidates: [URL] = []

        // The test bundle and the fixture share the SwiftPM products
        // directory (the same trick the smoke suite uses for its server).
        candidates.append(
            URL(fileURLWithPath: CommandLine.arguments[0]).resolvingSymlinksInPath()
                .deletingLastPathComponent()
                .appendingPathComponent("OpenComputerUseFixture")
        )

        // Source-relative fallback: this file lives at
        // <repo>/packages/OpenComputerUseKit/Tests/OpenComputerUseKitTests.
        let filePath = URL(fileURLWithPath: #filePath)
        for base in [filePath, filePath.resolvingSymlinksInPath()] {
            candidates.append(
                base
                    .deletingLastPathComponent() // OpenComputerUseKitTests
                    .deletingLastPathComponent() // Tests
                    .deletingLastPathComponent() // OpenComputerUseKit
                    .deletingLastPathComponent() // packages
                    .appendingPathComponent(".build/debug/OpenComputerUseFixture")
            )
        }

        // `swift test` runs from the package root.
        candidates.append(
            URL(fileURLWithPath: FileManager.default.currentDirectoryPath)
                .appendingPathComponent(".build/debug/OpenComputerUseFixture")
        )

        return candidates.first { FileManager.default.isExecutableFile(atPath: $0.path) }
    }

    private final class FixtureHandle {
        let process: Process
        let pid: pid_t

        init(process: Process) {
            self.process = process
            self.pid = process.processIdentifier
        }

        func terminate() {
            guard process.isRunning else {
                return
            }

            process.terminate()
            let deadline = Date().addingTimeInterval(5)
            while process.isRunning, Date() < deadline {
                Thread.sleep(forTimeInterval: 0.05)
            }
            if process.isRunning {
                process.interrupt()
            }
        }
    }

    private func requireLiveFixture() throws -> FixtureHandle {
        guard !NSScreen.screens.isEmpty else {
            throw XCTSkip("No GUI session available for the live window management tests")
        }
        guard let fixtureURL = fixtureBinaryURL() else {
            throw XCTSkip("Fixture binary not found at .build/debug/OpenComputerUseFixture (run `swift build` first)")
        }

        // The fixture shares a single state file across instances, so stop
        // any other instances and start from a clean slate.
        terminateExistingFixtures()
        try? FileManager.default.removeItem(at: FixtureBridge.stateFileURL)

        let process = Process()
        process.executableURL = fixtureURL
        var environment = ProcessInfo.processInfo.environment
        environment.removeValue(forKey: "OPEN_COMPUTER_USE_FIXTURE_HEADLESS")
        process.environment = environment
        process.standardOutput = Pipe()
        process.standardError = Pipe()
        try process.run()

        let handle = FixtureHandle(process: process)
        Thread.sleep(forTimeInterval: 1.5)

        guard handle.process.isRunning else {
            handle.terminate()
            throw XCTSkip("Fixture process exited during startup")
        }
        return handle
    }

    private func isFixtureApplication(_ application: NSRunningApplication) -> Bool {
        application.localizedName == FixtureBridge.appName
            || application.executableURL?.deletingPathExtension().lastPathComponent == FixtureBridge.appName
    }

    private func terminateExistingFixtures() {
        for application in NSWorkspace.shared.runningApplications
        where isFixtureApplication(application)
        {
            _ = application.terminate()
        }

        let deadline = Date().addingTimeInterval(5)
        while Date() < deadline,
              NSWorkspace.shared.runningApplications.contains(where: {
                  isFixtureApplication($0) && !$0.isTerminated
              })
        {
            Thread.sleep(forTimeInterval: 0.1)
        }
    }

    private func fixtureWindowIDs() -> [CGWindowID] {
        WindowDirectory.listWindows()
            .filter { $0.app == FixtureBridge.appName }
            .map(\.id)
    }

    private func waitForFixtureWindowCount(
        _ expected: Int,
        timeout: TimeInterval = 10
    ) -> [CGWindowID]? {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            let ids = fixtureWindowIDs()
            if ids.count == expected {
                return ids
            }
            Thread.sleep(forTimeInterval: 0.2)
        }
        return fixtureWindowIDs().count == expected ? fixtureWindowIDs() : nil
    }

    private func waitForFixtureState(timeout: TimeInterval = 10) throws -> FixtureAppState {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if let state = try? FixtureBridge.readState() {
                return state
            }
            Thread.sleep(forTimeInterval: 0.1)
        }
        throw ComputerUseError.message("Timed out waiting for the fixture state file")
    }

    private func postFixtureCommand(_ kind: String, identifier: String, value: String? = nil) throws {
        try FixtureBridge.post(FixtureCommand(kind: kind, identifier: identifier, value: value))
        Thread.sleep(forTimeInterval: 0.5)
    }

    private func decodeToolJSON(_ result: ToolCallResult) throws -> Any {
        let text = try XCTUnwrap(result.primaryText)
        let data = try XCTUnwrap(text.data(using: .utf8))
        return try JSONSerialization.jsonObject(with: data)
    }

    private func windowJSON(_ id: CGWindowID) -> [String: Any] {
        ["app": FixtureBridge.appName, "id": Int(id)]
    }

    private func focusedWindowFrame(pid: pid_t) -> CGRect? {
        let appElement = AXUIElementCreateApplication(pid)
        var value: CFTypeRef?
        guard
            AXUIElementCopyAttributeValue(appElement, kAXFocusedWindowAttribute as CFString, &value) == .success,
            let value,
            CFGetTypeID(value) == AXUIElementGetTypeID()
        else {
            return nil
        }

        let window = (value as! AXUIElement)
        var positionValue: CFTypeRef?
        var sizeValue: CFTypeRef?
        guard
            AXUIElementCopyAttributeValue(window, kAXPositionAttribute as CFString, &positionValue) == .success,
            AXUIElementCopyAttributeValue(window, kAXSizeAttribute as CFString, &sizeValue) == .success,
            let positionValue,
            let sizeValue,
            CFGetTypeID(positionValue) == AXValueGetTypeID(),
            CFGetTypeID(sizeValue) == AXValueGetTypeID()
        else {
            return nil
        }

        var position = CGPoint.zero
        var size = CGSize.zero
        guard
            AXValueGetValue(positionValue as! AXValue, .cgPoint, &position),
            AXValueGetValue(sizeValue as! AXValue, .cgSize, &size)
        else {
            return nil
        }

        return CGRect(origin: position, size: size)
    }

    private func expectComputerUseError(
        _ message: String,
        file: StaticString = #filePath,
        line: UInt = #line,
        _ operation: () throws -> ToolCallResult
    ) {
        do {
            _ = try operation()
            XCTFail("Expected error \(message), but the operation succeeded", file: file, line: line)
        } catch let error as ComputerUseError {
            XCTAssertEqual(error.errorDescription, message, file: file, line: line)
        } catch {
            XCTFail("Expected a ComputerUseError, got: \(error)", file: file, line: line)
        }
    }

    private func frameMatches(_ lhs: CGRect, _ rhs: CGRect, tolerance: CGFloat = 2) -> Bool {
        abs(lhs.minX - rhs.minX) <= tolerance
            && abs(lhs.minY - rhs.minY) <= tolerance
            && abs(lhs.width - rhs.width) <= tolerance
            && abs(lhs.height - rhs.height) <= tolerance
    }

    func testListWindowsShowsMultipleFixtureWindowsIncludingDialog() throws {
        let fixture = try requireLiveFixture()
        defer { fixture.terminate() }

        let dispatcher = ComputerUseToolDispatcher()

        guard let initialIDs = waitForFixtureWindowCount(1) else {
            XCTFail("Expected exactly one fixture window after launch")
            return
        }
        let mainID = try XCTUnwrap(initialIDs.first)

        let firstList = try dispatcher.callTool(name: "list_windows", arguments: [:])
        XCTAssertFalse(firstList.isError, "list_windows must succeed: \(firstList.primaryText ?? "")")
        let firstEntries = try decodeToolJSON(firstList) as? [[String: Any]]
        let fixtureEntries = (firstEntries ?? []).filter { $0["app"] as? String == FixtureBridge.appName }
        XCTAssertEqual(fixtureEntries.count, 1)
        // Titles require Screen Recording permission; assert only when visible.
        if let title = fixtureEntries.first(where: { $0["id"] as? Int == Int(mainID) })?["title"] as? String {
            XCTAssertEqual(title, "OpenComputerUseFixture")
        }

        // A second top-level window with a distinct title.
        try postFixtureCommand("open_window", identifier: "fixture-second", value: "Second Window A")
        guard let twoIDs = waitForFixtureWindowCount(2) else {
            XCTFail("Expected two fixture windows after open_window")
            return
        }
        let secondID = try XCTUnwrap(twoIDs.first { $0 != mainID })

        // A modal dialog window.
        try postFixtureCommand("show_dialog", identifier: "fixture-dialog")
        guard let threeIDs = waitForFixtureWindowCount(3) else {
            XCTFail("Expected three fixture windows after show_dialog")
            return
        }
        let dialogID = try XCTUnwrap(threeIDs.first { $0 != mainID && $0 != secondID })
        if let dialogTitle = WindowDirectory.listWindows().first { $0.id == dialogID }?.title {
            XCTAssertEqual(dialogTitle, "Fixture Dialog")
        }

        // Dismiss the dialog and close the second window.
        try postFixtureCommand("dismiss_dialog", identifier: "fixture-dialog")
        XCTAssertEqual(
            waitForFixtureWindowCount(2)?.filter { $0 != dialogID }.sorted(),
            [mainID, secondID].sorted()
        )

        try postFixtureCommand("close_window", identifier: "fixture-second")
        XCTAssertEqual(waitForFixtureWindowCount(1), [mainID])
    }

    func testIdenticalTitleWindowsResolveIndependently() throws {
        let fixture = try requireLiveFixture()
        defer { fixture.terminate() }

        _ = try waitForFixtureState()
        guard let mainID = fixtureWindowIDs().first else {
            XCTFail("Expected the fixture main window")
            return
        }

        // Open a second window with the exact same title as the main one.
        try postFixtureCommand("open_window", identifier: "fixture-second", value: "OpenComputerUseFixture")
        guard let twoIDs = waitForFixtureWindowCount(2) else {
            XCTFail("Expected two fixture windows with identical titles")
            return
        }
        let secondID = try XCTUnwrap(twoIDs.first { $0 != mainID })

        let dispatcher = ComputerUseToolDispatcher()
        let mainRef = try dispatcher.callTool(name: "get_window", arguments: ["window": windowJSON(mainID)])
        let secondRef = try dispatcher.callTool(name: "get_window", arguments: ["window": windowJSON(secondID)])
        XCTAssertFalse(mainRef.isError)
        XCTAssertFalse(secondRef.isError)
        XCTAssertEqual((try decodeToolJSON(mainRef) as? [String: Any])?["id"] as? Int, Int(mainID))
        XCTAssertEqual((try decodeToolJSON(secondRef) as? [String: Any])?["id"] as? Int, Int(secondID))

        // Both windows are observable through get_window_state; the ids in
        // the responses stay bound to the requested windows.
        let mainState = try dispatcher.callTool(name: "get_window_state", arguments: [
            "window": windowJSON(mainID),
            "include_text": true,
        ])
        let secondState = try dispatcher.callTool(name: "get_window_state", arguments: [
            "window": windowJSON(secondID),
            "include_text": true,
        ])
        XCTAssertFalse(mainState.isError, "get_window_state(main) must succeed: \(mainState.primaryText ?? "")")
        XCTAssertFalse(secondState.isError, "get_window_state(second) must succeed: \(secondState.primaryText ?? "")")

        let mainPayload = try decodeToolJSON(mainState) as? [String: Any]
        let secondPayload = try decodeToolJSON(secondState) as? [String: Any]
        XCTAssertEqual((mainPayload?["window"] as? [String: Any])?["id"] as? Int, Int(mainID))
        XCTAssertEqual((secondPayload?["window"] as? [String: Any])?["id"] as? Int, Int(secondID))
        XCTAssertEqual((mainPayload?["window"] as? [String: Any])?["app"] as? String, FixtureBridge.appName)

        // Both windows render a non-empty accessibility tree.
        let mainTree = (mainPayload?["accessibility"] as? [String: Any])?["tree"] as? String
        let secondTree = (secondPayload?["accessibility"] as? [String: Any])?["tree"] as? String
        XCTAssertFalse(mainTree?.isEmpty == true)
        XCTAssertFalse(secondTree?.isEmpty == true)

        // With screenshots, the two windows must be observed at their own
        // on-screen positions (the second window is offset by 40pt+).
        if let mainShot = (mainPayload?["screenshots"] as? [[String: Any]])?.first,
           let secondShot = (secondPayload?["screenshots"] as? [[String: Any]])?.first,
           let mainOriginX = mainShot["originX"] as? Double,
           let secondOriginX = secondShot["originX"] as? Double
        {
            XCTAssertNotEqual(mainOriginX, secondOriginX, "Identical-title windows must be observed independently")
        }
    }

    func testWindowAppFieldIsInformationalOnly() throws {
        let fixture = try requireLiveFixture()
        defer { fixture.terminate() }

        _ = try waitForFixtureState()
        guard let mainID = fixtureWindowIDs().first else {
            XCTFail("Expected the fixture main window")
            return
        }

        let dispatcher = ComputerUseToolDispatcher()
        let result = try dispatcher.callTool(name: "get_window", arguments: [
            "window": ["app": "Definitely Wrong App", "id": Int(mainID)],
        ])
        XCTAssertFalse(result.isError, "The window id is authoritative; a wrong app field must not fail: \(result.primaryText ?? "")")

        let payload = try decodeToolJSON(result) as? [String: Any]
        XCTAssertEqual(payload?["app"] as? String, FixtureBridge.appName)
        XCTAssertEqual(payload?["id"] as? Int, Int(mainID))
    }

    func testOutOfRangeWindowIDsAreRejectedWithoutTrapping() {
        let dispatcher = ComputerUseToolDispatcher()
        let tooLarge = Int(UInt32.max) + 1
        let cases: [[String: Any]] = [
            ["window": ["id": 5_000_000_000]],
            ["window": ["id": "99999999999"]],
            ["window_id": 5_000_000_000],
            ["window_id": "99999999999"],
            ["window": ["id": tooLarge]],
        ]
        for arguments in cases {
            let result = dispatcher.callToolAsResult(name: "get_window", arguments: arguments)
            XCTAssertTrue(result.isError, "\(arguments) must fail instead of trapping")
            XCTAssertTrue(
                result.primaryText?.hasPrefix("staleWindowHandle(") == true,
                "\(arguments) should report a stale handle, got: \(result.primaryText ?? "")"
            )
        }
    }

    func testIdentityVerdictOnlyReportsAbsentWhenProvablyClosed() {
        let target: CGWindowID = 42
        typealias Directory = WindowDirectory

        XCTAssertEqual(Directory.identityVerdict(mappedIDs: [7, 42], windowID: target, isOnCurrentSpace: false), .confirmed)
        XCTAssertEqual(Directory.identityVerdict(mappedIDs: [7, 8], windowID: target, isOnCurrentSpace: true), .absent)
        // Empty AX list: no conclusion.
        XCTAssertEqual(Directory.identityVerdict(mappedIDs: [], windowID: target, isOnCurrentSpace: true), .indeterminate)
        // Other Space / full-screen Space: AX cannot see the window.
        XCTAssertEqual(Directory.identityVerdict(mappedIDs: [7, 8], windowID: target, isOnCurrentSpace: false), .indeterminate)
        // Unmappable window in the list: no conclusion.
        XCTAssertEqual(Directory.identityVerdict(mappedIDs: [7, nil], windowID: target, isOnCurrentSpace: true), .indeterminate)
    }

    func testStaleAndInvalidWindowIDsAreRejected() throws {
        let fixture = try requireLiveFixture()
        defer { fixture.terminate() }

        _ = try waitForFixtureState()
        let dispatcher = ComputerUseToolDispatcher()
        let missingWindowMessage = "staleWindowHandle(999999): the window is no longer open; re-observe with list_windows."

        let tools: [(String, [String: Any])] = [
            ("get_window", ["window": ["id": 999999]]),
            ("get_window_state", ["window": ["id": 999999], "include_text": true]),
            ("activate_window", ["window": ["id": 999999]]),
            ("click", ["window": ["id": 999999], "element_index": "1"]),
        ]
        for (name, arguments) in tools {
            let result = dispatcher.callToolAsResult(name: name, arguments: arguments)
            XCTAssertTrue(result.isError, "\(name) with an unknown id must fail")
            XCTAssertEqual(result.primaryText, missingWindowMessage, "\(name) must report the official stale handle error")
        }

        // A real id becomes stale once the window goes away.
        guard let liveID = fixtureWindowIDs().first else {
            throw XCTSkip("No fixture window available for the termination part of the test")
        }
        fixture.terminate()
        let deadline = Date().addingTimeInterval(5)
        var stale = false
        while Date() < deadline {
            let result = dispatcher.callToolAsResult(name: "get_window", arguments: ["window": windowJSON(liveID)])
            if result.isError, (result.primaryText ?? "").hasPrefix("staleWindowHandle(\(liveID)):") {
                stale = true
                break
            }
            Thread.sleep(forTimeInterval: 0.1)
        }
        XCTAssertTrue(stale, "The terminated fixture's window must become stale")
    }

    func testWindowTargetedActionsRequirePriorObservation() throws {
        let fixture = try requireLiveFixture()
        defer { fixture.terminate() }

        _ = try waitForFixtureState()
        guard let mainID = fixtureWindowIDs().first else {
            XCTFail("Expected the fixture main window")
            return
        }

        let dispatcher = ComputerUseToolDispatcher()
        let result = dispatcher.callToolAsResult(name: "click", arguments: [
            "window": windowJSON(mainID),
            "element_index": "1",
        ])
        XCTAssertTrue(result.isError)
        XCTAssertEqual(
            result.primaryText,
            "No window state is available for window id \(mainID). Run get_window_state before action tools."
        )
    }

    func testScreenshotIDLifecycleAndCoordinateGate() throws {
        let fixture = try requireLiveFixture()
        defer { fixture.terminate() }

        _ = try waitForFixtureState()
        guard let mainID = fixtureWindowIDs().first else {
            XCTFail("Expected the fixture main window")
            return
        }
        let service = ComputerUseService()
        let ref = WindowRef(app: FixtureBridge.appName, id: mainID, title: nil)

        // Coordinate input on a never-observed window is rejected.
        expectComputerUseError(
            "call get_window_state before issuing coordinate input"
        ) {
            try service.click(
                window: ref,
                elementIndex: nil,
                x: 10,
                y: 10,
                clickCount: 1,
                mouseButton: "left"
            )
        }

        // An observation without a screenshot is not enough for coordinates.
        let textOnly = try service.getWindowState(
            window: ref,
            includeScreenshot: false,
            includeText: true
        )
        XCTAssertFalse(textOnly.isError, "get_window_state(include_text) must succeed: \(textOnly.primaryText ?? "")")
        expectComputerUseError(
            "call get_window_state with include_screenshot before issuing coordinate input"
        ) {
            try service.click(
                window: ref,
                elementIndex: nil,
                x: 10,
                y: 10,
                clickCount: 1,
                mouseButton: "left"
            )
        }

        // A full observation issues a screenshot id (when screen capture is
        // available) and the id is only valid until the next successful action.
        let observed = try service.getWindowState(
            window: WindowRef(app: FixtureBridge.appName, id: mainID, title: nil),
            includeScreenshot: true,
            includeText: true
        )
        XCTAssertFalse(observed.isError, "get_window_state must succeed: \(observed.primaryText ?? "")")
        let payload = try decodeToolJSON(observed) as? [String: Any]
        let screenshots = payload?["screenshots"] as? [[String: Any]]
        guard let screenshot = screenshots?.first, let shotID = screenshot["id"] as? String else {
            return // No screen recording permission: screenshot-dependent assertions are conditional.
        }
        XCTAssertTrue(shotID.hasPrefix("shot-\(mainID)-"))

        let state = try waitForFixtureState()
        let incrementIndex = try XCTUnwrap(
            state.elements.first { $0.identifier == "fixture-increment" }?.index
        )
        let initialCounter = try XCTUnwrap(state.elements.first { $0.identifier == "fixture-counter-label" })
            .value
            .flatMap { $0.replacingOccurrences(of: "Counter: ", with: "").fixtureIntValue }

        // A pixel-accurate click through the window2 flow (screenshot pixels,
        // validated by the observation binding).
        let meta = try XCTUnwrap(service.windowScreenshotMeta[mainID])
        let pixelSize = try XCTUnwrap(meta.screenshotPixelSize)
        let windowPoint = CGPoint(
            x: state.elements.first { $0.identifier == "fixture-increment" }!.frame.cgRect.midX,
            y: state.elements.first { $0.identifier == "fixture-increment" }!.frame.cgRect.midY
        )
        let pixelPoint = CGPoint(
            x: windowPoint.x * (pixelSize.width / meta.bounds.width),
            y: windowPoint.y * (pixelSize.height / meta.bounds.height)
        )

        let firstClick = try service.click(
            window: WindowRef(app: FixtureBridge.appName, id: mainID, title: nil),
            elementIndex: nil,
            x: pixelPoint.x,
            y: pixelPoint.y,
            clickCount: 1,
            mouseButton: "left",
            clickMethod: .auto,
            screenshotID: shotID
        )
        XCTAssertFalse(firstClick.isError, "Window-targeted pixel click must succeed: \(firstClick.primaryText ?? "")")

        let afterClick = try waitForFixtureState()
        let clickedCounter = try XCTUnwrap(afterClick.elements.first { $0.identifier == "fixture-counter-label" })
            .value
            .flatMap { $0.replacingOccurrences(of: "Counter: ", with: "").fixtureIntValue }
        XCTAssertEqual(clickedCounter, initialCounter.map { $0 + 1 }, "The pixel click must hit the increment button")

        // The successful action invalidated the observation: the same id is
        // now stale.
        expectComputerUseError(
            "stale screenshot id; re-observe with get_window_state before retrying."
        ) {
            try service.click(
                window: ref,
                elementIndex: String(incrementIndex),
                x: nil,
                y: nil,
                clickCount: 1,
                mouseButton: "left",
                screenshotID: shotID
            )
        }
    }

    func testAppTerminationMidOperationIsReportedAsStale() throws {
        let fixture = try requireLiveFixture()
        defer { fixture.terminate() }

        _ = try waitForFixtureState()
        guard let mainID = fixtureWindowIDs().first else {
            XCTFail("Expected the fixture main window")
            return
        }

        let service = ComputerUseService()
        let observed = try service.getWindowState(
            window: WindowRef(app: FixtureBridge.appName, id: mainID, title: nil),
            includeScreenshot: true,
            includeText: true
        )
        XCTAssertFalse(observed.isError)

        fixture.terminate()

        let deadline = Date().addingTimeInterval(5)
        var stale: String?
        while Date() < deadline {
            do {
                _ = try service.click(
                    window: WindowRef(app: FixtureBridge.appName, id: mainID, title: nil),
                    elementIndex: "1",
                    x: nil,
                    y: nil,
                    clickCount: 1,
                    mouseButton: "left"
                )
            } catch let error as ComputerUseError {
                let message = error.errorDescription ?? ""
                if message.hasPrefix("staleWindowHandle(\(mainID)):") {
                    stale = message
                    break
                }
            } catch {
                // Unexpected error type: keep polling until the window goes stale.
            }
            Thread.sleep(forTimeInterval: 0.1)
        }
        XCTAssertNotNil(stale, "Clicking a terminated fixture must report a staleWindowHandle error, got: \(stale ?? "nil")")
    }

    func testActivateWindowSwitchesFocusedWindow() throws {
        guard AXIsProcessTrusted() else {
            throw XCTSkip("Accessibility is not trusted; skipping the live activation test")
        }

        let fixture = try requireLiveFixture()
        defer { fixture.terminate() }

        _ = try waitForFixtureState()
        guard let mainID = fixtureWindowIDs().first else {
            XCTFail("Expected the fixture main window")
            return
        }

        try postFixtureCommand("open_window", identifier: "fixture-second", value: "Second Window B")
        guard let twoIDs = waitForFixtureWindowCount(2) else {
            XCTFail("Expected two fixture windows for the activation test")
            return
        }
        let secondID = try XCTUnwrap(twoIDs.first { $0 != mainID })

        let service = ComputerUseService()

        let activatedSecond = try service.activateWindow(
            window: WindowRef(app: FixtureBridge.appName, id: secondID, title: "Second Window B")
        )
        let secondRef = try decodeToolJSON(activatedSecond) as? [String: Any]
        XCTAssertEqual(secondRef?["id"] as? Int, Int(secondID))

        var focused = focusedWindowFrame(pid: fixture.pid)
        if focused == nil {
            Thread.sleep(forTimeInterval: 0.5)
            focused = focusedWindowFrame(pid: fixture.pid)
        }
        let secondBounds = try XCTUnwrap(WindowDirectory.currentBounds(for: secondID))
        XCTAssertEqual(
            frameMatches(focused ?? .null, secondBounds),
            true,
            "The second window must become the focused window (focused: \(String(describing: focused)), expected: \(secondBounds))"
        )

        // Switch back to the main window.
        let activatedMain = try service.activateWindow(
            window: WindowRef(app: FixtureBridge.appName, id: mainID, title: "OpenComputerUseFixture")
        )
        let mainRef = try decodeToolJSON(activatedMain) as? [String: Any]
        XCTAssertEqual(mainRef?["id"] as? Int, Int(mainID))

        focused = focusedWindowFrame(pid: fixture.pid)
        if focused == nil {
            Thread.sleep(forTimeInterval: 0.5)
            focused = focusedWindowFrame(pid: fixture.pid)
        }
        let mainBounds = try XCTUnwrap(WindowDirectory.currentBounds(for: mainID))
        XCTAssertEqual(
            frameMatches(focused ?? .null, mainBounds),
            true,
            "The main window must become the focused window again (focused: \(String(describing: focused)), expected: \(mainBounds))"
        )
    }

    // MARK: - Window identity (same bounds + closed-window ghosts)

    private func axStringAttribute(_ element: AXUIElement, _ attribute: String) -> String? {
        var value: CFTypeRef?
        guard AXUIElementCopyAttributeValue(element, attribute as CFString, &value) == .success, let value else {
            return nil
        }
        return value as? String
    }

    private func axWindowElement(pid: pid_t, title: String) -> AXUIElement? {
        let appElement = AXUIElementCreateApplication(pid)
        var value: CFTypeRef?
        guard
            AXUIElementCopyAttributeValue(appElement, kAXWindowsAttribute as CFString, &value) == .success,
            let windows = value as? [AXUIElement]
        else {
            return nil
        }

        return windows.first { element in
            axStringAttribute(element, kAXTitleAttribute as String) == title
        }
    }

    func testSameBoundsWindowsResolveIndependentIdentity() throws {
        guard AXIsProcessTrusted() else {
            throw XCTSkip("Accessibility is not trusted; skipping the same-bounds identity test")
        }

        let fixture = try requireLiveFixture()
        defer { fixture.terminate() }

        _ = try waitForFixtureState()
        guard let mainID = fixtureWindowIDs().first else {
            XCTFail("Expected the fixture main window")
            return
        }

        // A second window at exactly the main window's frame with the same
        // title: the case frame-based matching cannot disambiguate (it
        // returns the first candidate, i.e. an arbitrary window).
        try postFixtureCommand(
            "open_window",
            identifier: "fixture-second-same-bounds",
            value: "OpenComputerUseFixture"
        )
        guard let twoIDs = waitForFixtureWindowCount(2) else {
            XCTFail("Expected two same-bounds fixture windows")
            return
        }
        let secondID = try XCTUnwrap(twoIDs.first { $0 != mainID })

        let mainEntry = try XCTUnwrap(WindowDirectory.currentEntry(for: mainID))
        let secondEntry = try XCTUnwrap(WindowDirectory.currentEntry(for: secondID))
        XCTAssertTrue(
            frameMatches(mainEntry.bounds, secondEntry.bounds),
            "The fixture must create windows with identical bounds (got \(mainEntry.bounds) vs \(secondEntry.bounds))"
        )
        XCTAssertEqual(mainEntry.title, secondEntry.title, "The regression case needs identical titles")

        let appElement = AXUIElementCreateApplication(fixture.pid)

        if AXWindowIdentitySPI.shared.isAvailable {
            // Identity-based matching returns the exact window for each id,
            // verified through the accessibility identifiers the fixture
            // sets on its windows (independent of the matching logic).
            switch WindowDirectory.matchAXWindow(appElement: appElement, entry: mainEntry) {
            case .matched(let window):
                XCTAssertEqual(
                    axStringAttribute(window, "AXIdentifier"),
                    "fixture-window",
                    "The main window's entry must match the main window's AX element"
                )
            case .ambiguous:
                XCTFail("Same-bounds windows must not be ambiguous while the identity mapping is available")
            case .notFound:
                XCTFail("The main window must match its own entry")
            }

            switch WindowDirectory.matchAXWindow(appElement: appElement, entry: secondEntry) {
            case .matched(let window):
                XCTAssertEqual(
                    axStringAttribute(window, "AXIdentifier"),
                    "fixture-second-window",
                    "The second window's entry must match the second window's AX element, not the first same-bounds candidate"
                )
            case .ambiguous:
                XCTFail("Same-bounds windows must not be ambiguous while the identity mapping is available")
            case .notFound:
                XCTFail("The second window must match its own entry")
            }
        } else {
            // Without the identity mapping, identical bounds with identical
            // titles must be reported as ambiguous rather than guessed.
            guard case .ambiguous = WindowDirectory.matchAXWindow(appElement: appElement, entry: mainEntry) else {
                XCTFail("Expected .ambiguous for the main entry without the identity mapping")
                return
            }
            guard case .ambiguous = WindowDirectory.matchAXWindow(appElement: appElement, entry: secondEntry) else {
                XCTFail("Expected .ambiguous for the second entry without the identity mapping")
                return
            }
        }
    }

    func testClosedWindowIsRejectedEvenWhileCGEntryLingers() throws {
        let fixture = try requireLiveFixture()
        defer { fixture.terminate() }

        _ = try waitForFixtureState()
        guard let mainID = fixtureWindowIDs().first else {
            XCTFail("Expected the fixture main window")
            return
        }

        try postFixtureCommand("open_window", identifier: "fixture-second", value: "Ghost Probe Window")
        guard let twoIDs = waitForFixtureWindowCount(2) else {
            XCTFail("Expected the second fixture window")
            return
        }
        let secondID = try XCTUnwrap(twoIDs.first { $0 != mainID })

        let dispatcher = ComputerUseToolDispatcher()
        let staleMessage = "staleWindowHandle(\(secondID)): the window is no longer open; re-observe with list_windows."

        // Baseline: the live window resolves.
        let live = dispatcher.callToolAsResult(name: "get_window", arguments: ["window": ["id": Int(secondID)]])
        XCTAssertFalse(live.isError, "get_window on the live window must succeed: \(live.primaryText ?? "")")

        // Close the window. The CGWindow entry can linger in the full
        // window list while the close animation runs — get_window must
        // reject the id (AX-identity check) and must never succeed again.
        try FixtureBridge.post(FixtureCommand(kind: "close_window", identifier: "fixture-second"))

        let deadline = Date().addingTimeInterval(3)
        var rejected = false
        var rejectedWhileEntryLingering = false
        while Date() < deadline {
            let entryLingering = WindowDirectory.currentEntry(for: secondID) != nil
            let result = dispatcher.callToolAsResult(name: "get_window", arguments: ["window": ["id": Int(secondID)]])
            if result.isError {
                guard (result.primaryText ?? "") == staleMessage else {
                    XCTFail("Expected the official stale error, got: \(result.primaryText ?? "")")
                    break
                }
                rejected = true
                if entryLingering {
                    rejectedWhileEntryLingering = true
                }
                break
            }
            // The close command is asynchronous; the window may still be
            // open for a moment after the post.
            Thread.sleep(forTimeInterval: 0.02)
        }

        XCTAssertTrue(rejected, "get_window on the closed window must report the official stale error")
        // Evidence, not a hard assertion: the new behavior is the rejection
        // while the CG entry still lingers. Kept out of the assertion so the
        // test stays deterministic on machines where the entry purges fast
        // or the identity mapping is unavailable (indeterminate fallback).
        print("ghost rejected while CG entry lingering: \(rejectedWhileEntryLingering)")

        // Action tools on the closed id fail with the same official error.
        let actionTools: [(String, [String: Any])] = [
            ("activate_window", ["window": ["id": Int(secondID)]]),
            ("click", ["window": ["id": Int(secondID)], "element_index": "1"]),
        ]
        for (name, arguments) in actionTools {
            let result = dispatcher.callToolAsResult(name: name, arguments: arguments)
            XCTAssertTrue(result.isError, "\(name) on the closed window must fail")
            XCTAssertEqual(result.primaryText, staleMessage, "\(name) must report the official stale error")
        }
    }

    func testMinimizedWindowStillResolves() throws {
        guard AXIsProcessTrusted() else {
            throw XCTSkip("Accessibility is not trusted; skipping the minimized-window resolution test")
        }

        let fixture = try requireLiveFixture()
        defer { fixture.terminate() }

        _ = try waitForFixtureState()
        guard let mainID = fixtureWindowIDs().first else {
            XCTFail("Expected the fixture main window")
            return
        }

        try postFixtureCommand("open_window", identifier: "fixture-second", value: "Minimize Probe Window")
        guard let twoIDs = waitForFixtureWindowCount(2) else {
            XCTFail("Expected the second fixture window")
            return
        }
        let secondID = try XCTUnwrap(twoIDs.first { $0 != mainID })

        // Minimize the second window through the accessibility API.
        guard let window = axWindowElement(pid: fixture.pid, title: "Minimize Probe Window") else {
            throw XCTSkip("The second fixture window does not expose an accessibility element")
        }
        guard AXUIElementSetAttributeValue(window, kAXMinimizedAttribute as CFString, kCFBooleanTrue) == .success else {
            throw XCTSkip("The fixture window could not be minimized")
        }
        Thread.sleep(forTimeInterval: 0.5)

        // A minimized window is still a live window: it stays in the app's
        // accessibility window list and must resolve (no false rejection).
        let dispatcher = ComputerUseToolDispatcher()
        let result = dispatcher.callToolAsResult(name: "get_window", arguments: ["window": ["id": Int(secondID)]])
        XCTAssertFalse(result.isError, "A minimized window must still resolve: \(result.primaryText ?? "")")

        // Restore the window.
        _ = AXUIElementSetAttributeValue(window, kAXMinimizedAttribute as CFString, kCFBooleanFalse)
        Thread.sleep(forTimeInterval: 0.5)
    }
}

private extension String {
    var fixtureIntValue: Int? {
        Int(self)
    }
}
