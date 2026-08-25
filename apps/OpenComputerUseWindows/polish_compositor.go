package main

// Clean-room frame compositor engine (default for record polish).
// Pipeline mirrors polished-renderer:
//   remap idle timeline → zoom → lens warp → camera motion blur →
//   cursor (+depress + cursor motion blur) → keystroke chips → encode
//
// Does not vendor proprietary source; parameters were re-derived from public dumps.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type polishEngineKind int

const (
	polishEngineCompositor polishEngineKind = iota + 1
	polishEngineFFmpeg
)

func parsePolishEngine(v string) (polishEngineKind, error) {
	switch v {
	case "", "compositor", "default":
		return polishEngineCompositor, nil
	case "ffmpeg", "filter", "legacy":
		return polishEngineFFmpeg, nil
	default:
		return 0, fmt.Errorf("invalid --engine %q (compositor|ffmpeg)", v)
	}
}

type compositorOptions struct {
	polishOptions
	Engine     polishEngineKind
	MotionBlur motionBlurConfig
	FPS        int
}

func defaultCompositorOptions() compositorOptions {
	return compositorOptions{
		polishOptions: defaultPolishOptions(),
		Engine:        polishEngineCompositor,
		MotionBlur:    defaultMotionBlurConfig(),
		FPS:           30,
	}
}

func polishRecordingCompositor(inputVideo, eventsPath, outputVideo string, opts compositorOptions) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg is required for compositor polish")
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
	if opts.FPS <= 0 {
		opts.FPS = 30
	}

	events := append([]recordEvent(nil), log.Events...)
	sort.Slice(events, func(i, j int) bool { return events[i].TMs < events[j].TMs })
	clicks := analyzeClickEffects(events, log.Width, log.Height)
	idles := detectIdlePeriods(events, durationMs, opts.polishOptions)
	var zooms []zoomWindow
	if opts.SmartZoom {
		zooms = selectZoomWindowsFromClicks(clicks, durationMs, opts.polishOptions)
	}
	segments := buildPlaybackSegments(idles, zooms, durationMs, opts.polishOptions)
	cursorPath := []cursorKeyframe{}
	if opts.ShowCursorGhost {
		cursorPath = generateCursorPath(events, durationMs, log.Width, log.Height, opts.CursorStyle)
	}

	workDir, err := os.MkdirTemp("", "ocu-comp-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	linearPath := filepath.Join(workDir, "linear.mp4")
	if err := renderLinearTimeline(inputVideo, linearPath, segments); err != nil {
		return err
	}

	srcToOut := buildSourceToOutputMapper(segments)
	linearDur, _, _, err := probeVideoDurationMs(linearPath)
	if err != nil {
		return err
	}
	outDurationMs := linearDur
	if outDurationMs < 1 {
		outDurationMs = outputDurationFromSegments(segments)
	}

	outCursor := remapCursorPath(cursorPath, srcToOut, outDurationMs)
	outZooms := remapZooms(zooms, srcToOut)
	outChips := buildKeystrokeChips(events, func(srcMs int64) float64 {
		return srcToOut(float64(srcMs))
	})
	if !opts.ShowKeystrokes {
		outChips = nil
	}

	return renderCompositorPass(linearPath, outputVideo, width, height, opts.FPS, outDurationMs, outZooms, outCursor, outChips, opts)
}

func outputDurationFromSegments(segs []polishSegment) int64 {
	var out float64
	for _, s := range segs {
		dur := float64(s.EndMs - s.StartMs)
		rate := s.Rate
		if rate < 1.01 {
			rate = 1
		}
		out += dur / rate
	}
	return int64(out + 0.5)
}

func buildSourceToOutputMapper(segs []polishSegment) func(float64) float64 {
	type piece struct {
		src0, src1, out0, out1 float64
	}
	var pieces []piece
	var outCursor float64
	for _, s := range segs {
		rate := s.Rate
		if rate < 1.01 {
			rate = 1
		}
		src0 := float64(s.StartMs)
		src1 := float64(s.EndMs)
		outDur := (src1 - src0) / rate
		pieces = append(pieces, piece{src0, src1, outCursor, outCursor + outDur})
		outCursor += outDur
	}
	return func(srcMs float64) float64 {
		for _, p := range pieces {
			if srcMs >= p.src0 && srcMs <= p.src1 {
				if p.src1 <= p.src0 {
					return p.out0
				}
				u := (srcMs - p.src0) / (p.src1 - p.src0)
				return p.out0 + u*(p.out1-p.out0)
			}
		}
		if len(pieces) == 0 {
			return srcMs
		}
		if srcMs < pieces[0].src0 {
			return pieces[0].out0
		}
		return pieces[len(pieces)-1].out1
	}
}

func remapCursorPath(path []cursorKeyframe, mapFn func(float64) float64, outDur int64) []cursorKeyframe {
	out := make([]cursorKeyframe, 0, len(path))
	for _, kf := range path {
		t := int64(mapFn(float64(kf.TMs)) + 0.5)
		if t < 0 {
			t = 0
		}
		if t > outDur {
			t = outDur
		}
		out = append(out, cursorKeyframe{TMs: t, X: kf.X, Y: kf.Y, Scale: kf.Scale})
	}
	return out
}

func remapZooms(zooms []zoomWindow, mapFn func(float64) float64) []zoomWindow {
	out := make([]zoomWindow, 0, len(zooms))
	for _, z := range zooms {
		out = append(out, zoomWindow{
			StartMs: int64(mapFn(float64(z.StartMs)) + 0.5),
			EndMs:   int64(mapFn(float64(z.EndMs)) + 0.5),
			X:       z.X, Y: z.Y, Factor: z.Factor, Score: z.Score,
		})
	}
	return out
}

func renderLinearTimeline(input, output string, segs []polishSegment) error {
	if len(segs) == 0 {
		segs = []polishSegment{{StartMs: 0, EndMs: 1, Rate: 1}}
	}
	n := len(segs)
	var b strings.Builder
	fmt.Fprintf(&b, "[0:v]split=%d", n)
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
		if rate > 1.01 {
			chain += fmt.Sprintf(",setpts=PTS/%0.3f", rate)
		}
		chain += fmt.Sprintf("[v%d];", used)
		b.WriteString(chain)
		fmt.Fprintf(&concat, "[v%d]", used)
		used++
	}
	if used == 0 {
		return fmt.Errorf("no usable segments for linear timeline")
	}
	if used == 1 {
		b.WriteString("[v0]format=yuv420p[outv]")
	} else {
		fmt.Fprintf(&b, "%sconcat=n=%d:v=1:a=0,format=yuv420p[outv]", concat.String(), used)
	}
	cmd := exec.Command("ffmpeg", "-nostdin", "-y", "-i", input,
		"-filter_complex", b.String(),
		"-map", "[outv]",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "18",
		"-pix_fmt", "yuv420p", "-an", output)
	logPath := output + ".log"
	lf, _ := os.Create(logPath)
	if lf != nil {
		cmd.Stdout = lf
		cmd.Stderr = lf
		defer lf.Close()
	}
	if err := cmd.Run(); err != nil {
		detail, _ := os.ReadFile(logPath)
		return fmt.Errorf("linear timeline failed: %v: %s", err, trimTail(string(detail), 600))
	}
	return nil
}

func trimTail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func renderCompositorPass(linearVideo, output string, width, height, fps int, outDurationMs int64, zooms []zoomWindow, cursor []cursorKeyframe, chips []keystrokeChip, opts compositorOptions) error {
	frameCount := int(outDurationMs) * fps / 1000
	if frameCount < 1 {
		frameCount = 1
	}
	frameBytes := width * height * 4

	dec := exec.Command("ffmpeg", "-nostdin", "-i", linearVideo,
		"-f", "rawvideo", "-pix_fmt", "rgba", "-an", "-")
	decOut, err := dec.StdoutPipe()
	if err != nil {
		return err
	}
	decErr, _ := os.Create(output + ".dec.log")
	if decErr != nil {
		dec.Stderr = decErr
		defer decErr.Close()
	}
	if err := dec.Start(); err != nil {
		return err
	}

	enc := exec.Command("ffmpeg", "-nostdin", "-y",
		"-f", "rawvideo", "-pix_fmt", "rgba", "-s", fmt.Sprintf("%dx%d", width, height),
		"-r", fmt.Sprintf("%d", fps), "-i", "-",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "18",
		"-pix_fmt", "yuv420p", "-profile:v", "high", "-movflags", "+faststart",
		output)
	encIn, err := enc.StdinPipe()
	if err != nil {
		_ = dec.Process.Kill()
		return err
	}
	encLog, _ := os.Create(output + ".comp.log")
	if encLog != nil {
		enc.Stdout = encLog
		enc.Stderr = encLog
		defer encLog.Close()
	}
	if err := enc.Start(); err != nil {
		_ = dec.Process.Kill()
		return err
	}

	cursorSprite := newCompositorCursor()
	src := newRGBAFrame(width, height)
	zoomed := newRGBAFrame(width, height)
	warped := newRGBAFrame(width, height)
	blurred := newRGBAFrame(width, height)
	buf := make([]byte, frameBytes)

	var prevCursor *cursorKeyframe
	prevZoom := identityZoom()
	frameMs := 1000.0 / float64(fps)

	for fi := 0; fi < frameCount; fi++ {
		if _, err := io.ReadFull(decOut, buf); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				if fi == 0 {
					_ = encIn.Close()
					_ = dec.Wait()
					_ = enc.Wait()
					return fmt.Errorf("compositor: no frames decoded from linear video")
				}
				break
			}
			_ = encIn.Close()
			_ = dec.Process.Kill()
			_ = enc.Process.Kill()
			return err
		}
		copy(src.Pix, buf)
		tMs := float64(fi) * frameMs

		z := computeZoomStateAt(zooms, tMs, width, height)
		applyZoomPan(src, zoomed, z)

		scene := zoomed
		if lp, ok := computeLensWarp(z.Scale, z.FocalX, z.FocalY); ok {
			applyLensWarp(zoomed, warped, lp)
			scene = warped
		}

		applyCameraMotionBlur(scene, blurred, z, prevZoom, opts.MotionBlur)
		zoomForCursorPrev := prevZoom
		prevZoom = z

		if opts.ShowCursorGhost && len(cursor) > 0 {
			kf := cursorAt(cursor, tMs)
			if prevCursor != nil {
				overlayCursorWithMotionBlur(blurred, cursorSprite, kf, *prevCursor, z, zoomForCursorPrev, width, opts.MotionBlur)
			} else {
				overlayCursor(blurred, cursorSprite, kf, z, width)
			}
			cp := kf
			prevCursor = &cp
		}

		if text, op, ok := keystrokeOpacity(chips, tMs); ok {
			overlayKeystrokeChip(blurred, text, op)
		}

		if _, err := encIn.Write(blurred.Pix); err != nil {
			_ = encIn.Close()
			_ = dec.Process.Kill()
			_ = enc.Process.Kill()
			return err
		}
	}
	_ = encIn.Close()
	_ = dec.Wait()
	if err := enc.Wait(); err != nil {
		detail, _ := os.ReadFile(output + ".comp.log")
		return fmt.Errorf("compositor encode failed: %v: %s", err, trimTail(string(detail), 600))
	}
	return nil
}

func cursorAt(path []cursorKeyframe, tMs float64) cursorKeyframe {
	if len(path) == 0 {
		return cursorKeyframe{Scale: 1}
	}
	if tMs <= float64(path[0].TMs) {
		return path[0]
	}
	for i := 0; i+1 < len(path); i++ {
		a, b := path[i], path[i+1]
		if tMs >= float64(a.TMs) && tMs <= float64(b.TMs) {
			dt := float64(b.TMs - a.TMs)
			if dt < 1 {
				return b
			}
			u := (tMs - float64(a.TMs)) / dt
			return cursorKeyframe{
				TMs:   int64(tMs + 0.5),
				X:     int(float64(a.X) + (float64(b.X)-float64(a.X))*u + 0.5),
				Y:     int(float64(a.Y) + (float64(b.Y)-float64(a.Y))*u + 0.5),
				Scale: a.Scale + (b.Scale-a.Scale)*u,
			}
		}
	}
	return path[len(path)-1]
}
