package main

import (
	"context"
	"fmt"
	"image"
	"log"
	"maps"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rgblights/openrgb"
)

const thumbW, thumbH = 48, 12

// defaultARGBSize is used for addressable headers left at 0 LEDs.
const defaultARGBSize = 60

// testColors is the sequence shown by "Probar luces", 2 s each.
var testColors = []rgb{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {255, 255, 255}}

const testStep = 2 * time.Second

// Status is reported to the UI whenever something visible changes.
type Status struct {
	Connected bool
	Devices   []string
	Err       string
	Color     rgb // color of the first LED of the first device
}

// Engine samples the screen and drives the lights.
type Engine struct {
	mu  sync.Mutex
	cfg Config

	onStatus func(Status)
	hold     atomic.Bool // temporarily stop sampling (region selector open)
	cue      atomic.Pointer[Corsair]
	kb       atomic.Pointer[Keyboards]
	testFrom atomic.Int64 // unix nanos when "Probar luces" started; 0 = off

	client  *openrgb.Client
	devices []openrgb.Controller // controllers being driven
	all     []openrgb.Controller // every controller OpenRGB reported
	preview rgb                  // current color of the whole region
	state   map[int][]rgb        // smoothed color per device LED
	sent    map[int][]openrgb.Color
	lastErr string
	lastTx  time.Time
}

func NewEngine(cfg Config, onStatus func(Status)) *Engine {
	return &Engine{cfg: cfg, onStatus: onStatus}
}

// Config returns a copy of the current configuration.
func (e *Engine) Config() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	c := e.cfg
	c.Devices = append([]string(nil), c.Devices...)
	c.DeviceRegions = maps.Clone(c.DeviceRegions)
	c.DeviceColors = maps.Clone(c.DeviceColors)
	c.ZoneSizes = maps.Clone(c.ZoneSizes)
	return c
}

// Update changes the configuration and persists it.
func (e *Engine) Update(f func(*Config)) {
	e.mu.Lock()
	f(&e.cfg)
	e.cfg.normalize()
	c := e.cfg
	e.mu.Unlock()
	if err := saveConfig(c); err != nil {
		log.Printf("saving config: %v", err)
	}
}

// Hold pauses sampling without touching the saved configuration.
func (e *Engine) Hold(on bool) { e.hold.Store(on) }

// Reconnect drops the OpenRGB connection; the loop reconnects and
// re-detects devices on its next tick.
func (e *Engine) Reconnect() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.disconnectLocked("")
}

func (e *Engine) disconnectLocked(reason string) {
	if e.client != nil {
		e.client.Close()
		e.client = nil
	}
	e.devices = nil
	e.all = nil
	e.lastErr = reason
}

func (e *Engine) region(cfg Config) image.Rectangle {
	if cfg.Region != nil {
		return cfg.Region.Rect()
	}
	w, _, _ := procGetSystemMetrics.Call(0)
	h, _, _ := procGetSystemMetrics.Call(1)
	return image.Rect(0, 0, int(w), int(h))
}

// smoothAlpha is how far colors move toward their target in one frame.
func smoothAlpha(cfg Config, dt time.Duration) float64 {
	if cfg.SmoothingMs <= 0 {
		return 1
	}
	return 1 - math.Exp(-float64(dt.Milliseconds())/float64(cfg.SmoothingMs))
}

// thumbSource returns the thumbnail a device should mirror: its own region
// if it has one, else the general region (device "" = general).
type thumbSource func(device string) (Thumb, bool)

// captureSource grabs each distinct region at most once per frame.
func (e *Engine) captureSource(capt *Capturer, cfg Config) thumbSource {
	cache := map[image.Rectangle]Thumb{}
	general := e.region(cfg)
	return func(device string) (Thumb, bool) {
		r := general
		if dr, ok := cfg.DeviceRegions[device]; ok && device != "" && dr.W > 0 && dr.H > 0 {
			r = dr.Rect()
		}
		if t, ok := cache[r]; ok {
			return t, true
		}
		pix, err := capt.Grab(r)
		if err != nil {
			return Thumb{}, false
		}
		t := Thumb{thumbW, thumbH, append([]byte(nil), pix...)}
		cache[r] = t
		return t, true
	}
}

// withFixedColors makes devices with a pinned color see a solid image of it.
func withFixedColors(src thumbSource, cfg Config) thumbSource {
	if len(cfg.DeviceColors) == 0 {
		return src
	}
	return func(device string) (Thumb, bool) {
		if c, ok := cfg.fixedColor(device); ok && device != "" {
			return solidThumb(c), true
		}
		return src(device)
	}
}

// Run blocks until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	capt, err := NewCapturer(thumbW, thumbH)
	if err != nil {
		log.Printf("capture init: %v", err)
		e.report(Status{Err: err.Error()})
		return
	}
	defer capt.Close()
	defer e.Reconnect()
	defer func() { e.cue.Load().Close() }()
	defer func() { e.kb.Load().Close() }()

	var nextDial time.Time
	last := time.Now()
	for {
		cfg := e.Config()
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second / time.Duration(cfg.FPS)):
		}
		now := time.Now()
		dt := now.Sub(last)
		last = now

		if !e.connected() && !now.Before(nextDial) {
			nextDial = now.Add(3 * time.Second)
			if err := e.connect(cfg); err != nil {
				e.mu.Lock()
				changed := e.lastErr != err.Error()
				e.lastErr = err.Error()
				e.mu.Unlock()
				if changed {
					log.Printf("openrgb: %v", err)
					e.report(Status{Err: err.Error()})
				}
			} else {
				e.report(e.status())
			}
		}

		if !cfg.DisableCorsair && e.cue.Load() == nil {
			e.cue.Store(NewCorsair()) // loaded on first use
		}
		if !cfg.DisableKeyboards && e.kb.Load() == nil {
			e.kb.Store(NewKeyboards())
		}
		testC, testing := e.testColor(now)
		// Corsair: hold iCUE lighting control only while we are mirroring,
		// so pausing hands the peripherals back to iCUE's own effects.
		e.cue.Load().sync(!cfg.DisableCorsair && (testing || !cfg.Paused))
		e.kb.Load().sync(!cfg.DisableKeyboards && (testing || !cfg.Paused))

		var src thumbSource
		if testing {
			// "Probar luces": a solid color, instantly, ignoring pause/brightness.
			t := solidThumb(testC)
			src = func(string) (Thumb, bool) { return t, true }
			cfg.SmoothingMs, cfg.Brightness, cfg.Saturation, cfg.Spread, cfg.Vivid = 0, 1, 1, false, false
		} else {
			if cfg.Paused || e.hold.Load() {
				continue
			}
			src = e.captureSource(capt, cfg)
			if _, ok := src(""); !ok {
				continue // e.g. secure desktop (UAC/lock screen) is showing
			}
			src = withFixedColors(src, cfg)
		}
		e.cue.Load().frame(cfg, src, smoothAlpha(cfg, dt))
		e.kb.Load().frame(cfg, src, smoothAlpha(cfg, dt))
		if err := e.frame(cfg, src, dt); err != nil {
			e.mu.Lock()
			e.disconnectLocked("conexión perdida: " + err.Error())
			e.mu.Unlock()
			log.Printf("openrgb send: %v", err)
			e.report(Status{Err: "conexión perdida"})
		}
	}
}

func (e *Engine) connected() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.client != nil && e.client.DeviceListChanged() {
		// Devices were plugged/unplugged in OpenRGB: re-detect.
		e.disconnectLocked("")
	}
	return e.client != nil
}

func (e *Engine) connect(cfg Config) error {
	c, err := openrgb.Dial(cfg.Server, "rgblights", 2*time.Second)
	if err != nil {
		return fmt.Errorf("no se pudo conectar a OpenRGB en %s (¿está abierto con el servidor SDK activo?)", cfg.Server)
	}
	all, err := c.Controllers()
	if err != nil {
		c.Close()
		return err
	}
	// Re-apply configured sizes of resizable zones (ARGB headers): OpenRGB
	// may forget them when it restarts.
	resized := false
	for _, d := range all {
		for zi, z := range d.Zones {
			if z.MinLEDs == z.MaxLEDs {
				continue // fixed size
			}
			n, ok := cfg.ZoneSizes[z.Name]
			if !ok && z.NumLEDs == 0 {
				// Unconfigured ARGB header: assume a generous size. Sending
				// colors for more LEDs than are physically there is harmless.
				n, ok = defaultARGBSize, true
			}
			if ok && n != z.NumLEDs && n >= z.MinLEDs && n <= z.MaxLEDs {
				if err := c.ResizeZone(d.Index, zi, n); err != nil {
					c.Close()
					return err
				}
				log.Printf("zona %q de %q: %d → %d LEDs", z.Name, d.Name, z.NumLEDs, n)
				resized = true
			}
		}
	}
	if resized {
		time.Sleep(200 * time.Millisecond)
		if all, err = c.Controllers(); err != nil {
			c.Close()
			return err
		}
	}
	var use []openrgb.Controller
	for _, d := range all {
		if d.NumLEDs > 0 && matchesFilter(d.Name, cfg.Devices) {
			use = append(use, d)
		}
	}
	for _, d := range use {
		if err := c.SetCustomMode(d.Index); err != nil {
			c.Close()
			return err
		}
		log.Printf("usando %q (%d LEDs)", d.Name, d.NumLEDs)
	}
	e.mu.Lock()
	e.client = c
	e.devices = use
	e.all = all
	e.state = map[int][]rgb{}
	e.sent = map[int][]openrgb.Color{}
	e.lastErr = ""
	e.mu.Unlock()
	return nil
}

func matchesFilter(name string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, f := range filters {
		if f != "" && strings.Contains(strings.ToLower(name), strings.ToLower(f)) {
			return true
		}
	}
	return false
}

func (e *Engine) status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Status{Connected: e.client != nil, Err: e.lastErr}
	for _, d := range e.devices {
		s.Devices = append(s.Devices, d.Name)
	}
	if len(e.devices) > 0 {
		if st := e.state[e.devices[0].Index]; len(st) > 0 {
			s.Color = st[0]
		}
	}
	return s
}

func (e *Engine) report(s Status) {
	if e.onStatus != nil {
		e.onStatus(s)
	}
}

// frame computes and sends one update to every device.
func (e *Engine) frame(cfg Config, src thumbSource, dt time.Duration) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	alpha := smoothAlpha(cfg, dt)

	if t, ok := src(""); ok {
		e.preview = lerp(e.preview, adjust(regionColor(t, 0, t.W, cfg.Vivid), cfg.Saturation, cfg.Brightness), alpha)
	}
	if e.client == nil {
		return nil
	}
	// Re-send unchanged colors once a second so lights recover if another
	// program touched them.
	keepalive := time.Since(e.lastTx) > time.Second

	for _, d := range e.devices {
		t, ok := src(d.Name)
		if !ok {
			continue
		}
		cfg := cfg.forDevice(d.Name)
		target := ledColors(t, d.NumLEDs, cfg.Spread, cfg.Vivid)
		for i := range target {
			target[i] = adjust(target[i], cfg.Saturation, cfg.Brightness)
		}

		st := e.state[d.Index]
		if len(st) != d.NumLEDs {
			st = append([]rgb(nil), target...)
			e.state[d.Index] = st
		}
		out := make([]openrgb.Color, d.NumLEDs)
		changed := keepalive
		prev := e.sent[d.Index]
		for i := range st {
			st[i] = lerp(st[i], target[i], alpha)
			out[i] = openrgb.Color{R: uint8(math.Round(st[i].R)), G: uint8(math.Round(st[i].G)), B: uint8(math.Round(st[i].B))}
			if len(prev) != len(out) || prev[i] != out[i] {
				changed = true
			}
		}
		if !changed {
			continue
		}
		if err := e.client.UpdateLEDs(d.Index, out); err != nil {
			return err
		}
		e.sent[d.Index] = out
		e.lastTx = time.Now()
	}
	return nil
}

// DeviceView describes one OpenRGB controller for the preview window.
type DeviceView struct {
	Name   string
	Used   bool
	Zones  []openrgb.Zone
	Colors []openrgb.Color // last colors sent, one per LED (nil if unused)
	Region string          // which screen region the device follows
}

// Snapshot is what the preview window shows.
type Snapshot struct {
	Connected    bool
	Err          string
	Paused       bool
	Region       image.Rectangle
	RegionColor  rgb
	Devices      []DeviceView
	CorsairInfo  string // iCUE SDK status
	KeyboardInfo string // VialRGB keyboards status
}

// Snapshot returns the current state for display.
func (e *Engine) Snapshot() Snapshot {
	cfg := e.Config()
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Snapshot{
		Connected:   e.client != nil,
		Err:         e.lastErr,
		Paused:      cfg.Paused,
		Region:      e.region(cfg),
		RegionColor: e.preview,
	}
	used := map[int]bool{}
	for _, d := range e.devices {
		used[d.Index] = true
	}
	for _, d := range e.all {
		v := DeviceView{Name: d.Name, Used: used[d.Index], Zones: d.Zones}
		if v.Used {
			v.Colors = append([]openrgb.Color(nil), e.sent[d.Index]...)
		}
		s.Devices = append(s.Devices, v)
	}
	if !cfg.DisableCorsair {
		info, views := e.cue.Load().views()
		s.CorsairInfo = info
		s.Devices = append(s.Devices, views...)
	}
	if !cfg.DisableKeyboards {
		info, views := e.kb.Load().views()
		s.KeyboardInfo = info
		s.Devices = append(s.Devices, views...)
	}
	for i := range s.Devices {
		s.Devices[i].Region = "Región general"
		if r, ok := cfg.DeviceRegions[s.Devices[i].Name]; ok {
			s.Devices[i].Region = fmt.Sprintf("Región propia %d×%d en (%d, %d)", r.W, r.H, r.X, r.Y)
		}
		if _, ok := cfg.fixedColor(s.Devices[i].Name); ok {
			s.Devices[i].Region = "Color fijo " + cfg.DeviceColors[s.Devices[i].Name]
		}
	}
	return s
}

// StartTest shows red, green, blue and white on every device, 2 s each.
func (e *Engine) StartTest() { e.testFrom.Store(time.Now().UnixNano()) }

func (e *Engine) testColor(now time.Time) (rgb, bool) {
	from := e.testFrom.Load()
	if from == 0 {
		return rgb{}, false
	}
	i := int(now.Sub(time.Unix(0, from)) / testStep)
	if i < 0 || i >= len(testColors) {
		e.testFrom.CompareAndSwap(from, 0)
		return rgb{}, false
	}
	return testColors[i], true
}

func solidThumb(c rgb) Thumb {
	t := Thumb{W: 4, H: 1, Pix: make([]byte, 16)}
	for i := 0; i < 16; i += 4 {
		t.Pix[i], t.Pix[i+1], t.Pix[i+2] = uint8(c.B), uint8(c.G), uint8(c.R)
	}
	return t
}
