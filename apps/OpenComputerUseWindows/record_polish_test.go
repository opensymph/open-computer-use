package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildRecordEventFromInput(t *testing.T) {
	ev, ok := buildRecordEventFromInput("move", []string{"10", "20"})
	if !ok || ev.Type != "move" || ev.X != 10 || ev.Y != 20 {
		t.Fatalf("move event = %#v, %v", ev, ok)
	}
	ev, ok = buildRecordEventFromInput("click", []string{"--button", "right", "--count", "2", "--x", "5", "--y", "6"})
	if !ok || ev.Button != "right" || ev.Count != 2 || ev.X != 5 || ev.Y != 6 {
		t.Fatalf("click event = %#v, %v", ev, ok)
	}
	ev, ok = buildRecordEventFromInput("type", []string{"hello", "world"})
	if !ok || ev.Text != "hello world" {
		t.Fatalf("type event = %#v, %v", ev, ok)
	}
	ev, ok = buildRecordEventFromInput("key", []string{"ctrl+s"})
	if !ok || ev.Key != "ctrl+s" {
		t.Fatalf("key event = %#v, %v", ev, ok)
	}
}

func TestBuildPolishPlanKeystrokesAndIdle(t *testing.T) {
	log := recordEventLog{
		Width:  800,
		Height: 600,
		Events: []recordEvent{
			{TMs: 500, Type: "click", X: 100, Y: 120, Count: 1, Button: "left"},
			{TMs: 800, Type: "type", Text: "hi"},
			{TMs: 5000, Type: "key", Key: "Return"},
		},
	}
	opts := defaultPolishOptions()
	segments, zooms, ass := buildPolishPlan(log, 7000, opts)
	if len(segments) < 2 {
		t.Fatalf("expected idle speedup segments, got %#v", segments)
	}
	sped := false
	for _, s := range segments {
		if s.Rate > 1.5 {
			sped = true
		}
	}
	if !sped {
		t.Fatalf("expected an idle speedup segment, got %#v", segments)
	}
	if len(zooms) == 0 {
		t.Fatal("expected at least one zoom window around the click")
	}
	if !strings.Contains(ass, "Keystroke") || !strings.Contains(ass, "hi") {
		t.Fatalf("ASS missing keystroke caption:\n%s", ass)
	}
	if !strings.Contains(ass, "Ripple") {
		t.Fatalf("ASS missing ripple:\n%s", ass)
	}
	if !strings.Contains(ass, "Cursor") {
		t.Fatalf("ASS missing cursor ghost:\n%s", ass)
	}
}

func TestFormatASSTime(t *testing.T) {
	if got := formatASSTime(3661020); got != "1:01:01.02" {
		t.Fatalf("formatASSTime = %q", got)
	}
}

func TestKeyDisplayLabel(t *testing.T) {
	if got := keyDisplayLabel("ctrl+Return"); got != "ctrl + ↵ Enter" && got != "CTRL + ↵ Enter" {
		// ctrl stays as typed token unless mapped; ensure Return mapped.
		if !strings.Contains(got, "↵ Enter") {
			t.Fatalf("keyDisplayLabel = %q", got)
		}
	}
}

func TestBuildPolishFilterComplexSingleSegment(t *testing.T) {
	filter, err := buildPolishFilterComplex(
		[]polishSegment{{StartMs: 0, EndMs: 1000, Rate: 1}},
		nil,
		"/tmp/x.ass",
		1920, 1080,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filter, "ass=filename=") || !strings.Contains(filter, "[outv]") {
		t.Fatalf("filter = %s", filter)
	}
}

func TestDefaultPolishedOutput(t *testing.T) {
	got := defaultPolishedOutput("/tmp/a.mp4")
	want := "/tmp/a.polished.mp4"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMergePolishSegments(t *testing.T) {
	got := mergePolishSegments([]polishSegment{
		{0, 100, 1},
		{100, 200, 1},
		{200, 500, 3},
	})
	want := []polishSegment{{0, 200, 1}, {200, 500, 3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}
