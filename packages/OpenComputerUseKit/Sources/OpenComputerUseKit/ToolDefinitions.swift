import Foundation

public struct ToolDefinition: @unchecked Sendable {
    public let name: String
    public let description: String
    public let annotations: [String: Any]
    public let inputSchema: [String: Any]

    public init(name: String, description: String, annotations: [String: Any], inputSchema: [String: Any]) {
        self.name = name
        self.description = description
        self.annotations = annotations
        self.inputSchema = inputSchema
    }

    public var asDictionary: [String: Any] {
        var dictionary: [String: Any] = [
            "name": name,
            "description": description,
            "inputSchema": inputSchema,
        ]

        if !annotations.isEmpty {
            dictionary["annotations"] = annotations
        }

        return dictionary
    }
}

public enum ToolDefinitions {
    public static let all: [ToolDefinition] = [
        ToolDefinition(
            name: "click",
            description: "Click an element by index or pixel coordinates from screenshot. This tool is part of plugin `Computer Use`.",
            annotations: defaultAnnotations(),
            inputSchema: objectSchema(
                properties: [
                    "app": stringProperty(description: "App name or bundle identifier (legacy key-window targeting)"),
                    "window": windowProperty(),
                    "element_index": stringProperty(description: "Element index to click"),
                    "x": numberProperty(description: "X coordinate in screenshot pixel coordinates"),
                    "y": numberProperty(description: "Y coordinate in screenshot pixel coordinates"),
                    "click_count": integerProperty(description: "Number of clicks. Defaults to 1"),
                    "mouse_button": stringProperty(
                        description: "Mouse button to click. Defaults to left.",
                        enumValues: ["left", "right", "middle", "l", "r", "m"]
                    ),
                    "click_method": stringProperty(
                        description: "Click implementation: auto (default), accessibility, app_post, sky_click, or global. Accessibility requires element_index. app_post sends a public event directly to the target app. sky_click uses the macOS SkyLight background window path. Global may move the system pointer and requires OPEN_COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS=1.",
                        enumValues: ClickMethod.allCases.map(\.rawValue)
                    ),
                    "screenshotId": stringProperty(description: "Screenshot id from the latest get_window_state observation. Stale ids are rejected; re-observe after any state change."),
                ],
                required: []
            )
        ),
        ToolDefinition(
            name: "drag",
            description: "Drag from one point to another using pixel coordinates. This tool is part of plugin `Computer Use`.",
            annotations: defaultAnnotations(),
            inputSchema: objectSchema(
                properties: [
                    "app": stringProperty(description: "App name or bundle identifier (legacy key-window targeting)"),
                    "window": windowProperty(),
                    "from_x": numberProperty(description: "Start X coordinate"),
                    "from_y": numberProperty(description: "Start Y coordinate"),
                    "to_x": numberProperty(description: "End X coordinate"),
                    "to_y": numberProperty(description: "End Y coordinate"),
                    "screenshotId": stringProperty(description: "Screenshot id from the latest get_window_state observation. Stale ids are rejected; re-observe after any state change."),
                ],
                required: ["from_x", "from_y", "to_x", "to_y"]
            )
        ),
        ToolDefinition(
            name: "get_app_state",
            description: "Start an app use session if needed, then get the state of the app's key window and return a screenshot and accessibility tree. This must be called once per assistant turn before interacting with the app. This tool is part of plugin `Computer Use`.",
            annotations: readOnlyAnnotations(),
            inputSchema: objectSchema(
                properties: [
                    "app": stringProperty(description: "App name or bundle identifier"),
                    "text_limit": textLimitProperty(description: "Maximum text characters to return. Use \"max\" for full text. Defaults to 500."),
                    "max_tree_nodes": positiveIntegerProperty(description: "Maximum accessibility tree nodes to render. Defaults to 1200."),
                    "max_tree_depth": positiveIntegerProperty(description: "Maximum accessibility tree depth to render. Defaults to 64."),
                ],
                required: ["app"]
            )
        ),
        ToolDefinition(
            name: "list_apps",
            description: "List the apps on this computer. Returns the set of apps that are currently running, as well as any that have been used in the last 14 days, including details on usage frequency. This tool is part of plugin `Computer Use`.",
            annotations: readOnlyAnnotations(),
            inputSchema: objectSchema(properties: [:], required: [])
        ),
        ToolDefinition(
            name: "perform_secondary_action",
            description: "Invoke a secondary accessibility action exposed by an element. This tool is part of plugin `Computer Use`.",
            annotations: defaultAnnotations(),
            inputSchema: objectSchema(
                properties: [
                    "app": stringProperty(description: "App name or bundle identifier (legacy key-window targeting)"),
                    "window": windowProperty(),
                    "element_index": stringProperty(description: "Element identifier"),
                    "action": stringProperty(description: "Secondary accessibility action name (case-insensitive)"),
                ],
                required: ["element_index", "action"]
            )
        ),
        ToolDefinition(
            name: "press_key",
            description: "Press a key or key-combination on the keyboard, including modifier and navigation keys.\n  - This supports xdotool's `key` syntax.\n  - Examples: \"a\", \"Return\", \"Tab\", \"ctrl+c\", \"Control_L+Shift_L+period\", \"Up\", \"KP_0\" (for the numpad 0 key). Windows/Meta key chords are rejected. This tool is part of plugin `Computer Use`.",
            annotations: defaultAnnotations(),
            inputSchema: objectSchema(
                properties: [
                    "app": stringProperty(description: "App name or bundle identifier (legacy key-window targeting)"),
                    "window": windowProperty(),
                    "key": stringProperty(description: "Key or key combination to press"),
                ],
                required: ["key"]
            )
        ),
        ToolDefinition(
            name: "scroll",
            description: "Scroll an element in a direction by a number of pages, or scroll by pixel deltas from a window-relative coordinate (official window2 mode: pass x/y plus scrollX/scrollY; negative scrollY scrolls up, negative scrollX scrolls left; do not pass element_index in this mode). This tool is part of plugin `Computer Use`.",
            annotations: defaultAnnotations(),
            inputSchema: objectSchema(
                properties: [
                    "app": stringProperty(description: "App name or bundle identifier (legacy key-window targeting)"),
                    "window": windowProperty(),
                    "element_index": stringProperty(description: "Element identifier (page mode only)"),
                    "direction": stringProperty(description: "Scroll direction: up, down, left, or right (page mode only)"),
                    "pages": numberProperty(description: "Number of pages to scroll. Fractional values are supported. Defaults to 1"),
                    "x": numberProperty(description: "Window-relative X coordinate to scroll from (coordinate mode)"),
                    "y": numberProperty(description: "Window-relative Y coordinate to scroll from (coordinate mode)"),
                    "scrollX": numberProperty(description: "Horizontal pixel delta: negative scrolls left, positive scrolls right (coordinate mode)"),
                    "scrollY": numberProperty(description: "Vertical pixel delta: negative scrolls up, positive scrolls down (coordinate mode)"),
                    "screenshotId": stringProperty(description: "Screenshot id from the latest get_window_state observation. Stale ids are rejected; re-observe after any state change."),
                ],
                required: []
            )
        ),
        ToolDefinition(
            name: "set_value",
            description: "Set the value of a settable accessibility element. This tool is part of plugin `Computer Use`.",
            annotations: defaultAnnotations(),
            inputSchema: objectSchema(
                properties: [
                    "app": stringProperty(description: "App name or bundle identifier (legacy key-window targeting)"),
                    "window": windowProperty(),
                    "element_index": stringProperty(description: "Element identifier"),
                    "value": stringProperty(description: "Value to assign"),
                ],
                required: ["element_index", "value"]
            )
        ),
        ToolDefinition(
            name: "type_text",
            description: "Type literal text using keyboard input. This tool is part of plugin `Computer Use`.",
            annotations: defaultAnnotations(),
            inputSchema: objectSchema(
                properties: [
                    "app": stringProperty(description: "App name or bundle identifier (legacy key-window targeting)"),
                    "window": windowProperty(),
                    "text": stringProperty(description: "Literal text to type"),
                ],
                required: ["text"]
            )
        ),
        ToolDefinition(
            name: "list_windows",
            description: "List the currently open windows that can be targeted by window-based tools, including secondary and modal windows. This tool is part of plugin `Computer Use`.",
            annotations: readOnlyAnnotations(),
            inputSchema: objectSchema(properties: [:], required: [])
        ),
        ToolDefinition(
            name: "get_window",
            description: "Rehydrate a currently open window by its opaque id. Useful to recover a window binding after an error. This tool is part of plugin `Computer Use`.",
            annotations: readOnlyAnnotations(),
            inputSchema: objectSchema(
                properties: [
                    "app": stringProperty(description: "Optional app identifier carried forward from the original window"),
                    "window": windowProperty(),
                ],
                required: []
            )
        ),
        ToolDefinition(
            name: "launch_app",
            description: "Launch an app by its id from list_apps or an explicit executable path so its window can be targeted. Terminal, password-manager, and security apps are never launched (official safety policy). This tool is part of plugin `Computer Use`.",
            annotations: defaultAnnotations(),
            inputSchema: objectSchema(
                properties: [
                    "app": stringProperty(description: "App id from list_apps or an explicit executable process path"),
                ],
                required: ["app"]
            )
        ),
        ToolDefinition(
            name: "get_window_state",
            description: "Capture the state of a window: a screenshot and/or a structured accessibility tree. Coordinate actions should pass the returned screenshot id. This tool is part of plugin `Computer Use`.",
            annotations: readOnlyAnnotations(),
            inputSchema: objectSchema(
                properties: [
                    "window": windowProperty(),
                    "include_screenshot": booleanProperty(description: "Include window screenshots. Defaults to true"),
                    "include_text": booleanProperty(description: "Include structured text fields (focused_element, selected_text) in the accessibility state. Defaults to false"),
                    "text_limit": textLimitProperty(description: "Maximum text characters to return. Use \"max\" for full text. Defaults to 500."),
                    "max_tree_nodes": positiveIntegerProperty(description: "Maximum accessibility tree nodes to render. Defaults to 1200."),
                    "max_tree_depth": positiveIntegerProperty(description: "Maximum accessibility tree depth to render. Defaults to 64."),
                ],
                required: ["window"]
            )
        ),
        ToolDefinition(
            name: "activate_window",
            description: "Bring a window to the foreground. Input methods activate their target window automatically; use this only as an escape hatch. This tool is part of plugin `Computer Use`.",
            annotations: defaultAnnotations(),
            inputSchema: objectSchema(
                properties: [
                    "window": windowProperty(),
                ],
                required: ["window"]
            )
        ),
    ]
}

private func objectSchema(properties: [String: Any], required: [String]) -> [String: Any] {
    var schema: [String: Any] = [
        "type": "object",
        "properties": properties,
        "additionalProperties": false,
    ]

    if !required.isEmpty {
        schema["required"] = required
    }

    return schema
}

private func defaultAnnotations() -> [String: Any] {
    [
        "destructiveHint": false,
        "openWorldHint": false,
    ]
}

private func readOnlyAnnotations() -> [String: Any] {
    [
        "destructiveHint": false,
        "idempotentHint": true,
        "openWorldHint": false,
        "readOnlyHint": true,
    ]
}

private func stringProperty(description: String, enumValues: [String]? = nil) -> [String: Any] {
    var property: [String: Any] = [
        "type": "string",
        "description": description,
    ]

    if let enumValues {
        property["enum"] = enumValues
    }

    return property
}

// Official window2 `Window` argument. The id is an opaque handle (CGWindowID
// on macOS; HWND on Windows; X window id on Linux) that must come from
// list_windows/get_window_state, never constructed by the caller.
private func windowProperty() -> [String: Any] {
    [
        "type": "object",
        "description": "Target window from list_windows/get_window_state. Takes precedence over the legacy app argument. The id is an opaque handle; never construct it yourself.",
        "properties": [
            "app": stringProperty(description: "App identifier owning the window"),
            "id": [
                "type": "integer",
                "minimum": 0,
                "description": "Opaque window id from list_windows/get_window_state",
            ] as [String: Any],
            "title": stringProperty(description: "Optional user-visible window title"),
        ],
        "required": ["id"],
        "additionalProperties": false,
    ]
}

private func booleanProperty(description: String) -> [String: Any] {
    [
        "type": "boolean",
        "description": description,
    ]
}

private func integerProperty(description: String) -> [String: Any] {
    [
        "type": "integer",
        "description": description,
    ]
}

private func positiveIntegerProperty(description: String) -> [String: Any] {
    [
        "type": "integer",
        "minimum": 1,
        "description": description,
    ]
}

private func textLimitProperty(description: String) -> [String: Any] {
    [
        "anyOf": [
            [
                "type": "integer",
                "minimum": 1,
            ],
            [
                "type": "string",
                "enum": [SnapshotTextLimit.maxKeyword],
            ],
        ],
        "description": description,
    ]
}

private func numberProperty(description: String) -> [String: Any] {
    [
        "type": "number",
        "description": description,
    ]
}
