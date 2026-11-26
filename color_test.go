package main

import (
	"math"
	"testing"
)

func thumb(w, h int, px func(x, y int) (r, g, b byte)) Thumb {
	t := Thumb{W: w, H: h, Pix: make([]byte, w*h*4)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := px(x, y)
			o := (y*w + x) * 4
			t.Pix[o], t.Pix[o+1], t.Pix[o+2] = b, g, r
		}
	}
	return t
}

func near(a, b rgb) bool {
	return math.Abs(a.R-b.R) < 2 && math.Abs(a.G-b.G) < 2 && math.Abs(a.B-b.B) < 2
}

func TestMean(t *testing.T) {
	th := thumb(4, 2, func(x, y int) (byte, byte, byte) {
		if x < 2 {
			return 200, 0, 0
		}
		return 0, 0, 100
	})
	if c := regionColor(th, 0, 4, false); !near(c, rgb{100, 0, 50}) {
		t.Fatalf("mean = %+v", c)
	}
}

func TestVividPrefersColor(t *testing.T) {
	// 75% grey, 25% pure red: vivid should lean clearly red, plain mean grey-ish.
	th := thumb(4, 4, func(x, y int) (byte, byte, byte) {
		if x == 0 {
			return 255, 0, 0
		}
		return 128, 128, 128
	})
	v := regionColor(th, 0, 4, true)
	if v.R < 200 || v.G > 40 {
		t.Fatalf("vivid = %+v, want reddish", v)
	}
	// All grey: vivid falls back to the mean.
	g := thumb(2, 2, func(x, y int) (byte, byte, byte) { return 90, 90, 90 })
	if c := regionColor(g, 0, 2, true); !near(c, rgb{90, 90, 90}) {
		t.Fatalf("grey vivid = %+v", c)
	}
}

func TestSpread(t *testing.T) {
	th := thumb(6, 1, func(x, y int) (byte, byte, byte) {
		switch x / 2 {
		case 0:
			return 255, 0, 0
		case 1:
			return 0, 255, 0
		}
		return 0, 0, 255
	})
	cs := ledColors(th, 3, true, false)
	want := []rgb{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}}
	for i := range want {
		if !near(cs[i], want[i]) {
			t.Fatalf("led %d = %+v", i, cs[i])
		}
	}
	// More LEDs than columns must not panic or leave LEDs black.
	for i, c := range ledColors(th, 10, true, false) {
		if c == (rgb{}) {
			t.Fatalf("led %d black", i)
		}
	}
}

func TestAdjust(t *testing.T) {
	if c := adjust(rgb{100, 100, 100}, 2, 1); !near(c, rgb{100, 100, 100}) {
		t.Fatalf("grey changed: %+v", c)
	}
	if c := adjust(rgb{200, 100, 100}, 2, 0.5); !near(c, rgb{133.3, 33.3, 33.3}) {
		t.Fatalf("adjust = %+v", c)
	}
}
