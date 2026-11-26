package main

import (
	"os"
	"testing"
	"time"

	"rgblights/openrgb"
)

func TestToHSV(t *testing.T) {
	cases := []struct {
		in      openrgb.Color
		h, s, v uint8
	}{
		{openrgb.Color{R: 255}, 0, 255, 255},
		{openrgb.Color{G: 255}, 85, 255, 255},
		{openrgb.Color{B: 255}, 170, 255, 255},
		{openrgb.Color{R: 255, G: 255, B: 255}, 0, 0, 255},
		{openrgb.Color{}, 0, 0, 0},
		{openrgb.Color{R: 255, B: 255}, 213, 255, 255}, // magenta wraps hue below 255
	}
	for _, c := range cases {
		h, s, v := toHSV(c.in)
		if d := int(h) - int(c.h); d < -1 || d > 1 || s != c.s || v != c.v {
			t.Errorf("toHSV(%+v) = %d,%d,%d want %d,%d,%d", c.in, h, s, v, c.h, c.s, c.v)
		}
	}
}

// TestKeyboardLive flashes real VialRGB keyboards, then restores them.
// Run with RGBLIGHTS_LIVE=1.
func TestKeyboardLive(t *testing.T) {
	if os.Getenv("RGBLIGHTS_LIVE") == "" {
		t.Skip("set RGBLIGHTS_LIVE=1 to drive real hardware")
	}
	k := NewKeyboards()
	defer k.Close()
	k.sync(true)
	st, views := k.views()
	t.Logf("teclados: %s, %d", st, len(views))
	if len(views) == 0 {
		t.Fatal("no VialRGB keyboard found")
	}
	cfg := defaultConfig()
	cfg.Saturation, cfg.Brightness, cfg.Vivid = 1, 1, false
	for _, col := range testColors {
		t.Logf("color %+v", col)
		th := solidThumb(col)
		for i := 0; i < 20; i++ {
			k.frame(cfg, func(string) (Thumb, bool) { return th, true }, 1)
			time.Sleep(100 * time.Millisecond)
		}
	}
	_, views = k.views()
	if len(views) == 0 {
		t.Fatal("keyboard dropped during test")
	}
}
