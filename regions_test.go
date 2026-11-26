package main

import (
	"bytes"
	"testing"
)

func TestCaptureSourcePerDevice(t *testing.T) {
	enableDPIAwareness()
	capt, err := NewCapturer(thumbW, thumbH)
	if err != nil {
		t.Fatal(err)
	}
	defer capt.Close()
	vs := virtualScreen()
	cfg := defaultConfig()
	cfg.Region = &Region{X: vs.Min.X, Y: vs.Min.Y, W: 300, H: 200}
	own := Region{X: vs.Max.X - 300, Y: vs.Max.Y - 200, W: 300, H: 200}
	cfg.DeviceRegions = map[string]Region{"Mouse": own}

	e := NewEngine(cfg, nil)
	src := e.captureSource(capt, cfg)
	general, ok1 := src("")
	other, ok2 := src("Fans") // no own region: same as general
	mouse, ok3 := src("Mouse")
	if !ok1 || !ok2 || !ok3 {
		t.Fatal("capture failed")
	}
	if &general.Pix[0] != &other.Pix[0] {
		t.Fatal("device without own region did not reuse the general capture")
	}
	pix, err := capt.Grab(own.Rect())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mouse.Pix, pix) {
		t.Fatal("device region thumbnail does not match a direct capture of its region")
	}
}
