package main

// Cursor + keystroke overlays for the clean-room compositor.

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

type compositorCursor struct {
	sprite *rgbaFrame
	baseW  int
	baseH  int
}

func newCompositorCursor() *compositorCursor {
	return &compositorCursor{sprite: buildCursorSprite("arrow"), baseW: 48, baseH: 48}
}

func buildCursorSprite(cursorType string) *rgbaFrame {
	const size = 48
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	switch strings.ToLower(cursorType) {
	case "text", "ibeam":
		drawIBeam(img, color.NRGBA{20, 20, 20, 255})
	case "wait", "progress":
		drawWaitCursor(img)
	case "crosshair":
		drawCrosshair(img)
	case "pointer", "hand":
		drawArrow(img, 3, 4, color.NRGBA{0, 0, 0, 90})
		drawArrow(img, 1, 1, color.NRGBA{255, 255, 255, 255})
		drawArrow(img, 1, 1, color.NRGBA{20, 20, 20, 255})
	default: // arrow
		// Hotspot aligned to proprietary SVG ratio 3/24, 2/24 of viewBox.
		drawArrow(img, 3, 4, color.NRGBA{0, 0, 0, 90})
		drawArrow(img, 1, 1, color.NRGBA{255, 255, 255, 255})
		drawArrow(img, 0, 0, color.NRGBA{255, 255, 255, 255})
		drawArrow(img, 2, 0, color.NRGBA{255, 255, 255, 255})
		drawArrow(img, 0, 2, color.NRGBA{255, 255, 255, 255})
		drawArrow(img, 1, 1, color.NRGBA{20, 20, 20, 255})
	}
	f := newRGBAFrame(size, size)
	copy(f.Pix, img.Pix)
	return f
}

func drawIBeam(img *image.NRGBA, c color.NRGBA) {
	// Vertical bar with serifs — text caret.
	for y := 8; y <= 40; y++ {
		img.SetNRGBA(24, y, c)
		img.SetNRGBA(23, y, color.NRGBA{255, 255, 255, 200})
		img.SetNRGBA(25, y, color.NRGBA{255, 255, 255, 200})
	}
	for x := 18; x <= 30; x++ {
		img.SetNRGBA(x, 8, c)
		img.SetNRGBA(x, 40, c)
	}
}

func drawWaitCursor(img *image.NRGBA) {
	c := color.NRGBA{20, 20, 20, 255}
	o := color.NRGBA{255, 255, 255, 255}
	cx, cy, r := 24, 24, 12
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			d := (x-cx)*(x-cx) + (y-cy)*(y-cy)
			if d <= r*r && d >= (r-2)*(r-2) {
				img.SetNRGBA(x, y, o)
			}
			if d <= (r-2)*(r-2) && d >= (r-4)*(r-4) {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

func drawCrosshair(img *image.NRGBA) {
	c := color.NRGBA{20, 20, 20, 255}
	for i := 8; i <= 40; i++ {
		img.SetNRGBA(24, i, c)
		img.SetNRGBA(i, 24, c)
	}
}

func cursorHotspotRatio(cursorType string) (hx, hy float64) {
	switch strings.ToLower(cursorType) {
	case "text", "ibeam":
		return 0.5, 0.5
	case "wait", "crosshair":
		return 0.5, 0.5
	default:
		// Proprietary SVG viewBox hotspot ≈ (3/24, 2/24).
		return 3.0 / 24.0, 2.0 / 24.0
	}
}

func drawArrow(img *image.NRGBA, ox, oy int, c color.NRGBA) {
	// Classic NW pointer polygon scaled into 48px with hotspot ~(4,4).
	pts := [][2]int{
		{4 + ox, 4 + oy}, {4 + ox, 36 + oy}, {12 + ox, 28 + oy},
		{20 + ox, 42 + oy}, {26 + ox, 39 + oy}, {16 + ox, 26 + oy},
		{30 + ox, 26 + oy},
	}
	minX, minY, maxX, maxY := 48, 48, 0, 0
	for _, p := range pts {
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
			if pointInPoly(x, y, pts) {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

func overlayCursor(dst *rgbaFrame, sprite *compositorCursor, state cursorKeyframe, zoom zoomState, videoW int) {
	if sprite == nil {
		return
	}
	spr := sprite.sprite
	if state.CursorType != "" && state.CursorType != "arrow" {
		spr = buildCursorSprite(state.CursorType)
	}
	if spr == nil {
		return
	}
	scale := state.Scale
	if scale <= 0 {
		scale = 1
	}
	// Base cursor ~32px at 1920 width (proprietary BASE_CURSOR_SIZE_PX).
	base := 32.0 * (float64(videoW) / 1920.0) * 1.25
	size := base * scale
	if size < 8 {
		size = 8
	}
	tipX, tipY := cursorTipInFrame(float64(state.X), float64(state.Y), zoom, dst.W, dst.H)
	hx, hy := cursorHotspotRatio(state.CursorType)
	hotX := size * hx
	hotY := size * hy
	originX := tipX - hotX
	originY := tipY - hotY

	sw := float64(spr.W)
	sh := float64(spr.H)
	x0 := int(math.Floor(originX))
	y0 := int(math.Floor(originY))
	x1 := int(math.Ceil(originX + size))
	y1 := int(math.Ceil(originY + size))
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if x < 0 || y < 0 || x >= dst.W || y >= dst.H {
				continue
			}
			u := ((float64(x)+0.5 - originX) / size) * sw
			v := ((float64(y)+0.5 - originY) / size) * sh
			sr, sg, sb, sa := spr.sampleBilinear(u-0.5, v-0.5)
			if sa < 1 {
				continue
			}
			a := sa / 255.0
			dr, dg, db, _ := dst.at(x, y)
			nr := float64(dr)*(1-a) + sr*a
			ng := float64(dg)*(1-a) + sg*a
			nb := float64(db)*(1-a) + sb*a
			dst.set(x, y, byte(nr+0.5), byte(ng+0.5), byte(nb+0.5), 255)
		}
	}
}

func cursorTipInFrame(srcX, srcY float64, z zoomState, w, h int) (float64, float64) {
	// Inverse of zoom pan: output = (src - center)*scale + center + translate ... 
	// Proprietary applies zoom first then overlays cursor in zoomed space.
	cx := float64(w) * 0.5
	cy := float64(h) * 0.5
	tx := (srcX-cx)*z.Scale + cx + z.TranslateX
	ty := (srcY-cy)*z.Scale + cy + z.TranslateY
	return tx, ty
}

func overlayCursorWithMotionBlur(dst *rgbaFrame, sprite *compositorCursor, curr, prev cursorKeyframe, currZ, prevZ zoomState, videoW int, cfg motionBlurConfig) {
	if sprite == nil {
		return
	}
	cx, cy := cursorTipInFrame(float64(curr.X), float64(curr.Y), currZ, dst.W, dst.H)
	px, py := cursorTipInFrame(float64(prev.X), float64(prev.Y), prevZ, dst.W, dst.H)
	vx := cx - px
	vy := cy - py
	shutter := cfg.shutterFraction()
	red := cfg.CursorReduction
	blurX := vx * shutter * red
	blurY := vy * shutter * red
	blurLen := math.Hypot(blurX, blurY)
	if blurLen < cfg.VelocityThreshold {
		overlayCursor(dst, sprite, curr, currZ, videoW)
		return
	}
	samples := int(math.Ceil(blurLen / 1.5))
	if samples < 3 {
		samples = 3
	}
	if samples > 8 {
		samples = 8
	}
	for i := 0; i < samples; i++ {
		t := (float64(i) + 0.5) / float64(samples)
		// trail from prev toward curr
		kf := curr
		kf.X = int(float64(prev.X) + (float64(curr.X)-float64(prev.X))*t + 0.5)
		kf.Y = int(float64(prev.Y) + (float64(curr.Y)-float64(prev.Y))*t + 0.5)
		kf.Scale = prev.Scale + (curr.Scale-prev.Scale)*t
		// fade trail
		overlayCursorAlpha(dst, sprite, kf, currZ, videoW, 0.35+0.65*t)
	}
}

func overlayCursorAlpha(dst *rgbaFrame, sprite *compositorCursor, state cursorKeyframe, zoom zoomState, videoW int, alphaMul float64) {
	if alphaMul <= 0 {
		return
	}
	if sprite == nil || sprite.sprite == nil {
		return
	}
	scale := state.Scale
	if scale <= 0 {
		scale = 1
	}
	base := 32.0 * (float64(videoW) / 1920.0) * 1.25
	size := base * scale
	tipX, tipY := cursorTipInFrame(float64(state.X), float64(state.Y), zoom, dst.W, dst.H)
	hotX := size * (4.0 / 48.0)
	hotY := size * (4.0 / 48.0)
	originX := tipX - hotX
	originY := tipY - hotY
	sw := float64(sprite.sprite.W)
	sh := float64(sprite.sprite.H)
	x0 := int(math.Floor(originX))
	y0 := int(math.Floor(originY))
	x1 := int(math.Ceil(originX + size))
	y1 := int(math.Ceil(originY + size))
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if x < 0 || y < 0 || x >= dst.W || y >= dst.H {
				continue
			}
			u := ((float64(x)+0.5 - originX) / size) * sw
			v := ((float64(y)+0.5 - originY) / size) * sh
			sr, sg, sb, sa := sprite.sprite.sampleBilinear(u-0.5, v-0.5)
			a := (sa / 255.0) * alphaMul
			if a < 0.01 {
				continue
			}
			dr, dg, db, _ := dst.at(x, y)
			dst.set(x, y,
				byte(float64(dr)*(1-a)+sr*a+0.5),
				byte(float64(dg)*(1-a)+sg*a+0.5),
				byte(float64(db)*(1-a)+sb*a+0.5),
				255)
		}
	}
}

// --- keystroke chips ---

const (
	keyFadeInMs  = 150.0
	keyHoldMs    = 1200.0
	keyFadeOutMs = 400.0
)

type keystrokeChip struct {
	text      string
	showStart float64 // ms on output timeline
	peakAt    float64
}

func buildKeystrokeChips(events []recordEvent, timeMap func(srcMs int64) float64) []keystrokeChip {
	var chips []keystrokeChip
	for _, ev := range events {
		var label string
		switch ev.Type {
		case "key":
			label = keyDisplayLabel(ev.Key)
		case "type":
			// proprietary skips raw TextTyped for overlay chips; show short typed bursts
			if strings.TrimSpace(ev.Text) == "" {
				continue
			}
			label = ev.Text
			if len([]rune(label)) > 24 {
				label = string([]rune(label)[:21]) + "…"
			}
		default:
			continue
		}
		t := timeMap(ev.TMs)
		chips = append(chips, keystrokeChip{
			text:      label,
			showStart: t - keyFadeInMs,
			peakAt:    t,
		})
	}
	return chips
}

func keystrokeOpacity(chips []keystrokeChip, timeMs float64) (text string, opacity float64, ok bool) {
	var active *keystrokeChip
	for i := range chips {
		c := &chips[i]
		if timeMs >= c.showStart {
			active = c
		}
	}
	if active == nil {
		return "", 0, false
	}
	elapsed := timeMs - active.showStart
	total := keyFadeInMs + keyHoldMs + keyFadeOutMs
	if elapsed > total {
		return "", 0, false
	}
	// preempted by next?
	for i := range chips {
		if chips[i].showStart > active.showStart && timeMs >= chips[i].showStart {
			return "", 0, false
		}
	}
	fadeOutStart := keyFadeInMs + keyHoldMs
	var op float64
	if elapsed <= keyFadeInMs {
		op = remotionEase(elapsed / keyFadeInMs)
	} else if elapsed > fadeOutStart {
		t := clamp01((elapsed - fadeOutStart) / keyFadeOutMs)
		op = remotionEase(1 - t)
	} else {
		op = 1
	}
	return active.text, op, op > 0.01
}

func remotionEase(t float64) float64 {
	// Approx remotion ease used by proprietary keystrokes (smoothstep-ish bezier).
	return cubicBezier(0.33, 1, 0.68, 1, clamp01(t))
}

func overlayKeystrokeChip(dst *rgbaFrame, text string, opacity float64) {
	if opacity <= 0 || text == "" {
		return
	}
	scale := (float64(dst.W) / 1920.0) * 2.0
	face := basicfont.Face7x13
	width := font.MeasureString(face, text).Ceil()
	padX := int(18 * scale)
	padY := int(12 * scale)
	pillW := width + padX*2
	pillH := 13 + padY*2
	if pillW < int(80*scale) {
		pillW = int(80 * scale)
	}
	shadowPad := int(6 * scale)
	if shadowPad < 2 {
		shadowPad = 2
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, pillW+shadowPad*2, pillH+shadowPad*2))
	rr := int(10 * scale)
	if rr < 4 {
		rr = 4
	}
	// Soft drop shadow (proprietary keystrokes fade shadow before edges).
	fillRoundRect(canvas, shadowPad+2, shadowPad+3, pillW, pillH, rr, color.NRGBA{0, 0, 0, 90})
	fillRoundRect(canvas, shadowPad, shadowPad, pillW, pillH, rr, color.NRGBA{20, 20, 24, 220})
	d := &font.Drawer{
		Dst:  canvas,
		Src:  image.NewUniform(color.NRGBA{255, 255, 255, 255}),
		Face: face,
		Dot:  fixed.P(shadowPad+padX, shadowPad+padY+11),
	}
	d.DrawString(text)

	pillX := (dst.W - canvas.Bounds().Dx()) / 2
	pillY := dst.H - int(60*scale) - canvas.Bounds().Dy()
	overlayNRGBA(dst, canvas, pillX, pillY, opacity)
}

func fillRoundRect(img *image.NRGBA, x0, y0, w, h, r int, c color.NRGBA) {
	if r < 0 {
		r = 0
	}
	if r*2 > w {
		r = w / 2
	}
	if r*2 > h {
		r = h / 2
	}
	for y := y0; y < y0+h; y++ {
		for x := x0; x < x0+w; x++ {
			inside := true
			if x < x0+r && y < y0+r {
				dx, dy := x-(x0+r), y-(y0+r)
				inside = dx*dx+dy*dy <= r*r
			} else if x >= x0+w-r && y < y0+r {
				dx, dy := x-(x0+w-1-r), y-(y0+r)
				inside = dx*dx+dy*dy <= r*r
			} else if x < x0+r && y >= y0+h-r {
				dx, dy := x-(x0+r), y-(y0+h-1-r)
				inside = dx*dx+dy*dy <= r*r
			} else if x >= x0+w-r && y >= y0+h-r {
				dx, dy := x-(x0+w-1-r), y-(y0+h-1-r)
				inside = dx*dx+dy*dy <= r*r
			}
			if inside {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

func overlayNRGBA(dst *rgbaFrame, src *image.NRGBA, ox, oy int, opacity float64) {
	b := src.Bounds()
	var wg sync.WaitGroup
	_ = draw.Over
	for y := b.Min.Y; y < b.Max.Y; y++ {
		wg.Add(1)
		go func(y int) {
			defer wg.Done()
			for x := b.Min.X; x < b.Max.X; x++ {
				c := src.NRGBAAt(x, y)
				if c.A == 0 {
					continue
				}
				dx, dy := ox+x, oy+y
				if dx < 0 || dy < 0 || dx >= dst.W || dy >= dst.H {
					continue
				}
				a := (float64(c.A) / 255.0) * opacity
				dr, dg, db, _ := dst.at(dx, dy)
				dst.set(dx, dy,
					byte(float64(dr)*(1-a)+float64(c.R)*a+0.5),
					byte(float64(dg)*(1-a)+float64(c.G)*a+0.5),
					byte(float64(db)*(1-a)+float64(c.B)*a+0.5),
					255)
			}
		}(y)
	}
	wg.Wait()
}
