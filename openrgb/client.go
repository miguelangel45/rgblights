// Package openrgb is a minimal client for the OpenRGB SDK server protocol.
//
// Only what rgblights needs is implemented: listing controllers, switching
// them to their direct/custom mode and pushing LED colors. Controller data is
// requested with protocol version 0, which every OpenRGB server understands.
package openrgb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	pktRequestControllerCount = 0
	pktRequestControllerData  = 1
	pktSetClientName          = 50
	pktDeviceListUpdated      = 100
	pktResizeZone             = 1000
	pktUpdateLEDs             = 1050
	pktSetCustomMode          = 1100
)

var magic = [4]byte{'O', 'R', 'G', 'B'}

// Color is an RGB color.
type Color struct{ R, G, B uint8 }

// Controller is the subset of an OpenRGB controller description we use.
type Controller struct {
	Index    int
	Name     string
	Type     int32
	Location string
	Modes    []string
	Active   int // index into Modes
	NumLEDs  int
	Zones    []Zone
}

// Zone describes a zone of a controller.
type Zone struct {
	Name    string
	Type    int32 // 0 single, 1 linear, 2 matrix
	NumLEDs int
	MinLEDs int
	MaxLEDs int // MinLEDs != MaxLEDs means the size is configurable (e.g. ARGB headers)
}

// Client is a connection to an OpenRGB SDK server. It is safe for concurrent use.
type Client struct {
	mu      sync.Mutex // serialises requests
	conn    net.Conn
	replies chan packet
	readErr error

	flagMu      sync.Mutex
	listChanged bool
}

type packet struct {
	id   uint32
	data []byte
}

// Dial connects to an OpenRGB server and registers the client name.
func Dial(addr, clientName string, timeout time.Duration) (*Client, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, replies: make(chan packet, 8)}
	go c.readLoop()
	if err := c.send(0, pktSetClientName, append([]byte(clientName), 0)); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// DeviceListChanged reports (and clears) whether the server announced that
// its device list changed since the last call.
func (c *Client) DeviceListChanged() bool {
	c.flagMu.Lock()
	defer c.flagMu.Unlock()
	v := c.listChanged
	c.listChanged = false
	return v
}

// Controllers fetches every controller known to the server.
func (c *Client) Controllers() ([]Controller, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.send(0, pktRequestControllerCount, nil); err != nil {
		return nil, err
	}
	data, err := c.await(pktRequestControllerCount)
	if err != nil {
		return nil, err
	}
	if len(data) < 4 {
		return nil, errors.New("openrgb: short controller count")
	}
	count := int(binary.LittleEndian.Uint32(data))

	out := make([]Controller, 0, count)
	for i := 0; i < count; i++ {
		if err := c.send(uint32(i), pktRequestControllerData, nil); err != nil {
			return nil, err
		}
		data, err := c.await(pktRequestControllerData)
		if err != nil {
			return nil, err
		}
		ctrl, err := parseController(data)
		if err != nil {
			return nil, fmt.Errorf("openrgb: controller %d: %w", i, err)
		}
		ctrl.Index = i
		out = append(out, ctrl)
	}
	return out, nil
}

// SetCustomMode switches a controller to its direct (software controlled) mode.
func (c *Client) SetCustomMode(index int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.send(uint32(index), pktSetCustomMode, nil)
}

// ResizeZone sets the LED count of a resizable zone (e.g. an ARGB header).
// Call Controllers again afterwards to see the new layout.
func (c *Client) ResizeZone(index, zone, size int) error {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint32(buf, uint32(int32(zone)))
	binary.LittleEndian.PutUint32(buf[4:], uint32(int32(size)))
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.send(uint32(index), pktResizeZone, buf)
}

// UpdateLEDs sets every LED of a controller. len(colors) must equal the
// controller's LED count.
func (c *Client) UpdateLEDs(index int, colors []Color) error {
	n := len(colors)
	buf := make([]byte, 4+2+4*n)
	binary.LittleEndian.PutUint32(buf[0:], uint32(len(buf)))
	binary.LittleEndian.PutUint16(buf[4:], uint16(n))
	for i, col := range colors {
		o := 6 + 4*i
		buf[o], buf[o+1], buf[o+2] = col.R, col.G, col.B
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.send(uint32(index), pktUpdateLEDs, buf)
}

func (c *Client) send(dev, id uint32, payload []byte) error {
	hdr := make([]byte, 16, 16+len(payload))
	copy(hdr, magic[:])
	binary.LittleEndian.PutUint32(hdr[4:], dev)
	binary.LittleEndian.PutUint32(hdr[8:], id)
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(payload)))
	c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err := c.conn.Write(append(hdr, payload...))
	return err
}

func (c *Client) readPacket() (id uint32, data []byte, err error) {
	var hdr [16]byte
	if _, err = io.ReadFull(c.conn, hdr[:]); err != nil {
		return 0, nil, err
	}
	if !bytes.Equal(hdr[:4], magic[:]) {
		return 0, nil, errors.New("openrgb: bad packet magic")
	}
	id = binary.LittleEndian.Uint32(hdr[8:])
	size := binary.LittleEndian.Uint32(hdr[12:])
	if size > 64<<20 {
		return 0, nil, errors.New("openrgb: packet too large")
	}
	data = make([]byte, size)
	_, err = io.ReadFull(c.conn, data)
	return id, data, err
}

// readLoop consumes every packet from the server so the socket never backs
// up, forwarding replies to await and remembering device-list notifications.
func (c *Client) readLoop() {
	defer close(c.replies)
	for {
		id, data, err := c.readPacket()
		if err != nil {
			c.readErr = err
			return
		}
		if id == pktDeviceListUpdated {
			c.flagMu.Lock()
			c.listChanged = true
			c.flagMu.Unlock()
			continue
		}
		c.replies <- packet{id, data}
	}
}

// await waits for a reply with the wanted id.
func (c *Client) await(want uint32) ([]byte, error) {
	timeout := time.After(5 * time.Second)
	for {
		select {
		case p, ok := <-c.replies:
			if !ok {
				return nil, fmt.Errorf("openrgb: connection closed: %v", c.readErr)
			}
			if p.id == want {
				return p.data, nil
			}
		case <-timeout:
			return nil, errors.New("openrgb: timed out waiting for server")
		}
	}
}

// reader is a bounds-checked little-endian decoder.
type reader struct {
	b   []byte
	err error
}

func (r *reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || len(r.b) < n {
		r.err = io.ErrUnexpectedEOF
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

func (r *reader) u16() int {
	if v := r.take(2); v != nil {
		return int(binary.LittleEndian.Uint16(v))
	}
	return 0
}

func (r *reader) u32() uint32 {
	if v := r.take(4); v != nil {
		return binary.LittleEndian.Uint32(v)
	}
	return 0
}

func (r *reader) str() string {
	n := r.u16()
	v := r.take(n)
	return string(bytes.TrimRight(v, "\x00"))
}

// parseController decodes a protocol-version-0 controller description.
func parseController(data []byte) (Controller, error) {
	r := &reader{b: data}
	var c Controller
	r.u32() // data size
	c.Type = int32(r.u32())
	c.Name = r.str()
	r.str() // description
	r.str() // version
	r.str() // serial
	c.Location = r.str()

	numModes := r.u16()
	c.Active = int(int32(r.u32()))
	for i := 0; i < numModes && r.err == nil; i++ {
		c.Modes = append(c.Modes, r.str())
		r.take(4 * 9) // value, flags, speed min/max, colors min/max, speed, direction, color mode
		r.take(4 * r.u16())
	}

	numZones := r.u16()
	for i := 0; i < numZones && r.err == nil; i++ {
		var z Zone
		z.Name = r.str()
		z.Type = int32(r.u32())
		z.MinLEDs = int(r.u32())
		z.MaxLEDs = int(r.u32())
		z.NumLEDs = int(r.u32())
		if r.u16() > 0 { // matrix map
			h, w := r.u32(), r.u32()
			r.take(int(4 * h * w))
		}
		c.Zones = append(c.Zones, z)
	}

	c.NumLEDs = r.u16()
	return c, r.err
}
