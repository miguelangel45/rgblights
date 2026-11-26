package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// makeIcon draws a 32x32 hue ring as a .ico (PNG-compressed, Vista+).
// When grey is true the ring is desaturated, used while paused.
func makeIcon(grey bool) []byte {
	const size = 32
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	c := float64(size-1) / 2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)-c, float64(y)-c
			d := math.Hypot(dx, dy)
			// Antialiased ring between radius 8.5 and 15.5, plus a centre dot.
			a := math.Min(coverage(d, 8.5, 15.5), 1) + coverage(d, -1, 5)
			if a <= 0 {
				continue
			}
			h := math.Atan2(dy, dx)/(2*math.Pi) + 0.5
			r, g, b := hsv(h, 1, 1)
			if d < 6 {
				r, g, b = 1, 1, 1
			}
			if grey {
				l := 0.3*r + 0.59*g + 0.11*b
				r, g, b = l*0.8, l*0.8, l*0.8
			}
			img.SetNRGBA(x, y, color.NRGBA{uint8(r * 255), uint8(g * 255), uint8(b * 255), uint8(math.Min(a, 1) * 255)})
		}
	}
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, img)

	var ico bytes.Buffer
	w := func(v any) { binary.Write(&ico, binary.LittleEndian, v) }
	w(uint16(0))                  // reserved
	w(uint16(1))                  // type: icon
	w(uint16(1))                  // count
	w([4]uint8{size, size, 0, 0}) // width, height, colors, reserved
	w(uint16(1))                  // planes
	w(uint16(32))                 // bpp
	w(uint32(pngBuf.Len()))       // data size
	w(uint32(ico.Len() + 4))      // data offset (header is 22 bytes)
	ico.Write(pngBuf.Bytes())
	return ico.Bytes()
}

// coverage is how much of the pixel at distance d lies within [r0, r1].
func coverage(d, r0, r1 float64) float64 {
	return math.Max(0, math.Min(d-r0+0.5, 1)) * math.Max(0, math.Min(r1-d+0.5, 1))
}

func hsv(h, s, v float64) (r, g, b float64) {
	i := math.Floor(h * 6)
	f := h*6 - i
	p, q, t := v*(1-s), v*(1-f*s), v*(1-(1-f)*s)
	switch int(i) % 6 {
	case 0:
		return v, t, p
	case 1:
		return q, v, p
	case 2:
		return p, v, t
	case 3:
		return p, q, v
	case 4:
		return t, p, v
	}
	return v, p, q
}
