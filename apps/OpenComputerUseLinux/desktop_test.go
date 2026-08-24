package main

import (
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
	want := [][]string{{"mousemove", "--", "100", "200"}}
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
		{"mousemove", "--", "5", "6"},
		{"click", "--repeat", "2", "3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("click = %v, want %v", got, want)
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
		{"mousemove", "--", "1", "2"},
		{"mousedown", "1"},
		{"mousemove", "--", "3", "4"},
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
	if !reflect.DeepEqual(got, [][]string{{"type", "--", "hello world"}}) {
		t.Fatalf("type = %v", got)
	}

	got, err = buildXdotoolInvocations("key", []string{"ctrl+s"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, [][]string{{"key", "--", "ctrl+s"}}) {
		t.Fatalf("key = %v", got)
	}

	if _, err := buildXdotoolInvocations("teleport", nil); err == nil {
		t.Fatal("expected unknown action to error")
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
	got := buildFfmpegRecordArgs(":1", "/tmp/out.mp4", 60)
	want := []string{
		"-nostdin", "-y", "-f", "x11grab", "-draw_mouse", "1",
		"-framerate", "60", "-i", ":1",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"/tmp/out.mp4",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ffmpeg args = %v, want %v", got, want)
	}
	if got := buildFfmpegRecordArgs(":0", "x.mp4", 0); got[7] != "30" {
		t.Fatalf("fps<=0 should default to 30, got framerate %q", got[7])
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
