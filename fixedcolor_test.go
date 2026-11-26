package main

import "testing"

func TestFixedColors(t *testing.T) {
	if c, ok := parseHexColor("#FF8000"); !ok || c != (rgb{255, 128, 0}) {
		t.Fatalf("parse = %+v %v", c, ok)
	}
	for _, bad := range []string{"", "#12", "red", "#GG0000"} {
		if _, ok := parseHexColor(bad); ok {
			t.Fatalf("parsed invalid %q", bad)
		}
	}
	if hexColor(rgb{255, 128, 0}) != "#FF8000" {
		t.Fatal("hexColor")
	}

	cfg := defaultConfig()
	cfg.Spread, cfg.Vivid, cfg.Saturation = true, true, 1.5
	cfg.DeviceColors = map[string]string{"Mouse": "#3366CC"}

	grey := solidThumb(rgb{90, 90, 90})
	src := withFixedColors(func(string) (Thumb, bool) { return grey, true }, cfg)

	// A fixed device sees exactly its color, untouched by saturation/vivid/spread.
	th, _ := src("Mouse")
	dc := cfg.forDevice("Mouse")
	if dc.Spread || dc.Vivid || dc.Saturation != 1 {
		t.Fatalf("forDevice did not neutralise effects: %+v", dc)
	}
	for _, c := range ledColors(th, 5, dc.Spread, dc.Vivid) {
		if got := adjust(c, dc.Saturation, dc.Brightness); got != (rgb{0x33, 0x66, 0xCC}) {
			t.Fatalf("fixed LED = %+v", got)
		}
	}
	// Other devices and the general region still follow the screen.
	if th, _ := src("Fans"); regionColor(th, 0, th.W, false) != (rgb{90, 90, 90}) {
		t.Fatal("non-fixed device changed")
	}
	if th, _ := src(""); regionColor(th, 0, th.W, false) != (rgb{90, 90, 90}) {
		t.Fatal("general region changed")
	}
	if d := cfg.forDevice("Fans"); !d.Spread || d.Saturation != 1.5 {
		t.Fatal("forDevice changed a non-fixed device")
	}
}
