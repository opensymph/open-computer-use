//go:build linux

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/png"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// captureWindowPNG mirrors runtime.py's capture_window_png: a ZPixmap read of
// the root window region (what Gdk.pixbuf_get_from_window did under X11),
// black-frame suppression, PNG + base64. Every failure mode — no X display,
// unusual pixel formats, a compositor that rejects the read — degrades
// silently to "", exactly like the Python bridge.
func captureWindowPNG(display string, bounds *frame) string {
	if bounds == nil {
		return ""
	}
	x, y, width, height := captureRect(bounds)
	conn, err := xgb.NewConnDisplay(display)
	if err != nil {
		return ""
	}
	defer conn.Close()

	setup := xproto.Setup(conn)
	if setup == nil || conn.DefaultScreen < 0 || conn.DefaultScreen >= len(setup.Roots) {
		return ""
	}
	screen := setup.Roots[conn.DefaultScreen]

	// Clamp the requested rect into the root window.
	rootW, rootH := int(screen.WidthInPixels), int(screen.HeightInPixels)
	if x < 0 {
		width += x
		x = 0
	}
	if y < 0 {
		height += y
		y = 0
	}
	if x+width > rootW {
		width = rootW - x
	}
	if y+height > rootH {
		height = rootH - y
	}
	if width <= 0 || height <= 0 || width > 0xffff || height > 0xffff {
		return ""
	}

	reply, err := xproto.GetImage(
		conn, xproto.ImageFormatZPixmap, xproto.Drawable(screen.Root),
		int16(x), int16(y), uint16(width), uint16(height), ^uint32(0),
	).Reply()
	if err != nil || reply == nil {
		return ""
	}

	img := decodeZPixmap(setup, &screen, reply, width, height)
	if img == nil {
		return ""
	}
	if looksBlackRGB(img.Pix, width, height, img.Stride, 4) {
		return ""
	}
	var buf bytes.Buffer
	if png.Encode(&buf, img) != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// decodeZPixmap expands a GetImage ZPixmap reply into an RGBA image using the
// visual's channel masks and the pixmap format's scanline padding. Only
// TrueColor-class visuals at 16/24/32 bpp are understood; anything else
// returns nil.
func decodeZPixmap(setup *xproto.SetupInfo, screen *xproto.ScreenInfo, reply *xproto.GetImageReply, width, height int) *image.RGBA {
	var format *xproto.Format
	for i := range setup.PixmapFormats {
		if setup.PixmapFormats[i].Depth == reply.Depth {
			format = &setup.PixmapFormats[i]
			break
		}
	}
	if format == nil {
		return nil
	}
	bpp := int(format.BitsPerPixel)
	if bpp != 16 && bpp != 24 && bpp != 32 {
		return nil
	}

	var visual *xproto.VisualInfo
	for _, depth := range screen.AllowedDepths {
		for i := range depth.Visuals {
			if depth.Visuals[i].VisualId == reply.Visual {
				visual = &depth.Visuals[i]
				break
			}
		}
	}
	if visual == nil || visual.Class != xproto.VisualClassTrueColor {
		return nil
	}
	redShift, redBits := maskShiftBits(visual.RedMask)
	greenShift, greenBits := maskShiftBits(visual.GreenMask)
	blueShift, blueBits := maskShiftBits(visual.BlueMask)
	if redBits == 0 || greenBits == 0 || blueBits == 0 {
		return nil
	}

	pad := int(format.ScanlinePad)
	if pad <= 0 {
		pad = 8
	}
	stride := (width*bpp + pad - 1) / pad * (pad / 8)
	if len(reply.Data) < stride*(height-1)+width*bpp/8 {
		return nil
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var order binary.ByteOrder = binary.LittleEndian
	if setup.ImageByteOrder == 1 { // MSBFirst
		order = binary.BigEndian
	}
	for py := 0; py < height; py++ {
		row := py * stride
		for px := 0; px < width; px++ {
			offset := row + px*bpp/8
			var value uint32
			switch bpp {
			case 32:
				value = order.Uint32(reply.Data[offset : offset+4])
			case 24:
				if setup.ImageByteOrder == 1 {
					value = uint32(reply.Data[offset])<<16 | uint32(reply.Data[offset+1])<<8 | uint32(reply.Data[offset+2])
				} else {
					value = uint32(reply.Data[offset]) | uint32(reply.Data[offset+1])<<8 | uint32(reply.Data[offset+2])<<16
				}
			case 16:
				value = uint32(order.Uint16(reply.Data[offset : offset+2]))
			}
			out := img.Pix[py*img.Stride+px*4:]
			out[0] = scaleChannel(value>>redShift, redBits)
			out[1] = scaleChannel(value>>greenShift, greenBits)
			out[2] = scaleChannel(value>>blueShift, blueBits)
			out[3] = 0xff
		}
	}
	return img
}

func maskShiftBits(mask uint32) (shift, bits uint32) {
	if mask == 0 {
		return 0, 0
	}
	for mask&1 == 0 {
		mask >>= 1
		shift++
	}
	for mask&1 == 1 {
		mask >>= 1
		bits++
	}
	return shift, bits
}

func scaleChannel(value, bits uint32) byte {
	max := uint32(1)<<bits - 1
	return byte(value * 255 / max)
}
