package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func appendStr(b []byte, s string) []byte {
	b = binary.LittleEndian.AppendUint16(b, uint16(len(s)+1))
	return append(append(b, s...), 0)
}

// minimalController encodes a protocol-0 controller: no modes, one zone.
func minimalController(name string, leds int) []byte {
	b := binary.LittleEndian.AppendUint32(nil, 0)
	b = binary.LittleEndian.AppendUint32(b, 0)
	for _, s := range []string{name, "", "", "", ""} {
		b = appendStr(b, s)
	}
	b = binary.LittleEndian.AppendUint16(b, 0) // modes
	b = binary.LittleEndian.AppendUint32(b, 0) // active mode
	b = binary.LittleEndian.AppendUint16(b, 1) // zones
	b = appendStr(b, "z")
	for _, v := range []uint32{0, 0, 0, uint32(leds)} {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	b = binary.LittleEndian.AppendUint16(b, 0) // no matrix
	b = binary.LittleEndian.AppendUint16(b, uint16(leds))
	return b
}

// TestEngineEndToEnd runs the real capture loop against a fake OpenRGB
// server and checks LED updates arrive for the filtered device only.
func TestEngineEndToEnd(t *testing.T) {
	t.Setenv("RGBLIGHTS_CONFIG_DIR", t.TempDir())
	enableDPIAwareness()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctrls := [][]byte{minimalController("ASUS Aura Motherboard", 4), minimalController("Mouse", 2)}
	type upd struct {
		dev  uint32
		leds int
	}
	updates := make(chan upd, 100)
	customMode := make(chan uint32, 10)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reply := func(dev, id uint32, data []byte) {
			h := append([]byte("ORGB"), make([]byte, 12)...)
			binary.LittleEndian.PutUint32(h[4:], dev)
			binary.LittleEndian.PutUint32(h[8:], id)
			binary.LittleEndian.PutUint32(h[12:], uint32(len(data)))
			conn.Write(append(h, data...))
		}
		for {
			h := make([]byte, 16)
			if _, err := io.ReadFull(conn, h); err != nil {
				return
			}
			dev, id := binary.LittleEndian.Uint32(h[4:]), binary.LittleEndian.Uint32(h[8:])
			data := make([]byte, binary.LittleEndian.Uint32(h[12:]))
			io.ReadFull(conn, data)
			switch id {
			case 0:
				reply(0, 0, binary.LittleEndian.AppendUint32(nil, uint32(len(ctrls))))
			case 1:
				reply(dev, 1, ctrls[dev])
			case 1100:
				customMode <- dev
			case 1050:
				select {
				case updates <- upd{dev, int(binary.LittleEndian.Uint16(data[4:]))}:
				default:
				}
			}
		}
	}()

	cfg := defaultConfig()
	cfg.Server = ln.Addr().String()
	cfg.Devices = []string{"aura"}
	cfg.Region = &Region{X: 0, Y: 0, W: 200, H: 200}
	cfg.FPS = 60
	cfg.DisableCorsair = true   // never touch the real iCUE from tests
	cfg.DisableKeyboards = true // nor the real keyboard
	e := NewEngine(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	select {
	case d := <-customMode:
		if d != 0 {
			t.Fatalf("custom mode on device %d", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no custom mode request")
	}
	select {
	case u := <-updates:
		if u.dev != 0 || u.leds != 4 {
			t.Fatalf("update %+v, want device 0 with 4 LEDs", u)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no LED update received")
	}
	if s := e.status(); !s.Connected || len(s.Devices) != 1 {
		t.Fatalf("status %+v", s)
	}
}
