package main

import "math"

// rgb is a floating-point color with channels in [0, 255].
type rgb struct{ R, G, B float64 }

// Thumb is a small BGRA image (top-down rows).
type Thumb struct {
	W, H int
	Pix  []byte
}

// regionColor computes the color of columns [x0, x1) of the thumbnail.
//
// With vivid=false it is the plain mean. With vivid=true each pixel is
// weighted by how colorful and bright it is, so a red logo on a grey page
// tints the lights red instead of producing a muddy grey; a fully grey area
// still falls back to its mean.
func regionColor(t Thumb, x0, x1 int, vivid bool) rgb {
	var sr, sg, sb, sw float64
	var mr, mg, mb float64
	n := 0
	for y := 0; y < t.H; y++ {
		row := t.Pix[y*t.W*4:]
		for x := x0; x < x1; x++ {
			b, g, r := float64(row[x*4]), float64(row[x*4+1]), float64(row[x*4+2])
			mr, mg, mb = mr+r, mg+g, mb+b
			n++
			if vivid {
				mx := math.Max(r, math.Max(g, b))
				mn := math.Min(r, math.Min(g, b))
				sat := 0.0
				if mx > 0 {
					sat = (mx - mn) / mx
				}
				w := sat * sat * (mx / 255)
				sr, sg, sb, sw = sr+r*w, sg+g*w, sb+b*w, sw+w
			}
		}
	}
	if n == 0 {
		return rgb{}
	}
	mean := rgb{mr / float64(n), mg / float64(n), mb / float64(n)}
	if !vivid || sw < 1e-6 {
		return mean
	}
	v := rgb{sr / sw, sg / sw, sb / sw}
	// Blend toward the mean when only a sliver of the area is colorful, so a
	// handful of colored pixels doesn't override a mostly white/black scene.
	k := math.Min(1, sw/float64(n)*8)
	return lerp(mean, v, k)
}

// ledColors returns nLEDs colors. With spread=false every LED gets the
// color of the whole region; with spread=true the region is split into
// vertical strips mapped left to right onto the LEDs.
func ledColors(t Thumb, nLEDs int, spread, vivid bool) []rgb {
	out := make([]rgb, nLEDs)
	if nLEDs == 0 {
		return out
	}
	if !spread || nLEDs == 1 {
		c := regionColor(t, 0, t.W, vivid)
		for i := range out {
			out[i] = c
		}
		return out
	}
	for i := range out {
		x0 := i * t.W / nLEDs
		x1 := (i + 1) * t.W / nLEDs
		if x1 <= x0 {
			x1 = x0 + 1
		}
		out[i] = regionColor(t, x0, x1, vivid)
	}
	return out
}

func lerp(a, b rgb, k float64) rgb {
	return rgb{a.R + (b.R-a.R)*k, a.G + (b.G-a.G)*k, a.B + (b.B-a.B)*k}
}

// adjust applies saturation boost and brightness scaling. Saturation 1 keeps
// the color, >1 pushes it away from grey.
func adjust(c rgb, saturation, brightness float64) rgb {
	grey := (c.R + c.G + c.B) / 3
	c = rgb{
		grey + (c.R-grey)*saturation,
		grey + (c.G-grey)*saturation,
		grey + (c.B-grey)*saturation,
	}
	return rgb{clamp(c.R * brightness), clamp(c.G * brightness), clamp(c.B * brightness)}
}

func clamp(v float64) float64 {
	return math.Max(0, math.Min(255, v))
}
