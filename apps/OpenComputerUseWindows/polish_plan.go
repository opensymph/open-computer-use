package main

// Clean-room render-plan JSON (aligned with polished-renderer plan/types shape).
// Used for export/on-disk inspection and optional --plan polish input.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type renderPlanJSON struct {
	Video     renderPlanVideo     `json:"video"`
	Playback  renderPlanPlayback  `json:"playback"`
	Tracks    renderPlanTracks    `json:"tracks"`
	Diagnostics renderPlanDiagnostics `json:"diagnostics"`
}

type renderPlanVideo struct {
	InputVideoPath    string  `json:"inputVideoPath"`
	SourceDurationMs  float64 `json:"sourceDurationMs"`
	OutputDurationMs  float64 `json:"outputDurationMs"`
	Width             int     `json:"width"`
	Height            int     `json:"height"`
	FPS               int     `json:"fps"`
	ConfigHash        string  `json:"configHash"`
}

type renderPlanPlayback struct {
	Segments         []renderPlanSegment `json:"segments"`
	OutputDurationMs float64             `json:"outputDurationMs"`
	SourceDurationMs float64             `json:"sourceDurationMs"`
}

type renderPlanSegment struct {
	Type              string  `json:"type"` // action|gap
	SourceStartMs     float64 `json:"sourceStartMs"`
	SourceEndMs       float64 `json:"sourceEndMs"`
	SourceDurationMs  float64 `json:"sourceDurationMs"`
	OutputStartMs     float64 `json:"outputStartMs"`
	OutputEndMs       float64 `json:"outputEndMs"`
	OutputDurationMs  float64 `json:"outputDurationMs"`
	PlaybackRate      float64 `json:"playbackRate"`
}

type renderPlanTracks struct {
	ClickEffects     []renderPlanClick  `json:"clickEffects"`
	KeystrokeEvents  []renderPlanKey    `json:"keystrokeEvents"`
	ZoomWindows      []renderPlanZoom   `json:"zoomWindows"`
	CursorStyle      string             `json:"cursorStyle"`
	CursorPaths      []renderPlanCursor `json:"cursorPaths,omitempty"`
}

type renderPlanClick struct {
	TimeMs float64 `json:"timeMs"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Score  int     `json:"score,omitempty"`
}

type renderPlanKey struct {
	TimeMs    float64 `json:"timeMs"`
	Text      string  `json:"text,omitempty"`
	Key       string  `json:"key,omitempty"`
	EventType string  `json:"eventType,omitempty"`
}

type renderPlanZoom struct {
	StartMs float64 `json:"startMs"`
	EndMs   float64 `json:"endMs"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Factor  float64 `json:"factor"`
}

type renderPlanCursor struct {
	Style     string                 `json:"style"`
	Keyframes []renderPlanCursorKF   `json:"keyframes"`
}

type renderPlanCursorKF struct {
	TimeMs     float64 `json:"timeMs"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	CursorType string  `json:"cursorType,omitempty"`
	Scale      float64 `json:"scale,omitempty"`
}

type renderPlanDiagnostics struct {
	Warnings []string `json:"warnings"`
	Errors   []string `json:"errors"`
}

func cursorStyleName(s cursorStyle) string {
	switch s {
	case cursorStyleSlow:
		return "slow"
	case cursorStyleQuick:
		return "quick"
	case cursorStyleRapid:
		return "rapid"
	default:
		return "mellow"
	}
}

func buildRenderPlanJSON(inputVideo string, log recordEventLog, durationMs int64, opts polishOptions, plan polishPlan, segs []polishSegment) renderPlanJSON {
	outDur := float64(outputDurationFromSegments(segs))
	srcDur := float64(durationMs)
	var outCursor float64
	jsegs := make([]renderPlanSegment, 0, len(segs))
	for _, s := range segs {
		rate := s.Rate
		if rate < 1.01 {
			rate = 1
		}
		src0 := float64(s.StartMs)
		src1 := float64(s.EndMs)
		srcD := src1 - src0
		outD := srcD / rate
		typ := "action"
		if rate > 1.01 {
			typ = "gap"
		}
		jsegs = append(jsegs, renderPlanSegment{
			Type: typ,
			SourceStartMs: src0, SourceEndMs: src1, SourceDurationMs: srcD,
			OutputStartMs: outCursor, OutputEndMs: outCursor + outD, OutputDurationMs: outD,
			PlaybackRate: rate,
		})
		outCursor += outD
	}
	clicks := make([]renderPlanClick, 0, len(plan.Clicks))
	for _, c := range plan.Clicks {
		clicks = append(clicks, renderPlanClick{TimeMs: float64(c.TMs), X: float64(c.X), Y: float64(c.Y), Score: c.Score})
	}
	zooms := make([]renderPlanZoom, 0, len(plan.Zooms))
	for _, z := range plan.Zooms {
		zooms = append(zooms, renderPlanZoom{
			StartMs: float64(z.StartMs), EndMs: float64(z.EndMs),
			X: float64(z.X), Y: float64(z.Y), Factor: z.Factor,
		})
	}
	var keys []renderPlanKey
	for _, ev := range log.Events {
		switch ev.Type {
		case "key":
			keys = append(keys, renderPlanKey{TimeMs: float64(ev.TMs), Key: ev.Key, EventType: "keyCombo"})
		case "type":
			keys = append(keys, renderPlanKey{TimeMs: float64(ev.TMs), Text: ev.Text, EventType: "textTyped"})
		}
	}
	ckf := make([]renderPlanCursorKF, 0, len(plan.Cursor))
	for _, kf := range plan.Cursor {
		ckf = append(ckf, renderPlanCursorKF{
			TimeMs: float64(kf.TMs), X: float64(kf.X), Y: float64(kf.Y),
			CursorType: kf.CursorType, Scale: kf.Scale,
		})
	}
	w, h := plan.Width, plan.Height
	if w == 0 {
		w = log.Width
	}
	if h == 0 {
		h = log.Height
	}
	fps := log.FPS
	if fps <= 0 {
		fps = 30
	}
	return renderPlanJSON{
		Video: renderPlanVideo{
			InputVideoPath: inputVideo, SourceDurationMs: srcDur, OutputDurationMs: outDur,
			Width: w, Height: h, FPS: fps, ConfigHash: "ocu-cleanroom-v1",
		},
		Playback: renderPlanPlayback{Segments: jsegs, OutputDurationMs: outDur, SourceDurationMs: srcDur},
		Tracks: renderPlanTracks{
			ClickEffects: clicks, KeystrokeEvents: keys, ZoomWindows: zooms,
			CursorStyle: cursorStyleName(opts.CursorStyle),
			CursorPaths: []renderPlanCursor{{Style: cursorStyleName(opts.CursorStyle), Keyframes: ckf}},
		},
		Diagnostics: renderPlanDiagnostics{Warnings: []string{}, Errors: []string{}},
	}
}

func writeRenderPlanJSON(path string, plan renderPlanJSON) error {
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func defaultRenderPlanPath(inputVideo string) string {
	ext := filepath.Ext(inputVideo)
	if ext == "" {
		return inputVideo + ".render-plan.json"
	}
	return strings.TrimSuffix(inputVideo, ext) + ".render-plan.json"
}

func loadRenderPlanJSON(path string) (renderPlanJSON, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return renderPlanJSON{}, err
	}
	var plan renderPlanJSON
	if err := json.Unmarshal(data, &plan); err != nil {
		return renderPlanJSON{}, err
	}
	return plan, nil
}

func applyRenderPlanToPolish(plan renderPlanJSON, opts *polishOptions) ([]polishSegment, []zoomWindow, []cursorKeyframe, error) {
	if opts == nil {
		return nil, nil, nil, fmt.Errorf("nil polish options")
	}
	if plan.Tracks.CursorStyle != "" {
		if style, err := parseCursorStyle(plan.Tracks.CursorStyle); err == nil {
			opts.CursorStyle = style
		}
	}
	segs := make([]polishSegment, 0, len(plan.Playback.Segments))
	for _, s := range plan.Playback.Segments {
		segs = append(segs, polishSegment{
			StartMs: int64(s.SourceStartMs + 0.5),
			EndMs:   int64(s.SourceEndMs + 0.5),
			Rate:    s.PlaybackRate,
		})
	}
	zooms := make([]zoomWindow, 0, len(plan.Tracks.ZoomWindows))
	for _, z := range plan.Tracks.ZoomWindows {
		zooms = append(zooms, zoomWindow{
			StartMs: int64(z.StartMs + 0.5), EndMs: int64(z.EndMs + 0.5),
			X: int(z.X + 0.5), Y: int(z.Y + 0.5), Factor: z.Factor,
		})
	}
	var cursor []cursorKeyframe
	if len(plan.Tracks.CursorPaths) > 0 {
		for _, kf := range plan.Tracks.CursorPaths[0].Keyframes {
			scale := kf.Scale
			if scale <= 0 {
				scale = 1
			}
			cursor = append(cursor, cursorKeyframe{
				TMs: int64(kf.TimeMs + 0.5), X: int(kf.X + 0.5), Y: int(kf.Y + 0.5),
				Scale: scale, CursorType: kf.CursorType,
			})
		}
	}
	return segs, zooms, cursor, nil
}
