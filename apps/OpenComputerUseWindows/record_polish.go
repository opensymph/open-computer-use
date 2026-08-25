package main

// Open-source record polish pipeline. Mirrors the valuable Cursor
// RecordScreen / recording-renderer capabilities using ffmpeg + ASS:
//   - click ripples
//   - keystroke / typed-text captions
//   - idle gap speedup
//   - smart zoom around important clicks
//   - cursor ghost reconstructed from move events
//
// This intentionally does not call the proprietary polished-renderer binary.

import (
	"encoding/json"
	"errors"
	"fmt"
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

type polishOptions struct {
	ShowClickRipples bool
	ShowKeystrokes   bool
	ShowCursorGhost  bool
	IdleSpeedup      bool
	SmartZoom        bool
	MinIdleMs        int64
	IdleRate         float64
	ZoomFactor       float64
	ZoomDurationMs   int64
	MaxZooms         int
}

func defaultPolishOptions() polishOptions {
	return polishOptions{
		ShowClickRipples: true,
		ShowKeystrokes:   true,
		ShowCursorGhost:  true,
		IdleSpeedup:      true,
		SmartZoom:        true,
		MinIdleMs:        1500,
		IdleRate:         3.0,
		ZoomFactor:       1.45,
		ZoomDurationMs:   1400,
		MaxZooms:         8,
	}
}

type polishSegment struct {
	StartMs int64
	EndMs   int64
	Rate    float64
}

type zoomWindow struct {
	StartMs int64
	EndMs   int64
	X       int
	Y       int
	Factor  float64
}

// buildPolishPlan is pure analysis over the event log + duration.
func buildPolishPlan(log recordEventLog, durationMs int64, opts polishOptions) (segments []polishSegment, zooms []zoomWindow, ass string) {
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

	segments = buildIdleSegments(events, durationMs, opts)
	if opts.SmartZoom {
		zooms = selectZoomWindows(events, durationMs, opts)
	}
	ass = buildPolishASS(events, width, height, opts)
	return segments, zooms, ass
}

func buildIdleSegments(events []recordEvent, durationMs int64, opts polishOptions) []polishSegment {
	if !opts.IdleSpeedup || opts.IdleRate <= 1.01 {
		return []polishSegment{{StartMs: 0, EndMs: durationMs, Rate: 1}}
	}
	actionTimes := make([]int64, 0, len(events))
	for _, ev := range events {
		if ev.Type == "wait" {
			continue
		}
		if ev.TMs >= 0 && ev.TMs <= durationMs {
			actionTimes = append(actionTimes, ev.TMs)
		}
	}
	sort.Slice(actionTimes, func(i, j int) bool { return actionTimes[i] < actionTimes[j] })

	var segments []polishSegment
	cursor := int64(0)
	pad := int64(350)
	for _, t := range actionTimes {
		gapStart := cursor
		gapEnd := t - pad
		if gapEnd-gapStart >= opts.MinIdleMs {
			if gapStart > cursor {
				// nothing
			}
			if gapStart < gapEnd {
				segments = append(segments, polishSegment{StartMs: gapStart, EndMs: gapEnd, Rate: opts.IdleRate})
			}
			actionStart := gapEnd
			if actionStart < cursor {
				actionStart = cursor
			}
			actionEnd := t + pad
			if actionEnd > durationMs {
				actionEnd = durationMs
			}
			if actionEnd > actionStart {
				segments = append(segments, polishSegment{StartMs: actionStart, EndMs: actionEnd, Rate: 1})
			}
			cursor = actionEnd
		} else {
			actionEnd := t + pad
			if actionEnd > durationMs {
				actionEnd = durationMs
			}
			if actionEnd > cursor {
				segments = append(segments, polishSegment{StartMs: cursor, EndMs: actionEnd, Rate: 1})
				cursor = actionEnd
			}
		}
	}
	if cursor < durationMs {
		if durationMs-cursor >= opts.MinIdleMs {
			segments = append(segments, polishSegment{StartMs: cursor, EndMs: durationMs, Rate: opts.IdleRate})
		} else {
			segments = append(segments, polishSegment{StartMs: cursor, EndMs: durationMs, Rate: 1})
		}
	}
	if len(segments) == 0 {
		return []polishSegment{{StartMs: 0, EndMs: durationMs, Rate: 1}}
	}
	return mergePolishSegments(segments)
}

func mergePolishSegments(in []polishSegment) []polishSegment {
	if len(in) == 0 {
		return in
	}
	out := []polishSegment{in[0]}
	for _, seg := range in[1:] {
		last := &out[len(out)-1]
		if seg.StartMs <= last.EndMs && almostEqual(seg.Rate, last.Rate) {
			if seg.EndMs > last.EndMs {
				last.EndMs = seg.EndMs
			}
			continue
		}
		if seg.StartMs < last.EndMs {
			seg.StartMs = last.EndMs
		}
		if seg.EndMs <= seg.StartMs {
			continue
		}
		out = append(out, seg)
	}
	return out
}

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 0.001
}

func selectZoomWindows(events []recordEvent, durationMs int64, opts polishOptions) []zoomWindow {
	type candidate struct {
		tMs   int64
		x, y  int
		score int
	}
	var cands []candidate
	lastZoom := int64(-opts.ZoomDurationMs)
	for _, ev := range events {
		score := 0
		x, y := ev.X, ev.Y
		switch ev.Type {
		case "click":
			score = 80
			if ev.Count >= 2 {
				score = 95
			}
		case "drag":
			score = 70
			x, y = ev.X, ev.Y
		case "type", "key":
			score = 40
		default:
			continue
		}
		if score < 60 {
			continue
		}
		if x == 0 && y == 0 && ev.Type != "click" {
			continue
		}
		cands = append(cands, candidate{tMs: ev.TMs, x: x, y: y, score: score})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].tMs < cands[j].tMs
	})

	var zooms []zoomWindow
	for _, c := range cands {
		if len(zooms) >= opts.MaxZooms {
			break
		}
		start := c.tMs - 200
		if start < 0 {
			start = 0
		}
		end := start + opts.ZoomDurationMs
		if end > durationMs {
			end = durationMs
		}
		if start < lastZoom+800 {
			continue
		}
		zooms = append(zooms, zoomWindow{StartMs: start, EndMs: end, X: c.x, Y: c.y, Factor: opts.ZoomFactor})
		lastZoom = start
	}
	sort.Slice(zooms, func(i, j int) bool { return zooms[i].StartMs < zooms[j].StartMs })
	return zooms
}

func buildPolishASS(events []recordEvent, width, height int, opts polishOptions) string {
	var b strings.Builder
	b.WriteString("[Script Info]\n")
	b.WriteString("Title: open-computer-use polish\n")
	b.WriteString("ScriptType: v4.00+\n")
	b.WriteString(fmt.Sprintf("PlayResX: %d\nPlayResY: %d\n\n", width, height))
	b.WriteString("[V4+ Styles]\n")
	b.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	b.WriteString("Style: Keystroke,Menlo,42,&H00FFFFFF,&H000000FF,&H64000000,&H80000000,-1,0,0,0,100,100,0,0,1,2,0,2,40,40,60,1\n")
	b.WriteString("Style: Ripple,Arial,20,&H0000FFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,0,0,5,0,0,0,1\n")
	b.WriteString("Style: Cursor,Arial,28,&H0000D7FF,&H000000FF,&H64000000,&H00000000,-1,0,0,0,100,100,0,0,1,1,0,5,0,0,0,1\n\n")
	b.WriteString("[Events]\n")
	b.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	if opts.ShowKeystrokes {
		writeKeystrokeASS(&b, events)
	}
	if opts.ShowClickRipples {
		writeRippleASS(&b, events)
	}
	if opts.ShowCursorGhost {
		writeCursorGhostASS(&b, events)
	}
	return b.String()
}

func writeKeystrokeASS(b *strings.Builder, events []recordEvent) {
	const displayMs int64 = 1500
	const combineMs int64 = 500
	var pendingText string
	var pendingStart int64
	var pendingEnd int64
	flush := func() {
		if pendingText == "" {
			return
		}
		text := pendingText
		if len([]rune(text)) > 30 {
			runes := []rune(text)
			text = string(runes[:27]) + "…"
		}
		fmt.Fprintf(b, "Dialogue: 0,%s,%s,Keystroke,,0,0,0,,%s\n",
			formatASSTime(pendingStart), formatASSTime(pendingEnd), escapeASS(text))
		pendingText = ""
	}
	for _, ev := range events {
		switch ev.Type {
		case "type":
			if pendingText != "" && ev.TMs-pendingEnd <= combineMs {
				pendingText += ev.Text
				pendingEnd = ev.TMs + displayMs
			} else {
				flush()
				pendingText = ev.Text
				pendingStart = ev.TMs
				pendingEnd = ev.TMs + displayMs
			}
		case "key":
			flush()
			label := keyDisplayLabel(ev.Key)
			fmt.Fprintf(b, "Dialogue: 0,%s,%s,Keystroke,,0,0,0,,%s\n",
				formatASSTime(ev.TMs), formatASSTime(ev.TMs+displayMs), escapeASS(label))
		default:
			flush()
		}
	}
	flush()
}

func writeRippleASS(b *strings.Builder, events []recordEvent) {
	for _, ev := range events {
		if ev.Type != "click" && ev.Type != "drag" {
			continue
		}
		x, y := ev.X, ev.Y
		if ev.Type == "drag" {
			x, y = ev.ToX, ev.ToY
		}
		if x == 0 && y == 0 {
			continue
		}
		// Expanding ring approximation: several ASS drawing circles over ~400ms.
		for i, radius := range []int{12, 22, 34, 48} {
			start := ev.TMs + int64(i*70)
			end := start + 180
			// ASS drawing: m x y + circle-ish box ring using four bezier approx via simple square ring.
			draw := fmt.Sprintf(`{\pos(0,0)\p1\alpha&H60&\c&H00FFFF&}m %d %d l %d %d l %d %d l %d %d l %d %d{\p0}`,
				x-radius, y-radius,
				x+radius, y-radius,
				x+radius, y+radius,
				x-radius, y+radius,
				x-radius, y-radius,
			)
			fmt.Fprintf(b, "Dialogue: 1,%s,%s,Ripple,,0,0,0,,%s\n",
				formatASSTime(start), formatASSTime(end), draw)
		}
		// Center pulse
		fmt.Fprintf(b, "Dialogue: 2,%s,%s,Ripple,,0,0,0,,{\\pos(%d,%d)\\fs28\\c&H00FFFF&●}\n",
			formatASSTime(ev.TMs), formatASSTime(ev.TMs+220), x, y)
	}
}

func writeCursorGhostASS(b *strings.Builder, events []recordEvent) {
	type point struct {
		t       int64
		x, y    int
		visible bool
	}
	var points []point
	for _, ev := range events {
		switch ev.Type {
		case "move", "click":
			if ev.X != 0 || ev.Y != 0 {
				points = append(points, point{t: ev.TMs, x: ev.X, y: ev.Y, visible: true})
			}
		case "drag":
			points = append(points, point{t: ev.TMs, x: ev.X, y: ev.Y, visible: true})
			points = append(points, point{t: ev.TMs + 200, x: ev.ToX, y: ev.ToY, visible: true})
		}
	}
	for i, p := range points {
		end := p.t + 120
		if i+1 < len(points) {
			end = points[i+1].t
		}
		if end <= p.t {
			end = p.t + 80
		}
		fmt.Fprintf(b, "Dialogue: 3,%s,%s,Cursor,,0,0,0,,{\\pos(%d,%d)}▶\n",
			formatASSTime(p.t), formatASSTime(end), p.x, p.y)
	}
}

func keyDisplayLabel(key string) string {
	replacements := map[string]string{
		"Return": "↵ Enter", "Enter": "↵ Enter", "Tab": "⇥ Tab", "Escape": "⎋ Esc",
		"BackSpace": "⌫", "Delete": "⌦ Del", "space": "␣ Space", "Up": "↑", "Down": "↓",
		"Left": "←", "Right": "→", "Home": "⇱ Home", "End": "⇲ End",
		"Page_Up": "⇞ PgUp", "Page_Down": "⇟ PgDn",
		"return": "↵ Enter", "enter": "↵ Enter", "tab": "⇥ Tab", "escape": "⎋ Esc",
		"backspace": "⌫", "delete": "⌦ Del",
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
	return strings.Join(parts, " + ")
}

func escapeASS(text string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `{`, `\[`, `}`, `\]`, "\n", `\N`)
	return replacer.Replace(text)
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

// polishRecording renders overlays + idle speedup + optional zoom into output.
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

	segments, zooms, ass := buildPolishPlan(log, durationMs, opts)
	assPath := inputVideo + ".ass"
	if err := os.WriteFile(assPath, []byte(ass), 0o644); err != nil {
		return err
	}

	filter, err := buildPolishFilterComplex(segments, zooms, assPath, log.Width, log.Height)
	if err != nil {
		return err
	}
	args := []string{
		"-nostdin", "-y",
		"-i", inputVideo,
		"-filter_complex", filter,
		"-map", "[outv]",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "18",
		"-pix_fmt", "yuv420p",
		"-profile:v", "high",
		"-movflags", "+faststart",
		outputVideo,
	}
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
			if len(detail) > 600 {
				detail = detail[len(detail)-600:]
			}
		}
		return fmt.Errorf("ffmpeg polish failed: %v: %s", err, detail)
	}
	return nil
}

func buildPolishFilterComplex(segments []polishSegment, zooms []zoomWindow, assPath string, width, height int) (string, error) {
	if len(segments) == 0 {
		return "", errors.New("no polish segments")
	}
	return buildPolishFilterComplexSplit(segments, zooms, escapeFFmpegFilterPath(assPath), width, height)
}

func buildPolishFilterComplexSplit(segments []polishSegment, zooms []zoomWindow, assEscaped string, width, height int) (string, error) {
	var b strings.Builder
	n := len(segments)
	if n == 0 {
		return "", errors.New("no segments")
	}

	pre := "[0:v]"
	if len(zooms) > 0 {
		z := zooms[0]
		zf := z.Factor
		if zf < 1.05 {
			zf = 1.45
		}
		fmt.Fprintf(&b,
			"[0:v]zoompan=z='if(between(time,%0.3f,%0.3f),%0.3f,1)':x='iw/2-(iw/zoom/2)':y='ih/2-(ih/zoom/2)':d=1:s=%dx%d:fps=30[zoomed];",
			float64(z.StartMs)/1000.0, float64(z.EndMs)/1000.0, zf, width, height,
		)
		pre = "[zoomed]"
	}
	fmt.Fprintf(&b, "%sass=filename='%s'[annotated];", pre, assEscaped)

	if n == 1 && almostEqual(segments[0].Rate, 1) {
		b.WriteString("[annotated]copy[outv]")
		return b.String(), nil
	}

	fmt.Fprintf(&b, "[annotated]split=%d", n)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "[s%d]", i)
	}
	b.WriteString(";")

	concatInputs := strings.Builder{}
	used := 0
	for i, seg := range segments {
		start := float64(seg.StartMs) / 1000.0
		end := float64(seg.EndMs) / 1000.0
		if end <= start {
			continue
		}
		rate := seg.Rate
		if rate < 1.01 {
			rate = 1
		}
		fmt.Fprintf(&b, "[s%d]trim=start=%0.3f:end=%0.3f,setpts=(PTS-STARTPTS)/%0.3f[v%d];", i, start, end, rate, used)
		fmt.Fprintf(&concatInputs, "[v%d]", used)
		used++
	}
	if used == 0 {
		return "", errors.New("no usable polish segments")
	}
	if used == 1 {
		b.WriteString("[v0]copy[outv]")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "%sconcat=n=%d:v=1:a=0[outv]", concatInputs.String(), used)
	return b.String(), nil
}

func escapeFFmpegFilterPath(path string) string {
	path = filepath.ToSlash(path)
	replacer := strings.NewReplacer(`\`, `\\`, `:`, `\:`, `'`, `\'`, `[`, `\[`, `]`, `\]`)
	return replacer.Replace(path)
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
		case "--idle-rate":
			i++
			if i >= len(args) {
				return errors.New("--idle-rate requires a value")
			}
			rate, err := strconv.ParseFloat(args[i], 64)
			if err != nil || rate < 1 {
				return fmt.Errorf("invalid --idle-rate %q", args[i])
			}
			opts.IdleRate = rate
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
