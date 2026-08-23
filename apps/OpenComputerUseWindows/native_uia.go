//go:build windows

package main

// native_uia.go is the in-process UI Automation client binding (phase 3 of
// the native backend). It hand-rolls the COM interfaces via vtable slots
// verified against mingw-w64's uiautomationclient.h, and ports the
// tree-rendering / element-record / snapshot pipeline of the retired PS-era runtime
// byte-for-byte (Render-Tree, Get-ElementRecord, Get-PatternNames,
// Build-SnapshotForWindow, Get-FocusedSummary, Get-SelectedText,
// List-Windows, Get-MainElement).
//
// vtable slot notes (slot 0 = QueryInterface; IUnknown occupies 0-2):
//   IUIAutomation            5:GetRootElement 6:ElementFromHandle 8:GetFocusedElement
//                            21:CreateTrueCondition 23:CreatePropertyCondition
//   IUIAutomationElement      3:SetFocus 4:GetRuntimeId 6:FindAll 16:GetCurrentPattern
//                            20:ProcessId 21:ControlType 22:LocalizedControlType 23:Name
//                            29:AutomationId 30:ClassName 36:NativeWindowHandle 43:BoundingRectangle
//   IUIAutomationElementArray 3:get_Length 4:GetElement
//   IUIAutomationTextRangeArray 3:get_Length 4:GetElement
//   IUIAutomationTextPattern  5:GetSelection
//   IUIAutomationTextRange   12:GetText
//   IUIAutomationValuePattern 4:get_CurrentValue
//   IUIAutomationExpandCollapsePattern 5:get_CurrentExpandCollapseState
//
// GetSupportedPatterns (a managed-wrapper API with no COM equivalent) returns
// patterns sorted by pattern id; probing candidates in ascending id order
// reproduces the exact Get-PatternNames ordering (verified against Notepad).

import (
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// CLSID_CUIAutomation8. Headers say ...7395c6, but this system's registry
	// (and Windows 11 generally) registers the client central class as
	// ...7395c9; both hand out IID_IUIAutomation.
	uiaCLSID = "{E22AD333-B25F-460C-83D0-0581107395C9}"
	uiaIID   = "{30CBE57D-D9D0-452A-AB13-7AC5AC4825EE}" // IID_IUIAutomation

	// IUIAutomation slots.
	uiaSlotGetRootElement          = 5
	uiaSlotGetRawViewWalker        = 16
	uiaSlotElementFromHandle       = 6
	uiaSlotGetFocusedElement       = 8
	uiaSlotCreateTrueCondition     = 21
	uiaSlotCreatePropertyCondition = 23

	// IUIAutomationElement slots.
	elemSlotSetFocus                = 3
	elemSlotGetRuntimeId            = 4
	elemSlotFindAll                 = 6
	elemSlotGetCurrentPattern       = 16
	elemSlotGetCurrentPropertyValue = 10
	elemSlotCurrentProcessId        = 20
	elemSlotCurrentControlType      = 21
	elemSlotCurrentLocalizedType    = 22
	elemSlotCurrentName             = 23
	elemSlotCurrentAutomationId     = 29
	elemSlotCurrentClassName        = 30
	elemSlotCurrentIsContentElement = 34
	elemSlotCurrentNativeWindow     = 36
	elemSlotCurrentBoundingRect     = 43

	// IUIAutomationTreeWalker slots (shared by the raw/control/content walkers).
	walkerSlotGetFirstChildElement  = 4
	walkerSlotGetNextSiblingElement = 6

	// Array pattern (ElementArray / TextRangeArray share the layout).
	arrSlotLength     = 3
	arrSlotGetElement = 4

	// Pattern slots.
	textSlotGetSelection     = 5
	textRangeSlotGetText     = 12
	valueSlotGetCurrentValue = 4
	expandCollapseSlotState  = 5

	// Property / pattern ids.
	uiaPropControlType               = 30003
	uiaPropIsExpandCollapseAvailable = 30028
	uiaPropIsInvokeAvailable         = 30031
	uiaPropIsScrollAvailable         = 30034
	uiaPropIsScrollItemAvailable     = 30035
	uiaPropIsSelectionItemAvailable  = 30036
	uiaPropIsToggleAvailable         = 30041
	uiaPropIsValueAvailable          = 30043
	uiaPropProcessId                 = 30002
	uiaPatternInvoke                 = 10000
	uiaPatternValue                  = 10002
	uiaPatternScroll                 = 10004
	uiaPatternExpand                 = 10005
	uiaPatternSelect                 = 10010
	uiaPatternText                   = 10014
	uiaPatternToggle                 = 10015
	uiaPatternScrollItem             = 10017

	treeScopeChildren = 2

	defaultTextLimit      = 500
	accessibilityMaxNodes = 1200
	accessibilityMaxDepth = 64
)

// uiaControlTypeNames maps CONTROLTYPEID to the ProgrammaticName form
// ("ControlType.Button") used by Get-ElementControlTypeName.
var uiaControlTypeNames = map[int32]string{
	50000: "ControlType.Button", 50001: "ControlType.Calendar",
	50002: "ControlType.CheckBox", 50003: "ControlType.ComboBox",
	50004: "ControlType.Edit", 50005: "ControlType.Hyperlink",
	50006: "ControlType.Image", 50007: "ControlType.ListItem",
	50008: "ControlType.List", 50009: "ControlType.Menu",
	50010: "ControlType.MenuBar", 50011: "ControlType.MenuItem",
	50012: "ControlType.ProgressBar", 50013: "ControlType.RadioButton",
	50014: "ControlType.ScrollBar", 50015: "ControlType.Slider",
	50016: "ControlType.Spinner", 50017: "ControlType.StatusBar",
	50018: "ControlType.Tab", 50019: "ControlType.TabItem",
	50020: "ControlType.Text", 50021: "ControlType.ToolBar",
	50022: "ControlType.ToolTip", 50023: "ControlType.Tree",
	50024: "ControlType.TreeItem", 50025: "ControlType.Custom",
	50026: "ControlType.Group", 50027: "ControlType.Thumb",
	50028: "ControlType.DataGrid", 50029: "ControlType.DataItem",
	50030: "ControlType.Document", 50031: "ControlType.SplitButton",
	50032: "ControlType.Window", 50033: "ControlType.Pane",
	50034: "ControlType.Header", 50035: "ControlType.HeaderItem",
	50036: "ControlType.Table", 50037: "ControlType.TitleBar",
	50038: "ControlType.Separator", 50039: "ControlType.SemanticZoom",
}

// --- UIA apartment/thread plumbing ------------------------------------------

// All UIA COM calls run on one goroutine pinned to one OS thread with COM
// initialized, so apartment semantics stay deterministic regardless of which
// goroutine services a request.
var (
	uiaThreadOnce sync.Once
	uiaClientPtr  unsafe.Pointer
	uiaClientErr  error
	uiaJobs       chan func()
	uiaThreadID   uint32
)

func uiaOnThread(fn func()) error {
	uiaThreadOnce.Do(func() {
		uiaJobs = make(chan func())
		ready := make(chan struct{})
		go func() {
			runtime.LockOSThread()
			coInitializeEx(0 /* COINIT_MULTITHREADED */)
			uiaThreadID = windows.GetCurrentThreadId()
			close(ready)
			for job := range uiaJobs {
				job()
			}
		}()
		<-ready
		uiaClientPtr, uiaClientErr = oleCreateInstance(uiaCLSID, uiaIID)
	})
	if uiaClientErr != nil {
		return uiaClientErr
	}
	if windows.GetCurrentThreadId() == uiaThreadID {
		fn() // already on the UIA thread; avoid self-deadlock
		return nil
	}
	// Flat node-API calls (UiaNavigate & co.) require COM to be initialized
	// on the calling thread; dispatch every job to the dedicated UIA thread.
	done := make(chan struct{})
	uiaJobs <- func() { fn(); close(done) }
	<-done
	return nil
}

// uiaPtr converts a raw address to unsafe.Pointer through a pointer round
// trip so `go vet`'s unsafeptr check accepts it (same trick as storing COM
// out-params in unsafe.Pointer variables).
func uiaPtr(base uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&base))
}

// --- oleaut32 helpers -------------------------------------------------------

var (
	procSysFreeString    = windows.NewLazySystemDLL("oleaut32.dll").NewProc("SysFreeString")
	procVariantClear     = windows.NewLazySystemDLL("oleaut32.dll").NewProc("VariantClear")
	procSafeArrayDestroy = windows.NewLazySystemDLL("oleaut32.dll").NewProc("SafeArrayDestroy")
)

// bstrToString reads a BSTR out parameter; hr must be checked by the caller
// before invoking this. Freeing is skipped for empty strings.
func bstrToString(p uintptr) string {
	if p == 0 {
		return ""
	}
	s := bstrToStringKeep(p)
	procSysFreeString.Call(p)
	return s
}

// bstrToStringKeep copies a BSTR without freeing it — for BSTRs owned by a
// VARIANT, whose VariantClear call performs the single release.
func bstrToStringKeep(p uintptr) string {
	if p == 0 {
		return ""
	}
	n := *(*uint32)(uiaPtr(p - 4))
	return windows.UTF16ToString(unsafe.Slice((*uint16)(uiaPtr(p)), n/2))
}

// safeArrayCount returns cElements of the first (only) bound dimension.
func safeArrayCount(sa uintptr) int {
	if sa == 0 {
		return 0
	}
	return int(*(*uint32)(uiaPtr(sa + 24))) // rgsabound[0].cElements
}

// safeArrayInts reads a SAFEARRAY of I4 (GetRuntimeId) and destroys it.
func safeArrayInts(sa uintptr) []int {
	count := safeArrayCount(sa)
	if count == 0 {
		if sa != 0 {
			procSafeArrayDestroy.Call(sa)
		}
		return []int{}
	}
	data := *(*uintptr)(uiaPtr(sa + 16)) // pvData
	values := unsafe.Slice((*int32)(uiaPtr(data)), count)
	out := make([]int, count)
	for i, v := range values {
		out[i] = int(v)
	}
	procSafeArrayDestroy.Call(sa)
	return out
}

// --- element wrapper ---------------------------------------------------------

type uiaElement struct{ ptr unsafe.Pointer }

func (e uiaElement) valid() bool { return e.ptr != nil }

func (e uiaElement) release() {
	if e.ptr != nil {
		vtableCall(e.ptr, 2)
	}
}

func (e uiaElement) processId() int32 {
	var out int32
	if hr, _, _ := vtableCall(e.ptr, elemSlotCurrentProcessId, uintptr(unsafe.Pointer(&out))); int32(hr) < 0 {
		return 0
	}
	return out
}

func (e uiaElement) controlTypeId() int32 {
	var out int32
	if hr, _, _ := vtableCall(e.ptr, elemSlotCurrentControlType, uintptr(unsafe.Pointer(&out))); int32(hr) < 0 {
		return 0
	}
	return out
}

func (e uiaElement) controlTypeName() string {
	return uiaControlTypeNames[e.controlTypeId()]
}

func (e uiaElement) bstrProp(slot int) string {
	var p uintptr
	if hr, _, _ := vtableCall(e.ptr, slot, uintptr(unsafe.Pointer(&p))); int32(hr) < 0 {
		return ""
	}
	return bstrToString(p)
}

func (e uiaElement) localizedControlType() string { return e.bstrProp(elemSlotCurrentLocalizedType) }

// uiaLocalizedControlTypeEN maps CONTROLTYPEID to the English localized
// name. the retired PS-era runtime's managed UIA client returns these on this machine
// (dominant behavior; the first property access can occasionally race to
// the zh-CN core resources instead). Dual-run diffs normalize the two.
var uiaLocalizedControlTypeEN = map[int32]string{
	50000: "button", 50001: "calendar", 50002: "check box",
	50003: "combo box", 50004: "edit", 50005: "hyperlink",
	50006: "image", 50007: "list item", 50008: "list",
	50009: "menu", 50010: "menu bar", 50011: "menu item",
	50012: "progress bar", 50013: "radio button", 50014: "scroll bar",
	50015: "slider", 50016: "spinner", 50017: "status bar",
	50018: "tab", 50019: "tab item", 50020: "text", 50021: "tool bar",
	50022: "tooltip", 50023: "tree", 50024: "tree item", 50025: "custom",
	50026: "group", 50027: "thumb", 50028: "data grid",
	50029: "data item", 50030: "document", 50031: "split button",
	50032: "window", 50033: "pane", 50034: "header",
	50035: "header item", 50036: "table", 50037: "title bar",
	50038: "separator", 50039: "semantic zoom",
}

// localizedControlTypeName mirrors the PS daemon's observable behavior: for
// Win32-framework elements the managed client's MSAA proxies report the
// English localized control type (e.g. "pane") even on a localized system,
// while non-Win32 providers (Chrome "区域", XAML) supply their own localized
// value that must be passed through untouched.
func (e uiaElement) localizedControlTypeName() string {
	if e.frameworkId() == "Win32" {
		if name, ok := uiaLocalizedControlTypeEN[e.controlTypeId()]; ok {
			return name
		}
	}
	return e.localizedControlType()
}

// frameworkId reads UIA_FrameworkIdPropertyId (30024). The BSTR is owned by
// the VARIANT: copy it, then let VariantClear perform the single free (a
// manual SysFreeString here double-frees and corrupts the heap).
func (e uiaElement) frameworkId() string {
	v := oleVariant{}
	if hr, _, _ := vtableCall(e.ptr, elemSlotGetCurrentPropertyValue,
		uintptr(30024), uintptr(unsafe.Pointer(&v))); int32(hr) < 0 || v.vt != 8 {
		procVariantClear.Call(uintptr(unsafe.Pointer(&v)))
		return ""
	}
	p := *(*uintptr)(unsafe.Pointer(&v.value[0]))
	s := bstrToStringKeep(p)
	procVariantClear.Call(uintptr(unsafe.Pointer(&v)))
	return s
}

func (e uiaElement) name() string         { return e.bstrProp(elemSlotCurrentName) }
func (e uiaElement) automationId() string { return e.bstrProp(elemSlotCurrentAutomationId) }
func (e uiaElement) className() string    { return e.bstrProp(elemSlotCurrentClassName) }

func (e uiaElement) nativeWindowHandle() int64 {
	var out uintptr
	if hr, _, _ := vtableCall(e.ptr, elemSlotCurrentNativeWindow, uintptr(unsafe.Pointer(&out))); int32(hr) < 0 {
		return 0
	}
	return int64(out)
}

func (e uiaElement) runtimeId() []int {
	var sa uintptr
	if hr, _, _ := vtableCall(e.ptr, elemSlotGetRuntimeId, uintptr(unsafe.Pointer(&sa))); int32(hr) < 0 {
		return nil
	}
	return safeArrayInts(sa)
}

func (e uiaElement) runtimeIdKey() string {
	ids := e.runtimeId()
	if ids == nil {
		return "" // caller falls back to a random key (PS uses a GUID)
	}
	return strings.Join(intsToStrings(ids), ".")
}

// boundingRect mirrors get_CurrentBoundingRectangle (RECT of LONGs).
func (e uiaElement) boundingRect() (x, y, w, h float64, ok bool) {
	var rect [4]int32
	if hr, _, _ := vtableCall(e.ptr, elemSlotCurrentBoundingRect, uintptr(unsafe.Pointer(&rect[0]))); int32(hr) < 0 {
		return 0, 0, 0, 0, false
	}
	left, top, right, bottom := rect[0], rect[1], rect[2], rect[3]
	w = float64(right - left)
	h = float64(bottom - top)
	if left == 0 && top == 0 && right == 0 && bottom == 0 {
		return 0, 0, 0, 0, false // Rect.IsEmpty equivalent
	}
	if w <= 0 || h <= 0 {
		return 0, 0, 0, 0, false
	}
	return float64(left), float64(top), w, h, true
}

// currentPattern mirrors GetCurrentPattern: nil when unsupported.
func (e uiaElement) currentPattern(patternId int32) unsafe.Pointer {
	var out unsafe.Pointer
	if hr, _, _ := vtableCall(e.ptr, elemSlotGetCurrentPattern,
		uintptr(patternId), uintptr(unsafe.Pointer(&out))); int32(hr) < 0 {
		return nil
	}
	return out
}

// patternAvailable mirrors the managed IsXxxPatternAvailable properties
// (what GetSupportedPatterns reports); GetCurrentPattern alone can return
// patterns the provider does not declare (e.g. the WinUI Notepad document
// answers Scroll without reporting it).
func (e uiaElement) patternAvailable(propertyId int32) bool {
	variant := oleVariant{}
	if hr, _, _ := vtableCall(e.ptr, elemSlotGetCurrentPropertyValue,
		uintptr(propertyId), uintptr(unsafe.Pointer(&variant))); int32(hr) < 0 {
		return false
	}
	result := variant.vt == 11 /*VT_BOOL*/ && *(*int16)(unsafe.Pointer(&variant.value[0])) != 0
	procVariantClear.Call(uintptr(unsafe.Pointer(&variant)))
	return result
}

// findAllChildren mirrors FindAll(TreeScope.Children, TrueCondition).
func (e uiaElement) findAllChildren(condition unsafe.Pointer) []uiaElement {
	var arrayPtr unsafe.Pointer
	if hr, _, _ := vtableCall(e.ptr, elemSlotFindAll,
		uintptr(treeScopeChildren), uintptr(condition), uintptr(unsafe.Pointer(&arrayPtr))); int32(hr) < 0 {
		return nil
	}
	if arrayPtr == nil {
		return nil
	}
	defer oleRelease(arrayPtr)
	var length int32
	if hr, _, _ := vtableCall(arrayPtr, arrSlotLength, uintptr(unsafe.Pointer(&length))); int32(hr) < 0 {
		return nil
	}
	children := make([]uiaElement, 0, length)
	for i := int32(0); i < length; i++ {
		var elemPtr unsafe.Pointer
		if hr, _, _ := vtableCall(arrayPtr, arrSlotGetElement,
			uintptr(i), uintptr(unsafe.Pointer(&elemPtr))); int32(hr) < 0 || elemPtr == nil {
			continue
		}
		children = append(children, uiaElement{elemPtr})
	}
	return children
}

func intsToStrings(values []int) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = fmt.Sprintf("%d", v)
	}
	return out
}

// --- client-level helpers -----------------------------------------------------

func uiaElementFromHandle(hwnd int64) (uiaElement, error) {
	var out unsafe.Pointer
	hr, _, _ := vtableCall(uiaClientPtr, uiaSlotElementFromHandle,
		uintptr(hwnd), uintptr(unsafe.Pointer(&out)))
	if int32(hr) < 0 || out == nil {
		return uiaElement{}, fmt.Errorf("ElementFromHandle failed: 0x%08x", int32(hr))
	}
	return uiaElement{out}, nil
}

func uiaGetRootElement() (uiaElement, error) {
	var out unsafe.Pointer
	hr, _, _ := vtableCall(uiaClientPtr, uiaSlotGetRootElement, uintptr(unsafe.Pointer(&out)))
	if int32(hr) < 0 || out == nil {
		return uiaElement{}, fmt.Errorf("GetRootElement failed: 0x%08x", int32(hr))
	}
	return uiaElement{out}, nil
}

func uiaGetFocusedElement() (uiaElement, error) {
	var out unsafe.Pointer
	hr, _, _ := vtableCall(uiaClientPtr, uiaSlotGetFocusedElement, uintptr(unsafe.Pointer(&out)))
	if int32(hr) < 0 || out == nil {
		return uiaElement{}, fmt.Errorf("GetFocusedElement failed: 0x%08x", int32(hr))
	}
	return uiaElement{out}, nil
}

func uiaTrueCondition() (unsafe.Pointer, error) {
	var out unsafe.Pointer
	hr, _, _ := vtableCall(uiaClientPtr, uiaSlotCreateTrueCondition, uintptr(unsafe.Pointer(&out)))
	if int32(hr) < 0 || out == nil {
		return nil, fmt.Errorf("CreateTrueCondition failed: 0x%08x", int32(hr))
	}
	return out, nil
}

// oleVariant is the x64 VARIANT layout (24 bytes).
type oleVariant struct {
	vt    uint16
	r1    uint16
	r2    uint16
	r3    uint16
	value [8]byte
}

func uiaPropertyConditionInt(propertyId int32, value int32) (unsafe.Pointer, error) {
	variant := oleVariant{vt: 3 /* VT_I4 */}
	*(*int32)(unsafe.Pointer(&variant.value[0])) = value
	var out unsafe.Pointer
	hr, _, _ := vtableCall(uiaClientPtr, uiaSlotCreatePropertyCondition,
		uintptr(propertyId), uintptr(unsafe.Pointer(&variant)), uintptr(unsafe.Pointer(&out)))
	if int32(hr) < 0 || out == nil {
		return nil, fmt.Errorf("CreatePropertyCondition failed: 0x%08x", int32(hr))
	}
	return out, nil
}

// uiaRawViewWalker mirrors [Windows.Automation.TreeWalker]::RawViewWalker.
func uiaRawViewWalker() (unsafe.Pointer, error) {
	var out unsafe.Pointer
	hr, _, _ := vtableCall(uiaClientPtr, uiaSlotGetRawViewWalker, uintptr(unsafe.Pointer(&out)))
	if int32(hr) < 0 || out == nil {
		return nil, fmt.Errorf("get_RawViewWalker failed: 0x%08x", int32(hr))
	}
	return out, nil
}

// walkerChildren enumerates the raw-view children of an element, skipping
// IsContentElement=0 nodes. The managed FindAll(TreeScope.Children,
// TrueCondition) that Render-Tree uses hides the proxy-generated title bar
// and menu bar subtrees (both content=0); this filter reproduces its exact
// output (verified against Notepad, Explorer, Everything, and Edge).
func walkerChildren(walker unsafe.Pointer, e uiaElement) []uiaElement {
	var first unsafe.Pointer
	if hr, _, _ := vtableCall(walker, walkerSlotGetFirstChildElement,
		uintptr(e.ptr), uintptr(unsafe.Pointer(&first))); int32(hr) < 0 {
		return nil
	}
	children := make([]uiaElement, 0, 8)
	for current := first; current != nil; {
		var next unsafe.Pointer
		hr, _, _ := vtableCall(walker, walkerSlotGetNextSiblingElement,
			uintptr(current), uintptr(unsafe.Pointer(&next)))
		var isContent int32
		if hrC, _, _ := vtableCall(current, elemSlotCurrentIsContentElement,
			uintptr(unsafe.Pointer(&isContent))); int32(hrC) >= 0 && isContent != 0 {
			children = append(children, uiaElement{current})
		} else {
			oleRelease(current)
		}
		if int32(hr) < 0 {
			break
		}
		current = next
	}
	return children
}

// --- Get-PatternNames / Get-ElementValue ports --------------------------------

// Note on pattern semantics: the retired PS-era runtime's managed client
// registered .NET Framework client-side proxies (UiaCoreApi's static ctor
// calls UiaRegisterProviderCallback), which changed pattern availability and
// LocalizedControlType answers for Win32-hosted content versus a plain COM
// client (e.g. the WinUI Notepad document answers Scroll to GetCurrentPattern
// while the managed client reported it unsupported). That proxy layer cannot
// be reproduced in-process from Go; per the 2026-08-22 decision the raw COM
// answers below ARE the behavior baseline for the tree tools (the same view
// the official Swift runtime gets).

// uiaPatternActions probes candidate patterns in ascending pattern-id order,
// which is the order GetSupportedPatterns returns them in (verified against
// Notepad: Document=Value,Text; MenuItem=Invoke,ExpandCollapse,ScrollItem;
// Text=Text,ScrollItem — all ascending by id).
func uiaPatternActions(e uiaElement) []string {
	var names []string
	add := func(n string) { names = append(names, n) }
	for _, candidate := range []struct {
		id   int32
		prop int32
		name string
	}{
		{uiaPatternInvoke, uiaPropIsInvokeAvailable, "Invoke"},
		{uiaPatternValue, uiaPropIsValueAvailable, "SetValue"},
		{uiaPatternScroll, uiaPropIsScrollAvailable, "Scroll"},
		{uiaPatternExpand, uiaPropIsExpandCollapseAvailable, ""},
		{uiaPatternSelect, uiaPropIsSelectionItemAvailable, "Select"},
		{uiaPatternToggle, uiaPropIsToggleAvailable, "Toggle"},
		{uiaPatternScrollItem, uiaPropIsScrollItemAvailable, "ScrollIntoView"},
	} {
		if !e.patternAvailable(candidate.prop) {
			continue
		}
		pattern := e.currentPattern(candidate.id)
		if candidate.id == uiaPatternExpand {
			var state int32
			hr, _, _ := vtableCall(pattern, expandCollapseSlotState, uintptr(unsafe.Pointer(&state)))
			switch {
			case int32(hr) < 0:
				add("Expand")
				add("Collapse")
			case state == 0: // Collapsed
				add("Expand")
			case state == 1: // Expanded
				add("Collapse")
			}
			oleRelease(pattern)
			continue
		}
		add(candidate.name)
		oleRelease(pattern)
	}
	return names
}

// uiaElementValue mirrors Get-ElementValue (ValuePattern.Current.Value).
func uiaElementValue(e uiaElement, textLimit *int) string {
	pattern := e.currentPattern(uiaPatternValue)
	if pattern == nil {
		return ""
	}
	defer oleRelease(pattern)
	var p uintptr
	if hr, _, _ := vtableCall(pattern, valueSlotGetCurrentValue, uintptr(unsafe.Pointer(&p))); int32(hr) < 0 {
		return ""
	}
	return limitTextPS(bstrToString(p), textLimit)
}

// --- text-limit / rendering ports ----------------------------------------------

// limitTextPS mirrors Limit-Text: nil limit = "max" (no truncation).
func limitTextPS(text string, textLimit *int) string {
	if textLimit == nil {
		return text
	}
	runes := []rune(text)
	if len(runes) > *textLimit {
		return string(runes[:*textLimit]) + "..."
	}
	return text
}

// resolveTextLimitPS mirrors Resolve-TextLimit; nil result means "max".
func resolveTextLimitPS(value any) *int {
	switch v := value.(type) {
	case nil:
		limit := defaultTextLimit
		return &limit
	case bool:
		limit := defaultTextLimit
		return &limit
	case string:
		if strings.ToLower(strings.TrimSpace(v)) == "max" {
			return nil
		}
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil && n > 0 {
			return &n
		}
		limit := defaultTextLimit
		return &limit
	case float64:
		if v > 0 {
			n := int(v)
			return &n
		}
		limit := defaultTextLimit
		return &limit
	case int:
		if v > 0 {
			return &v
		}
		limit := defaultTextLimit
		return &limit
	}
	limit := defaultTextLimit
	return &limit
}

func framePSString(f *frame) string {
	round := func(v float64) int64 {
		r := int64(math.Round(v))
		if r == 0 {
			r = 0 // normalize -0
		}
		return r
	}
	return fmt.Sprintf("{x: %d, y: %d, width: %d, height: %d}",
		round(f.X), round(f.Y), round(f.Width), round(f.Height))
}

// uiaElementFrame mirrors Get-ElementFrame (window-relative).
func uiaElementFrame(e uiaElement, windowBounds *frame) *frame {
	x, y, w, h, ok := e.boundingRect()
	if !ok {
		return nil
	}
	if windowBounds != nil {
		return &frame{X: x - windowBounds.X, Y: y - windowBounds.Y, Width: w, Height: h}
	}
	return &frame{X: x, Y: y, Width: w, Height: h}
}

// uiaElementRecord mirrors Get-ElementRecord (field order = JSON contract).
func uiaElementRecord(e uiaElement, index int, windowBounds *frame, textLimit *int) elementRecord {
	runtimeId := e.runtimeId()
	if runtimeId == nil {
		runtimeId = []int{}
	}
	actions := uiaPatternActions(e)
	selected := false
	if e.patternAvailable(uiaPropIsSelectionItemAvailable) {
		if pattern := e.currentPattern(uiaPatternSelect); pattern != nil {
			var flag int32
			if hr, _, _ := vtableCall(pattern, selectSlotIsSelected, uintptr(unsafe.Pointer(&flag))); int32(hr) >= 0 {
				selected = flag != 0
			}
			oleRelease(pattern)
		}
	}
	return elementRecord{
		Index:                index,
		RuntimeID:            runtimeId,
		AutomationID:         e.automationId(),
		Name:                 limitTextPS(e.name(), textLimit),
		ControlType:          e.controlTypeName(),
		LocalizedControlType: e.localizedControlTypeName(),
		ClassName:            e.className(),
		Value:                uiaElementValue(e, textLimit),
		NativeWindowHandle:   e.nativeWindowHandle(),
		Frame:                uiaElementFrame(e, windowBounds),
		Actions:              actions,
		Selected:             selected,
	}
}

// uiaElementLine renders one element as a tree line body (index, role, title,
// Value, Secondary Actions, Frame) without indentation; uiaRenderTree prefixes
// depth tabs and selected_elements reuses it bare.
func uiaElementLine(record elementRecord) string {
	role := record.LocalizedControlType
	if strings.TrimSpace(role) == "" {
		role = record.ControlType
	}
	title := uiaElementTitle(record)
	line := fmt.Sprintf("%d %s %s", record.Index, role, title)
	if strings.TrimSpace(record.Value) != "" && record.Value != title {
		safeValue := strings.ReplaceAll(strings.ReplaceAll(record.Value, "\r", "\\r"), "\n", "\\n")
		line += " Value: " + safeValue
	}
	if len(record.Actions) > 0 {
		line += " Secondary Actions: " + strings.Join(record.Actions, ", ")
	}
	if record.Frame != nil {
		line += " Frame: " + framePSString(record.Frame)
	}
	return line
}

func uiaElementTitle(record elementRecord) string {
	if strings.TrimSpace(record.Name) != "" {
		return record.Name
	}
	if strings.TrimSpace(record.AutomationID) != "" {
		return "ID: " + record.AutomationID
	}
	return ""
}

// uiaRenderTree mirrors Render-Tree: budget/depth checks, runtimeId dedupe,
// byte-identical line rendering.
func uiaRenderTree(root uiaElement, walker unsafe.Pointer, windowBounds *frame,
	textLimit *int, maxTreeNodes, maxTreeDepth int) ([]elementRecord, []string) {

	if maxTreeNodes <= 0 {
		maxTreeNodes = accessibilityMaxNodes
	}
	if maxTreeDepth <= 0 {
		maxTreeDepth = accessibilityMaxDepth
	}

	records := make([]elementRecord, 0, 64)
	lines := make([]string, 0, 64)
	visited := map[string]bool{}
	nextIndex := 0
	counter := 0
	var visit func(node uiaElement, depth int)
	visit = func(node uiaElement, depth int) {
		if nextIndex >= maxTreeNodes || depth > maxTreeDepth {
			return
		}
		runtime := node.runtimeIdKey()
		if runtime == "" {
			// PS falls back to [guid]::NewGuid() on GetRuntimeId failure.
			counter++
			runtime = fmt.Sprintf("fallback-%d-%d", counter, windows.GetCurrentProcessId())
		}
		if visited[runtime] {
			return
		}
		visited[runtime] = true

		index := nextIndex
		nextIndex++
		record := uiaElementRecord(node, index, windowBounds, textLimit)
		records = append(records, record)

		lines = append(lines, strings.Repeat("	", depth+1)+uiaElementLine(record))

		for _, child := range walkerChildren(walker, node) {
			visit(child, depth+1)
			child.release()
		}
	}
	visit(root, 0)

	if records == nil {
		records = []elementRecord{}
	}
	if lines == nil {
		lines = []string{}
	}
	return records, lines
}

// --- focused summary / selected text ports --------------------------------------

// uiaFocusedSummary mirrors Get-FocusedSummary.
func uiaFocusedSummary(processId int32, textLimit *int) string {
	focused, err := uiaGetFocusedElement()
	if err != nil {
		return ""
	}
	defer focused.release()
	if focused.processId() != processId {
		return ""
	}
	role := focused.localizedControlTypeName()
	name := limitTextPS(focused.name(), textLimit)
	if strings.TrimSpace(name) == "" {
		return role
	}
	return role + " " + name
}

// uiaSelectedText mirrors Get-SelectedText.
func uiaSelectedText(processId int32, textLimit *int) string {
	focused, err := uiaGetFocusedElement()
	if err != nil {
		return ""
	}
	defer focused.release()
	if focused.processId() != processId {
		return ""
	}
	pattern := focused.currentPattern(uiaPatternText)
	if pattern == nil {
		return ""
	}
	defer oleRelease(pattern)
	var arrayPtr unsafe.Pointer
	if hr, _, _ := vtableCall(pattern, textSlotGetSelection, uintptr(unsafe.Pointer(&arrayPtr))); int32(hr) < 0 || arrayPtr == nil {
		return ""
	}
	defer oleRelease(arrayPtr)
	var length int32
	if hr, _, _ := vtableCall(arrayPtr, arrSlotLength, uintptr(unsafe.Pointer(&length))); int32(hr) < 0 || length <= 0 {
		return ""
	}
	var rangePtr unsafe.Pointer
	if hr, _, _ := vtableCall(arrayPtr, arrSlotGetElement,
		0, uintptr(unsafe.Pointer(&rangePtr))); int32(hr) < 0 || rangePtr == nil {
		return ""
	}
	defer oleRelease(rangePtr)
	maxLength := -1
	if textLimit != nil {
		maxLength = *textLimit + 1
	}
	var textPtr uintptr
	if hr, _, _ := vtableCall(rangePtr, textRangeSlotGetText,
		uintptr(maxLength), uintptr(unsafe.Pointer(&textPtr))); int32(hr) < 0 {
		return ""
	}
	return limitTextPS(bstrToString(textPtr), textLimit)
}

// --- List-Windows port -----------------------------------------------------------

// uiaListWindows mirrors List-Windows: UIA root children filtered to Window
// control type, in UIA enumeration order.
func uiaListWindows() ([]windowRef, error) {
	var refs []windowRef
	var jobErr error
	err := uiaOnThread(func() {
		root, err := uiaGetRootElement()
		if err != nil {
			jobErr = err
			return
		}
		defer root.release()
		condition, err := uiaPropertyConditionInt(uiaPropControlType, 50032 /* Window */)
		if err != nil {
			jobErr = err
			return
		}
		defer oleRelease(condition)
		children := root.findAllChildren(condition)
		for _, child := range children {
			handle := child.nativeWindowHandle()
			if handle == 0 || !windows.IsWindow(windows.HWND(handle)) {
				child.release()
				continue
			}
			pid := child.processId()
			processName := ""
			if pid > 0 {
				processName = processNameByPID(uint32(pid))
			}
			refs = append(refs, windowRef{
				App:   processName,
				ID:    handle,
				Title: child.name(),
			})
			child.release()
		}
	})
	if err != nil {
		return nil, err
	}
	if jobErr != nil {
		return nil, jobErr
	}
	return refs, nil
}

// uiaWindowsForProcess mirrors Get-WindowsForProcess: the main window first,
// then the other UIA top-level windows of the same pid.
func uiaWindowsForProcess(processName string, pid uint32, mainHWND uintptr, mainTitle string) []windowRef {
	windows := []windowRef{{App: processName, ID: int64(mainHWND), Title: mainTitle}}
	_ = uiaOnThread(func() {
		root, err := uiaGetRootElement()
		if err != nil {
			return
		}
		defer root.release()
		condition, err := uiaPropertyConditionInt(uiaPropProcessId, int32(pid))
		if err != nil {
			return
		}
		defer oleRelease(condition)
		children := root.findAllChildren(condition)
		for _, child := range children {
			handle := child.nativeWindowHandle()
			if handle == 0 || uintptr(handle) == mainHWND {
				child.release()
				continue
			}
			windows = append(windows, windowRef{
				App:   processName,
				ID:    handle,
				Title: child.name(),
			})
			child.release()
		}
	})
	return windows
}

// --- Get-MainElement / Build-SnapshotForWindow ports -------------------------------

// uiaMainElement mirrors Get-MainElement.
func uiaMainElement(mainHWND uintptr, pid uint32, processName string) (uiaElement, error) {
	if mainHWND != 0 {
		return uiaElementFromHandle(int64(mainHWND))
	}
	root, err := uiaGetRootElement()
	if err != nil {
		return uiaElement{}, err
	}
	defer root.release()
	condition, err := uiaPropertyConditionInt(uiaPropProcessId, int32(pid))
	if err != nil {
		return uiaElement{}, err
	}
	defer oleRelease(condition)
	children := root.findAllChildren(condition)
	defer func() {
		for _, child := range children {
			child.release()
		}
	}()
	if len(children) == 0 {
		return uiaElement{}, fmt.Errorf("No top-level UI Automation window is available for %s. Run the Windows runtime in the signed-in desktop session.", processName)
	}
	// Hand the first child to the caller; release the rest.
	first := children[0]
	children[0] = uiaElement{}
	return first, nil
}

// uiaBuildSnapshotForWindow mirrors Build-SnapshotForWindow with the
// screenshot supplied by the caller: the capture chain MUST run BEFORE any
// UIA tree walk in the same operation. Running PrintWindow/WGC after a tree
// walk corrupts the heap on this DWM/driver stack when the window has just
// moved; capturing first and walking second is stable (see history
// 2026-08-22). bounds is the window rect measured for the capture; png is
// the already-encoded screenshot ("" = omitted).
func uiaBuildSnapshotForWindow(appName string, pid int32, hwnd int64, element uiaElement,
	bounds *frame, png string, textLimit *int, maxTreeNodes, maxTreeDepth int) *appSnapshot {

	if bounds == nil {
		// Get-WindowBounds fallback: the element's own rect.
		if x, y, w, h, ok := element.boundingRect(); ok {
			bounds = &frame{X: x, Y: y, Width: w, Height: h}
		}
	}

	walker, walkerErr := uiaRawViewWalker()
	var records []elementRecord
	var lines []string
	if walkerErr == nil {
		defer oleRelease(walker)
		records, lines = uiaRenderTree(element, walker, bounds, textLimit, maxTreeNodes, maxTreeDepth)
	}

	// Official accessibility output fields: document_text is the value of
	// the most relevant document element (Document control type, falling
	// back to the first editable); selected_elements lists tree-formatted
	// lines for every currently selected SelectionItem element. Both ride
	// the same text-limit pipeline as tree rendering.
	documentText := ""
	for _, controlType := range []string{"ControlType.Document", "ControlType.Edit"} {
		for _, record := range records {
			if record.ControlType == controlType && strings.TrimSpace(record.Value) != "" {
				documentText = record.Value
				break
			}
		}
		if documentText != "" {
			break
		}
	}
	var selectedElements []string
	for _, record := range records {
		if record.Selected {
			selectedElements = append(selectedElements, uiaElementLine(record))
		}
	}

	return &appSnapshot{
		App: appDescriptor{
			Name:             appName,
			BundleIdentifier: appName,
			PID:              int(pid),
		},
		WindowHandle:        hwnd,
		WindowTitle:         limitTextPS(element.name(), textLimit),
		WindowBounds:        bounds,
		ScreenshotPNGBase64: png,
		TreeLines:           lines,
		FocusedSummary:      uiaFocusedSummary(pid, textLimit),
		SelectedText:        uiaSelectedText(pid, textLimit),
		DocumentText:        documentText,
		SelectedElements:    selectedElements,
		Elements:            records,
	}
}

// supportedPatternIds reads UIA_SupportedPatternIds (30025) — the property
// the managed client's GetSupportedPatterns is backed by. This can disagree
// with GetCurrentPattern (which fabricates patterns the provider does not
// declare), so pattern detection uses this list. The SAFEARRAY is owned by
// the VARIANT; it is read without destroying and freed once by VariantClear.
func (e uiaElement) supportedPatternIds() ([]int32, error) {
	variant := oleVariant{}
	if hr, _, _ := vtableCall(e.ptr, elemSlotGetCurrentPropertyValue,
		uintptr(int32(30025)), uintptr(unsafe.Pointer(&variant))); int32(hr) < 0 {
		return nil, fmt.Errorf("uia: get supported patterns hr=0x%08x", int32(hr))
	}
	defer procVariantClear.Call(uintptr(unsafe.Pointer(&variant)))
	if variant.vt != 0x2003 /* VT_I4|VT_ARRAY */ {
		return nil, fmt.Errorf("uia: supported patterns vt=0x%x", variant.vt)
	}
	sa := *(*uintptr)(unsafe.Pointer(&variant.value[0]))
	count := safeArrayCount(sa)
	if count == 0 {
		return []int32{}, nil
	}
	data := *(*uintptr)(uiaPtr(sa + 16)) // pvData
	values := unsafe.Slice((*int32)(uiaPtr(data)), count)
	ids := make([]int32, 0, count)
	for _, v := range values {
		ids = append(ids, v)
	}
	return ids, nil
}
