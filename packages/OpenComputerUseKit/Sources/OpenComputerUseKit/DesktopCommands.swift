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
    case click(button: String, count: Int, x: Int?, y: Int?, modifiers: [String])
    case mouseDown(button: String, x: Int?, y: Int?, modifiers: [String])
    case mouseUp(button: String, x: Int?, y: Int?, modifiers: [String])
    case drag(fromX: Int, fromY: Int, toX: Int, toY: Int, button: String)
    case scroll(direction: String, amount: Int, x: Int?, y: Int?, modifiers: [String])
    case type(text: String)
    case key(specification: String, holdMs: Int)
    case wait(seconds: Double)
}

/// Parsed `input` command: action plus optional `--api-size` (env fallback at perform time).
public struct DesktopInputCommand: Equatable, Sendable {
    public let action: DesktopInputAction
    public let apiSize: String?

    public init(action: DesktopInputAction, apiSize: String? = nil) {
        self.action = action
        self.apiSize = apiSize
    }
}

/// The parsed `record` subcommand request.
public struct DesktopRecordRequest: Equatable, Sendable {
    public enum Subcommand: String, Equatable, Sendable {
        case start
        case stop
        case discard
        case status
        case polish
        case proxy
    }

    public let subcommand: Subcommand
    public let output: String?
    /// Honored when ffmpeg is available (avfoundation). Ignored by the
    /// `/usr/sbin/screencapture -v` fallback.
    public let fps: Int
    public let pidfile: String?
    /// `demo` (default) matches Cursor RecordScreen encode settings when
    /// ffmpeg is used; `draft` keeps ultrafast. Ignored by screencapture.
    public let quality: String
    /// Whether to burn the OS cursor into the capture (ffmpeg only).
    public let drawMouse: Int
    /// Optional rename target for `record stop --save-as`.
    public let saveAs: String?
    /// When true, start remembers polish-on-stop; stop with `--polish` also
    /// triggers the post-process pipeline. Auto-defaults `drawMouse` to 0
    /// unless `--draw-mouse` was set explicitly.
    public let autoPolish: Bool
    /// Raw video for `record polish --input`.
    public let polishInput: String?
    /// Optional events sidecar override for `record polish --events`.
    public let polishEvents: String?
    /// Optional polished output path for `record polish --output`.
    public let polishOutput: String?
    public let showClickRipples: Bool
    public let showKeystrokes: Bool
    public let showCursorGhost: Bool
    public let idleSpeedup: Bool
    public let smartZoom: Bool
    public let idleRate: Double
    public let cursorStyle: String
    /// `compositor` | `ffmpeg`. macOS currently uses the ffmpeg filter path for both
    /// (full CPU compositor ships on Linux/Windows); the flag is accepted for CLI parity.
    public let polishEngine: String
    /// Existing render-plan JSON for `record polish --plan`.
    public let polishPlan: String?
    /// When false, skip writing `<stem>.render-plan.json` (`--no-write-plan`).
    public let writePlan: Bool
    /// Override path for plan export (`--write-plan`).
    public let writePlanPath: String?
    /// Raw video for `record proxy --input`.
    public let proxyInput: String?
    /// Output directory for proxy artifacts (`--output-dir`).
    public let proxyOutputDir: String?
    public let proxyWant1080p: Bool
    public let proxyWantFull: Bool

    public init(
        subcommand: Subcommand,
        output: String?,
        fps: Int,
        pidfile: String?,
        quality: String = "demo",
        drawMouse: Int = 1,
        saveAs: String? = nil,
        autoPolish: Bool = false,
        polishInput: String? = nil,
        polishEvents: String? = nil,
        polishOutput: String? = nil,
        showClickRipples: Bool = false,
        showKeystrokes: Bool = true,
        showCursorGhost: Bool = true,
        idleSpeedup: Bool = true,
        smartZoom: Bool = true,
        idleRate: Double = 3.0,
        cursorStyle: String = "mellow",
        polishEngine: String = "ffmpeg",
        polishPlan: String? = nil,
        writePlan: Bool = true,
        writePlanPath: String? = nil,
        proxyInput: String? = nil,
        proxyOutputDir: String? = nil,
        proxyWant1080p: Bool = true,
        proxyWantFull: Bool = true
    ) {
        self.subcommand = subcommand
        self.output = output
        self.fps = fps
        self.pidfile = pidfile
        self.quality = quality
        self.drawMouse = drawMouse
        self.saveAs = saveAs
        self.autoPolish = autoPolish
        self.polishInput = polishInput
        self.polishEvents = polishEvents
        self.polishOutput = polishOutput
        self.showClickRipples = showClickRipples
        self.showKeystrokes = showKeystrokes
        self.showCursorGhost = showCursorGhost
        self.idleSpeedup = idleSpeedup
        self.smartZoom = smartZoom
        self.idleRate = idleRate
        self.cursorStyle = cursorStyle
        self.polishEngine = polishEngine
        self.polishPlan = polishPlan
        self.writePlan = writePlan
        self.writePlanPath = writePlanPath
        self.proxyInput = proxyInput
        self.proxyOutputDir = proxyOutputDir
        self.proxyWant1080p = proxyWant1080p
        self.proxyWantFull = proxyWantFull
    }

    public var polishOptions: DesktopPolishOptions {
        DesktopPolishOptions(
            showClickRipples: showClickRipples,
            showKeystrokes: showKeystrokes,
            showCursorGhost: showCursorGhost,
            idleSpeedup: idleSpeedup,
            smartZoom: smartZoom,
            idleRate: idleRate,
            cursorStyle: cursorStyle
        )
    }
}

/// Options for the ffmpeg+ASS polish pipeline (Cursor RecordScreen-style).
public struct DesktopPolishOptions: Equatable, Sendable {
    public var showClickRipples: Bool
    public var showKeystrokes: Bool
    public var showCursorGhost: Bool
    public var idleSpeedup: Bool
    public var smartZoom: Bool
    public var minIdleMs: Int64
    public var idleRate: Double
    public var zoomFactor: Double
    public var zoomDurationMs: Int64
    public var maxZooms: Int
    /// Matches recording-renderer zoomImportanceThreshold (default 60).
    public var zoomImportance: Int
    /// Matches recording-renderer minZoomIntervalMs (default 1500).
    public var minZoomIntervalMs: Int64
    /// slow|mellow|quick|rapid — spring / cubic-bezier cursor motion style.
    public var cursorStyle: String

    public init(
        showClickRipples: Bool = false,
        showKeystrokes: Bool = true,
        showCursorGhost: Bool = true,
        idleSpeedup: Bool = true,
        smartZoom: Bool = true,
        minIdleMs: Int64 = 500,
        idleRate: Double = 3.0,
        zoomFactor: Double = 1.5,
        zoomDurationMs: Int64 = 2000,
        maxZooms: Int = 8,
        zoomImportance: Int = 60,
        minZoomIntervalMs: Int64 = 1500,
        cursorStyle: String = "mellow"
    ) {
        self.showClickRipples = showClickRipples
        self.showKeystrokes = showKeystrokes
        self.showCursorGhost = showCursorGhost
        self.idleSpeedup = idleSpeedup
        self.smartZoom = smartZoom
        self.minIdleMs = minIdleMs
        self.idleRate = idleRate
        self.zoomFactor = zoomFactor
        self.zoomDurationMs = zoomDurationMs
        self.maxZooms = maxZooms
        self.zoomImportance = zoomImportance
        self.minZoomIntervalMs = minZoomIntervalMs
        self.cursorStyle = cursorStyle
    }

    public static func `default`() -> DesktopPolishOptions {
        DesktopPolishOptions()
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

private let desktopAPISizeEnvironmentKey = "OPEN_COMPUTER_USE_API_SIZE"
private let desktopAPIWidthEnvironmentKey = "OPEN_COMPUTER_USE_API_WIDTH"
private let desktopAPIHeightEnvironmentKey = "OPEN_COMPUTER_USE_API_HEIGHT"
private let defaultTypingDelayMs = 12
private let defaultTypingBatchSize = 50

/// Coordinate scaler maps model/API-space coordinates to display pixels.
public struct DesktopCoordScaler: Equatable, Sendable {
    public let apiWidth: Int
    public let apiHeight: Int
    public let displayWidth: Int
    public let displayHeight: Int

    public var isActive: Bool {
        apiWidth > 0 && apiHeight > 0 && displayWidth > 0 && displayHeight > 0 &&
            (apiWidth != displayWidth || apiHeight != displayHeight)
    }

    public func scaleX(_ x: Int) -> Int {
        guard isActive, apiWidth > 0 else { return x }
        return Int((Double(x) * Double(displayWidth) / Double(apiWidth)).rounded())
    }

    public func scaleY(_ y: Int) -> Int {
        guard isActive, apiHeight > 0 else { return y }
        return Int((Double(y) * Double(displayHeight) / Double(apiHeight)).rounded())
    }

    public func scaleXY(x: Int, y: Int) -> (x: Int, y: Int) {
        (scaleX(x), scaleY(y))
    }
}

public func parseDesktopAPISize(_ value: String) throws -> (width: Int, height: Int) {
    let trimmed = value.lowercased().trimmingCharacters(in: .whitespaces)
    let parts = trimmed.split(separator: "x", omittingEmptySubsequences: false).map(String.init)
    guard parts.count == 2,
          let width = Int(parts[0]), width > 0,
          let height = Int(parts[1]), height > 0
    else {
        throw OpenComputerUseCLIError(message: "invalid api size \"\(value)\" (want WxH, e.g. 1280x800)")
    }
    return (width, height)
}

public func resolveDesktopAPISizeFromEnvironment(_ environment: [String: String]) throws -> (width: Int, height: Int)? {
    if let combined = environment[desktopAPISizeEnvironmentKey]?.trimmingCharacters(in: .whitespaces), !combined.isEmpty {
        let parsed = try parseDesktopAPISize(combined)
        return (parsed.width, parsed.height)
    }
    let widthText = environment[desktopAPIWidthEnvironmentKey]?.trimmingCharacters(in: .whitespaces) ?? ""
    let heightText = environment[desktopAPIHeightEnvironmentKey]?.trimmingCharacters(in: .whitespaces) ?? ""
    if widthText.isEmpty && heightText.isEmpty {
        return nil
    }
    guard !widthText.isEmpty, !heightText.isEmpty else {
        throw OpenComputerUseCLIError(
            message: "\(desktopAPIWidthEnvironmentKey) and \(desktopAPIHeightEnvironmentKey) must both be set"
        )
    }
    guard let width = Int(widthText), width > 0 else {
        throw OpenComputerUseCLIError(message: "invalid \(desktopAPIWidthEnvironmentKey)")
    }
    guard let height = Int(heightText), height > 0 else {
        throw OpenComputerUseCLIError(message: "invalid \(desktopAPIHeightEnvironmentKey)")
    }
    return (width, height)
}

public func desktopDisplaySizeForScaling() -> (width: Int, height: Int) {
    if let main = NSScreen.main {
        let frame = main.frame
        return (max(1, Int(frame.width.rounded())), max(1, Int(frame.height.rounded())))
    }
    let union = desktopUnionBounds()
    return (
        max(1, Int(union.width.rounded())),
        max(1, Int(union.height.rounded()))
    )
}

public func resolveDesktopInputScaler(apiSizeFlag: String?, environment: [String: String]) throws -> DesktopCoordScaler? {
    let apiSize: (width: Int, height: Int)?
    if let apiSizeFlag, !apiSizeFlag.isEmpty {
        apiSize = try parseDesktopAPISize(apiSizeFlag)
    } else {
        apiSize = try resolveDesktopAPISizeFromEnvironment(environment)
    }
    guard let apiSize else {
        return nil
    }
    let display = desktopDisplaySizeForScaling()
    return DesktopCoordScaler(
        apiWidth: apiSize.width,
        apiHeight: apiSize.height,
        displayWidth: display.width,
        displayHeight: display.height
    )
}

public func scaleDesktopInputAction(_ action: DesktopInputAction, scaler: DesktopCoordScaler?) -> DesktopInputAction {
    guard let scaler, scaler.isActive else {
        return action
    }
    switch action {
    case let .move(x, y):
        let scaled = scaler.scaleXY(x: x, y: y)
        return .move(x: scaled.x, y: scaled.y)
    case let .click(button, count, x, y, modifiers):
        if let x, let y {
            let scaled = scaler.scaleXY(x: x, y: y)
            return .click(button: button, count: count, x: scaled.x, y: scaled.y, modifiers: modifiers)
        }
        return action
    case let .mouseDown(button, x, y, modifiers):
        if let x, let y {
            let scaled = scaler.scaleXY(x: x, y: y)
            return .mouseDown(button: button, x: scaled.x, y: scaled.y, modifiers: modifiers)
        }
        return action
    case let .mouseUp(button, x, y, modifiers):
        if let x, let y {
            let scaled = scaler.scaleXY(x: x, y: y)
            return .mouseUp(button: button, x: scaled.x, y: scaled.y, modifiers: modifiers)
        }
        return action
    case let .drag(fromX, fromY, toX, toY, button):
        let from = scaler.scaleXY(x: fromX, y: fromY)
        let to = scaler.scaleXY(x: toX, y: toY)
        return .drag(fromX: from.x, fromY: from.y, toX: to.x, toY: to.y, button: button)
    case let .scroll(direction, amount, x, y, modifiers):
        if let x, let y {
            let scaled = scaler.scaleXY(x: x, y: y)
            return .scroll(direction: direction, amount: amount, x: scaled.x, y: scaled.y, modifiers: modifiers)
        }
        return action
    case .type, .key, .wait:
        return action
    }
}

public func extractDesktopAPISizeFlag(_ arguments: [String]) throws -> (apiSize: String?, rest: [String]) {
    var apiSize: String?
    var rest: [String] = []
    var index = 0
    while index < arguments.count {
        if arguments[index] == "--api-size" {
            index += 1
            guard index < arguments.count else {
                throw OpenComputerUseCLIError(message: "--api-size requires a value (e.g. 1280x800)")
            }
            apiSize = arguments[index]
        } else {
            rest.append(arguments[index])
        }
        index += 1
    }
    return (apiSize, rest)
}

public func parseDesktopInputCommand(_ arguments: [String]) throws -> DesktopInputCommand {
    let (apiSize, rest) = try extractDesktopAPISizeFlag(arguments)
    return DesktopInputCommand(action: try parseDesktopInputArguments(rest), apiSize: apiSize)
}

public func splitDesktopModifiers(_ value: String) -> [String] {
    value.split(separator: "+", omittingEmptySubsequences: false)
        .map { normalizeDesktopModifierName(String($0)) }
        .filter { !$0.isEmpty }
}

public func normalizeDesktopModifierName(_ name: String) -> String {
    switch name.lowercased().trimmingCharacters(in: .whitespaces) {
    case "ctrl", "control", "control_l", "control_r":
        return "ctrl"
    case "alt", "alt_l", "alt_r", "mod1", "option":
        return "alt"
    case "shift", "shift_l", "shift_r":
        return "shift"
    case "super", "meta", "win", "windows", "cmd", "command":
        return "cmd"
    default:
        return name.lowercased()
    }
}

func parseDesktopHoldMsFlag(_ rest: [String]) throws -> (holdMs: Int, remaining: [String]) {
    var holdMs = 0
    var remaining: [String] = []
    var index = 0
    while index < rest.count {
        if rest[index] == "--hold-ms" || rest[index] == "--hold" {
            index += 1
            guard index < rest.count, let parsed = Int(rest[index]), parsed >= 0 else {
                throw OpenComputerUseCLIError(message: "--hold-ms requires an integer millisecond value >= 0")
            }
            holdMs = parsed
        } else {
            remaining.append(rest[index])
        }
        index += 1
    }
    return (holdMs, remaining)
}

func parseDesktopModifiersFlag(_ rest: [String]) throws -> (modifiers: [String], remaining: [String]) {
    var modifiers: [String] = []
    var remaining: [String] = []
    var index = 0
    while index < rest.count {
        if rest[index] == "--modifiers" || rest[index] == "--mods" {
            index += 1
            guard index < rest.count else {
                throw OpenComputerUseCLIError(message: "--modifiers requires a value (e.g. ctrl+shift)")
            }
            modifiers = splitDesktopModifiers(rest[index])
        } else {
            remaining.append(rest[index])
        }
        index += 1
    }
    return (modifiers, remaining)
}

func parseDesktopOptionalXY(_ rest: [String]) throws -> (x: Int?, y: Int?, remaining: [String]) {
    var x: Int?
    var y: Int?
    var remaining: [String] = []
    var index = 0
    while index < rest.count {
        switch rest[index] {
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
            remaining.append(rest[index])
        }
        index += 1
    }
    if (x == nil) != (y == nil) {
        throw OpenComputerUseCLIError(message: "--x and --y must be provided together")
    }
    return (x, y, remaining)
}

func parseDesktopClickParams(_ rest: [String]) throws -> (button: String, count: Int, x: Int?, y: Int?) {
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
    return (button, count, x, y)
}

func splitDesktopTypeSegments(_ text: String) -> [String] {
    text
        .replacingOccurrences(of: "\r\n", with: "\n")
        .replacingOccurrences(of: "\r", with: "\n")
        .split(separator: "\n", omittingEmptySubsequences: false)
        .map(String.init)
}

func chunkDesktopString(_ value: String, size: Int) -> [String] {
    let batchSize = size > 0 ? size : defaultTypingBatchSize
    guard !value.isEmpty else { return [""] }
    var chunks: [String] = []
    var current = ""
    var runeCount = 0
    for character in value {
        if runeCount >= batchSize, !current.isEmpty {
            chunks.append(current)
            current = ""
            runeCount = 0
        }
        current.append(character)
        runeCount += 1
    }
    if !current.isEmpty {
        chunks.append(current)
    }
    return chunks
}

/// Parses `input <action> [options]` into a `DesktopInputAction`.
public func parseDesktopInputArguments(_ arguments: [String]) throws -> DesktopInputAction {
    guard let action = arguments.first, !action.isEmpty else {
        throw OpenComputerUseCLIError(message: "input requires an action: move, click, drag, scroll, type, key, mouse_down, mouse_up, or wait")
    }
    let rest = Array(arguments.dropFirst())

    switch action {
    case "move":
        guard rest.count == 2, let x = Int(rest[0]), let y = Int(rest[1]) else {
            throw OpenComputerUseCLIError(message: "move requires integer <x> <y>")
        }
        return .move(x: x, y: y)

    case "click":
        let (modifiers, rest2) = try parseDesktopModifiersFlag(rest)
        let (button, count, x, y) = try parseDesktopClickParams(rest2)
        return .click(button: button, count: count, x: x, y: y, modifiers: modifiers)

    case "mouse_down", "mousedown":
        let (modifiers, rest2) = try parseDesktopModifiersFlag(rest)
        let (button, _, x, y) = try parseDesktopClickParams(rest2)
        return .mouseDown(button: button, x: x, y: y, modifiers: modifiers)

    case "mouse_up", "mouseup":
        let (modifiers, rest2) = try parseDesktopModifiersFlag(rest)
        let (button, _, x, y) = try parseDesktopClickParams(rest2)
        return .mouseUp(button: button, x: x, y: y, modifiers: modifiers)

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
        let (modifiers, rest2) = try parseDesktopModifiersFlag(rest)
        guard let direction = rest2.first, desktopScrollNotches(direction) != nil else {
            throw OpenComputerUseCLIError(message: "scroll requires a direction: up, down, left, or right")
        }
        let (x, y, rest3) = try parseDesktopOptionalXY(Array(rest2.dropFirst()))
        var amount = 3
        if !rest3.isEmpty {
            guard rest3.count == 2, rest3[0] == "--amount" || rest3[0] == "-n", let parsed = Int(rest3[1]), parsed >= 1 else {
                throw OpenComputerUseCLIError(message: "unknown scroll option: \(rest3.first ?? "")")
            }
            amount = parsed
        }
        return .scroll(direction: direction.lowercased(), amount: amount, x: x, y: y, modifiers: modifiers)

    case "type":
        guard !rest.isEmpty else {
            throw OpenComputerUseCLIError(message: "type requires text, e.g. 'input type \"hello\"'")
        }
        return .type(text: rest.joined(separator: " "))

    case "key":
        let (holdMs, rest2) = try parseDesktopHoldMsFlag(rest)
        guard rest2.count == 1, !rest2[0].trimmingCharacters(in: .whitespaces).isEmpty else {
            throw OpenComputerUseCLIError(message: "key requires a single key or chord, e.g. 'input key ctrl+s'")
        }
        return .key(specification: rest2[0], holdMs: holdMs)

    case "wait":
        guard rest.count == 1, let seconds = Double(rest[0]), seconds >= 0 else {
            throw OpenComputerUseCLIError(message: "wait requires a duration in seconds, e.g. 'input wait 1.5'")
        }
        return .wait(seconds: seconds)

    default:
        throw OpenComputerUseCLIError(message: "unknown input action: \(action)")
    }
}

/// Parses `record <start|stop|discard|polish|proxy|status> [options]`.
public func parseDesktopRecordArguments(_ arguments: [String]) throws -> DesktopRecordRequest {
    guard let subcommandName = arguments.first else {
        throw OpenComputerUseCLIError(message: "record requires a subcommand: start, stop, discard, polish, proxy, or status")
    }
    let subcommand: DesktopRecordRequest.Subcommand
    switch subcommandName {
    case "start", "stop", "discard", "status", "polish", "proxy":
        subcommand = DesktopRecordRequest.Subcommand(rawValue: subcommandName)!
    default:
        throw OpenComputerUseCLIError(message: "unknown record subcommand: \(subcommandName)")
    }

    let rest = Array(arguments.dropFirst())
    if subcommand == .polish {
        return try parseDesktopRecordPolishArguments(rest)
    }
    if subcommand == .proxy {
        return try parseDesktopRecordProxyArguments(rest)
    }

    var output: String?
    var fps = 30
    var pidfile: String?
    var quality = "demo"
    var drawMouse = 1
    var drawMouseSet = false
    var saveAs: String?
    var autoPolish = false
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
                throw OpenComputerUseCLIError(message: "--fps requires a positive integer")
            }
            fps = parsed
        case "--pidfile":
            index += 1
            guard index < rest.count else {
                throw OpenComputerUseCLIError(message: "--pidfile requires a value")
            }
            pidfile = rest[index]
        case "--quality":
            index += 1
            guard index < rest.count else {
                throw OpenComputerUseCLIError(message: "--quality requires a value (demo, draft, proxy, or anyos)")
            }
            switch rest[index].lowercased() {
            case "demo", "high":
                quality = "demo"
            case "draft", "low":
                quality = "draft"
            case "proxy":
                quality = "proxy"
            case "anyos", "120":
                quality = "anyos"
            default:
                throw OpenComputerUseCLIError(message: "invalid --quality \"\(rest[index])\" (demo, draft, proxy, or anyos)")
            }
        case "--draw-mouse":
            index += 1
            guard index < rest.count, rest[index] == "0" || rest[index] == "1", let parsed = Int(rest[index]) else {
                throw OpenComputerUseCLIError(message: "--draw-mouse requires 0 or 1")
            }
            drawMouse = parsed
            drawMouseSet = true
        case "--save-as":
            index += 1
            guard index < rest.count else {
                throw OpenComputerUseCLIError(message: "--save-as requires a value")
            }
            saveAs = rest[index]
        case "--polish":
            autoPolish = true
        default:
            throw OpenComputerUseCLIError(message: "unknown record option: \(rest[index])")
        }
        index += 1
    }

    if autoPolish && !drawMouseSet {
        drawMouse = 0
    }

    return DesktopRecordRequest(
        subcommand: subcommand,
        output: output,
        fps: fps,
        pidfile: pidfile,
        quality: quality,
        drawMouse: drawMouse,
        saveAs: saveAs,
        autoPolish: autoPolish
    )
}

func parseDesktopRecordPolishArguments(_ rest: [String]) throws -> DesktopRecordRequest {
    var polishInput: String?
    var polishEvents: String?
    var polishOutput: String?
    var polishPlan: String?
    var writePlan = true
    var writePlanPath: String?
    var showClickRipples = false
    var showKeystrokes = true
    var showCursorGhost = true
    var idleSpeedup = true
    var smartZoom = true
    var idleRate = 3.0
    var cursorStyle = "mellow"
    var polishEngine = "ffmpeg"
    var index = 0
    while index < rest.count {
        switch rest[index] {
        case "--input", "-i":
            index += 1
            guard index < rest.count else {
                throw OpenComputerUseCLIError(message: "--input requires a value")
            }
            polishInput = rest[index]
        case "--events":
            index += 1
            guard index < rest.count else {
                throw OpenComputerUseCLIError(message: "--events requires a value")
            }
            polishEvents = rest[index]
        case "--output", "-o":
            index += 1
            guard index < rest.count else {
                throw OpenComputerUseCLIError(message: "--output requires a value")
            }
            polishOutput = rest[index]
        case "--plan":
            index += 1
            guard index < rest.count else {
                throw OpenComputerUseCLIError(message: "--plan requires a value")
            }
            polishPlan = rest[index]
        case "--write-plan":
            index += 1
            guard index < rest.count else {
                throw OpenComputerUseCLIError(message: "--write-plan requires a path")
            }
            writePlanPath = rest[index]
            writePlan = true
        case "--no-write-plan":
            writePlan = false
        case "--engine":
            index += 1
            guard index < rest.count else {
                throw OpenComputerUseCLIError(message: "--engine requires a value (compositor|ffmpeg)")
            }
            let value = rest[index].lowercased()
            guard ["compositor", "ffmpeg", "default", "legacy", "filter"].contains(value) else {
                throw OpenComputerUseCLIError(message: "invalid --engine \"\(rest[index])\" (compositor|ffmpeg)")
            }
            // Full CPU compositor is Linux/Windows; macOS keeps the ffmpeg filter path.
            polishEngine = "ffmpeg"
        case "--no-ripples":
            showClickRipples = false
        case "--ripples":
            showClickRipples = true
        case "--no-keystrokes":
            showKeystrokes = false
        case "--no-cursor":
            showCursorGhost = false
        case "--no-idle-speedup":
            idleSpeedup = false
        case "--no-zoom":
            smartZoom = false
        case "--cursor-style":
            index += 1
            guard index < rest.count else {
                throw OpenComputerUseCLIError(message: "--cursor-style requires a value")
            }
            let value = rest[index].lowercased()
            guard ["slow", "mellow", "quick", "rapid"].contains(value) else {
                throw OpenComputerUseCLIError(message: "invalid --cursor-style \"\(rest[index])\" (slow|mellow|quick|rapid)")
            }
            cursorStyle = value
        case "--idle-rate":
            index += 1
            guard index < rest.count, let parsed = Double(rest[index]), parsed >= 1 else {
                throw OpenComputerUseCLIError(message: "invalid --idle-rate \"\(index < rest.count ? rest[index] : "")\"")
            }
            idleRate = parsed
        default:
            throw OpenComputerUseCLIError(message: "unknown polish option: \(rest[index])")
        }
        index += 1
    }
    guard let polishInput, !polishInput.isEmpty else {
        throw OpenComputerUseCLIError(message: "record polish requires --input <raw.mp4>")
    }
    return DesktopRecordRequest(
        subcommand: .polish,
        output: nil,
        fps: 30,
        pidfile: nil,
        polishInput: polishInput,
        polishEvents: polishEvents,
        polishOutput: polishOutput,
        showClickRipples: showClickRipples,
        showKeystrokes: showKeystrokes,
        showCursorGhost: showCursorGhost,
        idleSpeedup: idleSpeedup,
        smartZoom: smartZoom,
        idleRate: idleRate,
        cursorStyle: cursorStyle,
        polishEngine: polishEngine,
        polishPlan: polishPlan,
        writePlan: writePlan,
        writePlanPath: writePlanPath
    )
}

func parseDesktopRecordProxyArguments(_ rest: [String]) throws -> DesktopRecordRequest {
    var proxyInput: String?
    var proxyOutputDir: String?
    var want1080p = true
    var wantFull = true
    var index = 0
    while index < rest.count {
        switch rest[index] {
        case "--input", "-i":
            index += 1
            guard index < rest.count else {
                throw OpenComputerUseCLIError(message: "--input requires a value")
            }
            proxyInput = rest[index]
        case "--output-dir", "-o":
            index += 1
            guard index < rest.count else {
                throw OpenComputerUseCLIError(message: "--output-dir requires a value")
            }
            proxyOutputDir = rest[index]
        case "--1080p":
            want1080p = true
        case "--full":
            wantFull = true
        case "--no-1080p":
            want1080p = false
        case "--no-full":
            wantFull = false
        default:
            throw OpenComputerUseCLIError(message: "unknown proxy option: \(rest[index])")
        }
        index += 1
    }
    guard let proxyInput, !proxyInput.isEmpty else {
        throw OpenComputerUseCLIError(message: "record proxy requires --input <raw.mp4>")
    }
    return DesktopRecordRequest(
        subcommand: .proxy,
        output: nil,
        fps: 30,
        pidfile: nil,
        proxyInput: proxyInput,
        proxyOutputDir: proxyOutputDir,
        proxyWant1080p: want1080p,
        proxyWantFull: wantFull
    )
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
    public static func runInput(_ command: DesktopInputCommand) throws -> String {
        let environment = ProcessInfo.processInfo.environment
        let scaler = try resolveDesktopInputScaler(apiSizeFlag: command.apiSize, environment: environment)
        let action = scaleDesktopInputAction(command.action, scaler: scaler)
        try DesktopInput.perform(action)
        if let event = buildRecordEvent(from: action) {
            appendRecordEventIfRecording(pidfile: nil, event: event)
        }
        switch action {
        case .move, .click, .mouseDown, .mouseUp, .drag, .scroll, .type, .key:
            return "input \(DesktopInput.actionName(action)) ok"
        case .wait(let seconds):
            return "waited \(seconds)s"
        }
    }

    /// Backward-compatible entry when no `--api-size` flag was parsed.
    public static func runInput(_ action: DesktopInputAction) throws -> String {
        try runInput(DesktopInputCommand(action: action))
    }

    /// `record <start|stop|discard|polish|proxy|status>`.
    public static func runRecord(_ request: DesktopRecordRequest) throws -> String {
        switch request.subcommand {
        case .start:
            return try DesktopRecord.start(request)
        case .stop:
            return try DesktopRecord.stop(
                pidfilePath: request.pidfile,
                saveAs: request.saveAs,
                polish: request.autoPolish
            )
        case .discard:
            return try DesktopRecord.discard(pidfilePath: request.pidfile)
        case .status:
            return try DesktopRecord.status(pidfilePath: request.pidfile)
        case .polish:
            return try DesktopRecord.runPolish(request)
        case .proxy:
            return try DesktopRecord.runProxy(request)
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
        case .mouseDown:
            return "mouse_down"
        case .mouseUp:
            return "mouse_up"
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
        case let .click(button, count, x, y, modifiers):
            try withModifiers(modifiers) {
                if let x, let y {
                    try postMouseEvent(type: .mouseMoved, point: globalPoint(CGPoint(x: x, y: y)), button: .left, clickState: 1)
                }
                try postClick(button: button, count: count)
            }
        case let .mouseDown(button, x, y, modifiers):
            try withModifiers(modifiers) {
                if let x, let y {
                    try postMouseEvent(type: .mouseMoved, point: globalPoint(CGPoint(x: x, y: y)), button: .left, clickState: 1)
                }
                let kind = mouseKind(button)
                let location = currentPointerLocation()
                try postMouseEvent(type: kind.downEvent, point: location, button: kind.cgButton, clickState: 1)
            }
        case let .mouseUp(button, x, y, modifiers):
            try withModifiers(modifiers) {
                if let x, let y {
                    try postMouseEvent(type: .mouseMoved, point: globalPoint(CGPoint(x: x, y: y)), button: .left, clickState: 1)
                }
                let kind = mouseKind(button)
                let location = currentPointerLocation()
                try postMouseEvent(type: kind.upEvent, point: location, button: kind.cgButton, clickState: 1)
            }
        case let .drag(fromX, fromY, toX, toY, button):
            try postDrag(from: CGPoint(x: fromX, y: fromY), to: CGPoint(x: toX, y: toY), button: button)
        case let .scroll(direction, amount, x, y, modifiers):
            try withModifiers(modifiers) {
                if let x, let y {
                    try postMouseEvent(type: .mouseMoved, point: globalPoint(CGPoint(x: x, y: y)), button: .left, clickState: 1)
                }
                try postScroll(direction: direction, amount: amount)
            }
        case let .type(text):
            try postTypeText(text)
        case let .key(specification, holdMs):
            try postKeyChord(specification, holdMs: holdMs)
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

    private static func modifierFromName(_ name: String) throws -> ParsedKeyPress.Modifier {
        switch normalizeDesktopModifierName(name) {
        case "cmd", "command", "super", "meta":
            return ParsedKeyPress.Modifier(flag: .maskCommand, keyCode: CGKeyCode(kVK_Command))
        case "shift":
            return ParsedKeyPress.Modifier(flag: .maskShift, keyCode: CGKeyCode(kVK_Shift))
        case "alt", "option":
            return ParsedKeyPress.Modifier(flag: .maskAlternate, keyCode: CGKeyCode(kVK_Option))
        case "ctrl", "control":
            return ParsedKeyPress.Modifier(flag: .maskControl, keyCode: CGKeyCode(kVK_Control))
        default:
            throw ComputerUseError.message("unsupported modifier '\(name)'")
        }
    }

    private static func withModifiers(_ modifiers: [String], body: () throws -> Void) throws {
        guard !modifiers.isEmpty else {
            try body()
            return
        }
        let parsed = try modifiers.map { try modifierFromName($0) }
        var activeFlags: CGEventFlags = []
        for modifier in parsed {
            guard let event = CGEvent(keyboardEventSource: nil, virtualKey: modifier.keyCode, keyDown: true) else {
                throw ComputerUseError.message("Failed to create modifier key down event.")
            }
            activeFlags.insert(modifier.flag)
            event.flags = activeFlags
            event.post(tap: .cghidEventTap)
        }
        defer {
            for modifier in parsed.reversed() {
                guard let event = CGEvent(keyboardEventSource: nil, virtualKey: modifier.keyCode, keyDown: false) else {
                    continue
                }
                event.flags = activeFlags
                event.post(tap: .cghidEventTap)
                activeFlags.remove(modifier.flag)
            }
        }
        try body()
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
        let segments = splitDesktopTypeSegments(text)
        for (segmentIndex, segment) in segments.enumerated() {
            if segmentIndex > 0 {
                try postKeyChord("Return", holdMs: 0)
            }
            if segment.isEmpty {
                continue
            }
            for chunk in chunkDesktopString(segment, size: defaultTypingBatchSize) {
                for unicodeChunk in InputSimulation.keyboardUnicodeChunks(for: chunk) {
                    var mutableChunk = unicodeChunk
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
                }
                Thread.sleep(forTimeInterval: Double(defaultTypingDelayMs) / 1000.0)
            }
        }
    }

    private static func postKeyChord(_ specification: String, holdMs: Int) throws {
        let parsed = try KeyPressParser.parse(specification)
        if holdMs > 0 {
            var activeFlags: CGEventFlags = []
            for modifier in parsed.modifiers {
                guard let event = CGEvent(keyboardEventSource: nil, virtualKey: modifier.keyCode, keyDown: true) else {
                    throw ComputerUseError.message("Failed to create modifier key down event.")
                }
                activeFlags.insert(modifier.flag)
                event.flags = activeFlags
                event.post(tap: .cghidEventTap)
            }
            guard let keyDown = CGEvent(keyboardEventSource: nil, virtualKey: parsed.keyCode, keyDown: true) else {
                throw ComputerUseError.message("Failed to create key event.")
            }
            keyDown.flags = activeFlags
            keyDown.post(tap: .cghidEventTap)
            Thread.sleep(forTimeInterval: Double(holdMs) / 1000.0)
            guard let keyUp = CGEvent(keyboardEventSource: nil, virtualKey: parsed.keyCode, keyDown: false) else {
                throw ComputerUseError.message("Failed to create key event.")
            }
            keyUp.flags = activeFlags
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
            return
        }

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

// MARK: - Record Events

/// Schema version 1 timeline event for `<stem>.events.json`.
public struct DesktopRecordEvent: Equatable, Sendable {
    public var tMs: Int64
    public var type: String
    public var x: Int?
    public var y: Int?
    public var toX: Int?
    public var toY: Int?
    public var button: String?
    public var count: Int?
    public var direction: String?
    public var amount: Int?
    public var text: String?
    public var key: String?
    public var seconds: Double?

    public init(
        tMs: Int64 = 0,
        type: String,
        x: Int? = nil,
        y: Int? = nil,
        toX: Int? = nil,
        toY: Int? = nil,
        button: String? = nil,
        count: Int? = nil,
        direction: String? = nil,
        amount: Int? = nil,
        text: String? = nil,
        key: String? = nil,
        seconds: Double? = nil
    ) {
        self.tMs = tMs
        self.type = type
        self.x = x
        self.y = y
        self.toX = toX
        self.toY = toY
        self.button = button
        self.count = count
        self.direction = direction
        self.amount = amount
        self.text = text
        self.key = key
        self.seconds = seconds
    }
}

extension DesktopRecordEvent: Codable {
    enum CodingKeys: String, CodingKey {
        case tMs = "t_ms"
        case type
        case x
        case y
        case toX = "to_x"
        case toY = "to_y"
        case button
        case count
        case direction
        case amount
        case text
        case key
        case seconds
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        tMs = try container.decodeIfPresent(Int64.self, forKey: .tMs) ?? 0
        type = try container.decode(String.self, forKey: .type)
        x = try container.decodeIfPresent(Int.self, forKey: .x)
        y = try container.decodeIfPresent(Int.self, forKey: .y)
        toX = try container.decodeIfPresent(Int.self, forKey: .toX)
        toY = try container.decodeIfPresent(Int.self, forKey: .toY)
        button = try container.decodeIfPresent(String.self, forKey: .button)
        count = try container.decodeIfPresent(Int.self, forKey: .count)
        direction = try container.decodeIfPresent(String.self, forKey: .direction)
        amount = try container.decodeIfPresent(Int.self, forKey: .amount)
        text = try container.decodeIfPresent(String.self, forKey: .text)
        key = try container.decodeIfPresent(String.self, forKey: .key)
        seconds = try container.decodeIfPresent(Double.self, forKey: .seconds)
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(tMs, forKey: .tMs)
        try container.encode(type, forKey: .type)
        try container.encodeIfPresent(x, forKey: .x)
        try container.encodeIfPresent(y, forKey: .y)
        try container.encodeIfPresent(toX, forKey: .toX)
        try container.encodeIfPresent(toY, forKey: .toY)
        try container.encodeIfPresent(button, forKey: .button)
        try container.encodeIfPresent(count, forKey: .count)
        try container.encodeIfPresent(direction, forKey: .direction)
        try container.encodeIfPresent(amount, forKey: .amount)
        try container.encodeIfPresent(text, forKey: .text)
        try container.encodeIfPresent(key, forKey: .key)
        try container.encodeIfPresent(seconds, forKey: .seconds)
    }
}

/// Sidecar event log written next to a recording.
public struct DesktopRecordEventLog: Equatable, Codable, Sendable {
    public var version: Int
    public var startedAtMs: Int64
    public var width: Int?
    public var height: Int?
    public var fps: Int?
    public var events: [DesktopRecordEvent]

    enum CodingKeys: String, CodingKey {
        case version
        case startedAtMs = "started_at_ms"
        case width
        case height
        case fps
        case events
    }

    public init(
        version: Int = 1,
        startedAtMs: Int64,
        width: Int? = nil,
        height: Int? = nil,
        fps: Int? = nil,
        events: [DesktopRecordEvent] = []
    ) {
        self.version = version
        self.startedAtMs = startedAtMs
        self.width = width
        self.height = height
        self.fps = fps
        self.events = events
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        version = try container.decodeIfPresent(Int.self, forKey: .version) ?? 1
        startedAtMs = try container.decode(Int64.self, forKey: .startedAtMs)
        width = try container.decodeIfPresent(Int.self, forKey: .width)
        height = try container.decodeIfPresent(Int.self, forKey: .height)
        fps = try container.decodeIfPresent(Int.self, forKey: .fps)
        events = try container.decodeIfPresent([DesktopRecordEvent].self, forKey: .events) ?? []
    }
}

/// Pure helper: turns a successful input action into a timeline event.
public func buildRecordEvent(from action: DesktopInputAction, tMs: Int64 = 0) -> DesktopRecordEvent? {
    switch action {
    case let .move(x, y):
        return DesktopRecordEvent(tMs: tMs, type: "move", x: x, y: y)
    case let .click(button, count, x, y, _):
        return DesktopRecordEvent(tMs: tMs, type: "click", x: x, y: y, button: button, count: count)
    case let .mouseDown(button, x, y, _):
        return DesktopRecordEvent(tMs: tMs, type: "mouse_down", x: x, y: y, button: button, count: 1)
    case let .mouseUp(button, x, y, _):
        return DesktopRecordEvent(tMs: tMs, type: "mouse_up", x: x, y: y, button: button, count: 1)
    case let .drag(fromX, fromY, toX, toY, button):
        return DesktopRecordEvent(tMs: tMs, type: "drag", x: fromX, y: fromY, toX: toX, toY: toY, button: button)
    case let .scroll(direction, amount, _, _, _):
        return DesktopRecordEvent(tMs: tMs, type: "scroll", direction: direction, amount: amount)
    case let .type(text):
        return DesktopRecordEvent(tMs: tMs, type: "type", text: text)
    case let .key(specification, _):
        return DesktopRecordEvent(tMs: tMs, type: "key", key: specification)
    case let .wait(seconds):
        return DesktopRecordEvent(tMs: tMs, type: "wait", seconds: seconds)
    }
}

public func defaultRenderPlanPath(forRaw input: String) -> String {
    let url = URL(fileURLWithPath: input)
    let ext = url.pathExtension
    if ext.isEmpty {
        return input + ".render-plan.json"
    }
    let stem = String(input.dropLast(ext.count + 1))
    return stem + ".render-plan.json"
}

public func recordEventsPath(forOutput output: String) -> String {
    let url = URL(fileURLWithPath: output)
    let ext = url.pathExtension
    if ext.isEmpty {
        return output + ".events.json"
    }
    let stem = String(output.dropLast(ext.count + 1))
    return stem + ".events.json"
}

public func defaultPolishedOutput(forRaw raw: String) -> String {
    let url = URL(fileURLWithPath: raw)
    let ext = url.pathExtension
    if ext.isEmpty {
        return raw + ".polished.mp4"
    }
    let stem = String(raw.dropLast(ext.count + 1))
    return stem + ".polished." + ext
}

func initRecordEventLog(output: String, width: Int, height: Int, fps: Int, started: Date) throws {
    let log = DesktopRecordEventLog(
        startedAtMs: Int64((started.timeIntervalSince1970 * 1000.0).rounded()),
        width: width > 0 ? width : nil,
        height: height > 0 ? height : nil,
        fps: fps > 0 ? fps : nil,
        events: []
    )
    try writeRecordEventLog(path: recordEventsPath(forOutput: output), log: log)
}

func writeRecordEventLog(path: String, log: DesktopRecordEventLog) throws {
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
    let data = try encoder.encode(log)
    try data.write(to: URL(fileURLWithPath: path))
}

func readRecordEventLog(path: String) throws -> DesktopRecordEventLog {
    let data = try Data(contentsOf: URL(fileURLWithPath: path))
    return try JSONDecoder().decode(DesktopRecordEventLog.self, from: data)
}

private let recordEventLock = NSLock()

/// Appends an event to the active recording's sidecar when a pidfile points
/// at a live recorder. Failures are ignored so event logging never breaks input.
func appendRecordEventIfRecording(pidfile: String?, event: DesktopRecordEvent) {
    recordEventLock.lock()
    defer { recordEventLock.unlock() }

    let path = pidfile ?? DesktopRecord.defaultPidfilePath()
    guard let state = DesktopRecord.readStateForEvents(pidfilePath: path),
          DesktopRecord.isProcessAlive(state.pid),
          !state.output.isEmpty
    else {
        return
    }
    let eventsPath = recordEventsPath(forOutput: state.output)
    var log: DesktopRecordEventLog
    if let existing = try? readRecordEventLog(path: eventsPath) {
        log = existing
    } else {
        var startedMs = Int64((Date().timeIntervalSince1970 * 1000.0).rounded())
        if let startedAt = state.startedAt {
            let formatter = ISO8601DateFormatter()
            formatter.formatOptions = [.withInternetDateTime]
            if let date = formatter.date(from: startedAt) {
                startedMs = Int64((date.timeIntervalSince1970 * 1000.0).rounded())
            }
        }
        log = DesktopRecordEventLog(startedAtMs: startedMs, fps: state.fps, events: [])
    }
    var event = event
    if event.tMs == 0 {
        let nowMs = Int64((Date().timeIntervalSince1970 * 1000.0).rounded())
        event.tMs = max(0, nowMs - log.startedAtMs)
    }
    log.events.append(event)
    try? writeRecordEventLog(path: eventsPath, log: log)
}

func removeRecordSidecars(output: String) {
    let fm = FileManager.default
    try? fm.removeItem(atPath: recordEventsPath(forOutput: output))
    try? fm.removeItem(atPath: output + ".log")
    try? fm.removeItem(atPath: output + ".ass")
    try? fm.removeItem(atPath: output + ".polish.log")
}

// MARK: - Record Polish (ffmpeg + ASS)

public struct DesktopPolishSegment: Equatable, Sendable {
    public var startMs: Int64
    public var endMs: Int64
    public var rate: Double

    public init(startMs: Int64, endMs: Int64, rate: Double) {
        self.startMs = startMs
        self.endMs = endMs
        self.rate = rate
    }
}

public struct DesktopZoomWindow: Equatable, Sendable {
    public var startMs: Int64
    public var endMs: Int64
    public var x: Int
    public var y: Int
    public var factor: Double

    public init(startMs: Int64, endMs: Int64, x: Int, y: Int, factor: Double) {
        self.startMs = startMs
        self.endMs = endMs
        self.x = x
        self.y = y
        self.factor = factor
    }
}

/// Pure analysis over the event log + duration.
public func buildPolishPlan(
    log: DesktopRecordEventLog,
    durationMs: Int64,
    opts: DesktopPolishOptions
) -> (segments: [DesktopPolishSegment], zooms: [DesktopZoomWindow], ass: String) {
    var durationMs = durationMs
    if durationMs <= 0 {
        durationMs = 1
    }
    let events = log.events.sorted { $0.tMs < $1.tMs }
    var width = log.width ?? 0
    var height = log.height ?? 0
    if width <= 0 { width = 1920 }
    if height <= 0 { height = 1200 }

    let segments = buildIdleSegments(events: events, durationMs: durationMs, opts: opts)
    let zooms = opts.smartZoom ? selectZoomWindows(events: events, durationMs: durationMs, opts: opts) : []
    let ass = buildPolishASS(events: events, width: width, height: height, opts: opts)
    return (segments, zooms, ass)
}

public func buildIdleSegments(
    events: [DesktopRecordEvent],
    durationMs: Int64,
    opts: DesktopPolishOptions
) -> [DesktopPolishSegment] {
    if !opts.idleSpeedup {
        return [DesktopPolishSegment(startMs: 0, endMs: durationMs, rate: 1)]
    }
    // Idle classification aligned with recording-renderer idle-classifier.js:
    // LOADING_WAIT (4x), THINKING_PAUSE (3x), VIEWING_RESULT (preserve 1x),
    // LONG_OPERATION (4x). Falls back to opts.idleRate when classification
    // suggests a generic speedup.
    let minIdle = opts.minIdleMs > 0 ? opts.minIdleMs : 500
    func actionType(_ ev: DesktopRecordEvent) -> String {
        switch ev.type {
        case "click":
            if (ev.count ?? 1) >= 3 { return "triple_click" }
            if (ev.count ?? 1) == 2 { return "double_click" }
            return "click"
        default:
            return ev.type
        }
    }
    func classify(duration: Int64, preceding: String, following: String) -> Double {
        if duration >= 10000 { return 4.0 }
        if (preceding == "click" || preceding == "double_click" || preceding == "triple_click") &&
            (following == "screenshot" || following == "none") {
            return 4.0
        }
        if preceding == "screenshot" && duration >= 500 && duration <= 3000 {
            return 1.0 // VIEWING_RESULT
        }
        if (preceding == "type" || preceding == "key") && (following == "type" || following == "key") && duration >= 5000 {
            return 3.0
        }
        if duration >= 5000 { return 3.0 }
        if duration >= 1000 &&
            (preceding == "click" || preceding == "double_click" || preceding == "type" || preceding == "key" || preceding == "scroll") {
            return 4.0
        }
        if duration >= minIdle {
            return max(opts.idleRate, 2.0)
        }
        return 1.0
    }

    if events.isEmpty {
        let rate = classify(duration: durationMs, preceding: "none", following: "none")
        return [DesktopPolishSegment(startMs: 0, endMs: durationMs, rate: rate)]
    }

    var periods: [(Int64, Int64, Double)] = []
    let first = events[0].tMs
    if first >= minIdle {
        periods.append((0, first, classify(duration: first, preceding: "none", following: actionType(events[0]))))
    }
    for i in 0..<(events.count - 1) {
        let gap = events[i + 1].tMs - events[i].tMs
        if gap < minIdle { continue }
        let rate = classify(duration: gap, preceding: actionType(events[i]), following: actionType(events[i + 1]))
        periods.append((events[i].tMs, events[i + 1].tMs, rate))
    }
    if let last = events.last, durationMs - last.tMs >= minIdle {
        periods.append((last.tMs, durationMs, classify(duration: durationMs - last.tMs, preceding: actionType(last), following: "none")))
    }

    var cuts: Set<Int64> = [0, durationMs]
    for p in periods {
        cuts.insert(p.0)
        cuts.insert(p.1)
    }
    let points = cuts.sorted()
    var segments: [DesktopPolishSegment] = []
    for i in 0..<(points.count - 1) {
        let a = points[i]
        let b = points[i + 1]
        if b <= a { continue }
        var rate = 1.0
        for p in periods where a >= p.0 && a < p.1 {
            rate = p.2
            break
        }
        segments.append(DesktopPolishSegment(startMs: a, endMs: b, rate: rate))
    }
    if segments.isEmpty {
        return [DesktopPolishSegment(startMs: 0, endMs: durationMs, rate: 1)]
    }
    return mergePolishSegments(segments)
}

public func mergePolishSegments(_ input: [DesktopPolishSegment]) -> [DesktopPolishSegment] {
    guard !input.isEmpty else { return input }
    var out: [DesktopPolishSegment] = [input[0]]
    for seg in input.dropFirst() {
        var seg = seg
        var last = out[out.count - 1]
        if seg.startMs <= last.endMs && abs(seg.rate - last.rate) < 0.001 {
            if seg.endMs > last.endMs {
                last.endMs = seg.endMs
                out[out.count - 1] = last
            }
            continue
        }
        if seg.startMs < last.endMs {
            seg.startMs = last.endMs
        }
        if seg.endMs <= seg.startMs {
            continue
        }
        out.append(seg)
    }
    return out
}

func selectZoomWindows(
    events: [DesktopRecordEvent],
    durationMs: Int64,
    opts: DesktopPolishOptions
) -> [DesktopZoomWindow] {
    // Port of recording-renderer click-importance + zoom selection:
    // base 50 + bonuses/penalties, threshold 60, min interval 1500ms, max 8/min.
    struct Candidate {
        var tMs: Int64
        var x: Int
        var y: Int
        var score: Int
    }
    let width = 1920
    let height = 1200
    var clicks: [(index: Int, ev: DesktopRecordEvent)] = []
    for (i, ev) in events.enumerated() where ev.type == "click" {
        let x = ev.x ?? 0
        let y = ev.y ?? 0
        if x == 0 && y == 0 { continue }
        clicks.append((i, ev))
    }

    var cands: [Candidate] = []
    var lastClickT: Int64 = 0
    var lastX = 0
    var lastY = 0
    var lastNonClick: Int64 = 0
    let rapidMs: Int64 = 500
    let idleMs: Int64 = 3000
    let sameArea = 50.0
    let edge = 100

    for (pos, item) in clicks.enumerated() {
        let ev = item.ev
        let x = ev.x ?? 0
        let y = ev.y ?? 0
        var score = 50
        let count = ev.count ?? 1
        if count >= 2 { score += 25 }
        if count >= 3 { score += 20 }
        if (ev.button ?? "").lowercased() == "right" { score += 15 }
        if lastClickT > 0 {
            let dt = ev.tMs - lastClickT
            if dt < rapidMs { score -= 25 }
            if dt > idleMs { score += 15 }
            let dist = hypot(Double(x - lastX), Double(y - lastY))
            if dist < sameArea { score -= 15 }
        }
        if x < edge || y < edge || x > width - edge || y > height - edge {
            score -= 20
        }
        if ev.tMs - lastNonClick > idleMs {
            score += 25
        }
        let nextIdx = item.index + 1
        if nextIdx < events.count {
            let nxt = events[nextIdx].type
            if nxt == "type" || nxt == "key" { score += 30 }
        }
        score = max(0, min(100, score))
        cands.append(Candidate(tMs: ev.tMs, x: x, y: y, score: score))
        lastClickT = ev.tMs
        lastX = x
        lastY = y
        lastNonClick = ev.tMs
        _ = pos
    }
    // Advance lastNonClick for non-click events chronologically (approx via re-scan)
    lastNonClick = 0
    var clickPos = 0
    for ev in events {
        if clickPos < cands.count && ev.type == "click" && ev.tMs == cands[clickPos].tMs {
            if ev.tMs - lastNonClick > idleMs {
                cands[clickPos].score = max(0, min(100, cands[clickPos].score + 0)) // already applied
            }
            lastNonClick = ev.tMs
            clickPos += 1
            continue
        }
        if ev.type != "wait" {
            lastNonClick = ev.tMs
        }
    }

    cands.sort { lhs, rhs in
        if lhs.score != rhs.score { return lhs.score > rhs.score }
        return lhs.tMs < rhs.tMs
    }

    var maxZooms = opts.maxZooms
    if durationMs > 0 {
        let perMin = Int((Double(durationMs) / 60000.0) * 8.0 + 0.999)
        maxZooms = max(1, min(maxZooms, max(1, perMin)))
    }

    var zooms: [DesktopZoomWindow] = []
    var lastStart = -opts.minZoomIntervalMs
    for c in cands {
        if c.score < opts.zoomImportance { continue }
        if zooms.count >= maxZooms { break }
        var start = c.tMs - 200
        if start < 0 { start = 0 }
        var end = start + opts.zoomDurationMs
        if end > durationMs { end = durationMs }
        if start < lastStart + opts.minZoomIntervalMs { continue }
        var overlap = false
        for z in zooms where start < z.endMs && end > z.startMs {
            overlap = true
            break
        }
        if overlap { continue }
        zooms.append(DesktopZoomWindow(startMs: start, endMs: end, x: c.x, y: c.y, factor: opts.zoomFactor))
        lastStart = start
    }
    zooms.sort { $0.startMs < $1.startMs }
    return zooms
}

public func buildPolishASS(
    events: [DesktopRecordEvent],
    width: Int,
    height: Int,
    opts: DesktopPolishOptions
) -> String {
    var b = ""
    b += "[Script Info]\n"
    b += "Title: open-computer-use polish\n"
    b += "ScriptType: v4.00+\n"
    b += "PlayResX: \(width)\nPlayResY: \(height)\n\n"
    b += "[V4+ Styles]\n"
    b += "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n"
    b += "Style: Keystroke,Menlo,42,&H00FFFFFF,&H000000FF,&H64000000,&H80000000,-1,0,0,0,100,100,0,0,1,2,0,2,40,40,60,1\n"
    b += "Style: Ripple,Arial,20,&H0000FFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,0,0,5,0,0,0,1\n"
    b += "Style: Cursor,Arial,28,&H0000D7FF,&H000000FF,&H64000000,&H00000000,-1,0,0,0,100,100,0,0,1,1,0,5,0,0,0,1\n\n"
    b += "[Events]\n"
    b += "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"

    if opts.showKeystrokes {
        b += writeKeystrokeASS(events: events)
    }
    if opts.showClickRipples {
        b += writeRippleASS(events: events)
    }
    if opts.showCursorGhost {
        b += writeCursorGhostASS(events: events)
    }
    return b
}

func writeKeystrokeASS(events: [DesktopRecordEvent]) -> String {
    let displayMs: Int64 = 1500
    let combineMs: Int64 = 500
    var pendingText = ""
    var pendingStart: Int64 = 0
    var pendingEnd: Int64 = 0
    var b = ""

    func flush() {
        guard !pendingText.isEmpty else { return }
        var text = pendingText
        if text.count > 30 {
            text = String(text.prefix(27)) + "…"
        }
        b += "Dialogue: 0,\(formatASSTime(pendingStart)),\(formatASSTime(pendingEnd)),Keystroke,,0,0,0,,\(escapeASS(text))\n"
        pendingText = ""
    }

    for ev in events {
        switch ev.type {
        case "type":
            let text = ev.text ?? ""
            if !pendingText.isEmpty && ev.tMs - pendingEnd <= combineMs {
                pendingText += text
                pendingEnd = ev.tMs + displayMs
            } else {
                flush()
                pendingText = text
                pendingStart = ev.tMs
                pendingEnd = ev.tMs + displayMs
            }
        case "key":
            flush()
            let label = keyDisplayLabel(ev.key ?? "")
            b += "Dialogue: 0,\(formatASSTime(ev.tMs)),\(formatASSTime(ev.tMs + displayMs)),Keystroke,,0,0,0,,\(escapeASS(label))\n"
        default:
            flush()
        }
    }
    flush()
    return b
}

func writeRippleASS(events: [DesktopRecordEvent]) -> String {
    // Thin expanding rings (transparent fill + border stroke). Never use filled
    // ASS \p1 boxes — those rendered as large opaque "blobs" on demos.
    var b = ""
    var lastT: Int64 = Int64.min / 4
    for ev in events {
        guard ev.type == "click" || ev.type == "drag" else { continue }
        var x = ev.x ?? 0
        var y = ev.y ?? 0
        if ev.type == "drag" {
            x = ev.toX ?? x
            y = ev.toY ?? y
        }
        if x == 0 && y == 0 { continue }
        let count = ev.count ?? 1
        if ev.tMs - lastT < 200 && count < 2 { continue }
        lastT = ev.tMs

        // Brief center flash (small glyph, not a filled disc).
        b += "Dialogue: 2,\(formatASSTime(ev.tMs)),\(formatASSTime(ev.tMs + 90)),Ripple,,0,0,0,,{\\pos(\(x),\(y))\\fs10\\c&H00D7FF&\\alpha&H40&●}\n"

        // Radii stay compact (~cursor scale). ASS colours are &HAABBGGRR&.
        for (i, radius) in [6, 11, 16, 22].enumerated() {
            let start = ev.tMs + Int64(i * 55)
            let end = start + 120
            let outlineAlpha = 0x30 + i * 0x28
            // Bezier circle approximation; \1a&HFF& = fully transparent fill,
            // \bord draws the visible thin ring.
            let draw =
                "{\\an5\\pos(\(x),\(y))\\p1\\1a&HFF&\\bord2\\3c&H00D7FF&\\3a&H" +
                String(format: "%02X", outlineAlpha) +
                "&\\shad0}m 0 \(-radius) b \(radius) \(-radius) \(radius) \(radius) 0 \(radius) b \(-radius) \(radius) \(-radius) \(-radius) 0 \(-radius){\\p0}"
            b += "Dialogue: 1,\(formatASSTime(start)),\(formatASSTime(end)),Ripple,,0,0,0,,\(draw)\n"
        }
    }
    return b
}

func writeCursorGhostASS(events: [DesktopRecordEvent]) -> String {
    struct Point {
        var t: Int64
        var x: Int
        var y: Int
    }
    var points: [Point] = []
    for ev in events {
        switch ev.type {
        case "move", "click":
            let x = ev.x ?? 0
            let y = ev.y ?? 0
            if x != 0 || y != 0 {
                points.append(Point(t: ev.tMs, x: x, y: y))
            }
        case "drag":
            points.append(Point(t: ev.tMs, x: ev.x ?? 0, y: ev.y ?? 0))
            points.append(Point(t: ev.tMs + 200, x: ev.toX ?? 0, y: ev.toY ?? 0))
        default:
            break
        }
    }
    var b = ""
    for (i, p) in points.enumerated() {
        var end = p.t + 120
        if i + 1 < points.count {
            end = points[i + 1].t
        }
        if end <= p.t {
            end = p.t + 80
        }
        b += "Dialogue: 3,\(formatASSTime(p.t)),\(formatASSTime(end)),Cursor,,0,0,0,,{\\pos(\(p.x),\(p.y))}▶\n"
    }
    return b
}

public func keyDisplayLabel(_ key: String) -> String {
    let replacements: [String: String] = [
        "Return": "↵ Enter", "Enter": "↵ Enter", "Tab": "⇥ Tab", "Escape": "⎋ Esc",
        "BackSpace": "⌫", "Delete": "⌦ Del", "space": "␣ Space", "Up": "↑", "Down": "↓",
        "Left": "←", "Right": "→", "Home": "⇱ Home", "End": "⇲ End",
        "Page_Up": "⇞ PgUp", "Page_Down": "⇟ PgDn",
        "return": "↵ Enter", "enter": "↵ Enter", "tab": "⇥ Tab", "escape": "⎋ Esc",
        "backspace": "⌫", "delete": "⌦ Del",
    ]
    let parts = key.split(separator: "+", omittingEmptySubsequences: false).map(String.init)
    let mapped = parts.map { part -> String in
        if let rep = replacements[part] { return rep }
        if let rep = replacements[part.lowercased()] { return rep }
        if part.count == 1 { return part.uppercased() }
        return part
    }
    return mapped.joined(separator: " + ")
}

public func escapeASS(_ text: String) -> String {
    text
        .replacingOccurrences(of: "\\", with: "\\\\")
        .replacingOccurrences(of: "{", with: "\\[")
        .replacingOccurrences(of: "}", with: "\\]")
        .replacingOccurrences(of: "\n", with: "\\N")
}

public func formatASSTime(_ ms: Int64) -> String {
    var ms = ms
    if ms < 0 { ms = 0 }
    let h = ms / 3_600_000
    ms %= 3_600_000
    let m = ms / 60_000
    ms %= 60_000
    let s = ms / 1000
    let cs = (ms % 1000) / 10
    return String(format: "%d:%02d:%02d.%02d", h, m, s, cs)
}

func escapeFFmpegFilterPath(_ path: String) -> String {
    path
        .replacingOccurrences(of: "\\", with: "\\\\")
        .replacingOccurrences(of: ":", with: "\\:")
        .replacingOccurrences(of: "'", with: "\\'")
        .replacingOccurrences(of: "[", with: "\\[")
        .replacingOccurrences(of: "]", with: "\\]")
}

public func buildPolishFilterComplex(
    segments: [DesktopPolishSegment],
    zooms: [DesktopZoomWindow],
    assPath: String,
    width: Int,
    height: Int
) throws -> String {
    guard !segments.isEmpty else {
        throw ComputerUseError.message("no polish segments")
    }
    return try buildPolishFilterComplexSplit(
        segments: segments,
        zooms: zooms,
        assEscaped: escapeFFmpegFilterPath(assPath),
        width: width,
        height: height
    )
}

func buildPolishFilterComplexSplit(
    segments: [DesktopPolishSegment],
    zooms: [DesktopZoomWindow],
    assEscaped: String,
    width: Int,
    height: Int
) throws -> String {
    guard !segments.isEmpty else {
        throw ComputerUseError.message("no segments")
    }
    var b = ""
    var pre = "[0:v]"
    // Like Go: only apply the first zoom window (multi-zoom simplified).
    if let z = zooms.first {
        var zf = z.factor
        if zf < 1.05 { zf = 1.45 }
        b += String(
            format: "[0:v]zoompan=z='if(between(time,%0.3f,%0.3f),%0.3f,1)':x='iw/2-(iw/zoom/2)':y='ih/2-(ih/zoom/2)':d=1:s=%dx%d:fps=30[zoomed];",
            Double(z.startMs) / 1000.0,
            Double(z.endMs) / 1000.0,
            zf,
            width,
            height
        )
        pre = "[zoomed]"
    }
    b += "\(pre)ass=filename='\(assEscaped)'[annotated];"

    if segments.count == 1, abs(segments[0].rate - 1) < 0.001 {
        b += "[annotated]copy[outv]"
        return b
    }

    let n = segments.count
    b += "[annotated]split=\(n)"
    for i in 0..<n {
        b += "[s\(i)]"
    }
    b += ";"

    var concatInputs = ""
    var used = 0
    for (i, seg) in segments.enumerated() {
        let start = Double(seg.startMs) / 1000.0
        let end = Double(seg.endMs) / 1000.0
        if end <= start { continue }
        var rate = seg.rate
        if rate < 1.01 { rate = 1 }
        b += String(format: "[s%d]trim=start=%0.3f:end=%0.3f,setpts=(PTS-STARTPTS)/%0.3f[v%d];", i, start, end, rate, used)
        concatInputs += "[v\(used)]"
        used += 1
    }
    if used == 0 {
        throw ComputerUseError.message("no usable polish segments")
    }
    if used == 1 {
        b += "[v0]copy[outv]"
        return b
    }
    b += "\(concatInputs)concat=n=\(used):v=1:a=0[outv]"
    return b
}

    return b
}

// MARK: - Render plan JSON (simplified export for polish parity)

public struct DesktopRenderPlanJSON: Codable, Equatable, Sendable {
    public struct Video: Codable, Equatable, Sendable {
        public var inputVideoPath: String
        public var sourceDurationMs: Double
        public var outputDurationMs: Double
        public var width: Int
        public var height: Int
        public var fps: Int
        public var configHash: String
    }

    public struct PlaybackSegment: Codable, Equatable, Sendable {
        public var type: String
        public var sourceStartMs: Double
        public var sourceEndMs: Double
        public var sourceDurationMs: Double
        public var outputStartMs: Double
        public var outputEndMs: Double
        public var outputDurationMs: Double
        public var playbackRate: Double
    }

    public struct Playback: Codable, Equatable, Sendable {
        public var segments: [PlaybackSegment]
        public var outputDurationMs: Double
        public var sourceDurationMs: Double
    }

    public struct ClickEffect: Codable, Equatable, Sendable {
        public var timeMs: Double
        public var x: Double
        public var y: Double
        public var score: Int?
    }

    public struct KeystrokeEvent: Codable, Equatable, Sendable {
        public var timeMs: Double
        public var text: String?
        public var key: String?
        public var eventType: String?
    }

    public struct ZoomWindow: Codable, Equatable, Sendable {
        public var startMs: Double
        public var endMs: Double
        public var x: Double
        public var y: Double
        public var factor: Double
    }

    public struct Tracks: Codable, Equatable, Sendable {
        public var clickEffects: [ClickEffect]
        public var keystrokeEvents: [KeystrokeEvent]
        public var zoomWindows: [ZoomWindow]
        public var cursorStyle: String
    }

    public var video: Video
    public var playback: Playback
    public var tracks: Tracks
}

public func outputDurationFromPolishSegments(_ segments: [DesktopPolishSegment]) -> Double {
    var total = 0.0
    for segment in segments {
        let rate = segment.rate < 1.01 ? 1.0 : segment.rate
        let sourceDuration = Double(segment.endMs - segment.startMs)
        if sourceDuration > 0 {
            total += sourceDuration / rate
        }
    }
    return total
}

public func buildRenderPlanJSON(
    inputVideo: String,
    log: DesktopRecordEventLog,
    durationMs: Int64,
    opts: DesktopPolishOptions,
    segments: [DesktopPolishSegment],
    zooms: [DesktopZoomWindow]
) -> DesktopRenderPlanJSON {
    let sourceDuration = Double(max(durationMs, 1))
    let outputDuration = outputDurationFromPolishSegments(segments)
    var outputCursor = 0.0
    var playbackSegments: [DesktopRenderPlanJSON.PlaybackSegment] = []
    for segment in segments {
        let rate = segment.rate < 1.01 ? 1.0 : segment.rate
        let sourceStart = Double(segment.startMs)
        let sourceEnd = Double(segment.endMs)
        let sourceSegmentDuration = sourceEnd - sourceStart
        let outputSegmentDuration = sourceSegmentDuration / rate
        playbackSegments.append(
            DesktopRenderPlanJSON.PlaybackSegment(
                type: rate > 1.01 ? "gap" : "action",
                sourceStartMs: sourceStart,
                sourceEndMs: sourceEnd,
                sourceDurationMs: sourceSegmentDuration,
                outputStartMs: outputCursor,
                outputEndMs: outputCursor + outputSegmentDuration,
                outputDurationMs: outputSegmentDuration,
                playbackRate: rate
            )
        )
        outputCursor += outputSegmentDuration
    }
    var clicks: [DesktopRenderPlanJSON.ClickEffect] = []
    for event in log.events where event.type == "click" {
        clicks.append(
            DesktopRenderPlanJSON.ClickEffect(
                timeMs: Double(event.tMs),
                x: Double(event.x ?? 0),
                y: Double(event.y ?? 0),
                score: 60
            )
        )
    }
    var keys: [DesktopRenderPlanJSON.KeystrokeEvent] = []
    for event in log.events {
        switch event.type {
        case "key":
            keys.append(
                DesktopRenderPlanJSON.KeystrokeEvent(
                    timeMs: Double(event.tMs),
                    key: event.key,
                    eventType: "keyCombo"
                )
            )
        case "type":
            keys.append(
                DesktopRenderPlanJSON.KeystrokeEvent(
                    timeMs: Double(event.tMs),
                    text: event.text,
                    eventType: "textTyped"
                )
            )
        default:
            break
        }
    }
    let zoomWindows = zooms.map {
        DesktopRenderPlanJSON.ZoomWindow(
            startMs: Double($0.startMs),
            endMs: Double($0.endMs),
            x: Double($0.x),
            y: Double($0.y),
            factor: $0.factor
        )
    }
    let width = log.width ?? 1920
    let height = log.height ?? 1200
    let fps = log.fps ?? 30
    return DesktopRenderPlanJSON(
        video: DesktopRenderPlanJSON.Video(
            inputVideoPath: inputVideo,
            sourceDurationMs: sourceDuration,
            outputDurationMs: outputDuration,
            width: width,
            height: height,
            fps: fps,
            configHash: "ocu-cleanroom-v1"
        ),
        playback: DesktopRenderPlanJSON.Playback(
            segments: playbackSegments,
            outputDurationMs: outputDuration,
            sourceDurationMs: sourceDuration
        ),
        tracks: DesktopRenderPlanJSON.Tracks(
            clickEffects: clicks,
            keystrokeEvents: keys,
            zoomWindows: zoomWindows,
            cursorStyle: opts.cursorStyle
        )
    )
}

public func writeRenderPlanJSON(path: String, plan: DesktopRenderPlanJSON) throws {
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
    let data = try encoder.encode(plan)
    try data.write(to: URL(fileURLWithPath: path))
}

func exportRenderPlanBestEffort(input: String, eventsPath: String, planPath: String, opts: DesktopPolishOptions) throws {
    var log = try readRecordEventLog(path: eventsPath)
    let probed = try probeVideoDurationMs(path: input)
    if (log.width ?? 0) <= 0 { log.width = probed.width }
    if (log.height ?? 0) <= 0 { log.height = probed.height }
    let plan = buildPolishPlan(log: log, durationMs: probed.durationMs, opts: opts)
    let renderPlan = buildRenderPlanJSON(
        inputVideo: input,
        log: log,
        durationMs: probed.durationMs,
        opts: opts,
        segments: plan.segments,
        zooms: plan.zooms
    )
    try writeRenderPlanJSON(path: planPath, plan: renderPlan)
}

func probeVideoDurationMs(path: String) throws -> (durationMs: Int64, width: Int, height: Int) {
    let ffprobe = DesktopRecord.resolveFfprobeURL()
        ?? URL(fileURLWithPath: "/usr/bin/ffprobe")
    let process = Process()
    process.executableURL = ffprobe
    process.arguments = [
        "-v", "error",
        "-show_entries", "format=duration:stream=width,height",
        "-of", "json",
        path,
    ]
    let pipe = Pipe()
    process.standardOutput = pipe
    process.standardError = Pipe()
    try process.run()
    process.waitUntilExit()
    let data = pipe.fileHandleForReading.readDataToEndOfFile()
    guard process.terminationStatus == 0 else {
        throw ComputerUseError.message("ffprobe failed for \(path)")
    }
    guard
        let obj = try JSONSerialization.jsonObject(with: data) as? [String: Any],
        let format = obj["format"] as? [String: Any],
        let durationStr = format["duration"] as? String,
        let seconds = Double(durationStr)
    else {
        throw ComputerUseError.message("invalid ffprobe duration for \(path)")
    }
    var width = 0
    var height = 0
    if let streams = obj["streams"] as? [[String: Any]] {
        for stream in streams {
            if let w = stream["width"] as? Int, let h = stream["height"] as? Int, w > 0, h > 0 {
                width = w
                height = h
                break
            }
        }
    }
    return (Int64(seconds * 1000.0), width, height)
}

func polishRecording(
    inputVideo: String,
    eventsPath: String,
    outputVideo: String,
    opts: DesktopPolishOptions,
    planPath: String? = nil,
    writePlan: Bool = false,
    writePlanPath: String? = nil
) throws {
    guard let ffmpegURL = DesktopRecord.resolveFfmpegURL() else {
        throw ComputerUseError.message("ffmpeg is required for record polish but was not found on PATH")
    }
    var log = try readRecordEventLog(path: eventsPath)
    let probed = try probeVideoDurationMs(path: inputVideo)
    if (log.width ?? 0) <= 0 { log.width = probed.width }
    if (log.height ?? 0) <= 0 { log.height = probed.height }

    let plan = buildPolishPlan(log: log, durationMs: probed.durationMs, opts: opts)
    if writePlan {
        let exportPath = writePlanPath ?? defaultRenderPlanPath(forRaw: inputVideo)
        let renderPlan = buildRenderPlanJSON(
            inputVideo: inputVideo,
            log: log,
            durationMs: probed.durationMs,
            opts: opts,
            segments: plan.segments,
            zooms: plan.zooms
        )
        try? writeRenderPlanJSON(path: exportPath, plan: renderPlan)
    }
    if let planPath, !planPath.isEmpty {
        // Accept --plan for CLI parity; macOS ffmpeg path still uses analyzed segments.
        _ = planPath
    }
    let assPath = inputVideo + ".ass"
    try plan.ass.write(to: URL(fileURLWithPath: assPath), atomically: true, encoding: .utf8)

    let filter = try buildPolishFilterComplex(
        segments: plan.segments,
        zooms: plan.zooms,
        assPath: assPath,
        width: log.width ?? probed.width,
        height: log.height ?? probed.height
    )
    let args = [
        "-nostdin", "-y",
        "-i", inputVideo,
        "-filter_complex", filter,
        "-map", "[outv]",
        "-c:v", "libx264",
        "-preset", "veryfast",
        "-crf", "18",
        "-pix_fmt", "yuv420p",
        "-profile:v", "high",
        "-movflags", "+faststart",
        outputVideo,
    ]
    let process = Process()
    process.executableURL = ffmpegURL
    process.arguments = args
    let logURL = URL(fileURLWithPath: outputVideo + ".polish.log")
    FileManager.default.createFile(atPath: logURL.path, contents: nil)
    let logHandle = try? FileHandle(forWritingTo: logURL)
    defer { try? logHandle?.close() }
    process.standardOutput = logHandle ?? FileHandle.nullDevice
    process.standardError = logHandle ?? FileHandle.nullDevice
    try process.run()
    process.waitUntilExit()
    guard process.terminationStatus == 0 else {
        var detail = ""
        if let data = FileManager.default.contents(atPath: logURL.path),
           let text = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines),
           !text.isEmpty
        {
            detail = text.count > 600 ? String(text.suffix(600)) : text
        }
        throw ComputerUseError.message("ffmpeg polish failed: \(detail)")
    }
}

// MARK: - Render proxies

public struct DesktopProxyArtifact: Codable, Equatable, Sendable {
    public var path: String
    public var width: Int
    public var height: Int
    public var createdAt: String
}

public struct DesktopRenderProxiesMetadata: Codable, Equatable, Sendable {
    public var version: Int
    public var source: String
    public var primary1080p: DesktopProxyArtifact?
    public var full: DesktopProxyArtifact?
    public var createdAt: String
}

func runProxyEncode(input: String, output: String, targetHeight: Int, ffmpegURL: URL) throws {
    var args = ["-nostdin", "-y", "-i", input]
    if targetHeight > 0 {
        args += ["-vf", "scale=-2:\(targetHeight):flags=lanczos"]
    }
    args += [
        "-c:v", "libx264",
        "-preset", "veryfast",
        "-crf", "17",
        "-pix_fmt", "yuv420p",
        "-profile:v", "high",
        "-x264-params", "keyint=1:min-keyint=1:scenecut=0:bframes=0",
        "-movflags", "+faststart",
        "-an",
        output,
    ]
    let process = Process()
    process.executableURL = ffmpegURL
    process.arguments = args
    let logURL = URL(fileURLWithPath: output + ".log")
    FileManager.default.createFile(atPath: logURL.path, contents: nil)
    let logHandle = try? FileHandle(forWritingTo: logURL)
    defer { try? logHandle?.close() }
    process.standardOutput = logHandle ?? FileHandle.nullDevice
    process.standardError = logHandle ?? FileHandle.nullDevice
    try process.run()
    process.waitUntilExit()
    guard process.terminationStatus == 0 else {
        var detail = ""
        if let data = FileManager.default.contents(atPath: logURL.path),
           let text = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines),
           !text.isEmpty
        {
            detail = text.count > 400 ? String(text.suffix(400)) : text
        }
        throw ComputerUseError.message("proxy encode failed: \(detail)")
    }
}

func generateRenderProxies(sourceVideo: String, outDir: String, want1080p: Bool, wantFull: Bool) throws -> DesktopRenderProxiesMetadata {
    guard let ffmpegURL = DesktopRecord.resolveFfmpegURL() else {
        throw ComputerUseError.message("ffmpeg is required for proxy generation")
    }
    let probed = try probeVideoDurationMs(path: sourceVideo)
    let createdAt = ISO8601DateFormatter().string(from: Date())
    var meta = DesktopRenderProxiesMetadata(version: 1, source: sourceVideo, createdAt: createdAt)
    if want1080p {
        let path = URL(fileURLWithPath: outDir).appendingPathComponent("proxy-1080p.mp4").path
        var targetHeight = 1080
        if probed.height > 0, probed.height < 1080 {
            targetHeight = probed.height
        }
        try runProxyEncode(input: sourceVideo, output: path, targetHeight: targetHeight, ffmpegURL: ffmpegURL)
        meta.primary1080p = DesktopProxyArtifact(path: path, width: 0, height: targetHeight, createdAt: createdAt)
    }
    if wantFull {
        let path = URL(fileURLWithPath: outDir).appendingPathComponent("proxy-full.mp4").path
        try runProxyEncode(input: sourceVideo, output: path, targetHeight: 0, ffmpegURL: ffmpegURL)
        meta.full = DesktopProxyArtifact(path: path, width: probed.width, height: probed.height, createdAt: createdAt)
    }
    let metaPath = URL(fileURLWithPath: outDir).appendingPathComponent("render-proxies.json").path
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
    let data = try encoder.encode(meta)
    try data.write(to: URL(fileURLWithPath: metaPath))
    return meta
}

// MARK: - Record

enum DesktopRecord {
    struct RecordState: Codable {
        let pid: Int32
        let output: String
        let backend: String?
        let fps: Int?
        let quality: String?
        let drawMouse: Int?
        let startedAt: String?
        let autoPolish: Bool?
        let eventsPath: String?

        init(
            pid: Int32,
            output: String,
            backend: String? = nil,
            fps: Int? = nil,
            quality: String? = nil,
            drawMouse: Int? = nil,
            startedAt: String? = nil,
            autoPolish: Bool? = nil,
            eventsPath: String? = nil
        ) {
            self.pid = pid
            self.output = output
            self.backend = backend
            self.fps = fps
            self.quality = quality
            self.drawMouse = drawMouse
            self.startedAt = startedAt
            self.autoPolish = autoPolish
            self.eventsPath = eventsPath
        }
    }

    static func defaultPidfilePath() -> String {
        FileManager.default.temporaryDirectory
            .appendingPathComponent("open-computer-use-record.pid")
            .standardizedFileURL
            .path
    }

    static func defaultOutputPath(preferMP4: Bool) -> String {
        let formatter = DateFormatter()
        formatter.dateFormat = "yyyyMMdd-HHmmss"
        formatter.locale = Locale(identifier: "en_US_POSIX")
        let ext = preferMP4 ? "mp4" : "mov"
        let name = "open-computer-use-recording-\(formatter.string(from: Date())).\(ext)"
        return FileManager.default.temporaryDirectory
            .appendingPathComponent(name)
            .standardizedFileURL
            .path
    }

    static func buildFfmpegAvfoundationArgs(
        output: String,
        fps: Int,
        quality: String,
        drawMouse: Int,
        screenDevice: String
    ) -> [String] {
        let resolvedFPS = fps > 0 ? fps : 30
        let cursor = (drawMouse == 0) ? "0" : "1"
        var args: [String] = [
            "-nostdin", "-y",
            "-f", "avfoundation",
            "-framerate", "\(resolvedFPS)",
            "-capture_cursor", cursor,
            "-i", screenDevice,
        ]
        args += ["-c:v", "libx264"]
        switch quality {
        case "draft":
            args += ["-preset", "ultrafast", "-pix_fmt", "yuv420p"]
        case "proxy", "anyos":
            args += [
                "-preset", "veryfast",
                "-crf", "17",
                "-pix_fmt", "yuv420p",
                "-profile:v", "high",
                "-x264-params", "keyint=1:min-keyint=1:scenecut=0:bframes=0",
                "-movflags", "+faststart",
                "-tune", "fastdecode",
            ]
        default:
            args += [
                "-preset", "veryfast",
                "-crf", "17",
                "-pix_fmt", "yuv420p",
                "-profile:v", "high",
                "-movflags", "+faststart",
                "-tune", "fastdecode",
            ]
        }
        args.append(output)
        return args
    }

    static func resolveFfmpegURL() -> URL? {
        resolveToolURL(named: "ffmpeg")
    }

    static func resolveFfprobeURL() -> URL? {
        if let ffmpeg = resolveFfmpegURL() {
            let sibling = ffmpeg.deletingLastPathComponent().appendingPathComponent("ffprobe")
            if FileManager.default.isExecutableFile(atPath: sibling.path) {
                return sibling
            }
        }
        return resolveToolURL(named: "ffprobe")
    }

    private static func resolveToolURL(named name: String) -> URL? {
        let candidates = [
            "/opt/homebrew/bin/\(name)",
            "/usr/local/bin/\(name)",
            "/usr/bin/\(name)",
        ]
        for path in candidates where FileManager.default.isExecutableFile(atPath: path) {
            return URL(fileURLWithPath: path)
        }
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/which")
        process.arguments = [name]
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = Pipe()
        do {
            try process.run()
            process.waitUntilExit()
        } catch {
            return nil
        }
        guard process.terminationStatus == 0 else {
            return nil
        }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        guard let path = String(data: data, encoding: .utf8)?
            .trimmingCharacters(in: .whitespacesAndNewlines),
            !path.isEmpty,
            FileManager.default.isExecutableFile(atPath: path)
        else {
            return nil
        }
        return URL(fileURLWithPath: path)
    }

    @discardableResult
    static func start(_ request: DesktopRecordRequest) throws -> String {
        let pidfilePath = request.pidfile ?? defaultPidfilePath()
        if let state = readState(pidfilePath: pidfilePath), processAlive(state.pid) {
            throw ComputerUseError.message(
                "recording already running (pid \(state.pid), output \(state.output)); run 'record stop' or 'record discard' first"
            )
        }

        let ffmpegURL = resolveFfmpegURL()
        let preferMP4 = ffmpegURL != nil
        let output = request.output ?? defaultOutputPath(preferMP4: preferMP4)
        let logURL = URL(fileURLWithPath: output + ".log")
        guard FileManager.default.createFile(atPath: logURL.path, contents: nil),
              let logHandle = try? FileHandle(forWritingTo: logURL)
        else {
            throw ComputerUseError.message("cannot create recording log at \(logURL.path)")
        }
        defer { try? logHandle.close() }

        let process = Process()
        let backend: String
        if let ffmpegURL {
            backend = "ffmpeg-avfoundation"
            process.executableURL = ffmpegURL
            let screenDevice = ProcessInfo.processInfo.environment["OPEN_COMPUTER_USE_AVFOUNDATION_SCREEN"]
                ?? "Capture screen 0"
            process.arguments = buildFfmpegAvfoundationArgs(
                output: output,
                fps: request.fps,
                quality: request.quality,
                drawMouse: request.drawMouse,
                screenDevice: screenDevice
            )
        } else {
            backend = "screencapture"
            let screenCaptureURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
            guard FileManager.default.isExecutableFile(atPath: screenCaptureURL.path) else {
                throw ComputerUseError.message(
                    "ffmpeg (preferred) or /usr/sbin/screencapture is required for screen recording"
                )
            }
            process.executableURL = screenCaptureURL
            process.arguments = ["-v", output]
        }

        process.standardOutput = logHandle
        process.standardError = logHandle
        do {
            try process.run()
        } catch {
            throw ComputerUseError.message("cannot start recorder (\(backend)): \(error.localizedDescription)")
        }
        let pid = process.processIdentifier
        // Deliberately not waited: the recorder keeps running after this CLI
        // exits, exactly like the Linux/Windows detached ffmpeg recorders.
        try waitRecordProcessReady(pid: pid, output: output, timeout: 3.0)

        let started = Date()
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        let pointer = DesktopCursor.current()
        let eventsPath = recordEventsPath(forOutput: output)
        try? initRecordEventLog(
            output: output,
            width: pointer.screen_width,
            height: pointer.screen_height,
            fps: request.fps,
            started: started
        )
        let state = RecordState(
            pid: pid,
            output: output,
            backend: backend,
            fps: request.fps,
            quality: request.quality,
            drawMouse: request.drawMouse,
            startedAt: formatter.string(from: started),
            autoPolish: request.autoPolish,
            eventsPath: eventsPath
        )
        do {
            try writeState(state, pidfilePath: pidfilePath)
        } catch {
            throw ComputerUseError.message("recording started (pid \(pid)) but pidfile write failed: \(error.localizedDescription)")
        }
        return "recording started: pid=\(pid) backend=\(backend) fps=\(request.fps) quality=\(request.quality) draw_mouse=\(request.drawMouse) polish=\(request.autoPolish) output=\(output)"
    }

    static func stop(pidfilePath: String?, saveAs: String?, polish: Bool) throws -> String {
        let path = pidfilePath ?? defaultPidfilePath()
        guard let state = readState(pidfilePath: path) else {
            throw ComputerUseError.message("no recording in progress (pidfile not found)")
        }
        var stopError: Error?
        if processAlive(state.pid) {
            if kill(state.pid, SIGINT) != 0 {
                if errno != ESRCH {
                    stopError = ComputerUseError.message("cannot signal recorder pid \(state.pid): \(String(cString: strerror(errno)))")
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
                    stopError = ComputerUseError.message("recorder pid \(state.pid) did not exit after stop signal")
                }
            }
        }
        try? FileManager.default.removeItem(atPath: path)
        if let stopError {
            throw ComputerUseError.message("recording output=\(state.output) but stop had a problem: \(stopError.localizedDescription)")
        }
        var finalOutput = state.output
        if let saveAs, !saveAs.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            let previous = state.output
            finalOutput = try relocateRecordOutput(current: previous, saveAs: saveAs)
            let oldEvents = recordEventsPath(forOutput: previous)
            let newEvents = recordEventsPath(forOutput: finalOutput)
            if oldEvents != newEvents, FileManager.default.fileExists(atPath: oldEvents) {
                try? FileManager.default.moveItem(atPath: oldEvents, toPath: newEvents)
            }
        }
        var message = "recording stopped: output=\(finalOutput)"
        if polish || (state.autoPolish == true) {
            let polished = defaultPolishedOutput(forRaw: finalOutput)
            let events = recordEventsPath(forOutput: finalOutput)
            do {
                try polishRecording(
                    inputVideo: finalOutput,
                    eventsPath: events,
                    outputVideo: polished,
                    opts: .default()
                )
                message += "\nrecording polished: output=\(polished)"
            } catch {
                throw ComputerUseError.message(
                    "recording stopped (\(finalOutput)) but polish failed: \(error.localizedDescription)"
                )
            }
        }
        return message
    }

    static func discard(pidfilePath: String?) throws -> String {
        let path = pidfilePath ?? defaultPidfilePath()
        guard let state = readState(pidfilePath: path) else {
            throw ComputerUseError.message("no recording in progress (pidfile not found)")
        }
        var stopError: Error?
        if processAlive(state.pid) {
            if kill(state.pid, SIGINT) != 0, errno != ESRCH {
                stopError = ComputerUseError.message("cannot signal recorder pid \(state.pid): \(String(cString: strerror(errno)))")
            } else {
                let deadline = Date().addingTimeInterval(10)
                while Date() < deadline, processAlive(state.pid) {
                    Thread.sleep(forTimeInterval: 0.1)
                }
            }
        }
        try? FileManager.default.removeItem(atPath: path)
        removeRecordSidecars(output: state.output)
        try? FileManager.default.removeItem(atPath: state.output)
        if let stopError {
            throw ComputerUseError.message(
                "recording discarded (output removed=\(state.output)) but stop had a problem: \(stopError.localizedDescription)"
            )
        }
        return "recording discarded: output=\(state.output)"
    }

    static func status(pidfilePath: String?) throws -> String {
        let path = pidfilePath ?? defaultPidfilePath()
        guard let state = readState(pidfilePath: path) else {
            return "{\n  \"running\": false\n}"
        }
        var report: [String: Any] = [
            "running": processAlive(state.pid),
            "pid": state.pid,
            "output": state.output,
        ]
        if let backend = state.backend {
            report["backend"] = backend
        }
        if let fps = state.fps {
            report["fps"] = fps
        }
        if let quality = state.quality {
            report["quality"] = quality
        }
        if let drawMouse = state.drawMouse {
            report["draw_mouse"] = drawMouse
        }
        if let startedAt = state.startedAt {
            report["started_at"] = startedAt
        }
        report["auto_polish"] = state.autoPolish ?? false
        if let eventsPath = state.eventsPath {
            report["events_path"] = eventsPath
        }
        if let attrs = try? FileManager.default.attributesOfItem(atPath: state.output),
           let size = attrs[.size] as? NSNumber
        {
            report["output_bytes"] = size.intValue
        }
        if let log = try? readRecordEventLog(path: recordEventsPath(forOutput: state.output)) {
            report["event_count"] = log.events.count
        }
        let data = try JSONSerialization.data(withJSONObject: report, options: [.prettyPrinted, .sortedKeys])
        guard let json = String(data: data, encoding: .utf8) else {
            throw ComputerUseError.message("cannot encode recording status")
        }
        return json
    }

    static func runPolish(_ request: DesktopRecordRequest) throws -> String {
        guard let input = request.polishInput, !input.isEmpty else {
            throw ComputerUseError.message("record polish requires --input <raw.mp4>")
        }
        let events = request.polishEvents ?? recordEventsPath(forOutput: input)
        let output = request.polishOutput ?? defaultPolishedOutput(forRaw: input)
        let writePlanPath = request.writePlan
            ? (request.writePlanPath ?? defaultRenderPlanPath(forRaw: input))
            : nil
        let started = Date()
        try polishRecording(
            inputVideo: input,
            eventsPath: events,
            outputVideo: output,
            opts: request.polishOptions,
            planPath: request.polishPlan,
            writePlan: request.writePlan,
            writePlanPath: writePlanPath
        )
        let elapsedMs = Int((Date().timeIntervalSince(started) * 1000.0).rounded())
        var message = "recording polished: engine=\(request.polishEngine) input=\(input) events=\(events) output=\(output) elapsed=\(elapsedMs)ms"
        if let writePlanPath {
            message += "\nrender plan written: \(writePlanPath)"
        }
        return message
    }

    static func runProxy(_ request: DesktopRecordRequest) throws -> String {
        guard let input = request.proxyInput, !input.isEmpty else {
            throw ComputerUseError.message("record proxy requires --input <raw.mp4>")
        }
        let outDir: String
        if let proxyOutputDir = request.proxyOutputDir, !proxyOutputDir.isEmpty {
            outDir = (proxyOutputDir as NSString).expandingTildeInPath
        } else {
            outDir = URL(fileURLWithPath: input).deletingLastPathComponent().appendingPathComponent("render-proxies").path
        }
        try FileManager.default.createDirectory(atPath: outDir, withIntermediateDirectories: true)
        let meta = try generateRenderProxies(
            sourceVideo: input,
            outDir: outDir,
            want1080p: request.proxyWant1080p,
            wantFull: request.proxyWantFull
        )
        return "render proxies ready: dir=\(outDir) primary=\(meta.primary1080p != nil) full=\(meta.full != nil)"
    }

    static func relocateRecordOutput(current: String, saveAs: String) throws -> String {
        var target = saveAs.trimmingCharacters(in: .whitespacesAndNewlines)
        if URL(fileURLWithPath: target).pathExtension.isEmpty {
            let fallbackExt = URL(fileURLWithPath: current).pathExtension
            target += ".\(fallbackExt.isEmpty ? "mp4" : fallbackExt)"
        }
        if !target.contains("/") && !target.hasPrefix("~") {
            target = URL(fileURLWithPath: current)
                .deletingLastPathComponent()
                .appendingPathComponent(URL(fileURLWithPath: target).lastPathComponent)
                .path
        }
        let destination = URL(fileURLWithPath: (target as NSString).expandingTildeInPath)
        try FileManager.default.createDirectory(
            at: destination.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        if FileManager.default.fileExists(atPath: destination.path) {
            try FileManager.default.removeItem(at: destination)
        }
        do {
            try FileManager.default.moveItem(atPath: current, toPath: destination.path)
        } catch {
            try FileManager.default.copyItem(atPath: current, toPath: destination.path)
            try? FileManager.default.removeItem(atPath: current)
        }
        let logSource = current + ".log"
        if FileManager.default.fileExists(atPath: logSource) {
            try? FileManager.default.moveItem(atPath: logSource, toPath: destination.path + ".log")
        }
        return destination.path
    }

    private static func waitRecordProcessReady(pid: Int32, output: String, timeout: TimeInterval) throws {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if !processAlive(pid) {
                var detail = ""
                if let data = FileManager.default.contents(atPath: output + ".log"),
                   let text = String(data: data, encoding: .utf8)?
                    .trimmingCharacters(in: .whitespacesAndNewlines),
                   !text.isEmpty
                {
                    detail = ": " + String(text.prefix(400))
                }
                throw ComputerUseError.message("recorder exited before recording became ready\(detail)")
            }
            if let attrs = try? FileManager.default.attributesOfItem(atPath: output),
               let size = attrs[.size] as? NSNumber,
               size.intValue > 0
            {
                return
            }
            Thread.sleep(forTimeInterval: 0.1)
        }
        if processAlive(pid) {
            return
        }
        throw ComputerUseError.message("recorder exited before recording became ready")
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

    /// Visible to event-append helper without exposing private processAlive.
    static func readStateForEvents(pidfilePath: String) -> RecordState? {
        readState(pidfilePath: pidfilePath)
    }

    static func isProcessAlive(_ pid: Int32) -> Bool {
        processAlive(pid)
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
