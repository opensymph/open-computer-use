import XCTest

@testable import OpenComputerUseKit

final class DesktopCommandTests: XCTestCase {
    // MARK: - CLI parsing

    func testCLIRecognizesScreenshotCommand() throws {
        XCTAssertEqual(try parseOpenComputerUseCLI(arguments: ["screenshot"]), .screenshot(output: nil))
        XCTAssertEqual(
            try parseOpenComputerUseCLI(arguments: ["screenshot", "--output", "/tmp/shot.png"]),
            .screenshot(output: "/tmp/shot.png")
        )
        XCTAssertEqual(
            try parseOpenComputerUseCLI(arguments: ["screenshot", "-o", "shot.png"]),
            .screenshot(output: "shot.png")
        )
        XCTAssertThrowsError(try parseOpenComputerUseCLI(arguments: ["screenshot", "--output"])) { error in
            XCTAssertEqual((error as? OpenComputerUseCLIError)?.message, "--output requires a value")
        }
        XCTAssertThrowsError(try parseOpenComputerUseCLI(arguments: ["screenshot", "--bogus"])) { error in
            XCTAssertEqual((error as? OpenComputerUseCLIError)?.message, "unknown screenshot option: --bogus")
        }
    }

    func testCLIRecognizesCursorPositionCommand() throws {
        XCTAssertEqual(try parseOpenComputerUseCLI(arguments: ["cursor-position"]), .cursorPosition)
        XCTAssertThrowsError(try parseOpenComputerUseCLI(arguments: ["cursor-position", "--display", ":0"]))
    }

    func testCLIRecognizesInputCommands() throws {
        XCTAssertEqual(
            try parseOpenComputerUseCLI(arguments: ["input", "move", "100", "200"]),
            .input(.move(x: 100, y: 200))
        )
        XCTAssertEqual(
            try parseOpenComputerUseCLI(arguments: ["input", "key", "ctrl+s"]),
            .input(.key(specification: "ctrl+s"))
        )
        XCTAssertEqual(
            try parseOpenComputerUseCLI(arguments: ["input", "wait", "1.5"]),
            .input(.wait(seconds: 1.5))
        )
        XCTAssertThrowsError(try parseOpenComputerUseCLI(arguments: ["input"]))
        XCTAssertThrowsError(try parseOpenComputerUseCLI(arguments: ["input", "teleport"]))
    }

    func testCLIRecognizesRecordCommands() throws {
        XCTAssertEqual(
            try parseOpenComputerUseCLI(arguments: ["record", "start", "--output", "/tmp/r.mp4", "--fps", "15", "--quality", "draft", "--draw-mouse", "0"]),
            .record(DesktopRecordRequest(subcommand: .start, output: "/tmp/r.mp4", fps: 15, pidfile: nil, quality: "draft", drawMouse: 0, saveAs: nil))
        )
        XCTAssertEqual(
            try parseOpenComputerUseCLI(arguments: ["record", "stop", "--pidfile", "/tmp/r.pid", "--save-as", "demo"]),
            .record(DesktopRecordRequest(subcommand: .stop, output: nil, fps: 30, pidfile: "/tmp/r.pid", quality: "demo", drawMouse: 1, saveAs: "demo"))
        )
        XCTAssertEqual(
            try parseOpenComputerUseCLI(arguments: ["record", "discard"]),
            .record(DesktopRecordRequest(subcommand: .discard, output: nil, fps: 30, pidfile: nil))
        )
        XCTAssertEqual(
            try parseOpenComputerUseCLI(arguments: ["record", "status"]),
            .record(DesktopRecordRequest(subcommand: .status, output: nil, fps: 30, pidfile: nil))
        )
        XCTAssertEqual(
            try parseOpenComputerUseCLI(arguments: ["record", "start", "--polish", "--output", "/tmp/p.mp4"]),
            .record(DesktopRecordRequest(subcommand: .start, output: "/tmp/p.mp4", fps: 30, pidfile: nil, quality: "demo", drawMouse: 0, saveAs: nil, autoPolish: true))
        )
        XCTAssertEqual(
            try parseOpenComputerUseCLI(arguments: ["record", "start", "--polish", "--draw-mouse", "1"]),
            .record(DesktopRecordRequest(subcommand: .start, output: nil, fps: 30, pidfile: nil, quality: "demo", drawMouse: 1, saveAs: nil, autoPolish: true))
        )
        XCTAssertEqual(
            try parseOpenComputerUseCLI(arguments: [
                "record", "polish", "--input", "/tmp/a.mp4", "--no-ripples", "--idle-rate", "4",
            ]),
            .record(DesktopRecordRequest(
                subcommand: .polish,
                output: nil,
                fps: 30,
                pidfile: nil,
                polishInput: "/tmp/a.mp4",
                showClickRipples: false,
                idleRate: 4
            ))
        )
        XCTAssertThrowsError(try parseOpenComputerUseCLI(arguments: ["record"]))
        XCTAssertThrowsError(try parseOpenComputerUseCLI(arguments: ["record", "pause"]))
        XCTAssertThrowsError(try parseOpenComputerUseCLI(arguments: ["record", "polish"]))
    }

    func testBuildFfmpegAvfoundationArgsDemoQuality() {
        let args = DesktopRecord.buildFfmpegAvfoundationArgs(
            output: "/tmp/out.mp4",
            fps: 60,
            quality: "demo",
            drawMouse: 0,
            screenDevice: "Capture screen 0"
        )
        XCTAssertEqual(args, [
            "-nostdin", "-y",
            "-f", "avfoundation",
            "-framerate", "60",
            "-capture_cursor", "0",
            "-i", "Capture screen 0",
            "-c:v", "libx264",
            "-preset", "veryfast",
            "-crf", "17",
            "-pix_fmt", "yuv420p",
            "-profile:v", "high",
            "-movflags", "+faststart",
            "-tune", "fastdecode",
            "/tmp/out.mp4",
        ])
    }

    func testDesktopHelpTopicsAreWired() {
        for topic in ["screenshot", "cursor-position", "input", "record"] {
            XCTAssertTrue(openComputerUseHelpText(command: topic).contains("Usage:"))
        }
        let recordHelp = openComputerUseHelpText(command: "record")
        for needle in ["polish", "--polish", "events.json"] {
            XCTAssertTrue(recordHelp.contains(needle), "record help missing \(needle):\n\(recordHelp)")
        }
        let top = openComputerUseHelpText(command: nil)
        for command in ["screenshot", "cursor-position", "input", "record"] {
            XCTAssertTrue(top.contains(command), "top-level help missing \(command)")
        }
        XCTAssertTrue(top.contains("polish"), "top-level help missing polish subcommand:\n\(top)")
    }

    func testDisplayCommandsRideTheAppAgentProxy() {
        for command: OpenComputerUseCLICommand in [
            .screenshot(output: nil),
            .cursorPosition,
            .input(.move(x: 1, y: 2)),
            .record(DesktopRecordRequest(subcommand: .status, output: nil, fps: 30, pidfile: nil)),
        ] {
            XCTAssertTrue(
                shouldUseMacOSAppAgentProxy(
                    command: command,
                    proxyDisabled: false,
                    appBundleAvailable: true,
                    runningFromLaunchServicesAppInstance: false
                ),
                "\(command) should proxy through the app agent for TCC permissions"
            )
        }
    }

    // MARK: - Input parsing

    func testParseDesktopMouseButton() throws {
        XCTAssertEqual(try parseDesktopMouseButton("left"), "left")
        XCTAssertEqual(try parseDesktopMouseButton("L"), "left")
        XCTAssertEqual(try parseDesktopMouseButton("1"), "left")
        XCTAssertEqual(try parseDesktopMouseButton("middle"), "middle")
        XCTAssertEqual(try parseDesktopMouseButton("m"), "middle")
        XCTAssertEqual(try parseDesktopMouseButton("right"), "right")
        XCTAssertEqual(try parseDesktopMouseButton("R"), "right")
        XCTAssertThrowsError(try parseDesktopMouseButton("purple"))
        XCTAssertThrowsError(try parseDesktopMouseButton("9"))
    }

    func testDesktopScrollNotches() {
        XCTAssertEqual(desktopScrollNotches("up")?.dy, 1)
        XCTAssertEqual(desktopScrollNotches("down")?.dy, -1)
        XCTAssertEqual(desktopScrollNotches("left")?.dx, -1)
        XCTAssertEqual(desktopScrollNotches("right")?.dx, 1)
        XCTAssertNil(desktopScrollNotches("sideways"))
    }

    func testParseDesktopInputClick() throws {
        XCTAssertEqual(
            try parseDesktopInputArguments(["click", "--button", "right", "--count", "2", "--x", "5", "--y", "6"]),
            .click(button: "right", count: 2, x: 5, y: 6)
        )
        XCTAssertEqual(
            try parseDesktopInputArguments(["click"]),
            .click(button: "left", count: 1, x: nil, y: nil)
        )
        XCTAssertThrowsError(try parseDesktopInputArguments(["click", "--x", "5"])) // --x without --y
    }

    func testParseDesktopInputDragAndScroll() throws {
        XCTAssertEqual(
            try parseDesktopInputArguments(["drag", "1", "2", "3", "4", "--button", "right"]),
            .drag(fromX: 1, fromY: 2, toX: 3, toY: 4, button: "right")
        )
        XCTAssertThrowsError(try parseDesktopInputArguments(["drag", "1", "2", "3"]))

        XCTAssertEqual(
            try parseDesktopInputArguments(["scroll", "down", "--amount", "5"]),
            .scroll(direction: "down", amount: 5)
        )
        XCTAssertEqual(
            try parseDesktopInputArguments(["scroll", "right"]),
            .scroll(direction: "right", amount: 3)
        )
        XCTAssertThrowsError(try parseDesktopInputArguments(["scroll", "sideways"]))
    }

    func testParseDesktopInputTypeAndKey() throws {
        XCTAssertEqual(
            try parseDesktopInputArguments(["type", "hello", "world"]),
            .type(text: "hello world")
        )
        XCTAssertThrowsError(try parseDesktopInputArguments(["type"]))

        XCTAssertEqual(
            try parseDesktopInputArguments(["key", "ctrl+s"]),
            .key(specification: "ctrl+s")
        )
        XCTAssertThrowsError(try parseDesktopInputArguments(["key"]))
    }

    func testParseWaitDuration() throws {
        XCTAssertEqual(try parseDesktopInputArguments(["wait", "1.5"]), .wait(seconds: 1.5))
        XCTAssertThrowsError(try parseDesktopInputArguments(["wait", "-1"]))
        XCTAssertThrowsError(try parseDesktopInputArguments(["wait"]))
    }

    // MARK: - Gate

    func testForegroundInputGateDefaultsOff() {
        XCTAssertFalse(DesktopInputGate.isEnabled(environment: [:]))
        XCTAssertFalse(DesktopInputGate.isEnabled(environment: [DesktopInputGate.environmentKey: "0"]))
        XCTAssertFalse(DesktopInputGate.isEnabled(environment: [DesktopInputGate.environmentKey: "false"]))
        XCTAssertTrue(DesktopInputGate.isEnabled(environment: [DesktopInputGate.environmentKey: "1"]))
        XCTAssertTrue(DesktopInputGate.isEnabled(environment: [DesktopInputGate.environmentKey: "YES"]))
        XCTAssertTrue(DesktopInputGate.isEnabled(environment: [DesktopInputGate.environmentKey: " on "]))
    }

    func testGateRequirementMessageNamesTheFlag() {
        XCTAssertTrue(DesktopInputGate.requirementMessage().contains(DesktopInputGate.environmentKey + "=1"))
    }

    // MARK: - Record defaults

    func testRecordDefaultsUseTemporaryDirectory() {
        let pidfile = DesktopRecord.defaultPidfilePath()
        XCTAssertTrue(pidfile.hasSuffix("open-computer-use-record.pid"), pidfile)

        let mp4 = DesktopRecord.defaultOutputPath(preferMP4: true)
        XCTAssertTrue(mp4.contains("open-computer-use-recording-"), mp4)
        XCTAssertTrue(mp4.hasSuffix(".mp4"), mp4)

        let mov = DesktopRecord.defaultOutputPath(preferMP4: false)
        XCTAssertTrue(mov.hasSuffix(".mov"), mov)
    }

    // MARK: - Record events + polish

    func testBuildRecordEventFromInputActions() {
        let move = buildRecordEvent(from: .move(x: 10, y: 20))
        XCTAssertEqual(move?.type, "move")
        XCTAssertEqual(move?.x, 10)
        XCTAssertEqual(move?.y, 20)

        let click = buildRecordEvent(from: .click(button: "right", count: 2, x: 5, y: 6))
        XCTAssertEqual(click?.button, "right")
        XCTAssertEqual(click?.count, 2)
        XCTAssertEqual(click?.x, 5)
        XCTAssertEqual(click?.y, 6)

        let typed = buildRecordEvent(from: .type(text: "hello world"))
        XCTAssertEqual(typed?.text, "hello world")

        let key = buildRecordEvent(from: .key(specification: "ctrl+s"))
        XCTAssertEqual(key?.key, "ctrl+s")
    }

    func testBuildPolishPlanKeystrokesAndIdle() {
        let log = DesktopRecordEventLog(
            startedAtMs: 0,
            width: 800,
            height: 600,
            events: [
                DesktopRecordEvent(tMs: 500, type: "click", x: 100, y: 120, button: "left", count: 1),
                DesktopRecordEvent(tMs: 800, type: "type", text: "hi"),
                DesktopRecordEvent(tMs: 5000, type: "key", key: "Return"),
            ]
        )
        var opts = DesktopPolishOptions.default()
        opts.showClickRipples = true
        let plan = buildPolishPlan(log: log, durationMs: 7000, opts: opts)
        XCTAssertGreaterThanOrEqual(plan.segments.count, 2, "expected idle speedup segments, got \(plan.segments)")
        XCTAssertTrue(plan.segments.contains(where: { $0.rate > 1.5 }), "expected an idle speedup segment, got \(plan.segments)")
        XCTAssertFalse(plan.zooms.isEmpty, "expected at least one zoom window around the click")
        XCTAssertTrue(plan.ass.contains("Keystroke"), plan.ass)
        XCTAssertTrue(plan.ass.contains("hi"), plan.ass)
        XCTAssertTrue(plan.ass.contains("Ripple"), plan.ass)
        XCTAssertTrue(plan.ass.contains("1a&HFF&"), "ripples must use transparent-fill rings, got:\n\(plan.ass)")
        XCTAssertTrue(plan.ass.contains("\\bord2"), "ripples must stroke outline rings")
        XCTAssertFalse(plan.ass.contains("\\p1\\alpha&H60&"), "should not use filled-square ripples")
        XCTAssertTrue(plan.ass.contains("Cursor"), plan.ass)
    }

    func testFormatASSTimeAndPolishedOutputPath() {
        XCTAssertEqual(formatASSTime(3_661_020), "1:01:01.02")
        XCTAssertEqual(defaultPolishedOutput(forRaw: "/tmp/a.mp4"), "/tmp/a.polished.mp4")
        XCTAssertEqual(recordEventsPath(forOutput: "/tmp/a.mp4"), "/tmp/a.events.json")
    }

    func testKeyDisplayLabelMapsReturn() {
        let label = keyDisplayLabel("ctrl+Return")
        XCTAssertTrue(label.contains("↵ Enter"), label)
    }

    func testBuildPolishFilterComplexSingleSegment() throws {
        let filter = try buildPolishFilterComplex(
            segments: [DesktopPolishSegment(startMs: 0, endMs: 1000, rate: 1)],
            zooms: [],
            assPath: "/tmp/x.ass",
            width: 1920,
            height: 1080
        )
        XCTAssertTrue(filter.contains("ass=filename="), filter)
        XCTAssertTrue(filter.contains("[outv]"), filter)
    }

    func testMergePolishSegments() {
        let got = mergePolishSegments([
            DesktopPolishSegment(startMs: 0, endMs: 100, rate: 1),
            DesktopPolishSegment(startMs: 100, endMs: 200, rate: 1),
            DesktopPolishSegment(startMs: 200, endMs: 500, rate: 3),
        ])
        XCTAssertEqual(got, [
            DesktopPolishSegment(startMs: 0, endMs: 200, rate: 1),
            DesktopPolishSegment(startMs: 200, endMs: 500, rate: 3),
        ])
    }
}
