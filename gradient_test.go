package main

import (
	"testing"

	"rgblights/openrgb"
)

func TestGradientParams(t *testing.T) {
	red, yellow := openrgb.Color{R: 255}, openrgb.Color{R: 255, G: 255}
	h, s, v, speed := gradientParams(red, yellow)
	if h != 0 || s != 255 || v != 255 || speed == 0 {
		t.Fatalf("red→yellow = h%d s%d v%d speed%d", h, s, v, speed)
	}
	// Right edge (x=224) must land near yellow's hue (~43).
	if end := gradientHue(h, speed, 224); end < 38 || end > 48 {
		t.Fatalf("right edge hue = %d, want ~43", end)
	}
	if mid := gradientHue(h, speed, 112); mid < 17 || mid > 25 {
		t.Fatalf("middle hue = %d, want ~21", mid)
	}

	// Backwards (yellow→red) would need a full rainbow: flat midpoint instead.
	h, _, _, speed = gradientParams(yellow, red)
	if speed != 0 || h < 15 || h > 28 {
		t.Fatalf("yellow→red = h%d speed%d, want flat ~21", h, speed)
	}
	// Grey on one side: no hue gradient.
	if _, _, _, speed = gradientParams(red, openrgb.Color{R: 90, G: 90, B: 90}); speed != 0 {
		t.Fatalf("red→grey speed = %d, want 0", speed)
	}
}
