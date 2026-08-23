package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeNode implements atspiNode for the pure-logic tests. All D-Bus-shaped
// failures are expressed as nil/false fields, mirroring how the dbNode
// collapses transport errors to zero values.
type fakeNode struct {
	name       string
	role       string
	children   []*fakeNode
	interfaces []string
	id         string
	pid        int
	states     map[uint32]bool
	extents    *[4]int32 // nil = Component call failed
	toolkit    string
	text       string // text content; CharacterCount/TextRange operate on runes
	selections [][2]int

	insertTextOK   bool
	insertLog      *[][3]any // (offset, text, length)
	setContentsOK  bool
	setContentsLog *[]string
	hasValue       bool
	value          float64
	setValueOK     bool
	setValueLog    *[]float64

	actions     []atspiAction
	doActionOK  bool
	doActionLog *[]int
}

func fakeStates(states ...uint32) map[uint32]bool {
	m := map[uint32]bool{}
	for _, s := range states {
		m[s] = true
	}
	return m
}

func (n *fakeNode) Name() string     { return n.name }
func (n *fakeNode) RoleName() string { return n.role }
func (n *fakeNode) ChildCount() int  { return len(n.children) }

func (n *fakeNode) ChildAt(index int) atspiNode {
	if index < 0 || index >= len(n.children) {
		return nil
	}
	return n.children[index]
}

func (n *fakeNode) Interfaces() []string {
	if n.interfaces == nil {
		return []string{"Accessible"}
	}
	return n.interfaces
}

func (n *fakeNode) AccessibleID() string { return n.id }
func (n *fakeNode) PID() int             { return n.pid }
func (n *fakeNode) StateContains(state uint32) bool {
	return n.states[state]
}

func (n *fakeNode) ComponentExtents() (int32, int32, int32, int32, bool) {
	if n.extents == nil {
		return 0, 0, 0, 0, false
	}
	return n.extents[0], n.extents[1], n.extents[2], n.extents[3], true
}

func (n *fakeNode) ToolkitName() string { return n.toolkit }

func (n *fakeNode) CharacterCount() int {
	return len([]rune(n.text))
}

func (n *fakeNode) TextRange(start, end int) string {
	runes := []rune(n.text)
	if start < 0 {
		start = 0
	}
	if end > len(runes) {
		end = len(runes)
	}
	if start >= end {
		return ""
	}
	return string(runes[start:end])
}

func (n *fakeNode) SelectionCount() int { return len(n.selections) }

func (n *fakeNode) Selection(index int) (int, int, bool) {
	if index < 0 || index >= len(n.selections) {
		return 0, 0, false
	}
	return n.selections[index][0], n.selections[index][1], true
}

func (n *fakeNode) InsertText(position int, text string, length int) bool {
	if n.insertLog != nil {
		*n.insertLog = append(*n.insertLog, [3]any{position, text, length})
	}
	return n.insertTextOK
}

func (n *fakeNode) SetTextContents(text string) bool {
	if n.setContentsLog != nil {
		*n.setContentsLog = append(*n.setContentsLog, text)
	}
	return n.setContentsOK
}

func (n *fakeNode) CurrentValue() (float64, bool) {
	if !n.hasValue {
		return 0, false
	}
	return n.value, true
}

func (n *fakeNode) SetCurrentValue(value float64) bool {
	if n.setValueLog != nil {
		*n.setValueLog = append(*n.setValueLog, value)
	}
	return n.setValueOK
}

func (n *fakeNode) Actions() []atspiAction { return n.actions }

func (n *fakeNode) DoAction(index int) bool {
	if n.doActionLog != nil {
		*n.doActionLog = append(*n.doActionLog, index)
	}
	return n.doActionOK
}

// --- recording runtime ---------------------------------------------------------

type recordedMouseEvent struct {
	x, y  int
	event string
}

type recordedKeyEvent struct {
	keyval uint32
	keystr string
	synth  uint32
}

type fakeRuntime struct {
	rt          *atspiRuntime
	mouseEvents []recordedMouseEvent
	keyEvents   []recordedKeyEvent
	sleeps      []time.Duration
	captures    int
	capturePNG  string
}

func newFakeRuntime(desktop *fakeNode) *fakeRuntime {
	fr := &fakeRuntime{}
	fr.rt = &atspiRuntime{
		desktop: desktop,
		capture: func(bounds *frame) string {
			fr.captures++
			return fr.capturePNG
		},
		mouseEvent: func(x, y int, event string) {
			fr.mouseEvents = append(fr.mouseEvents, recordedMouseEvent{x, y, event})
		},
		keyEvent: func(keyval uint32, keystr string, synthType uint32) {
			fr.keyEvents = append(fr.keyEvents, recordedKeyEvent{keyval, keystr, synthType})
		},
		sleep: func(d time.Duration) { fr.sleeps = append(fr.sleeps, d) },
	}
	return fr
}

func (fr *fakeRuntime) totalSleep() time.Duration {
	var total time.Duration
	for _, d := range fr.sleeps {
		total += d
	}
	return total
}

// editorFixture builds the canonical fake desktop:
//
//	desktop
//	└── app "Text Editor" (pid 4242)
//	    └── [0] window "doc.txt" (frame, ACTIVE|SHOWING, extents 100,100,800,600)
//	        ├── [0] button "Save" (id save-btn, actions press, frame 10,10,80,30)
//	        └── [1] text "hello world" (Text iface, editable, frame 10,50,400,300)
//	└── app "Idle" (pid 4243, no windows)
func editorFixture() *fakeNode {
	saveButton := &fakeNode{
		name:       "Save",
		role:       "push button",
		id:         "save-btn",
		extents:    &[4]int32{110, 110, 80, 30},
		actions:    []atspiAction{{Name: "press", Description: ""}},
		doActionOK: true,
	}
	textNode := &fakeNode{
		name:          "",
		role:          "text",
		interfaces:    []string{"Accessible", "Text", "EditableText"},
		text:          "hello world",
		extents:       &[4]int32{110, 150, 400, 300},
		insertTextOK:  true,
		setContentsOK: true,
	}
	window := &fakeNode{
		name:     "doc.txt",
		role:     "frame",
		states:   fakeStates(atspiStateActive, atspiStateShowing),
		extents:  &[4]int32{100, 100, 800, 600},
		children: []*fakeNode{saveButton, textNode},
	}
	app := &fakeNode{
		name:     "Text Editor",
		role:     "application",
		pid:      4242,
		toolkit:  "GTK",
		children: []*fakeNode{window},
	}
	idle := &fakeNode{name: "Idle", role: "application", pid: 4243}
	return &fakeNode{children: []*fakeNode{app, idle}}
}

// editorFixture keeps references reachable for assertions.
func fixtureParts(desktop *fakeNode) (app, window, saveButton, textNode *fakeNode) {
	app = desktop.children[0]
	window = app.children[0]
	saveButton = window.children[0]
	textNode = window.children[1]
	return
}

func limits(v int) *int { return &v }

// --- error text pins ----------------------------------------------------------

func TestPerformOperationErrorTextsByteIdentical(t *testing.T) {
	desktop := editorFixture()
	fr := newFakeRuntime(desktop)

	cases := []struct {
		name string
		op   linuxRequest
		want string
	}{
		{"appNotFound double quotes", linuxRequest{Tool: "get_app_state", App: "No Such App"}, `appNotFound("No Such App")`},
		{"appNotFound raw quotes not escaped", linuxRequest{Tool: "get_app_state", App: `we"ird`}, `appNotFound("we"ird")`},
		{"no window", linuxRequest{Tool: "get_app_state", App: "Idle"}, "No top-level AT-SPI window is available for Idle"},
		{"unsupportedTool", linuxRequest{Tool: "bogus_tool", App: "Text Editor"}, `unsupportedTool("bogus_tool")`},
		{"accessibility needs element", linuxRequest{Tool: "click", App: "Text Editor", ClickMethod: "accessibility"}, "click_method 'accessibility' requires element_index"},
		{"app_post", linuxRequest{Tool: "click", App: "Text Editor", ClickMethod: "app_post"}, "click_method 'app_post' is not supported on Linux"},
		{"sky_click", linuxRequest{Tool: "click", App: "Text Editor", ClickMethod: "sky_click"}, "click_method 'sky_click' is not supported on Linux"},
		{"invalid click_method lowercased", linuxRequest{Tool: "click", App: "Text Editor", ClickMethod: "PHYSICAL"}, "Invalid click_method 'physical'"},
		{"secondary action unknown element", linuxRequest{Tool: "perform_secondary_action", App: "Text Editor", Action: "press"}, "unknown element_index"},
		{"set_value unknown element", linuxRequest{Tool: "set_value", App: "Text Editor", Value: "v"}, "unknown element_index"},
		{"press_key unsupported", linuxRequest{Tool: "press_key", App: "Text Editor", Key: "NoSuchKey"}, "Unsupported key: NoSuchKey"},
		{"press_key empty", linuxRequest{Tool: "press_key", App: "Text Editor", Key: ""}, "Unsupported key: "},
		{"press_key only separators", linuxRequest{Tool: "press_key", App: "Text Editor", Key: "+++"}, "Unsupported key: +++"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := performOperation(fr.rt, &tc.op)
			if resp == nil || resp.OK || resp.Error != tc.want {
				t.Fatalf("performOperation(%q) = %#v, want error %q", tc.name, resp, tc.want)
			}
		})
	}
}

func TestSecondaryActionErrorTexts(t *testing.T) {
	desktop := editorFixture()
	_, _, saveButton, _ := fixtureParts(desktop)
	saveButton.doActionOK = false // matched action fails to execute
	fr := newFakeRuntime(desktop)
	record := &elementRecord{Index: 0, RuntimeID: []int{0, 0}, ControlType: "push button", Name: "Save"}
	resp := performOperation(fr.rt, &linuxRequest{Tool: "perform_secondary_action", App: "Text Editor", Element: record, Action: "press"})
	if resp.OK || resp.Error != "press is not a valid secondary action for element" {
		t.Fatalf("failing action error = %q", resp.Error)
	}

	resp = performOperation(fr.rt, &linuxRequest{Tool: "perform_secondary_action", App: "Text Editor", Element: record, Action: "NoSuchAction"})
	if resp.OK || resp.Error != "NoSuchAction is not a valid secondary action for element" {
		t.Fatalf("unmatched action error = %q (original case preserved)", resp.Error)
	}
}

func TestSetValueNotSettableError(t *testing.T) {
	desktop := editorFixture()
	fr := newFakeRuntime(desktop)
	// save button has no EditableText/Value interfaces
	record := &elementRecord{Index: 0, RuntimeID: []int{0, 0}, ControlType: "push button", Name: "Save"}
	resp := performOperation(fr.rt, &linuxRequest{Tool: "set_value", App: "Text Editor", Element: record, Value: "v"})
	if resp.OK || resp.Error != "Cannot set a value for an element that is not settable" {
		t.Fatalf("set_value error = %q", resp.Error)
	}
}

func TestCoordinateActionRequiresBoundsAndXY(t *testing.T) {
	desktop := editorFixture()
	_, window, _, _ := fixtureParts(desktop)
	window.extents = nil // no usable bounds anywhere
	fr := newFakeRuntime(desktop)
	resp := performOperation(fr.rt, &linuxRequest{Tool: "click", App: "Text Editor", ClickMethod: "global"})
	if resp.OK || resp.Error != "coordinate action requires window bounds and x/y" {
		t.Fatalf("coordinate error = %q", resp.Error)
	}
}

func TestAccessibilityMouseButtonGate(t *testing.T) {
	desktop := editorFixture()
	fr := newFakeRuntime(desktop)
	record := &elementRecord{Index: 0, RuntimeID: []int{0, 0}, ControlType: "push button", Name: "Save"}
	resp := performOperation(fr.rt, &linuxRequest{Tool: "click", App: "Text Editor", ClickMethod: "accessibility", Element: record, MouseButton: "right"})
	if resp.OK || resp.Error != "click_method 'accessibility' only supports mouse_button 'left'" {
		t.Fatalf("mouse_button gate error = %q", resp.Error)
	}
	// The l/r/m short names hit the same exact-match gate (Python quirk).
	resp = performOperation(fr.rt, &linuxRequest{Tool: "click", App: "Text Editor", ClickMethod: "accessibility", Element: record, MouseButton: "l"})
	if resp.OK || resp.Error != "click_method 'accessibility' only supports mouse_button 'left'" {
		t.Fatalf("short name gate error = %q", resp.Error)
	}
}

func TestAccessibilityClickFailureError(t *testing.T) {
	desktop := editorFixture()
	_, _, saveButton, _ := fixtureParts(desktop)
	saveButton.doActionOK = false
	fr := newFakeRuntime(desktop)
	record := &elementRecord{Index: 0, RuntimeID: []int{0, 0}, ControlType: "push button", Name: "Save"}
	resp := performOperation(fr.rt, &linuxRequest{Tool: "click", App: "Text Editor", ClickMethod: "accessibility", Element: record, MouseButton: "left"})
	if resp.OK || resp.Error != "click_method 'accessibility' could not click the requested element" {
		t.Fatalf("accessibility failure = %q", resp.Error)
	}
}

// --- tree rendering ------------------------------------------------------------

func TestRenderTreeExactLineFormat(t *testing.T) {
	desktop := editorFixture()
	_, window, saveButton, textNode := fixtureParts(desktop)
	textNode.name = "edit area"
	textNode.text = "line1\nline2\r\n"

	records, lines := renderTree(window, nodeExtents(window), []int{0}, limits(500), atspiMaxElements, atspiMaxDepth)
	if len(records) != 3 || len(lines) != 3 {
		t.Fatalf("records=%d lines=%d, want 3", len(records), len(lines))
	}
	wantLines := []string{
		"\t0 frame doc.txt Frame: {x: 0, y: 0, width: 800, height: 600}",
		"\t\t1 push button Save Secondary Actions: press Frame: {x: 10, y: 10, width: 80, height: 30}",
		"\t\t2 text edit area Value: line1\\nline2\\r\\n Frame: {x: 10, y: 50, width: 400, height: 300}",
	}
	for i, want := range wantLines {
		if lines[i] != want {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want)
		}
	}
	// records: runtimeId rooted at the app node, window at child index 0
	if fmt.Sprint(records[0].RuntimeID) != "[0]" || fmt.Sprint(records[1].RuntimeID) != "[0 0]" || fmt.Sprint(records[2].RuntimeID) != "[0 1]" {
		t.Fatalf("runtimeIds = %v %v %v", records[0].RuntimeID, records[1].RuntimeID, records[2].RuntimeID)
	}
	if records[0].NativeWindowHandle != 0 {
		t.Fatal("nativeWindowHandle must stay 0")
	}
	_ = saveButton
}

func TestRenderTreeRoleAndTitleFallbacks(t *testing.T) {
	window := &fakeNode{
		role:    "frame",
		extents: &[4]int32{0, 0, 100, 100},
		children: []*fakeNode{
			{name: "", role: "", id: ""},                 // both empty -> role "element", title ""
			{name: "", role: "custom", id: "auto-id-42"}, // title from automationId
			{name: "trailing ", role: "text"},            // line gets rstripped
		},
	}
	_, lines := renderTree(window, nil, []int{2}, limits(500), atspiMaxElements, atspiMaxDepth)
	want := []string{
		"\t0 frame  Frame: {x: 0, y: 0, width: 100, height: 100}",
		"\t\t1 element",
		"\t\t2 custom auto-id-42",
		"\t\t3 text trailing",
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %#v", lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestRenderTreeValueSegmentSkippedWhenEqualToTitle(t *testing.T) {
	window := &fakeNode{
		role:     "frame",
		children: []*fakeNode{{name: "same", role: "text", interfaces: []string{"Accessible", "Text"}, text: "same"}},
	}
	_, lines := renderTree(window, nil, []int{0}, limits(500), atspiMaxElements, atspiMaxDepth)
	if lines[1] != "\t\t1 text same" {
		t.Fatalf("value==title line = %q", lines[1])
	}
}

func TestRenderTreeBudgets(t *testing.T) {
	// 1300-node chain: node budget cuts at the request max.
	root := &fakeNode{role: "frame"}
	current := root
	for i := 0; i < 1299; i++ {
		child := &fakeNode{role: "filler", name: fmt.Sprintf("n%d", i)}
		current.children = []*fakeNode{child}
		current = child
	}
	records, lines := renderTree(root, nil, []int{0}, limits(500), 100, 2000)
	if len(records) != 100 || len(lines) != 100 {
		t.Fatalf("max_nodes cap = %d records, want 100", len(records))
	}
	// depth budget: depth > maxDepth is blocked, so maxDepth=64 renders 65 levels
	records, _ = renderTree(root, nil, []int{0}, limits(500), 5000, 64)
	if len(records) != 65 {
		t.Fatalf("max_depth cap = %d records, want 65", len(records))
	}
	// defaults are the macOS-shared budget values
	if atspiMaxElements != 1200 || atspiMaxDepth != 64 || atspiDefaultTextLimit != 500 {
		t.Fatalf("defaults = %d/%d/%d", atspiMaxElements, atspiMaxDepth, atspiDefaultTextLimit)
	}
}

func TestFrameSegmentUsesPythonRounding(t *testing.T) {
	// pyRound is banker's rounding: 2.5 -> 2, 3.5 -> 4, -2.5 -> -2
	for in, want := range map[float64]int{2.5: 2, 3.5: 4, -2.5: -2, -3.5: -4, 0.4: 0, 0.6: 1, 10.0: 10} {
		if got := pyRound(in); got != want {
			t.Fatalf("pyRound(%v) = %d, want %d", in, got, want)
		}
	}
	// frames carry integral extents, so the segment sees whole numbers
	window := &fakeNode{role: "frame", extents: &[4]int32{5, 6, 801, 601}}
	_, lines := renderTree(window, nil, []int{0}, limits(500), atspiMaxElements, atspiMaxDepth)
	if !strings.HasSuffix(lines[0], "Frame: {x: 5, y: 6, width: 801, height: 601}") {
		t.Fatalf("frame line = %q", lines[0])
	}
}

// --- text limit ----------------------------------------------------------------

func TestTextLimitSemantics(t *testing.T) {
	node := &fakeNode{role: "text", interfaces: []string{"Accessible", "Text"}, text: "hello world"}

	if got := textValue(node, limits(5)); got != "hello..." {
		t.Fatalf("textValue limit 5 = %q", got)
	}
	if got := textValue(node, limits(11)); got != "hello world" {
		t.Fatalf("textValue limit 11 = %q (no ellipsis at exact length)", got)
	}
	if got := textValue(node, nil); got != "hello world" {
		t.Fatalf("textValue max = %q", got)
	}
	if got := textValue(node, limits(500)); got != "hello world" {
		t.Fatalf("textValue default = %q", got)
	}
	// nil/invalid inputs fall back to the 500 default
	if got := parseTextLimit(nil, atspiDefaultTextLimit); got == nil || *got != 500 {
		t.Fatalf("parseTextLimit(nil) = %v", got)
	}
	if got := parseTextLimit("max", 500); got != nil {
		t.Fatalf("parseTextLimit(max) = %v", *got)
	}
	if got := parseTextLimit("MAX", 500); got != nil {
		t.Fatalf("parseTextLimit(MAX) = %v (case-insensitive)", *got)
	}
	if got := parseTextLimit(250, 500); got == nil || *got != 250 {
		t.Fatalf("parseTextLimit(250) = %v", got)
	}
	if got := parseTextLimit(0, 500); got == nil || *got != 500 {
		t.Fatalf("parseTextLimit(0) = %v (falls back)", got)
	}
}

func TestPositiveIntQuirks(t *testing.T) {
	cases := []struct {
		value    any
		fallback int
		want     int
	}{
		{nil, 7, 7},
		{true, 7, 7},  // bool -> fallback
		{false, 7, 7}, //
		{2.5, 7, 7},   // non-integral float -> fallback
		{3.0, 7, 3},   // integral float ok
		{0, 7, 7},     // non-positive -> fallback
		{-4, 7, 7},    //
		{42, 7, 42},   //
		{"42", 7, 42}, // Python int("42")
		{"4x", 7, 7},  //
		{json.Number("9"), 7, 9},
		{json.Number("1.5"), 7, 7},
	}
	for _, tc := range cases {
		if got := positiveInt(tc.value, tc.fallback); got != tc.want {
			t.Fatalf("positiveInt(%v) = %d, want %d", tc.value, got, tc.want)
		}
	}
}

func TestPyFloatStrMatchesPython(t *testing.T) {
	cases := map[float64]string{
		3.0:     "3.0",
		0.5:     "0.5",
		-1.25:   "-1.25",
		100.0:   "100.0",
		0.0:     "0.0",
		0.1:     "0.1",
		1e16:    "1e+16",
		1e15:    "1000000000000000.0",
		0.0001:  "0.0001",
		0.00001: "1e-05",
		123.456: "123.456",
		2.5e-8:  "2.5e-08",
	}
	for in, want := range cases {
		if got := pyFloatStr(in); got != want {
			t.Fatalf("pyFloatStr(%v) = %q, want %q", in, got, want)
		}
	}
	// numeric_value uses the same rendering
	node := &fakeNode{role: "slider", interfaces: []string{"Accessible", "Value"}, hasValue: true, value: 3.5}
	if got := numericValue(node); got != "3.5" {
		t.Fatalf("numericValue = %q", got)
	}
	node.value = 3.0
	if got := numericValue(node); got != "3.0" {
		t.Fatalf("numericValue integral = %q (Python str(float) keeps .0)", got)
	}
}

// --- list_apps -----------------------------------------------------------------

func TestListAppsTextFormatAndSort(t *testing.T) {
	terminal := &fakeNode{name: "Terminal", pid: 5000, children: []*fakeNode{{name: "shell", role: "frame", extents: &[4]int32{0, 0, 10, 10}}}}
	editor1 := &fakeNode{name: "editor", pid: 4243, children: []*fakeNode{{name: "b.txt", role: "frame"}}}
	editor2 := &fakeNode{name: "Editor", pid: 4242, children: []*fakeNode{{name: "a.txt", role: "frame"}}}
	untitledApp := &fakeNode{name: "bare", pid: 9, children: []*fakeNode{{name: "", role: "frame"}}}
	windowless := &fakeNode{name: "aa-windowless", pid: 1}
	desktop := &fakeNode{children: []*fakeNode{terminal, editor1, editor2, untitledApp, windowless}}
	fr := newFakeRuntime(desktop)

	got := listAppsText(fr.rt)
	// "Editor"/"editor" tie on the lowercased name -> lower pid first
	want := "bare -- bare [running, pid=9, window=untitled]\n" +
		"Editor -- Editor [running, pid=4242, window=a.txt]\n" +
		"editor -- editor [running, pid=4243, window=b.txt]\n" +
		"Terminal -- Terminal [running, pid=5000, window=shell]"
	if got != want {
		t.Fatalf("listAppsText =\n%q\nwant\n%q", got, want)
	}
}

func TestMatchesQueryRules(t *testing.T) {
	desktop := editorFixture()
	app, _, _, _ := fixtureParts(desktop)
	for _, query := range []string{"Text Editor", "text editor", "editor", "doc.txt", "DOC", "4242", "  Text Editor  "} {
		if !matchesQuery(app, query) {
			t.Fatalf("matchesQuery(%q) = false", query)
		}
	}
	for _, query := range []string{"", "  ", "4243", "other", "42420"} {
		if matchesQuery(app, query) {
			t.Fatalf("matchesQuery(%q) = true", query)
		}
	}
	// resolving by pid works through the desktop
	fr := newFakeRuntime(desktop)
	resolved, err := resolveApp(fr.rt, "4242")
	if err != nil || resolved.Name() != "Text Editor" {
		t.Fatalf("resolveApp(4242) = %v, %v", resolved, err)
	}
	if _, err := resolveApp(fr.rt, "9999"); err == nil || err.Error() != `appNotFound("9999")` {
		t.Fatalf("resolveApp pid miss = %v", err)
	}
}

// --- window selection ------------------------------------------------------------

func TestMainWindowPrefersActiveThenShowingThenFirst(t *testing.T) {
	w1 := &fakeNode{name: "w1", role: "frame"}
	w2 := &fakeNode{name: "w2", role: "dialog"} // dialog counts as a window
	w3 := &fakeNode{name: "w3", role: "frame", states: fakeStates(atspiStateShowing)}
	app := &fakeNode{name: "app", children: []*fakeNode{w1, w2, w3}}
	if index, win, err := mainWindow(app); err != nil || win.Name() != "w3" || index != 2 {
		t.Fatalf("SHOWING pick = %d %s %v", index, win.Name(), err)
	}
	w2.states = fakeStates(atspiStateActive)
	if _, win, _ := mainWindow(app); win.Name() != "w2" {
		t.Fatalf("ACTIVE beats SHOWING, got %s", win.Name())
	}
	w1.states = fakeStates(atspiStateActive)
	if _, win, _ := mainWindow(app); win.Name() != "w1" {
		t.Fatalf("first ACTIVE in window order wins, got %s", win.Name())
	}
	// extents-only children count as windows too
	plain := &fakeNode{name: "plain", role: "panel", extents: &[4]int32{0, 0, 5, 5}}
	oversized := &fakeNode{name: "huge", role: "panel", extents: &[4]int32{0, 0, 100001, 5}}
	app2 := &fakeNode{name: "app2", children: []*fakeNode{plain, oversized}}
	windows := appWindows(app2)
	if len(windows) != 1 || windows[0].node.Name() != "plain" {
		t.Fatalf("extents-based windows = %#v (oversized extents are invalid)", windows)
	}
}

// --- find_element fallback chain --------------------------------------------------

func TestFindElementFallbackChain(t *testing.T) {
	desktop := editorFixture()
	app, _, saveButton, textNode := fixtureParts(desktop)
	fr := newFakeRuntime(desktop)

	// 1. runtimeId path resolves directly
	node, err := findElement(fr.rt, app, &elementRecord{RuntimeID: []int{0, 0}})
	if err != nil || node != saveButton {
		t.Fatalf("path resolve = %v %v", node, err)
	}
	// 2. stale path -> automationId exact match
	node, _ = findElement(fr.rt, app, &elementRecord{RuntimeID: []int{0, 9}, AutomationID: "save-btn"})
	if node != saveButton {
		t.Fatalf("automationId rescan failed")
	}
	// 3. name+role both equal
	node, _ = findElement(fr.rt, app, &elementRecord{Name: "Save", ControlType: "push button"})
	if node != saveButton {
		t.Fatalf("name+role rescan failed")
	}
	// name equal but role different must NOT match
	node, _ = findElement(fr.rt, app, &elementRecord{Name: "Save", ControlType: "text"})
	if node != nil {
		t.Fatalf("name with wrong role matched %v", node.Name())
	}
	// 4. role equal + frame within 3px (window-relative: 110-100=10, 110-100=10, 80, 30)
	node, _ = findElement(fr.rt, app, &elementRecord{
		ControlType: "text",
		Frame:       &frame{X: 12, Y: 52, Width: 397, Height: 301},
	})
	if node != textNode {
		t.Fatalf("role+frame rescan failed")
	}
	// 4b. frame off by >3px does not match
	node, _ = findElement(fr.rt, app, &elementRecord{ControlType: "text", Frame: &frame{X: 14, Y: 50, Width: 400, Height: 300}})
	if node != nil {
		t.Fatalf("4px-off frame matched")
	}
	// nothing matches -> nil, nil
	node, err = findElement(fr.rt, app, &elementRecord{Name: "Ghost"})
	if node != nil || err != nil {
		t.Fatalf("no match = %v %v", node, err)
	}
	// nil record -> nil (no rescan, no error)
	node, err = findElement(fr.rt, app, nil)
	if node != nil || err != nil {
		t.Fatalf("nil record = %v %v", node, err)
	}
}

// --- focused/selected -------------------------------------------------------------

func TestFocusedSummaryAndSelectedText(t *testing.T) {
	desktop := editorFixture()
	_, window, saveButton, textNode := fixtureParts(desktop)
	fr := newFakeRuntime(desktop)

	saveButton.states = fakeStates(atspiStateFocused)
	if got := focusedSummary(fr.rt, 4242, limits(500)); got != "push button Save" {
		t.Fatalf("focusedSummary = %q", got)
	}
	// pid routing: no app for pid 1
	if got := focusedSummary(fr.rt, 1, limits(500)); got != "" {
		t.Fatalf("focusedSummary unknown pid = %q", got)
	}
	// no focused node -> ""
	saveButton.states = nil
	if got := focusedSummary(fr.rt, 4242, limits(500)); got != "" {
		t.Fatalf("focusedSummary none = %q", got)
	}

	// selected text: first selection of the focused Text node, end clamped
	// to start+limit+1
	textNode.states = fakeStates(atspiStateFocused)
	textNode.text = "hello world"
	textNode.selections = [][2]int{{0, 11}}
	if got := selectedText(fr.rt, 4242, limits(5)); got != "hello..." {
		t.Fatalf("selectedText limit 5 = %q", got)
	}
	if got := selectedText(fr.rt, 4242, nil); got != "hello world" {
		t.Fatalf("selectedText max = %q", got)
	}
	// focused node without Text interface -> ""
	textNode.states = nil
	saveButton.states = fakeStates(atspiStateFocused)
	if got := selectedText(fr.rt, 4242, limits(500)); got != "" {
		t.Fatalf("selectedText with focused button = %q", got)
	}
	_ = window
}

// --- preferred actions ------------------------------------------------------------

func TestPreferredActionIndex(t *testing.T) {
	node := &fakeNode{actions: []atspiAction{
		{Name: "", Description: "activate the thing"},
		{Name: "", Description: "Press"},
	}}
	if got := preferredActionIndex(node); got == nil || *got != 1 {
		t.Fatalf("exact match beats earlier fallback: %v", got)
	}
	node = &fakeNode{actions: []atspiAction{
		{Name: "", Description: "activate the thing"},
		{Name: "", Description: "something else"},
	}}
	if got := preferredActionIndex(node); got == nil || *got != 0 {
		t.Fatalf("fallback contains-activate: %v", got)
	}
	node = &fakeNode{actions: []atspiAction{{Name: "", Description: "nothing"}}}
	if got := preferredActionIndex(node); got != nil {
		t.Fatalf("no preferred action = %v", *got)
	}
	if doActionByIndex(node, nil) {
		t.Fatal("nil index must not call DoAction")
	}
}

// --- click dispatch -----------------------------------------------------------------

func TestClickAutoUsesAccessibilityActionFirst(t *testing.T) {
	desktop := editorFixture()
	_, _, saveButton, _ := fixtureParts(desktop)
	doLog := &[]int{}
	saveButton.doActionLog = doLog
	fr := newFakeRuntime(desktop)

	record := &elementRecord{Index: 0, RuntimeID: []int{0, 0}, ControlType: "push button", Name: "Save"}
	resp := performOperation(fr.rt, &linuxRequest{Tool: "click", App: "Text Editor", Element: record, MouseButton: "left", ClickMethod: "auto"})
	if !resp.OK || resp.Snapshot == nil {
		t.Fatalf("auto click = %#v", resp)
	}
	if len(*doLog) != 1 || (*doLog)[0] != 0 {
		t.Fatalf("DoAction log = %v", *doLog)
	}
	if len(fr.mouseEvents) != 0 {
		t.Fatalf("no mouse events expected, got %v", fr.mouseEvents)
	}
	// post-action settle is always 120ms
	if len(fr.sleeps) != 1 || fr.sleeps[0] != 120*time.Millisecond {
		t.Fatalf("sleeps = %v", fr.sleeps)
	}
}

func TestClickAutoFallsBackToCoordinates(t *testing.T) {
	desktop := editorFixture()
	_, _, saveButton, _ := fixtureParts(desktop)
	saveButton.doActionOK = false // action exists but fails
	fr := newFakeRuntime(desktop)

	record := &elementRecord{Index: 0, RuntimeID: []int{0, 0}, ControlType: "push button", Name: "Save",
		Frame: &frame{X: 10, Y: 10, Width: 80, Height: 30}}
	resp := performOperation(fr.rt, &linuxRequest{Tool: "click", App: "Text Editor", Element: record, MouseButton: "left", ClickMethod: "auto"})
	if !resp.OK {
		t.Fatalf("auto coordinate fallback = %q", resp.Error)
	}
	// center = window(100,100) + frame(10,10) + (40,15) = (150,125)
	want := []recordedMouseEvent{{150, 125, "abs"}, {150, 125, "b1p"}, {150, 125, "b1r"}}
	if fmt.Sprint(fr.mouseEvents) != fmt.Sprint(want) {
		t.Fatalf("mouse events = %v, want %v", fr.mouseEvents, want)
	}
	wantSleeps := []time.Duration{35 * time.Millisecond, 50 * time.Millisecond, 120 * time.Millisecond}
	if fmt.Sprint(fr.sleeps) != fmt.Sprint(wantSleeps) {
		t.Fatalf("sleeps = %v, want %v", fr.sleeps, wantSleeps)
	}
}

func TestClickAutoSkipsActionForNonLeftButton(t *testing.T) {
	desktop := editorFixture()
	_, _, saveButton, _ := fixtureParts(desktop)
	doLog := &[]int{}
	saveButton.doActionLog = doLog
	fr := newFakeRuntime(desktop)
	x, y := 5.0, 6.0
	resp := performOperation(fr.rt, &linuxRequest{Tool: "click", App: "Text Editor", ClickMethod: "auto",
		Element: &elementRecord{Index: 0, RuntimeID: []int{0, 0}, ControlType: "push button", Name: "Save"},
		X:       &x, Y: &y, MouseButton: "right"})
	if !resp.OK {
		t.Fatalf("right auto click = %q", resp.Error)
	}
	if len(*doLog) != 0 {
		t.Fatalf("DoAction should be skipped for right button: %v", *doLog)
	}
	want := []recordedMouseEvent{{105, 106, "abs"}, {105, 106, "b3p"}, {105, 106, "b3r"}}
	if fmt.Sprint(fr.mouseEvents) != fmt.Sprint(want) {
		t.Fatalf("mouse events = %v", fr.mouseEvents)
	}
}

func TestMouseButtonShortNamesFallBackToLeft(t *testing.T) {
	desktop := editorFixture()
	fr := newFakeRuntime(desktop)
	x, y := 1.0, 2.0
	resp := performOperation(fr.rt, &linuxRequest{Tool: "click", App: "Text Editor", ClickMethod: "global",
		X: &x, Y: &y, MouseButton: "r"}) // "r" is NOT right; quirk: unknown -> left
	if !resp.OK {
		t.Fatalf("short name click = %q", resp.Error)
	}
	want := []recordedMouseEvent{{101, 102, "abs"}, {101, 102, "b1p"}, {101, 102, "b1r"}}
	if fmt.Sprint(fr.mouseEvents) != fmt.Sprint(want) {
		t.Fatalf("mouse events = %v (short name must map to b1)", fr.mouseEvents)
	}
	// middle works
	fr2 := newFakeRuntime(editorFixture())
	resp = performOperation(fr2.rt, &linuxRequest{Tool: "click", App: "Text Editor", ClickMethod: "global",
		X: &x, Y: &y, MouseButton: "middle"})
	if !resp.OK || fmt.Sprint(fr2.mouseEvents[1].event) != "b2p" {
		t.Fatalf("middle click = %v %q", fr2.mouseEvents, resp.Error)
	}
}

func TestClickCountRepeatAndClamp(t *testing.T) {
	desktop := editorFixture()
	fr := newFakeRuntime(desktop)
	x, y := 0.0, 0.0
	resp := performOperation(fr.rt, &linuxRequest{Tool: "click", App: "Text Editor", ClickMethod: "global",
		X: &x, Y: &y, MouseButton: "left", ClickCount: 3})
	if !resp.OK {
		t.Fatalf("triple click = %q", resp.Error)
	}
	if len(fr.mouseEvents) != 9 {
		t.Fatalf("3 clicks = %d events, want 9", len(fr.mouseEvents))
	}
	fr = newFakeRuntime(editorFixture())
	resp = performOperation(fr.rt, &linuxRequest{Tool: "click", App: "Text Editor", ClickMethod: "global",
		X: &x, Y: &y, MouseButton: "left", ClickCount: 0})
	if !resp.OK || len(fr.mouseEvents) != 3 {
		t.Fatalf("count 0 clamps to 1: %v %q", fr.mouseEvents, resp.Error)
	}
}

// --- drag ---------------------------------------------------------------------------

func TestDragEventSequence(t *testing.T) {
	fr := newFakeRuntime(editorFixture())
	fx, fy, tx, ty := 0.0, 0.0, 120.0, 240.0
	resp := performOperation(fr.rt, &linuxRequest{Tool: "drag", App: "Text Editor", FromX: &fx, FromY: &fy, ToX: &tx, ToY: &ty})
	if !resp.OK {
		t.Fatalf("drag = %q", resp.Error)
	}
	events := fr.mouseEvents
	if len(events) != 15 {
		t.Fatalf("drag events = %d, want 15 (abs + b1p + 12 steps + b1r)", len(events))
	}
	if events[0] != (recordedMouseEvent{100, 100, "abs"}) || events[1] != (recordedMouseEvent{100, 100, "b1p"}) {
		t.Fatalf("drag start = %v %v", events[0], events[1])
	}
	if events[13] != (recordedMouseEvent{220, 340, "abs"}) || events[14] != (recordedMouseEvent{220, 340, "b1r"}) {
		t.Fatalf("drag end = %v %v", events[13], events[14])
	}
	// intermediate step 6 of 12: (100+120*6/12, 100+240*6/12) = (160, 220)
	if events[7] != (recordedMouseEvent{160, 220, "abs"}) {
		t.Fatalf("drag step 6 = %v", events[7])
	}
	if len(fr.sleeps) != 12+1 {
		t.Fatalf("drag sleeps = %d", len(fr.sleeps))
	}
}

// --- scroll -------------------------------------------------------------------------

func TestScrollElementKeyRepeat(t *testing.T) {
	fr := newFakeRuntime(editorFixture())
	resp := performOperation(fr.rt, &linuxRequest{Tool: "scroll", App: "Text Editor", Direction: "up", Pages: 2.3})
	if !resp.OK {
		t.Fatalf("scroll = %q", resp.Error)
	}
	// ceil(2.3) = 3 Page_Up PRESSRELEASEs (0xff55), 40ms apart
	if len(fr.keyEvents) != 3 {
		t.Fatalf("scroll key events = %v", fr.keyEvents)
	}
	for _, ev := range fr.keyEvents {
		if ev != (recordedKeyEvent{0xff55, "", atspiKeyPressRelease}) {
			t.Fatalf("scroll key = %v", ev)
		}
	}
	if len(fr.sleeps) != 4 || fr.sleeps[0] != 40*time.Millisecond {
		t.Fatalf("scroll sleeps = %v (3x40ms + 120ms settle)", fr.sleeps)
	}

	// direction down (default) and pages=0 -> one Page_Down
	fr = newFakeRuntime(editorFixture())
	resp = performOperation(fr.rt, &linuxRequest{Tool: "scroll", App: "Text Editor"})
	if !resp.OK || len(fr.keyEvents) != 1 || fr.keyEvents[0].keyval != 0xff56 {
		t.Fatalf("default scroll = %v %q", fr.keyEvents, resp.Error)
	}
	// left/right map to arrow keys
	fr = newFakeRuntime(editorFixture())
	resp = performOperation(fr.rt, &linuxRequest{Tool: "scroll", App: "Text Editor", Direction: "left"})
	if !resp.OK || fr.keyEvents[0].keyval != 0xff51 {
		t.Fatalf("left scroll = %v", fr.keyEvents)
	}
}

// --- keyboard -----------------------------------------------------------------------

func TestSendKeySequences(t *testing.T) {
	fr := newFakeRuntime(editorFixture())
	resp := performOperation(fr.rt, &linuxRequest{Tool: "press_key", App: "Text Editor", Key: "a"})
	if !resp.OK || len(fr.keyEvents) != 1 || fr.keyEvents[0] != (recordedKeyEvent{0, "a", atspiKeyString}) {
		t.Fatalf("'a' = %v %q", fr.keyEvents, resp.Error)
	}

	fr = newFakeRuntime(editorFixture())
	resp = performOperation(fr.rt, &linuxRequest{Tool: "press_key", App: "Text Editor", Key: "Return"})
	if !resp.OK || len(fr.keyEvents) != 1 || fr.keyEvents[0] != (recordedKeyEvent{0xff0d, "", atspiKeyPressRelease}) {
		t.Fatalf("Return = %v %q", fr.keyEvents, resp.Error)
	}

	fr = newFakeRuntime(editorFixture())
	resp = performOperation(fr.rt, &linuxRequest{Tool: "press_key", App: "Text Editor", Key: "enter"}) // alias
	if !resp.OK || fr.keyEvents[0].keyval != 0xff0d {
		t.Fatalf("enter alias = %v", fr.keyEvents)
	}

	fr = newFakeRuntime(editorFixture())
	resp = performOperation(fr.rt, &linuxRequest{Tool: "press_key", App: "Text Editor", Key: "ctrl+c"})
	want := []recordedKeyEvent{
		{0xffe3, "", atspiKeyPress},
		{0, "c", atspiKeyString},
		{0xffe3, "", atspiKeyRelease},
	}
	if !resp.OK || fmt.Sprint(fr.keyEvents) != fmt.Sprint(want) {
		t.Fatalf("ctrl+c = %v %q", fr.keyEvents, resp.Error)
	}

	fr = newFakeRuntime(editorFixture())
	// Raw keysym names are NOT valid modifiers: MODIFIER_KEYS only knows the
	// short aliases, so "Control_L"/"Shift_L" are silently skipped like any
	// unknown modifier, leaving a bare PRESSRELEASE of period.
	resp = performOperation(fr.rt, &linuxRequest{Tool: "press_key", App: "Text Editor", Key: "Control_L+Shift_L+period"})
	want = []recordedKeyEvent{
		{0x2e, "", atspiKeyPressRelease},
	}
	if !resp.OK || fmt.Sprint(fr.keyEvents) != fmt.Sprint(want) {
		t.Fatalf("chord = %v %q", fr.keyEvents, resp.Error)
	}

	// unknown modifier silently skipped
	fr = newFakeRuntime(editorFixture())
	resp = performOperation(fr.rt, &linuxRequest{Tool: "press_key", App: "Text Editor", Key: "bogusmod+x"})
	if !resp.OK || len(fr.keyEvents) != 1 || fr.keyEvents[0] != (recordedKeyEvent{0, "x", atspiKeyString}) {
		t.Fatalf("unknown modifier = %v %q", fr.keyEvents, resp.Error)
	}

	// modifier normalization: super/win/cmd -> Super_L
	fr = newFakeRuntime(editorFixture())
	resp = performOperation(fr.rt, &linuxRequest{Tool: "press_key", App: "Text Editor", Key: "win+F1"})
	want = []recordedKeyEvent{
		{0xffeb, "", atspiKeyPress},
		{0xffbe, "", atspiKeyPressRelease},
		{0xffeb, "", atspiKeyRelease},
	}
	if !resp.OK || fmt.Sprint(fr.keyEvents) != fmt.Sprint(want) {
		t.Fatalf("win+F1 = %v %q", fr.keyEvents, resp.Error)
	}
}

// --- text entry and set_value ---------------------------------------------------------

func TestTypeTextInsertsIntoEditableFirst(t *testing.T) {
	desktop := editorFixture()
	_, _, _, textNode := fixtureParts(desktop)
	insertLog := &[][3]any{}
	textNode.insertLog = insertLog
	fr := newFakeRuntime(desktop)
	resp := performOperation(fr.rt, &linuxRequest{Tool: "type_text", App: "Text Editor", Text: "héllo"})
	if !resp.OK {
		t.Fatalf("type_text = %q", resp.Error)
	}
	// offset = current character count (11), length counted in code points (5)
	if len(*insertLog) != 1 || (*insertLog)[0] != [3]any{11, "héllo", 5} {
		t.Fatalf("insert log = %v", *insertLog)
	}
	if len(fr.keyEvents) != 0 {
		t.Fatalf("no key events expected: %v", fr.keyEvents)
	}

	// no editable text anywhere -> keyboard STRING fallback
	desktop2 := editorFixture()
	_, _, _, text2 := fixtureParts(desktop2)
	text2.interfaces = []string{"Accessible", "Text"} // not editable
	fr2 := newFakeRuntime(desktop2)
	resp = performOperation(fr2.rt, &linuxRequest{Tool: "type_text", App: "Text Editor", Text: "abc"})
	if !resp.OK || len(fr2.keyEvents) != 1 || fr2.keyEvents[0] != (recordedKeyEvent{0, "abc", atspiKeyString}) {
		t.Fatalf("type_text fallback = %v %q", fr2.keyEvents, resp.Error)
	}
}

func TestSetValuePaths(t *testing.T) {
	desktop := editorFixture()
	_, _, _, textNode := fixtureParts(desktop)
	contentsLog := &[]string{}
	textNode.setContentsLog = contentsLog
	fr := newFakeRuntime(desktop)
	record := &elementRecord{Index: 1, RuntimeID: []int{0, 1}, ControlType: "text"}
	resp := performOperation(fr.rt, &linuxRequest{Tool: "set_value", App: "Text Editor", Element: record, Value: "new text"})
	if !resp.OK || len(*contentsLog) != 1 || (*contentsLog)[0] != "new text" {
		t.Fatalf("set_value editable = %v %q", *contentsLog, resp.Error)
	}

	// Value interface: parse the string as float like Python's float()
	slider := &fakeNode{role: "slider", interfaces: []string{"Accessible", "Value"}, hasValue: true, setValueOK: true}
	valueLog := &[]float64{}
	slider.setValueLog = valueLog
	desktop2 := &fakeNode{children: []*fakeNode{{name: "Mixer", pid: 7, children: []*fakeNode{
		{name: "win", role: "frame", extents: &[4]int32{0, 0, 10, 10}, children: []*fakeNode{slider}},
	}}}}
	fr2 := newFakeRuntime(desktop2)
	resp = performOperation(fr2.rt, &linuxRequest{Tool: "set_value", App: "Mixer", Element: &elementRecord{RuntimeID: []int{0, 0}}, Value: "3.5"})
	if !resp.OK || len(*valueLog) != 1 || (*valueLog)[0] != 3.5 {
		t.Fatalf("set_value numeric = %v %q", *valueLog, resp.Error)
	}
	// unparsable number -> not settable
	resp = performOperation(fr2.rt, &linuxRequest{Tool: "set_value", App: "Mixer", Element: &elementRecord{RuntimeID: []int{0, 0}}, Value: "abc"})
	if resp.OK || resp.Error != "Cannot set a value for an element that is not settable" {
		t.Fatalf("set_value abc = %q", resp.Error)
	}
}

// --- snapshot content --------------------------------------------------------------------

func TestBuildSnapshotContent(t *testing.T) {
	desktop := editorFixture()
	fr := newFakeRuntime(desktop)
	fr.capturePNG = "PNG64"
	snapshot, err := buildSnapshot(fr.rt, "Text Editor", limits(500), atspiMaxElements, atspiMaxDepth)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.App.Name != "Text Editor" || snapshot.App.BundleIdentifier != "Text Editor" || snapshot.App.PID != 4242 {
		t.Fatalf("app descriptor = %#v", snapshot.App)
	}
	if snapshot.WindowTitle != "doc.txt" {
		t.Fatalf("windowTitle = %q", snapshot.WindowTitle)
	}
	if snapshot.WindowBounds == nil || snapshot.WindowBounds.X != 100 || snapshot.WindowBounds.Width != 800 {
		t.Fatalf("windowBounds = %#v", snapshot.WindowBounds)
	}
	if snapshot.ScreenshotPNGBase64 != "PNG64" {
		t.Fatalf("screenshot = %q", snapshot.ScreenshotPNGBase64)
	}
	if len(snapshot.Elements) != 3 {
		t.Fatalf("elements = %d", len(snapshot.Elements))
	}
}

func TestPostActionSnapshotIgnoresRequestLimits(t *testing.T) {
	// pin runtime.py:864 — the refresh after an action always uses defaults
	longName := strings.Repeat("x", 600)
	window := &fakeNode{
		name: "w", role: "frame", extents: &[4]int32{0, 0, 10, 10},
		children: []*fakeNode{{name: longName, role: "label", extents: &[4]int32{0, 0, 5, 5}}},
	}
	desktop := &fakeNode{children: []*fakeNode{{name: "App", pid: 5, children: []*fakeNode{window}}}}
	fr := newFakeRuntime(desktop)
	x, y := 1.0, 1.0
	resp := performOperation(fr.rt, &linuxRequest{Tool: "click", App: "App", ClickMethod: "global", X: &x, Y: &y, TextLimit: "max"})
	if !resp.OK || resp.Snapshot == nil {
		t.Fatalf("click = %q", resp.Error)
	}
	if got := len(resp.Snapshot.Elements[1].Name); got != 503 {
		t.Fatalf("post-action name length = %d, want 500+3 (request text_limit=max must be ignored)", got)
	}
}

func TestListAppsOperationEnvelope(t *testing.T) {
	fr := newFakeRuntime(editorFixture())
	resp := performOperation(fr.rt, &linuxRequest{Tool: "list_apps"})
	if !resp.OK || resp.Snapshot != nil {
		t.Fatalf("list_apps envelope = %#v", resp)
	}
	want := "Text Editor -- Text Editor [running, pid=4242, window=doc.txt]"
	if resp.Text != want {
		t.Fatalf("list_apps text = %q (Idle has no windows and is skipped)", resp.Text)
	}
	// empty desktop -> empty text (main.go substitutes the "No running..." line)
	fr = newFakeRuntime(&fakeNode{})
	resp = performOperation(fr.rt, &linuxRequest{Tool: "list_apps"})
	if !resp.OK || resp.Text != "" {
		t.Fatalf("empty list_apps = %#v", resp)
	}
}

// --- screenshot black-frame ----------------------------------------------------------------

func TestLooksBlackRGB(t *testing.T) {
	black := make([]byte, 64*64*4)
	if !looksBlackRGB(black, 64, 64, 64*4, 4) {
		t.Fatal("all-zero image should be black")
	}
	// a channel value of 4 in one of the first three channels is enough
	black[1] = 4
	if looksBlackRGB(black, 64, 64, 64*4, 4) {
		t.Fatal("channel 4 at (0,0) should not be black")
	}
	// ...but the alpha channel is never examined (Python checks offset+0..2)
	black = make([]byte, 64*64*4)
	black[3] = 255
	if !looksBlackRGB(black, 64, 64, 64*4, 4) {
		t.Fatal("alpha channel must be ignored")
	}
	// but a bright pixel OUTSIDE the sampling grid stays invisible
	black = make([]byte, 64*64*4)
	black[1*64*4+1*4] = 255 // (1,1), grid step is 4
	if !looksBlackRGB(black, 64, 64, 64*4, 4) {
		t.Fatal("pixel outside the 16x16 grid must not matter")
	}
	if !looksBlackRGB(nil, 0, 10, 0, 4) || !looksBlackRGB(nil, 10, 10, 0, 2) {
		t.Fatal("degenerate inputs count as black")
	}
	if looksBlackRGB([]byte{255, 0, 0}, 1, 1, 3, 3) {
		t.Fatal("bright 1x1 RGB is not black")
	}
}

func TestCaptureRect(t *testing.T) {
	x, y, w, h := captureRect(&frame{X: 10.4, Y: 20.6, Width: 799.5, Height: 600.5})
	if x != 10 || y != 21 || w != 800 || h != 600 {
		t.Fatalf("captureRect = %d,%d,%d,%d (banker's rounding: 799.5 -> 800, 600.5 -> 600)", x, y, w, h)
	}
	_, _, w, h = captureRect(&frame{X: 0, Y: 0, Width: 0.2, Height: 0.4})
	if w != 1 || h != 1 {
		t.Fatalf("captureRect clamps to >= 1: %d %d", w, h)
	}
}
