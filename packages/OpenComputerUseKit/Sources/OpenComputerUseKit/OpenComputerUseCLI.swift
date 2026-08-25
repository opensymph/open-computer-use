import Foundation

public enum OpenComputerUseCLICommand: Equatable {
    case launchOnboarding
    case mcp
    case doctor
    case listApps
    case snapshot(app: String, textLimit: SnapshotTextLimit = .defaults, treeLimits: AccessibilityTreeLimits = .defaults)
    case call(OpenComputerUseCallInvocation)
    case turnEnded(payload: String?)
    case screenshot(output: String?)
    case cursorPosition
    case input(DesktopInputCommand)
    case record(DesktopRecordRequest)
    case help(command: String?)
    case version
}

public enum OpenComputerUseCallInvocation: Equatable {
    case single(toolName: String, argumentsJSON: String?, argumentsFile: String?)
    case sequence(callsJSON: String?, callsFile: String?, interCallDelay: TimeInterval)
}

public let openComputerUseDefaultInterCallDelay: TimeInterval = 1

public func shouldUseMacOSAppAgentProxy(
    command: OpenComputerUseCLICommand,
    proxyDisabled: Bool,
    appBundleAvailable: Bool,
    runningFromLaunchServicesAppInstance: Bool
) -> Bool {
    guard !proxyDisabled, appBundleAvailable else {
        return false
    }

        switch command {
        case .launchOnboarding:
            return !runningFromLaunchServicesAppInstance
        case .mcp, .doctor, .listApps, .snapshot, .call:
            return true
        case .screenshot, .cursorPosition, .input, .record:
            // Display-level commands ride the app agent too: the Screen
            // Recording / Accessibility TCC grants belong to the app bundle.
            return true
        case .turnEnded, .help, .version:
            return false
        }
}

public struct OpenComputerUseCLIError: LocalizedError, Equatable {
    public let message: String
    public let helpCommand: String?

    public init(message: String, helpCommand: String? = nil) {
        self.message = message
        self.helpCommand = helpCommand
    }

    public var errorDescription: String? {
        var lines = [message]
        lines.append("")
        lines.append(openComputerUseHelpText(command: helpCommand))
        return lines.joined(separator: "\n")
    }
}

public func parseOpenComputerUseCLI(arguments: [String]) throws -> OpenComputerUseCLICommand {
    guard let first = arguments.first else {
        return .launchOnboarding
    }

    switch first {
    case "-h", "--help", "help":
        if arguments.count > 2 {
            throw OpenComputerUseCLIError(message: "help accepts at most one command", helpCommand: nil)
        }

        return .help(command: arguments.dropFirst().first)
    case "-v", "--version", "version":
        guard arguments.count == 1 else {
            throw OpenComputerUseCLIError(message: "version does not accept any arguments", helpCommand: nil)
        }

        return .version
    case "mcp":
        return try parseSimpleCommand(name: "mcp", arguments: Array(arguments.dropFirst()), result: .mcp)
    case "doctor":
        return try parseSimpleCommand(name: "doctor", arguments: Array(arguments.dropFirst()), result: .doctor)
    case "list-apps":
        return try parseSimpleCommand(name: "list-apps", arguments: Array(arguments.dropFirst()), result: .listApps)
    case "call":
        return try parseCall(arguments: Array(arguments.dropFirst()))
    case "turn-ended":
        return try parseTurnEnded(arguments: Array(arguments.dropFirst()))
    case "snapshot":
        return try parseSnapshot(arguments: Array(arguments.dropFirst()))
    case "screenshot":
        return try parseScreenshot(arguments: Array(arguments.dropFirst()))
    case "cursor-position":
        return try parseSimpleCommand(name: "cursor-position", arguments: Array(arguments.dropFirst()), result: .cursorPosition)
    case "input":
        return try parseDesktopInput(arguments: Array(arguments.dropFirst()))
    case "record":
        return try parseDesktopRecord(arguments: Array(arguments.dropFirst()))
    default:
        if first.hasPrefix("-") {
            throw OpenComputerUseCLIError(message: "Unknown option: \(first)", helpCommand: nil)
        }

        throw OpenComputerUseCLIError(message: "Unknown command: \(first)", helpCommand: nil)
    }
}

public func openComputerUseHelpText(command: String? = nil) -> String {
    switch command {
    case nil:
        return """
        Open Computer Use

        Usage:
          open-computer-use [command] [options]
          open-computer-use

        Commands:
          mcp                  Start the stdio MCP server.
          doctor               Print permission status and launch onboarding if needed.
          list-apps            Print running or recently used apps.
          snapshot <app>       Print the current accessibility snapshot for an app.
          call <tool>           Call one tool, or run a JSON array of tool calls.
          turn-ended           Notify the running MCP process that the host turn ended.
          screenshot           Capture the whole desktop to PNG (Screen Recording permission).
          cursor-position      Print the pointer position and desktop size as JSON.
          input <action>       Global CGEvent input: move/click/drag/scroll/type/key/wait/mouse_down/mouse_up.
          record <start|stop|discard|polish|proxy|status>  Record screen; polish overlays via ffmpeg+ASS.
          help [command]       Show general or command-specific help.
          version              Print the CLI version.

        Global options:
          -h, --help           Show help.
          -v, --version        Show version.

        Notes:
          Running without a command launches the permission onboarding app.
          Use `open-computer-use help <command>` for command-specific help.
          The screenshot/cursor-position/input/record commands operate on the
          whole desktop; input requires OPEN_COMPUTER_USE_MACOS_ALLOW_FOREGROUND_INPUT=1
          and the Accessibility permission, screenshot requires Screen Recording.
        """
    case "mcp":
        return """
        Usage:
          open-computer-use mcp

        Start the stdio MCP server.
        """
    case "doctor":
        return """
        Usage:
          open-computer-use doctor

        Print the current Accessibility and Screen Recording permission state.
        If permissions are missing, this also launches the onboarding app.
        """
    case "list-apps":
        return """
        Usage:
          open-computer-use list-apps

        Print running apps plus recently used apps that can be targeted by Computer Use.
        """
    case "snapshot":
        return """
        Usage:
          open-computer-use snapshot [--text-limit <positive-int|max>] [--max-tree-nodes <positive-int>] [--max-tree-depth <positive-int>] <app>

        Arguments:
          <app>                App name or bundle identifier to inspect.

        Options:
          --text-limit         Override the default 500 character text limit. Use `max` for full text.
          --max-tree-nodes     Override the default 1200 node accessibility tree budget.
          --max-tree-depth     Override the default 64 level accessibility tree depth.

        Print the current accessibility snapshot for the target app.
        """
    case "call":
        return """
        Usage:
          open-computer-use call <tool> [--args '<json-object>']
          open-computer-use call <tool> [--args-file <path>]
          open-computer-use call --calls '<json-array>' [--sleep <seconds>]
          open-computer-use call --calls-file <path> [--sleep <seconds>]

        Examples:
          open-computer-use call list_apps
          open-computer-use call get_app_state --args '{"app":"TextEdit"}'
          open-computer-use call --calls '[{"tool":"get_app_state","args":{"app":"TextEdit"}},{"tool":"press_key","args":{"app":"TextEdit","key":"Return"}}]'
          open-computer-use call --calls-file examples/textedit-overlay-seq.json --sleep 0.5

        The JSON array form keeps all calls in one process so follow-up actions
        can reuse the app state and element indices captured by get_app_state.
        Sequence execution stops after the first tool result with isError=true.
        Sequence runs sleep \(formatOpenComputerUseDelay(openComputerUseDefaultInterCallDelay)) between successful operations by default.
        """
    case "turn-ended":
        return """
        Usage:
          open-computer-use turn-ended [--previous-notify <argv>] [payload]

        Notify a running local MCP process that the current host turn has ended.
        Codex legacy notify appends the after-agent JSON payload as the last argument.
        """
    case "screenshot":
        return """
        Usage:
          open-computer-use screenshot [--output <path.png>]

        Capture the whole desktop (all displays) to PNG. With --output the PNG
        is written to that path; otherwise base64 PNG is printed. Requires the
        Screen Recording permission (run `open-computer-use doctor` to grant).
        """
    case "cursor-position":
        return """
        Usage:
          open-computer-use cursor-position

        Print the pointer position (top-left-origin desktop coordinates) and the
        desktop size as JSON, mirroring the Linux/Windows runtimes' output.
        """
    case "input":
        return """
        Usage:
          open-computer-use input [--api-size WxH] <action> [options]

        Actions (global synthetic input via CGEvent):
          move <x> <y>
          click [--button left|right|middle] [--count N] [--modifiers ctrl+shift] [--x X --y Y]
          mouse_down|mouse_up [--button left|right|middle] [--modifiers ...] [--x X --y Y]
          drag <from_x> <from_y> <to_x> <to_y> [--button left]
          scroll <up|down|left|right> [--amount N] [--modifiers ...] [--x X --y Y]
          type <text>                 newlines become Return; long text is batched (~50 chars)
          key <key-or-chord> [--hold-ms N]   e.g. ctrl+s, Return, Page_Up
          wait <seconds>

        --api-size (or OPEN_COMPUTER_USE_API_SIZE / WIDTH+HEIGHT) maps model/API
        coordinates to the display when sizes differ. Every action except wait moves
        the real pointer/keyboard and requires OPEN_COMPUTER_USE_MACOS_ALLOW_FOREGROUND_INPUT=1
        plus the Accessibility permission (run `open-computer-use doctor` to grant).
        """
    case "record":
        return """
        Usage:
          open-computer-use record start [--output <path.mp4|.mov>] [--fps N]
                                         [--quality demo|draft|proxy|anyos] [--draw-mouse 0|1] [--polish] [--pidfile <path>]
          open-computer-use record stop  [--pidfile <path>] [--save-as <name-or-path>] [--polish]
          open-computer-use record discard [--pidfile <path>]
          open-computer-use record polish --input <raw.mp4> [--events <file>] [--output <polished.mp4>]
                                         [--plan <render-plan.json>] [--write-plan <file>] [--no-write-plan]
                                         [--engine compositor|ffmpeg] [--cursor-style slow|mellow|quick|rapid]
                                         [--ripples] [--no-ripples] [--no-keystrokes] [--no-cursor]
                                         [--no-idle-speedup] [--no-zoom]
          open-computer-use record proxy --input <raw.mp4> [--output-dir <dir>] [--1080p] [--full] [--no-1080p] [--no-full]
          open-computer-use record status [--pidfile <path>]

        Record the screen. Prefers ffmpeg avfoundation when ffmpeg is on PATH
        (same demo-quality H.264 mp4 encode as Linux/Windows / Cursor RecordScreen:
        veryfast + crf 17 + High + faststart; proxy/anyos use all-intra keyint=1).
        Falls back to /usr/sbin/screencapture -v (.mov; --fps/--quality/--draw-mouse ignored).
        While recording, display `input` actions append <output>.events.json after each action
        completes. `record polish` uses the macOS ffmpeg+ASS path (accepts `--engine` and
        `--plan` for CLI parity; writes simplified `<stem>.render-plan.json` unless
        `--no-write-plan`). `record proxy` shells ffmpeg to produce proxy-1080p.mp4 and
        proxy-full.mp4 (crf 17, veryfast, keyint=1). Overlays: cursor ghost, keystroke
        captions, optional --ripples. --polish on start defaults draw-mouse to 0. discard
        stops and deletes the output plus sidecars. Defaults: fps 30, quality demo,
        draw-mouse 1, output in $TMPDIR, pidfile $TMPDIR/open-computer-use-record.pid.
        Requires Screen Recording permission (run `open-computer-use doctor` to grant).
        Override the avfoundation device with OPEN_COMPUTER_USE_AVFOUNDATION_SCREEN.
        """
    case "version":
        return """
        Usage:
          open-computer-use version
          open-computer-use --version
          open-computer-use -v

        Print the CLI version.
        """
    case "help":
        return """
        Usage:
          open-computer-use help [command]

        Show general help or help for a specific command.
        """
    default:
        return """
        Unknown help topic: \(command ?? "")

        \(openComputerUseHelpText())
        """
    }
}

private func parseSimpleCommand(
    name: String,
    arguments: [String],
    result: OpenComputerUseCLICommand
) throws -> OpenComputerUseCLICommand {
    if arguments.isEmpty {
        return result
    }

    if arguments.count == 1, let option = arguments.first, option == "-h" || option == "--help" {
        return .help(command: name)
    }

    throw OpenComputerUseCLIError(message: "\(name) does not accept any arguments", helpCommand: name)
}

private func parseTurnEnded(arguments: [String]) throws -> OpenComputerUseCLICommand {
    if arguments.count == 1, let option = arguments.first, option == "-h" || option == "--help" {
        return .help(command: "turn-ended")
    }

    var payload: String?
    var index = 0
    while index < arguments.count {
        let argument = arguments[index]

        switch argument {
        case "--previous-notify":
            let valueIndex = index + 1
            guard valueIndex < arguments.count else {
                throw OpenComputerUseCLIError(message: "--previous-notify requires a value", helpCommand: "turn-ended")
            }
            index = valueIndex
        case "-h", "--help":
            throw OpenComputerUseCLIError(message: "turn-ended help must be requested as `open-computer-use turn-ended --help`", helpCommand: "turn-ended")
        default:
            if argument.hasPrefix("-") {
                throw OpenComputerUseCLIError(message: "Unknown turn-ended option: \(argument)", helpCommand: "turn-ended")
            }

            guard payload == nil else {
                throw OpenComputerUseCLIError(message: "turn-ended accepts at most one payload argument", helpCommand: "turn-ended")
            }

            payload = argument
        }

        index += 1
    }

    return .turnEnded(payload: payload)
}

private func parseSnapshot(arguments: [String]) throws -> OpenComputerUseCLICommand {
    if arguments.isEmpty {
        throw OpenComputerUseCLIError(message: "snapshot requires an app name or bundle identifier", helpCommand: "snapshot")
    }

    if arguments.count == 1, let value = arguments.first, value == "-h" || value == "--help" {
        return .help(command: "snapshot")
    }

    var app: String?
    var textLimit = SnapshotTextLimit.defaults
    var maxTreeNodes: Int?
    var maxTreeDepth: Int?

    var index = 0
    while index < arguments.count {
        let argument = arguments[index]
        switch argument {
        case "--text-limit":
            index += 1
            guard index < arguments.count else {
                throw OpenComputerUseCLIError(message: "--text-limit requires a positive integer or max value", helpCommand: "snapshot")
            }
            textLimit = try parseTextLimitOption(arguments[index], option: "--text-limit")
        case "--max-tree-nodes":
            index += 1
            guard index < arguments.count else {
                throw OpenComputerUseCLIError(message: "--max-tree-nodes requires a positive integer value", helpCommand: "snapshot")
            }
            maxTreeNodes = try parsePositiveIntegerOption(arguments[index], option: "--max-tree-nodes")
        case "--max-tree-depth":
            index += 1
            guard index < arguments.count else {
                throw OpenComputerUseCLIError(message: "--max-tree-depth requires a positive integer value", helpCommand: "snapshot")
            }
            maxTreeDepth = try parsePositiveIntegerOption(arguments[index], option: "--max-tree-depth")
        case "-h", "--help":
            throw OpenComputerUseCLIError(message: "snapshot help must be requested as `open-computer-use snapshot --help`", helpCommand: "snapshot")
        default:
            if argument.hasPrefix("-") {
                throw OpenComputerUseCLIError(message: "Unknown snapshot option: \(argument)", helpCommand: "snapshot")
            }

            guard app == nil else {
                throw OpenComputerUseCLIError(message: "snapshot accepts exactly one <app> argument", helpCommand: "snapshot")
            }

            app = argument
        }
        index += 1
    }

    guard let app else {
        throw OpenComputerUseCLIError(message: "snapshot requires an app name or bundle identifier", helpCommand: "snapshot")
    }

    return .snapshot(
        app: app,
        textLimit: textLimit,
        treeLimits: AccessibilityTreeLimits.defaults.replacing(
            maxNodeCount: maxTreeNodes,
            maxDepth: maxTreeDepth
        )
    )
}

private func parseScreenshot(arguments: [String]) throws -> OpenComputerUseCLICommand {
    if arguments.count == 1, let option = arguments.first, option == "-h" || option == "--help" {
        return .help(command: "screenshot")
    }

    var output: String?
    var index = 0
    while index < arguments.count {
        let argument = arguments[index]
        switch argument {
        case "--output", "-o":
            index += 1
            guard index < arguments.count else {
                throw OpenComputerUseCLIError(message: "--output requires a value", helpCommand: "screenshot")
            }
            output = arguments[index]
        default:
            throw OpenComputerUseCLIError(message: "unknown screenshot option: \(argument)", helpCommand: "screenshot")
        }
        index += 1
    }
    return .screenshot(output: output)
}

private func parseDesktopInput(arguments: [String]) throws -> OpenComputerUseCLICommand {
    if arguments.count == 1, let option = arguments.first, option == "-h" || option == "--help" {
        return .help(command: "input")
    }
    return .input(try parseDesktopInputCommand(arguments))
}

private func parseDesktopRecord(arguments: [String]) throws -> OpenComputerUseCLICommand {
    if arguments.count == 1, let option = arguments.first, option == "-h" || option == "--help" {
        return .help(command: "record")
    }
    return .record(try parseDesktopRecordArguments(arguments))
}

private func parseTextLimitOption(_ value: String, option: String) throws -> SnapshotTextLimit {
    if value.lowercased() == SnapshotTextLimit.maxKeyword {
        return .max
    }

    guard let integer = Int(value), integer > 0 else {
        throw OpenComputerUseCLIError(message: "\(option) must be a positive integer or max", helpCommand: "snapshot")
    }
    return SnapshotTextLimit(maxCount: integer)
}

private func parsePositiveIntegerOption(_ value: String, option: String) throws -> Int {
    guard let integer = Int(value), integer > 0 else {
        throw OpenComputerUseCLIError(message: "\(option) must be a positive integer", helpCommand: "snapshot")
    }
    return integer
}

private func parseCall(arguments: [String]) throws -> OpenComputerUseCLICommand {
    if arguments.count == 1, let option = arguments.first, option == "-h" || option == "--help" {
        return .help(command: "call")
    }

    var toolName: String?
    var argumentsJSON: String?
    var argumentsFile: String?
    var callsJSON: String?
    var callsFile: String?
    var interCallDelay = openComputerUseDefaultInterCallDelay

    var index = 0
    while index < arguments.count {
        let argument = arguments[index]

        switch argument {
        case "--args":
            argumentsJSON = try parseOptionValue("--args", arguments: arguments, index: &index)
        case "--args-file":
            argumentsFile = try parseOptionValue("--args-file", arguments: arguments, index: &index)
        case "--calls":
            callsJSON = try parseOptionValue("--calls", arguments: arguments, index: &index)
        case "--calls-file":
            callsFile = try parseOptionValue("--calls-file", arguments: arguments, index: &index)
        case "--sleep":
            interCallDelay = try parseTimeIntervalOptionValue("--sleep", arguments: arguments, index: &index)
        case "-h", "--help":
            throw OpenComputerUseCLIError(message: "call help must be requested as `open-computer-use call --help`", helpCommand: "call")
        default:
            if argument.hasPrefix("-") {
                throw OpenComputerUseCLIError(message: "Unknown call option: \(argument)", helpCommand: "call")
            }

            guard toolName == nil else {
                throw OpenComputerUseCLIError(message: "call accepts at most one tool name", helpCommand: "call")
            }

            toolName = argument
        }

        index += 1
    }

    let hasSequenceInput = callsJSON != nil || callsFile != nil
    if hasSequenceInput {
        if callsJSON != nil, callsFile != nil {
            throw OpenComputerUseCLIError(message: "Use either --calls or --calls-file, not both", helpCommand: "call")
        }

        if toolName != nil || argumentsJSON != nil || argumentsFile != nil {
            throw OpenComputerUseCLIError(
                message: "call sequence does not accept a tool name, --args, or --args-file",
                helpCommand: "call"
            )
        }

        return .call(.sequence(
            callsJSON: callsJSON,
            callsFile: callsFile,
            interCallDelay: interCallDelay
        ))
    }

    if argumentsJSON != nil, argumentsFile != nil {
        throw OpenComputerUseCLIError(message: "Use either --args or --args-file, not both", helpCommand: "call")
    }

    if interCallDelay != openComputerUseDefaultInterCallDelay {
        throw OpenComputerUseCLIError(
            message: "--sleep is only supported with --calls or --calls-file",
            helpCommand: "call"
        )
    }

    guard let toolName else {
        throw OpenComputerUseCLIError(message: "call requires a tool name or --calls/--calls-file", helpCommand: "call")
    }

    return .call(.single(toolName: toolName, argumentsJSON: argumentsJSON, argumentsFile: argumentsFile))
}

private func parseOptionValue(
    _ option: String,
    arguments: [String],
    index: inout Int
) throws -> String {
    let valueIndex = index + 1
    guard valueIndex < arguments.count else {
        throw OpenComputerUseCLIError(message: "\(option) requires a value", helpCommand: "call")
    }

    index = valueIndex
    return arguments[valueIndex]
}

private func parseTimeIntervalOptionValue(
    _ option: String,
    arguments: [String],
    index: inout Int
) throws -> TimeInterval {
    let rawValue = try parseOptionValue(option, arguments: arguments, index: &index)
    guard let value = Double(rawValue), value.isFinite, value >= 0 else {
        throw OpenComputerUseCLIError(
            message: "\(option) requires a non-negative number of seconds",
            helpCommand: "call"
        )
    }

    return value
}

private func formatOpenComputerUseDelay(_ delay: TimeInterval) -> String {
    if delay.rounded() == delay {
        return "\(Int(delay))s"
    }

    return "\(delay)s"
}
