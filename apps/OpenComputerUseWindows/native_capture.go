//go:build windows

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WGC (Windows.Graphics.Capture) implementation, ported 1:1 from the
// validated OCUCapture C# in the retired PS-era runtime. Hard conclusions preserved here:
//
//   - WinRT interfaces carry three IInspectable vtable slots (3-5) before
//     their first projected method, on top of IUnknown (0-2).
//   - The D3D11 immediate context refuses QI for its own IID, so Map/Unmap/
//     CopyResource go through raw vtable slots (14/15/47).
//   - D3D11CreateDevice: driverType=1 (hardware), flags=0x20 (BGRA support),
//     sdkVersion=7; pixel format B8G8R8A8UIntNormalized=87.
//
// The vtable layout below mirrors the ComImport declarations in the retired PS-era runtime:
// placeholder methods occupy their real slots so slot indexes line up.
var (
	d3d11                                    = windows.NewLazySystemDLL("d3d11.dll")
	combase                                  = windows.NewLazySystemDLL("combase.dll")
	procD3D11CreateDevice                    = d3d11.NewProc("D3D11CreateDevice")
	procCreateDirect3D11DeviceFromDXGIDevice = d3d11.NewProc("CreateDirect3D11DeviceFromDXGIDevice")
	procWindowsCreateString                  = combase.NewProc("WindowsCreateString")
	procRoGetActivationFactory               = combase.NewProc("RoGetActivationFactory")
)

// WGC vtable slot indexes (IUnknown=0..2, IInspectable=3..5 for WinRT
// interfaces, then the projected methods in declaration order).
const (
	slotItemGetSize            = 7 // IGraphicsCaptureItem.get_Size
	slotInteropCreateForWindow = 3 // IGraphicsCaptureItemInterop.CreateForWindow
	slotStaticsCreateFree      = 6 // IDirect3D11CaptureFramePoolStatics2.CreateFreeThreaded
	slotPoolRecreate           = 6 // IDirect3D11CaptureFramePool.Recreate
	slotPoolTryGetNextFrame    = 7
	slotPoolCreateSession      = 10
	slotFrameGetSurface        = 6  // IDirect3D11CaptureFrame.get_Surface
	slotFrameGetContentSize    = 8
	slotSessionStartCapture    = 6  // IGraphicsCaptureSession.StartCapture
	slotDxgiAccessGetInterface = 3  // IDirect3DDxgiInterfaceAccess.GetInterface
	slotTextureGetDesc         = 10 // ID3D11Texture2D.GetDesc (after 7 placeholder slots)
	slotDeviceCreateTexture2D  = 5  // ID3D11Device.CreateTexture2D
	slotCtxMap                 = 14 // ID3D11DeviceContext.Map
	slotCtxUnmap               = 15
	slotCtxCopyResource        = 47
)

// wgcGUIDs are the interface IIDs validated by the PS-era probes.
const (
	iidIDXGIDevice          = "54EC77FA-1377-44E6-8C32-88FD5F44C84C"
	iidIGraphicsCaptureItem = "79C3F95B-31F7-4EC2-A464-632EF5D30760"
	iidItemInterop          = "3628E81B-3CAC-4C60-B7F4-23CE0E0C3356"
	iidFramePoolStatics2    = "589B103F-6BBC-5DF5-A991-02E28B3B66D5"
	iidID3D11Texture2D      = "6F15AAF2-D208-4E89-9AB4-489535D34F9C"
	iidID3D11Device         = "DB6F6DDB-AC77-4E88-8253-819DF9BBF140"
)

const wgcPixelFormatB8G8R8A8 = 87

type sizeInt32 struct {
	Width, Height int32
}

type texture2DDesc struct {
	Width, Height, MipLevels, ArraySize, Format uint32
	SampleCount, SampleQuality                  uint32
	Usage, BindFlags, CPUAccessFlags, MiscFlags uint32
}

type mappedSubresource struct {
	pData      unsafe.Pointer
	rowPitch   uint32
	depthPitch uint32
}

// wgcDevice is the once-per-process device trio (D3D11 device, immediate
// context, WinRT IDirect3D device wrapper).
type wgcDevice struct {
	d3dDevice   unsafe.Pointer
	context     unsafe.Pointer
	winrtDevice unsafe.Pointer
}

var (
	wgcDeviceOnce sync.Once
	wgcDeviceErr  error
	wgcDevices    wgcDevice

	wgcEntriesMu sync.Mutex
	wgcEntries   = map[int64]*wgcCaptureEntry{}

	wgcCaptureMu sync.Mutex // serializes frame draining like OCUCapture._captureLock
)

func ensureWGCDevice() error {
	wgcDeviceOnce.Do(func() {
		// RoGetActivationFactory requires an initialized COM apartment.
		// MTA avoids goroutine/thread affinity for the free-threaded frame
		// pool; S_FALSE (already initialized) is fine.
		procCoInitializeEx.Call(0, 0 /*COINIT_MULTITHREADED*/)
		var device, context unsafe.Pointer
		var featureLevel int32
		hr, _, _ := procD3D11CreateDevice.Call(0, 1, 0, 0x20, 0, 0, 7,
			uintptr(unsafe.Pointer(&device)), uintptr(unsafe.Pointer(&featureLevel)),
			uintptr(unsafe.Pointer(&context)))
		if int32(hr) < 0 || device == nil || context == nil {
			wgcDeviceErr = fmt.Errorf("D3D11CreateDevice failed: 0x%08x", int32(hr))
			return
		}
		dxgi, err := comQueryInterface(device, iidIDXGIDevice)
		if err != nil {
			wgcDeviceErr = err
			return
		}
		defer comRelease(dxgi)
		var winrtDevice unsafe.Pointer
		hr, _, _ = procCreateDirect3D11DeviceFromDXGIDevice.Call(
			uintptr(dxgi), uintptr(unsafe.Pointer(&winrtDevice)))
		if int32(hr) < 0 {
			wgcDeviceErr = fmt.Errorf("CreateDirect3D11DeviceFromDXGIDevice failed: 0x%08x", int32(hr))
			return
		}
		wgcDevices = wgcDevice{d3dDevice: device, context: context, winrtDevice: winrtDevice}
	})
	return wgcDeviceErr
}

// activateWGCFactory resolves a WinRT activation factory by runtime class
// name and interface id (RoGetActivationFactory).
func activateWGCFactory(runtimeClass, iid string) (unsafe.Pointer, error) {
	guid, err := clsidFromString(iid)
	if err != nil {
		return nil, err
	}
	utf16, err := windows.UTF16PtrFromString(runtimeClass)
	if err != nil {
		return nil, err
	}
	var hstring uintptr
	hr, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(utf16)),
		uintptr(len([]rune(runtimeClass))), uintptr(unsafe.Pointer(&hstring)))
	if int32(hr) < 0 {
		return nil, fmt.Errorf("WindowsCreateString failed: 0x%08x", int32(hr))
	}
	defer combase.NewProc("WindowsDeleteString").Call(hstring)
	var factory unsafe.Pointer
	hr, _, _ = procRoGetActivationFactory.Call(hstring,
		uintptr(unsafe.Pointer(&guid)), uintptr(unsafe.Pointer(&factory)))
	if int32(hr) < 0 {
		return nil, fmt.Errorf("RoGetActivationFactory(%s) failed: 0x%08x", runtimeClass, int32(hr))
	}
	return factory, nil
}

// comQueryInterface / comRelease reuse the raw vtable helpers from
// native_win32.go (slots 0 and 2).
func comQueryInterface(self unsafe.Pointer, iid string) (unsafe.Pointer, error) {
	result, err := oleQueryInterface(self, iid)
	if err != nil {
		return nil, fmt.Errorf("QueryInterface(%s): %w", iid, err)
	}
	return result, nil
}

func comRelease(self unsafe.Pointer) { oleRelease(self) }

// comCall invokes a vtable slot and asserts a non-negative HRESULT.
func comCall(self unsafe.Pointer, slot int, args ...uintptr) error {
	hr, _, _ := vtableCall(self, slot, args...)
	if int32(hr) < 0 {
		return fmt.Errorf("vtable slot %d failed: 0x%08x", slot, int32(hr))
	}
	return nil
}

type wgcCaptureEntry struct {
	item, pool, session unsafe.Pointer
	width, height       int32
	// windowLeft/Top record the window rect the entry was created for
	// (diagnostics for stale-pool postmortems).
	windowLeft, windowTop int32
}

func releaseWGCCaptureEntry(entry *wgcCaptureEntry) {
	if entry == nil {
		return
	}
	if entry.session != nil {
		comRelease(entry.session)
		entry.session = nil
	}
	if entry.pool != nil {
		comRelease(entry.pool)
		entry.pool = nil
	}
	if entry.item != nil {
		comRelease(entry.item)
		entry.item = nil
	}
}

func createWGCCaptureEntry(hwnd windows.HWND) (*wgcCaptureEntry, error) {
	if err := ensureWGCDevice(); err != nil {
		return nil, err
	}
	itemFactory, err := activateWGCFactory("Windows.Graphics.Capture.GraphicsCaptureItem", iidItemInterop)
	if err != nil {
		return nil, err
	}
	defer comRelease(itemFactory)
	itemIID, err := clsidFromString(iidIGraphicsCaptureItem)
	if err != nil {
		return nil, err
	}
	var item unsafe.Pointer
	if err := comCall(itemFactory, slotInteropCreateForWindow,
		uintptr(hwnd), uintptr(unsafe.Pointer(&itemIID)), uintptr(unsafe.Pointer(&item))); err != nil {
		return nil, fmt.Errorf("CreateForWindow failed: %w", err)
	}
	var size sizeInt32
	if err := comCall(item, slotItemGetSize, uintptr(unsafe.Pointer(&size))); err != nil {
		comRelease(item)
		return nil, fmt.Errorf("get_Size failed: %w", err)
	}

	poolFactory, err := activateWGCFactory("Windows.Graphics.Capture.Direct3D11CaptureFramePool", iidFramePoolStatics2)
	if err != nil {
		comRelease(item)
		return nil, err
	}
	defer comRelease(poolFactory)
	var pool unsafe.Pointer
	if err := comCall(poolFactory, slotStaticsCreateFree,
		uintptr(wgcDevices.winrtDevice), wgcPixelFormatB8G8R8A8, 2,
		uintptr(size.Width)|uintptr(uint32(size.Height))<<32, uintptr(unsafe.Pointer(&pool))); err != nil {
		comRelease(item)
		return nil, fmt.Errorf("CreateFreeThreaded failed: %w", err)
	}
	// Cursor suppression is intentionally not implemented: the property
	// lives on IGraphicsCaptureSession2, which this build's session does not
	// expose (verified via IInspectable::GetIids), and the frame pool
	// interface ends at get_DispatcherQueue — the previous slot-13 call was
	// out of bounds. The retired PS-era OCUCapture never disabled the cursor
	// either (git 4009084), so this matches the dual-run baseline.
	var session unsafe.Pointer
	if err := comCall(pool, slotPoolCreateSession, uintptr(item), uintptr(unsafe.Pointer(&session))); err != nil {
		comRelease(pool)
		comRelease(item)
		return nil, fmt.Errorf("CreateCaptureSession failed: %w", err)
	}
	if err := comCall(session, slotSessionStartCapture); err != nil {
		comRelease(session)
		comRelease(pool)
		comRelease(item)
		return nil, fmt.Errorf("StartCapture failed: %w", err)
	}
	rect, _ := getWindowRect(hwnd)
	return &wgcCaptureEntry{
		item: item, pool: pool, session: session,
		width: size.Width, height: size.Height,
		windowLeft: rect.Left, windowTop: rect.Top,
	}, nil
}

// forgetWGCWindow drops cached capture state for one window.
func forgetWGCWindow(hwnd windows.HWND) {
	wgcEntriesMu.Lock()
	defer wgcEntriesMu.Unlock()
	if entry, ok := wgcEntries[int64(hwnd)]; ok {
		releaseWGCCaptureEntry(entry)
		delete(wgcEntries, int64(hwnd))
	}
}

func cleanupStaleWGCWindows() {
	wgcEntriesMu.Lock()
	defer wgcEntriesMu.Unlock()
	for key := range wgcEntries {
		if !windows.IsWindow(windows.HWND(key)) {
			releaseWGCCaptureEntry(wgcEntries[key])
			delete(wgcEntries, key)
		}
	}
}

// captureWGCWindow mirrors OCUCapture.CaptureWindow: returns BGRA pixels and
// the content size, working while the window is occluded. A frame-pool that
// stops delivering after a window move is dropped and rebuilt once before
// giving up (a free-threaded pool can go silent on geometry churn).
func captureWGCWindow(hwnd windows.HWND) ([]byte, int, int, error) {
	if !windows.IsWindow(hwnd) {
		return nil, 0, 0, errors.New("WGC: window is gone")
	}
	cleanupStaleWGCWindows()

	wgcCaptureMu.Lock()
	defer wgcCaptureMu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		wgcEntriesMu.Lock()
		entry, ok := wgcEntries[int64(hwnd)]
		if !ok {
			var err error
			entry, err = createWGCCaptureEntry(hwnd)
			if err != nil {
				wgcEntriesMu.Unlock()
				return nil, 0, 0, err
			}
			wgcEntries[int64(hwnd)] = entry
		}
		wgcEntriesMu.Unlock()

		var frame unsafe.Pointer
		for i := 0; i < 20; i++ {
			hr, _, _ := vtableCall(entry.pool, slotPoolTryGetNextFrame, uintptr(unsafe.Pointer(&frame)))
			if int32(hr) >= 0 && frame != nil {
				break
			}
			frame = nil
			sleepMs(25)
		}
		if frame == nil {
			if attempt == 0 {
				// Stale pool: drop the cached entry and rebuild once.
				wgcEntriesMu.Lock()
				delete(wgcEntries, int64(hwnd))
				wgcEntriesMu.Unlock()
				releaseWGCCaptureEntry(entry)
				continue
			}
			return nil, 0, 0, errors.New("WGC: no frame arrived")
		}
		var content sizeInt32
		if err := comCall(frame, slotFrameGetContentSize, uintptr(unsafe.Pointer(&content))); err != nil {
			comRelease(frame)
			return nil, 0, 0, err
		}
		if content.Width != entry.width || content.Height != entry.height {
			// Window resized since the pool was created: rebuild once.
			comRelease(frame)
			wgcEntriesMu.Lock()
			delete(wgcEntries, int64(hwnd))
			wgcEntriesMu.Unlock()
			releaseWGCCaptureEntry(entry)
			var err error
			entry, err = createWGCCaptureEntry(hwnd)
			if err != nil {
				return nil, 0, 0, err
			}
			wgcEntriesMu.Lock()
			wgcEntries[int64(hwnd)] = entry
			wgcEntriesMu.Unlock()
			if attempt > 0 {
				return nil, 0, 0, errors.New("WGC: frame never matched the window size")
			}
			continue
		}
		pixels, err := wgcFramePixels(frame, content)
		comRelease(frame)
		if err != nil {
			return nil, 0, 0, err
		}
		return pixels, int(content.Width), int(content.Height), nil
	}
	return nil, 0, 0, errors.New("WGC: no frame arrived")
}

// wgcFramePixels mirrors FrameToBitmap: staging texture copy + Map read.
func wgcFramePixels(frame unsafe.Pointer, content sizeInt32) ([]byte, error) {
	var surface unsafe.Pointer
	if err := comCall(frame, slotFrameGetSurface, uintptr(unsafe.Pointer(&surface))); err != nil {
		return nil, err
	}
	defer comRelease(surface)
	access, err := comQueryInterface(surface, "A9B3D012-3DF2-4EE3-B8D1-8695F457D3C1")
	if err != nil {
		return nil, err
	}
	defer comRelease(access)
	texIID, err := clsidFromString(iidID3D11Texture2D)
	if err != nil {
		return nil, err
	}
	var texture unsafe.Pointer
	if err := comCall(access, slotDxgiAccessGetInterface,
		uintptr(unsafe.Pointer(&texIID)), uintptr(unsafe.Pointer(&texture))); err != nil {
		return nil, err
	}
	defer comRelease(texture)

	var desc texture2DDesc
	if err := comCall(texture, slotTextureGetDesc, uintptr(unsafe.Pointer(&desc))); err != nil {
		return nil, err
	}

	stagingDesc := texture2DDesc{
		Width: desc.Width, Height: desc.Height,
		MipLevels: 1, ArraySize: 1, Format: desc.Format,
		SampleCount: 1,
		Usage:       3 /*STAGING*/, CPUAccessFlags: 0x20000, /*READ*/
	}
	device, err := comQueryInterface(wgcDevices.d3dDevice, iidID3D11Device)
	if err != nil {
		return nil, err
	}
	defer comRelease(device)
	var staging unsafe.Pointer
	if err := comCall(device, slotDeviceCreateTexture2D,
		uintptr(unsafe.Pointer(&stagingDesc)), 0, uintptr(unsafe.Pointer(&staging))); err != nil {
		return nil, err
	}
	defer comRelease(staging)

	width, height := int(desc.Width), int(desc.Height)
	// The pool texture can be larger than the window content; copy row by
	// row using the content width and keep the content size.
	copyWidth := minInt(width, int(content.Width))
	copyHeight := minInt(height, int(content.Height))
	pixels := make([]byte, int(content.Width)*int(content.Height)*4)
	if err := comCall(wgcDevices.context, slotCtxCopyResource, uintptr(staging), uintptr(texture)); err != nil {
		return nil, err
	}
	var mapped mappedSubresource
	if err := comCall(wgcDevices.context, slotCtxMap,
		uintptr(staging), 0, 1 /*READ*/, 0, uintptr(unsafe.Pointer(&mapped))); err != nil {
		return nil, err
	}
	defer comCall(wgcDevices.context, slotCtxUnmap, uintptr(staging), 0)
	if mapped.pData == nil || mapped.rowPitch == 0 || int(mapped.rowPitch) < copyWidth*4 {
		// A geometry-churning window can hand back a degenerate map; treat it
		// as a capture failure so the chain falls back to print/gdi instead of
		// reading through a garbage pointer.
		return nil, errors.New("WGC: frame map is unusable")
	}
	source := (*[1 << 30]byte)(mapped.pData)
	for y := 0; y < copyHeight; y++ {
		row := source[y*int(mapped.rowPitch) : y*int(mapped.rowPitch)+copyWidth*4]
		dest := pixels[y*int(content.Width)*4 : (y*int(content.Width)+copyWidth)*4]
		copy(dest, row)
	}
	return pixels, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// captureWindowPngDirect mirrors Capture-WindowPngBase64: the wgc -> print
// -> gdi chain with OPEN_COMPUTER_USE_WINDOWS_CAPTURE forced-mode handling.
// Returns "" when auto mode exhausts every backend (image omitted).
//
// CAUTION: PrintWindow and WGC on this DWM/driver stack asynchronously stomp
// process memory when a UIA client has walked the same window (heap-layout
// sensitive; see history 2026-08-22). Production captures therefore run in
// a sacrificial child process via nativeCaptureWindowPng; this direct entry
// is only for that child (and forced diagnostics).
func captureWindowPngDirect(req psRequest, hwnd windows.HWND, bounds *frame) (string, error) {
	mode := ""
	if req.EnvFlags != nil {
		if raw, ok := req.EnvFlags["OPEN_COMPUTER_USE_WINDOWS_CAPTURE"]; ok {
			mode = strings.ToLower(strings.TrimSpace(raw))
		}
	}
	if mode != "wgc" && mode != "print" && mode != "gdi" {
		mode = "auto"
	}
	if bounds == nil || bounds.Width <= 0 || bounds.Height <= 0 {
		if mode == "gdi" {
			return "", nil
		}
		// wgc/print derive the size from the window itself; only the GDI
		// path needs valid screen bounds.
		bounds = nil
	}

	var chain []string
	if mode == "auto" {
		chain = []string{"wgc", "print", "gdi"}
	} else {
		chain = []string{mode}
	}
	var failures []string

	for _, backend := range chain {
		switch backend {
		case "wgc":
			if hwnd == 0 {
				failures = append(failures, "wgc: wgc requires a window handle")
				continue
			}
			pixels, width, height, err := captureWGCWindow(hwnd)
			if err == nil && isBlankPixels(pixels, width, height) {
				err = errors.New("wgc produced an all-black frame")
			}
			if err == nil {
				encoded, encodeErr := pngBase64FromBGRA(pixels, width, height)
				if encodeErr == nil {
					return encoded, nil
				}
				err = encodeErr
			}
			failures = append(failures, "wgc: "+err.Error())
		case "print":
			if hwnd == 0 {
				failures = append(failures, "print: print requires a window handle")
				continue
			}
			pixels, width, height, err := capturePrintWindowPixels(hwnd)
			if err == nil && isBlankPixels(pixels, width, height) {
				err = errors.New("print produced an all-black frame")
			}
			if err == nil {
				encoded, encodeErr := pngBase64FromBGRA(pixels, width, height)
				if encodeErr == nil {
					return encoded, nil
				}
				err = encodeErr
			}
			failures = append(failures, "print: "+err.Error())
		case "gdi":
			if bounds == nil {
				failures = append(failures, "gdi: gdi requires window bounds")
				continue
			}
			pixels, width, height, err := captureGDIRegion(bounds)
			if err == nil {
				encoded, encodeErr := pngBase64FromBGRA(pixels, width, height)
				if encodeErr == nil {
					return encoded, nil
				}
				err = encodeErr
			}
			failures = append(failures, "gdi: "+err.Error())
		}
	}

	if mode != "auto" {
		return "", fmt.Errorf("OPEN_COMPUTER_USE_WINDOWS_CAPTURE=%s failed: %s", mode, strings.Join(failures, "; "))
	}
	// auto exhausted every backend; omit the image rather than failing the op.
	return "", nil
}

// windowsBuildNumber reports the OS build number via ntdll RtlGetVersion
// (Environment.OSVersion lies without a manifest).
type osVersionInfoEx struct {
	size        uint32
	major       uint32
	minor       uint32
	build       uint32
	platform    uint32
	csd         [128]uint16
	spMajor     uint16
	spMinor     uint16
	suiteMask   uint16
	productType uint8
	reserved    uint8
}

var procRtlGetVersion = windows.NewLazySystemDLL("ntdll.dll").NewProc("RtlGetVersion")

func windowsBuildNumber() uint32 {
	info := osVersionInfoEx{size: uint32(unsafe.Sizeof(osVersionInfoEx{}))}
	if hr, _, _ := procRtlGetVersion.Call(uintptr(unsafe.Pointer(&info))); int32(hr) < 0 {
		return 0
	}
	return info.build
}

// runningUnderGoTest reports whether this process is a `go test` binary
// (os.Args[0] like "app.test" / "app.test.exe", or test flags registered).
// Re-executing such a binary with capture args would run the whole test suite
// instead of the capture subcommand, so callers must fall back to the
// in-process chain.
func runningUnderGoTest() bool {
	return testBinaryMode() || flag.Lookup("test.v") != nil
}

// nativeCaptureWindowPng runs the screenshot chain in a child instance of
// this same executable ("opencomputerusewindows.exe capture ..."). The child
// performs the WGC/PrintWindow work and exits immediately, so the driver's
// memory-stomping failure mode lands in a sacrificial process instead of the
// long-lived MCP/CLI process. Falls back to the in-process chain when the
// executable cannot be located or when running under `go test` (test binaries
// do not understand the capture subcommand; screenshot-related tests exercise
// the in-process chain directly).
func nativeCaptureWindowPng(req psRequest, hwnd windows.HWND, bounds *frame) (string, error) {
	if runningUnderGoTest() {
		return captureWindowPngDirect(req, hwnd, bounds)
	}
	self, err := os.Executable()
	if err != nil {
		return captureWindowPngDirect(req, hwnd, bounds)
	}
	mode := "auto"
	if req.EnvFlags != nil {
		if raw, ok := req.EnvFlags["OPEN_COMPUTER_USE_WINDOWS_CAPTURE"]; ok {
			trimmed := strings.ToLower(strings.TrimSpace(raw))
			if trimmed == "wgc" || trimmed == "print" || trimmed == "gdi" {
				mode = trimmed
			}
		}
	}
	args := []string{"capture", "--hwnd", strconv.FormatInt(int64(hwnd), 10), "--mode", mode}
	if bounds != nil {
		args = append(args,
			"--bounds",
			strconv.FormatInt(int64(math.Round(bounds.X)), 10),
			strconv.FormatInt(int64(math.Round(bounds.Y)), 10),
			strconv.FormatInt(int64(math.Round(bounds.Width)), 10),
			strconv.FormatInt(int64(math.Round(bounds.Height)), 10))
	}
	cmd := exec.Command(self, args...)
	cmd.Stderr = nil
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		return captureWindowPngDirect(req, hwnd, bounds)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return "", errors.New("capture: child timed out")
	}
	output := strings.TrimRight(stdout.String(), "\r\n")
	if strings.HasPrefix(output, "ERR:") {
		return "", errors.New(strings.TrimPrefix(output, "ERR:"))
	}
	if output == "" {
		return "", nil // auto mode exhausted every backend: omit the image
	}
	return output, nil
}
