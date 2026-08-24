package main

import (
	"image/color"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMouseButtonNumber(t *testing.T) {
	cases := map[string]int{
		"": 1, "left": 1, "L": 1, "1": 1,
		"middle": 2, "m": 2, "2": 2,
		"right": 3, "R": 3, "3": 3,
	}
	for input, want := range cases {
		got, err := mouseButtonNumber(input)
		if err != nil || got != want {
			t.Fatalf("mouseButtonNumber(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	if _, err := mouseButtonNumber("purple"); err == nil {
		t.Fatal("expected error for invalid button")
	}
}

func TestScrollNotches(t *testing.T) {
	cases := map[string][2]int{
		"up":    {1, 0},
		"down":  {-1, 0},
		"left":  {0, -1},
		"right": {0, 1},
	}
	for direction, want := range cases {
		dy, dx, ok := scrollNotches(direction)
		if !ok || dy != want[0] || dx != want[1] {
			t.Fatalf("scrollNotches(%q) = %d, %d, %v; want %v", direction, dy, dx, ok, want)
		}
	}
	if _, _, ok := scrollNotches("sideways"); ok {
		t.Fatal("expected invalid direction to be rejected")
	}
}

func opEqual(got, want inputOp) bool {
	return reflect.DeepEqual(got, want)
}

func TestBuildInputOpsMove(t *testing.T) {
	got, err := buildInputOps("move", []string{"100", "200"})
	if err != nil {
		t.Fatal(err)
	}
	if !opEqual(got[0], inputOp{kind: "move", x: 100, y: 200}) || len(got) != 1 {
		t.Fatalf("move = %#v", got)
	}
	if _, err := buildInputOps("move", []string{"x", "2"}); err == nil {
		t.Fatal("expected non-integer coordinates to be rejected")
	}
}

func TestBuildInputOpsClick(t *testing.T) {
	got, err := buildInputOps("click", []string{"--button", "right", "--count", "2", "--x", "5", "--y", "6"})
	if err != nil {
		t.Fatal(err)
	}
	want := []inputOp{
		{kind: "move", x: 5, y: 6},
		{kind: "click", button: 3, count: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("click = %#v, want %#v", got, want)
	}

	got, err = buildInputOps("click", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []inputOp{{kind: "click", button: 1, count: 1}}) {
		t.Fatalf("default click = %#v", got)
	}

	if _, err := buildInputOps("click", []string{"--x", "5"}); err == nil {
		t.Fatal("expected error when only --x provided")
	}
}

func TestBuildInputOpsDrag(t *testing.T) {
	got, err := buildInputOps("drag", []string{"1", "2", "3", "4", "--button", "right"})
	if err != nil {
		t.Fatal(err)
	}
	want := []inputOp{{kind: "drag", x: 1, y: 2, toX: 3, toY: 4, button: 3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("drag = %#v, want %#v", got, want)
	}
	if _, err := buildInputOps("drag", []string{"1", "2", "3"}); err == nil {
		t.Fatal("expected error for missing drag coordinate")
	}
}

func TestBuildInputOpsScrollTypeKey(t *testing.T) {
	got, err := buildInputOps("scroll", []string{"down", "--amount", "5"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []inputOp{{kind: "scroll", dy: -5, dx: 0}}) {
		t.Fatalf("scroll = %#v", got)
	}
	got, err = buildInputOps("scroll", []string{"right"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []inputOp{{kind: "scroll", dy: 0, dx: 3}}) {
		t.Fatalf("scroll default amount = %#v", got)
	}

	got, err = buildInputOps("type", []string{"hello", "world"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []inputOp{{kind: "type", text: "hello world"}}) {
		t.Fatalf("type = %#v", got)
	}

	got, err = buildInputOps("key", []string{"ctrl+s"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []inputOp{{kind: "key", key: "ctrl+s"}}) {
		t.Fatalf("key = %#v", got)
	}

	// The Windows/Meta deny applies to the display-level key path too.
	if _, err := buildInputOps("key", []string{"win+r"}); err == nil {
		t.Fatal("expected Windows/Meta chord to be denied")
	}

	if _, err := buildInputOps("teleport", nil); err == nil {
		t.Fatal("expected unknown action to error")
	}
}

func TestParseWaitDuration(t *testing.T) {
	d, err := parseWaitDuration([]string{"1.5"})
	if err != nil || d != 1500*time.Millisecond {
		t.Fatalf("parseWaitDuration = %v, %v", d, err)
	}
	if _, err := parseWaitDuration([]string{"-1"}); err == nil {
		t.Fatal("expected negative duration to be rejected")
	}
	if _, err := parseWaitDuration(nil); err == nil {
		t.Fatal("expected missing duration to be rejected")
	}
}

func TestRunInputCommandGate(t *testing.T) {
	// Without the opt-in flag the command must refuse before any input fires.
	t.Setenv(foregroundInputFlag, "")
	var out strings.Builder
	err := runInputCommand([]string{"move", "1", "2"}, &out)
	if err == nil || !strings.Contains(err.Error(), foregroundInputFlag) {
		t.Fatalf("expected gate error mentioning %s, got %v", foregroundInputFlag, err)
	}
	if out.Len() != 0 {
		t.Fatalf("gated input must not print, got %q", out.String())
	}
	// wait is ungated and must not hit the gate.
	out.Reset()
	if err := runInputCommand([]string{"wait", "0.01"}, &out); err != nil || !strings.Contains(out.String(), "waited") {
		t.Fatalf("wait = %v, %q", err, out.String())
	}
}

func TestBuildFfmpegRecordArgs(t *testing.T) {
	got := buildFfmpegRecordArgs("C:\\tmp\\out.mp4", 60)
	want := []string{
		"-nostdin", "-y", "-f", "gdigrab",
		"-framerate", "60", "-i", "desktop",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"C:\\tmp\\out.mp4",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ffmpeg args = %v, want %v", got, want)
	}
	if got := buildFfmpegRecordArgs("x.mp4", 0); got[5] != "30" {
		t.Fatalf("fps<=0 should default to 30, got framerate %q", got[5])
	}
}

func TestDefaultRecordOutput(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 7, 5, 0, time.UTC)
	got := defaultRecordOutput(now)
	if filepath.Base(got) != "open-computer-use-recording-20260824-090705.mp4" {
		t.Fatalf("defaultRecordOutput = %q", got)
	}
	if filepath.Base(defaultRecordPidfile()) != "open-computer-use-record.pid" {
		t.Fatalf("defaultRecordPidfile = %q", defaultRecordPidfile())
	}
}

func TestRecordPidfileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec.pid")
	if _, ok := readRecordPidfile(path); ok {
		t.Fatal("missing pidfile should not be ok")
	}
	state := recordState{PID: 4321, Output: "C:\\tmp\\o.mp4"}
	if err := writeRecordPidfile(path, state); err != nil {
		t.Fatal(err)
	}
	got, ok := readRecordPidfile(path)
	if !ok || got != state {
		t.Fatalf("roundtrip = %#v, %v", got, ok)
	}
}

func TestScreenshotHelpAndDispatch(t *testing.T) {
	// help topics for the new commands must be wired.
	for _, topic := range []string{"screenshot", "cursor-position", "input", "record"} {
		if text := helpText(topic); !strings.Contains(text, "Usage:") {
			t.Fatalf("helpText(%q) missing usage:\n%s", topic, text)
		}
	}
	// The top-level help should list the new commands.
	top := helpText("")
	for _, cmd := range []string{"screenshot", "cursor-position", "input", "record"} {
		if !strings.Contains(top, cmd) {
			t.Fatalf("top-level help missing %q", cmd)
		}
	}
}

func TestBGRAToNRGBA(t *testing.T) {
	// 1x2 pixels: BGRA little-endian byte order (B,G,R,A).
	pixels := []byte{
		0x11, 0x22, 0x33, 0xff,
		0xAA, 0xBB, 0xCC, 0xff,
	}
	img := bgraToNRGBA(pixels, 1, 2)
	r, g, b := rgb8(img.At(0, 0))
	if r != 0x33 || g != 0x22 || b != 0x11 {
		t.Fatalf("pixel(0,0) = %#x %#x %#x", r, g, b)
	}
	r, g, b = rgb8(img.At(0, 1))
	if r != 0xCC || g != 0xBB || b != 0xAA {
		t.Fatalf("pixel(0,1) = %#x %#x %#x", r, g, b)
	}
}

func rgb8(c color.Color) (r, g, b uint8) {
	r32, g32, b32, _ := c.RGBA()
	return uint8(r32 >> 8), uint8(g32 >> 8), uint8(b32 >> 8)
}
