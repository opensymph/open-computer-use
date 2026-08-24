import AppKit
import ApplicationServices
import CoreGraphics
import Foundation

// Display-level desktop commands mirroring the Linux/Windows runtimes'
// desktop command set (same command names, flags, and output shapes):
// whole-screen capture, pointer query, global CGEvent input, and screen
// recording. The accessibility MCP tools stay per-app and non-intrusive;
// these commands operate on the whole desktop like xdotool/ffmpeg do.

/// The pointer report printed by `cursor-position`, identical in shape to the
/// Linux/Windows runtimes' JSON (pointer coordinates plus the desktop size).
public struct DesktopPointerInfo: Codable, Equatable, Sendable {
    public let x: Int
    public let y: Int
    public let screen_width: Int
    public let screen_height: Int

    public init(x: Int, y: Int, screenWidth: Int, screenHeight: Int) {
        self.x = x
        self.y = y
        self.screen_width = screenWidth
        self.screen_height = screenHeight
    }
}

/// One display-level input action parsed from the `input` subcommand.
/// Parsing is pure so it stays unit-testable without a desktop session.
public enum DesktopInputAction: Equatable, Sendable {
    case move(x: Int, y: Int)
    case click(button: String, count: Int, x: Int?, y: Int?)
    case drag(fromX: Int, fromY: Int, toX: Int, toY: Int, button: String)
    case scroll(direction: String, amount: Int)
    case type(text: String)
    case key(specification: String)
    case wait(seconds: Double)
}

/// The parsed `record` subcommand request.
public struct DesktopRecordRequest: Equatable, Sendable {
    public enum Subcommand: String, Equatable, Sendable {
        case start
        case stop
        case status
    }

    public let subcommand: Subcommand
    public let output: String?
    /// Accepted for cross-platform parity; `screencapture` picks its own rate.
    public let fps: Int
    public let pidfile: String?

    public init(subcommand: Subcommand, output: String?, fps: Int, pidfile: String?) {
        self.subcommand = subcommand
        self.output = output
        self.fps = fps
        self.pidfile = pidfile
    }
}

/// The environment gate for display-level synthetic input. Global CGEvent
/// posting moves the real pointer/keyboard and can change foreground focus,
/// so it stays disabled unless explicitly opted in — the macOS sibling of the
/// Linux `OPEN_COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS` and Windows
/// `OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT` gates.
public enum DesktopInputGate {
    public static let environmentKey = "OPEN_COMPUTER_USE_MACOS_ALLOW_FOREGROUND_INPUT"

    public static func isEnabled(environment: [String: String]) -> Bool {
        let value = environment[environmentKey]?.lowercased()
        return value == "1" || value == "true" || value == "yes" || value == "on"
    }

    public static func requirementMessage() -> String {
        "input actions move the real pointer/keyboard and require \(environmentKey)=1"
    }
}

// MARK: - Parsing

/// Normalizes the shared button spellings (left/middle/right, single-letter
/// aliases, and 1/2/3) to the canonical names the CGEvent layer takes.
public func parseDesktopMouseButton(_ value: String) throws -> String {
    switch value.lowercased().trimmingCharacters(in: .whitespaces) {
    case "", "left", "l", "1":
        return "left"
    case "middle", "m", "2":
        return "middle"
    case "right", "r", "3":
        return "right"
    default:
        throw OpenComputerUseCLIError(message: "invalid mouse button \"\(value)\" (left, right, or middle)")
    }
}

/// Maps a scroll direction to wheel notches: dy positive scrolls up, dx
/// positive scrolls right (the shared convention across runtimes).
public func desktopScrollNotches(_ direction: String) -> (dy: Int, dx: Int)? {
    switch direction.lowercased().trimmingCharacters(in: .whitespaces) {
    case "up":
        return (1, 0)
    case "down":
        return (-1, 0)
    case "left":
        return (0, -1)
    case "right":
        return (0, 1)
    default:
        return nil
    }
}

func isDesktopIntArgument(_ value: String) -> Bool {
    Int(value) != nil
}

/// Parses `input <action> [options]` into a `DesktopInputAction`.
public func parseDesktopInputArguments(_ arguments: [String]) throws -> DesktopInputAction {
    guard let action = arguments.first, !action.isEmpty else {
        throw OpenComputerUseCLIError(message: "input requires an action: move, click, drag, scroll, type, key, or wait")
    }
    let rest = Array(arguments.dropFirst())

    switch action {
    case "move":
        guard rest.count == 2, let x = Int(rest[0]), let y = Int(rest[1]) else {
            throw OpenComputerUseCLIError(message: "move requires integer <x> <y>")
        }
        return .move(x: x, y: y)

    case "click":
        var button = "left"
        var count = 1
        var x: Int?
        var y: Int?
        var index = 0
        while index < rest.count {
            switch rest[index] {
            case "--button", "-b":
                index += 1
                guard index < rest.count else {
                    throw OpenComputerUseCLIError(message: "--button requires a value")
                }
                button = try parseDesktopMouseButton(rest[index])
            case "--count", "-c":
                index += 1
                guard index < rest.count, let parsed = Int(rest[index]), parsed >= 1 else {
                    throw OpenComputerUseCLIError(message: "--count requires a positive integer")
                }
                count = parsed
            case "--x":
                index += 1
                guard index < rest.count, let parsed = Int(rest[index]) else {
                    throw OpenComputerUseCLIError(message: "--x requires an integer")
                }
                x = parsed
            case "--y":
                index += 1
                guard index < rest.count, let parsed = Int(rest[index]) else {
                    throw OpenComputerUseCLIError(message: "--y requires an integer")
                }
                y = parsed
            default:
                throw OpenComputerUseCLIError(message: "unknown click option: \(rest[index])")
            }
            index += 1
        }
        if (x == nil) != (y == nil) {
            throw OpenComputerUseCLIError(message: "click --x and --y must be provided together")
        }
        return .click(button: button, count: count, x: x, y: y)

    case "drag":
        guard rest.count >= 4,
              isDesktopIntArgument(rest[0]), isDesktopIntArgument(rest[1]),
              isDesktopIntArgument(rest[2]), isDesktopIntArgument(rest[3]) else {
            throw OpenComputerUseCLIError(message: "drag requires <from_x> <from_y> <to_x> <to_y>")
        }
        var button = "left"
        if rest.count > 4 {
            let options = Array(rest.dropFirst(4))
            guard options.count == 2, options[0] == "--button" || options[0] == "-b" else {
                throw OpenComputerUseCLIError(message: "unknown drag option: \(options.first ?? "")")
            }
            button = try parseDesktopMouseButton(options[1])
        }
        return .drag(
            fromX: Int(rest[0]) ?? 0,
            fromY: Int(rest[1]) ?? 0,
            toX: Int(rest[2]) ?? 0,
            toY: Int(rest[3]) ?? 0,
            button: button
        )

    case "scroll":
        guard let direction = rest.first, desktopScrollNotches(direction) != nil else {
            throw OpenComputerUseCLIError(message: "scroll requires a direction: up, down, left, or right")
        }
        var amount = 3
        if rest.count > 1 {
            guard rest.count == 3, rest[1] == "--amount" || rest[1] == "-n", let parsed = Int(rest[2]), parsed >= 1 else {
                throw OpenComputerUseCLIError(message: "unknown scroll option: \(rest.count > 1 ? rest[1] : "")")
            }
            amount = parsed
        }
        return .scroll(direction: direction.lowercased(), amount: amount)

    case "type":
        guard !rest.isEmpty else {
            throw OpenComputerUseCLIError(message: "type requires text, e.g. 'input type \"hello\"'")
        }
        return .type(text: rest.joined(separator: " "))

    case "key":
        guard rest.count == 1, !rest[0].trimmingCharacters(in: .whitespaces).isEmpty else {
            throw OpenComputerUseCLIError(message: "key requires a single key or chord, e.g. 'input key ctrl+s'")
        }
        return .key(specification: rest[0])

    case "wait":
        guard rest.count == 1, let seconds = Double(rest[0]), seconds >= 0 else {
            throw OpenComputerUseCLIError(message: "wait requires a duration in seconds, e.g. 'input wait 1.5'")
        }
        return .wait(seconds: seconds)

    default:
        throw OpenComputerUseCLIError(message: "unknown input action: \(action)")
    }
}

/// Parses `record <start|stop|status> [options]`.
public func parseDesktopRecordArguments(_ arguments: [String]) throws -> DesktopRecordRequest {
    guard let subcommandName = arguments.first else {
        throw OpenComputerUseCLIError(message: "record requires a subcommand: start, stop, or status")
    }
    let subcommand: DesktopRecordRequest.Subcommand
    switch subcommandName {
    case "start", "stop", "status":
        subcommand = DesktopRecordRequest.Subcommand(rawValue: subcommandName)!
    default:
        throw OpenComputerUseCLIError(message: "unknown record subcommand: \(subcommandName)")
    }

    var output: String?
    var fps = 30
    var pidfile: String?
    let rest = Array(arguments.dropFirst())
    var index = 0
    while index < rest.count {
        switch rest[index] {
        case "--output", "-o":
            index += 1
            guard index < rest.count else {
                throw OpenComputerUseCLIError(message: "--output requires a value")
            }
            output = rest[index]
        case "--fps":
            index += 1
            guard index < rest.count, let parsed = Int(rest[index]), parsed >= 1 else {
                throw OpenComputerUseCLIError(message: "--fps requires a positive integer (accepted for parity; ignored on macOS)")
            }
            fps = parsed
        case "--pidfile":
            index += 1
            guard index < rest.count else {
                throw OpenComputerUseCLIError(message: "--pidfile requires a value")
            }
            pidfile = rest[index]
        default:
            throw OpenComputerUseCLIError(message: "unknown record option: \(rest[index])")
        }
        index += 1
    }

    return DesktopRecordRequest(subcommand: subcommand, output: output, fps: fps, pidfile: pidfile)
}

// MARK: - Runner

/// Executes the display-level commands and returns their stdout text.
public enum DesktopCommandRunner {
    /// `screenshot [--output path.png]`: composite of every display as PNG,
    /// written to the path or printed as base64.
    public static func runScreenshot(outputPath: String?) throws -> String {
        let pngData = try DesktopScreenshot.capturePNGData()
        if let outputPath {
            let url = URL(fileURLWithPath: outputPath)
            do {
                try pngData.write(to: url)
            } catch {
                throw ComputerUseError.message("cannot write screenshot: \(error.localizedDescription)")
            }
            let width = DesktopScreenshot.pngPixelWidth(pngData) ?? 0
            let height = DesktopScreenshot.pngPixelHeight(pngData) ?? 0
            return "Saved \(width)x\(height) screenshot to \(url.path)"
        }
        return pngData.base64EncodedString()
    }

    /// `cursor-position`: pointer position and desktop size as JSON.
    public static func runCursorPosition() throws -> String {
        let info = DesktopCursor.current()
        // Hand-rolled to match the Linux/Windows output byte-for-byte
        // (declaration order, 2-space indent).
        return """
        {
          "x": \(info.x),
          "y": \(info.y),
          "screen_width": \(info.screen_width),
          "screen_height": \(info.screen_height)
        }
        """
    }

    /// `input <action>`: global synthetic input behind the opt-in gate.
    public static func runInput(_ action: DesktopInputAction) throws -> String {
        try DesktopInput.perform(action)
        switch action {
        case .move, .click, .drag, .scroll, .type, .key:
            return "input \(DesktopInput.actionName(action)) ok"
        case .wait(let seconds):
            return "waited \(seconds)s"
        }
    }

    /// `record <start|stop|status>`.
    public static func runRecord(_ request: DesktopRecordRequest) throws -> String {
        switch request.subcommand {
        case .start:
            return try DesktopRecord.start(outputPath: request.output)
        case .stop:
            return try DesktopRecord.stop(pidfilePath: request.pidfile)
        case .status:
            return try DesktopRecord.status(pidfilePath: request.pidfile)
        }
    }
}

// MARK: - Screenshot

enum DesktopScreenshot {
    static func capturePNGData() throws -> Data {
        guard CGPreflightScreenCaptureAccess() else {
            throw ComputerUseError.message(
                "screenshot requires the Screen Recording permission. Grant it to this app via System Settings > Privacy & Security > Screen & System Audio Recording (or run `open-computer-use doctor`), then restart the process."
            )
        }

        var displayIDs = [CGDirectDisplayID](repeating: 0, count: 16)
        var displayCount: UInt32 = 0
        CGGetActiveDisplayList(16, &displayIDs, &displayCount)
        guard displayCount > 0 else {
            throw ComputerUseError.message("no displays detected")
        }

        let activeIDs = Array(displayIDs.prefix(Int(displayCount)))
        let unionBounds = activeIDs.reduce(CGRect.null) { $0.union(CGDisplayBounds($1)) }
        let width = max(1, Int(unionBounds.width.rounded()))
        let height = max(1, Int(unionBounds.height.rounded()))

        guard
            let context = CGContext(
                data: nil,
                width: width,
                height: height,
                bitsPerComponent: 8,
                bytesPerRow: 0,
                space: CGColorSpaceCreateDeviceRGB(),
                bitmapInfo: CGImageAlphaInfo.premultipliedFirst.rawValue | CGBitmapInfo.byteOrder32Big.rawValue
            )
        else {
            throw ComputerUseError.message("cannot create screenshot context")
        }

        for displayID in activeIDs {
            guard let image = displayImage(for: displayID) else {
                throw ComputerUseError.message("screen capture returned no image for display \(displayID); check the Screen Recording permission")
            }
            let bounds = CGDisplayBounds(displayID)
            // Display bounds are top-left origin; the bitmap context is
            // bottom-left origin. Flipping each display's rect keeps the
            // composite readable top-down like the Linux/Windows captures.
            let rect = CGRect(
                x: bounds.minX - unionBounds.minX,
                y: CGFloat(height) - (bounds.maxY - unionBounds.minY),
                width: bounds.width,
                height: bounds.height
            )
            context.draw(image, in: rect)
        }

        guard let composite = context.makeImage() else {
            throw ComputerUseError.message("cannot compose screenshot")
        }
        guard let pngData = NSBitmapImageRep(cgImage: composite).representation(using: .png, properties: [:]) else {
            throw ComputerUseError.message("cannot encode screenshot as PNG")
        }
        return pngData
    }

    // CGDisplayCreateImage is deprecated in favor of ScreenCaptureKit on
    // newer macOS; the wrapper documents the intent and keeps one call site.
    @available(macOS, deprecated: 15.0)
    private static func displayImage(for displayID: CGDirectDisplayID) -> CGImage? {
        CGDisplayCreateImage(displayID)
    }

    private static func pngPixelSize(_ data: Data) -> (width: Int, height: Int)? {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil),
              let image = CGImageSourceCreateImageAtIndex(source, 0, nil)
        else {
            return nil
        }
        return (image.width, image.height)
    }

    static func pngPixelWidth(_ data: Data) -> Int? {
        pngPixelSize(data)?.width
    }

    static func pngPixelHeight(_ data: Data) -> Int? {
        pngPixelSize(data)?.height
    }
}

// MARK: - Cursor

/// Union of every display's bounds in Quartz global (top-left origin)
/// coordinates — the desktop space the display commands report and accept.
func desktopUnionBounds() -> CGRect {
    var displayIDs = [CGDirectDisplayID](repeating: 0, count: 16)
    var displayCount: UInt32 = 0
    CGGetActiveDisplayList(16, &displayIDs, &displayCount)
    var union = CGRect.null
    for displayID in displayIDs.prefix(Int(displayCount)) {
        union = union.union(CGDisplayBounds(displayID))
    }
    return union
}

enum DesktopCursor {
    /// Pointer position in top-left-origin desktop coordinates (origin at the
    /// top-left of the upper-leftmost display) plus the whole-desktop size.
    static func current() -> DesktopPointerInfo {
        let union = desktopUnionBounds()
        guard !union.isNull else {
            return DesktopPointerInfo(x: 0, y: 0, screenWidth: 0, screenHeight: 0)
        }
        // NSEvent.mouseLocation is AppKit-global (bottom-left origin); CGEvent
        // reports the same pointer in Quartz-global (top-left origin), which
        // is the space display commands use.
        guard let pointer = CGEvent(source: nil)?.location else {
            return DesktopPointerInfo(x: 0, y: 0, screenWidth: Int(union.width.rounded()), screenHeight: Int(union.height.rounded()))
        }
        let x = Int((pointer.x - union.minX).rounded())
        let y = Int((pointer.y - union.minY).rounded())
        return DesktopPointerInfo(
            x: x,
            y: y,
            screenWidth: Int(union.width.rounded()),
            screenHeight: Int(union.height.rounded())
        )
    }
}

// MARK: - Input

enum DesktopInput {
    static func actionName(_ action: DesktopInputAction) -> String {
        switch action {
        case .move:
            return "move"
        case .click:
            return "click"
        case .drag:
            return "drag"
        case .scroll:
            return "scroll"
        case .type:
            return "type"
        case .key:
            return "key"
        case .wait:
            return "wait"
        }
    }

    static func perform(_ action: DesktopInputAction) throws {
        // wait is a local sleep, not a pointer/keyboard action, so it is
        // ungated (same as the Linux/Windows runtimes).
        if case let .wait(seconds) = action {
            Thread.sleep(forTimeInterval: seconds)
            return
        }

        guard DesktopInputGate.isEnabled(environment: ProcessInfo.processInfo.environment) else {
            throw ComputerUseError.message(DesktopInputGate.requirementMessage())
        }
        guard AXIsProcessTrusted() else {
            throw ComputerUseError.message(
                "input requires the Accessibility permission. Grant it to this app via System Settings > Privacy & Security > Accessibility (or run `open-computer-use doctor`), then restart the process."
            )
        }

        switch action {
        case let .move(x, y):
            try postMouseEvent(type: .mouseMoved, point: globalPoint(CGPoint(x: x, y: y)), button: .left, clickState: 1)
        case let .click(button, count, x, y):
            if let x, let y {
                try postMouseEvent(type: .mouseMoved, point: globalPoint(CGPoint(x: x, y: y)), button: .left, clickState: 1)
            }
            try postClick(button: button, count: count)
        case let .drag(fromX, fromY, toX, toY, button):
            try postDrag(from: CGPoint(x: fromX, y: fromY), to: CGPoint(x: toX, y: toY), button: button)
        case let .scroll(direction, amount):
            try postScroll(direction: direction, amount: amount)
        case let .type(text):
            try postTypeText(text)
        case let .key(specification):
            try postKeyChord(specification)
        case .wait:
            break // handled above
        }
    }

    /// Converts top-left-origin desktop coordinates (as printed by
    /// cursor-position and screenshot) into Quartz global coordinates, the
    /// space CGEvent mouse positions use.
    private static func globalPoint(_ point: CGPoint) -> CGPoint {
        let union = desktopUnionBounds()
        guard !union.isNull else {
            return point
        }
        return CGPoint(x: point.x + union.minX, y: point.y + union.minY)
    }

    /// Current pointer position in Quartz global coordinates.
    private static func currentPointerLocation() -> CGPoint {
        CGEvent(source: nil)?.location ?? .zero
    }

    private static func mouseKind(_ button: String) -> MouseButtonKind {
        switch button {
        case "right":
            return .right
        case "middle":
            return .middle
        default:
            return .left
        }
    }

    private static func postMouseEvent(type: CGEventType, point: CGPoint, button: CGMouseButton, clickState: Int) throws {
        guard let event = CGEvent(mouseEventSource: nil, mouseType: type, mouseCursorPosition: point, mouseButton: button) else {
            throw ComputerUseError.message("Failed to create mouse event \(type.rawValue).")
        }
        event.setIntegerValueField(.mouseEventClickState, value: Int64(clickState))
        event.post(tap: .cghidEventTap)
        Thread.sleep(forTimeInterval: 0.03)
    }

    private static func postClick(button: String, count: Int) throws {
        let kind = mouseKind(button)
        for _ in 0..<max(count, 1) {
            let location = currentPointerLocation()
            try postMouseEvent(type: .mouseMoved, point: location, button: kind.cgButton, clickState: count)
            try postMouseEvent(type: kind.downEvent, point: location, button: kind.cgButton, clickState: count)
            try postMouseEvent(type: kind.upEvent, point: location, button: kind.cgButton, clickState: count)
        }
    }

    private static func postDrag(from start: CGPoint, to end: CGPoint, button: String) throws {
        let kind = mouseKind(button)
        let startGlobal = globalPoint(start)
        let endGlobal = globalPoint(end)
        try postMouseEvent(type: .mouseMoved, point: startGlobal, button: kind.cgButton, clickState: 1)
        try postMouseEvent(type: kind.downEvent, point: startGlobal, button: kind.cgButton, clickState: 1)
        for step in 1...10 {
            let progress = CGFloat(step) / 10
            let point = CGPoint(
                x: startGlobal.x + ((endGlobal.x - startGlobal.x) * progress),
                y: startGlobal.y + ((endGlobal.y - startGlobal.y) * progress)
            )
            try postMouseEvent(type: .leftMouseDragged, point: point, button: kind.cgButton, clickState: 1)
        }
        try postMouseEvent(type: kind.upEvent, point: endGlobal, button: kind.cgButton, clickState: 1)
    }

    private static func postScroll(direction: String, amount: Int) throws {
        // scrollGlobally pages are 12 lines; a wheel notch is ~3 lines, so
        // amount N (notches) maps to N/4 pages.
        let pages = Double(max(amount, 1)) / 4
        guard let event = CGEvent(scrollWheelEvent2Source: nil, units: .line, wheelCount: 2, wheel1: wheel1(direction: direction, pages: pages), wheel2: wheel2(direction: direction, pages: pages), wheel3: 0) else {
            throw ComputerUseError.message("Failed to create scroll event.")
        }
        event.location = currentPointerLocation()
        event.post(tap: .cghidEventTap)
        Thread.sleep(forTimeInterval: 0.1)
    }

    private static func wheel1(direction: String, pages: Double) -> Int32 {
        switch direction {
        case "up":
            return scrollWheelDelta(for: pages)
        case "down":
            return -scrollWheelDelta(for: pages)
        default:
            return 0
        }
    }

    private static func wheel2(direction: String, pages: Double) -> Int32 {
        switch direction {
        case "left":
            return scrollWheelDelta(for: pages)
        case "right":
            return -scrollWheelDelta(for: pages)
        default:
            return 0
        }
    }

    private static func scrollWheelDelta(for pages: Double) -> Int32 {
        let rawValue = (12.0 * pages).rounded(.toNearestOrAwayFromZero)
        let clamped = min(Double(Int32.max), max(1, rawValue))
        return Int32(clamped)
    }

    private static func postTypeText(_ text: String) throws {
        for chunk in InputSimulation.keyboardUnicodeChunks(for: text) {
            var mutableChunk = chunk
            guard let down = CGEvent(keyboardEventSource: nil, virtualKey: 0, keyDown: true),
                  let up = CGEvent(keyboardEventSource: nil, virtualKey: 0, keyDown: false) else {
                throw ComputerUseError.message("Failed to create keyboard event.")
            }
            mutableChunk.withUnsafeMutableBufferPointer { buffer in
                guard let baseAddress = buffer.baseAddress else {
                    return
                }
                down.keyboardSetUnicodeString(stringLength: buffer.count, unicodeString: baseAddress)
                up.keyboardSetUnicodeString(stringLength: buffer.count, unicodeString: baseAddress)
            }
            down.post(tap: .cghidEventTap)
            up.post(tap: .cghidEventTap)
            Thread.sleep(forTimeInterval: 0.02)
        }
    }

    private static func postKeyChord(_ specification: String) throws {
        let parsed = try KeyPressParser.parse(specification)
        var activeFlags: CGEventFlags = []
        for modifier in parsed.modifiers {
            guard let event = CGEvent(keyboardEventSource: nil, virtualKey: modifier.keyCode, keyDown: true) else {
                throw ComputerUseError.message("Failed to create modifier key down event.")
            }
            activeFlags.insert(modifier.flag)
            event.flags = activeFlags
            event.post(tap: .cghidEventTap)
        }

        guard let keyDown = CGEvent(keyboardEventSource: nil, virtualKey: parsed.keyCode, keyDown: true),
              let keyUp = CGEvent(keyboardEventSource: nil, virtualKey: parsed.keyCode, keyDown: false) else {
            throw ComputerUseError.message("Failed to create key event.")
        }
        keyDown.flags = activeFlags
        keyUp.flags = activeFlags
        keyDown.post(tap: .cghidEventTap)
        keyUp.post(tap: .cghidEventTap)

        for modifier in parsed.modifiers.reversed() {
            guard let event = CGEvent(keyboardEventSource: nil, virtualKey: modifier.keyCode, keyDown: false) else {
                throw ComputerUseError.message("Failed to create modifier key up event.")
            }
            event.flags = activeFlags
            event.post(tap: .cghidEventTap)
            activeFlags.remove(modifier.flag)
        }
        Thread.sleep(forTimeInterval: 0.1)
    }
}

// MARK: - Record

enum DesktopRecord {
    struct RecordState: Codable {
        let pid: Int32
        let output: String
    }

    static func defaultPidfilePath() -> String {
        FileManager.default.temporaryDirectory
            .appendingPathComponent("open-computer-use-record.pid")
            .standardizedFileURL
            .path
    }

    static func defaultOutputPath() -> String {
        let formatter = DateFormatter()
        formatter.dateFormat = "yyyyMMdd-HHmmss"
        formatter.locale = Locale(identifier: "en_US_POSIX")
        let name = "open-computer-use-recording-\(formatter.string(from: Date())).mov"
        return FileManager.default.temporaryDirectory
            .appendingPathComponent(name)
            .standardizedFileURL
            .path
    }

    @discardableResult
    static func start(outputPath: String?) throws -> String {
        let pidfilePath = defaultPidfilePath()
        if let state = readState(pidfilePath: pidfilePath), processAlive(state.pid) {
            throw ComputerUseError.message("recording already running (pid \(state.pid), output \(state.output)); run 'record stop' first")
        }

        let screenCaptureURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
        guard FileManager.default.isExecutableFile(atPath: screenCaptureURL.path) else {
            throw ComputerUseError.message("screencapture is required for screen recording but was not found at /usr/sbin/screencapture")
        }

        let output = outputPath ?? defaultOutputPath()
        let process = Process()
        process.executableURL = screenCaptureURL
        process.arguments = ["-v", output]
        let logURL = URL(fileURLWithPath: output + ".log")
        guard FileManager.default.createFile(atPath: logURL.path, contents: nil),
              let logHandle = try? FileHandle(forWritingTo: logURL) else {
            throw ComputerUseError.message("cannot create recording log at \(logURL.path)")
        }
        defer { try? logHandle.close() }
        process.standardOutput = logHandle
        process.standardError = logHandle
        do {
            try process.run()
        } catch {
            throw ComputerUseError.message("cannot start screencapture: \(error.localizedDescription)")
        }
        let pid = process.processIdentifier
        // Deliberately not waited: the recorder keeps running after this CLI
        // exits, exactly like the Linux/Windows detached ffmpeg recorders.
        let state = RecordState(pid: pid, output: output)
        do {
            try writeState(state, pidfilePath: pidfilePath)
        } catch {
            throw ComputerUseError.message("recording started (pid \(pid)) but pidfile write failed: \(error.localizedDescription)")
        }
        return "recording started: pid=\(pid) output=\(output)"
    }

    static func stop(pidfilePath: String?) throws -> String {
        let path = pidfilePath ?? defaultPidfilePath()
        guard let state = readState(pidfilePath: path) else {
            throw ComputerUseError.message("no recording in progress (pidfile not found)")
        }
        var stopError: Error?
        if processAlive(state.pid) {
            if kill(state.pid, SIGINT) != 0 {
                if errno != ESRCH {
                    stopError = ComputerUseError.message("cannot signal screencapture pid \(state.pid): \(String(cString: strerror(errno)))")
                }
            } else {
                let deadline = Date().addingTimeInterval(10)
                while Date() < deadline {
                    if !processAlive(state.pid) {
                        break
                    }
                    Thread.sleep(forTimeInterval: 0.1)
                }
                if processAlive(state.pid) {
                    stopError = ComputerUseError.message("screencapture pid \(state.pid) did not exit after stop signal")
                }
            }
        }
        try? FileManager.default.removeItem(atPath: path)
        if let stopError {
            throw ComputerUseError.message("recording output=\(state.output) but stop had a problem: \(stopError.localizedDescription)")
        }
        return "recording stopped: output=\(state.output)"
    }

    static func status(pidfilePath: String?) throws -> String {
        let path = pidfilePath ?? defaultPidfilePath()
        guard let state = readState(pidfilePath: path) else {
            return "{\n  \"running\": false\n}"
        }
        let report: [String: Any] = [
            "running": processAlive(state.pid),
            "pid": state.pid,
            "output": state.output,
        ]
        let data = try JSONSerialization.data(withJSONObject: report, options: [.prettyPrinted, .sortedKeys])
        guard let json = String(data: data, encoding: .utf8) else {
            throw ComputerUseError.message("cannot encode recording status")
        }
        return json
    }

    private static func writeState(_ state: RecordState, pidfilePath: String) throws {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        let data = try encoder.encode(state)
        try data.write(to: URL(fileURLWithPath: pidfilePath))
    }

    private static func readState(pidfilePath: String) -> RecordState? {
        guard let data = FileManager.default.contents(atPath: pidfilePath),
              let state = try? JSONDecoder().decode(RecordState.self, from: data),
              state.pid > 0
        else {
            return nil
        }
        return state
    }

    private static func processAlive(_ pid: Int32) -> Bool {
        guard pid > 0 else {
            return false
        }
        if kill(pid, 0) == 0 {
            return true
        }
        return errno == EPERM
    }
}
