package main

import (
	"os"
	"testing"
	"time"
)

// TestCorsairLive flashes real Corsair devices. Run with RGBLIGHTS_LIVE=1.
func TestCorsairLive(t *testing.T) {
	if os.Getenv("RGBLIGHTS_LIVE") == "" {
		t.Skip("set RGBLIGHTS_LIVE=1 to drive real hardware")
	}
	c := NewCorsair()
	defer c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.sync(true)
		c.mu.Lock()
		n := len(c.devices)
		active := c.active
		c.mu.Unlock()
		if n > 0 && active {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.mu.Lock()
	for _, d := range c.devices {
		for i, l := range d.leds {
			t.Logf("%s led %d: id=0x%X cx=%.1f cy=%.1f", d.name, i, l.ID, l.CX, l.CY)
		}
	}
	c.mu.Unlock()
	st, views := c.views()
	t.Logf("%s, %d dispositivos", st, len(views))
	cfg := defaultConfig()
	cfg.Saturation, cfg.Brightness, cfg.Vivid = 1, 1, false
	if os.Getenv("RGBLIGHTS_LIVE") == "list" {
		return
	}
	for _, col := range testColors {
		t.Logf("color %+v", col)
		for i := 0; i < 30; i++ {
			th := solidThumb(col)
			c.frame(cfg, func(string) (Thumb, bool) { return th, true }, 1)
			time.Sleep(100 * time.Millisecond)
		}
	}
}
