package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// This file is the pure-logic core of the Linux runtime: a faithful Go port
// of the retired runtime.py AT-SPI2 bridge. Every behavior quirk of the
// Python implementation is preserved verbatim and pinned by
// atspi_node_test.go; error strings are byte-identical with the Python
// originals. The D-Bus-backed implementation of atspiNode lives in
// native_atspi.go (linux build tag).

const (
	atspiMaxElements      = 1200
	atspiMaxDepth         = 64
	atspiDefaultTextLimit = 500
)

// AT-SPI2 state enum values (at-spi2-core AtspiStateType).
const (
	atspiStateActive  = 1
	atspiStateFocused = 12
	atspiStateShowing = 25
)

// atspiNode is one AT-SPI2 accessible object, mirroring the PyGObject
// Atspi.Accessible surface the Python bridge used. Just like runtime.py's
// safe() wrapper, every method collapses transport failures to the zero
// value instead of returning errors.
type atspiNode interface {
	Name() string
	RoleName() string
	ChildCount() int
	ChildAt(index int) atspiNode
	// Interfaces returns libatspi-style short interface names ("Text",
	// "EditableText", ...), always starting with "Accessible".
	Interfaces() []string
	AccessibleID() string
	PID() int
	StateContains(state uint32) bool
	// ComponentExtents reports the raw screen-coordinates extents reply;
	// ok is false when the object has no usable Component interface.
	ComponentExtents() (x, y, width, height int32, ok bool)
	ToolkitName() string
	CharacterCount() int
	TextRange(start, end int) string
	SelectionCount() int
	Selection(index int) (start, end int, ok bool)
	InsertText(position int, text string, length int) bool
	SetTextContents(text string) bool
	CurrentValue() (value float64, ok bool)
	SetCurrentValue(value float64) bool
	Actions() []atspiAction
	DoAction(index int) bool
}

type atspiAction struct {
	Name        string
	Description string
}

// atspiRuntime wires the pure operation logic to its environment. Tests
// substitute fakes; native_backend.go builds the production instance.
type atspiRuntime struct {
	desktop    atspiNode
	capture    func(bounds *frame) string // base64 PNG, "" to omit
	mouseEvent func(x, y int, event string)
	keyEvent   func(keyval uint32, keystr string, synthType uint32)
	sleep      func(time.Duration)
}

// --- generic helpers -------------------------------------------------------

// supportsInterface mirrors runtime.py: case-insensitive exact match against
// the short interface names.
func supportsInterface(node atspiNode, interfaceName string) bool {
	if node == nil {
		return false
	}
	expected := strings.ToLower(interfaceName)
	for _, iface := range node.Interfaces() {
		if strings.ToLower(iface) == expected {
			return true
		}
	}
	return false
}

// limitText mirrors limit_text: a nil limit means "max" (unlimited), and
// truncation reads one extra character so the ellipsis only appears when the
// text actually exceeds the limit. Counting is in code points, like Python.
func limitText(value string, limit *int) string {
	if limit == nil {
		return value
	}
	runes := []rune(value)
	if len(runes) > *limit {
		return string(runes[:*limit]) + "..."
	}
	return value
}

// positiveInt mirrors runtime.py's positive_int: bools and non-integral
// floats fall back, as do non-positive or unparsable values.
func positiveInt(value any, fallback int) int {
	switch v := value.(type) {
	case nil:
		return fallback
	case bool:
		return fallback
	case int:
		if v > 0 {
			return v
		}
		return fallback
	case int32:
		return positiveInt(int64(v), fallback)
	case int64:
		if v > 0 && v <= int64(maxInt()) {
			return int(v)
		}
		return fallback
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
			return fallback
		}
		if v <= 0 || v > float64(maxInt()) {
			return fallback
		}
		return int(v)
	case json.Number:
		if integer, err := v.Int64(); err == nil {
			return positiveInt(integer, fallback)
		}
		if float, err := v.Float64(); err == nil {
			return positiveInt(float, fallback)
		}
		return fallback
	case string:
		integer, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fallback
		}
		return positiveInt(integer, fallback)
	}
	return fallback
}

// parseTextLimit mirrors parse_text_limit: the string "max"
// (case-insensitive) means unlimited (nil), everything else is positive_int.
func parseTextLimit(value any, fallback int) *int {
	if s, ok := value.(string); ok && strings.EqualFold(s, "max") {
		return nil
	}
	limit := positiveInt(value, fallback)
	return &limit
}

// pyRound mirrors Python's round(): round half to even, not math.Round's
// half away from zero (round(2.5) == 2, round(3.5) == 4).
func pyRound(f float64) int {
	return int(math.RoundToEven(f))
}

// pyFloatStr mirrors Python str(float): shortest round-trip digits, integral
// values keep a trailing ".0", and the fixed/scientific switch happens at
// decimal point position > 16 or <= -4 (e.g. 1e16 renders "1e+16").
func pyFloatStr(v float64) string {
	switch {
	case math.IsNaN(v):
		return "nan"
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	}
	if v == 0 {
		if math.Signbit(v) {
			return "-0.0"
		}
		return "0.0"
	}
	sci := strconv.FormatFloat(v, 'e', -1, 64)
	negative := strings.HasPrefix(sci, "-")
	sci = strings.TrimPrefix(sci, "-")
	eAt := strings.IndexByte(sci, 'e')
	digits := strings.Replace(sci[:eAt], ".", "", 1)
	exponent, _ := strconv.Atoi(sci[eAt+1:])
	decpt := exponent + 1

	var b strings.Builder
	if negative {
		b.WriteByte('-')
	}
	if decpt > -4 && decpt <= 16 {
		switch {
		case decpt <= 0:
			b.WriteString("0.")
			b.WriteString(strings.Repeat("0", -decpt))
			b.WriteString(digits)
		case decpt >= len(digits):
			b.WriteString(digits)
			b.WriteString(strings.Repeat("0", decpt-len(digits)))
			b.WriteString(".0")
		default:
			b.WriteString(digits[:decpt])
			b.WriteByte('.')
			b.WriteString(digits[decpt:])
		}
		return b.String()
	}
	b.WriteByte(digits[0])
	if len(digits) > 1 {
		b.WriteByte('.')
		b.WriteString(digits[1:])
	}
	b.WriteByte('e')
	exp := decpt - 1
	if exp < 0 {
		b.WriteByte('-')
		exp = -exp
	} else {
		b.WriteByte('+')
	}
	expText := strconv.Itoa(exp)
	if len(expText) < 2 {
		expText = "0" + expText
	}
	b.WriteString(expText)
	return b.String()
}

// --- node accessors ---------------------------------------------------------

// nodeExtents mirrors extents(): the Component extents with the validity
// clamp (width/height must be in 1..100000).
func nodeExtents(node atspiNode) *frame {
	x, y, width, height, ok := node.ComponentExtents()
	if !ok || width <= 0 || height <= 0 || width > 100000 || height > 100000 {
		return nil
	}
	return &frame{X: float64(x), Y: float64(y), Width: float64(width), Height: float64(height)}
}

// relativeFrame mirrors relative_frame: screen bounds re-based on the window
// origin when window bounds are known.
func relativeFrame(node atspiNode, windowBounds *frame) *frame {
	bounds := nodeExtents(node)
	if bounds == nil {
		return nil
	}
	if windowBounds == nil {
		return bounds
	}
	return &frame{
		X:      bounds.X - windowBounds.X,
		Y:      bounds.Y - windowBounds.Y,
		Width:  bounds.Width,
		Height: bounds.Height,
	}
}

// --- app/window resolution --------------------------------------------------

func iterApps(rt *atspiRuntime) []atspiNode {
	root := rt.desktop
	var apps []atspiNode
	for index := 0; index < root.ChildCount(); index++ {
		app := root.ChildAt(index)
		if app != nil && app.Name() != "" {
			apps = append(apps, app)
		}
	}
	return apps
}

type appWindow struct {
	index int
	node  atspiNode
}

// appWindows mirrors app_windows: a child counts as a window when its role
// (lowercased) is frame/window/dialog/alert or it has valid extents.
func appWindows(app atspiNode) []appWindow {
	var windows []appWindow
	for index := 0; index < app.ChildCount(); index++ {
		child := app.ChildAt(index)
		if child == nil {
			continue
		}
		role := strings.ToLower(child.RoleName())
		if role == "frame" || role == "window" || role == "dialog" || role == "alert" || nodeExtents(child) != nil {
			windows = append(windows, appWindow{index: index, node: child})
		}
	}
	return windows
}

// mainWindow mirrors main_window: ACTIVE first, then SHOWING, then the
// first window; the pinned error text otherwise.
func mainWindow(app atspiNode) (int, atspiNode, error) {
	windows := appWindows(app)
	if len(windows) == 0 {
		return 0, nil, errors.New("No top-level AT-SPI window is available for " + app.Name())
	}
	for _, window := range windows {
		if window.node.StateContains(atspiStateActive) {
			return window.index, window.node, nil
		}
	}
	for _, window := range windows {
		if window.node.StateContains(atspiStateShowing) {
			return window.index, window.node, nil
		}
	}
	return windows[0].index, windows[0].node, nil
}

func isAllASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// matchesQuery mirrors matches_query: an all-digit query matches the pid;
// otherwise the app name or any window title, equal or containing, all
// lowercased.
func matchesQuery(app atspiNode, query string) bool {
	normalized := strings.ToLower(strings.TrimSpace(query))
	if normalized == "" {
		return false
	}
	if isAllASCIIDigits(normalized) {
		if pid, err := strconv.Atoi(normalized); err == nil && app.PID() == pid {
			return true
		}
	}
	appName := strings.ToLower(app.Name())
	if appName == normalized || strings.Contains(appName, normalized) {
		return true
	}
	for _, window := range appWindows(app) {
		title := strings.ToLower(window.node.Name())
		if title == normalized || strings.Contains(title, normalized) {
			return true
		}
	}
	return false
}

// resolveApp mirrors resolve_app, including the pinned appNotFound("<query>")
// text with raw (non-escaped) double quotes.
func resolveApp(rt *atspiRuntime, query string) (atspiNode, error) {
	for _, app := range iterApps(rt) {
		if matchesQuery(app, query) {
			return app, nil
		}
	}
	return nil, fmt.Errorf("appNotFound(\"%s\")", query)
}

// --- records and tree rendering ----------------------------------------------

// actionNames mirrors action_names: name-or-description labels, deduplicated,
// in action order.
func actionNames(node atspiNode) []string {
	var names []string
	for _, action := range node.Actions() {
		label := action.Name
		if label == "" {
			label = action.Description
		}
		if label != "" && !slices.Contains(names, label) {
			names = append(names, label)
		}
	}
	return names
}

// textValue mirrors text_value: only for Text-capable nodes, reads
// min(count, limit+1) characters so limitText can append the ellipsis.
func textValue(node atspiNode, limit *int) string {
	if !supportsInterface(node, "Text") {
		return ""
	}
	count := node.CharacterCount()
	if count <= 0 {
		return ""
	}
	end := count
	if limit != nil && *limit+1 < count {
		end = *limit + 1
	}
	return limitText(node.TextRange(0, end), limit)
}

// numericValue mirrors numeric_value, including Python's str(float) shape.
func numericValue(node atspiNode) string {
	if !supportsInterface(node, "Value") {
		return ""
	}
	value, ok := node.CurrentValue()
	if !ok {
		return ""
	}
	return pyFloatStr(value)
}

func elementValue(node atspiNode, limit *int) string {
	if text := textValue(node, limit); text != "" {
		return text
	}
	return numericValue(node)
}

// recordFor mirrors record_for. runtimeId is a copy of path, rooted at the
// app node; nativeWindowHandle stays 0.
func recordFor(node atspiNode, index int, path []int, windowBounds *frame, limit *int) elementRecord {
	role := node.RoleName()
	runtimeID := make([]int, len(path))
	copy(runtimeID, path)
	return elementRecord{
		Index:                index,
		RuntimeID:            runtimeID,
		AutomationID:         node.AccessibleID(),
		Name:                 limitText(node.Name(), limit),
		ControlType:          role,
		LocalizedControlType: role,
		ClassName:            node.ToolkitName(),
		Value:                elementValue(node, limit),
		NativeWindowHandle:   0,
		Frame:                relativeFrame(node, windowBounds),
		Actions:              actionNames(node),
	}
}

// renderTree mirrors render_tree: preorder DFS with the node/depth budgets,
// one text line per record in the exact Python format.
func renderTree(root atspiNode, windowBounds *frame, rootPath []int, limit *int, maxNodes, maxDepth int) ([]elementRecord, []string) {
	var records []elementRecord
	var lines []string

	var visit func(node atspiNode, depth int, path []int)
	visit = func(node atspiNode, depth int, path []int) {
		if len(records) >= maxNodes || depth > maxDepth || node == nil {
			return
		}
		index := len(records)
		record := recordFor(node, index, path, windowBounds, limit)
		records = append(records, record)

		role := record.LocalizedControlType
		if role == "" {
			role = record.ControlType
		}
		if role == "" {
			role = "element"
		}
		title := record.Name
		if title == "" {
			title = record.AutomationID
		}
		valueSegment := ""
		if record.Value != "" && record.Value != title {
			safeValue := strings.NewReplacer("\r", "\\r", "\n", "\\n").Replace(record.Value)
			valueSegment = " Value: " + safeValue
		}
		actionsSegment := ""
		if len(record.Actions) > 0 {
			actionsSegment = " Secondary Actions: " + strings.Join(record.Actions, ", ")
		}
		frameSegment := ""
		if record.Frame != nil {
			f := record.Frame
			frameSegment = fmt.Sprintf(" Frame: {x: %d, y: %d, width: %d, height: %d}",
				pyRound(f.X), pyRound(f.Y), pyRound(f.Width), pyRound(f.Height))
		}
		line := strings.Repeat("\t", depth+1) +
			fmt.Sprintf("%d %s %s%s%s%s", index, role, title, valueSegment, actionsSegment, frameSegment)
		lines = append(lines, strings.TrimRightFunc(line, unicode.IsSpace))

		for childIndex := 0; childIndex < node.ChildCount(); childIndex++ {
			visit(node.ChildAt(childIndex), depth+1, append(path, childIndex))
		}
	}
	visit(root, 0, rootPath)
	return records, lines
}

// --- focused element / selection ----------------------------------------------

func findFirst(root atspiNode, predicate func(atspiNode) bool) atspiNode {
	if root == nil {
		return nil
	}
	if predicate(root) {
		return root
	}
	for index := 0; index < root.ChildCount(); index++ {
		if found := findFirst(root.ChildAt(index), predicate); found != nil {
			return found
		}
	}
	return nil
}

// focusedSummary mirrors focused_summary: find the app by pid, DFS its main
// window for the FOCUSED node, and render "role name"; any failure is "".
func focusedSummary(rt *atspiRuntime, pid int, limit *int) string {
	for _, app := range iterApps(rt) {
		if app.PID() != pid {
			continue
		}
		_, window, err := mainWindow(app)
		if err != nil {
			return ""
		}
		focused := findFirst(window, func(node atspiNode) bool {
			return node.StateContains(atspiStateFocused)
		})
		if focused == nil {
			return ""
		}
		role := focused.RoleName()
		name := limitText(focused.Name(), limit)
		return strings.TrimSpace(role + " " + name)
	}
	return ""
}

// selectedText mirrors selected_text: the first selection of the FOCUSED
// Text node, with the end offset clamped to start+limit+1 so the ellipsis
// logic still applies.
func selectedText(rt *atspiRuntime, pid int, limit *int) string {
	for _, app := range iterApps(rt) {
		if app.PID() != pid {
			continue
		}
		_, window, err := mainWindow(app)
		if err != nil {
			return ""
		}
		focused := findFirst(window, func(node atspiNode) bool {
			return node.StateContains(atspiStateFocused)
		})
		if focused == nil || !supportsInterface(focused, "Text") {
			return ""
		}
		if focused.SelectionCount() <= 0 {
			return ""
		}
		start, end, ok := focused.Selection(0)
		if !ok {
			return ""
		}
		if limit != nil {
			if maxEnd := start + *limit + 1; end > maxEnd {
				end = maxEnd
			}
		}
		return limitText(focused.TextRange(start, end), limit)
	}
	return ""
}

// --- snapshot -----------------------------------------------------------------

// buildSnapshot mirrors build_snapshot.
func buildSnapshot(rt *atspiRuntime, query string, limit *int, maxNodes, maxDepth int) (*appSnapshot, error) {
	app, err := resolveApp(rt, query)
	if err != nil {
		return nil, err
	}
	windowIndex, window, err := mainWindow(app)
	if err != nil {
		return nil, err
	}
	bounds := nodeExtents(window)
	records, lines := renderTree(window, bounds, []int{windowIndex}, limit, maxNodes, maxDepth)
	pid := app.PID()
	appName := app.Name()
	snapshot := &appSnapshot{
		App: appDescriptor{
			Name:             appName,
			BundleIdentifier: appName,
			PID:              pid,
		},
		WindowTitle:    limitText(window.Name(), limit),
		WindowBounds:   bounds,
		TreeLines:      lines,
		FocusedSummary: focusedSummary(rt, pid, limit),
		SelectedText:   selectedText(rt, pid, limit),
		Elements:       records,
	}
	if rt.capture != nil {
		snapshot.ScreenshotPNGBase64 = rt.capture(bounds)
	}
	return snapshot, nil
}

// listAppsText mirrors list_apps_text: sorted by (name lowercased, pid),
// apps without windows skipped, empty window title becomes "untitled".
func listAppsText(rt *atspiRuntime) string {
	apps := iterApps(rt)
	sort.SliceStable(apps, func(i, j int) bool {
		nameI := strings.ToLower(apps[i].Name())
		nameJ := strings.ToLower(apps[j].Name())
		if nameI != nameJ {
			return nameI < nameJ
		}
		return apps[i].PID() < apps[j].PID()
	})
	var lines []string
	for _, app := range apps {
		windows := appWindows(app)
		if len(windows) == 0 {
			continue
		}
		title := windows[0].node.Name()
		if title == "" {
			title = "untitled"
		}
		name := app.Name()
		lines = append(lines, fmt.Sprintf("%s -- %s [running, pid=%d, window=%s]", name, name, app.PID(), title))
	}
	return strings.Join(lines, "\n")
}

// --- element resolution ---------------------------------------------------------

// iterAll mirrors iter_all: preorder collection capped at atspiMaxElements
// (always the default, never the request's max_tree_nodes).
func iterAll(root atspiNode) []atspiNode {
	var items []atspiNode
	var visit func(node atspiNode)
	visit = func(node atspiNode) {
		if node == nil || len(items) >= atspiMaxElements {
			return
		}
		items = append(items, node)
		for index := 0; index < node.ChildCount(); index++ {
			visit(node.ChildAt(index))
		}
	}
	visit(root)
	return items
}

func resolvePath(app atspiNode, path []int) atspiNode {
	if len(path) == 0 {
		return nil
	}
	node := app
	for _, index := range path {
		node = node.ChildAt(index)
		if node == nil {
			return nil
		}
	}
	return node
}

// sameFrame mirrors same_frame: every frame field must be within 3px.
func sameFrame(recordFrame, nodeFrame *frame) bool {
	if recordFrame == nil || nodeFrame == nil {
		return false
	}
	return math.Abs(recordFrame.X-nodeFrame.X) <= 3 &&
		math.Abs(recordFrame.Y-nodeFrame.Y) <= 3 &&
		math.Abs(recordFrame.Width-nodeFrame.Width) <= 3 &&
		math.Abs(recordFrame.Height-nodeFrame.Height) <= 3
}

// findElement mirrors find_element: runtimeId path first, then a full-tree
// rescan by automationId, then name+role, then role plus a 3px frame match.
func findElement(rt *atspiRuntime, app atspiNode, record *elementRecord) (atspiNode, error) {
	if record == nil {
		return nil, nil
	}
	if node := resolvePath(app, record.RuntimeID); node != nil {
		return node, nil
	}
	_, window, err := mainWindow(app)
	if err != nil {
		return nil, err
	}
	targetName := record.Name
	targetID := record.AutomationID
	targetRole := record.ControlType
	windowBounds := nodeExtents(window)
	for _, candidate := range iterAll(window) {
		if targetID != "" && candidate.AccessibleID() == targetID {
			return candidate, nil
		}
		if targetName != "" && candidate.Name() == targetName && candidate.RoleName() == targetRole {
			return candidate, nil
		}
		if targetRole != "" && candidate.RoleName() == targetRole &&
			sameFrame(record.Frame, relativeFrame(candidate, windowBounds)) {
			return candidate, nil
		}
	}
	return nil, nil
}

// --- actions ----------------------------------------------------------------------

var preferredExactActions = map[string]bool{
	"click":            true,
	"press":            true,
	"activate":         true,
	"default.activate": true,
	"invoke":           true,
	"select":           true,
	"toggle":           true,
	"open":             true,
}

// preferredActionIndex mirrors preferred_action_index: exact match on the
// lowercased name-or-description wins immediately; otherwise the first
// action containing activate/click/press.
func preferredActionIndex(node atspiNode) *int {
	fallback := -1
	for index, action := range node.Actions() {
		label := action.Name
		if label == "" {
			label = action.Description
		}
		lower := strings.ToLower(label)
		if preferredExactActions[lower] {
			result := index
			return &result
		}
		if fallback < 0 && (strings.Contains(lower, "activate") || strings.Contains(lower, "click") || strings.Contains(lower, "press")) {
			fallback = index
		}
	}
	if fallback < 0 {
		return nil
	}
	result := fallback
	return &result
}

func doActionByIndex(node atspiNode, index *int) bool {
	if index == nil {
		return false
	}
	return node.DoAction(*index)
}

// --- coordinates and input timing --------------------------------------------------

// screenPoint mirrors screen_point: an element with a frame and window
// bounds clicks its center; otherwise explicit x/y are required.
func screenPoint(windowBounds *frame, element *elementRecord, x, y *float64) (float64, float64, error) {
	if element != nil {
		if f := element.Frame; f != nil && windowBounds != nil {
			return windowBounds.X + f.X + f.Width/2, windowBounds.Y + f.Y + f.Height/2, nil
		}
	}
	if x == nil || y == nil || windowBounds == nil {
		return 0, 0, errors.New("coordinate action requires window bounds and x/y")
	}
	return windowBounds.X + *x, windowBounds.Y + *y, nil
}

// mouseButtonEvents mirrors mouse_button_events, resolving the official
// l/r/m short names to their real buttons; anything else resolves to the
// left button (the service layer rejects values outside the schema enum).
func mouseButtonEvents(button string) (down, up string) {
	normalized := button
	if normalized == "" {
		normalized = "left"
	}
	switch strings.ToLower(normalized) {
	case "right", "r":
		return "b3p", "b3r"
	case "middle", "m":
		return "b2p", "b2r"
	default:
		return "b1p", "b1r"
	}
}

// sendMouseClick mirrors send_mouse_click: abs -> down -> 35ms -> up ->
// 50ms, repeated count times (count <= 0 clamps to 1).
func (rt *atspiRuntime) sendMouseClick(x, y float64, button string, count int) {
	down, up := mouseButtonEvents(button)
	repeat := count
	if repeat < 1 {
		repeat = 1
	}
	for i := 0; i < repeat; i++ {
		rt.mouseEvent(pyRound(x), pyRound(y), "abs")
		rt.mouseEvent(pyRound(x), pyRound(y), down)
		rt.sleep(35 * time.Millisecond)
		rt.mouseEvent(pyRound(x), pyRound(y), up)
		rt.sleep(50 * time.Millisecond)
	}
}

// sendDrag mirrors send_drag: press at the start, 12 interpolated abs steps
// 20ms apart, release at the end.
func (rt *atspiRuntime) sendDrag(fromX, fromY, toX, toY float64) {
	rt.mouseEvent(pyRound(fromX), pyRound(fromY), "abs")
	rt.mouseEvent(pyRound(fromX), pyRound(fromY), "b1p")
	const steps = 12
	for step := 1; step <= steps; step++ {
		x := fromX + (toX-fromX)*float64(step)/steps
		y := fromY + (toY-fromY)*float64(step)/steps
		rt.mouseEvent(pyRound(x), pyRound(y), "abs")
		rt.sleep(20 * time.Millisecond)
	}
	rt.mouseEvent(pyRound(toX), pyRound(toY), "b1r")
}

// sendKey mirrors send_key: "<mod>+<mod>+<key>" chords, unknown modifiers
// silently skipped, single characters synthesized as STRING events, named
// keys as PRESSRELEASE, modifiers released in reverse order. The main key is
// resolved BEFORE any modifier is pressed and any later failure releases the
// already-pressed modifiers in reverse, so a bad key can never leave the
// keyboard with stuck modifiers.
func (rt *atspiRuntime) sendKey(key string) error {
	var parts []string
	for _, part := range strings.Split(key, "+") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return fmt.Errorf("Unsupported key: %s", key)
	}
	mainKey := parts[len(parts)-1]
	modifiers := parts[:len(parts)-1]

	normalized := mainKey
	if alias, ok := keyAliases[strings.ToLower(mainKey)]; ok {
		normalized = alias
	}
	var mainKeyval uint32
	mainIsString := utf8.RuneCountInString(normalized) == 1
	if !mainIsString {
		var err error
		mainKeyval, err = keyvalForName(normalized)
		if err != nil {
			return err
		}
	}

	var pressed []uint32
	releasePressed := func() {
		for index := len(pressed) - 1; index >= 0; index-- {
			rt.keyEvent(pressed[index], "", atspiKeyRelease)
		}
	}
	for _, modifier := range modifiers {
		name, ok := modifierKeys[strings.ToLower(modifier)]
		if !ok {
			continue
		}
		value, err := keyvalForName(name)
		if err != nil {
			releasePressed()
			return err
		}
		rt.keyEvent(value, "", atspiKeyPress)
		pressed = append(pressed, value)
	}
	if mainIsString {
		rt.keyEvent(0, normalized, atspiKeyString)
	} else {
		rt.keyEvent(mainKeyval, "", atspiKeyPressRelease)
	}
	releasePressed()
	return nil
}

func (rt *atspiRuntime) sendText(text string) {
	rt.keyEvent(0, text, atspiKeyString)
}

// --- text/value mutation ---------------------------------------------------------------

func findEditableText(root atspiNode) atspiNode {
	return findFirst(root, func(node atspiNode) bool {
		return supportsInterface(node, "EditableText") && supportsInterface(node, "Text")
	})
}

// insertText mirrors insert_text: insert at the end of the first editable
// text node; length is counted in code points, like Python's len().
func insertText(root atspiNode, text string) bool {
	node := findEditableText(root)
	if node == nil {
		return false
	}
	offset := node.CharacterCount()
	return node.InsertText(offset, text, utf8.RuneCountInString(text))
}

// setElementValue mirrors set_element_value: EditableText wins and its
// result is returned directly; otherwise the Value interface, with the
// string parsed as a float like Python's float().
func setElementValue(node atspiNode, value string) bool {
	if node != nil && supportsInterface(node, "EditableText") {
		return node.SetTextContents(value)
	}
	if node != nil && supportsInterface(node, "Value") {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return false
		}
		return node.SetCurrentValue(parsed)
	}
	return false
}

// invokeSecondaryAction mirrors invoke_secondary_action: case-insensitive
// match on action name or description; a matched-but-failed action still
// reports the pinned error text.
func invokeSecondaryAction(node atspiNode, action string) error {
	if node == nil {
		return errors.New("unknown element_index")
	}
	normalized := strings.ToLower(action)
	for index, candidate := range node.Actions() {
		if normalized == strings.ToLower(candidate.Name) || normalized == strings.ToLower(candidate.Description) {
			if doActionByIndex(node, &index) {
				return nil
			}
			break
		}
	}
	return fmt.Errorf("%s is not a valid secondary action for element", action)
}

// scrollElement mirrors scroll_element: Page_Down by default, repeat is
// ceil(pages) with a minimum of 1, 40ms between key presses.
func (rt *atspiRuntime) scrollElement(direction string, pages float64) error {
	key := "Page_Down"
	switch direction {
	case "up":
		key = "Page_Up"
	case "left":
		key = "Left"
	case "right":
		key = "Right"
	}
	if pages == 0 {
		pages = 1
	}
	repeat := int(math.Ceil(pages))
	if repeat < 1 {
		repeat = 1
	}
	for i := 0; i < repeat; i++ {
		if err := rt.sendKey(key); err != nil {
			return err
		}
		rt.sleep(40 * time.Millisecond)
	}
	return nil
}

// --- screenshot helpers (pure part) -------------------------------------------------------

// captureRect mirrors the coordinate conversion in capture_window_png.
func captureRect(bounds *frame) (x, y, width, height int) {
	x = pyRound(bounds.X)
	y = pyRound(bounds.Y)
	width = pyRound(bounds.Width)
	if width < 1 {
		width = 1
	}
	height = pyRound(bounds.Height)
	if height < 1 {
		height = 1
	}
	return x, y, width, height
}

// looksBlackRGB mirrors pixbuf_looks_black: a 16x16 sampling grid where every
// sampled pixel must have all of its first three channels <= 3 for the image
// to count as black.
func looksBlackRGB(pixels []byte, width, height, stride, channels int) bool {
	if width <= 0 || height <= 0 || channels < 3 {
		return true
	}
	stepX := width / 16
	if stepX < 1 {
		stepX = 1
	}
	stepY := height / 16
	if stepY < 1 {
		stepY = 1
	}
	checked := 0
	for y := 0; y < height; y += stepY {
		row := y * stride
		for x := 0; x < width; x += stepX {
			offset := row + x*channels
			if offset+2 >= len(pixels) {
				return false
			}
			if pixels[offset] > 3 || pixels[offset+1] > 3 || pixels[offset+2] > 3 {
				return false
			}
			checked++
		}
	}
	return checked > 0
}

// --- the operation dispatch ------------------------------------------------------------------

func errorResponse(message string) *linuxResponse {
	return &linuxResponse{Error: message}
}

// performOperation mirrors perform_operation. Domain errors ride the
// linuxResponse error field, exactly like the Python bridge's ok:false
// envelope.
func performOperation(rt *atspiRuntime, op *linuxRequest) *linuxResponse {
	tool := op.Tool
	if tool == "list_apps" {
		return &linuxResponse{OK: true, Text: listAppsText(rt)}
	}
	if tool == "get_app_state" {
		snapshot, err := buildSnapshot(rt, op.App,
			parseTextLimit(op.TextLimit, atspiDefaultTextLimit),
			positiveInt(op.MaxTreeNodes, atspiMaxElements),
			positiveInt(op.MaxTreeDepth, atspiMaxDepth))
		if err != nil {
			return errorResponse(err.Error())
		}
		return &linuxResponse{OK: true, Snapshot: snapshot}
	}

	app, err := resolveApp(rt, op.App)
	if err != nil {
		return errorResponse(err.Error())
	}
	_, window, err := mainWindow(app)
	if err != nil {
		return errorResponse(err.Error())
	}
	bounds := op.WindowBounds
	if bounds == nil {
		bounds = nodeExtents(window)
	}
	elementRecord := op.Element
	element, err := findElement(rt, app, elementRecord)
	if err != nil {
		return errorResponse(err.Error())
	}

	switch tool {
	case "click":
		clickMethod := op.ClickMethod
		if clickMethod == "" {
			clickMethod = "auto"
		}
		clickMethod = strings.ToLower(clickMethod)
		switch clickMethod {
		case "accessibility":
			if element == nil {
				return errorResponse("click_method 'accessibility' requires element_index")
			}
			if op.MouseButton != "left" {
				return errorResponse("click_method 'accessibility' only supports mouse_button 'left'")
			}
			if !doActionByIndex(element, preferredActionIndex(element)) {
				return errorResponse("click_method 'accessibility' could not click the requested element")
			}
		case "app_post":
			return errorResponse("click_method 'app_post' is not supported on Linux")
		case "sky_click":
			return errorResponse("click_method 'sky_click' is not supported on Linux")
		case "global":
			x, y, err := screenPoint(bounds, elementRecord, op.X, op.Y)
			if err != nil {
				return errorResponse(err.Error())
			}
			rt.sendMouseClick(x, y, op.MouseButton, op.ClickCount)
		case "auto":
			handled := false
			if element != nil && op.MouseButton == "left" {
				handled = doActionByIndex(element, preferredActionIndex(element))
			}
			if !handled {
				x, y, err := screenPoint(bounds, elementRecord, op.X, op.Y)
				if err != nil {
					return errorResponse(err.Error())
				}
				rt.sendMouseClick(x, y, op.MouseButton, op.ClickCount)
			}
		default:
			return errorResponse(fmt.Sprintf("Invalid click_method '%s'", clickMethod))
		}
	case "perform_secondary_action":
		if err := invokeSecondaryAction(element, op.Action); err != nil {
			return errorResponse(err.Error())
		}
	case "scroll":
		if err := rt.scrollElement(op.Direction, op.Pages); err != nil {
			return errorResponse(err.Error())
		}
	case "drag":
		fromX, fromY, err := screenPoint(bounds, nil, op.FromX, op.FromY)
		if err != nil {
			return errorResponse(err.Error())
		}
		toX, toY, err := screenPoint(bounds, nil, op.ToX, op.ToY)
		if err != nil {
			return errorResponse(err.Error())
		}
		rt.sendDrag(fromX, fromY, toX, toY)
	case "type_text":
		if !insertText(window, op.Text) {
			rt.sendText(op.Text)
		}
	case "press_key":
		if err := rt.sendKey(op.Key); err != nil {
			return errorResponse(err.Error())
		}
	case "set_value":
		if element == nil {
			return errorResponse("unknown element_index")
		}
		if !setElementValue(element, op.Value) {
			return errorResponse("Cannot set a value for an element that is not settable")
		}
	default:
		return errorResponse(fmt.Sprintf("unsupportedTool(\"%s\")", tool))
	}

	rt.sleep(120 * time.Millisecond)
	// The post-action snapshot intentionally ignores the request's
	// text_limit/max_tree_* and uses the defaults (runtime.py:864).
	defaultLimit := atspiDefaultTextLimit
	snapshot, err := buildSnapshot(rt, op.App, &defaultLimit, atspiMaxElements, atspiMaxDepth)
	if err != nil {
		return errorResponse(err.Error())
	}
	return &linuxResponse{OK: true, Snapshot: snapshot}
}
