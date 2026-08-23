package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestToolDefinitionCount(t *testing.T) {
	if got := len(toolDefinitions()); got != 14 {
		t.Fatalf("toolDefinitions() count = %d, want 14", got)
	}
}

func TestWindow2ToolDefinitions(t *testing.T) {
	for _, name := range []string{"list_windows", "get_window", "launch_app", "get_window_state", "activate_window"} {
		tool := findToolDefinition(t, name)
		if tool.InputSchema["type"] != "object" {
			t.Fatalf("%s schema type = %v", name, tool.InputSchema["type"])
		}
	}

	launch := findToolDefinition(t, "launch_app")
	if required := launch.InputSchema["required"].([]string); len(required) != 1 || required[0] != "app" {
		t.Fatalf("launch_app required = %#v", required)
	}

	state := findToolDefinition(t, "get_window_state")
	properties := state.InputSchema["properties"].(map[string]any)
	for _, property := range []string{"window", "include_screenshot", "include_text"} {
		if _, ok := properties[property]; !ok {
			t.Fatalf("get_window_state schema missing %s", property)
		}
	}
	if required := state.InputSchema["required"].([]string); len(required) != 1 || required[0] != "window" {
		t.Fatalf("get_window_state required = %#v", required)
	}
	window := properties["window"].(map[string]any)
	if window["type"] != "object" {
		t.Fatalf("window property type = %v", window["type"])
	}

	activate := findToolDefinition(t, "activate_window")
	if required := activate.InputSchema["required"].([]string); len(required) != 1 || required[0] != "window" {
		t.Fatalf("activate_window required = %#v", required)
	}
}

func TestActionToolsAcceptWindowAndScreenshotId(t *testing.T) {
	for _, name := range []string{"click", "drag", "press_key", "scroll", "set_value", "type_text", "perform_secondary_action"} {
		tool := findToolDefinition(t, name)
		properties := tool.InputSchema["properties"].(map[string]any)
		if _, ok := properties["window"]; !ok {
			t.Fatalf("%s schema should accept a window argument", name)
		}
	}
	for _, name := range []string{"click", "drag", "scroll"} {
		tool := findToolDefinition(t, name)
		properties := tool.InputSchema["properties"].(map[string]any)
		if _, ok := properties["screenshotId"]; !ok {
			t.Fatalf("%s schema should accept screenshotId", name)
		}
	}

	scroll := findToolDefinition(t, "scroll")
	properties := scroll.InputSchema["properties"].(map[string]any)
	for _, property := range []string{"x", "y", "scrollX", "scrollY"} {
		if _, ok := properties[property]; !ok {
			t.Fatalf("scroll schema missing coordinate property %s", property)
		}
	}
	if _, ok := scroll.InputSchema["required"]; ok {
		t.Fatal("scroll required should be relaxed to support both page and coordinate modes")
	}

	click := findToolDefinition(t, "click")
	clickProperties := click.InputSchema["properties"].(map[string]any)
	button := clickProperties["mouse_button"].(map[string]any)
	if got := strings.Join(button["enum"].([]string), ","); got != "left,right,middle,l,r,m" {
		t.Fatalf("mouse_button enum = %q, want left,right,middle,l,r,m", got)
	}
}

func TestClickMethodSchemaAndParser(t *testing.T) {
	tool := findToolDefinition(t, "click")
	properties := tool.InputSchema["properties"].(map[string]any)
	method := properties["click_method"].(map[string]any)
	values := method["enum"].([]string)
	if strings.Join(values, ",") != "auto,accessibility,app_post,sky_click,global" {
		t.Fatalf("click_method enum = %#v", values)
	}

	for input, want := range map[string]string{
		"":              "auto",
		" AUTO ":        "auto",
		"Accessibility": "accessibility",
		"app_post":      "app_post",
		"SKY_CLICK":     "sky_click",
		"GLOBAL":        "global",
	} {
		got, err := parseClickMethod(input)
		if err != nil {
			t.Fatalf("parseClickMethod(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("parseClickMethod(%q) = %q, want %q", input, got, want)
		}
	}

	for _, input := range []string{"physical", "targeted"} {
		if _, err := parseClickMethod(input); err == nil || !strings.Contains(err.Error(), "Expected one of: auto, accessibility, app_post, sky_click, global") {
			t.Fatalf("parseClickMethod(%s) error = %v", input, err)
		}
	}
}

func TestWindowsGatesGlobalClickBeforeSnapshotLookup(t *testing.T) {
	x, y := 10.0, 20.0
	result := newService().click(nil, "Notepad", "", &x, &y, 1, "left", "global")
	want := "click_method 'global' requires OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT=1 because it moves the real mouse pointer and changes foreground focus. Set OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT=1 to enable it."
	if !result.IsError || result.Content[0].Text != want {
		t.Fatalf("global click result = %#v", result)
	}
}

func TestWindowsAllowsGlobalClickWithForegroundInputFlag(t *testing.T) {
	t.Setenv("OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT", "1")
	x, y := 10.0, 20.0
	// Past the gate, the call proceeds to the normal snapshot lookup which
	// fails because no state has been observed yet.
	result := newService().click(nil, "Notepad", "", &x, &y, 1, "left", "global")
	if !result.IsError || !strings.Contains(result.Content[0].Text, "No app state is available") {
		t.Fatalf("global click with flag result = %#v", result)
	}
}

func TestInputMethodParsingAndGating(t *testing.T) {
	for input, want := range map[string]string{"": "auto", "auto": "auto", "GLOBAL": "global"} {
		got, err := parseInputMethod(input)
		if err != nil || got != want {
			t.Fatalf("parseInputMethod(%q) = %v, %v", input, got, err)
		}
	}
	if _, err := parseInputMethod("uia"); err == nil || !strings.Contains(err.Error(), "Expected one of: auto, global") {
		t.Fatalf("parseInputMethod(uia) error = %v", err)
	}

	// Gated without the flag, before snapshot lookup.
	result := newService().callTool("press_key", map[string]any{"app": "Notepad", "key": "a", "input_method": "global"})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "input_method 'global' requires OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT=1") {
		t.Fatalf("gated press_key result = %#v", result)
	}

	t.Setenv("OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT", "1")
	result = newService().callTool("press_key", map[string]any{"app": "Notepad", "key": "a", "input_method": "global"})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "No app state is available") {
		t.Fatalf("flagged press_key result = %#v", result)
	}
}

func TestWindowsRejectsUnsupportedSkyClickBeforeSnapshotLookup(t *testing.T) {
	x, y := 10.0, 20.0
	result := newService().click(nil, "Notepad", "", &x, &y, 1, "left", "sky_click")
	if !result.IsError || result.Content[0].Text != "click_method 'sky_click' is not supported on Windows" {
		t.Fatalf("sky_click result = %#v", result)
	}
}

func TestActionRequiresWindowOrApp(t *testing.T) {
	result := newService().callTool("type_text", map[string]any{"text": "hi"})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "provide either window") {
		t.Fatalf("missing target result = %#v", result)
	}
}

func TestMouseButtonAliases(t *testing.T) {
	for input, want := range map[string]string{
		"": "left", "l": "left", "L": "left", "left": "left",
		"r": "right", "right": "right",
		"m": "middle", "MIDDLE": "middle",
	} {
		got, err := parseMouseButton(input)
		if err != nil {
			t.Fatalf("parseMouseButton(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("parseMouseButton(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := parseMouseButton("side"); err == nil || !strings.Contains(err.Error(), "Expected one of: left, right, middle, l, r, m") {
		t.Fatalf("invalid mouse_button error = %v", err)
	}
}

func TestPressKeyRejectsWindowsMetaChords(t *testing.T) {
	for _, key := range []string{"super", "Win", "meta", "cmd+tab", "Control_L+OS", "windows"} {
		if err := validateKeyChord(key); err == nil {
			t.Fatalf("validateKeyChord(%q) should be rejected", key)
		}
	}
	for _, key := range []string{"a", "Control_L+a", "ctrl+shift+period", "KP_0", "Alt+F4"} {
		if err := validateKeyChord(key); err != nil {
			t.Fatalf("validateKeyChord(%q): %v", key, err)
		}
	}
}

func TestDeniedAppsAreRejected(t *testing.T) {
	for _, app := range []string{
		"cmd", "cmd.exe", "powershell", "pwsh", "WindowsTerminal",
		"C:\\Windows\\System32\\cmd.exe",
		"1Password", "Bitwarden.exe", "KeePassXC", "lastpass",
		"SecurityHealth", "MsMpEng",
	} {
		if denied, _ := deniedAppName(app); !denied {
			t.Fatalf("deniedAppName(%q) should be denied", app)
		}
	}
	for _, app := range []string{"Notepad", "calc", "记事本", "chrome"} {
		if denied, _ := deniedAppName(app); denied {
			t.Fatalf("deniedAppName(%q) should be allowed", app)
		}
	}

	result := newService().launchApp("powershell")
	if !result.IsError || !strings.Contains(result.Content[0].Text, "appDenied") {
		t.Fatalf("launch_app(powershell) result = %#v", result)
	}
	result = newService().callTool("type_text", map[string]any{"app": "cmd", "text": "whoami"})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "appDenied") {
		t.Fatalf("type_text on cmd result = %#v", result)
	}
}

func TestGetWindowStateRequiresScreenshotOrText(t *testing.T) {
	result := newService().callTool("get_window_state", map[string]any{
		"window":             map[string]any{"app": "Notepad", "id": 12345},
		"include_screenshot": false,
		"include_text":       false,
	})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "must request include_text, include_screenshot, or both") {
		t.Fatalf("both-false result = %#v", result)
	}
}

func TestScreenshotIDStaleRejection(t *testing.T) {
	svc := newService()
	svc.screenshots[4242] = screenshotMeta{ID: "shot-4242-1"}

	// Matching id passes validation and fails later only because the runtime
	// is unavailable in unit tests; a stale id must fail before that.
	window := &windowRef{App: "Notepad", ID: 4242}
	if stale := svc.checkScreenshotID(window, "shot-4242-1"); stale != nil {
		t.Fatalf("fresh screenshot id rejected: %#v", stale)
	}
	stale := svc.checkScreenshotID(window, "shot-4242-0")
	if stale == nil || !strings.Contains(stale.Content[0].Text, "stale screenshot id") {
		t.Fatalf("stale screenshot id result = %#v", stale)
	}
	if stale := svc.checkScreenshotID(nil, "shot-4242-1"); stale == nil {
		t.Fatal("screenshotId without window targeting should be rejected")
	}
}

func TestOptionalWindowParsing(t *testing.T) {
	window, err := optionalWindow(map[string]any{
		"window": map[string]any{"app": "Notepad", "id": float64(99), "title": "无标题 - 记事本"},
	})
	if err != nil || window == nil || window.App != "Notepad" || window.ID != 99 || window.Title != "无标题 - 记事本" {
		t.Fatalf("optionalWindow object = (%#v, %v)", window, err)
	}

	window, err = optionalWindow(map[string]any{"window_id": json.Number("4242")})
	if err != nil || window == nil || window.ID != 4242 {
		t.Fatalf("optionalWindow window_id = (%#v, %v)", window, err)
	}

	if window, err := optionalWindow(map[string]any{}); err != nil || window != nil {
		t.Fatalf("optionalWindow empty = (%#v, %v)", window, err)
	}

	if _, err := optionalWindow(map[string]any{"window": map[string]any{"app": "X", "id": 1.5}}); err == nil {
		t.Fatal("fractional window id should be rejected")
	}
}

func TestScrollCoordinateModeValidation(t *testing.T) {
	result := newService().callTool("scroll", map[string]any{
		"window": map[string]any{"app": "Notepad", "id": 4242},
		"x":      10.0, "y": 20.0,
		"scrollX": 0.0, "scrollY": 0.0,
	})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "non-zero scrollX or scrollY") {
		t.Fatalf("zero-delta scroll result = %#v", result)
	}

	result = newService().callTool("scroll", map[string]any{
		"window":  map[string]any{"app": "Notepad", "id": 4242},
		"scrollY": 600.0,
	})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "requires both x and y") {
		t.Fatalf("missing x/y scroll result = %#v", result)
	}
}

func TestGetAppStateSchemaIncludesTextLimit(t *testing.T) {
	tool := findToolDefinition(t, "get_app_state")
	properties := tool.InputSchema["properties"].(map[string]any)
	if _, ok := properties["show_full_text"]; ok {
		t.Fatal("get_app_state schema should not expose show_full_text")
	}
	textLimit := properties["text_limit"].(map[string]any)
	anyOf := textLimit["anyOf"].([]any)
	integerLimit := anyOf[0].(map[string]any)
	if got := integerLimit["type"]; got != "integer" {
		t.Fatalf("text_limit integer type = %v, want integer", got)
	}
	if got := integerLimit["minimum"]; got != 1 {
		t.Fatalf("text_limit integer minimum = %v, want 1", got)
	}
	maxLimit := anyOf[1].(map[string]any)
	if got := maxLimit["type"]; got != "string" {
		t.Fatalf("text_limit max type = %v, want string", got)
	}
	enum := maxLimit["enum"].([]string)
	if len(enum) != 1 || enum[0] != "max" {
		t.Fatalf("text_limit enum = %#v, want [max]", enum)
	}
	maxTreeNodes := properties["max_tree_nodes"].(map[string]any)
	if got := maxTreeNodes["type"]; got != "integer" {
		t.Fatalf("max_tree_nodes type = %v, want integer", got)
	}
	if got := maxTreeNodes["minimum"]; got != 1 {
		t.Fatalf("max_tree_nodes minimum = %v, want 1", got)
	}
	maxTreeDepth := properties["max_tree_depth"].(map[string]any)
	if got := maxTreeDepth["type"]; got != "integer" {
		t.Fatalf("max_tree_depth type = %v, want integer", got)
	}
	if got := maxTreeDepth["minimum"]; got != 1 {
		t.Fatalf("max_tree_depth minimum = %v, want 1", got)
	}
	required := tool.InputSchema["required"].([]string)
	if len(required) != 1 || required[0] != "app" {
		t.Fatalf("required = %#v, want [app]", required)
	}
}

func TestParseSnapshotArgsSupportsTextLimit(t *testing.T) {
	app, textLimit, maxTreeNodes, maxTreeDepth, err := parseSnapshotArgs([]string{"--text-limit", "1000", "Notepad"})
	if err != nil {
		t.Fatal(err)
	}
	if app != "Notepad" || textLimit == nil || textLimit.runtimeValue() != 1000 || maxTreeNodes != nil || maxTreeDepth != nil {
		t.Fatalf("parseSnapshotArgs = (%q, %#v, %v, %v), want (Notepad, 1000, nil, nil)", app, textLimit, maxTreeNodes, maxTreeDepth)
	}

	app, textLimit, maxTreeNodes, maxTreeDepth, err = parseSnapshotArgs([]string{"Notepad", "--text-limit", "max"})
	if err != nil {
		t.Fatal(err)
	}
	if app != "Notepad" || textLimit == nil || textLimit.runtimeValue() != "max" || maxTreeNodes != nil || maxTreeDepth != nil {
		t.Fatalf("parseSnapshotArgs max = (%q, %#v, %v, %v), want (Notepad, max, nil, nil)", app, textLimit, maxTreeNodes, maxTreeDepth)
	}

	app, textLimit, maxTreeNodes, maxTreeDepth, err = parseSnapshotArgs([]string{"Notepad"})
	if err != nil {
		t.Fatal(err)
	}
	if app != "Notepad" || textLimit != nil || maxTreeNodes != nil || maxTreeDepth != nil {
		t.Fatalf("parseSnapshotArgs default = (%q, %#v, %v, %v), want (Notepad, nil, nil, nil)", app, textLimit, maxTreeNodes, maxTreeDepth)
	}

	app, textLimit, maxTreeNodes, maxTreeDepth, err = parseSnapshotArgs([]string{"--max-tree-nodes", "3000", "--max-tree-depth", "96", "Notepad"})
	if err != nil {
		t.Fatal(err)
	}
	if app != "Notepad" || textLimit != nil || maxTreeNodes == nil || *maxTreeNodes != 3000 || maxTreeDepth == nil || *maxTreeDepth != 96 {
		t.Fatalf("parseSnapshotArgs custom tree budget = (%q, %#v, %v, %v), want (Notepad, nil, 3000, 96)", app, textLimit, maxTreeNodes, maxTreeDepth)
	}
}

func TestParseSnapshotArgsRejectsInvalidTextLimit(t *testing.T) {
	for _, value := range []string{"0", "-1", "1.5", "full"} {
		if _, _, _, _, err := parseSnapshotArgs([]string{"--text-limit", value, "Notepad"}); err == nil || err.Error() != "--text-limit must be a positive integer or max" {
			t.Fatalf("invalid text_limit %q error = %v", value, err)
		}
	}
	if _, _, _, _, err := parseSnapshotArgs([]string{"--text-limit"}); err == nil || err.Error() != "--text-limit requires a positive integer or max value" {
		t.Fatalf("missing text_limit error = %v", err)
	}
	if _, _, _, _, err := parseSnapshotArgs([]string{"--show-full-text", "Notepad"}); err == nil || err.Error() != "unknown snapshot option: --show-full-text" {
		t.Fatalf("old show_full_text flag error = %v", err)
	}
}

func TestParseSnapshotArgsRejectsInvalidTreeBudget(t *testing.T) {
	if _, _, _, _, err := parseSnapshotArgs([]string{"--max-tree-nodes", "0", "Notepad"}); err == nil || err.Error() != "--max-tree-nodes must be a positive integer" {
		t.Fatalf("invalid max_tree_nodes error = %v", err)
	}
	if _, _, _, _, err := parseSnapshotArgs([]string{"--max-tree-depth", "1.5", "Notepad"}); err == nil || err.Error() != "--max-tree-depth must be a positive integer" {
		t.Fatalf("invalid max_tree_depth error = %v", err)
	}
	if _, _, _, _, err := parseSnapshotArgs([]string{"--max-tree-nodes"}); err == nil || err.Error() != "--max-tree-nodes requires a positive integer value" {
		t.Fatalf("missing max_tree_nodes error = %v", err)
	}
}

func TestCallSequenceStopsAfterFirstToolError(t *testing.T) {
	output, hasError, err := runCallCommand([]string{
		"--calls",
		`[{"tool":"not_a_tool"},{"tool":"list_apps"}]`,
	}, newService())
	if err != nil {
		t.Fatal(err)
	}
	if !hasError {
		t.Fatal("expected hasError")
	}
	items, ok := output.([]map[string]any)
	if !ok {
		t.Fatalf("output type = %T", output)
	}
	if len(items) != 1 {
		t.Fatalf("sequence output count = %d, want 1", len(items))
	}
}

func TestReadArgumentsAcceptsJSONObject(t *testing.T) {
	args, err := readArguments(`{"app":"Notepad","pages":2}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if args["app"] != "Notepad" {
		t.Fatalf("app = %v", args["app"])
	}
	if args["pages"].(json.Number).String() != "2" {
		t.Fatalf("pages = %v", args["pages"])
	}
}

func TestElementIndexAcceptsStringAndJSONNumber(t *testing.T) {
	args, err := readArguments(`{"app":"Notepad","element_index":0}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := optionalElementIndex(args); got != "0" {
		t.Fatalf("numeric element_index = %q, want 0", got)
	}
	if got := optionalElementIndex(map[string]any{"element_index": "14"}); got != "14" {
		t.Fatalf("string element_index = %q, want 14", got)
	}
	if got := optionalElementIndex(map[string]any{"element_index": json.Number("1.5")}); got != "" {
		t.Fatalf("fractional element_index = %q, want empty", got)
	}
}

func TestMCPInitializeResponseContainsToolsCapability(t *testing.T) {
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      float64(1),
		"method":  "initialize",
		"params":  map[string]any{},
	}
	response := handleMCPRequest(request, newService())
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %#v", response)
	}
	capabilities := result["capabilities"].(map[string]any)
	if _, ok := capabilities["tools"]; !ok {
		t.Fatalf("missing tools capability: %#v", capabilities)
	}
}

func TestCLIHelpMentionsWindowsRuntime(t *testing.T) {
	var out bytes.Buffer
	if err := runCLI([]string{"--help"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Open Computer Use for Windows") {
		t.Fatalf("help text did not mention Windows runtime:\n%s", out.String())
	}
}

func TestRuntimeFlagsRemainOptIn(t *testing.T) {
	// The five runtime flags are protocol surface: every foreground/launch/
	// focus/text-fallback/capture override must stay request-time opt-in.
	required := []string{
		"OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT",
		"OPEN_COMPUTER_USE_WINDOWS_ALLOW_APP_LAUNCH",
		"OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOCUS_ACTIONS",
		"OPEN_COMPUTER_USE_WINDOWS_ALLOW_UIA_TEXT_FALLBACK",
		"OPEN_COMPUTER_USE_WINDOWS_CAPTURE",
	}
	for _, name := range required {
		found := false
		for _, flag := range runtimeEnvFlags {
			if flag == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("runtime flag %s must stay in runtimeEnvFlags", name)
		}
	}
	if !strings.Contains(serverInstructions, "does not auto-launch apps, perform SetFocus/activate_window, or use UIA text fallback by default") {
		t.Fatal("MCP instructions must document the Windows background-focus policy")
	}
}

func TestDeprecatedBackendEnvWarnsOnceAndIsIgnored(t *testing.T) {
	t.Setenv("OPEN_COMPUTER_USE_WINDOWS_BACKEND", "powershell")
	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	warnDeprecatedBackendEnv()
	os.Stderr = original
	writer.Close()
	output, _ := io.ReadAll(reader)
	if !strings.Contains(string(output), "[deprecated] OPEN_COMPUTER_USE_WINDOWS_BACKEND is no longer used") {
		t.Fatalf("deprecation warning output = %q", string(output))
	}
}

func TestMCPToolsListCoversAllFourteenTools(t *testing.T) {
	// Window2 surface + safety tool set stays at exactly 14 tools.
	names := map[string]bool{}
	for _, tool := range toolDefinitions() {
		names[tool.Name] = true
	}
	for _, name := range []string{
		"list_apps", "list_windows", "get_window", "launch_app",
		"get_window_state", "activate_window", "get_app_state",
		"click", "perform_secondary_action", "scroll", "drag",
		"type_text", "press_key", "set_value",
	} {
		if !names[name] {
			t.Fatalf("missing tool definition %q", name)
		}
	}
}

func stubWindowBounds(t *testing.T, rect gateWindowRect, ok bool) {
	t.Helper()
	original := queryWindowBounds
	queryWindowBounds = func(hwnd int64) (gateWindowRect, bool) { return rect, ok }
	t.Cleanup(func() { queryWindowBounds = original })
}

func TestCoordinateGateRequiresObservation(t *testing.T) {
	// Window-targeted coordinate actions with no observation meta must be
	// rejected before any snapshot lookup or backend dispatch.
	want := "call get_window_state before issuing coordinate input"
	result := newService().callTool("click", map[string]any{
		"window": map[string]any{"app": "Notepad", "id": 4242},
		"x":      10.0, "y": 20.0,
	})
	if !result.IsError || result.Content[0].Text != want {
		t.Fatalf("no-meta click result = %#v", result)
	}

	result = newService().callTool("scroll", map[string]any{
		"window": map[string]any{"app": "Notepad", "id": 4242},
		"x":      10.0, "y": 20.0,
		"scrollY": 120.0,
	})
	if !result.IsError || result.Content[0].Text != want {
		t.Fatalf("no-meta scroll result = %#v", result)
	}

	result = newService().callTool("drag", map[string]any{
		"window": map[string]any{"app": "Notepad", "id": 4242},
		"from_x": 1.0, "from_y": 2.0, "to_x": 30.0, "to_y": 40.0,
	})
	if !result.IsError || result.Content[0].Text != want {
		t.Fatalf("no-meta drag result = %#v", result)
	}
}

func TestCoordinateGateRequiresScreenshot(t *testing.T) {
	// Observed with include_screenshot=false: meta exists but has no image.
	svc := newService()
	svc.screenshots[4242] = screenshotMeta{}
	result := svc.callTool("click", map[string]any{
		"window": map[string]any{"app": "Notepad", "id": 4242},
		"x":      10.0, "y": 20.0,
	})
	want := "call get_window_state with include_screenshot before issuing coordinate input"
	if !result.IsError || result.Content[0].Text != want {
		t.Fatalf("no-image click result = %#v", result)
	}
}

func TestCoordinateGateRejectsChangedBounds(t *testing.T) {
	svc := newService()
	svc.screenshots[4242] = screenshotMeta{
		ID: "shot-4242-1", HasImage: true,
		Bounds:    gateWindowRect{Left: 0, Top: 0, Right: 800, Bottom: 600},
		BoundsOK:  true,
		ShotWidth: 800, ShotHeight: 600, DimsOK: true,
	}
	stubWindowBounds(t, gateWindowRect{Left: 10, Top: 0, Right: 810, Bottom: 600}, true)
	result := svc.callTool("click", map[string]any{
		"window": map[string]any{"app": "Notepad", "id": 4242},
		"x":      10.0, "y": 20.0,
	})
	want := "window bounds changed; call get_window_state before continuing"
	if !result.IsError || result.Content[0].Text != want {
		t.Fatalf("changed-bounds click result = %#v", result)
	}
}

func TestCoordinateGateRejectsOutsideScreenshot(t *testing.T) {
	svc := newService()
	bounds := gateWindowRect{Left: 0, Top: 0, Right: 800, Bottom: 600}
	svc.screenshots[4242] = screenshotMeta{
		ID: "shot-4242-1", HasImage: true,
		Bounds: bounds, BoundsOK: true,
		ShotWidth: 800, ShotHeight: 600, DimsOK: true,
	}
	stubWindowBounds(t, bounds, true)

	result := svc.callTool("click", map[string]any{
		"window": map[string]any{"app": "Notepad", "id": 4242},
		"x":      900.0, "y": 20.0,
	})
	if !result.IsError || result.Content[0].Text != "(900, 20) is outside screenshot bounds" {
		t.Fatalf("outside-bounds click result = %#v", result)
	}

	// A point inside the screenshot with unchanged bounds passes the gate and
	// only fails later because no window state was cached in this unit test.
	result = svc.callTool("click", map[string]any{
		"window": map[string]any{"app": "Notepad", "id": 4242},
		"x":      10.0, "y": 20.0,
	})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "No window state is available") {
		t.Fatalf("inside-bounds click result = %#v", result)
	}
}

func TestCoordinateGateSkipsElementAndLegacyTargets(t *testing.T) {
	// Element-targeted clicks never hit the coordinate gate.
	result := newService().callTool("click", map[string]any{
		"window":        map[string]any{"app": "Notepad", "id": 4242},
		"element_index": "1",
	})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "No window state is available") {
		t.Fatalf("element click result = %#v", result)
	}

	// Legacy app-keyed coordinate clicks are not gated (get_app_state never
	// writes screenshot meta).
	x, y := 10.0, 20.0
	result = newService().click(nil, "Notepad", "", &x, &y, 1, "left", "auto")
	if !result.IsError || !strings.Contains(result.Content[0].Text, "No app state is available") {
		t.Fatalf("legacy coordinate click result = %#v", result)
	}
}

func TestDeniedAppDisplayNames(t *testing.T) {
	for _, app := range []string{
		"AVG Internet Security", "avast premium security", "BitDefender Security Center",
		"C:\\Program Files\\AVG Internet Security\\avgui.exe",
	} {
		if denied, _ := deniedAppName(app); !denied {
			t.Fatalf("deniedAppName(%q) should be denied", app)
		}
	}
	for _, app := range []string{"avg", "avast", "bitdefender", "Notepad"} {
		if denied, _ := deniedAppName(app); denied {
			t.Fatalf("deniedAppName(%q) should be allowed", app)
		}
	}

	result := newService().launchApp("Avast Premium Security")
	if !result.IsError || !strings.Contains(result.Content[0].Text, "appDenied") {
		t.Fatalf("launch_app(Avast Premium Security) result = %#v", result)
	}
}

func TestTypeTextLengthLimit(t *testing.T) {
	result := newService().callTool("type_text", map[string]any{
		"app": "Notepad", "text": strings.Repeat("a", 8193),
	})
	want := "text is too large for SendInput (max 8192 UTF-16 code units)"
	if !result.IsError || result.Content[0].Text != want {
		t.Fatalf("oversized type_text result = %#v", result)
	}

	// Exactly 8192 UTF-16 code units (4096 ASCII + 2048 non-BMP runes, each
	// counting as a surrogate pair) passes the cap and fails later only
	// because no app state was observed in this unit test.
	text := strings.Repeat("a", 4096) + strings.Repeat("\U0001F600", 2048)
	if got := len(utf16.Encode([]rune(text))); got != 8192 {
		t.Fatalf("test text length = %d, want 8192", got)
	}
	result = newService().callTool("type_text", map[string]any{"app": "Notepad", "text": text})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "No app state is available") {
		t.Fatalf("max-length type_text result = %#v", result)
	}
}

func TestRightDoubleClickRejected(t *testing.T) {
	for _, button := range []string{"right", "r", "R"} {
		result := newService().callTool("click", map[string]any{
			"app": "Notepad", "x": 10.0, "y": 20.0,
			"mouse_button": button, "click_count": 2.0,
		})
		if !result.IsError || result.Content[0].Text != "right double click is not supported" {
			t.Fatalf("right double click (%q) result = %#v", button, result)
		}
	}

	// Left double click and right single click pass the check and fail later
	// only because no app state was observed in this unit test.
	result := newService().callTool("click", map[string]any{
		"app": "Notepad", "x": 10.0, "y": 20.0, "click_count": 2.0,
	})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "No app state is available") {
		t.Fatalf("left double click result = %#v", result)
	}
	result = newService().callTool("click", map[string]any{
		"app": "Notepad", "x": 10.0, "y": 20.0, "mouse_button": "right",
	})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "No app state is available") {
		t.Fatalf("right single click result = %#v", result)
	}
}

func TestPNGDimensionsParsesIHDR(t *testing.T) {
	header := []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, // signature
		0, 0, 0, 13, 'I', 'H', 'D', 'R', // length + chunk type
		0, 0, 0x03, 0x20, // width = 800
		0, 0, 0x02, 0x58, // height = 600
	}
	width, height, ok := pngDimensions(base64.StdEncoding.EncodeToString(header))
	if !ok || width != 800 || height != 600 {
		t.Fatalf("pngDimensions = (%d, %d, %v), want (800, 600, true)", width, height, ok)
	}
	if _, _, ok := pngDimensions("not-base64!!!"); ok {
		t.Fatal("pngDimensions should reject non-PNG input")
	}
}

func findToolDefinition(t *testing.T, name string) toolDefinition {
	t.Helper()
	for _, tool := range toolDefinitions() {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("missing tool definition %q", name)
	return toolDefinition{}
}
