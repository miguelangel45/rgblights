package main

import (
	"image"
	"testing"
)

func TestCaptureDesktop(t *testing.T) {
	enableDPIAwareness()
	c, err := NewCapturer(thumbW, thumbH)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	vs := virtualScreen()
	if vs.Empty() {
		t.Fatal("empty virtual screen")
	}
	pix, err := c.Grab(image.Rect(vs.Min.X, vs.Min.Y, vs.Min.X+400, vs.Min.Y+300))
	if err != nil {
		t.Fatal(err)
	}
	if len(pix) != thumbW*thumbH*4 {
		t.Fatalf("len = %d", len(pix))
	}
	t.Logf("virtual screen %v, region color %+v", vs, regionColor(Thumb{thumbW, thumbH, pix}, 0, thumbW, false))
}

func TestIcon(t *testing.T) {
	ico := makeIcon(false)
	if len(ico) < 100 || string(ico[22:26]) != "\x89PNG" {
		t.Fatalf("bad icon header % x", ico[:30])
	}
}
