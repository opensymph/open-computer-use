//go:build windows

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This file hosts the syscall and PNG parsing helpers backing the official
// coordinate-input observation gate in main.go. The gate state itself lives
// on service.screenshots (screenshotMeta) and is populated by
// get_window_state observations.

// procGetWindowRect is declared here (not in native_win32.go) so the service
// layer gate stays self-contained.
var procGetWindowRect = windows.NewLazySystemDLL("user32.dll").NewProc("GetWindowRect")

// gateWindowRect mirrors the Win32 RECT returned by GetWindowRect.
type gateWindowRect struct {
	Left, Top, Right, Bottom int32
}

// queryWindowBounds is a variable so tests can stub the live syscall.
var queryWindowBounds = func(hwnd int64) (gateWindowRect, bool) {
	var rect gateWindowRect
	ret, _, _ := procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
	return rect, ret != 0
}

var pngSignature = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// pngDimensions reads the pixel width/height from the PNG IHDR chunk of a
// base64-encoded screenshot. Only the first ~60 base64 bytes are decoded (45
// raw bytes); the IHDR width and height live at raw offsets 16..23.
func pngDimensions(base64PNG string) (width, height int, ok bool) {
	encoded := base64PNG
	if len(encoded) > 60 {
		encoded = encoded[:60]
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) < 24 {
		return 0, 0, false
	}
	if !bytes.Equal(raw[:8], pngSignature) || !bytes.Equal(raw[12:16], []byte("IHDR")) {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint32(raw[16:20])), int(binary.BigEndian.Uint32(raw[20:24])), true
}
