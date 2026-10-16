import Foundation

func normalizedElementIndexArgument(_ value: Any?) -> String? {
    if let string = value as? String {
        return string.isEmpty ? nil : string
    }

    if let integer = value as? Int {
        return String(integer)
    }

    if let number = value as? NSNumber {
        if CFGetTypeID(number as CFTypeRef) == CFBooleanGetTypeID() {
            return nil
        }

        return normalizedElementIndexNumber(number.doubleValue)
    }

    if let double = value as? Double {
        return normalizedElementIndexNumber(double)
    }

    return nil
}

private func normalizedElementIndexNumber(_ value: Double) -> String? {
    guard value.isFinite, value.rounded(.towardZero) == value else {
        return nil
    }

    guard value >= Double(Int.min), value <= Double(Int.max) else {
        return nil
    }

    return String(Int(value))
}

// Service-layer safety ceilings: clamp absurd click_count/pages instead of
// looping input injection or scrolling for effectively unbounded time.
let maxToolClickCount = 100
let maxToolScrollPages = 1000.0

func clampedClickCount(_ rawValue: Double?) -> Int {
    let value = Int(rawValue ?? 1)
    return min(max(value, 1), maxToolClickCount)
}

func clampedScrollPages(_ value: Double) -> Double {
    min(value, maxToolScrollPages)
}

public final class ComputerUseToolDispatcher {
    private let service: ComputerUseService

    public init(service: ComputerUseService = ComputerUseService()) {
        self.service = service
    }

    // Official window2 action targeting: the `window` object (or the flat
    // `window_id` alias) takes precedence over the legacy `app` argument;
    // with neither present the unified missing-argument error applies.
    private enum ActionTarget {
        case window(WindowRef)
        case app(String)
    }

    private func actionTarget(_ arguments: [String: Any]) throws -> ActionTarget {
        if let window = try WindowArgumentParsing.optional(arguments) {
            return .window(window)
        }

        if let app = optionalString("app", in: arguments), !app.isEmpty {
            return .app(app)
        }

        throw ComputerUseError.message("Missing required argument: provide either window (from list_windows/get_window_state) or app")
    }

    private func optionalBool(_ key: String, in arguments: [String: Any]) throws -> Bool? {
        guard let value = arguments[key] else {
            return nil
        }

        if let bool = value as? Bool {
            return bool
        }

        if let number = value as? NSNumber, CFGetTypeID(number as CFTypeRef) == CFBooleanGetTypeID() {
            return number.boolValue
        }

        throw ComputerUseError.message("\(key) must be a boolean")
    }

    private func isCoordinateScroll(_ arguments: [String: Any]) -> Bool {
        optionalDouble("scrollX", in: arguments) != nil || optionalDouble("scrollY", in: arguments) != nil
    }

    private func coordinateScrollArguments(_ arguments: [String: Any]) throws -> (x: Double, y: Double, scrollX: Double, scrollY: Double) {
        guard let x = optionalDouble("x", in: arguments), let y = optionalDouble("y", in: arguments) else {
            throw ComputerUseError.message("coordinate scroll requires both x and y (window-relative)")
        }

        let scrollX = optionalDouble("scrollX", in: arguments) ?? 0
        let scrollY = optionalDouble("scrollY", in: arguments) ?? 0
        guard scrollX != 0 || scrollY != 0 else {
            throw ComputerUseError.message("coordinate scroll requires a non-zero scrollX or scrollY pixel delta")
        }

        return (x, y, scrollX, scrollY)
    }

    public func callTool(name: String, arguments: [String: Any]) throws -> ToolCallResult {
        switch name {
        case "list_windows":
            return try service.listWindows()
        case "get_window":
            guard let window = try WindowArgumentParsing.required(arguments) else {
                throw ComputerUseError.message("Missing required argument: window.id")
            }
            return try service.getWindow(window: window)
        case "get_window_state":
            guard let window = try WindowArgumentParsing.required(arguments) else {
                throw ComputerUseError.message("Missing required argument: window")
            }
            let includeScreenshot = try optionalBool("include_screenshot", in: arguments) ?? true
            let includeText = try optionalBool("include_text", in: arguments) ?? false
            guard includeScreenshot || includeText else {
                throw ComputerUseError.message("get_window_state must request include_text, include_screenshot, or both")
            }
            return try service.getWindowState(
                window: window,
                includeScreenshot: includeScreenshot,
                includeText: includeText,
                textLimit: try optionalTextLimit("text_limit", in: arguments) ?? .defaults,
                treeLimits: AccessibilityTreeLimits.defaults.replacing(
                    maxNodeCount: try optionalPositiveInt("max_tree_nodes", in: arguments),
                    maxDepth: try optionalPositiveInt("max_tree_depth", in: arguments)
                )
            )
        case "activate_window":
            guard let window = try WindowArgumentParsing.required(arguments) else {
                throw ComputerUseError.message("Missing required argument: window.id")
            }
            return try service.activateWindow(window: window)
        case "launch_app":
            return try service.launchApp(app: requireString("app", in: arguments))
        case "list_apps":
            return service.listApps()
        case "get_app_state":
            return try service.getAppState(
                app: requireString("app", in: arguments),
                textLimit: try optionalTextLimit("text_limit", in: arguments) ?? .defaults,
                treeLimits: AccessibilityTreeLimits.defaults.replacing(
                    maxNodeCount: try optionalPositiveInt("max_tree_nodes", in: arguments),
                    maxDepth: try optionalPositiveInt("max_tree_depth", in: arguments)
                )
            )
        case "click":
            let clickMethod = try parseClickMethod(optionalString("click_method", in: arguments))
            let mouseButton = optionalString("mouse_button", in: arguments) ?? "left"
            _ = try mouseButtonKindForToolArgument(mouseButton)
            let screenshotID = optionalString("screenshotId", in: arguments)

            switch try actionTarget(arguments) {
            case let .window(window):
                return try service.click(
                    window: window,
                    elementIndex: optionalElementIndex(in: arguments),
                    x: optionalDouble("x", in: arguments),
                    y: optionalDouble("y", in: arguments),
                    clickCount: clampedClickCount(optionalDouble("click_count", in: arguments)),
                    mouseButton: mouseButton,
                    clickMethod: clickMethod,
                    screenshotID: screenshotID
                )
            case let .app(app):
                return try service.click(
                    app: app,
                    elementIndex: optionalElementIndex(in: arguments),
                    x: optionalDouble("x", in: arguments),
                    y: optionalDouble("y", in: arguments),
                    clickCount: clampedClickCount(optionalDouble("click_count", in: arguments)),
                    mouseButton: mouseButton,
                    clickMethod: clickMethod,
                    screenshotID: screenshotID
                )
            }
        case "perform_secondary_action":
            switch try actionTarget(arguments) {
            case let .window(window):
                return try service.performSecondaryAction(
                    window: window,
                    elementIndex: requireElementIndex(in: arguments),
                    action: requireString("action", in: arguments)
                )
            case let .app(app):
                return try service.performSecondaryAction(
                    app: app,
                    elementIndex: requireElementIndex(in: arguments),
                    action: requireString("action", in: arguments)
                )
            }
        case "scroll":
            switch try actionTarget(arguments) {
            case let .window(window):
                if isCoordinateScroll(arguments) {
                    let (x, y, scrollX, scrollY) = try coordinateScrollArguments(arguments)
                    return try service.scroll(
                        window: window,
                        x: x,
                        y: y,
                        scrollX: scrollX,
                        scrollY: scrollY,
                        screenshotID: optionalString("screenshotId", in: arguments)
                    )
                }

                guard let direction = optionalString("direction", in: arguments), !direction.isEmpty else {
                    throw ComputerUseError.message("Missing required argument: direction")
                }
                guard let elementIndex = optionalElementIndex(in: arguments) else {
                    throw ComputerUseError.message("scroll requires either element_index + direction (page mode) or x/y + scrollX/scrollY (coordinate mode)")
                }
                return try service.scroll(
                    window: window,
                    direction: direction,
                    elementIndex: elementIndex,
                    pages: clampedScrollPages(optionalDouble("pages", in: arguments) ?? 1)
                )
            case let .app(app):
                if isCoordinateScroll(arguments) {
                    let (x, y, scrollX, scrollY) = try coordinateScrollArguments(arguments)
                    return try service.scroll(
                        app: app,
                        x: x,
                        y: y,
                        scrollX: scrollX,
                        scrollY: scrollY,
                        screenshotID: optionalString("screenshotId", in: arguments)
                    )
                }

                return try service.scroll(
                    app: app,
                    direction: requireString("direction", in: arguments),
                    elementIndex: requireElementIndex(in: arguments),
                    pages: clampedScrollPages(optionalDouble("pages", in: arguments) ?? 1)
                )
            }
        case "drag":
            let screenshotID = optionalString("screenshotId", in: arguments)

            switch try actionTarget(arguments) {
            case let .window(window):
                return try service.drag(
                    window: window,
                    fromX: requireDouble("from_x", in: arguments),
                    fromY: requireDouble("from_y", in: arguments),
                    toX: requireDouble("to_x", in: arguments),
                    toY: requireDouble("to_y", in: arguments),
                    screenshotID: screenshotID
                )
            case let .app(app):
                return try service.drag(
                    app: app,
                    fromX: requireDouble("from_x", in: arguments),
                    fromY: requireDouble("from_y", in: arguments),
                    toX: requireDouble("to_x", in: arguments),
                    toY: requireDouble("to_y", in: arguments),
                    screenshotID: screenshotID
                )
            }
        case "type_text":
            switch try actionTarget(arguments) {
            case let .window(window):
                return try service.typeText(window: window, text: requireString("text", in: arguments))
            case let .app(app):
                return try service.typeText(app: app, text: requireString("text", in: arguments))
            }
        case "press_key":
            switch try actionTarget(arguments) {
            case let .window(window):
                return try service.pressKey(window: window, key: requireString("key", in: arguments))
            case let .app(app):
                return try service.pressKey(app: app, key: requireString("key", in: arguments))
            }
        case "set_value":
            switch try actionTarget(arguments) {
            case let .window(window):
                return try service.setValue(
                    window: window,
                    elementIndex: requireElementIndex(in: arguments),
                    value: requireString("value", in: arguments)
                )
            case let .app(app):
                return try service.setValue(
                    app: app,
                    elementIndex: requireElementIndex(in: arguments),
                    value: requireString("value", in: arguments)
                )
            }
        default:
            throw ComputerUseError.unsupportedTool(name)
        }
    }

    public func callToolAsResult(name: String, arguments: [String: Any]) -> ToolCallResult {
        do {
            return try callTool(name: name, arguments: arguments)
        } catch let error as ComputerUseError {
            return ToolCallResult.text(
                error.errorDescription ?? String(describing: error),
                isError: error.toolResultIsError
            )
        } catch {
            let message = (error as? LocalizedError)?.errorDescription ?? String(describing: error)
            return ToolCallResult.text(message, isError: true)
        }
    }

    private func requireString(_ key: String, in arguments: [String: Any]) throws -> String {
        guard let value = arguments[key] as? String, !value.isEmpty else {
            throw ComputerUseError.missingArgument(key)
        }

        return value
    }

    private func optionalString(_ key: String, in arguments: [String: Any]) -> String? {
        arguments[key] as? String
    }

    private func optionalTextLimit(_ key: String, in arguments: [String: Any]) throws -> SnapshotTextLimit? {
        guard let value = arguments[key] else {
            return nil
        }

        if let string = value as? String {
            guard string.lowercased() == SnapshotTextLimit.maxKeyword else {
                throw ComputerUseError.invalidArguments("\(key) must be a positive integer or max")
            }
            return .max
        }

        let maxCount = try positiveInt(from: value, key: key, expectedDescription: "a positive integer or max")
        return SnapshotTextLimit(maxCount: maxCount)
    }

    private func requireElementIndex(in arguments: [String: Any]) throws -> String {
        guard let value = optionalElementIndex(in: arguments) else {
            throw ComputerUseError.missingArgument("element_index")
        }

        return value
    }

    private func optionalElementIndex(in arguments: [String: Any]) -> String? {
        normalizedElementIndexArgument(arguments["element_index"])
    }

    private func requireDouble(_ key: String, in arguments: [String: Any]) throws -> Double {
        guard let value = optionalDouble(key, in: arguments) else {
            throw ComputerUseError.missingArgument(key)
        }

        return value
    }

    private func optionalDouble(_ key: String, in arguments: [String: Any]) -> Double? {
        if let double = arguments[key] as? Double {
            return double
        }

        if let integer = arguments[key] as? Int {
            return Double(integer)
        }

        if let number = arguments[key] as? NSNumber {
            return number.doubleValue
        }

        return nil
    }

    private func optionalPositiveInt(_ key: String, in arguments: [String: Any]) throws -> Int? {
        guard let value = arguments[key] else {
            return nil
        }

        return try positiveInt(from: value, key: key, expectedDescription: "a positive integer")
    }

    private func positiveInt(from value: Any, key: String, expectedDescription: String) throws -> Int {
        if let integer = value as? Int {
            return try validatePositiveInt(integer, key: key, expectedDescription: expectedDescription)
        }

        if let double = value as? Double {
            return try validatePositiveWholeNumber(double, key: key, expectedDescription: expectedDescription)
        }

        if let number = value as? NSNumber {
            if CFGetTypeID(number as CFTypeRef) == CFBooleanGetTypeID() {
                throw ComputerUseError.invalidArguments("\(key) must be \(expectedDescription)")
            }
            return try validatePositiveWholeNumber(number.doubleValue, key: key, expectedDescription: expectedDescription)
        }

        throw ComputerUseError.invalidArguments("\(key) must be \(expectedDescription)")
    }

    private func validatePositiveWholeNumber(_ value: Double, key: String, expectedDescription: String) throws -> Int {
        guard value.isFinite, value.rounded(.towardZero) == value else {
            throw ComputerUseError.invalidArguments("\(key) must be \(expectedDescription)")
        }

        guard value >= Double(Int.min), value <= Double(Int.max) else {
            throw ComputerUseError.invalidArguments("\(key) is outside the supported integer range")
        }

        return try validatePositiveInt(Int(value), key: key, expectedDescription: expectedDescription)
    }

    private func validatePositiveInt(_ value: Int, key: String, expectedDescription: String) throws -> Int {
        guard value > 0 else {
            throw ComputerUseError.invalidArguments("\(key) must be \(expectedDescription)")
        }
        return value
    }
}

public struct OpenComputerUseCallSpec {
    public let tool: String
    public let arguments: [String: Any]

    public init(tool: String, arguments: [String: Any]) {
        self.tool = tool
        self.arguments = arguments
    }
}

public struct OpenComputerUseCallOutput {
    public let jsonObject: Any
    public let hasToolError: Bool

    public init(jsonObject: Any, hasToolError: Bool) {
        self.jsonObject = jsonObject
        self.hasToolError = hasToolError
    }

    public func jsonText() throws -> String {
        let data = try JSONSerialization.data(
            withJSONObject: jsonObject,
            options: [.prettyPrinted, .withoutEscapingSlashes]
        )
        guard let text = String(data: data, encoding: .utf8) else {
            throw ComputerUseError.message("Failed to encode call output as JSON.")
        }
        return text
    }
}

public typealias OpenComputerUseSleepHandler = (TimeInterval) -> Void

public func runOpenComputerUseCall(
    _ invocation: OpenComputerUseCallInvocation,
    service: ComputerUseService = ComputerUseService(),
    sleepHandler: OpenComputerUseSleepHandler = { Thread.sleep(forTimeInterval: $0) }
) throws -> OpenComputerUseCallOutput {
    let dispatcher = ComputerUseToolDispatcher(service: service)

    switch invocation {
    case let .single(toolName, argumentsJSON, argumentsFile):
        let arguments = try readOpenComputerUseToolArguments(
            json: argumentsJSON,
            file: argumentsFile
        )
        let result = dispatcher.callToolAsResult(name: toolName, arguments: arguments)
        return OpenComputerUseCallOutput(
            jsonObject: result.asDictionary,
            hasToolError: result.isError
        )

    case let .sequence(callsJSON, callsFile, interCallDelay):
        let calls = try readOpenComputerUseCallSequence(json: callsJSON, file: callsFile)
        var outputs: [[String: Any]] = []
        var hasToolError = false

        for (index, call) in calls.enumerated() {
            let result = dispatcher.callToolAsResult(name: call.tool, arguments: call.arguments)
            outputs.append([
                "tool": call.tool,
                "result": result.asDictionary,
            ])

            if result.isError {
                hasToolError = true
                break
            }

            if index < calls.count - 1, interCallDelay > 0 {
                sleepHandler(interCallDelay)
            }
        }

        return OpenComputerUseCallOutput(jsonObject: outputs, hasToolError: hasToolError)
    }
}

public func readOpenComputerUseToolArguments(
    json: String?,
    file: String?
) throws -> [String: Any] {
    guard let source = try readOpenComputerUseJSONSource(json: json, file: file) else {
        return [:]
    }

    let object = try decodeOpenComputerUseJSONObject(source)
    guard let arguments = object as? [String: Any] else {
        throw OpenComputerUseCLIError(message: "--args must be a JSON object", helpCommand: "call")
    }

    return arguments
}

public func readOpenComputerUseCallSequence(
    json: String?,
    file: String?
) throws -> [OpenComputerUseCallSpec] {
    guard let source = try readOpenComputerUseJSONSource(json: json, file: file) else {
        throw OpenComputerUseCLIError(message: "call sequence requires --calls or --calls-file", helpCommand: "call")
    }

    let object = try decodeOpenComputerUseJSONObject(source)
    guard let array = object as? [Any] else {
        throw OpenComputerUseCLIError(message: "--calls must be a JSON array", helpCommand: "call")
    }

    return try array.enumerated().map { index, item in
        guard let dictionary = item as? [String: Any] else {
            throw OpenComputerUseCLIError(
                message: "call sequence item #\(index + 1) must be a JSON object",
                helpCommand: "call"
            )
        }

        guard let tool = (dictionary["tool"] ?? dictionary["name"]) as? String, !tool.isEmpty else {
            throw OpenComputerUseCLIError(
                message: "call sequence item #\(index + 1) requires a non-empty tool",
                helpCommand: "call"
            )
        }

        let rawArguments = dictionary["args"] ?? dictionary["arguments"] ?? [:]
        guard let arguments = rawArguments as? [String: Any] else {
            throw OpenComputerUseCLIError(
                message: "call sequence item #\(index + 1) args must be a JSON object",
                helpCommand: "call"
            )
        }

        return OpenComputerUseCallSpec(tool: tool, arguments: arguments)
    }
}

private func readOpenComputerUseJSONSource(json: String?, file: String?) throws -> String? {
    if json != nil, file != nil {
        throw OpenComputerUseCLIError(message: "Use either inline JSON or a JSON file, not both", helpCommand: "call")
    }

    if let json {
        return json
    }

    guard let file else {
        return nil
    }

    do {
        return try String(contentsOfFile: file, encoding: .utf8)
    } catch {
        throw OpenComputerUseCLIError(
            message: "Unable to read JSON file \(file): \(error.localizedDescription)",
            helpCommand: "call"
        )
    }
}

private func decodeOpenComputerUseJSONObject(_ source: String) throws -> Any {
    guard let data = source.data(using: .utf8) else {
        throw OpenComputerUseCLIError(message: "JSON input must be UTF-8 text", helpCommand: "call")
    }

    do {
        return try JSONSerialization.jsonObject(with: data)
    } catch {
        throw OpenComputerUseCLIError(
            message: "Invalid JSON input: \(error.localizedDescription)",
            helpCommand: "call"
        )
    }
}
