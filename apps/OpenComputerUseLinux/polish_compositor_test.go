package main

import (
	"image/color"
	"math"
	"testing"
)

func TestZoomLevelAndFocusEaseInOut(t *testing.T) {
	windows := []zoomWindow{{StartMs: 1000, EndMs: 2000, X: 400, Y: 300, Factor: 1.5}}
	// Mid hold
	level, fx, fy, ok := zoomLevelAndFocus(windows, 1500)
	if !ok || math.Abs(level-1.5) > 1e-6 || fx != 400 || fy != 300 {
		t.Fatalf("hold: got level=%v fx=%v fy=%v ok=%v", level, fx, fy, ok)
	}
	// Mid zoom-in (~650ms before start with 700ms duration)
	level, _, _, ok = zoomLevelAndFocus(windows, 1000-350)
	if !ok || level <= 1.0 || level >= 1.5 {
		t.Fatalf("zoom-in mid: got level=%v ok=%v", level, ok)
	}
	// After zoom-out window
	level, _, _, ok = zoomLevelAndFocus(windows, 2000+350)
	if !ok || level <= 1.0 || level >= 1.5 {
		t.Fatalf("zoom-out mid: got level=%v ok=%v", level, ok)
	}
	// Far outside
	_, _, _, ok = zoomLevelAndFocus(windows, 50)
	if ok {
		t.Fatal("expected no zoom far before window")
	}
}

func TestLensWarpActiveOnlyWhenZoomed(t *testing.T) {
	if _, ok := computeLensWarp(1.0, 0.5, 0.5); ok {
		t.Fatal("lens should be inactive at zoom 1")
	}
	p, ok := computeLensWarp(1.8, 0.3, 0.7)
	if !ok || p.Perspective <= 0 {
		t.Fatalf("lens expected at zoom 1.8: %#v ok=%v", p, ok)
	}
}

func TestSourceToOutputMapperIdleSpeedup(t *testing.T) {
	segs := []polishSegment{
		{StartMs: 0, EndMs: 1000, Rate: 1},
		{StartMs: 1000, EndMs: 5000, Rate: 4}, // 4s → 1s
		{StartMs: 5000, EndMs: 6000, Rate: 1},
	}
	mapFn := buildSourceToOutputMapper(segs)
	if got := mapFn(500); math.Abs(got-500) > 1 {
		t.Fatalf("active region map: got %v", got)
	}
	// start of idle → 1000 out
	if got := mapFn(1000); math.Abs(got-1000) > 1 {
		t.Fatalf("idle start: got %v", got)
	}
	// end of idle → 2000 out (1000 + 4000/4)
	if got := mapFn(5000); math.Abs(got-2000) > 1 {
		t.Fatalf("idle end: got %v want ~2000", got)
	}
	if got := mapFn(6000); math.Abs(got-3000) > 1 {
		t.Fatalf("final: got %v want ~3000", got)
	}
}

func TestCursorAtInterpolates(t *testing.T) {
	path := []cursorKeyframe{
		{TMs: 0, X: 0, Y: 0, Scale: 1},
		{TMs: 100, X: 100, Y: 50, Scale: 0.75},
	}
	kf := cursorAt(path, 50)
	if kf.X < 40 || kf.X > 60 || kf.Y < 20 || kf.Y > 30 {
		t.Fatalf("interp pos: got %+v", kf)
	}
	if kf.Scale < 0.85 || kf.Scale > 0.90 {
		t.Fatalf("interp scale: got %v", kf.Scale)
	}
}

func TestKeystrokeOpacityFade(t *testing.T) {
	chips := []keystrokeChip{{text: "Enter", showStart: 1000, peakAt: 1150}}
	if _, _, ok := keystrokeOpacity(chips, 500); ok {
		t.Fatal("too early")
	}
	text, op, ok := keystrokeOpacity(chips, 1000+keyFadeInMs/2)
	if !ok || text != "Enter" || op <= 0 || op >= 1 {
		t.Fatalf("fade-in: text=%q op=%v ok=%v", text, op, ok)
	}
	_, op, ok = keystrokeOpacity(chips, 1000+keyFadeInMs+100)
	if !ok || math.Abs(op-1) > 1e-6 {
		t.Fatalf("hold: op=%v ok=%v", op, ok)
	}
	_, _, ok = keystrokeOpacity(chips, 1000+keyFadeInMs+keyHoldMs+keyFadeOutMs+10)
	if ok {
		t.Fatal("should be gone after fade-out")
	}
}

func TestParsePolishEngine(t *testing.T) {
	k, err := parsePolishEngine("compositor")
	if err != nil || k != polishEngineCompositor {
		t.Fatalf("compositor: %v %v", k, err)
	}
	k, err = parsePolishEngine("ffmpeg")
	if err != nil || k != polishEngineFFmpeg {
		t.Fatalf("ffmpeg: %v %v", k, err)
	}
	if _, err := parsePolishEngine("bogus"); err == nil {
		t.Fatal("expected error")
	}
}

func TestApplyZoomPanIdentity(t *testing.T) {
	src := newRGBAFrame(8, 8)
	for i := range src.Pix {
		src.Pix[i] = byte(i % 251)
	}
	dst := newRGBAFrame(8, 8)
	applyZoomPan(src, dst, identityZoom())
	for i := range src.Pix {
		if src.Pix[i] != dst.Pix[i] {
			t.Fatalf("identity mismatch at %d", i)
		}
	}
}

func TestCameraMotionBlurPassthroughWhenStill(t *testing.T) {
	src := newRGBAFrame(4, 4)
	src.clear(color.NRGBA{10, 20, 30, 255})
	dst := newRGBAFrame(4, 4)
	z := identityZoom()
	applyCameraMotionBlur(src, dst, z, z, defaultMotionBlurConfig())
	for i := range src.Pix {
		if src.Pix[i] != dst.Pix[i] {
			t.Fatalf("still blur mismatch at %d", i)
		}
	}
}
