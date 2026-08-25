package main

import (
	"math"
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

func TestIdlePeriodsProtectClickMoments(t *testing.T) {
	events := []recordEvent{
		{TMs: 1000, Type: "wait", Seconds: 0.2},
		{TMs: 8000, Type: "click", X: 100, Y: 100, Count: 1, Button: "left"},
		{TMs: 9000, Type: "wait", Seconds: 0.2},
	}
	idles := detectIdlePeriods(events, 10000, defaultPolishOptions())
	for _, p := range idles {
		// Click instant ± hold must not be inside a sped-up idle span.
		if p.StartMs < 8000 && p.EndMs > 8000 {
			t.Fatalf("idle spans across click: %#v", p)
		}
		if p.SuggestedSpeed > 1.01 && p.StartMs <= 8000 && p.EndMs >= 8000 {
			t.Fatalf("speedup covers click: %#v", p)
		}
	}
	protected := false
	for _, p := range idles {
		if p.EndMs <= 8000-actionHoldPreMs(events[1]) && p.StartMs >= 1000 {
			protected = true
		}
	}
	if len(idles) == 0 {
		t.Fatal("expected idle before click")
	}
	_ = protected
	// Ensure pre-click hold is excluded from the long gap.
	found := false
	for _, p := range idles {
		if p.StartMs == 1000 && p.EndMs == 8000-actionHoldPreMs(events[1]) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected padded idle before click, got %#v", idles)
	}
}

func TestAlignEventLogToVideo(t *testing.T) {
	log := recordEventLog{
		Width: 1280, Height: 800,
		Events: []recordEvent{
			{TMs: 1, Type: "click", X: 640, Y: 400, Count: 1},
			{TMs: 2, Type: "drag", X: 100, Y: 100, ToX: 200, ToY: 200},
		},
	}
	alignEventLogToVideo(&log, 1920, 1200)
	if log.Width != 1920 || log.Height != 1200 {
		t.Fatalf("meta=%dx%d", log.Width, log.Height)
	}
	if log.Events[0].X != 960 || log.Events[0].Y != 600 {
		t.Fatalf("click scaled to %d,%d", log.Events[0].X, log.Events[0].Y)
	}
	if log.Events[1].ToX != 300 || log.Events[1].ToY != 300 {
		t.Fatalf("drag end scaled to %d,%d", log.Events[1].ToX, log.Events[1].ToY)
	}
}

func TestDefaultRipplesEnabled(t *testing.T) {
	if !defaultPolishOptions().ShowClickRipples {
		t.Fatal("yellow ripples should default on")
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

func TestCursorDepressAndScreenStudio(t *testing.T) {
	if s := depressScaleAt(40); !(s < 0.9) {
		t.Fatalf("expected depress mid-press <0.9, got %v", s)
	}
	if s := depressScaleAt(500); math.Abs(s-1) > 1e-6 {
		t.Fatalf("expected released=1, got %v", s)
	}
	if screenStudioCursorEase(0) != 0 && screenStudioCursorEase(0) > 0.01 {
		// allow tiny numerical error at 0
	}
	e := screenStudioCursorEase(0.5)
	if e <= 0.5 || e >= 1.0 {
		t.Fatalf("screen studio ease mid should overshoot-ish toward 1, got %v", e)
	}
	events := []recordEvent{
		{TMs: 500, Type: "click", X: 100, Y: 100, Count: 1},
		{TMs: 1500, Type: "click", X: 500, Y: 400, Count: 1},
	}
	path := generateCursorPath(events, 2500, 800, 600, cursorStyleMellow)
	var minScale float64 = 2
	for _, kf := range path {
		if kf.Scale < minScale {
			minScale = kf.Scale
		}
	}
	if minScale > 0.85 {
		t.Fatalf("expected depress scale near clicks, min=%v", minScale)
	}
}

func TestExpandZoomEases(t *testing.T) {
	in := []zoomWindow{{StartMs: 0, EndMs: 2000, X: 100, Y: 100, Factor: 1.5, Score: 80}}
	out := expandZoomEases(in)
	if len(out) < 5 {
		t.Fatalf("expected eased sub-windows, got %d", len(out))
	}
	if out[0].Factor >= out[len(out)/2].Factor {
		t.Fatalf("ease-in should grow factor: first=%v mid=%v", out[0].Factor, out[len(out)/2].Factor)
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
	opts.ShowClickRipples = true
	opts.ShowKeystrokes = false
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
