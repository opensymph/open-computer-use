import AppKit
import Foundation
import XCTest
@testable import OpenComputerUseKit

final class LaunchAppTests: XCTestCase {
    private let calculatorBundleIdentifier = "com.apple.calculator"

    // MARK: Dispatcher routing

    func testDispatcherRejectsMissingAppArgument() {
        let dispatcher = ComputerUseToolDispatcher()
        let result = dispatcher.callToolAsResult(name: "launch_app", arguments: [:])

        XCTAssertTrue(result.isError)
        XCTAssertEqual(result.primaryText, "Missing required argument: app")
    }

    func testDispatcherRoutesLaunchAppToNativeImplementation() {
        let dispatcher = ComputerUseToolDispatcher()
        let result = dispatcher.callToolAsResult(
            name: "launch_app",
            arguments: ["app": "com.opencpu.definitely-not-installed-0"]
        )

        XCTAssertTrue(result.isError)
        XCTAssertNil(
            result.primaryText?.range(of: "not supported yet on macOS"),
            "launch_app must no longer report the window2 stub error on macOS"
        )
        XCTAssertEqual(result.primaryText, "appNotFound(\"com.opencpu.definitely-not-installed-0\")")
    }

    // MARK: Nonexistent apps

    func testLaunchNonexistentBundleIdentifier() {
        let query = "com.opencpu.definitely-not-installed-0"
        XCTAssertThrowsError(try AppDiscovery.launch(query)) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "appNotFound(\"\(query)\")"
            )
        }
    }

    func testLaunchNonexistentAppName() {
        let query = "definitely-not-a-real-app-name-xyz-42"
        XCTAssertThrowsError(try AppDiscovery.launch(query)) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "appNotFound(\"\(query)\")"
            )
        }
    }

    func testLaunchEmptyQueryIsRejected() {
        XCTAssertThrowsError(try AppDiscovery.launch("   ")) { error in
            XCTAssertTrue(
                (error as? ComputerUseError)?.errorDescription?.hasPrefix("invalidArguments(") == true
            )
        }
    }

    // MARK: Security policy

    func testLaunchBlockedBundleIdentifierIsDenied() {
        XCTAssertThrowsError(try AppDiscovery.launch("com.1password.1password")) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "Computer Use is not allowed to use the app 'com.1password.1password' for safety reasons."
            )
        }
    }

    func testLaunchBlockedBundleIdentifierIsDeniedCaseInsensitively() {
        for query in ["COM.1PASSWORD.1PASSWORD", "com.lastpass.lastpass", "com.lastpass.LastPass"] {
            XCTAssertThrowsError(try AppDiscovery.launch(query), query) { error in
                XCTAssertEqual(
                    (error as? ComputerUseError)?.errorDescription,
                    "Computer Use is not allowed to use the app '\(query)' for safety reasons."
                )
            }
        }
        XCTAssertTrue(AppSafetyPolicy.isBlocked(bundleIdentifier: "Com.Bitwarden.Desktop"))
    }

    func testLaunchRejectsNonApplicationAndRelativePaths() {
        for query in ["/tmp", "/etc/hosts", "relative/path/Foo.app", "/tmp/definitely-not-an-app.txt"] {
            XCTAssertThrowsError(try AppDiscovery.launch(query), query) { error in
                let description = (error as? ComputerUseError)?.errorDescription ?? ""
                XCTAssertTrue(
                    description.hasPrefix("invalidArguments(") || description.hasPrefix("appNotFound("),
                    "\(query) should be rejected, got: \(description)"
                )
            }
        }
    }

    func testLaunchAppSuffixedNameFallsBackToNameLookup() {
        let query = "Definitely-Not-A-Real-App-42.app"
        XCTAssertThrowsError(try AppDiscovery.launch(query)) { error in
            XCTAssertEqual(
                (error as? ComputerUseError)?.errorDescription,
                "appNotFound(\"\(query)\")"
            )
        }
    }

    // MARK: Real launch (requires a GUI session)

    func testLaunchAppByBundleIdentifierThenReusesInstanceByName() throws {
        guard !NSScreen.screens.isEmpty else {
            throw XCTSkip("No GUI session available for the live launch_app test")
        }
        guard NSWorkspace.shared.urlForApplication(withBundleIdentifier: calculatorBundleIdentifier) != nil else {
            throw XCTSkip("Calculator is not installed on this system")
        }

        let fileManager = FileManager.default
        let preexisting = NSWorkspace.shared.runningApplications.first {
            $0.bundleIdentifier == calculatorBundleIdentifier && !$0.isTerminated
        }

        defer {
            if preexisting == nil {
                for application in NSWorkspace.shared.runningApplications where
                    application.bundleIdentifier == calculatorBundleIdentifier
                {
                    _ = application.terminate()
                }
                let deadline = Date().addingTimeInterval(5)
                while Date() < deadline,
                      NSWorkspace.shared.runningApplications.contains(where: {
                          $0.bundleIdentifier == calculatorBundleIdentifier && !$0.isTerminated
                      })
                {
                    Thread.sleep(forTimeInterval: 0.1)
                }
            }
        }

        let service = ComputerUseService()

        let firstResult = try service.launchApp(app: calculatorBundleIdentifier)
        XCTAssertFalse(firstResult.isError, "launch_app must succeed: \(firstResult.primaryText ?? "")")
        let firstJSON = try decodeLaunchAppResult(firstResult)
        let firstPID = try requirePositiveInt(firstJSON, key: "pid")
        XCTAssertEqual(firstJSON["bundleIdentifier"] as? String, calculatorBundleIdentifier)
        let firstName = firstJSON["name"] as? String
        XCTAssertFalse((firstName ?? "").isEmpty)
        let firstWindows = firstJSON["windows"] as? [[String: Any]]
        XCTAssertNotNil(firstWindows, "launch_app must report the available windows")

        // The app may still be creating its first window; give it a moment.
        var windows = firstWindows ?? []
        let deadline = Date().addingTimeInterval(10)
        while windows.isEmpty, Date() < deadline {
            Thread.sleep(forTimeInterval: 0.2)
            windows = AppDiscovery.windows(for: pid_t(firstPID)).map {
                [
                    "app": firstName ?? "",
                    "id": Int($0.id),
                    "title": $0.title ?? "",
                ] as [String: Any]
            }
        }
        XCTAssertFalse(windows.isEmpty, "Calculator must expose at least one on-screen window after launch")
        for window in windows {
            let windowID = window["id"] as? Int
            XCTAssertNotNil(windowID)
            XCTAssertTrue((windowID ?? 0) > 0)
            XCTAssertNotNil(window["title"])
        }

        let secondResult = try service.launchApp(app: "Calculator")
        XCTAssertFalse(secondResult.isError, "launch_app must succeed: \(secondResult.primaryText ?? "")")
        let secondJSON = try decodeLaunchAppResult(secondResult)
        let secondPID = try requirePositiveInt(secondJSON, key: "pid")

        XCTAssertEqual(
            secondPID,
            firstPID,
            "launch_app must reuse the running instance instead of creating a duplicate"
        )

        let instances = NSWorkspace.shared.runningApplications.filter {
            $0.bundleIdentifier == calculatorBundleIdentifier && !$0.isTerminated
        }
        XCTAssertEqual(
            instances.count,
            1,
            "launch_app must not spawn duplicate instances (observed pids: \(instances.map(\.processIdentifier)))"
        )

        if let preexisting, preexisting.processIdentifier != firstPID {
            XCTFail("The preexisting Calculator instance must have been reused")
        }
    }

    private func decodeLaunchAppResult(_ result: ToolCallResult) throws -> [String: Any] {
        let text = try XCTUnwrap(result.primaryText)
        let data = try XCTUnwrap(text.data(using: .utf8))
        let object = try JSONSerialization.jsonObject(with: data)
        return try XCTUnwrap(object as? [String: Any])
    }

    private func requirePositiveInt(_ json: [String: Any], key: String) throws -> Int {
        let value = try XCTUnwrap(json[key] as? Int, "launch_app result missing \(key)")
        XCTAssertTrue(value > 0, "\(key) must be positive, got \(value)")
        return value
    }
}
