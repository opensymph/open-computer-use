package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestResolveDisplay(t *testing.T) {
	if got := resolveDisplay(":1", ":0"); got != ":1" {
		t.Fatalf("explicit flag should win, got %q", got)
	}
	if got := resolveDisplay("", ":2"); got != ":2" {
		t.Fatalf("env should be used when no flag, got %q", got)
	}
	if got := resolveDisplay("  ", "  "); got != ":0" {
		t.Fatalf("blank flag and env should fall back to :0, got %q", got)
	}
}

func TestMouseButtonNumber(t *testing.T) {
	cases := map[string]int{
		"": 1, "left": 1, "L": 1, "1": 1,
		"middle": 2, "m": 2, "2": 2,
		"right": 3, "R": 3, "3": 3,
		"4": 4,
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

func TestScrollButton(t *testing.T) {
	for dir, want := range map[string]int{"up": 4, "down": 5, "left": 6, "right": 7} {
		got, ok := scrollButton(dir)
		if !ok || got != want {
			t.Fatalf("scrollButton(%q) = %d, %v; want %d", dir, got, ok, want)
		}
	}
	if _, ok := scrollButton("sideways"); ok {
		t.Fatal("expected invalid direction to be rejected")
	}
}

func TestBuildXdotoolInvocationsMove(t *testing.T) {
	got, err := buildXdotoolInvocations("move", []string{"100", "200"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"mousemove", "--sync", "--", "100", "200"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("move = %v, want %v", got, want)
	}
	if _, err := buildXdotoolInvocations("move", []string{"x", "2"}); err == nil {
		t.Fatal("expected non-integer coordinates to be rejected")
	}
}

func TestBuildXdotoolInvocationsClick(t *testing.T) {
	got, err := buildXdotoolInvocations("click", []string{"--button", "right", "--count", "2", "--x", "5", "--y", "6"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"mousemove", "--sync", "--", "5", "6"},
		{"click", "--repeat", "2", "--delay", "50", "3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("click = %v, want %v", got, want)
	}

	got, err = buildXdotoolInvocations("click", []string{"--modifiers", "ctrl+shift", "--x", "1", "--y", "2"})
	if err != nil {
		t.Fatal(err)
	}
	want = [][]string{
		{"keydown", "--", "ctrl"},
		{"keydown", "--", "shift"},
		{"mousemove", "--sync", "--", "1", "2"},
		{"click", "1"},
		{"keyup", "--", "shift"},
		{"keyup", "--", "ctrl"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("click+mods = %v, want %v", got, want)
	}

	got, err = buildXdotoolInvocations("click", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, [][]string{{"click", "1"}}) {
		t.Fatalf("default click = %v", got)
	}

	if _, err := buildXdotoolInvocations("click", []string{"--x", "5"}); err == nil {
		t.Fatal("expected error when only --x provided")
	}
}

func TestBuildXdotoolInvocationsDrag(t *testing.T) {
	got, err := buildXdotoolInvocations("drag", []string{"1", "2", "3", "4"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"mousemove", "--sync", "--", "1", "2"},
		{"mousedown", "1"},
		{"mousemove", "--sync", "--", "3", "4"},
		{"mouseup", "1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("drag = %v, want %v", got, want)
	}
	if _, err := buildXdotoolInvocations("drag", []string{"1", "2", "3"}); err == nil {
		t.Fatal("expected error for missing drag coordinate")
	}
}

func TestBuildXdotoolInvocationsScrollTypeKey(t *testing.T) {
	got, err := buildXdotoolInvocations("scroll", []string{"down", "--amount", "5"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, [][]string{{"click", "--repeat", "5", "5"}}) {
		t.Fatalf("scroll = %v", got)
	}

	got, err = buildXdotoolInvocations("type", []string{"hello", "world"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, [][]string{{"type", "--delay", "12", "--", "hello world"}}) {
		t.Fatalf("type = %v", got)
	}

	got, err = buildXdotoolInvocations("type", []string{"a\nb"})
	if err != nil {
		t.Fatal(err)
	}
	wantType := [][]string{
		{"type", "--delay", "12", "--", "a"},
		{"key", "--", "Return"},
		{"type", "--delay", "12", "--", "b"},
	}
	if !reflect.DeepEqual(got, wantType) {
		t.Fatalf("type newlines = %v, want %v", got, wantType)
	}

	got, err = buildXdotoolInvocations("key", []string{"ctrl+s"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, [][]string{{"key", "--", "ctrl+s"}}) {
		t.Fatalf("key = %v", got)
	}

	got, err = buildXdotoolInvocations("key", []string{"a", "--hold-ms", "100"})
	if err != nil {
		t.Fatal(err)
	}
	wantHold := [][]string{
		{"keydown", "--", "a"},
		{"__sleep_ms__", "100"},
		{"keyup", "--", "a"},
	}
	if !reflect.DeepEqual(got, wantHold) {
		t.Fatalf("key hold = %v, want %v", got, wantHold)
	}

	got, err = buildXdotoolInvocations("mouse_down", []string{"--button", "left", "--x", "9", "--y", "8"})
	if err != nil {
		t.Fatal(err)
	}
	wantDown := [][]string{
		{"mousemove", "--sync", "--", "9", "8"},
		{"mousedown", "1"},
	}
	if !reflect.DeepEqual(got, wantDown) {
		t.Fatalf("mouse_down = %v, want %v", got, wantDown)
	}

	if _, err := buildXdotoolInvocations("teleport", nil); err == nil {
		t.Fatal("expected unknown action to error")
	}
}

func TestCoordScaler(t *testing.T) {
	s := newCoordScaler(1280, 800, 1920, 1200)
	x, y := s.scaleXY(640, 400)
	if x != 960 || y != 600 {
		t.Fatalf("scaleXY = %d,%d", x, y)
	}
	if s.unscaleX(960) != 640 || s.unscaleY(600) != 400 {
		t.Fatalf("unscale failed")
	}
}

func TestExtractDisplayFlag(t *testing.T) {
	display, rest, err := extractDisplayFlag([]string{"--display", ":1", "type", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if display != ":1" || !reflect.DeepEqual(rest, []string{"type", "hi"}) {
		t.Fatalf("extractDisplayFlag = %q %v", display, rest)
	}
	if _, _, err := extractDisplayFlag([]string{"--display"}); err == nil {
		t.Fatal("expected error for dangling --display")
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

func TestBuildFfmpegRecordArgs(t *testing.T) {
	got := buildFfmpegRecordArgs(":1", "/tmp/out.mp4", recordOptions{
		fps: 60, quality: "demo", drawMouse: 0, videoSize: "1920x1200",
	})
	want := []string{
		"-nostdin", "-y",
		"-video_size", "1920x1200",
		"-framerate", "60",
		"-draw_mouse", "0",
		"-f", "x11grab",
		"-i", ":1",
		"-vf", "scale=1920:-2:flags=lanczos,fps=60",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "17",
		"-pix_fmt", "yuv420p",
		"-profile:v", "high",
		"-movflags", "+faststart",
		"-tune", "fastdecode",
		"/tmp/out.mp4",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("demo ffmpeg args = %#v, want %#v", got, want)
	}

	draft := buildFfmpegRecordArgs(":0", "x.mp4", recordOptions{fps: 0, quality: "draft", drawMouse: 1})
	if !reflect.DeepEqual(draft, []string{
		"-nostdin", "-y",
		"-framerate", "30",
		"-draw_mouse", "1",
		"-f", "x11grab",
		"-i", ":0",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-pix_fmt", "yuv420p",
		"x.mp4",
	}) {
		t.Fatalf("draft ffmpeg args = %#v", draft)
	}
}

func TestNormalizeRecordQuality(t *testing.T) {
	for _, in := range []string{"", "demo", "high", "DEMO"} {
		got, err := normalizeRecordQuality(in)
		if err != nil || got != "demo" {
			t.Fatalf("normalizeRecordQuality(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"draft", "low"} {
		got, err := normalizeRecordQuality(in)
		if err != nil || got != "draft" {
			t.Fatalf("normalizeRecordQuality(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := normalizeRecordQuality("medium"); err == nil {
		t.Fatal("expected invalid quality to fail")
	}
}

func TestRelocateRecordOutput(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "raw.mp4")
	if err := os.WriteFile(src, []byte("mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(src+".log", []byte("log"), 0o644)
	dst, err := relocateRecordOutput(src, "demo-take")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "demo-take.mp4")
	if dst != want {
		t.Fatalf("relocated path = %q, want %q", dst, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(want + ".log"); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultRecordOutput(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 7, 5, 0, time.UTC)
	got := defaultRecordOutput(now)
	if filepath.Base(got) != "open-computer-use-recording-20260824-090705.mp4" {
		t.Fatalf("defaultRecordOutput = %q", got)
	}
}

func TestRecordPidfileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec.pid")
	if _, ok := readRecordPidfile(path); ok {
		t.Fatal("missing pidfile should not be ok")
	}
	state := recordState{PID: 4321, Output: "/tmp/o.mp4", Display: ":1"}
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
