//go:build windows

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"unsafe"
)

// TestINPUTStructMatchesX64ABIPins pins the INPUT ABI that SendInput's cbSize
// and the union layout depend on (winuser.h: type DWORD + 8-byte-aligned
// union; MOUSEINPUT is the 32-byte largest member; KEYBDINPUT is 24 bytes).
func TestINPUTStructMatchesX64ABIPins(t *testing.T) {
	if unsafe.Sizeof(tagINPUT{}) != 40 {
		t.Fatalf("sizeof(INPUT) = %d, want 40 (x64 cbSize contract)", unsafe.Sizeof(tagINPUT{}))
	}
	if unsafe.Offsetof(tagINPUT{}.mi) != 8 {
		t.Fatalf("union offset = %d, want 8 (ULONG_PTR alignment after type DWORD)", unsafe.Offsetof(tagINPUT{}.mi))
	}
	if unsafe.Sizeof(mouseInput{}) != 32 {
		t.Fatalf("sizeof(MOUSEINPUT) = %d, want 32", unsafe.Sizeof(mouseInput{}))
	}
	if unsafe.Sizeof(keybdInput{}) != 24 {
		t.Fatalf("sizeof(KEYBDINPUT) = %d, want 24", unsafe.Sizeof(keybdInput{}))
	}
}

// cKeybdInput mirrors the C KEYBDINPUT placement inside the INPUT union so
// keyEvent's packing can be verified against the bytes SendInput will read.
type cKeybdInput struct {
	wVk, wScan  uint16
	dwFlags     uint32
	time        uint32
	pad         uint32
	dwExtraInfo uintptr
}

// TestKeyEventPacksKeybdInputUnionFields verifies wVk/wScan land on the
// union's first 4 bytes (mi.dx) and dwFlags on mi.dy — the previous packing
// wrote wScan into dwFlags and lost the flags entirely, silently breaking
// every global press_key/type_text chord.
func TestKeyEventPacksKeybdInputUnionFields(t *testing.T) {
	input := keyEvent(0x41, 0x1E, keyeventfScancode)
	ki := (*cKeybdInput)(unsafe.Pointer(&input.mi))
	if ki.wVk != 0x41 {
		t.Fatalf("wVk = %#x, want 0x41", ki.wVk)
	}
	if ki.wScan != 0x1E {
		t.Fatalf("wScan = %#x, want 0x1e", ki.wScan)
	}
	if ki.dwFlags != keyeventfScancode {
		t.Fatalf("dwFlags = %#x, want %#x", ki.dwFlags, keyeventfScancode)
	}
	if ki.time != 0 || ki.dwExtraInfo != 0 {
		t.Fatalf("time/dwExtraInfo = %#x/%#x, want zero", ki.time, ki.dwExtraInfo)
	}

	// KEYEVENTF_UNICODE path: wVk must be 0 and wScan carries the UTF-16 unit.
	input = keyEvent(0, 0x0100, keyeventfUnicode)
	ki = (*cKeybdInput)(unsafe.Pointer(&input.mi))
	if ki.wVk != 0 || ki.wScan != 0x0100 || ki.dwFlags != keyeventfUnicode {
		t.Fatalf("unicode event = wVk:%#x wScan:%#x dwFlags:%#x", ki.wVk, ki.wScan, ki.dwFlags)
	}

	// High scan codes (surrogate units reach 0xFFFF) must not smear into
	// neighbouring fields once packed into dx.
	input = keyEvent(0, 0xFFFF, keyeventfUnicode|keyeventfKeyUp)
	ki = (*cKeybdInput)(unsafe.Pointer(&input.mi))
	if ki.wVk != 0 || ki.wScan != 0xFFFF || ki.dwFlags != keyeventfUnicode|keyeventfKeyUp {
		t.Fatalf("surrogate event = wVk:%#x wScan:%#x dwFlags:%#x", ki.wVk, ki.wScan, ki.dwFlags)
	}
}

// TestOleVariantMatchesX64VARIANT pins the VARIANT size COM callees copy
// (go-ole variant_amd64.go: vt + 3 reserved WORDs, 16-byte payload union at
// offset 8, total 24). A 16-byte struct gets 8 bytes written past its end.
func TestOleVariantMatchesX64VARIANT(t *testing.T) {
	if unsafe.Sizeof(oleVariant{}) != 24 {
		t.Fatalf("sizeof(VARIANT) = %d, want 24 (x64 VARIANT)", unsafe.Sizeof(oleVariant{}))
	}
	if unsafe.Offsetof(oleVariant{}.value) != 8 {
		t.Fatalf("payload offset = %d, want 8 (union aligned to pointer size)", unsafe.Offsetof(oleVariant{}.value))
	}
	if unsafe.Sizeof(oleVariant{}.value) != 16 {
		t.Fatalf("payload size = %d, want 16 ({pvRecord, pRecInfo} pair)", unsafe.Sizeof(oleVariant{}.value))
	}
}

// TestNormalizeVirtualScreenMultiMonitor covers normalization against the
// virtual desktop, including the negative origin produced by monitors placed
// left of the primary (SM_XVIRTUALSCREEN can be negative).
func TestNormalizeVirtualScreenMultiMonitor(t *testing.T) {
	// Two monitors: primary 1920x1080 at (0,0), secondary 3840x2160 at
	// (-3840, 0). Virtual screen: origin (-3840, 0), extent (5760, 2160).
	const origin, extent = -3840, 5760
	if got := normalizeVirtualScreen(-3840, origin, extent); got != 0 {
		t.Fatalf("left edge = %d, want 0", got)
	}
	// Primary origin (0) sits two thirds into this virtual desktop.
	if got := normalizeVirtualScreen(0, origin, extent); got != 43690 {
		t.Fatalf("primary origin = %d, want 43690", got)
	}
	// Primary right edge = virtual-desktop right edge.
	if got := normalizeVirtualScreen(1919, origin, extent); got != 65523 {
		t.Fatalf("primary right edge = %d, want 65523", got)
	}
	if got := normalizeVirtualScreen(0, 0, 5760); got != 0 {
		t.Fatalf("zero origin = %d, want 0", got)
	}
	// The old primary-screen normalization mapped the same coordinate to
	// 65535*(1919/1920) — off-monitor on any multi-monitor layout.
	if normalizeVirtualScreen(0, origin, extent) == normalizeVirtualScreen(0, 0, 1920) {
		t.Fatalf("virtual normalization must differ from primary-only for negative origins")
	}
	if got := normalizeVirtualScreen(100, 0, 0); got != 0 {
		t.Fatalf("degenerate extent = %d, want 0", got)
	}
}

func TestClampClickCountBounds(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want int
	}{{-3, 1}, {0, 1}, {1, 1}, {2, 2}, {100, 100}, {101, 100}, {1000000, 100}} {
		if got := clampClickCount(tc.in); got != tc.want {
			t.Fatalf("clampClickCount(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestClampScrollPagesBounds(t *testing.T) {
	if got := clampScrollPages(0.5); got != 0.5 {
		t.Fatalf("fractional pages changed: %v", got)
	}
	if got := clampScrollPages(1001); got != 1000 {
		t.Fatalf("pages = %v, want 1000", got)
	}
	if got := clampScrollPages(1e9); got != 1000 {
		t.Fatalf("pages = %v, want 1000", got)
	}
}

// TestRunMCPRecoversAfterMalformedLine pins the L2 fix: a bad JSON-RPC line
// must yield one -32700 error response and the server must keep serving the
// following valid requests (the streaming json.Decoder loop could never
// consume the bad bytes and spun at 100% CPU forever).
func TestRunMCPRecoversAfterMalformedLine(t *testing.T) {
	input := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\" TRAILING\n" +
		"\n" +
		"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"ping\"}\n"
	var stdout strings.Builder
	if err := runMCP(strings.NewReader(input), &stdout); err != nil {
		t.Fatalf("runMCP = %v", err)
	}
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("response lines = %d, want 2: %q", len(lines), stdout.String())
	}
	var errorResponse map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &errorResponse); err != nil {
		t.Fatalf("first line not JSON: %v", err)
	}
	if code := errorResponse["error"].(map[string]any)["code"]; code != float64(-32700) {
		t.Fatalf("first response code = %v, want -32700", code)
	}
	var pingResponse map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &pingResponse); err != nil {
		t.Fatalf("second line not JSON: %v", err)
	}
	if _, ok := pingResponse["result"]; !ok {
		t.Fatalf("ping response missing result: %q", lines[1])
	}
	if pingResponse["id"] != float64(2) {
		t.Fatalf("ping response id = %v, want 2", pingResponse["id"])
	}
}

// TestSnapshotCacheStripsScreenshotAndBoundsSize pins the P1 cache fix:
// cached snapshots keep elements/bounds for element_index resolution but drop
// the base64 PNG, and the cache never grows past maxCachedSnapshots keys.
func TestSnapshotCacheStripsScreenshotAndBoundsSize(t *testing.T) {
	svc := newService()
	snapshot := &appSnapshot{
		App:                 appDescriptor{Name: "App", PID: 1234},
		ScreenshotPNGBase64: "aGVsbG8=",
		WindowBounds:        &frame{X: 10, Y: 20, Width: 100, Height: 50},
		Elements:            []elementRecord{{Index: 0, Name: "btn"}},
	}
	svc.rememberSnapshot("query", snapshot)
	if cached := svc.snapshots["app"]; cached == nil || cached.ScreenshotPNGBase64 != "" {
		t.Fatalf("cached copy keeps screenshot: %+v", cached)
	}
	if cached := svc.snapshots["query"]; cached == nil || cached.Elements[0].Name != "btn" || cached.WindowBounds == nil {
		t.Fatalf("cached copy lost elements/bounds: %+v", cached)
	}
	if snapshot.ScreenshotPNGBase64 == "" {
		t.Fatalf("caller-visible snapshot must keep its screenshot")
	}

	for i := 0; i < maxCachedSnapshots+10; i++ {
		svc.cacheSnapshot(windowKey(int64(1000+i)), snapshot)
	}
	if len(svc.snapshots) > maxCachedSnapshots {
		t.Fatalf("cache holds %d entries, want <= %d", len(svc.snapshots), maxCachedSnapshots)
	}
	latest := windowKey(int64(1000 + maxCachedSnapshots + 9))
	if _, ok := svc.snapshots[latest]; !ok {
		t.Fatalf("most recent window entry evicted: %s missing", latest)
	}
}
