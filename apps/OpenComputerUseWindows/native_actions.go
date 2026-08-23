//go:build windows

package main

// native_actions.go ports the action-tool half of the retired PS-era runtime's
// Invoke-Operation dispatch (click / perform_secondary_action / scroll /
// drag / type_text / press_key / set_value) onto the in-process UIA COM
// client. Every error string, timing constant, and fallback order mirrors
// the PS-era runtime byte-for-byte; the psRequest/psResponse contract is
// unchanged.
//
// UIA pattern vtable slots (verified against mingw-w64 uiautomationclient.h;
// in these interfaces the action methods are declared BEFORE the Current
// getters):
//
//	IUIAutomationInvokePattern            3:Invoke
//	IUIAutomationTogglePattern            3:Toggle 4:get_CurrentToggleState
//	IUIAutomationExpandCollapsePattern    3:Expand 4:Collapse 5:get_CurrentExpandCollapseState
//	IUIAutomationValuePattern             3:SetValue 4:get_CurrentValue 5:get_CurrentIsReadOnly
//	IUIAutomationSelectionItemPattern     3:Select 6:get_CurrentIsSelected
//	IUIAutomationScrollPattern            3:Scroll
//	IUIAutomationScrollItemPattern        3:ScrollIntoView

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	invokeSlotInvoke       = 3
	toggleSlotToggle       = 3
	toggleSlotGetState     = 4
	expandSlotExpand       = 3
	expandSlotCollapse     = 4
	valueSlotSetValue      = 3
	valueSlotIsReadOnly    = 5
	selectSlotSelect       = 3
	selectSlotIsSelected   = 6
	scrollSlotScroll       = 3
	scrollItemSlotIntoView = 3

	foregroundInputFlagName = "OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT"
	focusActionsFlagName    = "OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOCUS_ACTIONS"
	uiaTextFallbackFlagName = "OPEN_COMPUTER_USE_WINDOWS_ALLOW_UIA_TEXT_FALLBACK"
)

// --- element state fingerprints (auto-click verification) -------------------

// uiaElementStateFingerprint mirrors Get-ElementStateFingerprint: observable
// toggle/expand/selection state joined with ';' ("" when nothing applies).
func uiaElementStateFingerprint(e uiaElement) string {
	if !e.valid() {
		return ""
	}
	var parts []string
	if pattern := e.currentPattern(uiaPatternToggle); pattern != nil {
		var state int32
		if hr, _, _ := vtableCall(pattern, toggleSlotGetState, uintptr(unsafe.Pointer(&state))); int32(hr) >= 0 {
			parts = append(parts, fmt.Sprintf("T=%d", state))
		}
		oleRelease(pattern)
	}
	if pattern := e.currentPattern(uiaPatternExpand); pattern != nil {
		var state int32
		if hr, _, _ := vtableCall(pattern, expandCollapseSlotState, uintptr(unsafe.Pointer(&state))); int32(hr) >= 0 {
			parts = append(parts, fmt.Sprintf("E=%d", state))
		}
		oleRelease(pattern)
	}
	if pattern := e.currentPattern(uiaPatternSelect); pattern != nil {
		var selected int32
		if hr, _, _ := vtableCall(pattern, selectSlotIsSelected, uintptr(unsafe.Pointer(&selected))); int32(hr) >= 0 {
			parts = append(parts, fmt.Sprintf("S=%t", selected != 0))
		}
		oleRelease(pattern)
	}
	return strings.Join(parts, ";")
}

// uiaFocusFingerprint mirrors Get-FocusFingerprint: the focused element's
// runtime id joined with '-'.
func uiaFocusFingerprint() string {
	focused, err := uiaGetFocusedElement()
	if err != nil {
		return ""
	}
	defer focused.release()
	return strings.Join(intsToStrings(focused.runtimeId()), "-")
}

func uiaClickEvidence(e uiaElement) string {
	return uiaElementStateFingerprint(e) + "|" + uiaFocusFingerprint()
}

// --- element lookup (Find-Element / Get-AllElements) ------------------------

// collectAllElements mirrors Get-AllElements: the root plus every descendant,
// in the same walker order the tree renderer indexes (RawViewWalker children
// with IsContentElement=0 nodes skipped). The caller releases every element.
func collectAllElements(root uiaElement, walker unsafe.Pointer) []uiaElement {
	items := []uiaElement{root}
	var visit func(node uiaElement)
	visit = func(node uiaElement) {
		for _, child := range walkerChildren(walker, node) {
			items = append(items, child)
			visit(child)
		}
	}
	visit(root)
	return items
}

func sameRuntimeId(left, right []int) bool {
	if left == nil || right == nil || len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// uiaFindElement mirrors Find-Element: runtime-id match first, then
// automation-id-or-name plus control type. Consumes and releases allElements.
func uiaFindElement(record *elementRecord, root uiaElement, walker unsafe.Pointer) uiaElement {
	if record == nil {
		return uiaElement{}
	}
	all := collectAllElements(root, walker)
	defer func() {
		for _, element := range all {
			element.release()
		}
	}()
	for _, element := range all {
		if sameRuntimeId(element.runtimeId(), record.RuntimeID) {
			keep := element
			// Prevent the deferred release from dropping the winner by
			// clearing its slot (COM ownership moves to the caller).
			for i := range all {
				if all[i] == keep {
					all[i] = uiaElement{}
				}
			}
			return keep
		}
	}
	for _, element := range all {
		sameAutomationId := strings.TrimSpace(record.AutomationID) != "" && element.automationId() == record.AutomationID
		sameName := strings.TrimSpace(record.Name) != "" && element.name() == record.Name
		sameType := element.controlTypeName() == record.ControlType
		if (sameAutomationId || sameName) && sameType {
			keep := element
			for i := range all {
				if all[i] == keep {
					all[i] = uiaElement{}
				}
			}
			return keep
		}
	}
	return uiaElement{}
}

// --- pattern actions (Invoke-PreferredClick / Invoke-SecondaryAction) -------

func patternValueIsReadOnly(e uiaElement) (readOnly bool, hasPattern bool) {
	pattern := e.currentPattern(uiaPatternValue)
	if pattern == nil {
		return false, false
	}
	defer oleRelease(pattern)
	var flag int32
	if hr, _, _ := vtableCall(pattern, valueSlotIsReadOnly, uintptr(unsafe.Pointer(&flag))); int32(hr) < 0 {
		return false, true
	}
	return flag != 0, true
}

func patternCurrentValue(e uiaElement) (string, bool) {
	pattern := e.currentPattern(uiaPatternValue)
	if pattern == nil {
		return "", false
	}
	defer oleRelease(pattern)
	var p uintptr
	if hr, _, _ := vtableCall(pattern, valueSlotGetCurrentValue, uintptr(unsafe.Pointer(&p))); int32(hr) < 0 {
		return "", true
	}
	return bstrToString(p), true
}

// uiaPreferredClick mirrors Invoke-PreferredClick: Invoke, then
// SelectionItem.Select, then Toggle.
func uiaPreferredClick(e uiaElement) bool {
	if pattern := e.currentPattern(uiaPatternInvoke); pattern != nil {
		vtableCall(pattern, invokeSlotInvoke)
		oleRelease(pattern)
		return true
	}
	if pattern := e.currentPattern(uiaPatternSelect); pattern != nil {
		vtableCall(pattern, selectSlotSelect)
		oleRelease(pattern)
		return true
	}
	if pattern := e.currentPattern(uiaPatternToggle); pattern != nil {
		vtableCall(pattern, toggleSlotToggle)
		oleRelease(pattern)
		return true
	}
	return false
}

// uiaSecondaryAction mirrors Invoke-SecondaryAction; returns the PS error
// text when the action is unsupported for the element.
func uiaSecondaryAction(req psRequest, e uiaElement) error {
	action := strings.ToLower(req.Action)
	switch action {
	case "invoke":
		if pattern := e.currentPattern(uiaPatternInvoke); pattern != nil {
			vtableCall(pattern, invokeSlotInvoke)
			oleRelease(pattern)
			return nil
		}
	case "toggle":
		if pattern := e.currentPattern(uiaPatternToggle); pattern != nil {
			vtableCall(pattern, toggleSlotToggle)
			oleRelease(pattern)
			return nil
		}
	case "select":
		if pattern := e.currentPattern(uiaPatternSelect); pattern != nil {
			vtableCall(pattern, selectSlotSelect)
			oleRelease(pattern)
			return nil
		}
	case "expand":
		if pattern := e.currentPattern(uiaPatternExpand); pattern != nil {
			vtableCall(pattern, expandSlotExpand)
			oleRelease(pattern)
			return nil
		}
	case "collapse":
		if pattern := e.currentPattern(uiaPatternExpand); pattern != nil {
			vtableCall(pattern, expandSlotCollapse)
			oleRelease(pattern)
			return nil
		}
	case "scrollintoview":
		if pattern := e.currentPattern(uiaPatternScrollItem); pattern != nil {
			vtableCall(pattern, scrollItemSlotIntoView)
			oleRelease(pattern)
			return nil
		}
	case "setfocus":
		if !requestEnvFlagEnabled(req, focusActionsFlagName) {
			return errors.New("SetFocus is disabled by default to avoid stealing user focus; set " + focusActionsFlagName + "=1 to enable it.")
		}
		vtableCall(e.ptr, elemSlotSetFocus)
		return nil
	}
	return fmt.Errorf("%s is not a valid secondary action for %d", action, req.Element.Index)
}

// uiaScrollPattern mirrors Invoke-Scroll: ScrollPattern with Large
// increments, repeated ceil(pages) times with 40ms gaps. false = no pattern.
func uiaScrollPattern(e uiaElement, direction string, pages float64) bool {
	pattern := e.currentPattern(uiaPatternScroll)
	if pattern == nil {
		return false
	}
	defer oleRelease(pattern)
	horizontal, vertical := int32(0), int32(0) // ScrollAmount_NoAmount
	switch direction {
	case "up":
		vertical = 1 // LargeDecrement
	case "down":
		vertical = 2 // LargeIncrement
	case "left":
		horizontal = 1
	case "right":
		horizontal = 2
	}
	repeat := int(math.Ceil(pages))
	if repeat < 1 {
		repeat = 1
	}
	for i := 0; i < repeat; i++ {
		vtableCall(pattern, scrollSlotScroll, uintptr(horizontal), uintptr(vertical))
		sleepMs(40)
	}
	return true
}

// --- coordinates -------------------------------------------------------------

// screenPoint mirrors Get-ScreenPoint: the element-frame center converted to
// screen coordinates (false when either input is missing).
func screenPoint(localFrame, windowBounds *frame) (int, int, bool) {
	if localFrame == nil || windowBounds == nil {
		return 0, 0, false
	}
	return int(mathRound(windowBounds.X + localFrame.X + localFrame.Width/2)),
		int(mathRound(windowBounds.Y + localFrame.Y + localFrame.Height/2)),
		true
}

// clickPointFromRequest resolves the click/scroll target point: the element
// frame center when supplied, else window-relative x/y.
func clickPointFromRequest(req psRequest, windowBounds *frame) (int, int) {
	if req.Element != nil && req.Element.Frame != nil && windowBounds != nil {
		x, y, _ := screenPoint(req.Element.Frame, windowBounds)
		return x, y
	}
	x, y := 0.0, 0.0
	if req.X != nil {
		x = *req.X
	}
	if req.Y != nil {
		y = *req.Y
	}
	baseX, baseY := 0.0, 0.0
	if windowBounds != nil {
		baseX, baseY = windowBounds.X, windowBounds.Y
	}
	return int(mathRound(baseX + x)), int(mathRound(baseY + y))
}

// windowCoord mirrors [math]::Round($windowBounds.<axis> + [double]$value):
// PS coerces a missing bound/value to 0.
func windowCoord(bounds *frame, axis string, value *float64) int {
	b := 0.0
	if bounds != nil {
		if axis == "x" {
			b = bounds.X
		} else {
			b = bounds.Y
		}
	}
	v := 0.0
	if value != nil {
		v = *value
	}
	return int(mathRound(b + v))
}

// --- foreground gating -------------------------------------------------------

// nativeAssertForegroundInputAllowed mirrors Assert-ForegroundInputAllowed.
func nativeAssertForegroundInputAllowed(req psRequest, action string) error {
	if req.AllowForegroundInput || requestEnvFlagEnabled(req, foregroundInputFlagName) {
		return nil
	}
	return fmt.Errorf("%s requires %s=1 because it moves the real mouse pointer and changes foreground focus. Set %s=1 to enable it.",
		action, foregroundInputFlagName, foregroundInputFlagName)
}

// --- type_text entry chain ---------------------------------------------------

// textWindowHandleCandidate mirrors Test-TextWindowHandleCandidate.
func textWindowHandleCandidate(e uiaElement, mainHwnd int64) bool {
	if !e.valid() {
		return false
	}
	handle := e.nativeWindowHandle()
	if handle == 0 || handle == mainHwnd {
		return false
	}
	controlType := e.controlTypeName()
	className := e.className()
	return strings.Contains(controlType, "Edit") ||
		strings.Contains(controlType, "Document") ||
		strings.Contains(className, "Edit") ||
		strings.Contains(className, "Rich") ||
		strings.Contains(className, "Text")
}

// uiaFindTextEntryElement mirrors Find-TextEntryElement.
func uiaFindTextEntryElement(process windowedProcess, root uiaElement, walker unsafe.Pointer) uiaElement {
	if focused, err := uiaGetFocusedElement(); err == nil {
		if focused.processId() == int32(process.pid) {
			if _, has := patternValueIsReadOnly(focused); has {
				readOnly, _ := patternValueIsReadOnly(focused)
				if !readOnly {
					return focused
				}
			}
		}
		focused.release()
	}
	all := collectAllElements(root, walker)
	defer func() {
		for _, element := range all {
			element.release()
		}
	}()
	for _, element := range all {
		readOnly, has := patternValueIsReadOnly(element)
		if !has || readOnly {
			continue
		}
		controlType := element.controlTypeName()
		if strings.Contains(controlType, "Edit") || strings.Contains(controlType, "Document") {
			keep := element
			clearElementSlot(all, keep)
			return keep
		}
	}
	for _, element := range all {
		readOnly, has := patternValueIsReadOnly(element)
		if has && !readOnly {
			keep := element
			clearElementSlot(all, keep)
			return keep
		}
	}
	return uiaElement{}
}

func clearElementSlot(all []uiaElement, keep uiaElement) {
	for i := range all {
		if all[i] == keep {
			all[i] = uiaElement{}
		}
	}
}

// uiaFindTextEntryWindowHandle mirrors Find-TextEntryWindowHandle.
func uiaFindTextEntryWindowHandle(process windowedProcess, preferred uiaElement, rootHwnd int64, walker unsafe.Pointer) int64 {
	if textWindowHandleCandidate(preferred, rootHwnd) {
		return preferred.nativeWindowHandle()
	}
	if rootHwnd == 0 {
		rootHwnd = int64(process.mainHWND)
	}
	root, err := uiaElementFromHandle(rootHwnd)
	if err != nil {
		return 0
	}
	defer root.release()
	all := collectAllElements(root, walker)
	defer func() {
		for _, element := range all {
			element.release()
		}
	}()
	for _, element := range all {
		if !textWindowHandleCandidate(element, rootHwnd) {
			continue
		}
		readOnly, has := patternValueIsReadOnly(element)
		if has && !readOnly {
			return element.nativeWindowHandle()
		}
	}
	for _, element := range all {
		if textWindowHandleCandidate(element, rootHwnd) {
			return element.nativeWindowHandle()
		}
	}
	return 0
}

// uiaTypeText mirrors Invoke-TypeText. Returns (handled, error).
func uiaTypeText(req psRequest, process windowedProcess, root uiaElement, rootHwnd int64, walker unsafe.Pointer) (bool, error) {
	element := uiaFindTextEntryElement(process, root, walker)
	defer element.release()
	targetHwnd := uiaFindTextEntryWindowHandle(process, element, rootHwnd, walker)
	if targetHwnd != 0 && sendTextToEditHandle(windows.HWND(targetHwnd), req.Text) {
		return true, nil
	}
	if element.valid() {
		readOnly, has := patternValueIsReadOnly(element)
		if has && !readOnly {
			if !requestEnvFlagEnabled(req, uiaTextFallbackFlagName) {
				return false, errors.New("UIA ValuePattern text fallback is disabled by default because it may bring the target app to the foreground; set " + uiaTextFallbackFlagName + "=1 to enable it.")
			}
			current := ""
			if value, ok := patternCurrentValue(element); ok {
				current = value
			}
			if pattern := element.currentPattern(uiaPatternValue); pattern != nil {
				utf16, err := windows.UTF16PtrFromString(current + req.Text)
				if err == nil {
					vtableCall(pattern, valueSlotSetValue, uintptr(unsafe.Pointer(utf16)))
				}
				oleRelease(pattern)
			}
			return true, nil
		}
	}
	return false, nil
}

// --- dispatch ----------------------------------------------------------------

// nativeActionContext mirrors the Invoke-Operation action-tool preamble.
type nativeActionContext struct {
	process       windowedProcess
	hwnd          int64
	windowRoot    uiaElement // valid only on the windowId path
	hasWindowRoot bool
	windowBounds  *frame
}

func resolveNativeActionContext(req psRequest) (nativeActionContext, error) {
	context := nativeActionContext{}
	if req.WindowID != 0 {
		window, processName, err := resolveWindowFromHandle(req.WindowID)
		if err != nil {
			return context, err
		}
		var pid uint32
		getWindowThreadProcessID(req.WindowID, &pid)
		context.process = windowedProcess{name: processName, pid: pid, mainHWND: windows.HWND(req.WindowID)}
		context.hwnd = req.WindowID
		root, err := uiaElementFromHandle(req.WindowID)
		if err != nil {
			return context, err
		}
		context.windowRoot = root
		context.hasWindowRoot = true
		_ = window
	} else {
		process, err := nativeResolveApp(req.App)
		if err != nil {
			return context, err
		}
		context.process = process
		context.hwnd = int64(process.mainHWND)
	}
	context.windowBounds = req.WindowBounds
	if context.windowBounds == nil && req.Element != nil && req.Element.Frame != nil {
		context.windowBounds = windowRectFrame(windows.HWND(context.hwnd))
	}
	return context, nil
}

// nativePerformAction executes one action tool on the UIA thread. It returns
// a domain error (PS error text) or a transport error.
func nativePerformAction(req psRequest) error {
	var actionErr error
	if err := uiaOnThread(func() {
		context, err := resolveNativeActionContext(req)
		if err != nil {
			actionErr = err
			return
		}
		if context.hasWindowRoot {
			defer context.windowRoot.release()
		}
		walker, err := uiaRawViewWalker()
		if err != nil {
			actionErr = err
			return
		}
		defer oleRelease(walker)

		var element uiaElement
		if req.Element != nil {
			root := context.windowRoot
			if !context.hasWindowRoot {
				mainElement, err := uiaMainElement(uintptr(context.process.mainHWND), context.process.pid, context.process.name)
				if err != nil {
					actionErr = err
					return
				}
				root = mainElement
				defer root.release()
			}
			element = uiaFindElement(req.Element, root, walker)
			defer element.release()
		}

		actionErr = dispatchNativeAction(req, context, element, walker)
	}); err != nil {
		return err
	}
	return actionErr
}

func dispatchNativeAction(req psRequest, context nativeActionContext, element uiaElement, walker unsafe.Pointer) error {
	hwnd := windows.HWND(context.hwnd)
	bounds := context.windowBounds

	switch req.Tool {
	case "click":
		return dispatchNativeClick(req, context, element, hwnd, bounds)
	case "perform_secondary_action":
		if !element.valid() {
			return fmt.Errorf("unknown element_index '%d'", req.Element.Index)
		}
		return uiaSecondaryAction(req, element)
	case "scroll":
		return dispatchNativeScroll(req, context, element, hwnd, bounds, walker)
	case "drag":
		fromX := windowCoord(bounds, "x", req.FromX)
		fromY := windowCoord(bounds, "y", req.FromY)
		toX := windowCoord(bounds, "x", req.ToX)
		toY := windowCoord(bounds, "y", req.ToY)
		if req.InputMethod == "global" {
			if err := nativeAssertForegroundInputAllowed(req, "input_method 'global'"); err != nil {
				return err
			}
			enableForegroundWindow(hwnd)
			realMouseDrag(fromX, fromY, toX, toY)
			return nil
		}
		postDrag(hwnd, fromX, fromY, toX, toY)
		return nil
	case "type_text":
		if req.InputMethod == "global" {
			if err := nativeAssertForegroundInputAllowed(req, "input_method 'global'"); err != nil {
				return err
			}
			enableForegroundWindow(hwnd)
			realTypeText(req.Text)
			return nil
		}
		root := context.windowRoot
		if !context.hasWindowRoot {
			mainElement, err := uiaMainElement(uintptr(context.process.mainHWND), context.process.pid, context.process.name)
			if err != nil {
				return err
			}
			defer mainElement.release()
			root = mainElement
		}
		handled, err := uiaTypeText(req, context.process, root, context.hwnd, walker)
		if err != nil {
			return err
		}
		if !handled {
			postTextChars(hwnd, req.Text)
		}
		return nil
	case "press_key":
		if req.InputMethod == "global" {
			if err := nativeAssertForegroundInputAllowed(req, "input_method 'global'"); err != nil {
				return err
			}
			enableForegroundWindow(hwnd)
			modifiers, vk, err := realKeyForName(req.Key)
			if err != nil {
				return err
			}
			realKeyChord(modifiers, vk)
			return nil
		}
		return postKeyChord(hwnd, req.Key)
	case "set_value":
		if !element.valid() {
			return fmt.Errorf("unknown element_index '%d'", req.Element.Index)
		}
		pattern := element.currentPattern(uiaPatternValue)
		if pattern == nil {
			return errors.New("Cannot set a value for an element that is not settable")
		}
		defer oleRelease(pattern)
		utf16, err := windows.UTF16PtrFromString(req.Value)
		if err != nil {
			return err
		}
		vtableCall(pattern, valueSlotSetValue, uintptr(unsafe.Pointer(utf16)))
		return nil
	}
	return fmt.Errorf("unsupportedTool(%q)", req.Tool)
}

func dispatchNativeClick(req psRequest, context nativeActionContext, element uiaElement, hwnd windows.HWND, bounds *frame) error {
	clickMethod := req.ClickMethod
	if strings.TrimSpace(clickMethod) == "" {
		clickMethod = "auto"
	}
	switch clickMethod {
	case "accessibility":
		if !element.valid() {
			return errors.New("click_method 'accessibility' requires element_index")
		}
		if req.MouseButton == "right" || req.MouseButton == "middle" {
			return fmt.Errorf("click_method 'accessibility' does not support mouse_button '%s'", req.MouseButton)
		}
		if !uiaPreferredClick(element) {
			return errors.New("click_method 'accessibility' could not click the requested element")
		}
		return nil
	case "app_post":
		x, y := clickPointFromRequest(req, bounds)
		postMouseClick(hwnd, x, y, req.MouseButton, req.ClickCount)
		return nil
	case "global":
		if err := nativeAssertForegroundInputAllowed(req, "click_method 'global'"); err != nil {
			return err
		}
		x, y := clickPointFromRequest(req, bounds)
		sendRealClick(hwnd, x, y, req.MouseButton, req.ClickCount)
		return nil
	case "sky_click":
		return errors.New("click_method 'sky_click' is not supported on Windows")
	case "auto":
		evidence := uiaClickEvidence(element)
		handled := false
		if element.valid() && req.MouseButton != "right" && req.MouseButton != "middle" {
			handled = uiaPreferredClick(element)
		}
		pointResolved := false
		x, y := 0, 0
		if !handled {
			x, y = clickPointFromRequest(req, bounds)
			pointResolved = true
			postMouseClick(hwnd, x, y, req.MouseButton, req.ClickCount)
		}
		// Verification tail: if nothing observable changed within the
		// comparison window, the UIA/PostMessage chain most likely did not
		// land. Replay physically only under the foreground-input opt-in.
		sleepMs(250)
		if uiaClickEvidence(element) == evidence {
			if req.AllowForegroundInput || requestEnvFlagEnabled(req, foregroundInputFlagName) {
				if !pointResolved {
					x, y = clickPointFromRequest(req, bounds)
				}
				sendRealClick(hwnd, x, y, req.MouseButton, req.ClickCount)
			}
		}
		return nil
	}
	return fmt.Errorf("Invalid click_method '%s'", clickMethod)
}

// sendRealClick mirrors Send-RealClick: activate, move the pointer, click.
func sendRealClick(hwnd windows.HWND, x, y int, button string, count int) {
	enableForegroundWindow(hwnd)
	realMouseMove(x, y)
	realMouseClick(mouseButtonForInput(button), count)
}

func mouseButtonForInput(button string) string {
	if strings.TrimSpace(button) == "" {
		return "left"
	}
	return strings.ToLower(strings.TrimSpace(button))
}

func dispatchNativeScroll(req psRequest, context nativeActionContext, element uiaElement, hwnd windows.HWND, bounds *frame, walker unsafe.Pointer) error {
	if req.InputMethod == "global" {
		if err := nativeAssertForegroundInputAllowed(req, "input_method 'global'"); err != nil {
			return err
		}
		if req.ScrollX != nil || req.ScrollY != nil {
			scrollX, scrollY := 0.0, 0.0
			if req.ScrollX != nil {
				scrollX = *req.ScrollX
			}
			if req.ScrollY != nil {
				scrollY = *req.ScrollY
			}
			x := windowCoord(bounds, "x", req.X)
			y := windowCoord(bounds, "y", req.Y)
			sendRealScrollDelta(hwnd, x, y, scrollX, scrollY)
			return nil
		}
		x, y := clickPointFromRequest(req, bounds)
		sendRealScroll(hwnd, x, y, req.Direction, req.Pages)
		return nil
	}
	if req.ScrollX != nil || req.ScrollY != nil {
		// Official window2 coordinate scroll: window-relative x/y plus pixel
		// deltas (~40px per notch).
		scrollX, scrollY := 0.0, 0.0
		if req.ScrollX != nil {
			scrollX = *req.ScrollX
		}
		if req.ScrollY != nil {
			scrollY = *req.ScrollY
		}
		x := windowCoord(bounds, "x", req.X)
		y := windowCoord(bounds, "y", req.Y)
		postScrollDelta(hwnd, x, y, scrollX, scrollY)
		return nil
	}
	handled := false
	if element.valid() {
		handled = uiaScrollPattern(element, req.Direction, req.Pages)
	}
	if !handled {
		x, y := clickPointFromRequest(req, bounds)
		postScrollByPages(hwnd, x, y, req.Direction, req.Pages)
	}
	return nil
}

// sendRealScrollDelta mirrors Send-RealScrollDelta.
func sendRealScrollDelta(hwnd windows.HWND, screenX, screenY int, scrollX, scrollY float64) {
	enableForegroundWindow(hwnd)
	realMouseMove(screenX, screenY)
	dy, dx := 0, 0
	if scrollY != 0 {
		dy = int(-1 * mathRound(scrollY*120/40))
	}
	if scrollX != 0 {
		dx = int(mathRound(scrollX * 120 / 40))
	}
	realWheel(dy, dx)
}

// sendRealScroll mirrors Send-RealScroll.
func sendRealScroll(hwnd windows.HWND, screenX, screenY int, direction string, pages float64) {
	enableForegroundWindow(hwnd)
	realMouseMove(screenX, screenY)
	delta := int(mathRound(120 * pages))
	horizontal := direction == "left" || direction == "right"
	if direction == "down" || direction == "right" {
		delta = -delta
	}
	if horizontal {
		realWheel(0, delta)
	} else {
		realWheel(delta, 0)
	}
}

// nativeActionTool routes one action request: perform the action on the UIA
// thread, wait 120ms, then build the post-action snapshot with default
// budgets (the PS dispatch tail).
func nativeActionTool(req psRequest) (*psResponse, error) {
	if err := uiaOnThread(func() {}); err != nil {
		return nil, err
	}
	if err := nativePerformAction(req); err != nil {
		return &psResponse{Error: err.Error()}, nil
	}
	sleepMs(120)
	snapshotReq := psRequest{EnvFlags: req.EnvFlags}
	var snapshot *appSnapshot
	var err error
	if req.WindowID != 0 {
		snapshotReq.WindowID = req.WindowID
		snapshot, err = nativeSnapshotForWindowId(snapshotReq, req.WindowID)
	} else {
		snapshotReq.App = req.App
		snapshot, err = nativeSnapshotForApp(snapshotReq)
	}
	if err != nil {
		return &psResponse{Error: err.Error()}, nil
	}
	return &psResponse{OK: true, Snapshot: snapshot}, nil
}

// nativeSnapshotForApp mirrors Build-Snapshot for the post-action tail.
func nativeSnapshotForApp(req psRequest) (*appSnapshot, error) {
	process, err := nativeResolveApp(req.App)
	if err != nil {
		return nil, err
	}
	bounds, png := captureFirstPng(req, int64(process.mainHWND))
	var snapshot *appSnapshot
	var jobErr error
	if err := uiaOnThread(func() {
		mainElement, err := uiaMainElement(uintptr(process.mainHWND), process.pid, process.name)
		if err != nil {
			jobErr = err
			return
		}
		defer mainElement.release()
		snapshot = uiaBuildSnapshotForWindow(process.name, int32(process.pid), int64(process.mainHWND),
			mainElement, bounds, png, resolveTextLimitPS(req.TextLimit), req.MaxTreeNodes, req.MaxTreeDepth)
	}); err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, jobErr
	}
	return snapshot, nil
}
