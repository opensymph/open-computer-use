package main

// Clean-room effect implementations aligned with polished-renderer parameters.

import (
	"math"
	"sync"
)

type zoomState struct {
	Scale      float64
	TranslateX float64
	TranslateY float64
	FocalX     float64 // 0..1
	FocalY     float64
}

func identityZoom() zoomState {
	return zoomState{Scale: 1, FocalX: 0.5, FocalY: 0.5}
}

// computeZoomStateAt mirrors polished-renderer zoom_level_and_focus + translation.
func computeZoomStateAt(windows []zoomWindow, timeMs float64, videoW, videoH int) zoomState {
	level, fx, fy, ok := zoomLevelAndFocus(windows, timeMs)
	if !ok || level <= 1.02 {
		return identityZoom()
	}
	w := float64(videoW)
	h := float64(videoH)
	crx := 0.5
	cry := 0.5
	if w > 0 {
		crx = fx / w
	}
	if h > 0 {
		cry = fy / h
	}
	tx := (0.5 - crx) * (level - 1) * w
	ty := (0.5 - cry) * (level - 1) * h
	tx = clampTranslation(tx, level, w)
	ty = clampTranslation(ty, level, h)
	return zoomState{Scale: level, TranslateX: tx, TranslateY: ty, FocalX: crx, FocalY: cry}
}

func clampTranslation(t, zoom, size float64) float64 {
	maxT := (zoom - 1) * size * 0.5
	if t < -maxT {
		return -maxT
	}
	if t > maxT {
		return maxT
	}
	return t
}

func zoomLevelAndFocus(windows []zoomWindow, timeMs float64) (level, fx, fy float64, ok bool) {
	for index, window := range windows {
		var next *zoomWindow
		var prev *zoomWindow
		if index+1 < len(windows) {
			next = &windows[index+1]
		}
		if index > 0 {
			prev = &windows[index-1]
		}
		windowStart := float64(window.StartMs)
		windowEnd := float64(window.EndMs)
		zoomInStart := windowStart - zoomInDurationMs

		if timeMs >= zoomInStart && timeMs < windowStart {
			progress := (timeMs - zoomInStart) / zoomInDurationMs
			eased := zoomInEase(clamp01(progress))
			startZoom := 1.0
			if prev != nil {
				prevEnd := float64(prev.EndMs)
				prevZoomOutEnd := prevEnd + zoomOutDurationMs
				if timeMs < prevZoomOutEnd {
					prevProgress := (timeMs - prevEnd) / zoomOutDurationMs
					prevEased := zoomOutEase(clamp01(prevProgress))
					startZoom = prev.Factor - (prev.Factor-1)*prevEased
				}
			}
			return startZoom + (window.Factor-startZoom)*eased, float64(window.X), float64(window.Y), true
		}

		if timeMs >= windowStart && timeMs <= windowEnd {
			return window.Factor, float64(window.X), float64(window.Y), true
		}

		zoomOutEnd := windowEnd + zoomOutDurationMs
		if timeMs > windowEnd && timeMs <= zoomOutEnd {
			if next != nil {
				nextZoomInStart := float64(next.StartMs) - zoomInDurationMs
				if timeMs >= nextZoomInStart {
					continue
				}
			}
			progress := (timeMs - windowEnd) / zoomOutDurationMs
			eased := zoomOutEase(clamp01(progress))
			return window.Factor - (window.Factor-1)*eased, float64(window.X), float64(window.Y), true
		}
	}
	return 0, 0, 0, false
}

func clamp01(t float64) float64 {
	if t < 0 {
		return 0
	}
	if t > 1 {
		return 1
	}
	return t
}

// applyZoomPan maps output pixels from zoomed view of src into dst.
func applyZoomPan(src, dst *rgbaFrame, z zoomState) {
	if z.Scale <= 1.00001 && math.Abs(z.TranslateX) < 1e-4 && math.Abs(z.TranslateY) < 1e-4 {
		copy(dst.Pix, src.Pix)
		return
	}
	w := float64(src.W)
	h := float64(src.H)
	cx := w * 0.5
	cy := h * 0.5
	inv := 1.0 / z.Scale
	var wg sync.WaitGroup
	workers := 4
	rows := src.H
	chunk := (rows + workers - 1) / workers
	for wi := 0; wi < workers; wi++ {
		y0 := wi * chunk
		y1 := y0 + chunk
		if y1 > rows {
			y1 = rows
		}
		if y0 >= y1 {
			continue
		}
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			for y := y0; y < y1; y++ {
				for x := 0; x < src.W; x++ {
					sx := cx + (float64(x)+0.5-cx-z.TranslateX)*inv - 0.5
					sy := cy + (float64(y)+0.5-cy-z.TranslateY)*inv - 0.5
					r, g, b, a := src.sampleBilinear(sx, sy)
					dst.set(x, y, byte(r+0.5), byte(g+0.5), byte(b+0.5), byte(a+0.5))
				}
			}
		}(y0, y1)
	}
	wg.Wait()
}

type lensParams struct {
	FocalX, FocalY float64
	Perspective    float64
	RotateXDeg     float64
	RotateYDeg     float64
}

func computeLensWarp(zoomLevel float64, focalX, focalY float64) (lensParams, bool) {
	if zoomLevel <= 1.01 {
		return lensParams{}, false
	}
	perspective := lerpClamped(zoomLevel, 1.0, 2.5, 2500.0, 1000.0)
	maxRot := 0.3
	rx := triLerpClamped(focalY, 0, 0.5, 1, maxRot, 0, -maxRot)
	ry := triLerpClamped(focalX, 0, 0.5, 1, -maxRot, 0, maxRot)
	scale := zoomLevel - 1
	return lensParams{
		FocalX: focalX, FocalY: focalY,
		Perspective: perspective,
		RotateXDeg:  rx * scale,
		RotateYDeg:  ry * scale,
	}, true
}

func lerpClamped(x, x0, x1, y0, y1 float64) float64 {
	if math.Abs(x1-x0) < 1e-9 {
		return y0
	}
	t := clamp01((x - x0) / (x1 - x0))
	return y0 + (y1-y0)*t
}

func triLerpClamped(x, x0, x1, x2, y0, y1, y2 float64) float64 {
	if x <= x1 {
		return lerpClamped(x, x0, x1, y0, y1)
	}
	return lerpClamped(x, x1, x2, y1, y2)
}

func mapLensUV(u, v float64, p lensParams, width, height int) (float64, float64, bool) {
	rx := p.RotateXDeg * math.Pi / 180
	ry := p.RotateYDeg * math.Pi / 180
	w := float64(width)
	h := float64(height)
	cx := (u - p.FocalX) * w
	cy := (v - p.FocalY) * h
	zOff := cx*math.Sin(ry) + cy*math.Sin(rx)
	denom := 1.0 + zOff/math.Max(p.Perspective, 1)
	if math.Abs(denom) < 1e-9 {
		return 0, 0, false
	}
	scale := 1.0 / denom
	wu := p.FocalX + (cx*scale)/w
	wv := p.FocalY + (cy*scale)/h
	if wu < 0 || wu > 1 || wv < 0 || wv > 1 {
		return 0, 0, false
	}
	return wu, wv, true
}

func applyLensWarp(src, dst *rgbaFrame, p lensParams) {
	var wg sync.WaitGroup
	workers := 4
	chunk := (src.H + workers - 1) / workers
	for wi := 0; wi < workers; wi++ {
		y0 := wi * chunk
		y1 := y0 + chunk
		if y1 > src.H {
			y1 = src.H
		}
		if y0 >= y1 {
			continue
		}
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			for y := y0; y < y1; y++ {
				for x := 0; x < src.W; x++ {
					u := (float64(x) + 0.5) / float64(src.W)
					v := (float64(y) + 0.5) / float64(src.H)
					su, sv, ok := mapLensUV(u, v, p, src.W, src.H)
					if !ok {
						dst.set(x, y, 0, 0, 0, 255)
						continue
					}
					sx := su*float64(src.W) - 0.5
					sy := sv*float64(src.H) - 0.5
					r, g, b, a := src.sampleBilinear(sx, sy)
					dst.set(x, y, byte(r+0.5), byte(g+0.5), byte(b+0.5), byte(a+0.5))
				}
			}
		}(y0, y1)
	}
	wg.Wait()
}

type motionBlurConfig struct {
	ShutterAngle       float64
	MaxBlurFraction    float64
	CursorReduction    float64
	VelocityThreshold  float64
	MaxSampleCount     int
}

func defaultMotionBlurConfig() motionBlurConfig {
	return motionBlurConfig{
		ShutterAngle:      360,
		MaxBlurFraction:   1.0,
		CursorReduction:   0.4,
		VelocityThreshold: 0.001,
		MaxSampleCount:    12, // slightly below proprietary "high" for speed
	}
}

func (c motionBlurConfig) shutterFraction() float64 {
	return clamp01(c.ShutterAngle / 360.0)
}

func estimateMaxBlurLen(w, h int, curr, prev zoomState, shutter float64) float64 {
	// Corner-based estimate
	corners := [][2]float64{{0.5, 0.5}, {float64(w) - 0.5, 0.5}, {0.5, float64(h) - 0.5}, {float64(w) - 0.5, float64(h) - 0.5}}
	maxLen := 0.0
	cx := float64(w) * 0.5
	cy := float64(h) * 0.5
	currInv := 1.0 / math.Max(curr.Scale, 1e-9)
	for _, cxy := range corners {
		sx := cx + (cxy[0]-cx-curr.TranslateX)*currInv
		sy := cy + (cxy[1]-cy-curr.TranslateY)*currInv
		prevX := cx + (sx-cx)*prev.Scale + prev.TranslateX
		prevY := cy + (sy-cy)*prev.Scale + prev.TranslateY
		bx := (cxy[0] - prevX) * shutter
		by := (cxy[1] - prevY) * shutter
		l := math.Hypot(bx, by)
		if l > maxLen {
			maxLen = l
		}
	}
	return maxLen
}

func applyCameraMotionBlur(src, dst *rgbaFrame, curr, prev zoomState, cfg motionBlurConfig) {
	shutter := cfg.shutterFraction()
	maxBlur := estimateMaxBlurLen(src.W, src.H, curr, prev, shutter)
	if maxBlur < cfg.VelocityThreshold {
		copy(dst.Pix, src.Pix)
		return
	}
	maxBlurPx := cfg.MaxBlurFraction * math.Hypot(float64(src.W), float64(src.H))
	w := float64(src.W)
	h := float64(src.H)
	cx := w * 0.5
	cy := h * 0.5
	currInv := 1.0 / math.Max(curr.Scale, 1e-9)
	prevScale := prev.Scale

	var wg sync.WaitGroup
	workers := 4
	chunk := (src.H + workers - 1) / workers
	for wi := 0; wi < workers; wi++ {
		y0 := wi * chunk
		y1 := y0 + chunk
		if y1 > src.H {
			y1 = src.H
		}
		if y0 >= y1 {
			continue
		}
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			for y := y0; y < y1; y++ {
				yf := float64(y) + 0.5
				sy := cy + (yf-cy-curr.TranslateY)*currInv
				prevY := cy + (sy-cy)*prevScale + prev.TranslateY
				blurY := (yf - prevY) * shutter
				for x := 0; x < src.W; x++ {
					xf := float64(x) + 0.5
					sx := cx + (xf-cx-curr.TranslateX)*currInv
					prevX := cx + (sx-cx)*prevScale + prev.TranslateX
					bx := (xf - prevX) * shutter
					by := blurY
					blurLen := math.Hypot(bx, by)
					if blurLen < 0.001 {
						r, g, b, a := src.at(x, y)
						dst.set(x, y, r, g, b, a)
						continue
					}
					if maxBlurPx > 0 && blurLen > maxBlurPx {
						s := maxBlurPx / blurLen
						bx *= s
						by *= s
						blurLen = maxBlurPx
					}
					samples := int(math.Ceil(blurLen / 0.75))
					if samples < 4 {
						samples = 4
					}
					if samples > cfg.MaxSampleCount {
						samples = cfg.MaxSampleCount
					}
					jitter := gradientNoise(float64(x), float64(y))
					inv := 1.0 / float64(samples)
					dirX := bx / blurLen
					dirY := by / blurLen
					var ar, ag, ab, aa float64
					for i := 0; i < samples; i++ {
						t := ((float64(i)+jitter)*inv - 0.5) * blurLen
						r, g, b, a := src.sampleBilinear(float64(x)+dirX*t, float64(y)+dirY*t)
						ar += r
						ag += g
						ab += b
						aa += a
					}
					dst.set(x, y, byte(ar*inv+0.5), byte(ag*inv+0.5), byte(ab*inv+0.5), byte(aa*inv+0.5))
				}
			}
		}(y0, y1)
	}
	wg.Wait()
}
