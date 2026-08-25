package main

// Open-source record polish pipeline aligned with Cursor's recording-renderer
// analysis + polished-renderer compositor behavior (without linking the
// proprietary native addon):
//
//   - click ripples as thin expanding rings (PNG overlays, not filled blobs)
//   - keystroke captions (ASS)
//   - idle classification (LOADING_WAIT / VIEWING_RESULT / THINKING_PAUSE /
//     LONG_OPERATION) with per-class speedups
//   - click importance scoring → multi-window smart zoom
//   - cursor path with cubic-bezier motion styles (slow/mellow/quick/rapid)
//
// Reference constants were taken from recording-renderer preprocessing
// (idle_classifier / click-importance / cursor-path / DEFAULT_PREPROCESSING_CONFIG)
// visible in the host agent bundle; the .node binary itself is a compiled
// Rust N-API addon (not source).

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type cursorStyle int

const (
	cursorStyleSlow cursorStyle = iota + 1
	cursorStyleMellow
	cursorStyleQuick
	cursorStyleRapid
)

type idleClass int

const (
	idleUnspecified idleClass = iota
	idleLoadingWait
	idleViewingResult
	idleThinkingPause
	idleLongOperation
)

type polishOptions struct {
	ShowClickRipples bool
	ShowKeystrokes   bool
	ShowCursorGhost  bool
	IdleSpeedup      bool
	SmartZoom        bool
	CursorStyle      cursorStyle
	MinIdleMs        int64
	ZoomFactor       float64
	ZoomDurationMs   int64
	MaxZooms         int
	ZoomImportance   int
	MinZoomInterval  int64
}

func defaultPolishOptions() polishOptions {
	return polishOptions{
		ShowClickRipples: true,
		ShowKeystrokes:   true,
		ShowCursorGhost:  true,
		IdleSpeedup:      true,
		SmartZoom:        true,
		CursorStyle:      cursorStyleMellow,
		MinIdleMs:        500,
		ZoomFactor:       1.5,
		ZoomDurationMs:   1400,
		MaxZooms:         8,
		ZoomImportance:   60,
		MinZoomInterval:  1500,
	}
}

type polishSegment struct {
	StartMs int64
	EndMs   int64
	Rate    float64
	Zoom    *zoomWindow // optional zoom applied for this segment
}

type zoomWindow struct {
	StartMs int64
	EndMs   int64
	X       int
	Y       int
	Factor  float64
	Score   int
}

type idlePeriod struct {
	StartMs        int64
	EndMs          int64
	Classification idleClass
	SuggestedSpeed float64
}

type clickEffect struct {
	TMs      int64
	X, Y     int
	Count    int
	Button   string
	Score    int
	Followed bool
}

type cursorKeyframe struct {
	TMs int64
	X   int
	Y   int
}

type polishPlan struct {
	Segments []polishSegment
	Zooms    []zoomWindow
	Clicks   []clickEffect
	Cursor   []cursorKeyframe
	ASS      string
	Width    int
	Height   int
}

func parseCursorStyle(value string) (cursorStyle, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "mellow":
		return cursorStyleMellow, nil
	case "slow":
		return cursorStyleSlow, nil
	case "quick":
		return cursorStyleQuick, nil
	case "rapid":
		return cursorStyleRapid, nil
	default:
		return 0, fmt.Errorf("invalid --cursor-style %q (slow|mellow|quick|rapid)", value)
	}
}

func buildPolishPlan(log recordEventLog, durationMs int64, opts polishOptions) polishPlan {
	if durationMs <= 0 {
		durationMs = 1
	}
	events := append([]recordEvent(nil), log.Events...)
	sort.Slice(events, func(i, j int) bool { return events[i].TMs < events[j].TMs })

	width, height := log.Width, log.Height
	if width <= 0 {
		width = 1920
	}
	if height <= 0 {
		height = 1200
	}

	clicks := analyzeClickEffects(events, width, height)
	idles := detectIdlePeriods(events, durationMs, opts)
	var zooms []zoomWindow
	if opts.SmartZoom {
		zooms = selectZoomWindowsFromClicks(clicks, durationMs, opts)
	}
	segments := buildPlaybackSegments(idles, zooms, durationMs, opts)
	cursor := []cursorKeyframe{}
	if opts.ShowCursorGhost {
		cursor = generateCursorPath(events, durationMs, width, height, opts.CursorStyle)
	}
	ass := ""
	if opts.ShowKeystrokes {
		ass = buildKeystrokeASS(events, width, height, opts)
	}
	return polishPlan{
		Segments: segments,
		Zooms:    zooms,
		Clicks:   filterClickEffects(clicks, 200),
		Cursor:   cursor,
		ASS:      ass,
		Width:    width,
		Height:   height,
	}
}

// filterClickEffects drops rapid navigational clicks (keeps doubles/triples),
// matching recording-renderer click-effects.js (minIntervalMs=200).
func filterClickEffects(effects []clickEffect, minIntervalMs int64) []clickEffect {
	if len(effects) == 0 {
		return effects
	}
	out := make([]clickEffect, 0, len(effects))
	var lastT int64
	hasLast := false
	for _, e := range effects {
		if hasLast && e.TMs-lastT < minIntervalMs && e.Count < 2 {
			continue
		}
		out = append(out, e)
		lastT = e.TMs
		hasLast = true
	}
	return out
}

// --- click importance (ported from recording-renderer click-importance.js) ---

func analyzeClickEffects(events []recordEvent, width, height int) []clickEffect {
	var clicks []clickEffect
	for i, ev := range events {
		if ev.Type != "click" {
			continue
		}
		if ev.X == 0 && ev.Y == 0 {
			continue
		}
		followed := false
		if i+1 < len(events) && (events[i+1].Type == "type" || events[i+1].Type == "key") {
			followed = true
		}
		clicks = append(clicks, clickEffect{
			TMs: ev.TMs, X: ev.X, Y: ev.Y, Count: ev.Count, Button: ev.Button, Followed: followed,
		})
	}
	const (
		rapidClickThresholdMs = 500
		sameAreaThresholdPx   = 50
		edgeMarginPx          = 100
		idleThresholdMs       = 3000
	)
	var lastClickT int64
	var lastX, lastY int
	var lastNonClick int64
	clickIdx := 0
	for _, ev := range events {
		if ev.Type == "click" && clickIdx < len(clicks) && clicks[clickIdx].TMs == ev.TMs {
			c := &clicks[clickIdx]
			score := 50
			if c.Count >= 2 {
				score += 25
			}
			if c.Count >= 3 {
				score += 20
			}
			if strings.EqualFold(c.Button, "right") {
				score += 15
			}
			if lastClickT > 0 {
				dt := c.TMs - lastClickT
				if dt < rapidClickThresholdMs {
					score -= 25
				}
				if dt > idleThresholdMs {
					score += 15
				}
				dx, dy := float64(c.X-lastX), float64(c.Y-lastY)
				if math.Hypot(dx, dy) < sameAreaThresholdPx {
					score -= 15
				}
			}
			if c.X < edgeMarginPx || c.Y < edgeMarginPx || c.X > width-edgeMarginPx || c.Y > height-edgeMarginPx {
				score -= 20
			}
			if c.TMs-lastNonClick > idleThresholdMs {
				score += 25
			}
			if c.Followed {
				score += 30
			}
			c.Score = clampInt(score, 0, 100)
			lastClickT, lastX, lastY = c.TMs, c.X, c.Y
			lastNonClick = c.TMs
			clickIdx++
			continue
		}
		if ev.Type != "wait" {
			lastNonClick = ev.TMs
		}
	}
	return clicks
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func selectZoomWindowsFromClicks(clicks []clickEffect, durationMs int64, opts polishOptions) []zoomWindow {
	sorted := append([]clickEffect(nil), clicks...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Score != sorted[j].Score {
			return sorted[i].Score > sorted[j].Score
		}
		return sorted[i].TMs < sorted[j].TMs
	})
	var zooms []zoomWindow
	lastStart := int64(-opts.MinZoomInterval)
	maxZooms := opts.MaxZooms
	if durationMs > 0 {
		perMin := int(float64(durationMs)/60000.0*8 + 0.999)
		if perMin < 1 {
			perMin = 1
		}
		if perMin < maxZooms {
			maxZooms = perMin
		}
	}
	for _, c := range sorted {
		if c.Score < opts.ZoomImportance {
			continue
		}
		if len(zooms) >= maxZooms {
			break
		}
		start := c.TMs - 200
		if start < 0 {
			start = 0
		}
		end := start + opts.ZoomDurationMs
		if end > durationMs {
			end = durationMs
		}
		if start < lastStart+opts.MinZoomInterval {
			continue
		}
		// reject overlaps with existing
		overlap := false
		for _, z := range zooms {
			if start < z.EndMs && end > z.StartMs {
				overlap = true
				break
			}
		}
		if overlap {
			continue
		}
		zooms = append(zooms, zoomWindow{StartMs: start, EndMs: end, X: c.X, Y: c.Y, Factor: opts.ZoomFactor, Score: c.Score})
		lastStart = start
	}
	sort.Slice(zooms, func(i, j int) bool { return zooms[i].StartMs < zooms[j].StartMs })
	return zooms
}

// --- idle classification (ported from idle-classifier.js) ---

func detectIdlePeriods(events []recordEvent, durationMs int64, opts polishOptions) []idlePeriod {
	cfgMin := opts.MinIdleMs
	if cfgMin <= 0 {
		cfgMin = 500
	}
	if len(events) == 0 {
		if durationMs >= cfgMin {
			return []idlePeriod{{0, durationMs, idleLongOperation, 2.0}}
		}
		return nil
	}
	var periods []idlePeriod
	first := events[0].TMs
	if first >= cfgMin {
		c, speed := classifyIdlePeriod(first, "none", eventActionType(events[0]))
		periods = append(periods, idlePeriod{0, first, c, speed})
	}
	for i := 0; i < len(events)-1; i++ {
		cur, next := events[i], events[i+1]
		gap := next.TMs - cur.TMs
		if gap < cfgMin {
			continue
		}
		c, speed := classifyIdlePeriod(gap, eventActionType(cur), eventActionType(next))
		periods = append(periods, idlePeriod{cur.TMs, next.TMs, c, speed})
	}
	last := events[len(events)-1]
	if durationMs-last.TMs >= cfgMin {
		c, speed := classifyIdlePeriod(durationMs-last.TMs, eventActionType(last), "none")
		periods = append(periods, idlePeriod{last.TMs, durationMs, c, speed})
	}
	return periods
}

func eventActionType(ev recordEvent) string {
	switch ev.Type {
	case "click":
		if ev.Count >= 3 {
			return "triple_click"
		}
		if ev.Count == 2 {
			return "double_click"
		}
		return "click"
	case "type":
		return "type"
	case "key":
		return "key"
	case "scroll":
		return "scroll"
	case "drag":
		return "drag"
	default:
		return ev.Type
	}
}

func classifyIdlePeriod(durationMs int64, preceding, following string) (idleClass, float64) {
	const (
		loadingWaitThresholdMs   = 1000
		viewingResultMinMs       = 500
		viewingResultMaxMs       = 3000
		thinkingPauseThresholdMs = 5000
		longOperationThresholdMs = 10000
		defaultSpeedup           = 2.0
		loadingSpeedup           = 4.0
		thinkingSpeedup          = 3.0
	)
	if durationMs >= longOperationThresholdMs {
		return idleLongOperation, loadingSpeedup
	}
	if (preceding == "click" || preceding == "double_click" || preceding == "triple_click") &&
		(following == "screenshot" || following == "none") {
		return idleLoadingWait, loadingSpeedup
	}
	if preceding == "screenshot" {
		if durationMs >= viewingResultMinMs && durationMs <= viewingResultMaxMs {
			return idleViewingResult, 1.0
		}
	}
	if (preceding == "type" || preceding == "key") && (following == "type" || following == "key") {
		if durationMs >= thinkingPauseThresholdMs {
			return idleThinkingPause, thinkingSpeedup
		}
	}
	if durationMs >= thinkingPauseThresholdMs {
		return idleThinkingPause, thinkingSpeedup
	}
	if durationMs >= loadingWaitThresholdMs &&
		(preceding == "click" || preceding == "double_click" || preceding == "type" || preceding == "key" || preceding == "scroll") {
		return idleLoadingWait, loadingSpeedup
	}
	return idleViewingResult, 1.0
}

func buildPlaybackSegments(idles []idlePeriod, zooms []zoomWindow, durationMs int64, opts polishOptions) []polishSegment {
	// Build a timeline of cut points from idle + zoom boundaries, then assign
	// rate (from idle) and optional zoom (covering window) per leaf segment.
	cuts := map[int64]struct{}{0: {}, durationMs: {}}
	for _, p := range idles {
		cuts[p.StartMs] = struct{}{}
		cuts[p.EndMs] = struct{}{}
	}
	for _, z := range zooms {
		cuts[z.StartMs] = struct{}{}
		cuts[z.EndMs] = struct{}{}
	}
	points := make([]int64, 0, len(cuts))
	for t := range cuts {
		if t >= 0 && t <= durationMs {
			points = append(points, t)
		}
	}
	sort.Slice(points, func(i, j int) bool { return points[i] < points[j] })

	rateAt := func(t int64) float64 {
		if !opts.IdleSpeedup {
			return 1
		}
		for _, p := range idles {
			if t >= p.StartMs && t < p.EndMs {
				if p.SuggestedSpeed < 1.01 {
					return 1
				}
				// Preserve VIEWING_RESULT
				if p.Classification == idleViewingResult {
					return 1
				}
				return p.SuggestedSpeed
			}
		}
		return 1
	}
	zoomAt := func(t int64) *zoomWindow {
		for i := range zooms {
			z := &zooms[i]
			if t >= z.StartMs && t < z.EndMs {
				return z
			}
		}
		return nil
	}

	var segs []polishSegment
	for i := 0; i+1 < len(points); i++ {
		a, b := points[i], points[i+1]
		if b <= a {
			continue
		}
		mid := a
		seg := polishSegment{StartMs: a, EndMs: b, Rate: rateAt(mid)}
		if z := zoomAt(mid); z != nil {
			cp := *z
			seg.Zoom = &cp
		}
		segs = append(segs, seg)
	}
	if len(segs) == 0 {
		return []polishSegment{{StartMs: 0, EndMs: durationMs, Rate: 1}}
	}
	return mergeCompatibleSegments(segs)
}

func mergeCompatibleSegments(in []polishSegment) []polishSegment {
	if len(in) == 0 {
		return in
	}
	out := []polishSegment{in[0]}
	for _, seg := range in[1:] {
		last := &out[len(out)-1]
		sameZoom := (last.Zoom == nil && seg.Zoom == nil) ||
			(last.Zoom != nil && seg.Zoom != nil && last.Zoom.X == seg.Zoom.X && last.Zoom.Y == seg.Zoom.Y && almostEqual(last.Zoom.Factor, seg.Zoom.Factor))
		if sameZoom && almostEqual(last.Rate, seg.Rate) && seg.StartMs <= last.EndMs {
			if seg.EndMs > last.EndMs {
				last.EndMs = seg.EndMs
			}
			continue
		}
		out = append(out, seg)
	}
	return out
}

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 0.001 }

func evenInt(v int) int {
	if v < 0 {
		v = 0
	}
	return v - (v % 2)
}

// --- cursor path with cubic-bezier easing (cursor-path.js) ---

func generateCursorPath(events []recordEvent, durationMs int64, width, height int, style cursorStyle) []cursorKeyframe {
	type wp struct {
		t    int64
		x, y int
	}
	var waypoints []wp
	for _, ev := range events {
		switch ev.Type {
		case "move", "click":
			if ev.X != 0 || ev.Y != 0 {
				waypoints = append(waypoints, wp{ev.TMs, ev.X, ev.Y})
			}
		case "drag":
			waypoints = append(waypoints, wp{ev.TMs, ev.X, ev.Y})
			waypoints = append(waypoints, wp{ev.TMs + 180, ev.ToX, ev.ToY})
		}
	}
	if len(waypoints) == 0 {
		return []cursorKeyframe{{0, width / 2, height / 2}, {durationMs, width / 2, height / 2}}
	}
	sort.Slice(waypoints, func(i, j int) bool { return waypoints[i].t < waypoints[j].t })
	// Dedup near-identical times
	dedup := waypoints[:1]
	for _, w := range waypoints[1:] {
		if w.t-dedup[len(dedup)-1].t < 16 {
			dedup[len(dedup)-1] = w
			continue
		}
		dedup = append(dedup, w)
	}
	waypoints = dedup

	const fps = 30
	frameMs := int64(1000 / fps)
	var frames []cursorKeyframe
	// Hold at first point from 0
	if waypoints[0].t > 0 {
		for t := int64(0); t < waypoints[0].t; t += frameMs {
			frames = append(frames, cursorKeyframe{t, waypoints[0].x, waypoints[0].y})
		}
	}
	for i := 0; i+1 < len(waypoints); i++ {
		a, b := waypoints[i], waypoints[i+1]
		dur := b.t - a.t
		if dur <= 0 {
			continue
		}
		// Add a slight arc (perpendicular offset) for natural motion.
		steps := int(dur/frameMs) + 1
		for s := 0; s <= steps; s++ {
			u := float64(s) / float64(steps)
			e := bezierEase(style, u)
			x := float64(a.x) + (float64(b.x)-float64(a.x))*e
			y := float64(a.y) + (float64(b.y)-float64(a.y))*e
			// Arc amplitude ~8% of travel distance
			dx, dy := float64(b.x-a.x), float64(b.y-a.y)
			dist := math.Hypot(dx, dy)
			if dist > 1 {
				nx, ny := -dy/dist, dx/dist
				arc := math.Sin(u*math.Pi) * dist * 0.08
				x += nx * arc
				y += ny * arc
			}
			frames = append(frames, cursorKeyframe{a.t + int64(float64(dur)*u), int(x + 0.5), int(y + 0.5)})
		}
	}
	last := waypoints[len(waypoints)-1]
	for t := last.t; t <= durationMs; t += frameMs {
		frames = append(frames, cursorKeyframe{t, last.x, last.y})
	}
	return frames
}

func bezierEase(style cursorStyle, t float64) float64 {
	// Prefer spring-physics progress (polished-renderer SPRING_CONFIGS), with
	// cubic-bezier as a stable fallback for pathological samples.
	if p := springEase(style, t); p >= 0 {
		return p
	}
	switch style {
	case cursorStyleSlow:
		return cubicBezier(0.25, 0.1, 0.25, 1.0, t)
	case cursorStyleQuick:
		return cubicBezier(0.0, 0.0, 0.2, 1.0, t)
	case cursorStyleRapid:
		return cubicBezier(0.4, 0.0, 0.2, 1.0, t)
	default: // mellow
		return cubicBezier(0.42, 0.0, 0.58, 1.0, t)
	}
}

// springEase integrates a damped spring toward 1.0 over normalized time t∈[0,1],
// using proprietary MotionStyle spring configs (tension/friction/mass).
func springEase(style cursorStyle, t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	var tension, friction, mass float64
	switch style {
	case cursorStyleSlow:
		tension, friction, mass = 120, 30, 1.2
	case cursorStyleQuick:
		tension, friction, mass = 280, 24, 0.8
	case cursorStyleRapid:
		tension, friction, mass = 400, 30, 0.5
	default: // mellow / unspecified
		tension, friction, mass = 170, 26, 1.0
	}
	// Simulate ~0.45s of spring motion mapped onto t∈[0,1].
	const durationSec = 0.45
	dt := 1.0 / 120.0
	steps := int(durationSec*t/dt) + 1
	pos, vel := 0.0, 0.0
	target := 1.0
	for i := 0; i < steps; i++ {
		force := -tension*(pos-target) - friction*vel
		acc := force / mass
		vel += acc * dt
		pos += vel * dt
	}
	if pos < 0 {
		pos = 0
	}
	if pos > 1 {
		pos = 1
	}
	return pos
}

func cubicBezier(x1, y1, x2, y2, t float64) float64 {
	x := t
	for i := 0; i < 8; i++ {
		currentX := bezierValue(x1, x2, x)
		slope := bezierSlope(x1, x2, x)
		if math.Abs(currentX-t) < 1e-6 || math.Abs(slope) < 1e-6 {
			break
		}
		x = x - (currentX-t)/slope
	}
	return bezierValue(y1, y2, x)
}

func bezierValue(p1, p2, t float64) float64 {
	u := 1 - t
	return 3*u*u*t*p1 + 3*u*t*t*p2 + t*t*t
}

func bezierSlope(p1, p2, t float64) float64 {
	u := 1 - t
	return 3*u*u*p1 + 6*u*t*(p2-p1) + 3*t*t*(1-p2)
}

// --- ASS keystrokes only (ripples/cursor use PNG overlays — never ASS filled shapes) ---

func buildKeystrokeASS(events []recordEvent, width, height int, opts polishOptions) string {
	if !opts.ShowKeystrokes {
		return ""
	}
	var b strings.Builder
	b.WriteString("[Script Info]\nTitle: open-computer-use polish\nScriptType: v4.00+\n")
	b.WriteString(fmt.Sprintf("PlayResX: %d\nPlayResY: %d\n\n", width, height))
	b.WriteString("[V4+ Styles]\n")
	b.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	b.WriteString("Style: Keystroke,Arial,36,&H00FFFFFF,&H000000FF,&H64000000,&HAA000000,-1,0,0,0,100,100,0,0,1,2,0,2,40,40,48,1\n\n")
	b.WriteString("[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	const displayMs int64 = 1500
	const combineMs int64 = 500
	var pending string
	var pendingStart, pendingEnd int64
	flush := func() {
		if pending == "" {
			return
		}
		text := pending
		if len([]rune(text)) > 30 {
			text = string([]rune(text)[:27]) + "…"
		}
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Keystroke,,0,0,0,,%s\n",
			formatASSTime(pendingStart), formatASSTime(pendingEnd), escapeASS(text))
		pending = ""
	}
	for _, ev := range events {
		switch ev.Type {
		case "type":
			if pending != "" && ev.TMs-pendingEnd <= combineMs {
				pending += ev.Text
				pendingEnd = ev.TMs + displayMs
			} else {
				flush()
				pending = ev.Text
				pendingStart = ev.TMs
				pendingEnd = ev.TMs + displayMs
			}
		case "key":
			flush()
			fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Keystroke,,0,0,0,,%s\n",
				formatASSTime(ev.TMs), formatASSTime(ev.TMs+displayMs), escapeASS(keyDisplayLabel(ev.Key)))
		default:
			flush()
		}
	}
	flush()
	return b.String()
}

func keyDisplayLabel(key string) string {
	replacements := map[string]string{
		"Return": "Enter", "Enter": "Enter", "Tab": "Tab", "Escape": "Esc",
		"BackSpace": "Backspace", "Delete": "Del", "space": "Space",
		"Up": "Up", "Down": "Down", "Left": "Left", "Right": "Right",
		"ctrl": "Ctrl", "alt": "Alt", "shift": "Shift", "meta": "Meta", "cmd": "Cmd",
	}
	parts := strings.Split(key, "+")
	for i, p := range parts {
		if rep, ok := replacements[p]; ok {
			parts[i] = rep
			continue
		}
		if rep, ok := replacements[strings.ToLower(p)]; ok {
			parts[i] = rep
			continue
		}
		if len(p) == 1 {
			parts[i] = strings.ToUpper(p)
		}
	}
	return strings.Join(parts, "+")
}

func escapeASS(text string) string {
	return strings.NewReplacer(`\`, `\\`, `{`, `\[`, `}`, `\]`, "\n", `\N`).Replace(text)
}

func formatASSTime(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	h := ms / 3600000
	ms %= 3600000
	m := ms / 60000
	ms %= 60000
	s := ms / 1000
	cs := (ms % 1000) / 10
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, cs)
}

// --- PNG assets: thin ripple rings + cursor arrow ---

// writeRippleRingPNG draws a crisp 1–2px ring (NOT a filled disk).
// size = (radius + thickness + pad) * 2; hotspot is image center.
func writeRippleRingPNG(path string, radius, thickness int, alpha uint8) error {
	if radius < 1 {
		radius = 1
	}
	if thickness < 1 {
		thickness = 1
	}
	pad := 2
	size := (radius + thickness + pad) * 2
	// NRGBA = straight (non-premultiplied) alpha — required so yellow stays yellow.
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	cx, cy := float64(size)/2, float64(size)/2
	inner := float64(radius)
	outer := float64(radius + thickness)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			if d < inner-0.6 || d > outer+0.6 {
				continue
			}
			var coverage float64
			switch {
			case d >= inner && d <= outer:
				coverage = 1
			case d < inner:
				coverage = 1 - (inner - d)
			default:
				coverage = 1 - (d - outer)
			}
			if coverage <= 0 {
				continue
			}
			a := uint8(float64(alpha) * coverage)
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 215, B: 0, A: a})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func writeRippleDotPNG(path string, radius int, alpha uint8) error {
	if radius < 1 {
		radius = 1
	}
	size := (radius + 2) * 2
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	cx, cy := float64(size)/2, float64(size)/2
	r := float64(radius)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			if d > r+0.6 {
				continue
			}
			coverage := 1.0
			if d > r {
				coverage = 1 - (d - r)
			}
			a := uint8(float64(alpha) * coverage)
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 215, B: 0, A: a})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func rippleRingSize(radius, thickness int) int {
	return (radius + thickness + 2) * 2
}

func writeCursorArrowPNG(path string) error {
	const size = 18
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	// Classic NW arrow (hotspot ≈ 1,1), outline then fill.
	body := [][2]int{
		{1, 1}, {1, 15}, {4, 12}, {7, 17}, {9, 16}, {6, 11}, {12, 11},
	}
	drawFilledPolyNRGBA(img, body, color.NRGBA{R: 255, G: 255, B: 255, A: 245})
	// 1px dark outline neighbors
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if img.NRGBAAt(x, y).A == 0 {
				continue
			}
			for _, d := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
				nx, ny := x+d[0], y+d[1]
				if nx < 0 || ny < 0 || nx >= size || ny >= size {
					img.SetNRGBA(x, y, color.NRGBA{R: 15, G: 15, B: 15, A: 255})
					break
				}
				if img.NRGBAAt(nx, ny).A == 0 {
					img.SetNRGBA(x, y, color.NRGBA{R: 15, G: 15, B: 15, A: 255})
					break
				}
			}
		}
	}
	drawFilledPolyNRGBA(img, [][2]int{{2, 2}, {2, 12}, {4, 10}, {6, 14}, {7, 13}, {5, 9}, {10, 9}}, color.NRGBA{R: 255, G: 255, B: 255, A: 250})
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func drawFilledPolyNRGBA(img *image.NRGBA, poly [][2]int, c color.NRGBA) {
	minX, minY, maxX, maxY := img.Bounds().Max.X, img.Bounds().Max.Y, 0, 0
	for _, p := range poly {
		if p[0] < minX {
			minX = p[0]
		}
		if p[1] < minY {
			minY = p[1]
		}
		if p[0] > maxX {
			maxX = p[0]
		}
		if p[1] > maxY {
			maxY = p[1]
		}
	}
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			if pointInPoly(x, y, poly) {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

func pointInPoly(x, y int, poly [][2]int) bool {
	inside := false
	j := len(poly) - 1
	for i := 0; i < len(poly); i++ {
		xi, yi := poly[i][0], poly[i][1]
		xj, yj := poly[j][0], poly[j][1]
		if ((yi > y) != (yj > y)) && (x < (xj-xi)*(y-yi)/(yj-yi)+xi) {
			inside = !inside
		}
		j = i
	}
	return inside
}

func probeVideoDurationMs(path string) (int64, int, int, error) {
	cmd := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format=duration:stream=width,height",
		"-of", "json", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("ffprobe failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	var payload struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return 0, 0, 0, err
	}
	seconds, err := strconv.ParseFloat(payload.Format.Duration, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid duration %q", payload.Format.Duration)
	}
	w, h := 0, 0
	for _, s := range payload.Streams {
		if s.Width > 0 && s.Height > 0 {
			w, h = s.Width, s.Height
			break
		}
	}
	return int64(seconds * 1000), w, h, nil
}

func polishRecording(inputVideo, eventsPath, outputVideo string, opts polishOptions) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return errors.New("ffmpeg is required for record polish but was not found on PATH")
	}
	log, err := readRecordEventLog(eventsPath)
	if err != nil {
		return fmt.Errorf("cannot read events (%s): %w", eventsPath, err)
	}
	durationMs, width, height, err := probeVideoDurationMs(inputVideo)
	if err != nil {
		return err
	}
	if log.Width <= 0 {
		log.Width = width
	}
	if log.Height <= 0 {
		log.Height = height
	}
	plan := buildPolishPlan(log, durationMs, opts)

	workDir, err := os.MkdirTemp("", "ocu-polish-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	assPath := filepath.Join(workDir, "keys.ass")
	if plan.ASS != "" {
		if err := os.WriteFile(assPath, []byte(plan.ASS), 0o644); err != nil {
			return err
		}
	} else {
		assPath = ""
	}

	// Ripple ring assets — small thin yellow rings (avoid filled "blob" look).
	// Radii stay compact; no center disc (disc+inner-ring read as a blob).
	ringRadii := []int{6, 11, 16, 22}
	ringPaths := make([]string, 0, len(ringRadii))
	for i, r := range ringRadii {
		p := filepath.Join(workDir, fmt.Sprintf("ring%d.png", i))
		alpha := uint8(220 - i*40)
		if err := writeRippleRingPNG(p, r, 2, alpha); err != nil {
			return err
		}
		ringPaths = append(ringPaths, p)
	}
	cursorPath := filepath.Join(workDir, "cursor.png")
	if opts.ShowCursorGhost {
		if err := writeCursorArrowPNG(cursorPath); err != nil {
			return err
		}
	}

	filter, inputArgs, err := buildAdvancedFilterComplex(plan, inputVideo, assPath, ringPaths, ringRadii, "", cursorPath, opts)
	if err != nil {
		return err
	}
	args := append([]string{"-nostdin", "-y"}, inputArgs...)
	args = append(args,
		"-filter_complex", filter,
		"-map", "[outv]",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "18",
		"-pix_fmt", "yuv420p",
		"-profile:v", "high",
		"-movflags", "+faststart",
		outputVideo,
	)
	cmd := exec.Command("ffmpeg", args...)
	logFile, _ := os.Create(outputVideo + ".polish.log")
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		defer logFile.Close()
	}
	if err := cmd.Run(); err != nil {
		detail := ""
		if data, readErr := os.ReadFile(outputVideo + ".polish.log"); readErr == nil {
			detail = strings.TrimSpace(string(data))
			if len(detail) > 800 {
				detail = detail[len(detail)-800:]
			}
		}
		return fmt.Errorf("ffmpeg polish failed: %v: %s", err, detail)
	}
	return nil
}

func buildAdvancedFilterComplex(plan polishPlan, inputVideo, assPath string, rings []string, ringRadii []int, dotPNG, cursorPNG string, opts polishOptions) (string, []string, error) {
	// Inputs: 0=video, 1..=rings, optional dot, optional cursor
	inputs := []string{"-i", inputVideo}
	ringIndex := make([]int, len(rings))
	for i, p := range rings {
		ringIndex[i] = 1 + i
		inputs = append(inputs, "-loop", "1", "-i", p)
	}
	next := 1 + len(rings)
	_ = dotPNG // reserved; filled center discs removed (they read as blobs)
	cursorIdx := -1
	if opts.ShowCursorGhost && cursorPNG != "" && len(plan.Cursor) > 0 {
		cursorIdx = next
		inputs = append(inputs, "-loop", "1", "-i", cursorPNG)
	}

	var b strings.Builder
	current := "[0:v]"

	// 1) Click ripple overlays — thin expanding rings only (no filled center disc).
	// Stagger windows so at most one ring is fully opaque at a time.
	if opts.ShowClickRipples && len(plan.Clicks) > 0 && len(rings) > 0 {
		label := current
		overlayN := 0
		const thickness = 2
		for _, click := range plan.Clicks {
			for i, ridx := range ringIndex {
				radius := 6
				if i < len(ringRadii) {
					radius = ringRadii[i]
				}
				ringSize := rippleRingSize(radius, thickness)
				// Non-overlapping ~70ms windows → expanding ripple, not a filled blob.
				start := float64(click.TMs)/1000.0 + float64(i)*0.070
				end := start + 0.075
				x := click.X - ringSize/2
				y := click.Y - ringSize/2
				out := fmt.Sprintf("[rp%d]", overlayN)
				fmt.Fprintf(&b, "[%d:v]format=rgba[ring%d];%s[ring%d]overlay=x=%d:y=%d:format=auto:enable='between(t,%0.3f,%0.3f)'%s;",
					ridx, overlayN, label, overlayN, x, y, start, end, out)
				label = out
				overlayN++
				if overlayN > 160 {
					break
				}
			}
			if overlayN > 160 {
				break
			}
		}
		current = label
	}

	// 2) Cursor ghost — subsample keyframes (~10 fps overlays via enable windows)
	if cursorIdx >= 0 {
		label := current
		step := 3 // every 3rd 30fps keyframe ≈ 10 overlays/sec
		if step < 1 {
			step = 1
		}
		n := 0
		for i := 0; i < len(plan.Cursor); i += step {
			kf := plan.Cursor[i]
			start := float64(kf.TMs) / 1000.0
			end := start + 0.12
			if i+step < len(plan.Cursor) {
				end = float64(plan.Cursor[i+step].TMs) / 1000.0
			}
			if end <= start {
				end = start + 0.05
			}
			out := fmt.Sprintf("[cu%d]", n)
			fmt.Fprintf(&b, "%s[%d:v]overlay=x=%d:y=%d:enable='between(t,%0.3f,%0.3f)'%s;",
				label, cursorIdx, kf.X, kf.Y, start, end, out)
			label = out
			n++
			if n > 400 {
				break
			}
		}
		current = label
	}

	// 3) Keystroke ASS (never contains ripples)
	if opts.ShowKeystrokes && assPath != "" && plan.ASS != "" {
		fmt.Fprintf(&b, "%sass=filename='%s'[ann];", current, escapeFFmpegFilterPath(assPath))
		current = "[ann]"
	} else {
		fmt.Fprintf(&b, "%scopy[ann];", current)
		current = "[ann]"
	}

	// 4) Multi-zoom + idle speedup via split/trim/crop/scale/setpts/concat
	segs := plan.Segments
	if len(segs) == 0 {
		segs = []polishSegment{{StartMs: 0, EndMs: 1, Rate: 1}}
	}
	n := len(segs)
	fmt.Fprintf(&b, "%ssplit=%d", current, n)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "[s%d]", i)
	}
	b.WriteString(";")

	var concat strings.Builder
	used := 0
	for i, seg := range segs {
		start := float64(seg.StartMs) / 1000.0
		end := float64(seg.EndMs) / 1000.0
		if end <= start {
			continue
		}
		rate := seg.Rate
		if rate < 1.01 {
			rate = 1
		}
		chain := fmt.Sprintf("[s%d]trim=start=%0.3f:end=%0.3f,setpts=PTS-STARTPTS", i, start, end)
		if seg.Zoom != nil && seg.Zoom.Factor > 1.05 {
			z := seg.Zoom.Factor
			cw := float64(plan.Width) / z
			ch := float64(plan.Height) / z
			// Center crop around click, clamped.
			cx := float64(seg.Zoom.X) - cw/2
			cy := float64(seg.Zoom.Y) - ch/2
			if cx < 0 {
				cx = 0
			}
			if cy < 0 {
				cy = 0
			}
			if cx+cw > float64(plan.Width) {
				cx = float64(plan.Width) - cw
			}
			if cy+ch > float64(plan.Height) {
				cy = float64(plan.Height) - ch
			}
			chain += fmt.Sprintf(",crop=%d:%d:%d:%d,scale=%d:%d",
				evenInt(int(cw)), evenInt(int(ch)), evenInt(int(cx)), evenInt(int(cy)), evenInt(plan.Width), evenInt(plan.Height))
		}
		if rate > 1.01 {
			chain += fmt.Sprintf(",setpts=PTS/%0.3f", rate)
		}
		chain += fmt.Sprintf("[v%d];", used)
		b.WriteString(chain)
		fmt.Fprintf(&concat, "[v%d]", used)
		used++
	}
	if used == 0 {
		return "", nil, errors.New("no usable polish segments")
	}
	if used == 1 {
		b.WriteString("[v0]format=yuv420p[outv]")
	} else {
		fmt.Fprintf(&b, "%sconcat=n=%d:v=1:a=0,format=yuv420p[outv]", concat.String(), used)
	}
	return b.String(), inputs, nil
}

func escapeFFmpegFilterPath(path string) string {
	path = filepath.ToSlash(path)
	return strings.NewReplacer(`\`, `\\`, `:`, `\:`, `'`, `\'`, `[`, `\[`, `]`, `\]`).Replace(path)
}

func runPolishCommand(args []string, stdout io.Writer) error {
	var input, events, output string
	opts := defaultPolishOptions()
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--input", "-i":
			i++
			if i >= len(args) {
				return errors.New("--input requires a value")
			}
			input = args[i]
		case "--events":
			i++
			if i >= len(args) {
				return errors.New("--events requires a value")
			}
			events = args[i]
		case "--output", "-o":
			i++
			if i >= len(args) {
				return errors.New("--output requires a value")
			}
			output = args[i]
		case "--no-ripples":
			opts.ShowClickRipples = false
		case "--no-keystrokes":
			opts.ShowKeystrokes = false
		case "--no-cursor":
			opts.ShowCursorGhost = false
		case "--no-idle-speedup":
			opts.IdleSpeedup = false
		case "--no-zoom":
			opts.SmartZoom = false
		case "--cursor-style":
			i++
			if i >= len(args) {
				return errors.New("--cursor-style requires a value")
			}
			style, err := parseCursorStyle(args[i])
			if err != nil {
				return err
			}
			opts.CursorStyle = style
		case "--idle-rate":
			// retained for compatibility; classification now drives rates
			i++
			if i >= len(args) {
				return errors.New("--idle-rate requires a value")
			}
		default:
			return fmt.Errorf("unknown polish option: %s", args[i])
		}
	}
	if input == "" {
		return errors.New("record polish requires --input <raw.mp4>")
	}
	if events == "" {
		events = recordEventsPath(input)
	}
	if output == "" {
		output = defaultPolishedOutput(input)
	}
	started := time.Now()
	if err := polishRecording(input, events, output, opts); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "recording polished: input=%s events=%s output=%s elapsed=%s\n",
		input, events, output, time.Since(started).Round(time.Millisecond))
	return nil
}
