package openrgb

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func putStr(b []byte, s string) []byte {
	b = binary.LittleEndian.AppendUint16(b, uint16(len(s)+1))
	b = append(b, s...)
	return append(b, 0)
}

// fakeController builds a protocol-0 controller blob with one mode, a matrix
// zone and nLEDs LEDs.
func fakeController(name string, nLEDs int) []byte {
	var b []byte
	b = binary.LittleEndian.AppendUint32(b, 0) // size, patched below
	b = binary.LittleEndian.AppendUint32(b, 1) // type
	for _, s := range []string{name, "desc", "1.0", "serial", "loc"} {
		b = putStr(b, s)
	}
	b = binary.LittleEndian.AppendUint16(b, 1) // modes
	b = binary.LittleEndian.AppendUint32(b, 0) // active
	b = putStr(b, "Direct")
	for i := 0; i < 9; i++ {
		b = binary.LittleEndian.AppendUint32(b, 0)
	}
	b = binary.LittleEndian.AppendUint16(b, 1) // one mode color
	b = append(b, 1, 2, 3, 0)

	b = binary.LittleEndian.AppendUint16(b, 1) // zones
	b = putStr(b, "Strip")
	for i := 0; i < 3; i++ {
		b = binary.LittleEndian.AppendUint32(b, 0)
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(nLEDs))
	b = binary.LittleEndian.AppendUint16(b, 4*(2+2)) // 1x2 matrix
	b = binary.LittleEndian.AppendUint32(b, 1)
	b = binary.LittleEndian.AppendUint32(b, 2)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint32(b, 1)

	b = binary.LittleEndian.AppendUint16(b, uint16(nLEDs))
	for i := 0; i < nLEDs; i++ {
		b = putStr(b, "LED")
		b = binary.LittleEndian.AppendUint32(b, 0)
	}
	b = binary.LittleEndian.AppendUint16(b, uint16(nLEDs))
	for i := 0; i < nLEDs; i++ {
		b = append(b, 0, 0, 0, 0)
	}
	binary.LittleEndian.PutUint32(b, uint32(len(b)))
	return b
}

type rx struct {
	dev, id uint32
	data    []byte
}

func fakeServer(t *testing.T, ctrls [][]byte) (addr string, got chan rx) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = make(chan rx, 32)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reply := func(dev, id uint32, data []byte) {
			h := make([]byte, 16)
			copy(h, "ORGB")
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
			dev := binary.LittleEndian.Uint32(h[4:])
			id := binary.LittleEndian.Uint32(h[8:])
			data := make([]byte, binary.LittleEndian.Uint32(h[12:]))
			io.ReadFull(conn, data)
			got <- rx{dev, id, data}
			switch id {
			case pktRequestControllerCount:
				// An unsolicited notification first, to exercise the reader.
				reply(0, pktDeviceListUpdated, nil)
				reply(0, id, binary.LittleEndian.AppendUint32(nil, uint32(len(ctrls))))
			case pktRequestControllerData:
				reply(dev, id, ctrls[dev])
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestClient(t *testing.T) {
	addr, got := fakeServer(t, [][]byte{fakeController("ASUS Aura", 3), fakeController("GPU", 1)})
	c, err := Dial(addr, "test", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if p := <-got; p.id != pktSetClientName || string(p.data) != "test\x00" {
		t.Fatalf("client name packet = %+v", p)
	}

	ctrls, err := c.Controllers()
	if err != nil {
		t.Fatal(err)
	}
	if len(ctrls) != 2 || ctrls[0].Name != "ASUS Aura" || ctrls[0].NumLEDs != 3 || ctrls[1].NumLEDs != 1 {
		t.Fatalf("controllers = %+v", ctrls)
	}
	if len(ctrls[0].Zones) != 1 || ctrls[0].Zones[0].NumLEDs != 3 || ctrls[0].Location != "loc" {
		t.Fatalf("zones = %+v", ctrls[0])
	}
	if !c.DeviceListChanged() || c.DeviceListChanged() {
		t.Fatal("device list flag not set once")
	}
	<-got
	<-got
	<-got

	if err := c.SetCustomMode(0); err != nil {
		t.Fatal(err)
	}
	if p := <-got; p.id != pktSetCustomMode || p.dev != 0 {
		t.Fatalf("custom mode packet = %+v", p)
	}

	if err := c.UpdateLEDs(0, []Color{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}}); err != nil {
		t.Fatal(err)
	}
	p := <-got
	want := []byte{18, 0, 0, 0, 3, 0, 255, 0, 0, 0, 0, 255, 0, 0, 0, 0, 255, 0}
	if p.id != pktUpdateLEDs || string(p.data) != string(want) {
		t.Fatalf("update packet = %+v", p)
	}
}

func TestParseTruncated(t *testing.T) {
	b := fakeController("x", 2)
	for n := 0; n < len(b)-40; n += 7 {
		if _, err := parseController(b[:n]); err == nil {
			t.Fatalf("no error for truncated data len %d", n)
		}
	}
}
