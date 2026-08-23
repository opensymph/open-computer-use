package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSendKeyRejectsBadMainKeyWithNoEvents pins the L1 fix: an unparseable
// main key must fail before any modifier is pressed. The old code pressed
// modifiers first, so "ctrl+bogus" left Ctrl stuck down after the error.
func TestSendKeyRejectsBadMainKeyWithNoEvents(t *testing.T) {
	fr := newFakeRuntime(editorFixture())
	resp := performOperation(fr.rt, &linuxRequest{Tool: "press_key", App: "Text Editor", Key: "ctrl+bogus"})
	if resp.OK {
		t.Fatalf("ctrl+bogus unexpectedly succeeded")
	}
	if len(fr.keyEvents) != 0 {
		t.Fatalf("key events = %v, want none (ctrl must never be pressed)", fr.keyEvents)
	}

	// A valid chord still round-trips modifiers down → key → modifiers up.
	fr = newFakeRuntime(editorFixture())
	resp = performOperation(fr.rt, &linuxRequest{Tool: "press_key", App: "Text Editor", Key: "ctrl+a"})
	want := []recordedKeyEvent{
		{0xffe3, "", atspiKeyPress},
		{0, "a", atspiKeyString},
		{0xffe3, "", atspiKeyRelease},
	}
	if !resp.OK || strings.Compare(fmtKeyEvents(fr.keyEvents), fmtKeyEvents(want)) != 0 {
		t.Fatalf("ctrl+a = %v (ok=%v)", fr.keyEvents, resp.OK)
	}
}

func fmtKeyEvents(events []recordedKeyEvent) string {
	encoded, _ := json.Marshal(events)
	return string(encoded)
}

// TestParseMouseButtonRejectsUnknownValues pins the P1#3 fix: the l/r/m short
// names resolve to their real buttons and anything else is an error instead
// of silently clicking the left button.
func TestParseMouseButtonRejectsUnknownValues(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"", "left"}, {"left", "left"}, {"LEFT", "left"},
		{"l", "left"}, {"r", "right"}, {"m", "middle"},
		{"right", "right"}, {"middle", "middle"},
	} {
		got, err := parseMouseButton(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("parseMouseButton(%q) = (%q, %v), want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"x", "side", "lef", "0"} {
		if got, err := parseMouseButton(bad); err == nil {
			t.Fatalf("parseMouseButton(%q) = %q, want error", bad, got)
		}
	}
	// Short names must reach the runtime as real buttons.
	if down, up := mouseButtonEvents("r"); down != "b3p" || up != "b3r" {
		t.Fatalf("r events = %s/%s, want b3p/b3r", down, up)
	}
	if down, up := mouseButtonEvents("m"); down != "b2p" || up != "b2r" {
		t.Fatalf("m events = %s/%s, want b2p/b2r", down, up)
	}
}

func TestClampBounds(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want int
	}{{-3, 1}, {0, 1}, {1, 1}, {7, 7}, {100, 100}, {101, 100}, {1e9, 100}} {
		if got := clampClickCount(tc.in); got != tc.want {
			t.Fatalf("clampClickCount(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
	if got := clampScrollPages(0.5); got != 0.5 {
		t.Fatalf("fractional pages changed: %v", got)
	}
	if got := clampScrollPages(1001); got != 1000 {
		t.Fatalf("pages = %v, want 1000", got)
	}
}

// TestSnapshotCacheStripsScreenshotAndBoundsSize pins the P1#1 fix: cached
// snapshots keep elements/bounds but drop the base64 PNG, and the cache never
// grows past maxCachedSnapshots keys.
func TestSnapshotCacheStripsScreenshotAndBoundsSize(t *testing.T) {
	svc := newService()
	snapshot := &appSnapshot{
		App:                 appDescriptor{Name: "Editor", PID: 4242},
		ScreenshotPNGBase64: "aGVsbG8=",
		WindowBounds:        &frame{X: 100, Y: 100, Width: 800, Height: 600},
		Elements:            []elementRecord{{Index: 0, Name: "Save"}},
	}
	svc.rememberSnapshot("query", snapshot)
	if cached := svc.snapshots["editor"]; cached == nil || cached.ScreenshotPNGBase64 != "" {
		t.Fatalf("cached copy keeps screenshot: %+v", cached)
	}
	if cached := svc.snapshots["query"]; cached == nil || cached.Elements[0].Name != "Save" || cached.WindowBounds == nil {
		t.Fatalf("cached copy lost elements/bounds: %+v", cached)
	}
	if snapshot.ScreenshotPNGBase64 == "" {
		t.Fatalf("caller-visible snapshot must keep its screenshot")
	}
	for i := 0; i < maxCachedSnapshots+6; i++ {
		svc.cacheSnapshot("app"+strings.Repeat("x", i+1), snapshot)
	}
	if len(svc.snapshots) > maxCachedSnapshots {
		t.Fatalf("cache holds %d entries, want <= %d", len(svc.snapshots), maxCachedSnapshots)
	}
}

// TestRunMCPRecoversAfterMalformedLine pins the L2 fix: a bad JSON-RPC line
// yields one -32700 response and the server keeps serving the next request
// (the streaming json.Decoder loop never consumed the bad bytes and spun at
// 100% CPU forever).
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
	if _, ok := pingResponse["result"]; !ok || pingResponse["id"] != float64(2) {
		t.Fatalf("ping response = %q", lines[1])
	}
}
