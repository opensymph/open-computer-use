package main

// Frame buffers and sampling helpers for the clean-room polish compositor.
// Algorithms mirror polished-renderer behavior without copying proprietary source.

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
)

type rgbaFrame struct {
	W, H int
	Pix  []byte // RGBA, len = W*H*4
}

func newRGBAFrame(w, h int) *rgbaFrame {
	return &rgbaFrame{W: w, H: h, Pix: make([]byte, w*h*4)}
}

func (f *rgbaFrame) clone() *rgbaFrame {
	out := newRGBAFrame(f.W, f.H)
	copy(out.Pix, f.Pix)
	return out
}

func (f *rgbaFrame) clear(c color.NRGBA) {
	for i := 0; i < len(f.Pix); i += 4 {
		f.Pix[i] = c.R
		f.Pix[i+1] = c.G
		f.Pix[i+2] = c.B
		f.Pix[i+3] = c.A
	}
}

func (f *rgbaFrame) at(x, y int) (r, g, b, a byte) {
	if x < 0 || y < 0 || x >= f.W || y >= f.H {
		return 0, 0, 0, 0
	}
	i := (y*f.W + x) * 4
	return f.Pix[i], f.Pix[i+1], f.Pix[i+2], f.Pix[i+3]
}

func (f *rgbaFrame) set(x, y int, r, g, b, a byte) {
	if x < 0 || y < 0 || x >= f.W || y >= f.H {
		return
	}
	i := (y*f.W + x) * 4
	f.Pix[i] = r
	f.Pix[i+1] = g
	f.Pix[i+2] = b
	f.Pix[i+3] = a
}

// sampleBilinear samples with straight-alpha RGBA.
func (f *rgbaFrame) sampleBilinear(x, y float64) (r, g, b, a float64) {
	if f.W == 0 || f.H == 0 {
		return 0, 0, 0, 0
	}
	if x < 0 || y < 0 || x >= float64(f.W)-1e-6 || y >= float64(f.H)-1e-6 {
		// clamp to edge
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
		if x > float64(f.W-1) {
			x = float64(f.W - 1)
		}
		if y > float64(f.H-1) {
			y = float64(f.H - 1)
		}
	}
	x0 := int(math.Floor(x))
	y0 := int(math.Floor(y))
	x1 := x0 + 1
	y1 := y0 + 1
	if x1 >= f.W {
		x1 = f.W - 1
	}
	if y1 >= f.H {
		y1 = f.H - 1
	}
	fx := x - float64(x0)
	fy := y - float64(y0)
	r00, g00, b00, a00 := f.at(x0, y0)
	r10, g10, b10, a10 := f.at(x1, y0)
	r01, g01, b01, a01 := f.at(x0, y1)
	r11, g11, b11, a11 := f.at(x1, y1)
	w00 := (1 - fx) * (1 - fy)
	w10 := fx * (1 - fy)
	w01 := (1 - fx) * fy
	w11 := fx * fy
	r = float64(r00)*w00 + float64(r10)*w10 + float64(r01)*w01 + float64(r11)*w11
	g = float64(g00)*w00 + float64(g10)*w10 + float64(g01)*w01 + float64(g11)*w11
	b = float64(b00)*w00 + float64(b10)*w10 + float64(b01)*w01 + float64(b11)*w11
	a = float64(a00)*w00 + float64(a10)*w10 + float64(a01)*w01 + float64(a11)*w11
	return
}

func loadPNGFrame(path string) (*rgbaFrame, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	out := newRGBAFrame(b.Dx(), b.Dy())
	rgba := image.NewNRGBA(b)
	draw.Draw(rgba, b, img, b.Min, draw.Src)
	copy(out.Pix, rgba.Pix)
	return out, nil
}

func gradientNoise(x, y float64) float64 {
	f := 52.9829189*x + 0.06711056*y
	return math.Abs(math.Sin(f)*43758.5453) - math.Floor(math.Abs(math.Sin(f)*43758.5453))
}
