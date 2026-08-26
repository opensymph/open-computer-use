package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

var version = "1.2.0"

var clickMethodValues = []string{"auto", "accessibility", "app_post", "sky_click", "global"}

var mouseButtonValues = []string{"left", "right", "middle", "l", "r", "m"}

// Official Computer Use non-negotiable safety denies: the Windows/Meta key
// must never be pressed, and terminal hosts, password managers, and Windows
// security apps must never be automated.
var bannedModifierNames = []string{"super", "win", "cmd", "meta", "command", "os", "windows"}

var deniedAppExactNames = []string{
	"windowsterminal", "wt", "cmd", "powershell", "pwsh", "consolehost", "conhost",
	"defender", "msmpeng", "nissrv", "securityhealth", "securityhealthservice", "wdav",
}

var deniedAppPatterns = []string{"1password", "lastpass", "bitwarden", "keepass", "dashlane"}

// Official Computer Use safety denies by AV product display name: any app
// identifier containing one of these (case-insensitive) is refused.
var deniedAppDisplayNames = []string{
	"avg internet security",
	"avast premium security",
	"bitdefender security center",
}

const serverInstructions = "Computer Use tools let you interact with Windows apps by performing UI actions.\n\nBegin each turn by observing: `list_apps` + `list_windows` to select a target, then `get_app_state` (legacy app-keyed flow) or `get_window_state` (window2 flow) before acting. The available tools are list_apps, list_windows, get_window, launch_app, get_window_state, activate_window, get_app_state, click, perform_secondary_action, scroll, drag, type_text, press_key, and set_value.\n\nPrefer element-targeted interactions over coordinate clicks when an index for the targeted element is available. Window-targeted tools accept a `window` object (`{app, id, title}`) whose opaque id comes from list_windows/get_window_state and take precedence over the legacy `app` argument. Coordinate actions may pass `screenshotId` from the latest get_window_state; stale ids are rejected. The Windows runtime does not auto-launch apps, perform SetFocus/activate_window, or use UIA text fallback by default, so background-capable actions do not intentionally steal the user's foreground focus. The Windows/Meta key and terminal, password-manager, and security apps are never automated (official safety policy)."

type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Annotations map[string]any `json:"annotations,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

type contentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

type toolCallResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError"`
}

func textResult(text string, isError bool) toolCallResult {
	return toolCallResult{Content: []contentItem{{Type: "text", Text: text}}, IsError: isError}
}

type appDescriptor struct {
	Name             string `json:"name"`
	BundleIdentifier string `json:"bundleIdentifier,omitempty"`
	PID              int    `json:"pid"`
}

type frame struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

func (f frame) renderedLocalFrame() string {
	return fmt.Sprintf("{{x: %.0f, y: %.0f, width: %.0f, height: %.0f}}", f.X, f.Y, f.Width, f.Height)
}

type elementRecord struct {
	Index                int      `json:"index"`
	RuntimeID            []int    `json:"runtimeId,omitempty"`
	AutomationID         string   `json:"automationId,omitempty"`
	Name                 string   `json:"name,omitempty"`
	ControlType          string   `json:"controlType,omitempty"`
	LocalizedControlType string   `json:"localizedControlType,omitempty"`
	ClassName            string   `json:"className,omitempty"`
	Value                string   `json:"value,omitempty"`
	NativeWindowHandle   int64    `json:"nativeWindowHandle,omitempty"`
	Frame                *frame   `json:"frame,omitempty"`
	Actions              []string `json:"actions,omitempty"`
	// Selected marks SelectionItem-pattern elements that are currently
	// selected; it feeds the official selected_elements output field and is
	// not part of any wire contract.
	Selected bool `json:"-"`
}

type appSnapshot struct {
	App                 appDescriptor   `json:"app"`
	WindowHandle        int64           `json:"windowHandle,omitempty"`
	WindowTitle         string          `json:"windowTitle,omitempty"`
	WindowBounds        *frame          `json:"windowBounds,omitempty"`
	ScreenshotPNGBase64 string          `json:"screenshotPngBase64,omitempty"`
	TreeLines           []string        `json:"treeLines,omitempty"`
	FocusedSummary      string          `json:"focusedSummary,omitempty"`
	SelectedText        string          `json:"selectedText,omitempty"`
	DocumentText        string          `json:"documentText,omitempty"`
	SelectedElements    []string        `json:"selectedElements,omitempty"`
	Elements            []elementRecord `json:"elements,omitempty"`
}

// windowRef mirrors the official window2 `Window` type: an opaque integer
// window handle (HWND on Windows) plus the owning app identifier and title.
type windowRef struct {
	App   string `json:"app"`
	ID    int64  `json:"id"`
	Title string `json:"title,omitempty"`
}

// listAppsApp mirrors the official `ListAppsApp` type.
type listAppsApp struct {
	DisplayName string      `json:"displayName,omitempty"`
	ID          string      `json:"id"`
	IsRunning   bool        `json:"isRunning,omitempty"`
	Windows     []windowRef `json:"windows,omitempty"`
}

type accessibilityState struct {
	Tree             string   `json:"tree"`
	DocumentText     string   `json:"document_text,omitempty"`
	FocusedElement   string   `json:"focused_element,omitempty"`
	SelectedElements []string `json:"selected_elements,omitempty"`
	SelectedText     string   `json:"selected_text,omitempty"`
}

type screenshotInfo struct {
	ID      string  `json:"id"`
	URL     string  `json:"url"`
	Width   float64 `json:"width,omitempty"`
	Height  float64 `json:"height,omitempty"`
	OriginX float64 `json:"originX,omitempty"`
	OriginY float64 `json:"originY,omitempty"`
	ZIndex  int     `json:"zIndex"`
}

type windowState struct {
	Window        windowRef           `json:"window"`
	Accessibility *accessibilityState `json:"accessibility"`
	Screenshots   []screenshotInfo    `json:"screenshots"`
}

func (s *appSnapshot) renderedText() string {
	if s == nil {
		return ""
	}
	appRef := s.App.BundleIdentifier
	if appRef == "" {
		appRef = s.App.Name
	}
	title := s.WindowTitle
	if strings.TrimSpace(title) == "" {
		title = s.App.Name
	}

	lines := []string{
		fmt.Sprintf("App=%s (pid %d)", appRef, s.App.PID),
		fmt.Sprintf("Window: %q, App: %s.", title, s.App.Name),
	}
	lines = append(lines, s.TreeLines...)
	if strings.TrimSpace(s.SelectedText) != "" {
		lines = append(lines, "", fmt.Sprintf("Selected text: [%s]", s.SelectedText))
	} else if strings.TrimSpace(s.FocusedSummary) != "" {
		lines = append(lines, "", fmt.Sprintf("The focused UI element is %s.", s.FocusedSummary))
	}
	return strings.Join(lines, "\n")
}

func (s *appSnapshot) result() toolCallResult {
	result := toolCallResult{
		Content: []contentItem{{Type: "text", Text: s.renderedText()}},
	}
	if s != nil && s.ScreenshotPNGBase64 != "" {
		result.Content = append(result.Content, contentItem{
			Type:     "image",
			Data:     s.ScreenshotPNGBase64,
			MimeType: "image/png",
		})
	}
	return result
}

type psRequest struct {
	Tool                 string            `json:"tool"`
	App                  string            `json:"app,omitempty"`
	WindowID             int64             `json:"windowId,omitempty"`
	Element              *elementRecord    `json:"element,omitempty"`
	X                    *float64          `json:"x,omitempty"`
	Y                    *float64          `json:"y,omitempty"`
	FromX                *float64          `json:"from_x,omitempty"`
	FromY                *float64          `json:"from_y,omitempty"`
	ToX                  *float64          `json:"to_x,omitempty"`
	ToY                  *float64          `json:"to_y,omitempty"`
	ScrollX              *float64          `json:"scrollX,omitempty"`
	ScrollY              *float64          `json:"scrollY,omitempty"`
	ClickCount           int               `json:"click_count,omitempty"`
	MouseButton          string            `json:"mouse_button,omitempty"`
	ClickMethod          string            `json:"click_method,omitempty"`
	InputMethod          string            `json:"input_method,omitempty"`
	AllowForegroundInput bool              `json:"allow_foreground_input,omitempty"`
	EnvFlags             map[string]string `json:"env_flags,omitempty"`
	Action               string            `json:"action,omitempty"`
	Direction            string            `json:"direction,omitempty"`
	Pages                float64           `json:"pages,omitempty"`
	Text                 string            `json:"text,omitempty"`
	Key                  string            `json:"key,omitempty"`
	Value                string            `json:"value,omitempty"`
	WindowBounds         *frame            `json:"windowBounds,omitempty"`
	TextLimit            any               `json:"text_limit,omitempty"`
	MaxTreeNodes         int               `json:"max_tree_nodes,omitempty"`
	MaxTreeDepth         int               `json:"max_tree_depth,omitempty"`
	IncludeScreenshot    *bool             `json:"include_screenshot,omitempty"`
}

type textLimit struct {
	max   bool
	count int
}

func (limit textLimit) runtimeValue() any {
	if limit.max {
		return "max"
	}
	return limit.count
}

type psResponse struct {
	OK       bool          `json:"ok"`
	Text     string        `json:"text,omitempty"`
	Error    string        `json:"error,omitempty"`
	Snapshot *appSnapshot  `json:"snapshot,omitempty"`
	Windows  []windowRef   `json:"windows,omitempty"`
	Window   *windowRef    `json:"window,omitempty"`
	Apps     []listAppsApp `json:"apps,omitempty"`
}

// screenshotMeta is the observation binding for one window: the latest
// screenshot id, whether the producing get_window_state included a
// screenshot, the window bounds observed via GetWindowRect, and the
// screenshot PNG pixel dimensions.
type screenshotMeta struct {
	ID string
	// HasImage marks that the producing observation returned a screenshot
	// (include_screenshot=true with image data and window bounds).
	HasImage bool
	// Bounds/BoundsOK record the window rect at observation time.
	Bounds   gateWindowRect
	BoundsOK bool
	// ShotWidth/ShotHeight/DimsOK record the screenshot PNG pixel size
	// parsed from the IHDR chunk.
	ShotWidth  int
	ShotHeight int
	DimsOK     bool
}

// maxCachedSnapshots bounds the element-lookup cache. Snapshots are cached
// for element_index resolution only; the base64 screenshot is stripped from
// cached copies (full-size PNGs would otherwise accumulate for every observed
// window for the lifetime of the process).
const maxCachedSnapshots = 16

type service struct {
	snapshots   map[string]*appSnapshot
	screenshots map[int64]screenshotMeta
	nextShotID  int
	cacheOrder  []string
}

func newService() *service {
	return &service{
		snapshots:   map[string]*appSnapshot{},
		screenshots: map[int64]screenshotMeta{},
	}
}

// cacheSnapshot stores a screenshot-free shallow copy under key. The caller's
// snapshot keeps its image for the tool result being built.
func (s *service) cacheSnapshot(key string, snapshot *appSnapshot) {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return
	}
	if _, exists := s.snapshots[key]; !exists {
		s.cacheOrder = append(s.cacheOrder, key)
	}
	cached := *snapshot
	cached.ScreenshotPNGBase64 = ""
	s.snapshots[key] = &cached
	for len(s.cacheOrder) > maxCachedSnapshots {
		oldest := s.cacheOrder[0]
		s.cacheOrder = s.cacheOrder[1:]
		delete(s.snapshots, oldest)
	}
}

func windowKey(windowID int64) string {
	return fmt.Sprintf("win:%d", windowID)
}

func (s *service) callTool(name string, args map[string]any) toolCallResult {
	switch name {
	case "list_apps":
		return s.listApps()
	case "list_windows":
		return s.listWindows()
	case "get_window":
		window, err := optionalWindow(args)
		if err != nil {
			return textResult(err.Error(), true)
		}
		if window == nil || window.ID == 0 {
			return textResult("Missing required argument: window.id", true)
		}
		return s.getWindow(window.ID)
	case "launch_app":
		app := requiredString(args, "app")
		if app == "" {
			return textResult("Missing required argument: app", true)
		}
		return s.launchApp(app)
	case "get_window_state":
		return s.getWindowState(args)
	case "activate_window":
		window, err := optionalWindow(args)
		if err != nil {
			return textResult(err.Error(), true)
		}
		if window == nil || window.ID == 0 {
			return textResult("Missing required argument: window.id", true)
		}
		return s.activateWindow(window.ID)
	case "get_app_state":
		maxTreeNodes, err := optionalPositiveInt(args, "max_tree_nodes")
		if err != nil {
			return textResult(err.Error(), true)
		}
		maxTreeDepth, err := optionalPositiveInt(args, "max_tree_depth")
		if err != nil {
			return textResult(err.Error(), true)
		}
		textLimit, err := optionalTextLimit(args, "text_limit")
		if err != nil {
			return textResult(err.Error(), true)
		}
		return s.getAppState(requiredString(args, "app"), textLimit, maxTreeNodes, maxTreeDepth)
	case "click":
		clickMethod, err := parseClickMethod(optionalString(args, "click_method"))
		if err != nil {
			return textResult(err.Error(), true)
		}
		mouseButton, err := parseMouseButton(optionalString(args, "mouse_button"))
		if err != nil {
			return textResult(err.Error(), true)
		}
		target, app, result := resolveActionTarget(args)
		if result != nil {
			return *result
		}
		if stale := s.checkScreenshotID(target, optionalString(args, "screenshotId")); stale != nil {
			return *stale
		}
		return s.click(
			target,
			app,
			optionalElementIndex(args),
			optionalFloat(args, "x"),
			optionalFloat(args, "y"),
			clampClickCount(intValue(optionalFloat(args, "click_count"), 1)),
			mouseButton,
			clickMethod,
		)
	case "perform_secondary_action":
		target, app, result := resolveActionTarget(args)
		if result != nil {
			return *result
		}
		return s.performSecondaryAction(
			target,
			app,
			requiredElementIndex(args),
			requiredString(args, "action"),
		)
	case "scroll":
		inputMethod, err := parseInputMethod(optionalString(args, "input_method"))
		if err != nil {
			return textResult(err.Error(), true)
		}
		if inputMethod == "global" {
			if gated := gateForegroundInput("input_method 'global'"); gated != nil {
				return *gated
			}
		}
		target, app, result := resolveActionTarget(args)
		if result != nil {
			return *result
		}
		scrollX := optionalFloat(args, "scrollX")
		scrollY := optionalFloat(args, "scrollY")
		x := optionalFloat(args, "x")
		y := optionalFloat(args, "y")
		if scrollX != nil || scrollY != nil {
			// Official window2 coordinate scroll: x/y + pixel deltas.
			if stale := s.checkScreenshotID(target, optionalString(args, "screenshotId")); stale != nil {
				return *stale
			}
			return s.scrollAtPoint(target, app, x, y, scrollX, scrollY, inputMethod)
		}
		return s.scrollByPages(
			target,
			app,
			requiredString(args, "direction"),
			requiredElementIndex(args),
			clampScrollPages(floatValue(optionalFloat(args, "pages"), 1)),
			inputMethod,
		)
	case "drag":
		inputMethod, err := parseInputMethod(optionalString(args, "input_method"))
		if err != nil {
			return textResult(err.Error(), true)
		}
		if inputMethod == "global" {
			if gated := gateForegroundInput("input_method 'global'"); gated != nil {
				return *gated
			}
		}
		target, app, result := resolveActionTarget(args)
		if result != nil {
			return *result
		}
		if stale := s.checkScreenshotID(target, optionalString(args, "screenshotId")); stale != nil {
			return *stale
		}
		return s.drag(
			target,
			app,
			requiredFloat(args, "from_x"),
			requiredFloat(args, "from_y"),
			requiredFloat(args, "to_x"),
			requiredFloat(args, "to_y"),
			inputMethod,
		)
	case "type_text":
		inputMethod, err := parseInputMethod(optionalString(args, "input_method"))
		if err != nil {
			return textResult(err.Error(), true)
		}
		if inputMethod == "global" {
			if gated := gateForegroundInput("input_method 'global'"); gated != nil {
				return *gated
			}
		}
		target, app, result := resolveActionTarget(args)
		if result != nil {
			return *result
		}
		return s.typeText(target, app, requiredString(args, "text"), inputMethod)
	case "press_key":
		inputMethod, err := parseInputMethod(optionalString(args, "input_method"))
		if err != nil {
			return textResult(err.Error(), true)
		}
		if inputMethod == "global" {
			if gated := gateForegroundInput("input_method 'global'"); gated != nil {
				return *gated
			}
		}
		target, app, result := resolveActionTarget(args)
		if result != nil {
			return *result
		}
		key := optionalString(args, "key")
		if key == "" {
			return textResult("Missing required argument: key", true)
		}
		if err := validateKeyChord(key); err != nil {
			return textResult(err.Error(), true)
		}
		return s.pressKey(target, app, key, inputMethod)
	case "set_value":
		target, app, result := resolveActionTarget(args)
		if result != nil {
			return *result
		}
		return s.setValue(target, app, requiredElementIndex(args), optionalString(args, "value"))
	default:
		return textResult(fmt.Sprintf("unsupportedTool(%q)", name), true)
	}
}

// resolveActionTarget picks the action window: the official `window` object
// (or `window_id`) when supplied, falling back to the legacy `app` argument.
func resolveActionTarget(args map[string]any) (*windowRef, string, *toolCallResult) {
	window, err := optionalWindow(args)
	if err != nil {
		return nil, "", &toolCallResult{Content: []contentItem{{Type: "text", Text: err.Error()}}, IsError: true}
	}
	app := requiredString(args, "app")
	if window == nil && app == "" {
		return nil, "", &toolCallResult{Content: []contentItem{{Type: "text", Text: "Missing required argument: provide either window (from list_windows/get_window_state) or app"}}, IsError: true}
	}
	if window != nil {
		if window.App != "" {
			app = window.App
		}
		return window, app, nil
	}
	if denied, name := deniedAppName(app); denied {
		return nil, "", &toolCallResult{Content: []contentItem{{Type: "text", Text: fmt.Sprintf("appDenied(%q): automating terminal apps, password managers, or Windows security apps is not permitted (official Computer Use safety policy).", name)}}, IsError: true}
	}
	return nil, app, nil
}

// checkScreenshotID enforces the official observation binding: a screenshotId
// is only valid for the window whose latest get_window_state produced it.
func (s *service) checkScreenshotID(window *windowRef, screenshotID string) *toolCallResult {
	if strings.TrimSpace(screenshotID) == "" {
		return nil
	}
	if window == nil {
		return &toolCallResult{Content: []contentItem{{Type: "text", Text: "screenshotId requires window targeting; pass window from get_window_state and re-observe."}}, IsError: true}
	}
	meta, ok := s.screenshots[window.ID]
	if !ok || meta.ID != strings.TrimSpace(screenshotID) {
		return &toolCallResult{Content: []contentItem{{Type: "text", Text: "stale screenshot id; re-observe with get_window_state before retrying."}}, IsError: true}
	}
	return nil
}

// checkCoordinateGate enforces the official observation binding for
// window2-targeted coordinate input (click at x/y, coordinate scroll, drag)
// after checkScreenshotID and before dispatch: the window must have been
// observed by get_window_state, the observation must include a screenshot,
// the window bounds must be unchanged since the observation, and the point
// must lie inside the screenshot pixels. Element-targeted actions and the
// legacy app-keyed flow (get_app_state never writes screenshot meta) never
// reach this gate.
func (s *service) checkCoordinateGate(target *windowRef, x, y float64) *toolCallResult {
	meta, ok := s.screenshots[target.ID]
	if !ok {
		return &toolCallResult{Content: []contentItem{{Type: "text", Text: "call get_window_state before issuing coordinate input"}}, IsError: true}
	}
	if !meta.HasImage {
		return &toolCallResult{Content: []contentItem{{Type: "text", Text: "call get_window_state with include_screenshot before issuing coordinate input"}}, IsError: true}
	}
	if meta.BoundsOK {
		if current, ok := queryWindowBounds(target.ID); ok && current != meta.Bounds {
			return &toolCallResult{Content: []contentItem{{Type: "text", Text: "window bounds changed; call get_window_state before continuing"}}, IsError: true}
		}
	}
	if meta.DimsOK && (x < 0 || y < 0 || x >= float64(meta.ShotWidth) || y >= float64(meta.ShotHeight)) {
		return &toolCallResult{Content: []contentItem{{Type: "text", Text: fmt.Sprintf("(%d, %d) is outside screenshot bounds", int(x), int(y))}}, IsError: true}
	}
	return nil
}

func (s *service) listApps() toolCallResult {
	response, err := runRuntimeOperation(psRequest{Tool: "list_apps"})
	if err != nil {
		return textResult(err.Error(), true)
	}
	if !response.OK {
		return textResult(response.Error, true)
	}
	if strings.TrimSpace(response.Text) == "" {
		response.Text = "No running top-level apps are visible to this Windows runtime."
	}
	result := textResult(response.Text, false)
	// Official window2 semantics: apps carry their open targetable windows.
	if len(response.Apps) > 0 {
		encoded, err := json.MarshalIndent(response.Apps, "", "  ")
		if err == nil {
			result.Content = append(result.Content, contentItem{Type: "text", Text: string(encoded)})
		}
	}
	return result
}

func (s *service) listWindows() toolCallResult {
	response, err := runRuntimeOperation(psRequest{Tool: "list_windows"})
	if err != nil {
		return textResult(err.Error(), true)
	}
	if !response.OK {
		return textResult(response.Error, true)
	}
	encoded, err := json.MarshalIndent(response.Windows, "", "  ")
	if err != nil {
		return textResult(err.Error(), true)
	}
	return textResult(string(encoded), false)
}

func (s *service) getWindow(windowID int64) toolCallResult {
	response, err := runRuntimeOperation(psRequest{Tool: "get_window", WindowID: windowID})
	if err != nil {
		return textResult(err.Error(), true)
	}
	if !response.OK {
		return textResult(response.Error, true)
	}
	if response.Window == nil {
		return textResult("Windows runtime did not return a window for id "+strconv.FormatInt(windowID, 10), true)
	}
	encoded, err := json.MarshalIndent(response.Window, "", "  ")
	if err != nil {
		return textResult(err.Error(), true)
	}
	return textResult(string(encoded), false)
}

func (s *service) launchApp(app string) toolCallResult {
	if denied, name := deniedAppName(app); denied {
		return textResult(fmt.Sprintf("appDenied(%q): automating terminal apps, password managers, or Windows security apps is not permitted (official Computer Use safety policy).", name), true)
	}
	response, err := runRuntimeOperation(psRequest{Tool: "launch_app", App: app})
	if err != nil {
		return textResult(err.Error(), true)
	}
	if !response.OK {
		return textResult(response.Error, true)
	}
	if response.Window == nil {
		return textResult("launch_app did not return a window for "+app, true)
	}
	encoded, err := json.MarshalIndent(response.Window, "", "  ")
	if err != nil {
		return textResult(err.Error(), true)
	}
	return textResult(string(encoded), false)
}

func (s *service) activateWindow(windowID int64) toolCallResult {
	response, err := runRuntimeOperation(psRequest{Tool: "activate_window", WindowID: windowID})
	if err != nil {
		return textResult(err.Error(), true)
	}
	if !response.OK {
		return textResult(response.Error, true)
	}
	if response.Window == nil {
		return textResult("Windows runtime did not return a window for id "+strconv.FormatInt(windowID, 10), true)
	}
	encoded, err := json.MarshalIndent(response.Window, "", "  ")
	if err != nil {
		return textResult(err.Error(), true)
	}
	return textResult(string(encoded), false)
}

func (s *service) getWindowState(args map[string]any) toolCallResult {
	window, err := optionalWindow(args)
	if err != nil {
		return textResult(err.Error(), true)
	}
	if window == nil || window.ID == 0 {
		return textResult("Missing required argument: window", true)
	}
	includeScreenshot := true
	if value, ok := args["include_screenshot"]; ok {
		includeScreenshot, ok = value.(bool)
		if !ok {
			return textResult("include_screenshot must be a boolean", true)
		}
	}
	includeText := false
	if value, ok := args["include_text"]; ok {
		includeText, ok = value.(bool)
		if !ok {
			return textResult("include_text must be a boolean", true)
		}
	}
	if !includeScreenshot && !includeText {
		return textResult("get_window_state must request include_text, include_screenshot, or both", true)
	}
	request := psRequest{Tool: "get_window_state", WindowID: window.ID, IncludeScreenshot: &includeScreenshot}
	maxTreeNodes, err := optionalPositiveInt(args, "max_tree_nodes")
	if err != nil {
		return textResult(err.Error(), true)
	}
	if maxTreeNodes != nil {
		request.MaxTreeNodes = *maxTreeNodes
	}
	maxTreeDepth, err := optionalPositiveInt(args, "max_tree_depth")
	if err != nil {
		return textResult(err.Error(), true)
	}
	if maxTreeDepth != nil {
		request.MaxTreeDepth = *maxTreeDepth
	}
	textLimit, err := optionalTextLimit(args, "text_limit")
	if err != nil {
		return textResult(err.Error(), true)
	}
	if textLimit != nil {
		request.TextLimit = textLimit.runtimeValue()
	}

	response, err := runRuntimeOperation(request)
	if err != nil {
		return textResult(err.Error(), true)
	}
	if !response.OK {
		return textResult(response.Error, true)
	}
	snapshot := response.Snapshot
	if snapshot == nil {
		return textResult("Windows runtime did not return a window state.", true)
	}
	snapshot.WindowHandle = window.ID
	s.rememberSnapshot(window.App, snapshot)
	s.cacheSnapshot(windowKey(window.ID), snapshot)

	state := windowState{
		Window: windowRef{App: snapshot.App.Name, ID: window.ID, Title: snapshot.WindowTitle},
		Accessibility: &accessibilityState{
			Tree: strings.Join(snapshot.TreeLines, "\n"),
		},
		Screenshots: []screenshotInfo{},
	}
	if includeText {
		state.Accessibility.FocusedElement = snapshot.FocusedSummary
		state.Accessibility.SelectedText = snapshot.SelectedText
		state.Accessibility.DocumentText = snapshot.DocumentText
		state.Accessibility.SelectedElements = snapshot.SelectedElements
	} else {
		state.Accessibility.FocusedElement = ""
		state.Accessibility.SelectedText = ""
		state.Accessibility.DocumentText = ""
		state.Accessibility.SelectedElements = nil
	}
	// Every successful observation records its binding meta so coordinate
	// input can distinguish "never observed" from "observed without a
	// screenshot" and detect moved/resized windows.
	meta := screenshotMeta{}
	if bounds, ok := queryWindowBounds(window.ID); ok {
		meta.Bounds = bounds
		meta.BoundsOK = true
	}
	if includeScreenshot && snapshot.ScreenshotPNGBase64 != "" && snapshot.WindowBounds != nil {
		s.nextShotID++
		shot := screenshotInfo{
			ID:      fmt.Sprintf("shot-%d-%d", window.ID, s.nextShotID),
			URL:     "data:image/png;base64," + snapshot.ScreenshotPNGBase64,
			OriginX: snapshot.WindowBounds.X,
			OriginY: snapshot.WindowBounds.Y,
			ZIndex:  0,
		}
		if snapshot.WindowBounds != nil {
			shot.Width = snapshot.WindowBounds.Width
			shot.Height = snapshot.WindowBounds.Height
		}
		state.Screenshots = append(state.Screenshots, shot)
		meta.ID = shot.ID
		meta.HasImage = true
		if width, height, ok := pngDimensions(snapshot.ScreenshotPNGBase64); ok {
			meta.ShotWidth = width
			meta.ShotHeight = height
			meta.DimsOK = true
		}
	}
	s.screenshots[window.ID] = meta
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return textResult(err.Error(), true)
	}
	result := toolCallResult{Content: []contentItem{{Type: "text", Text: string(encoded)}}}
	if includeScreenshot && snapshot.ScreenshotPNGBase64 != "" {
		result.Content = append(result.Content, contentItem{Type: "image", Data: snapshot.ScreenshotPNGBase64, MimeType: "image/png"})
	}
	return result
}

func (s *service) getAppState(app string, textLimit *textLimit, maxTreeNodes, maxTreeDepth *int) toolCallResult {
	if app == "" {
		return textResult("Missing required argument: app", true)
	}
	request := psRequest{Tool: "get_app_state", App: app}
	if textLimit != nil {
		request.TextLimit = textLimit.runtimeValue()
	}
	if maxTreeNodes != nil {
		request.MaxTreeNodes = *maxTreeNodes
	}
	if maxTreeDepth != nil {
		request.MaxTreeDepth = *maxTreeDepth
	}
	snapshot, result := s.refreshSnapshot(app, request)
	if result.IsError {
		return result
	}
	return snapshot.result()
}

// actionContext resolves the cached snapshot and cache key for an action,
// supporting both window2 window targeting and the legacy app key window.
func (s *service) actionContext(target *windowRef, app string) (*appSnapshot, string, string, *toolCallResult) {
	if target != nil {
		snapshot := s.snapshots[windowKey(target.ID)]
		if snapshot == nil {
			return nil, "", "", &toolCallResult{Content: []contentItem{{Type: "text", Text: "No window state is available for window id " + strconv.FormatInt(target.ID, 10) + ". Run get_window_state before action tools."}}, IsError: true}
		}
		return snapshot, windowKey(target.ID), app, nil
	}
	snapshot := s.currentSnapshot(app)
	if snapshot == nil {
		return nil, "", "", &toolCallResult{Content: []contentItem{{Type: "text", Text: "No app state is available for " + app + ". Run get_app_state before action tools."}}, IsError: true}
	}
	return snapshot, app, app, nil
}

func (s *service) click(target *windowRef, app, elementIndex string, x, y *float64, clickCount int, mouseButton, clickMethod string) toolCallResult {
	if mouseButton == "right" && clickCount >= 2 {
		return textResult("right double click is not supported", true)
	}
	if elementIndex == "" && (x == nil || y == nil) {
		return textResult("click requires either element_index or x/y", true)
	}
	if clickMethod == "accessibility" && elementIndex == "" {
		return textResult("click_method 'accessibility' requires element_index", true)
	}
	if clickMethod == "global" {
		if gated := gateForegroundInput("click_method 'global'"); gated != nil {
			return *gated
		}
	}
	if clickMethod == "sky_click" {
		return textResult("click_method 'sky_click' is not supported on Windows", true)
	}
	if target != nil && elementIndex == "" && x != nil && y != nil {
		if gated := s.checkCoordinateGate(target, *x, *y); gated != nil {
			return *gated
		}
	}
	snapshot, key, appName, failure := s.actionContext(target, app)
	if failure != nil {
		return *failure
	}
	request := psRequest{
		Tool:         "click",
		App:          appName,
		X:            x,
		Y:            y,
		ClickCount:   clickCount,
		MouseButton:  mouseButton,
		ClickMethod:  clickMethod,
		WindowBounds: snapshot.WindowBounds,
	}
	if target != nil {
		request.WindowID = target.ID
	}
	if elementIndex != "" {
		record, err := lookupElement(snapshot, elementIndex)
		if err != nil {
			return textResult(err.Error(), true)
		}
		request.Element = record
	}
	return s.actionResult(key, request)
}

func (s *service) performSecondaryAction(target *windowRef, app, elementIndex, action string) toolCallResult {
	if elementIndex == "" {
		return textResult("Missing required argument: element_index", true)
	}
	if action == "" {
		return textResult("Missing required argument: action", true)
	}
	snapshot, key, appName, failure := s.actionContext(target, app)
	if failure != nil {
		return *failure
	}
	record, err := lookupElement(snapshot, elementIndex)
	if err != nil {
		return textResult(err.Error(), true)
	}
	request := psRequest{Tool: "perform_secondary_action", App: appName, Element: record, Action: strings.ToLower(action)}
	if target != nil {
		request.WindowID = target.ID
	}
	return s.actionResult(key, request)
}

func (s *service) scrollByPages(target *windowRef, app, direction, elementIndex string, pages float64, inputMethod string) toolCallResult {
	if elementIndex == "" {
		return textResult("scroll requires either element_index + direction (page mode) or x/y + scrollX/scrollY (coordinate mode)", true)
	}
	normalized := strings.ToLower(direction)
	if normalized != "up" && normalized != "down" && normalized != "left" && normalized != "right" {
		return textResult("Invalid scroll direction: "+direction, true)
	}
	if pages <= 0 {
		return textResult("pages must be > 0", true)
	}
	snapshot, key, appName, failure := s.actionContext(target, app)
	if failure != nil {
		return *failure
	}
	record, err := lookupElement(snapshot, elementIndex)
	if err != nil {
		return textResult(err.Error(), true)
	}
	request := psRequest{Tool: "scroll", App: appName, Element: record, Direction: normalized, Pages: pages, InputMethod: inputMethod}
	if target != nil {
		request.WindowID = target.ID
	}
	return s.actionResult(key, request)
}

func (s *service) scrollAtPoint(target *windowRef, app string, x, y, scrollX, scrollY *float64, inputMethod string) toolCallResult {
	if x == nil || y == nil {
		return textResult("coordinate scroll requires both x and y (window-relative)", true)
	}
	if (scrollX == nil || *scrollX == 0) && (scrollY == nil || *scrollY == 0) {
		return textResult("coordinate scroll requires a non-zero scrollX or scrollY pixel delta", true)
	}
	if target != nil {
		if gated := s.checkCoordinateGate(target, *x, *y); gated != nil {
			return *gated
		}
	}
	snapshot, key, appName, failure := s.actionContext(target, app)
	if failure != nil {
		return *failure
	}
	request := psRequest{
		Tool:         "scroll",
		App:          appName,
		X:            x,
		Y:            y,
		ScrollX:      scrollX,
		ScrollY:      scrollY,
		WindowBounds: snapshot.WindowBounds,
		InputMethod:  inputMethod,
	}
	if target != nil {
		request.WindowID = target.ID
	}
	return s.actionResult(key, request)
}

func (s *service) drag(target *windowRef, app string, fromX, fromY, toX, toY *float64, inputMethod string) toolCallResult {
	if fromX == nil {
		return textResult("Missing required argument: from_x", true)
	}
	if fromY == nil {
		return textResult("Missing required argument: from_y", true)
	}
	if toX == nil {
		return textResult("Missing required argument: to_x", true)
	}
	if toY == nil {
		return textResult("Missing required argument: to_y", true)
	}
	if target != nil {
		if gated := s.checkCoordinateGate(target, *fromX, *fromY); gated != nil {
			return *gated
		}
		if gated := s.checkCoordinateGate(target, *toX, *toY); gated != nil {
			return *gated
		}
	}
	snapshot, key, appName, failure := s.actionContext(target, app)
	if failure != nil {
		return *failure
	}
	request := psRequest{Tool: "drag", App: appName, FromX: fromX, FromY: fromY, ToX: toX, ToY: toY, WindowBounds: snapshot.WindowBounds, InputMethod: inputMethod}
	if target != nil {
		request.WindowID = target.ID
	}
	return s.actionResult(key, request)
}

func (s *service) typeText(target *windowRef, app, text, inputMethod string) toolCallResult {
	if text == "" {
		return textResult("Missing required argument: text", true)
	}
	// Official SendInput ceiling, enforced for every input_method.
	if len(utf16.Encode([]rune(text))) > maxSendInputUTF16Units {
		return textResult("text is too large for SendInput (max 8192 UTF-16 code units)", true)
	}
	_, key, appName, failure := s.actionContext(target, app)
	if failure != nil {
		return *failure
	}
	request := psRequest{Tool: "type_text", App: appName, Text: text, InputMethod: inputMethod}
	if target != nil {
		request.WindowID = target.ID
	}
	return s.actionResult(key, request)
}

func (s *service) pressKey(target *windowRef, app, key, inputMethod string) toolCallResult {
	_, keyName, appName, failure := s.actionContext(target, app)
	if failure != nil {
		return *failure
	}
	request := psRequest{Tool: "press_key", App: appName, Key: key, InputMethod: inputMethod}
	if target != nil {
		request.WindowID = target.ID
	}
	return s.actionResult(keyName, request)
}

func (s *service) setValue(target *windowRef, app, elementIndex, value string) toolCallResult {
	if elementIndex == "" {
		return textResult("Missing required argument: element_index", true)
	}
	snapshot, key, appName, failure := s.actionContext(target, app)
	if failure != nil {
		return *failure
	}
	record, err := lookupElement(snapshot, elementIndex)
	if err != nil {
		return textResult(err.Error(), true)
	}
	request := psRequest{Tool: "set_value", App: appName, Element: record, Value: value}
	if target != nil {
		request.WindowID = target.ID
	}
	return s.actionResult(key, request)
}

func (s *service) actionResult(key string, request psRequest) toolCallResult {
	snapshot, result := s.refreshSnapshot(key, request)
	if result.IsError {
		return result
	}
	// Any successful action invalidates cached screenshot ids for the window
	// (official semantics: ids are only valid for the producing observation).
	if request.WindowID != 0 {
		delete(s.screenshots, request.WindowID)
	}
	return snapshot.result()
}

func (s *service) currentSnapshot(app string) *appSnapshot {
	return s.snapshots[strings.ToLower(app)]
}

func (s *service) refreshSnapshot(app string, request psRequest) (*appSnapshot, toolCallResult) {
	response, err := runRuntimeOperation(request)
	if err != nil {
		return nil, textResult(err.Error(), true)
	}
	if !response.OK {
		return nil, textResult(response.Error, true)
	}
	if response.Snapshot == nil {
		return nil, textResult("Windows runtime did not return an app snapshot.", true)
	}
	s.rememberSnapshot(app, response.Snapshot)
	return response.Snapshot, toolCallResult{}
}

func (s *service) rememberSnapshot(query string, snapshot *appSnapshot) {
	keys := []string{query, snapshot.App.Name, snapshot.App.BundleIdentifier, strconv.Itoa(snapshot.App.PID)}
	if snapshot.WindowHandle > 0 {
		keys = append(keys, windowKey(snapshot.WindowHandle))
	}
	for _, key := range keys {
		s.cacheSnapshot(key, snapshot)
	}
}

func lookupElement(snapshot *appSnapshot, elementIndex string) (*elementRecord, error) {
	index, err := strconv.Atoi(elementIndex)
	if err != nil {
		return nil, fmt.Errorf("unknown element_index %q", elementIndex)
	}
	for _, record := range snapshot.Elements {
		if record.Index == index {
			copy := record
			return &copy, nil
		}
	}
	return nil, fmt.Errorf("unknown element_index %q", elementIndex)
}

// runtimeBackend is the in-process runtime operation surface. The Windows
// build wires the native Go implementation (native_backend.go) into
// nativeRuntime via init(); it stays nil on other platforms so this module
// builds everywhere its tests run.
type runtimeBackend interface {
	// call executes one operation. A returned error means the runtime could
	// not execute the operation at all; a domain failure is a *psResponse
	// with OK=false.
	call(req psRequest) (*psResponse, error)
}

var nativeRuntime runtimeBackend

// runtimeEnvFlags lists the environment variables the runtime consults per
// operation. They are folded into every request so request-time semantics
// are identical regardless of when the process started.
var runtimeEnvFlags = []string{
	"OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT",
	"OPEN_COMPUTER_USE_WINDOWS_ALLOW_APP_LAUNCH",
	"OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOCUS_ACTIONS",
	"OPEN_COMPUTER_USE_WINDOWS_ALLOW_UIA_TEXT_FALLBACK",
	"OPEN_COMPUTER_USE_WINDOWS_CAPTURE",
}

// windowsBackendEnv selected the runtime backend when the PS-era daemon
// still existed. The runtime is now always the in-process Go implementation;
// the variable is recognized only to warn that it no longer does anything.
const windowsBackendEnv = "OPEN_COMPUTER_USE_WINDOWS_BACKEND"

// warnDeprecatedBackendEnv prints one deprecation warning when the retired
// backend selector is still set, then ignores it.
func warnDeprecatedBackendEnv() {
	if strings.TrimSpace(os.Getenv(windowsBackendEnv)) != "" {
		fmt.Fprintf(os.Stderr, "[deprecated] %s is no longer used: the Windows runtime is always the in-process Go implementation.\n", windowsBackendEnv)
	}
}

// prepareRuntimeRequestEnv folds the current env flag values (and the
// foreground-input gate result) into the request so the runtime observes
// request-time semantics for every flag.
func prepareRuntimeRequestEnv(request psRequest) psRequest {
	request.AllowForegroundInput = envFlagEnabled(foregroundInputFlag)
	flags := make(map[string]string, len(runtimeEnvFlags))
	for _, name := range runtimeEnvFlags {
		flags[name] = os.Getenv(name)
	}
	request.EnvFlags = flags
	return request
}

// runRuntimeOperation is the single runtime boundary used by the tool layer.
// It dispatches to the in-process native runtime (the retired PS/C#
// runtime used to sit behind this same boundary).
func runRuntimeOperation(request psRequest) (*psResponse, error) {
	request = prepareRuntimeRequestEnv(request)
	if nativeRuntime == nil {
		return nil, errors.New("Windows Computer Use runtime requires the Windows desktop session")
	}
	return nativeRuntime.call(request)
}

func requiredString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func optionalString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func requiredElementIndex(args map[string]any) string {
	return strings.TrimSpace(optionalElementIndex(args))
}

func optionalElementIndex(args map[string]any) string {
	return elementIndexString(args["element_index"])
}

func elementIndexString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			return strconv.FormatInt(integer, 10)
		}
		if float, err := value.Float64(); err == nil {
			return integerElementIndexFloat(float)
		}
	case float64:
		return integerElementIndexFloat(value)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	}
	return ""
}

func integerElementIndexFloat(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
		return ""
	}
	return strconv.FormatInt(int64(value), 10)
}

func requiredFloat(args map[string]any, key string) *float64 {
	return optionalFloat(args, key)
}

func optionalFloat(args map[string]any, key string) *float64 {
	switch value := args[key].(type) {
	case float64:
		return &value
	case int:
		float := float64(value)
		return &float
	case json.Number:
		float, err := value.Float64()
		if err == nil {
			return &float
		}
	}
	return nil
}

func optionalTextLimit(args map[string]any, key string) (*textLimit, error) {
	value, ok := args[key]
	if !ok {
		return nil, nil
	}
	return textLimitFromValue(value, key)
}

func textLimitFromValue(value any, key string) (*textLimit, error) {
	if stringValue, ok := value.(string); ok {
		if strings.EqualFold(stringValue, "max") {
			return &textLimit{max: true}, nil
		}
		return nil, fmt.Errorf("%s must be a positive integer or max", key)
	}
	integer, err := positiveIntFromValue(value, key)
	if err != nil {
		return nil, fmt.Errorf("%s must be a positive integer or max", key)
	}
	return &textLimit{count: *integer}, nil
}

func optionalPositiveInt(args map[string]any, key string) (*int, error) {
	value, ok := args[key]
	if !ok {
		return nil, nil
	}
	return positiveIntFromValue(value, key)
}

func positiveIntFromValue(value any, key string) (*int, error) {
	switch typed := value.(type) {
	case int:
		return positiveIntFromInt64(int64(typed), key)
	case float64:
		if !isWholeNumber(typed) {
			return nil, fmt.Errorf("%s must be a positive integer", key)
		}
		return positiveIntFromFloat64(typed, key)
	case json.Number:
		integer, err := typed.Int64()
		if err != nil {
			return nil, fmt.Errorf("%s must be a positive integer", key)
		}
		return positiveIntFromInt64(integer, key)
	default:
		return nil, fmt.Errorf("%s must be a positive integer", key)
	}
}

func positiveIntFromFloat64(value float64, key string) (*int, error) {
	if !isWholeNumber(value) || value <= 0 || value > float64(maxInt()) {
		return nil, fmt.Errorf("%s must be a positive integer", key)
	}
	integer := int(value)
	return &integer, nil
}

func positiveIntFromInt64(value int64, key string) (*int, error) {
	if value <= 0 || value > int64(maxInt()) {
		return nil, fmt.Errorf("%s must be a positive integer", key)
	}
	integer := int(value)
	return &integer, nil
}

func isWholeNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && math.Trunc(value) == value
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

func intValue(value *float64, fallback int) int {
	if value == nil {
		return fallback
	}
	return int(*value)
}

func floatValue(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func parseClickMethod(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return "auto", nil
	}
	for _, candidate := range clickMethodValues {
		if normalized == candidate {
			return normalized, nil
		}
	}
	return "", fmt.Errorf("Invalid click_method %q. Expected one of: %s", value, strings.Join(clickMethodValues, ", "))
}

var inputMethodValues = []string{"auto", "global"}

// maxSendInputUTF16Units is the official SendInput ceiling for type_text.
const maxSendInputUTF16Units = 8192

// Service-layer safety ceilings: clamp absurd click_count/pages instead of
// looping input injection or scrolling for effectively unbounded time.
const (
	maxClickCount      = 100
	maxScrollPages     = 1000
	maxMCPRequestBytes = 64 << 20 // set_value/type payloads ride JSON-RPC lines
)

// clampClickCount bounds click_count to 1..maxClickCount.
func clampClickCount(value int) int {
	if value < 1 {
		return 1
	}
	if value > maxClickCount {
		return maxClickCount
	}
	return value
}

// clampScrollPages bounds pages to at most maxScrollPages (>0 is validated
// separately by the scroll tools).
func clampScrollPages(value float64) float64 {
	if value > maxScrollPages {
		return maxScrollPages
	}
	return value
}

// parseInputMethod validates the optional input_method parameter on the
// action tools (auto = UIA/PostMessage background chain, global = real
// SendInput injection gated behind the foreground-input opt-in flag).
func parseInputMethod(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return "auto", nil
	}
	for _, candidate := range inputMethodValues {
		if normalized == candidate {
			return normalized, nil
		}
	}
	return "", fmt.Errorf("Invalid input_method %q. Expected one of: %s", value, strings.Join(inputMethodValues, ", "))
}

const foregroundInputFlag = "OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT"

func envFlagEnabled(name string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// gateForegroundInput rejects real-pointer/keyboard injection before any
// snapshot lookup unless the explicit opt-in flag is set. The runtime script
// enforces the same gate on its side (defense in depth).
func gateForegroundInput(action string) *toolCallResult {
	if envFlagEnabled(foregroundInputFlag) {
		return nil
	}
	return &toolCallResult{
		Content: []contentItem{{Type: "text", Text: fmt.Sprintf("%s requires %s=1 because it moves the real mouse pointer and changes foreground focus. Set %s=1 to enable it.", action, foregroundInputFlag, foregroundInputFlag)}},
		IsError: true,
	}
}

// parseMouseButton normalizes the official MouseButton aliases (l/r/m) and
// defaults to left, mirroring the window2 click defaults.
func parseMouseButton(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "":
		return "left", nil
	case "l":
		return "left", nil
	case "r":
		return "right", nil
	case "m":
		return "middle", nil
	case "left", "right", "middle":
		return normalized, nil
	}
	return "", fmt.Errorf("Invalid mouse_button %q. Expected one of: %s", value, strings.Join(mouseButtonValues, ", "))
}

// validateKeyChord rejects Windows/Meta key chords per the official
// non-negotiable safety policy before anything reaches the runtime.
func validateKeyChord(key string) error {
	for _, part := range strings.Split(key, "+") {
		normalized := strings.ToLower(strings.TrimSpace(part))
		for _, banned := range bannedModifierNames {
			if normalized == banned {
				return fmt.Errorf("press_key with the Windows/Meta key (%s) is not permitted by the official Computer Use safety policy.", part)
			}
		}
	}
	return nil
}

// deniedAppName reports whether the app identifier matches the official
// safety deny list (terminal hosts, password managers, security apps).
func deniedAppName(app string) (bool, string) {
	leaf := app
	if index := strings.LastIndexAny(leaf, `/\`); index >= 0 {
		leaf = leaf[index+1:]
	}
	leaf = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(leaf)), ".exe")
	if leaf == "" {
		return false, ""
	}
	for _, name := range deniedAppExactNames {
		if leaf == name {
			return true, leaf
		}
	}
	for _, pattern := range deniedAppPatterns {
		if strings.Contains(leaf, pattern) {
			return true, leaf
		}
	}
	lowered := strings.ToLower(app)
	for _, displayName := range deniedAppDisplayNames {
		if strings.Contains(lowered, displayName) {
			return true, leaf
		}
	}
	return false, ""
}

// optionalWindow parses the official `window` object ({app, id, title}) or
// the flat `window_id` alias from tool arguments.
func optionalWindow(args map[string]any) (*windowRef, error) {
	if value, ok := args["window_id"]; ok && args["window"] == nil {
		id, err := windowIDFromValue(value)
		if err != nil {
			return nil, err
		}
		if id > 0 {
			return &windowRef{ID: id}, nil
		}
		return nil, nil
	}
	raw, ok := args["window"]
	if !ok || raw == nil {
		return nil, nil
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("window must be an object with an integer id")
	}
	window := &windowRef{}
	if app, ok := object["app"].(string); ok {
		window.App = strings.TrimSpace(app)
	}
	if title, ok := object["title"].(string); ok {
		window.Title = title
	}
	id, err := windowIDFromValue(object["id"])
	if err != nil {
		return nil, err
	}
	window.ID = id
	if window.ID <= 0 {
		return nil, errors.New("window.id must be an integer >= 0")
	}
	return window, nil
}

func windowIDFromValue(value any) (int64, error) {
	switch value := value.(type) {
	case float64:
		if value != math.Trunc(value) {
			return 0, errors.New("window id must be an integer >= 0")
		}
		return int64(value), nil
	case json.Number:
		return value.Int64()
	case int:
		return int64(value), nil
	case int64:
		return value, nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return 0, errors.New("window id must be an integer >= 0")
		}
		return parsed, nil
	}
	return 0, nil
}

func toolDefinitions() []toolDefinition {
	windowArg := windowProperty()
	screenshotArg := stringProperty("Screenshot id from the latest get_window_state observation. Stale ids are rejected; re-observe after any state change.")
	return []toolDefinition{
		{
			Name:        "click",
			Description: "Click an element by index or pixel coordinates from screenshot. This tool is part of plugin `Computer Use`.",
			Annotations: defaultAnnotations(),
			InputSchema: objectSchema(map[string]any{
				"app":           stringProperty("App name or bundle identifier (legacy key-window targeting)"),
				"window":        windowArg,
				"element_index": stringProperty("Element index to click"),
				"x":             numberProperty("X coordinate in screenshot pixel coordinates"),
				"y":             numberProperty("Y coordinate in screenshot pixel coordinates"),
				"click_count":   integerProperty("Number of clicks. Defaults to 1"),
				"mouse_button":  enumStringProperty("Mouse button to click. Defaults to left.", mouseButtonValues),
				"click_method":  enumStringProperty("Click implementation: auto (default), accessibility, app_post, sky_click, or global. Accessibility requires element_index. Windows supports app_post through HWND messages; global moves the real pointer (activate + SendInput) and requires OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT=1; sky_click is not supported on Windows.", clickMethodValues),
				"screenshotId":  screenshotArg,
			}, nil),
		},
		{
			Name:        "drag",
			Description: "Drag from one point to another using pixel coordinates. This tool is part of plugin `Computer Use`.",
			Annotations: defaultAnnotations(),
			InputSchema: objectSchema(map[string]any{
				"app":          stringProperty("App name or bundle identifier (legacy key-window targeting)"),
				"window":       windowArg,
				"from_x":       numberProperty("Start X coordinate"),
				"from_y":       numberProperty("Start Y coordinate"),
				"to_x":         numberProperty("End X coordinate"),
				"to_y":         numberProperty("End Y coordinate"),
				"input_method": enumStringProperty("Input injection: auto (default) keeps the background UIA/window-message chain; global activates the window and injects real SendInput (requires OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT=1).", inputMethodValues),
				"screenshotId": screenshotArg,
			}, []string{"from_x", "from_y", "to_x", "to_y"}),
		},
		{
			Name:        "get_app_state",
			Description: "Get the state of an already running app's key window and return a screenshot and accessibility tree. This must be called once per assistant turn before interacting with the app. This tool is part of plugin `Computer Use`.",
			Annotations: readOnlyAnnotations(),
			InputSchema: objectSchema(map[string]any{
				"app":            stringProperty("App name or bundle identifier"),
				"text_limit":     textLimitProperty("Maximum text characters to return. Use \"max\" for full text. Defaults to 500."),
				"max_tree_nodes": positiveIntegerProperty("Maximum accessibility tree nodes to render. Defaults to 1200."),
				"max_tree_depth": positiveIntegerProperty("Maximum accessibility tree depth to render. Defaults to 64."),
			}, []string{"app"}),
		},
		{
			Name:        "list_apps",
			Description: "List the apps on this computer. Returns the set of apps that are currently running, as well as any that have been used in the last 14 days, including details on usage frequency. This tool is part of plugin `Computer Use`.",
			Annotations: readOnlyAnnotations(),
			InputSchema: objectSchema(map[string]any{}, nil),
		},
		{
			Name:        "perform_secondary_action",
			Description: "Invoke a secondary accessibility action exposed by an element. This tool is part of plugin `Computer Use`.",
			Annotations: defaultAnnotations(),
			InputSchema: objectSchema(map[string]any{
				"app":           stringProperty("App name or bundle identifier (legacy key-window targeting)"),
				"window":        windowArg,
				"element_index": stringProperty("Element identifier"),
				"action":        stringProperty("Secondary accessibility action name (case-insensitive)"),
			}, []string{"element_index", "action"}),
		},
		{
			Name:        "press_key",
			Description: "Press a key or key-combination on the keyboard, including modifier and navigation keys.\n  - This supports xdotool's `key` syntax.\n  - Examples: \"a\", \"Return\", \"Tab\", \"ctrl+c\", \"Control_L+Shift_L+period\", \"Up\", \"KP_0\" (for the numpad 0). Windows/Meta key chords are rejected. This tool is part of plugin `Computer Use`.",
			Annotations: defaultAnnotations(),
			InputSchema: objectSchema(map[string]any{
				"app":          stringProperty("App name or bundle identifier (legacy key-window targeting)"),
				"window":       windowArg,
				"key":          stringProperty("Key or key-combination to press"),
				"input_method": enumStringProperty("Input injection: auto (default) keeps the background UIA/window-message chain; global activates the window and injects real SendInput (requires OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT=1).", inputMethodValues),
			}, []string{"key"}),
		},
		{
			Name:        "scroll",
			Description: "Scroll an element in a direction by a number of pages, or scroll by pixel deltas from a window-relative coordinate (official window2 mode: pass x/y plus scrollX/scrollY; negative scrollY scrolls up, negative scrollX scrolls left; do not pass element_index in this mode). This tool is part of plugin `Computer Use`.",
			Annotations: defaultAnnotations(),
			InputSchema: objectSchema(map[string]any{
				"app":           stringProperty("App name or bundle identifier (legacy key-window targeting)"),
				"window":        windowArg,
				"element_index": stringProperty("Element identifier (page mode only)"),
				"direction":     stringProperty("Scroll direction: up, down, left, or right (page mode only)"),
				"pages":         numberProperty("Number of pages to scroll. Fractional values are supported. Defaults to 1"),
				"x":             numberProperty("Window-relative X coordinate to scroll from (coordinate mode)"),
				"y":             numberProperty("Window-relative Y coordinate to scroll from (coordinate mode)"),
				"scrollX":       numberProperty("Horizontal pixel delta: negative scrolls left, positive scrolls right (coordinate mode)"),
				"scrollY":       numberProperty("Vertical pixel delta: negative scrolls up, positive scrolls down (coordinate mode)"),
				"input_method":  enumStringProperty("Input injection: auto (default) keeps the background UIA/window-message chain; global activates the window and injects real SendInput (requires OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT=1).", inputMethodValues),
				"screenshotId":  screenshotArg,
			}, nil),
		},
		{
			Name:        "set_value",
			Description: "Set the value of a settable accessibility element. This tool is part of plugin `Computer Use`.",
			Annotations: defaultAnnotations(),
			InputSchema: objectSchema(map[string]any{
				"app":           stringProperty("App name or bundle identifier (legacy key-window targeting)"),
				"window":        windowArg,
				"element_index": stringProperty("Element identifier"),
				"value":         stringProperty("Value to assign"),
			}, []string{"element_index", "value"}),
		},
		{
			Name:        "type_text",
			Description: "Type literal text using keyboard input. This tool is part of plugin `Computer Use`.",
			Annotations: defaultAnnotations(),
			InputSchema: objectSchema(map[string]any{
				"app":          stringProperty("App name or bundle identifier (legacy key-window targeting)"),
				"window":       windowArg,
				"text":         stringProperty("Literal text to type"),
				"input_method": enumStringProperty("Input injection: auto (default) keeps the background UIA/window-message chain; global activates the window and injects real SendInput (requires OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT=1).", inputMethodValues),
			}, []string{"text"}),
		},
		{
			Name:        "list_windows",
			Description: "List the currently open windows that can be targeted by window-based tools, including secondary and modal windows. This tool is part of plugin `Computer Use`.",
			Annotations: readOnlyAnnotations(),
			InputSchema: objectSchema(map[string]any{}, nil),
		},
		{
			Name:        "get_window",
			Description: "Rehydrate a currently open window by its opaque id. Useful to recover a window binding after an error. This tool is part of plugin `Computer Use`.",
			Annotations: readOnlyAnnotations(),
			InputSchema: objectSchema(map[string]any{
				"app":    stringProperty("Optional app identifier carried forward from the original window"),
				"window": windowArg,
			}, nil),
		},
		{
			Name:        "launch_app",
			Description: "Launch an app by its id from list_apps or an explicit .exe path so its window can be targeted. Terminal, password-manager, and security apps are never launched (official safety policy). This tool is part of plugin `Computer Use`.",
			Annotations: defaultAnnotations(),
			InputSchema: objectSchema(map[string]any{
				"app": stringProperty("App id from list_apps or an explicit .exe process path"),
			}, []string{"app"}),
		},
		{
			Name:        "get_window_state",
			Description: "Capture the state of a window: a screenshot and/or a structured accessibility tree. Coordinate actions should pass the returned screenshot id. This tool is part of plugin `Computer Use`.",
			Annotations: readOnlyAnnotations(),
			InputSchema: objectSchema(map[string]any{
				"window":             windowArg,
				"include_screenshot": booleanProperty("Include window screenshots. Defaults to true"),
				"include_text":       booleanProperty("Include structured text fields (focused_element, selected_text) in the accessibility state. Defaults to false"),
				"text_limit":         textLimitProperty("Maximum text characters to return. Use \"max\" for full text. Defaults to 500."),
				"max_tree_nodes":     positiveIntegerProperty("Maximum accessibility tree nodes to render. Defaults to 1200."),
				"max_tree_depth":     positiveIntegerProperty("Maximum accessibility tree depth to render. Defaults to 64."),
			}, []string{"window"}),
		},
		{
			Name:        "activate_window",
			Description: "Bring a window to the foreground. Input methods activate their target window automatically; use this only as an escape hatch. Requires the focus opt-in flag on Windows. This tool is part of plugin `Computer Use`.",
			Annotations: defaultAnnotations(),
			InputSchema: objectSchema(map[string]any{
				"window": windowArg,
			}, []string{"window"}),
		},
	}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func defaultAnnotations() map[string]any {
	return map[string]any{"destructiveHint": false, "openWorldHint": false}
}

func readOnlyAnnotations() map[string]any {
	return map[string]any{"destructiveHint": false, "idempotentHint": true, "openWorldHint": false, "readOnlyHint": true}
}

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// windowProperty describes the official window2 `Window` argument. The id is
// an opaque handle (HWND on Windows; CGWindowID on macOS; X window id on
// Linux) that must come from list_windows/get_window_state, never constructed.
func windowProperty() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Target window from list_windows/get_window_state. Takes precedence over the legacy app argument. The id is an opaque handle; never construct it yourself.",
		"properties": map[string]any{
			"app":   stringProperty("App identifier owning the window"),
			"id":    map[string]any{"type": "integer", "minimum": 0, "description": "Opaque window id from list_windows/get_window_state"},
			"title": stringProperty("Optional user-visible window title"),
		},
		"required":             []string{"id"},
		"additionalProperties": false,
	}
}

func booleanProperty(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func enumStringProperty(description string, values []string) map[string]any {
	property := stringProperty(description)
	property["enum"] = values
	return property
}

func numberProperty(description string) map[string]any {
	return map[string]any{"type": "number", "description": description}
}

func integerProperty(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func positiveIntegerProperty(description string) map[string]any {
	return map[string]any{"type": "integer", "minimum": 1, "description": description}
}

func textLimitProperty(description string) map[string]any {
	return map[string]any{
		"anyOf": []any{
			map[string]any{"type": "integer", "minimum": 1},
			map[string]any{"type": "string", "enum": []string{"max"}},
		},
		"description": description,
	}
}

func main() {
	warnDeprecatedBackendEnv()
	if err := runCLI(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCLI(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, helpText(""))
		return nil
	}

	switch args[0] {
	case "-h", "--help", "help":
		topic := ""
		if len(args) > 1 {
			topic = args[1]
		}
		fmt.Fprint(stdout, helpText(topic))
		return nil
	case "-v", "--version", "version":
		fmt.Fprintln(stdout, version)
		return nil
	case "mcp":
		return runMCP(os.Stdin, stdout)
	case "doctor":
		fmt.Fprintln(stdout, "Windows runtime: UI Automation and Win32 window-message bridge are available when this process runs in the signed-in desktop session.")
		return nil
	case "list-apps":
		result := newService().callTool("list_apps", map[string]any{})
		if result.IsError {
			return errors.New(result.Content[0].Text)
		}
		fmt.Fprintln(stdout, result.Content[0].Text)
		return nil
	case "snapshot":
		app, textLimit, maxTreeNodes, maxTreeDepth, err := parseSnapshotArgs(args[1:])
		if err != nil {
			return err
		}
		toolArgs := map[string]any{
			"app": app,
		}
		if textLimit != nil {
			toolArgs["text_limit"] = textLimit.runtimeValue()
		}
		if maxTreeNodes != nil {
			toolArgs["max_tree_nodes"] = *maxTreeNodes
		}
		if maxTreeDepth != nil {
			toolArgs["max_tree_depth"] = *maxTreeDepth
		}
		result := newService().callTool("get_app_state", toolArgs)
		if result.IsError {
			return errors.New(result.Content[0].Text)
		}
		fmt.Fprintln(stdout, result.Content[0].Text)
		return nil
	case "capture":
		// Internal: sacrificial screenshot worker. Runs the WGC/PrintWindow
		// chain in this short-lived process so the driver's memory-stomping
		// failure mode cannot touch a long-lived parent (see native_capture.go).
		return runCaptureCommand(args[1:], stdout)
	case "op":
		// Internal: sacrificial runtime worker. Executes one psRequest against
		// the in-process native runtime and prints one psResponse JSON line,
		// keeping every native call (Win32/UIA/capture) out of the long-lived
		// MCP/CLI parent (see native_op_client.go).
		return runOperationCommand(os.Stdin, stdout)
	case "call":
		output, hasError, err := runCallCommand(args[1:], newService())
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(encoded))
		if hasError {
			return errors.New("tool call returned isError=true")
		}
		return nil
	case "screenshot":
		return runScreenshotCommand(args[1:], stdout)
	case "cursor-position":
		return runCursorPositionCommand(args[1:], stdout)
	case "input":
		return runInputCommand(args[1:], stdout)
	case "record":
		return runRecordCommand(args[1:], stdout)
	default:
		return fmt.Errorf("unknown command: %s\n\n%s", args[0], helpText(""))
	}
}

func parseSnapshotArgs(args []string) (string, *textLimit, *int, *int, error) {
	var app string
	var textLimit *textLimit
	var maxTreeNodes *int
	var maxTreeDepth *int
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--text-limit":
			index++
			if index >= len(args) {
				return "", nil, nil, nil, errors.New("--text-limit requires a positive integer or max value")
			}
			value, err := parseTextLimitOption(args[index], "--text-limit")
			if err != nil {
				return "", nil, nil, nil, err
			}
			textLimit = value
		case "--max-tree-nodes":
			index++
			if index >= len(args) {
				return "", nil, nil, nil, errors.New("--max-tree-nodes requires a positive integer value")
			}
			value, err := parsePositiveIntegerOption(args[index], "--max-tree-nodes")
			if err != nil {
				return "", nil, nil, nil, err
			}
			maxTreeNodes = &value
		case "--max-tree-depth":
			index++
			if index >= len(args) {
				return "", nil, nil, nil, errors.New("--max-tree-depth requires a positive integer value")
			}
			value, err := parsePositiveIntegerOption(args[index], "--max-tree-depth")
			if err != nil {
				return "", nil, nil, nil, err
			}
			maxTreeDepth = &value
		default:
			if strings.HasPrefix(arg, "-") {
				return "", nil, nil, nil, fmt.Errorf("unknown snapshot option: %s", arg)
			}
			if app != "" {
				return "", nil, nil, nil, errors.New("snapshot accepts exactly one app name, process name, window title, or pid")
			}
			app = arg
		}
	}
	if app == "" {
		return "", nil, nil, nil, errors.New("snapshot requires an app name, process name, window title, or pid")
	}
	return app, textLimit, maxTreeNodes, maxTreeDepth, nil
}

func parseTextLimitOption(value, option string) (*textLimit, error) {
	if strings.EqualFold(value, "max") {
		return &textLimit{max: true}, nil
	}
	integer, err := strconv.Atoi(value)
	if err != nil || integer <= 0 {
		return nil, fmt.Errorf("%s must be a positive integer or max", option)
	}
	return &textLimit{count: integer}, nil
}

func parsePositiveIntegerOption(value, option string) (int, error) {
	integer, err := strconv.Atoi(value)
	if err != nil || integer <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", option)
	}
	return integer, nil
}

func runCallCommand(args []string, svc *service) (any, bool, error) {
	if len(args) == 0 {
		return nil, false, errors.New("call requires a tool name or --calls/--calls-file")
	}

	var toolName, argsJSON, argsFile, callsJSON, callsFile string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--args":
			index++
			if index >= len(args) {
				return nil, false, errors.New("--args requires a value")
			}
			argsJSON = args[index]
		case "--args-file":
			index++
			if index >= len(args) {
				return nil, false, errors.New("--args-file requires a value")
			}
			argsFile = args[index]
		case "--calls":
			index++
			if index >= len(args) {
				return nil, false, errors.New("--calls requires a value")
			}
			callsJSON = args[index]
		case "--calls-file":
			index++
			if index >= len(args) {
				return nil, false, errors.New("--calls-file requires a value")
			}
			callsFile = args[index]
		default:
			if strings.HasPrefix(arg, "-") {
				return nil, false, fmt.Errorf("unknown call option: %s", arg)
			}
			if toolName != "" {
				return nil, false, errors.New("call accepts at most one tool name")
			}
			toolName = arg
		}
	}

	if callsJSON != "" || callsFile != "" {
		if toolName != "" || argsJSON != "" || argsFile != "" {
			return nil, false, errors.New("call sequence does not accept a tool name, --args, or --args-file")
		}
		calls, err := readCallSequence(callsJSON, callsFile)
		if err != nil {
			return nil, false, err
		}
		var outputs []map[string]any
		hasError := false
		for _, call := range calls {
			result := svc.callTool(call.Tool, call.Args)
			outputs = append(outputs, map[string]any{"tool": call.Tool, "result": result})
			if result.IsError {
				hasError = true
				break
			}
		}
		return outputs, hasError, nil
	}

	if toolName == "" {
		return nil, false, errors.New("call requires a tool name or --calls/--calls-file")
	}
	arguments, err := readArguments(argsJSON, argsFile)
	if err != nil {
		return nil, false, err
	}
	result := svc.callTool(toolName, arguments)
	return result, result.IsError, nil
}

type callSpec struct {
	Tool string
	Args map[string]any
}

func readArguments(inline, file string) (map[string]any, error) {
	if inline != "" && file != "" {
		return nil, errors.New("Use either inline JSON or a JSON file, not both")
	}
	if inline == "" && file == "" {
		return map[string]any{}, nil
	}
	source, err := readJSONSource(inline, file)
	if err != nil {
		return nil, err
	}
	var args map[string]any
	decoder := json.NewDecoder(strings.NewReader(source))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("Invalid JSON input: %w", err)
	}
	if args == nil {
		return nil, errors.New("--args must be a JSON object")
	}
	return args, nil
}

func readCallSequence(inline, file string) ([]callSpec, error) {
	if inline != "" && file != "" {
		return nil, errors.New("Use either --calls or --calls-file, not both")
	}
	source, err := readJSONSource(inline, file)
	if err != nil {
		return nil, err
	}
	var raw []map[string]any
	decoder := json.NewDecoder(strings.NewReader(source))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("Invalid JSON input: %w", err)
	}
	calls := make([]callSpec, 0, len(raw))
	for index, item := range raw {
		name, _ := item["tool"].(string)
		if name == "" {
			name, _ = item["name"].(string)
		}
		if name == "" {
			return nil, fmt.Errorf("call sequence item #%d requires a non-empty tool", index+1)
		}
		args, _ := item["args"].(map[string]any)
		if args == nil {
			args, _ = item["arguments"].(map[string]any)
		}
		if args == nil {
			args = map[string]any{}
		}
		calls = append(calls, callSpec{Tool: name, Args: args})
	}
	return calls, nil
}

func readJSONSource(inline, file string) (string, error) {
	if inline != "" {
		return inline, nil
	}
	if file == "" {
		return "", errors.New("JSON input is required")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// runOperationCommand implements the internal op subcommand: read one
// psRequest JSON document from stdin, execute it against the in-process
// native runtime, print one psResponse JSON line on stdout. Any failure exits
// non-zero with no JSON so the parent retries on a fresh worker.
func runOperationCommand(stdin io.Reader, stdout io.Writer) error {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	var request psRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return err
	}
	response, err := (&nativeBackendImpl{}).call(request)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(encoded))
	return err
}

// runCaptureCommand implements the internal capture subcommand: prints the
// base64 PNG (or nothing) on stdout; forced-mode failures print ERR:<message>.
func runCaptureCommand(args []string, stdout io.Writer) error {
	var hwnd int64
	mode := "auto"
	var boundsVals [4]float64
	haveBounds := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--hwnd":
			index++
			if index >= len(args) {
				return errors.New("--hwnd requires a value")
			}
			value, err := strconv.ParseInt(args[index], 10, 64)
			if err != nil {
				return errors.New("--hwnd must be an integer")
			}
			hwnd = value
		case "--mode":
			index++
			if index >= len(args) {
				return errors.New("--mode requires a value")
			}
			mode = args[index]
		case "--bounds":
			if index+4 >= len(args) {
				return errors.New("--bounds requires four numbers")
			}
			for i := 0; i < 4; i++ {
				value, err := strconv.ParseFloat(args[index+1+i], 64)
				if err != nil {
					return errors.New("--bounds values must be numbers")
				}
				boundsVals[i] = value
			}
			index += 4
			haveBounds = true
		default:
			return fmt.Errorf("unknown capture option: %s", args[index])
		}
	}
	if hwnd <= 0 {
		return errors.New("capture requires --hwnd")
	}
	var bounds *frame
	if haveBounds {
		bounds = &frame{X: boundsVals[0], Y: boundsVals[1], Width: boundsVals[2], Height: boundsVals[3]}
	}
	req := psRequest{EnvFlags: map[string]string{"OPEN_COMPUTER_USE_WINDOWS_CAPTURE": mode}}
	png, err := captureWindowPngDirect(req, windows.HWND(hwnd), bounds)
	if err != nil {
		fmt.Fprintf(stdout, "ERR:%s", err.Error())
		return nil
	}
	if png != "" {
		fmt.Fprintln(stdout, png)
	}
	return nil
}

func runMCP(stdin io.Reader, stdout io.Writer) error {
	svc := newService()
	// One JSON-RPC request per line. A streaming json.Decoder cannot recover
	// from a malformed frame: the bad bytes are never consumed, so Decode
	// fails in a hot loop and every subsequent request is starved.
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), maxMCPRequestBytes)
	encoder := json.NewEncoder(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var request map[string]any
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			_ = encoder.Encode(jsonRPCError(nil, -32700, "Invalid JSON-RPC payload"))
			continue
		}
		response := handleMCPRequest(request, svc)
		if response != nil {
			if err := encoder.Encode(response); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func handleMCPRequest(request map[string]any, svc *service) map[string]any {
	id := request["id"]
	method, _ := request["method"].(string)
	params, _ := request["params"].(map[string]any)
	switch method {
	case "initialize":
		return jsonRPCResult(id, map[string]any{
			"protocolVersion": "2025-03-26",
			"serverInfo": map[string]any{
				"name":    "open-computer-use",
				"version": version,
			},
			"capabilities": map[string]any{"tools": map[string]any{"listChanged": false}},
			"instructions": serverInstructions,
		})
	case "notifications/initialized", "notifications/turn-ended":
		return nil
	case "ping":
		return jsonRPCResult(id, map[string]any{})
	case "tools/list":
		return jsonRPCResult(id, map[string]any{"tools": toolDefinitions()})
	case "tools/call":
		name, _ := params["name"].(string)
		arguments, _ := params["arguments"].(map[string]any)
		if arguments == nil {
			arguments = map[string]any{}
		}
		return jsonRPCResult(id, svc.callTool(name, arguments))
	default:
		if method == "" {
			return nil
		}
		return jsonRPCError(id, -32601, "Method not found: "+method)
	}
}

func jsonRPCResult(id any, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func jsonRPCError(id any, code int, message string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	}
}

func helpText(command string) string {
	switch command {
	case "mcp":
		return "Usage:\n  open-computer-use.exe mcp\n\nStart the stdio MCP server.\n"
	case "call":
		return "Usage:\n  open-computer-use.exe call <tool> [--args '<json-object>']\n  open-computer-use.exe call --calls '<json-array>'\n\nThe JSON array form keeps all calls in one process so element_index state can be reused.\n"
	case "snapshot":
		return "Usage:\n  open-computer-use.exe snapshot [--text-limit <positive-int|max>] [--max-tree-nodes <positive-int>] [--max-tree-depth <positive-int>] <app>\n\nPrint the current Windows UI Automation snapshot for the target app.\n"
	case "screenshot":
		return "Usage:\n  open-computer-use.exe screenshot [--output <path.png>]\n\nCapture the whole Windows virtual desktop (all monitors) to PNG via a GDI\nscreen read. With --output the PNG is written to that path; otherwise base64\nPNG is printed.\n"
	case "cursor-position":
		return "Usage:\n  open-computer-use.exe cursor-position\n\nPrint the mouse pointer position (virtual-screen coordinates) and the desktop\nsize as JSON, mirroring the Linux runtime's cursor-position output.\n"
	case "input":
		return "Usage:\n  open-computer-use.exe input <action> [--api-size WxH] [options]\n\nActions (global synthetic input via SendInput):\n  move <x> <y>\n  click [--button left|right|middle] [--count N] [--modifiers ctrl+shift] [--x X --y Y]\n  mouse_down|mouse_up [--button left|right|middle] [--modifiers ...] [--x X --y Y]\n  drag <from_x> <from_y> <to_x> <to_y> [--button left]\n  scroll <up|down|left|right> [--amount N] [--modifiers ...] [--x X --y Y]\n  type <text>                 newlines become Return; long text is batched\n  key <key-or-chord> [--hold-ms N]   e.g. ctrl+s, Return (Windows/Meta denied)\n  wait <seconds>\n\n--api-size (or OPEN_COMPUTER_USE_API_SIZE) maps model/API coordinates to the\nvirtual desktop when the display size is known. Every action except wait moves\nthe real pointer/keyboard and requires OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT=1.\n"
	case "record":
		return "Usage:\n  open-computer-use.exe record start [--output <path.mp4>] [--fps N]\n                              [--quality demo|draft|proxy|anyos] [--draw-mouse 0|1] [--polish] [--pidfile <path>]\n  open-computer-use.exe record stop  [--pidfile <path>] [--save-as <name-or-path>] [--polish]\n  open-computer-use.exe record discard [--pidfile <path>]\n  open-computer-use.exe record polish --input <raw.mp4> [--events <file>] [--output <polished.mp4>]\n                              [--plan] [--write-plan <file>] [--engine compositor|ffmpeg]\n                              [--cursor-style slow|mellow|quick|rapid]\n                              [--ripples] [--no-ripples] [--no-keystrokes] [--no-cursor]\n                              [--no-idle-speedup] [--no-zoom]\n  open-computer-use.exe record proxy --input <raw.mp4> [--output-dir <dir>] [--1080p] [--full]\n  open-computer-use.exe record status [--pidfile <path>]\n\nRecord the desktop with ffmpeg gdigrab (H.264 mp4); ffmpeg must be on PATH.\nWhile recording, display `input` actions append <output>.events.json.\n`record polish` defaults to a clean-room frame compositor (idle remap → zoom →\nlens warp → motion blur → yellow click ripples → cursor + keystroke chips; ripples on by default, `--no-ripples` to disable). `--engine ffmpeg` keeps\nthe legacy filter path. start runs ffmpeg detached;\nstop raises Ctrl+Break so the mp4 is finalized; discard stops and deletes the\noutput (and event sidecars).\nDefaults: fps 30, quality demo (RecordScreen-aligned veryfast/crf17/High/\nfaststart), draw-mouse 1 (auto 0 with --polish), output in %TEMP%, pidfile\n%TEMP%\\open-computer-use-record.pid. Use --quality draft for ultrafast,\n--quality anyos (or proxy) for all-intra staging capture, or --fps 120 with anyos.\n"
	default:
		return `Open Computer Use for Windows

Usage:
  open-computer-use.exe [command] [options]

Commands:
  mcp                  Start the stdio MCP server.
  doctor               Print Windows runtime notes.
  list-apps            Print running apps with top-level windows.
  snapshot <app>       Print the current UI Automation snapshot for an app.
  call <tool>           Call one tool, or run a JSON array of tool calls.
  screenshot           Capture the whole virtual desktop to PNG.
  cursor-position      Print the mouse pointer position as JSON.
  input <action>       Global SendInput input: move/click/drag/scroll/type/key/wait.
  record <start|stop|discard|polish|proxy|status>  Record + optional RecordScreen-style polish.
  help [command]       Show general or command-specific help.
  version              Print the CLI version.

Notes:
  The Windows runtime uses UI Automation first, then Win32 window messages for
  fallback input. Run it in the signed-in desktop session, not as a service.
  The screenshot/cursor-position/input/record commands operate on the whole
  desktop; input requires OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT=1
  and record requires ffmpeg on PATH.
`
	}
}
