package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"

	"rgblights/openrgb"
)

// Corsair drives Corsair peripherals through the iCUE SDK v4 while iCUE
// keeps running. iCUE must have "Enable SDK" switched on in its settings.
//
// The SDK DLL is not bundled: it is looked up next to rgblights.exe and in
// the folders of other programs that ship Corsair's signed copy.
var icueDLLCandidates = []string{
	"", // next to rgblights.exe (filled in at runtime)
	`C:\Program Files\Elgato\StreamDeck\iCUESDK.x64_2019.dll`,
	`C:\Program Files\Corsair\Corsair iCUE5 Software\iCUESDK.x64_2019.dll`,
	`C:\Program Files\Corsair\CORSAIR iCUE 5 Software\iCUESDK.x64_2019.dll`,
}

const (
	cueStringM           = 128
	cueDeviceMax         = 64
	cueLedMax            = 512
	cueStateConnected    = 6
	cueAllDevices        = 0xFFFFFFFF
	cueExclusiveLighting = 1
)

var cueStateNames = []string{"inválido", "cerrado", "conectando", "tiempo agotado",
	"conexión rechazada (activa el SDK en iCUE)", "conexión perdida", "conectado"}

var cueTypeNames = map[uint32]string{1: "Teclado", 2: "Ratón", 4: "Alfombrilla", 8: "Auriculares",
	0x10: "Soporte auriculares", 0x20: "Controlador de ventiladores", 0x40: "Controlador LED",
	0x80: "RAM", 0x100: "Refrigeración", 0x200: "Placa base", 0x400: "GPU", 0x800: "Touchbar", 0x1000: "Mando"}

type cueDeviceInfo struct {
	Type         uint32
	ID           [cueStringM]byte
	Serial       [cueStringM]byte
	Model        [cueStringM]byte
	LedCount     int32
	ChannelCount int32
}

type cueLedPosition struct {
	ID     uint32
	_      uint32
	CX, CY float64
}

type cueLedColor struct {
	ID         uint32
	R, G, B, A uint8
}

type cueDevice struct {
	info   cueDeviceInfo
	name   string
	leds   []cueLedPosition
	u, v   []float64 // LED position normalised to 0..1 across the device
	state  []rgb
	colors []cueLedColor
	sent   []cueLedColor
}

type Corsair struct {
	dll                                             *windows.DLL
	connect, getDevices, getLedPositions, setColors *windows.Proc
	requestControl, releaseControl, disconnect      *windows.Proc

	state   atomic.Int32 // latest session state from the SDK callback
	changed atomic.Bool  // state changed since the engine last looked

	mu      sync.Mutex
	devices []*cueDevice
	active  bool // we hold exclusive lighting control
	loadErr string
}

var cueCallback = sync.OnceValue(func() uintptr {
	return windows.NewCallback(func(ctx uintptr, event *int32) uintptr {
		c := corsairInstance
		state := *event
		c.state.Store(state)
		c.changed.Store(true)
		return 0
	})
})

// corsairInstance is kept reachable for the lifetime of the process so the
// pointer handed to the SDK callback stays valid.
var corsairInstance *Corsair

// NewCorsair loads the SDK and starts connecting to iCUE. It returns a
// usable value even on failure; Status explains what went wrong.
func NewCorsair() *Corsair {
	c := &Corsair{}
	corsairInstance = c
	if exe, err := os.Executable(); err == nil {
		icueDLLCandidates[0] = filepath.Join(filepath.Dir(exe), "iCUESDK.x64_2019.dll")
	}
	for _, p := range icueDLLCandidates {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		dll, err := windows.LoadDLL(p)
		if err != nil {
			log.Printf("icue: no se pudo cargar %s: %v", p, err)
			continue
		}
		c.dll = dll
		log.Printf("icue: usando %s", p)
		break
	}
	if c.dll == nil {
		c.loadErr = "no se encontró iCUESDK.x64_2019.dll (cópiala junto a rgblights.exe)"
		return c
	}
	procs := map[string]**windows.Proc{
		"CorsairConnect": &c.connect, "CorsairGetDevices": &c.getDevices,
		"CorsairGetLedPositions": &c.getLedPositions, "CorsairSetLedColors": &c.setColors,
		"CorsairRequestControl": &c.requestControl, "CorsairReleaseControl": &c.releaseControl,
		"CorsairDisconnect": &c.disconnect,
	}
	for name, dst := range procs {
		p, err := c.dll.FindProc(name)
		if err != nil {
			c.loadErr = "la DLL de iCUE no es la versión 4 del SDK"
			c.dll = nil
			return c
		}
		*dst = p
	}
	if r, _, _ := c.connect.Call(cueCallback(), 0); r != 0 {
		c.loadErr = fmt.Sprintf("CorsairConnect falló (código %d)", r)
	}
	return c
}

func cueErr(r uintptr) error {
	if r == 0 {
		return nil
	}
	names := []string{"", "no conectado", "sin control", "protocolo incompatible", "argumentos inválidos",
		"operación inválida", "dispositivo no encontrado", "no permitido"}
	if int(r) < len(names) {
		return errors.New("icue: " + names[r])
	}
	return fmt.Errorf("icue: error %d", r)
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// sync reacts to session changes: enumerates devices once connected and
// takes or releases lighting control. Called from the engine loop.
func (c *Corsair) sync(want bool) {
	if c == nil || c.dll == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.changed.Swap(false) {
		st := c.state.Load()
		log.Printf("icue: estado %s", c.stateNameLocked())
		c.devices, c.active = nil, false
		if st == cueStateConnected {
			if err := c.enumerateLocked(); err != nil {
				log.Printf("icue: %v", err)
			}
		}
	}
	if c.state.Load() != cueStateConnected || want == c.active {
		return
	}
	for _, d := range c.devices {
		var r uintptr
		if want {
			r, _, _ = c.requestControl.Call(uintptr(unsafe.Pointer(&d.info.ID[0])), cueExclusiveLighting)
		} else {
			r, _, _ = c.releaseControl.Call(uintptr(unsafe.Pointer(&d.info.ID[0])))
		}
		if err := cueErr(r); err != nil {
			log.Printf("icue: control de %s: %v", d.name, err)
		}
		d.sent = nil
	}
	c.active = want
}

func (c *Corsair) enumerateLocked() error {
	filter := uint32(cueAllDevices)
	infos := make([]cueDeviceInfo, cueDeviceMax)
	var n int32
	r, _, _ := c.getDevices.Call(uintptr(unsafe.Pointer(&filter)), cueDeviceMax,
		uintptr(unsafe.Pointer(&infos[0])), uintptr(unsafe.Pointer(&n)))
	if err := cueErr(r); err != nil {
		return err
	}
	for _, info := range infos[:n] {
		if info.LedCount <= 0 {
			continue
		}
		d := &cueDevice{info: info, name: cstr(info.Model[:])}
		if t, ok := cueTypeNames[info.Type]; ok {
			d.name += " (" + t + ")"
		}
		pos := make([]cueLedPosition, cueLedMax)
		var m int32
		r, _, _ := c.getLedPositions.Call(uintptr(unsafe.Pointer(&d.info.ID[0])), cueLedMax,
			uintptr(unsafe.Pointer(&pos[0])), uintptr(unsafe.Pointer(&m)))
		if err := cueErr(r); err != nil || m == 0 {
			log.Printf("icue: posiciones de %s: %v", d.name, err)
			continue
		}
		d.leds = pos[:m]
		d.u, d.v = normalisePositions(d.leds)
		d.colors = make([]cueLedColor, m)
		for i, l := range d.leds {
			d.colors[i].ID = l.ID
			d.colors[i].A = 255
		}
		c.devices = append(c.devices, d)
		log.Printf("icue: usando %q (%d LEDs)", d.name, m)
	}
	return nil
}

// normalisePositions maps LED centres (in mm) to 0..1 on both axes.
func normalisePositions(leds []cueLedPosition) (u, v []float64) {
	minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, l := range leds {
		minX, maxX = math.Min(minX, l.CX), math.Max(maxX, l.CX)
		minY, maxY = math.Min(minY, l.CY), math.Max(maxY, l.CY)
	}
	u, v = make([]float64, len(leds)), make([]float64, len(leds))
	for i, l := range leds {
		if maxX > minX {
			u[i] = (l.CX - minX) / (maxX - minX)
		} else {
			u[i] = 0.5
		}
		if maxY > minY {
			v[i] = (l.CY - minY) / (maxY - minY)
		} else {
			v[i] = 0.5
		}
	}
	return u, v
}

// frame sends one update. With spread, each LED takes the color of the
// matching spot of the region (keyboards get a 2-D picture of it).
func (c *Corsair) frame(cfg Config, src thumbSource, alpha float64) {
	if c == nil || c.dll == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return
	}
	for _, d := range c.devices {
		t, ok := src(d.name)
		if !ok {
			continue
		}
		cfg := cfg.forDevice(d.name)
		whole := adjust(regionColor(t, 0, t.W, cfg.Vivid), cfg.Saturation, cfg.Brightness)
		if len(d.state) != len(d.leds) {
			d.state = make([]rgb, len(d.leds))
			for i := range d.state {
				d.state[i] = whole
			}
		}
		changed := d.sent == nil
		for i := range d.leds {
			target := whole
			if cfg.Spread {
				target = adjust(spotColor(t, d.u[i], d.v[i], cfg.Vivid), cfg.Saturation, cfg.Brightness)
			}
			d.state[i] = lerp(d.state[i], target, alpha)
			col := &d.colors[i]
			col.R, col.G, col.B = uint8(math.Round(d.state[i].R)), uint8(math.Round(d.state[i].G)), uint8(math.Round(d.state[i].B))
			if !changed && d.sent[i] != *col {
				changed = true
			}
		}
		if !changed {
			continue
		}
		r, _, _ := c.setColors.Call(uintptr(unsafe.Pointer(&d.info.ID[0])), uintptr(len(d.colors)),
			uintptr(unsafe.Pointer(&d.colors[0])))
		if err := cueErr(r); err != nil {
			log.Printf("icue: %s: %v", d.name, err)
			continue
		}
		d.sent = append(d.sent[:0], d.colors...)
	}
}

// spotColor averages a small neighbourhood of the thumbnail around (u, v).
func spotColor(t Thumb, u, v float64, vivid bool) rgb {
	cx := int(u * float64(t.W-1))
	cy := int(v * float64(t.H-1))
	x0, x1 := max(cx-1, 0), min(cx+2, t.W)
	y0, y1 := max(cy-1, 0), min(cy+2, t.H)
	sub := Thumb{W: x1 - x0, H: y1 - y0, Pix: make([]byte, 0, (x1-x0)*(y1-y0)*4)}
	for y := y0; y < y1; y++ {
		sub.Pix = append(sub.Pix, t.Pix[(y*t.W+x0)*4:(y*t.W+x1)*4]...)
	}
	return regionColor(sub, 0, sub.W, vivid)
}

func (c *Corsair) stateNameLocked() string {
	s := int(c.state.Load())
	if s >= 0 && s < len(cueStateNames) {
		return cueStateNames[s]
	}
	return fmt.Sprint(s)
}

// views describes the Corsair devices for the preview window.
func (c *Corsair) views() (status string, out []DeviceView) {
	if c == nil {
		return "iCUE: iniciando…", nil
	}
	if c.dll == nil {
		return c.loadErr, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loadErr != "" {
		return c.loadErr, nil
	}
	status = "iCUE: " + c.stateNameLocked()
	for _, d := range c.devices {
		v := DeviceView{Name: d.name, Used: c.active, Zones: []openrgb.Zone{{Name: "LEDs", NumLEDs: len(d.leds)}}}
		for _, col := range d.sent {
			v.Colors = append(v.Colors, openrgb.Color{R: col.R, G: col.G, B: col.B})
		}
		out = append(out, v)
	}
	return status, out
}

// Close gives lighting back to iCUE.
func (c *Corsair) Close() {
	if c == nil || c.dll == nil {
		return
	}
	c.sync(false)
	c.disconnect.Call()
}
