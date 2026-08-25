package main

import (
	"os"
	"path/filepath"
	"strconv"
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

func TestClickImportanceAndMultiZoom(t *testing.T) {
	events := []recordEvent{
		{TMs: 200, Type: "click", X: 400, Y: 300, Count: 1, Button: "left"},
		{TMs: 400, Type: "type", Text: "name"}, // followed by typing → high importance
		{TMs: 2500, Type: "click", X: 900, Y: 500, Count: 2, Button: "left"},
		{TMs: 6000, Type: "click", X: 100, Y: 80, Count: 1, Button: "left"}, // near edge, lower
	}
	clicks := analyzeClickEffects(events, 1920, 1200)
	if len(clicks) != 3 {
		t.Fatalf("clicks=%d", len(clicks))
	}
	if clicks[0].Score < 60 {
		t.Fatalf("first click (followed by type) should be important, score=%d", clicks[0].Score)
	}
	zooms := selectZoomWindowsFromClicks(clicks, 8000, defaultPolishOptions())
	if len(zooms) < 2 {
		t.Fatalf("expected multi-zoom windows, got %#v", zooms)
	}
}

func TestIdleClassification(t *testing.T) {
	c, speed := classifyIdlePeriod(6000, "type", "key")
	if c != idleThinkingPause || speed < 2.5 {
		t.Fatalf("thinking pause = %v speed=%v", c, speed)
	}
	c, speed = classifyIdlePeriod(1500, "click", "none")
	if c != idleLoadingWait || speed < 3.5 {
		t.Fatalf("loading wait = %v speed=%v", c, speed)
	}
	c, speed = classifyIdlePeriod(800, "screenshot", "click")
	if c != idleViewingResult || speed != 1.0 {
		t.Fatalf("viewing result = %v speed=%v", c, speed)
	}
}

func TestPolishPlanIdleAndKeystrokes(t *testing.T) {
	log := recordEventLog{
		Width:  800,
		Height: 600,
		Events: []recordEvent{
			{TMs: 500, Type: "click", X: 100, Y: 120, Count: 1, Button: "left"},
			{TMs: 700, Type: "type", Text: "hi"},
			{TMs: 5000, Type: "key", Key: "Return"},
		},
	}
	plan := buildPolishPlan(log, 7000, defaultPolishOptions())
	sped := false
	for _, s := range plan.Segments {
		if s.Rate > 1.5 {
			sped = true
		}
	}
	if !sped {
		t.Fatalf("expected idle speedup segments, got %#v", plan.Segments)
	}
	if !strings.Contains(plan.ASS, "Keystroke") || !strings.Contains(plan.ASS, "hi") {
		t.Fatalf("ASS missing keystroke caption:\n%s", plan.ASS)
	}
	// Ripples are PNG overlays now — ASS should NOT contain filled Ripple blobs.
	if strings.Contains(plan.ASS, "Ripple") {
		t.Fatalf("ASS should not use Ripple filled shapes anymore:\n%s", plan.ASS)
	}
	if len(plan.Cursor) < 10 {
		t.Fatalf("expected interpolated cursor path, got %d frames", len(plan.Cursor))
	}
}

func TestBezierEaseMonotonic(t *testing.T) {
	prev := -0.01
	for i := 0; i <= 20; i++ {
		v := bezierEase(cursorStyleMellow, float64(i)/20)
		if v < prev-1e-6 || v > 1.01 {
			t.Fatalf("ease not monotonic/bounded at i=%d v=%v prev=%v", i, v, prev)
		}
		prev = v
	}
}

func TestFormatASSTime(t *testing.T) {
	if got := formatASSTime(3661020); got != "1:01:01.02" {
		t.Fatalf("formatASSTime = %q", got)
	}
}

func TestDefaultPolishedOutput(t *testing.T) {
	got := defaultPolishedOutput("/tmp/a.mp4")
	want := "/tmp/a.polished.mp4"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestParseCursorStyle(t *testing.T) {
	s, err := parseCursorStyle("rapid")
	if err != nil || s != cursorStyleRapid {
		t.Fatalf("got %v %v", s, err)
	}
	if _, err := parseCursorStyle("nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestWriteRippleRingIsTransparentOutside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ring.png")
	if err := writeRippleRingPNG(path, 10, 2, 180); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < 100 {
		t.Fatalf("ring png missing/too small: %v %d", err, fi.Size())
	}
}

func TestFilterClickEffectsKeepsFirstClick(t *testing.T) {
	in := []clickEffect{
		{TMs: 100, X: 1, Y: 1, Count: 1},
		{TMs: 150, X: 2, Y: 2, Count: 1},
		{TMs: 400, X: 3, Y: 3, Count: 1},
	}
	out := filterClickEffects(in, 200)
	if len(out) != 2 || out[0].TMs != 100 || out[1].TMs != 400 {
		t.Fatalf("got %#v", out)
	}
}

func TestRippleFilterContainsOverlay(t *testing.T) {
	opts := defaultPolishOptions()
	opts.IdleSpeedup = false
	opts.SmartZoom = false
	opts.ShowCursorGhost = false
	log := recordEventLog{Width: 640, Height: 360, Events: []recordEvent{
		{TMs: 200, Type: "click", X: 320, Y: 180, Count: 1, Button: "left"},
		{TMs: 250, Type: "type", Text: "x"},
	}}
	plan := buildPolishPlan(log, 1000, opts)
	if len(plan.Clicks) != 1 {
		t.Fatalf("clicks=%d %#v", len(plan.Clicks), plan.Clicks)
	}
	dir := t.TempDir()
	radii := []int{6, 11, 16, 22}
	rings := make([]string, 0, len(radii))
	for i, r := range radii {
		p := filepath.Join(dir, "ring"+strconv.Itoa(i)+".png")
		if err := writeRippleRingPNG(p, r, 2, 200); err != nil {
			t.Fatal(err)
		}
		rings = append(rings, p)
	}
	filter, _, err := buildAdvancedFilterComplex(plan, "/tmp/x.mp4", "", rings, radii, "", "", opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filter, "overlay") {
		t.Fatalf("no overlay in filter:\n%s", filter)
	}
}
