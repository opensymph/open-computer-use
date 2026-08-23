//go:build windows

package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// --- Win32 constants (mirroring the values declared in the retired PS-era runtime) ---------

const (
	swRestore = 9

	wmSettext      = 0x000C
	wmMousemove    = 0x0200
	wmLButtonDown  = 0x0201
	wmLButtonUp    = 0x0202
	wmRButtonDown  = 0x0204
	wmRButtonUp    = 0x0205
	wmMButtonDown  = 0x0207
	wmMButtonUp    = 0x0208
	wmMouseWheel   = 0x020A
	wmMouseHWheel  = 0x020E
	wmKeydown      = 0x0100
	wmKeyup        = 0x0101
	wmChar         = 0x0102
	emSetsel       = 0x00B1
	emReplaceSel   = 0x00C2
	wheelDeltaUnit = 120
	pixelsPerNotch = 40 // ~3-line system scroll step
)

// --- Low-level user32/gdi32 procedures not exposed by x/sys/windows --------

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	gdi32                   = windows.NewLazySystemDLL("gdi32.dll")
	procSendInput           = user32.NewProc("SendInput")
	procMapVirtualKey       = user32.NewProc("MapVirtualKeyW")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procGetWindowTextLength = user32.NewProc("GetWindowTextLengthW")

	procCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBmp = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject        = gdi32.NewProc("SelectObject")
	procDeleteDC            = gdi32.NewProc("DeleteDC")
	procDeleteObject        = gdi32.NewProc("DeleteObject")
	procBitBlt              = gdi32.NewProc("BitBlt")
	procGetDIBits           = gdi32.NewProc("GetDIBits")
	procPrintWindow         = user32.NewProc("PrintWindow")
	procPostMessage         = user32.NewProc("PostMessageW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procAttachThreadInput   = user32.NewProc("AttachThreadInput")
	procAllowSetForeground  = user32.NewProc("AllowSetForegroundWindow")
	procSendMessage         = user32.NewProc("SendMessageW")
	procGetWindow           = user32.NewProc("GetWindow")
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseeventfMove       = 0x0001
	mouseeventfLeftDown   = 0x0002
	mouseeventfLeftUp     = 0x0004
	mouseeventfRightDown  = 0x0008
	mouseeventfRightUp    = 0x0010
	mouseeventfMiddleDown = 0x0020
	mouseeventfMiddleUp   = 0x0040
	mouseeventfWheel      = 0x0800
	mouseeventfHWheel     = 0x1000
	mouseeventfVirtualDesk = 0x4000
	mouseeventfAbsolute    = 0x8000
	keyeventfKeyUp        = 0x0002
	keyeventfUnicode      = 0x0004
	keyeventfScancode     = 0x0008
	mapvkVKToVSC          = 0
	srcCopy               = 0x00CC0022
	pwRenderFullContent   = 2
	biRGB                 = 0
	dibRGBColors          = 0

	// GetSystemMetrics virtual-desktop indices (winuser.h).
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
)

type tagINPUT struct {
	inputType uint32
	// union: mouseInput (larger member) overlayed with keybdInput.
	mi mouseInput
}

type mouseInput struct {
	dx, dy      int32
	mouseData   uint32
	dwFlags     uint32
	time        uint32
	padding     uint32
	dwExtraInfo uintptr
}

type keybdInput struct {
	wVk, wScan  uint16
	dwFlags     uint32
	time        uint32
	padding     uint32
	dwExtraInfo uintptr
}

// sendInputs injects one batch and verifies every event landed: SendInput
// returns the count of events actually inserted (0 when blocked by UIPI or
// another thread), so a short count must surface as an error instead of a
// silently dropped keystroke.
func sendInputs(inputs []tagINPUT) error {
	if len(inputs) == 0 {
		return nil
	}
	injected, _, _ := procSendInput.Call(uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), unsafe.Sizeof(tagINPUT{}))
	if injected != uintptr(len(inputs)) {
		return fmt.Errorf("SendInput injected %d of %d events", injected, len(inputs))
	}
	return nil
}

func mouseEvent(flags uint32, dx, dy int32, data uint32) tagINPUT {
	return tagINPUT{inputType: inputMouse, mi: mouseInput{dx: dx, dy: dy, mouseData: data, dwFlags: flags}}
}

// keyEvent packs a KEYBDINPUT through the MOUSEINPUT-shaped union member.
// In the x64 INPUT union ki.wVk/ki.wScan overlay mi.dx (offsets 0/2) and
// ki.dwFlags overlays mi.dy (offset 4); anything placed in mi.dwFlags lands
// in KEYBDINPUT padding and is lost.
func keyEvent(vk, scan uint16, flags uint32) tagINPUT {
	return tagINPUT{inputType: inputKeyboard, mi: mouseInput{
		dx: int32(vk) | int32(scan)<<16,
		dy: int32(flags),
	}}
}

// mapVirtualKey wraps MapVirtualKeyW(vk, MAPVK_VK_TO_VSC).
func mapVirtualKey(vk uint16) uint16 {
	ret, _, _ := procMapVirtualKey.Call(uintptr(vk), mapvkVKToVSC)
	return uint16(ret)
}

func systemMetrics(index int) int {
	ret, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int(ret)
}

// normalizeVirtualScreen maps a screen coordinate onto the 0..65535 range of
// the entire virtual desktop, as MOUSEEVENTF_ABSOLUTE|MOUSEEVENTF_VIRTUALDESK
// expects (OCUInput.Normalize widened to multi-monitor: the virtual-screen
// origin can be negative for monitors placed left of/above the primary).
func normalizeVirtualScreen(value, origin, extent int) int32 {
	if extent <= 0 {
		return 0
	}
	return int32((float64(value-origin) * 65535.0) / float64(extent))
}

// virtualScreenNormalizedPoint normalizes a screen point against the virtual
// desktop (origin + extent from GetSystemMetrics).
func virtualScreenNormalizedPoint(x, y int) (int32, int32) {
	return normalizeVirtualScreen(x, systemMetrics(smXVirtualScreen), systemMetrics(smCXVirtualScreen)),
		normalizeVirtualScreen(y, systemMetrics(smYVirtualScreen), systemMetrics(smCYVirtualScreen))
}

// --- Foreground (SendInput) input layer, byte-for-byte with OCUInput -------

// realMouseMove moves the real pointer to absolute screen coordinates.
func realMouseMove(x, y int) error {
	nx, ny := virtualScreenNormalizedPoint(x, y)
	if err := sendInputs([]tagINPUT{mouseEvent(mouseeventfMove|mouseeventfAbsolute|mouseeventfVirtualDesk, nx, ny, 0)}); err != nil {
		return err
	}
	sleepMs(20)
	return nil
}

// realMouseClick presses the physical mouse button count times.
func realMouseClick(button string, count int) error {
	var down, up uint32 = mouseeventfLeftDown, mouseeventfLeftUp
	switch button {
	case "right":
		down, up = mouseeventfRightDown, mouseeventfRightUp
	case "middle":
		down, up = mouseeventfMiddleDown, mouseeventfMiddleUp
	}
	repeat := count
	if repeat < 1 {
		repeat = 1
	}
	for i := 0; i < repeat; i++ {
		if err := sendInputs([]tagINPUT{mouseEvent(down, 0, 0, 0)}); err != nil {
			return err
		}
		sleepMs(35)
		if err := sendInputs([]tagINPUT{mouseEvent(up, 0, 0, 0)}); err != nil {
			return err
		}
		sleepMs(60)
	}
	return nil
}

// realMouseDrag drags the physical pointer across 12 interpolated steps.
func realMouseDrag(fromX, fromY, toX, toY int) error {
	if err := realMouseMove(fromX, fromY); err != nil {
		return err
	}
	if err := sendInputs([]tagINPUT{mouseEvent(mouseeventfLeftDown, 0, 0, 0)}); err != nil {
		return err
	}
	sleepMs(30)
	const steps = 12
	for i := 1; i <= steps; i++ {
		x := fromX + int(mathRound(float64(toX-fromX)*(float64(i)/float64(steps))))
		y := fromY + int(mathRound(float64(toY-fromY)*(float64(i)/float64(steps))))
		nx, ny := virtualScreenNormalizedPoint(x, y)
		if err := sendInputs([]tagINPUT{mouseEvent(mouseeventfMove|mouseeventfAbsolute|mouseeventfVirtualDesk, nx, ny, 0)}); err != nil {
			return err
		}
		sleepMs(20)
	}
	return sendInputs([]tagINPUT{mouseEvent(mouseeventfLeftUp, 0, 0, 0)})
}

// realWheel scrolls the physical wheel: dy positive scrolls up (WHEEL_DELTA
// units), dx positive scrolls right.
func realWheel(dy, dx int) error {
	if dy != 0 {
		if err := sendInputs([]tagINPUT{mouseEvent(mouseeventfWheel, 0, 0, uint32(int32(dy)))}); err != nil {
			return err
		}
	}
	if dx != 0 {
		if err := sendInputs([]tagINPUT{mouseEvent(mouseeventfHWheel, 0, 0, uint32(int32(dx)))}); err != nil {
			return err
		}
	}
	return nil
}

// realKeyChord presses a chord via virtual keys + mapped scan codes: all keys
// down, then everything up in reverse order.
func realKeyChord(modifierVks []uint16, vk uint16) error {
	chord := make([]struct{ vk, scan uint16 }, 0, len(modifierVks)+1)
	downs := make([]tagINPUT, 0, len(modifierVks)+1)
	for _, modifier := range modifierVks {
		chord = append(chord, struct{ vk, scan uint16 }{modifier, mapVirtualKey(modifier)})
		downs = append(downs, keyEvent(modifier, mapVirtualKey(modifier), keyeventfScancode))
	}
	chord = append(chord, struct{ vk, scan uint16 }{vk, mapVirtualKey(vk)})
	downs = append(downs, keyEvent(vk, mapVirtualKey(vk), keyeventfScancode))
	if err := sendInputs(downs); err != nil {
		return err
	}
	sleepMs(40)
	for j := len(chord) - 1; j >= 0; j-- {
		if err := sendInputs([]tagINPUT{keyEvent(chord[j].vk, chord[j].scan, keyeventfScancode|keyeventfKeyUp)}); err != nil {
			return err
		}
	}
	return nil
}

// realTypeText types text via KEYEVENTF_UNICODE, one event pair per UTF-16
// code unit so surrogate pairs survive as two events.
func realTypeText(text string) error {
	for _, unit := range utf16Units(text) {
		if err := sendInputs([]tagINPUT{keyEvent(0, unit, keyeventfUnicode)}); err != nil {
			return err
		}
		sleepMs(5)
		if err := sendInputs([]tagINPUT{keyEvent(0, unit, keyeventfUnicode|keyeventfKeyUp)}); err != nil {
			return err
		}
		sleepMs(5)
	}
	return nil
}

// --- Background (window-message) input layer -------------------------------

func lParamFromPoint(x, y int32) uintptr {
	return uintptr((uint32(uint16(y)) << 16) | uint32(uint16(x)))
}

func wheelWParam(delta int) uintptr {
	return uintptr(uint32(uint16(delta)) << 16)
}

// postMouseClick mirrors Send-MouseClick: MOUSEMOVE + button down/up pairs
// posted to the window with client-relative coordinates.
func postMouseClick(hwnd windows.HWND, screenX, screenY int, button string, count int) {
	x, y := screenToClient(hwnd, screenX, screenY)
	param := lParamFromPoint(int32(x), int32(y))

	down, up := uintptr(wmLButtonDown), uintptr(wmLButtonUp)
	downFlag := uintptr(0x0001)
	switch button {
	case "right":
		down, up = uintptr(wmRButtonDown), uintptr(wmRButtonUp)
		downFlag = 0x0002
	case "middle":
		down, up = uintptr(wmMButtonDown), uintptr(wmMButtonUp)
		downFlag = 0x0010
	}

	repeat := count
	if repeat < 1 {
		repeat = 1
	}
	for i := 0; i < repeat; i++ {
		postMessage(hwnd, wmMousemove, 0, param)
		postMessage(hwnd, uint32(down), downFlag, param)
		sleepMs(35)
		postMessage(hwnd, uint32(up), 0, param)
		sleepMs(50)
	}
}

// postDrag mirrors Send-Drag: 12 interpolated WM_MOUSEMOVE steps.
func postDrag(hwnd windows.HWND, fromX, fromY, toX, toY int) {
	startX, startY := screenToClient(hwnd, fromX, fromY)
	endX, endY := screenToClient(hwnd, toX, toY)

	const steps = 12
	startParam := lParamFromPoint(int32(startX), int32(startY))
	postMessage(hwnd, wmMousemove, 0, startParam)
	postMessage(hwnd, wmLButtonDown, 1, startParam)
	for i := 1; i <= steps; i++ {
		x := int(mathRound(float64(startX) + float64(endX-startX)*float64(i)/float64(steps)))
		y := int(mathRound(float64(startY) + float64(endY-startY)*float64(i)/float64(steps)))
		postMessage(hwnd, wmMousemove, 1, lParamFromPoint(int32(x), int32(y)))
		sleepMs(20)
	}
	postMessage(hwnd, wmLButtonUp, 0, lParamFromPoint(int32(endX), int32(endY)))
}

// postScrollByPages mirrors Send-Scroll (notch-based page scroll).
func postScrollByPages(hwnd windows.HWND, screenX, screenY int, direction string, pages float64) {
	x, y := screenToClient(hwnd, screenX, screenY)
	param := lParamFromPoint(int32(x), int32(y))
	delta := int(mathRound(120 * pages))
	message := wmMouseWheel
	if direction == "down" || direction == "right" {
		delta = -delta
	}
	if direction == "left" || direction == "right" {
		message = wmMouseHWheel
	}
	postMessage(hwnd, uint32(message), wheelWParam(delta), param)
}

// postScrollDelta mirrors Send-ScrollDelta (~40px per notch conversion).
func postScrollDelta(hwnd windows.HWND, screenX, screenY int, scrollX, scrollY float64) {
	x, y := screenToClient(hwnd, screenX, screenY)
	param := lParamFromPoint(int32(x), int32(y))
	postMessage(hwnd, wmMousemove, 0, param)
	if scrollY != 0 {
		// WM_MOUSEWHEEL: positive delta scrolls up, so scrollY (down
		// positive) is negated.
		delta := int(-1 * mathRound(scrollY*120/40))
		postMessage(hwnd, wmMouseWheel, wheelWParam(delta), param)
	}
	if scrollX != 0 {
		// WM_MOUSEHWHEEL: positive delta scrolls right, matching positive
		// scrollX.
		delta := int(mathRound(scrollX * 120 / 40))
		postMessage(hwnd, wmMouseHWheel, wheelWParam(delta), param)
	}
}

// postTextChars mirrors Send-Text: one WM_CHAR per UTF-16 code unit.
func postTextChars(hwnd windows.HWND, text string) {
	for _, unit := range utf16Units(text) {
		postMessage(hwnd, wmChar, uintptr(unit), 0)
		sleepMs(8)
	}
}

// sendTextToEditHandle mirrors Send-TextToEditHandle: EM_SETSEL(-1,-1) +
// EM_REPLACESEL, falling back to WM_SETTEXT with the current value appended.
// sendTextToEditHandle mirrors Send-TextToEditHandle: EM_SETSEL(-1,-1) moves
// the caret to the end, then EM_REPLACESEL inserts only the new text (the
// caret placement is what makes the append correct — passing current+text
// here would duplicate the buffer).
func sendTextToEditHandle(hwnd windows.HWND, text string) bool {
	if hwnd == 0 {
		return false
	}
	_, _, _ = sendMessage(hwnd, emSetsel, ^uintptr(0), ^uintptr(0))
	utf16, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return false
	}
	sendMessage(hwnd, emReplaceSel, 1, uintptr(unsafe.Pointer(utf16)))
	return true
}

// virtualKeyForName mirrors Get-VirtualKey (xdotool-style aliases).
func virtualKeyForName(key string) (uint16, error) {
	normalized := strings.ToLower(strings.TrimSpace(key))
	table := map[string]uint16{
		"return": 0x0D, "enter": 0x0D, "tab": 0x09, "escape": 0x1B, "esc": 0x1B,
		"backspace": 0x08, "back_space": 0x08, "delete": 0x2E, "space": 0x20,
		"left": 0x25, "up": 0x26, "right": 0x27, "down": 0x28,
		"home": 0x24, "end": 0x23, "page_up": 0x21, "prior": 0x21, "page_down": 0x22, "next": 0x22,
		"period": 0xBE, "greater": 0xBE, "less": 0xBC, "comma": 0xBC,
		"slash": 0xBF, "question": 0xBF, "semicolon": 0xBA, "apostrophe": 0xDE,
		"bracketleft": 0xDB, "bracketright": 0xDD, "backslash": 0xDC, "grave": 0xC0,
		"minus": 0xBD, "equal": 0xBB,
		"numpad_enter": 0x0D, "numpad_add": 0x6B, "numpad_subtract": 0x6D,
		"numpad_multiply": 0x6A, "numpad_divide": 0x6F, "numpad_decimal": 0x6E,
	}
	if vk, ok := table[normalized]; ok {
		return vk, nil
	}
	if len(normalized) >= 2 && normalized[0] == 'f' {
		if n := decimalDigits(normalized[1:]); n >= 1 && n <= 12 {
			return uint16(0x70 + n - 1), nil
		}
	}
	if prefix, digits, ok := splitKeypadName(normalized); ok {
		_ = prefix
		if digits >= 0 && digits <= 9 {
			return uint16(0x60 + digits), nil
		}
	}
	if len(normalized) == 1 {
		code := int(strings.ToUpper(normalized)[0])
		if (code >= 0x30 && code <= 0x39) || (code >= 0x41 && code <= 0x5A) {
			return uint16(code), nil
		}
	}
	return 0, fmt.Errorf("Unsupported key: %s", key)
}

// modifierVirtualKeyForName mirrors Get-ModifierVirtualKey, rejecting the
// banned Windows/Meta aliases with the official policy error text.
func modifierVirtualKeyForName(name string) (uint16, error) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	for _, banned := range bannedModifierNames {
		if normalized == banned {
			return 0, fmt.Errorf("press_key with the Windows/Meta key (%s) is not permitted by the official Computer Use safety policy.", name)
		}
	}
	switch normalized {
	case "ctrl", "control", "control_l", "control_r", "ctrl_l", "ctrl_r":
		return 0x11, nil
	case "shift", "shift_l", "shift_r":
		return 0x10, nil
	case "alt", "alt_l", "alt_r":
		return 0x12, nil
	}
	return 0, fmt.Errorf("Unsupported modifier: %s", name)
}

// postKeyChord mirrors Send-Key: modifiers down, main key down, 25ms, key up,
// modifiers up in reverse order.
func postKeyChord(hwnd windows.HWND, key string) error {
	parts := splitChord(key)
	if len(parts) == 0 {
		return errors.New("key is required")
	}
	main := parts[len(parts)-1]
	modifiers := make([]uint16, 0, len(parts)-1)
	for i := 0; i < len(parts)-1; i++ {
		vk, err := modifierVirtualKeyForName(parts[i])
		if err != nil {
			return err
		}
		modifiers = append(modifiers, vk)
	}
	vk, err := virtualKeyForName(main)
	if err != nil {
		return err
	}
	for _, modifier := range modifiers {
		postMessage(hwnd, wmKeydown, uintptr(modifier), 0)
	}
	postMessage(hwnd, wmKeydown, uintptr(vk), 0)
	sleepMs(25)
	postMessage(hwnd, wmKeyup, uintptr(vk), 0)
	for i := len(modifiers) - 1; i >= 0; i-- {
		postMessage(hwnd, wmKeyup, uintptr(modifiers[i]), 0)
	}
	return nil
}

// realKeyForName resolves a chord into modifier VKs and the main VK for the
// SendInput layer (Send-RealKey).
func realKeyForName(key string) ([]uint16, uint16, error) {
	parts := splitChord(key)
	if len(parts) == 0 {
		return nil, 0, errors.New("key is required")
	}
	main := parts[len(parts)-1]
	modifiers := make([]uint16, 0, len(parts)-1)
	for i := 0; i < len(parts)-1; i++ {
		vk, err := modifierVirtualKeyForName(parts[i])
		if err != nil {
			return nil, 0, err
		}
		modifiers = append(modifiers, vk)
	}
	vk, err := virtualKeyForName(main)
	if err != nil {
		return nil, 0, err
	}
	return modifiers, vk, nil
}

// --- Window helpers ---------------------------------------------------------

type windowRect struct {
	Left, Top, Right, Bottom int32
}

func getWindowRect(hwnd windows.HWND) (windowRect, bool) {
	var rect windowRect
	ret, _, _ := user32.NewProc("GetWindowRect").Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
	return rect, ret != 0
}

// newFrame mirrors New-Frame: nil for degenerate (negative) sizes.
func newFrame(x, y, width, height float64) *frame {
	if width < 0 || height < 0 {
		return nil
	}
	return &frame{X: x, Y: y, Width: width, Height: height}
}

// windowRectFrame mirrors Get-WindowRectFrame.
func windowRectFrame(hwnd windows.HWND) *frame {
	rect, ok := getWindowRect(hwnd)
	if !ok {
		return nil
	}
	return newFrame(float64(rect.Left), float64(rect.Top),
		float64(rect.Right-rect.Left), float64(rect.Bottom-rect.Top))
}

func screenToClient(hwnd windows.HWND, x, y int) (int, int) {
	point := struct{ X, Y int32 }{int32(x), int32(y)}
	user32.NewProc("ScreenToClient").Call(uintptr(hwnd), uintptr(unsafe.Pointer(&point)))
	return int(point.X), int(point.Y)
}

// windowText returns the window title via GetWindowTextW (the Win32
// equivalent of the UIA Name the PS-era runtime reports for windows).
func windowText(hwnd windows.HWND) string {
	length, _, _ := procGetWindowTextLength.Call(uintptr(hwnd))
	if length == 0 {
		return ""
	}
	buffer := make([]uint16, length+1)
	user32.NewProc("GetWindowTextW").Call(uintptr(hwnd),
		uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	return windows.UTF16ToString(buffer)
}

// isMainWindowCandidate mirrors .NET's Process.MainWindowHandle filter: the
// first top-level window of the process in EnumWindows z-order that is visible
// and has no owner. Owned windows (Edge's untitled Chrome_WidgetWin helpers)
// are skipped; titles and WS_EX_TOOLWINDOW are irrelevant (the explorer shell
// process reports an untitled unowned tool window as its main window).
func isMainWindowCandidate(hwnd windows.HWND) bool {
	if !windows.IsWindowVisible(hwnd) {
		return false
	}
	owner, _, _ := procGetWindow.Call(uintptr(hwnd), 4 /* GW_OWNER */)
	return owner == 0
}

// topLevelWindow describes one enumerated top-level window.
type topLevelWindow struct {
	hwnd windows.HWND
	pid  uint32
}

// enumerateTopLevelWindows returns top-level windows in z-order.
func enumerateTopLevelWindows() []topLevelWindow {
	var results []topLevelWindow
	cb := windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		var pid uint32
		windows.GetWindowThreadProcessId(hwnd, &pid)
		results = append(results, topLevelWindow{hwnd: hwnd, pid: pid})
		return 1 // continue
	})
	user32.NewProc("EnumWindows").Call(cb, 0)
	return results
}

// processEntry pairs a pid with its process name (exe leaf, no extension —
// the ProcessName equivalent).
type processEntry struct {
	pid  uint32
	name string
}

// processNameByPID resolves ProcessName for a pid ("" when unavailable).
func processNameByPID(pid uint32) string {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(handle)
	var buffer [windows.MAX_PATH]uint16
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return ""
	}
	path := windows.UTF16ToString(buffer[:size])
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// processesSnapshot lists all processes via the toolhelp snapshot.
func processesSnapshot() []processEntry {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	var processes []processEntry
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil
	}
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		processes = append(processes, processEntry{pid: entry.ProcessID, name: strings.TrimSuffix(name, filepath.Ext(name))})
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}
	return processes
}

// runningWindowedProcess mirrors Get-Process | Where MainWindowHandle != 0:
// one entry per process owning at least one main-window-candidate window,
// with the main window resolved the way .NET does (first candidate in
// z-order).
type windowedProcess struct {
	pid       uint32
	name      string
	mainHWND  windows.HWND
	mainTitle string
}

func windowedProcesses() []windowedProcess {
	byPID := map[uint32][]topLevelWindow{}
	for _, window := range enumerateTopLevelWindows() {
		if window.pid == 0 || !windows.IsWindowVisible(window.hwnd) {
			continue
		}
		byPID[window.pid] = append(byPID[window.pid], window)
	}
	var processes []windowedProcess
	for _, entry := range processesSnapshot() {
		windows_, ok := byPID[entry.pid]
		if !ok {
			continue
		}
		var main *topLevelWindow
		for i := range windows_ {
			if isMainWindowCandidate(windows_[i].hwnd) {
				main = &windows_[i]
				break
			}
		}
		if main == nil {
			continue
		}
		name := entry.name
		if name == "" {
			name = processNameByPID(entry.pid)
		}
		if name == "" {
			continue
		}
		processes = append(processes, windowedProcess{
			pid:       entry.pid,
			name:      name,
			mainHWND:  main.hwnd,
			mainTitle: windowText(main.hwnd),
		})
	}
	// Sort-Object ProcessName, Id (case-insensitive by name).
	sort.SliceStable(processes, func(i, j int) bool {
		if strings.ToLower(processes[i].name) != strings.ToLower(processes[j].name) {
			return strings.ToLower(processes[i].name) < strings.ToLower(processes[j].name)
		}
		return processes[i].pid < processes[j].pid
	})
	return processes
}

func newWindowRef(processName string, hwnd windows.HWND, title string) *windowRef {
	return &windowRef{App: processName, ID: int64(hwnd), Title: title}
}

// --- Capture: PrintWindow + GDI (WGC arrives in phase 2) -------------------

// captureWindowPixels captures a window into 32bpp BGRA pixels and its size.
func captureWindowPixels(hwnd windows.HWND, mode string) ([]byte, int, int, error) {
	switch mode {
	case "print":
		return capturePrintWindowPixels(hwnd)
	default: // gdi
		return captureGDIRegion(windowRectFrame(hwnd))
	}
}

func capturePrintWindowPixels(hwnd windows.HWND) ([]byte, int, int, error) {
	rect, ok := getWindowRect(hwnd)
	if !ok {
		return nil, 0, 0, errors.New("PrintWindow: GetWindowRect failed")
	}
	width := int(rect.Right - rect.Left)
	height := int(rect.Bottom - rect.Top)
	if width <= 0 || height <= 0 {
		return nil, 0, 0, errors.New("PrintWindow: empty window rect")
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, 0, 0, errors.New("PrintWindow: GetDC failed")
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, 0, 0, errors.New("PrintWindow: CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)

	bitmap, _, _ := procCreateCompatibleBmp.Call(screenDC, uintptr(width), uintptr(height))
	if bitmap == 0 {
		return nil, 0, 0, errors.New("PrintWindow: CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(bitmap)

	previous, _, _ := procSelectObject.Call(memDC, bitmap)
	defer procSelectObject.Call(memDC, previous)

	ret, _, _ := procPrintWindow.Call(uintptr(hwnd), memDC, pwRenderFullContent)
	if ret == 0 {
		return nil, 0, 0, errors.New("PrintWindow failed")
	}
	return dibPixels(memDC, bitmap, width, height)
}

// captureGDIRegion mirrors the CopyFromScreen path: a screen-sized grab of
// the bounds region.
func captureGDIRegion(bounds *frame) ([]byte, int, int, error) {
	rect := bounds
	if rect == nil || rect.Width <= 0 || rect.Height <= 0 {
		return nil, 0, 0, errors.New("gdi requires window bounds")
	}
	width := int(mathRound(rect.Width))
	height := int(mathRound(rect.Height))

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, 0, 0, errors.New("gdi: GetDC failed")
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, 0, 0, errors.New("gdi: CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)

	bitmap, _, _ := procCreateCompatibleBmp.Call(screenDC, uintptr(width), uintptr(height))
	if bitmap == 0 {
		return nil, 0, 0, errors.New("gdi: CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(bitmap)

	previous, _, _ := procSelectObject.Call(memDC, bitmap)
	defer procSelectObject.Call(memDC, previous)

	// CopyFromScreen equivalent (SRCCOPY only, like the default .NET path).
	procBitBlt.Call(memDC, 0, 0, uintptr(width), uintptr(height), screenDC,
		uintptr(int(mathRound(rect.X))), uintptr(int(mathRound(rect.Y))), srcCopy)
	return dibPixels(memDC, bitmap, width, height)
}

var procGetDC = user32.NewProc("GetDC")
var procReleaseDC = user32.NewProc("ReleaseDC")

// dibPixels reads a selected DIB via GetDIBits as top-down 32bpp BGRA.
func dibPixels(memDC uintptr, bitmap uintptr, width, height int) ([]byte, int, int, error) {
	type bitmapInfoHeader struct {
		Size          uint32
		Width         int32
		Height        int32
		Planes        uint16
		BitCount      uint16
		Compression   uint32
		SizeImage     uint32
		XPelsPerMeter int32
		YPelsPerMeter int32
		ClrUsed       uint32
		ClrImportant  uint32
	}
	info := struct {
		Header bitmapInfoHeader
		Colors [1]uint32
	}{}
	info.Header = bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(info.Header)),
		Width:       int32(width),
		Height:      int32(-height), // top-down
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}
	pixels := make([]byte, width*height*4)
	ret, _, _ := procGetDIBits.Call(memDC, uintptr(bitmap), 0, uintptr(height),
		uintptr(unsafe.Pointer(&pixels[0])), uintptr(unsafe.Pointer(&info)), dibRGBColors)
	if ret == 0 {
		return nil, 0, 0, errors.New("GetDIBits failed")
	}
	return pixels, width, height, nil
}

// isBlankPixels mirrors IsBlankBitmap: 4x4 sampled brightness grid.
func isBlankPixels(pixels []byte, width, height int) bool {
	if len(pixels) == 0 || width < 2 || height < 2 {
		return true
	}
	const steps = 4
	for ix := 0; ix < steps; ix++ {
		for iy := 0; iy < steps; iy++ {
			x := width * (ix*2 + 1) / (steps * 2)
			y := height * (iy*2 + 1) / (steps * 2)
			offset := (y*width + x) * 4
			b, g, r := pixels[offset], pixels[offset+1], pixels[offset+2]
			if b > 0 || g > 0 || r > 0 {
				return false
			}
		}
	}
	return true
}

// pngBase64FromBGRA converts 32bpp BGRA pixels into a base64 PNG.
func pngBase64FromBGRA(pixels []byte, width, height int) (string, error) {
	output := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		row := pixels[y*width*4 : (y+1)*width*4]
		for x := 0; x < width; x++ {
			offset := x * 4
			output.SetRGBA(x, y, color.RGBA{R: row[offset+2], G: row[offset+1], B: row[offset], A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, output); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buffer.Bytes()), nil
}

// --- Activation -------------------------------------------------------------

// enableForegroundWindow mirrors Enable-ForegroundWindow.
func enableForegroundWindow(hwnd windows.HWND) {
	showWindow(hwnd, swRestore)
	setForegroundWindow(hwnd)
	sleepMs(150)
}

// --- Start Menu (.lnk) app enumeration ---------------------------------------

var shell32 = windows.NewLazySystemDLL("shell32.dll")
var procSHGetFolderPath = shell32.NewProc("SHGetFolderPathW")

const (
	csidlStartMenu       = 0x000b
	csidlCommonStartMenu = 0x0016
)

func knownFolderPath(csidl int) string {
	var buffer [windows.MAX_PATH]uint16
	ret, _, _ := procSHGetFolderPath.Call(0, uintptr(csidl), 0, 0, uintptr(unsafe.Pointer(&buffer[0])))
	if ret != 0 {
		return ""
	}
	return windows.UTF16ToString(buffer[:])
}

// shortcutTarget resolves a .lnk target the same way WScript.Shell does: the
// shell's own IShellLinkW object (IPersistFile::Load + GetPath), which applies
// IDList resolution, environment-variable expansion, and distributed link
// tracking (stale targets heal to the moved file — e.g. WeChat.exe →
// Weixin.exe). A hand-rolled LinkInfo parser cannot reproduce that.
var ()

const (
	clsidShellLink  = "{00021401-0000-0000-C000-000000000046}"
	iidIShellLinkW  = "{000214F9-0000-0000-C000-000000000046}"
	iidIPersistFile = "{0000010B-0000-0000-C000-000000000046}"

	shellLinkSlotGetPath = 3 // IShellLinkW::GetPath (after IUnknown 0-2)
	persistFileSlotLoad  = 5 // IPersistFile::Load
)

// shortcutTarget loads linkPath through the shell link COM object and returns
// the resolved target path ("" when the link has no file-system target, e.g.
// control-panel IDList-only links — WScript returns "" for those too).
func shortcutTarget(shellLink, persistFile unsafe.Pointer, linkPath string) string {
	utf16, err := windows.UTF16PtrFromString(linkPath)
	if err != nil {
		return ""
	}
	// IPersistFile::Load(path, STGM_READ=0; STGM_WRITE(1) gets E_ACCESSDENIED
	// on ProgramData links the user cannot write).
	if hr, _, _ := vtableCall(persistFile, persistFileSlotLoad,
		uintptr(unsafe.Pointer(utf16)), 0); int32(hr) < 0 {
		return ""
	}
	var findData [592]byte // WIN32_FIND_DATAW, unused output buffer
	var buffer [windows.MAX_PATH]uint16
	// IShellLinkW::GetPath(buf, MAX_PATH, &findData, 0).
	if hr, _, _ := vtableCall(shellLink, shellLinkSlotGetPath,
		uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)),
		uintptr(unsafe.Pointer(&findData[0])), 0); int32(hr) < 0 {
		return ""
	}
	return windows.UTF16ToString(buffer[:])
}

// dirEntryNative describes one directory entry in native enumeration order.
type dirEntryNative struct {
	name   string
	isDir  bool
	hidden bool
}

// enumerateDirNative mirrors Get-ChildItem's enumeration: FindFirstFileW /
// FindNextFileW (NTFS directory order, no lexical resort) with hidden and
// system entries skipped (Get-ChildItem without -Force hides them).
var (
	kernel32           = windows.NewLazySystemDLL("kernel32.dll")
	procFindFirstFileW = kernel32.NewProc("FindFirstFileW")
	procFindNextFileW  = kernel32.NewProc("FindNextFileW")
	procFindClose      = kernel32.NewProc("FindClose")
)

type win32FindData struct {
	attributes    uint32
	creationTime  [8]byte
	lastAccess    [8]byte
	lastWrite     [8]byte
	sizeHigh      uint32
	sizeLow       uint32
	reserved0     uint32
	reserved1     uint32
	fileName      [windows.MAX_PATH]uint16
	alternateName [14]uint16
}

func enumerateDirNative(dir string) []dirEntryNative {
	pattern, err := windows.UTF16PtrFromString(strings.TrimSuffix(dir, `\`) + `\*`)
	if err != nil {
		return nil
	}
	var data win32FindData
	handle, _, _ := procFindFirstFileW.Call(uintptr(unsafe.Pointer(pattern)), uintptr(unsafe.Pointer(&data)))
	if handle == ^uintptr(0) { // INVALID_HANDLE_VALUE
		return nil
	}
	defer procFindClose.Call(handle)
	var entries []dirEntryNative
	for {
		name := windows.UTF16ToString(data.fileName[:])
		if name != "." && name != ".." {
			entries = append(entries, dirEntryNative{
				name:   name,
				isDir:  data.attributes&0x10 != 0, // FILE_ATTRIBUTE_DIRECTORY
				hidden: data.attributes&0x6 != 0,  // HIDDEN | SYSTEM
			})
		}
		ret, _, _ := procFindNextFileW.Call(handle, uintptr(unsafe.Pointer(&data)))
		if ret == 0 {
			break
		}
	}
	return entries
}

// startMenuApps mirrors Get-StartMenuApps: deduped .lnk inventory of the user
// + common Start Menus, enumerated in the PS-era Get-ChildItem -Recurse
// order (each directory's files first, then its subdirectories, both sorted
// culture-aware case-insensitively, depth-first).
func startMenuApps() []listAppsApp {
	var apps []listAppsApp
	seen := map[string]bool{}
	roots := []string{knownFolderPath(csidlStartMenu), knownFolderPath(csidlCommonStartMenu)}
	if err := uiaOnThread(func() {
		shellLink, err := oleCreateInstance(clsidShellLink, iidIShellLinkW)
		if err != nil {
			return
		}
		defer oleRelease(shellLink)
		persistFile, err := oleQueryInterface(shellLink, iidIPersistFile)
		if err != nil {
			return
		}
		defer oleRelease(persistFile)
		addApp := func(linkPath string) {
			target := shortcutTarget(shellLink, persistFile, linkPath)
			if strings.TrimSpace(target) == "" {
				return
			}
			if _, err := os.Stat(target); err != nil {
				return
			}
			id := strings.ToLower(strings.TrimSuffix(filepath.Base(target), filepath.Ext(target)))
			if seen[id] {
				return
			}
			seen[id] = true
			apps = append(apps, listAppsApp{
				DisplayName: strings.TrimSuffix(filepath.Base(linkPath), filepath.Ext(linkPath)),
				ID:          id,
			})
		}
		var walk func(dir string)
		walk = func(dir string) {
			entries := enumerateDirNative(dir)
			subdirs := make([]string, 0, len(entries))
			for _, entry := range entries {
				if entry.hidden {
					continue // Get-ChildItem without -Force skips hidden/system
				}
				full := filepath.Join(dir, entry.name)
				if entry.isDir {
					subdirs = append(subdirs, full)
					continue
				}
				if strings.EqualFold(filepath.Ext(entry.name), ".lnk") {
					addApp(full)
				}
			}
			for _, subdir := range subdirs {
				walk(subdir)
			}
		}
		for _, root := range roots {
			if strings.TrimSpace(root) == "" {
				continue
			}
			walk(root)
		}
	}); err != nil {
		return nil
	}
	return apps
}

// --- Small shared helpers ---------------------------------------------------

func sleepMs(milliseconds int) {
	time.Sleep(time.Duration(milliseconds) * time.Millisecond)
}

// mathRound mirrors [math]::Round (banker's rounding, midpoint-to-even).
func mathRound(value float64) float64 {
	return math.Round(value)
}

// utf16Units splits text into UTF-16 code units so surrogate pairs are
// delivered as two events (matching the PS runtimes' per-char behavior).
func utf16Units(text string) []uint16 {
	return utf16.Encode([]rune(text))
}

func splitChord(key string) []string {
	rawParts := strings.Split(key, "+")
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func decimalDigits(value string) int {
	if value == "" {
		return -1
	}
	number := 0
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return -1
		}
		number = number*10 + int(ch-'0')
	}
	return number
}

// splitKeypadName matches ^(kp|numpad)_([0-9])$.
func splitKeypadName(name string) (string, int, bool) {
	for _, prefix := range []string{"kp_", "numpad_"} {
		if strings.HasPrefix(name, prefix) {
			rest := name[len(prefix):]
			if len(rest) == 1 && rest[0] >= '0' && rest[0] <= '9' {
				return prefix, int(rest[0] - '0'), true
			}
		}
	}
	return "", -1, false
}

// --- Minimal raw COM helpers (go-ole-free core paths) -----------------------

var ole32 = windows.NewLazySystemDLL("ole32.dll")
var procCoInitializeEx = ole32.NewProc("CoInitializeEx")
var procCoCreateInstance = ole32.NewProc("CoCreateInstance")

func coInitializeEx(mode uint32) {
	procCoInitializeEx.Call(0, uintptr(mode))
}

func clsidFromString(value string) (windows.GUID, error) {
	var guid windows.GUID
	// CLSIDFromString requires the braced registry form.
	if !strings.HasPrefix(value, "{") {
		value = "{" + value + "}"
	}
	utf16, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return guid, err
	}
	hr, _, _ := ole32.NewProc("CLSIDFromString").Call(uintptr(unsafe.Pointer(utf16)), uintptr(unsafe.Pointer(&guid)))
	if int32(hr) < 0 {
		return guid, fmt.Errorf("CLSIDFromString(%s) failed: 0x%08x", value, int32(hr))
	}
	return guid, nil
}

// oleCreateInstance creates a COM object and returns the requested interface.
func oleCreateInstance(clsid, iid string) (unsafe.Pointer, error) {
	classGUID, err := clsidFromString(clsid)
	if err != nil {
		return nil, err
	}
	iidGUID, err := clsidFromString(iid)
	if err != nil {
		return nil, err
	}
	var unknown unsafe.Pointer
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&classGUID)), 0, 1, /*CLSCTX_INPROC_SERVER*/
		uintptr(unsafe.Pointer(&iidGUID)), uintptr(unsafe.Pointer(&unknown)))
	if int32(hr) < 0 {
		return nil, fmt.Errorf("CoCreateInstance failed: 0x%08x", int32(hr))
	}
	return unknown, nil
}

// oleQueryInterface queries an object for another interface.
func oleQueryInterface(self unsafe.Pointer, iid string) (unsafe.Pointer, error) {
	iidGUID, err := clsidFromString(iid)
	if err != nil {
		return nil, err
	}
	var result unsafe.Pointer
	hr, _, _ := vtableCall(self, 0, uintptr(unsafe.Pointer(&iidGUID)), uintptr(unsafe.Pointer(&result)))
	if int32(hr) < 0 {
		return nil, fmt.Errorf("QueryInterface failed: 0x%08x", int32(hr))
	}
	return result, nil
}

// oleRelease calls IUnknown::Release.
func oleRelease(self unsafe.Pointer) {
	if self != nil {
		vtableCall(self, 2)
	}
}

// vtableCall invokes a raw COM vtable slot (slot 0 = QueryInterface). It
// returns the raw HRESULT carry (uintptr), reserved payload, and syscall
// errno; HRESULT sign checks use int32(hr).
func vtableCall(self unsafe.Pointer, slot int, args ...uintptr) (uintptr, uintptr, error) {
	// *self is the vtable pointer; treat it as a function-pointer array.
	vtable := *(**[1024]uintptr)(self)
	proc := vtable[slot]
	selfArg := uintptr(self)
	switch len(args) {
	case 0:
		ret, _, err := syscall.Syscall(proc, 1, selfArg, 0, 0)
		return ret, 0, err
	case 1:
		return syscall.Syscall(proc, 2, selfArg, args[0], 0)
	case 2:
		return syscall.Syscall(proc, 3, selfArg, args[0], args[1])
	case 3:
		r1, r2, err := syscall.Syscall6(proc, 4, selfArg, args[0], args[1], args[2], 0, 0)
		return r1, r2, err
	default:
		r1, r2, err := syscall.SyscallN(proc, append([]uintptr{selfArg}, args...)...)
		return r1, r2, err
	}
}

func postMessage(hwnd windows.HWND, message uint32, wParam, lParam uintptr) {
	procPostMessage.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
}

func showWindow(hwnd windows.HWND, command int) {
	procShowWindow.Call(uintptr(hwnd), uintptr(command))
}

// setForegroundWindow mirrors the official activation sequence: attach the
// calling thread to the target window's input queue and grant the target
// process foreground rights before SetForegroundWindow, so activation does
// not silently fail while another process holds the foreground lock. Every
// failure mode keeps the historical semantics (BOOL return of
// SetForegroundWindow, detach always happens).
func setForegroundWindow(hwnd windows.HWND) bool {
	var pid uint32
	targetThread, _ := windows.GetWindowThreadProcessId(hwnd, &pid)
	selfThread := windows.GetCurrentThreadId()
	attached := false
	if targetThread != 0 && targetThread != selfThread {
		ret, _, _ := procAttachThreadInput.Call(uintptr(selfThread), uintptr(targetThread), 1)
		attached = ret != 0
	}
	if pid != 0 {
		procAllowSetForeground.Call(uintptr(pid))
	}
	ret, _, _ := procSetForegroundWindow.Call(uintptr(hwnd))
	if attached {
		procAttachThreadInput.Call(uintptr(selfThread), uintptr(targetThread), 0)
	}
	return ret != 0
}

func sendMessage(hwnd windows.HWND, message uint32, wParam, lParam uintptr) (uintptr, uintptr, error) {
	ret, _, err := procSendMessage.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	return ret, 0, err
}
