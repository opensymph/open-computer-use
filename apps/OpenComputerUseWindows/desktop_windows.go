//go:build windows

package main

// Native Win32 backends for the desktop-level commands: GDI whole-virtual-
// screen capture, GetCursorPos, the SendInput layer already used by
// input_method=global, and a detached ffmpeg gdigrab recorder stopped with a
// Ctrl+Break console event (the Windows sibling of the Linux SIGINT stop).
// desktop.go keeps the parsing and command construction portable.

import (
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// --- screenshot ------------------------------------------------------------

// captureScreenImage grabs the whole virtual desktop (all monitors) as an
// NRGBA image through the same GDI BitBlt path the per-window capture chain
// uses (captureGDIRegion over the virtual-screen bounds).
func captureScreenImage() (image.Image, error) {
	left := systemMetrics(smXVirtualScreen)
	top := systemMetrics(smYVirtualScreen)
	width := systemMetrics(smCXVirtualScreen)
	height := systemMetrics(smCYVirtualScreen)
	if width <= 0 || height <= 0 {
		return nil, errors.New("no display detected (GetSystemMetrics virtual screen is empty)")
	}
	pixels, w, h, err := captureGDIRegion(&frame{
		X:      float64(left),
		Y:      float64(top),
		Width:  float64(width),
		Height: float64(height),
	})
	if err != nil {
		return nil, fmt.Errorf("screen capture failed: %w", err)
	}
	return bgraToNRGBA(pixels, w, h), nil
}

// bgraToNRGBA swizzles top-down 32bpp BGRA DIB pixels into a PNG-encodable
// NRGBA image.
func bgraToNRGBA(pixels []byte, width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		row := pixels[y*width*4 : (y+1)*width*4]
		dst := img.Pix[y*img.Stride : y*img.Stride+width*4]
		for x := 0; x < width; x++ {
			dst[x*4+0] = row[x*4+2]
			dst[x*4+1] = row[x*4+1]
			dst[x*4+2] = row[x*4+0]
			dst[x*4+3] = 0xff
		}
	}
	return img
}

// --- cursor-position -------------------------------------------------------

var procGetCursorPos = user32.NewProc("GetCursorPos")

// queryPointer returns the pointer position in virtual-screen coordinates and
// the virtual desktop size, mirroring the Linux root-window semantics.
func queryPointer() (pointerInfo, error) {
	var point struct{ X, Y int32 }
	ret, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&point)))
	if ret == 0 {
		return pointerInfo{}, errors.New("GetCursorPos failed")
	}
	return pointerInfo{
		X:            int(point.X),
		Y:            int(point.Y),
		ScreenWidth:  systemMetrics(smCXVirtualScreen),
		ScreenHeight: systemMetrics(smCYVirtualScreen),
	}, nil
}

// --- input (SendInput) -----------------------------------------------------

// mouseButtonName maps the shared X11-style button numbering back to the
// names the SendInput helpers take.
func mouseButtonName(button int) string {
	switch button {
	case 2:
		return "middle"
	case 3:
		return "right"
	default:
		return "left"
	}
}

func mouseDownUpFlags(button string) (uint32, uint32) {
	switch button {
	case "right":
		return mouseeventfRightDown, mouseeventfRightUp
	case "middle":
		return mouseeventfMiddleDown, mouseeventfMiddleUp
	default:
		return mouseeventfLeftDown, mouseeventfLeftUp
	}
}

// realMouseDragButton is realMouseDrag generalized to any button.
func realMouseDragButton(fromX, fromY, toX, toY int, button string) error {
	if err := realMouseMove(fromX, fromY); err != nil {
		return err
	}
	down, up := mouseDownUpFlags(button)
	if err := sendInputs([]tagINPUT{mouseEvent(down, 0, 0, 0)}); err != nil {
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
	return sendInputs([]tagINPUT{mouseEvent(up, 0, 0, 0)})
}

// runInputOps executes the prepared SendInput operations in order.
func runInputOps(ops []inputOp) error {
	for _, op := range ops {
		var err error
		switch op.kind {
		case "move":
			err = realMouseMove(op.x, op.y)
		case "click":
			err = realMouseClick(mouseButtonName(op.button), op.count)
		case "mouse_down":
			down, _ := mouseDownUpFlags(mouseButtonName(op.button))
			err = sendInputs([]tagINPUT{mouseEvent(down, 0, 0, 0)})
		case "mouse_up":
			_, up := mouseDownUpFlags(mouseButtonName(op.button))
			err = sendInputs([]tagINPUT{mouseEvent(up, 0, 0, 0)})
		case "drag":
			err = realMouseDragButton(op.x, op.y, op.toX, op.toY, mouseButtonName(op.button))
		case "scroll":
			err = realWheel(op.dy*wheelDeltaUnit, op.dx*wheelDeltaUnit)
		case "type":
			err = realTypeText(op.text)
		case "key":
			var modifiers []uint16
			var vk uint16
			modifiers, vk, err = realKeyForName(op.key)
			if err == nil {
				err = realKeyChord(modifiers, vk)
			}
		case "keydown":
			err = realKeyDownName(op.key)
		case "keyup":
			err = realKeyUpName(op.key)
		case "sleep_ms":
			if op.sleepMs > 0 {
				sleepMs(op.sleepMs)
			}
		default:
			err = fmt.Errorf("unknown input op: %s", op.kind)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func vkForKeyName(name string) (uint16, error) {
	if vk, err := modifierVirtualKeyForName(name); err == nil {
		return vk, nil
	}
	return virtualKeyForName(name)
}

func realKeyDownName(name string) error {
	vk, err := vkForKeyName(name)
	if err != nil {
		return err
	}
	return sendInputs([]tagINPUT{keyEvent(vk, mapVirtualKey(vk), keyeventfScancode)})
}

func realKeyUpName(name string) error {
	vk, err := vkForKeyName(name)
	if err != nil {
		return err
	}
	return sendInputs([]tagINPUT{keyEvent(vk, mapVirtualKey(vk), keyeventfScancode|keyeventfKeyUp)})
}

// --- record (ffmpeg gdigrab) ----------------------------------------------

const (
	ctrlBreakEvent   = 1
	createNewConsole = 0x00000010
	newProcessGroup  = 0x00000200
)

var (
	procAttachConsole            = kernel32.NewProc("AttachConsole")
	procFreeConsole              = kernel32.NewProc("FreeConsole")
	procSetConsoleCtrlHandler    = kernel32.NewProc("SetConsoleCtrlHandler")
	procGenerateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
	procGetConsoleWindow         = kernel32.NewProc("GetConsoleWindow")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
)

// startRecordProcess launches ffmpeg detached so recording survives this CLI
// process exiting; `record stop` later signals it by pid. stdout/stderr go to
// a sibling .log file for debugging. CREATE_NEW_CONSOLE (+ hidden window)
// guarantees the recorder owns a console even when this CLI has none, which
// the Ctrl+Break stop path needs; CREATE_NEW_PROCESS_GROUP makes its pid a
// ctrl-event group id.
func startRecordProcess(output string, args []string) (int, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return 0, errors.New("ffmpeg is required for screen recording but was not found on PATH")
	}
	logFile, err := os.Create(output + ".log")
	if err != nil {
		return 0, fmt.Errorf("cannot create recording log: %w", err)
	}
	cmd := exec.Command(ffmpeg, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewConsole | newProcessGroup,
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return 0, fmt.Errorf("cannot start ffmpeg: %w", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	logFile.Close()
	return pid, nil
}

// stopRecordProcess asks ffmpeg to finish. ffmpeg finalizes the mp4 on
// Ctrl+Break, so stop attaches the recorder's console, raises the event for
// its process group, and waits briefly for it to exit. If the console dance is
// impossible the recorder is terminated hard and the caller is told the mp4
// may not be playable.
func stopRecordProcess(pid int) error {
	if pid <= 0 {
		return errors.New("invalid recording pid")
	}
	if !processAlive(pid) {
		return nil // already gone
	}
	if err := sendCtrlBreak(pid); err != nil {
		terminateProcessHard(pid)
		return fmt.Errorf("could not deliver Ctrl+Break to ffmpeg pid %d (%v); it was terminated hard and the mp4 may not be playable", pid, err)
	}
	for i := 0; i < 100; i++ {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("ffmpeg pid %d did not exit after stop signal", pid)
}

// consoleOwnerPID reports the conhost pid owning our current console, so it
// can be re-attached after the stop dance detaches it (0 when we have none).
func consoleOwnerPID() int {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return 0
	}
	var owner uint32
	procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&owner)))
	return int(owner)
}

// sendCtrlBreak raises CTRL_BREAK_EVENT for the recorder's process group from
// this process, restoring our own console afterwards.
func sendCtrlBreak(pid int) error {
	originalConsole := consoleOwnerPID()
	// AttachConsole fails while we still own a console.
	_, _, _ = procFreeConsole.Call()
	ret, _, _ := procAttachConsole.Call(uintptr(pid))
	if ret == 0 {
		if originalConsole != 0 {
			_, _, _ = procAttachConsole.Call(uintptr(originalConsole))
		}
		return errors.New("AttachConsole to the recorder failed")
	}
	defer func() {
		_, _, _ = procFreeConsole.Call()
		if originalConsole != 0 {
			_, _, _ = procAttachConsole.Call(uintptr(originalConsole))
		}
	}()
	// Ignore the event in ourselves so only the recorder group sees it.
	procSetConsoleCtrlHandler.Call(0, 1)
	ret, _, _ = procGenerateConsoleCtrlEvent.Call(ctrlBreakEvent, uintptr(pid))
	if ret == 0 {
		return errors.New("GenerateConsoleCtrlEvent failed")
	}
	return nil
}

func terminateProcessHard(pid int) {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(handle)
	_ = windows.TerminateProcess(handle, 1)
}
