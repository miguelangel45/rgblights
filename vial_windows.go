package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"rgblights/hid"
	"rgblights/openrgb"
)

// Keyboards drives QMK/Vial keyboards that expose VialRGB over raw HID
// (usage page 0xFF60), such as a Corne running Vial.
//
// Protocol (vial-qmk quantum/vialrgb.c), 32-byte reports:
//
//	get: 08 40            → 08 40 verLo verHi maxBrightness
//	get: 08 41            → 08 41 modeLo modeHi speed h s v
//	get: 08 43            → 08 43 countLo countHi
//	get: 08 44 idxLo idxHi → 08 44 x y flags row col
//	set: 07 41 modeLo modeHi speed h s v          (not saved to EEPROM)
//	set: 07 42 firstLo firstHi n h s v ...       (direct colors, ≤9 LEDs)
type Keyboards struct {
	mu       sync.Mutex
	kbs      []*vialKeyboard
	active   bool
	nextScan time.Time
	status   string
}

type vialKeyboard struct {
	dev    *hid.Device
	name   string
	maxV   uint8
	prev   [6]byte // mode, speed, h, s, v to restore on release
	u, v   []float64
	state  []rgb
	sent   []openrgb.Color
	lastTx time.Time
	mode   uint16  // effect we last set; modeUnknown after taking control
	x      []uint8 // LED x in QMK units (0..224), for the gradient preview

	gradL, gradR rgb      // smoothed left/right colors for the gradient
	gradSent     [4]uint8 // h, s, v, speed last sent
}

const (
	vialEffectDirect = 1
	vialEffectSolid  = 2
	// vialEffectGradientLR is VIALRGB_EFFECT_GRADIENT_LEFT_RIGHT.
	vialEffectGradientLR = 5
	modeUnknown          = 0xFFFF
	vialChunk            = 9
)

func NewKeyboards() *Keyboards { return &Keyboards{status: "buscando…"} }

// scanLocked opens every VialRGB keyboard that is plugged in.
func (k *Keyboards) scanLocked() {
	infos, err := hid.FindUsage(0xFF60, 0x61)
	if err != nil {
		k.status = err.Error()
		return
	}
	for _, in := range infos {
		kb, err := openVial(in)
		if err != nil {
			log.Printf("teclado %q: %v", in.Product, err)
			continue
		}
		log.Printf("teclado: usando %q (%d LEDs)", kb.name, len(kb.u))
		k.kbs = append(k.kbs, kb)
	}
	switch {
	case len(k.kbs) > 0:
		k.status = "conectado"
	case len(infos) > 0:
		k.status = "teclado VIA/Vial sin VialRGB"
	default:
		k.status = "ninguno con VialRGB"
	}
}

func openVial(in hid.Info) (*vialKeyboard, error) {
	d, err := hid.Open(in)
	if err != nil {
		return nil, err
	}
	kb := &vialKeyboard{dev: d, name: in.Product}
	fail := func(err error) (*vialKeyboard, error) { d.Close(); return nil, err }

	r, err := kb.get(0x40)
	if err != nil {
		return fail(err)
	}
	if binary.LittleEndian.Uint16(r[2:]) < 1 {
		return fail(errors.New("sin VialRGB"))
	}
	kb.maxV = r[4]
	if kb.maxV == 0 {
		kb.maxV = 255
	}
	if r, err = kb.get(0x41); err != nil {
		return fail(err)
	}
	copy(kb.prev[:], r[2:8])

	if r, err = kb.get(0x43); err != nil {
		return fail(err)
	}
	n := int(binary.LittleEndian.Uint16(r[2:]))
	if n == 0 {
		return fail(errors.New("0 LEDs"))
	}
	pos := make([]cueLedPosition, n)
	for i := 0; i < n; i++ {
		r, err := kb.get(0x44, byte(i), byte(i>>8))
		if err != nil {
			return fail(err)
		}
		pos[i] = cueLedPosition{ID: uint32(i), CX: float64(r[2]), CY: float64(r[3])}
		kb.x = append(kb.x, r[2])
	}
	kb.u, kb.v = normalisePositions(pos)
	return kb, nil
}

// get sends a VialRGB query and checks the reply echoes it.
func (kb *vialKeyboard) get(cmd byte, args ...byte) ([]byte, error) {
	r, err := kb.dev.Request(append([]byte{0x08, cmd}, args...))
	if err != nil {
		return nil, err
	}
	if len(r) < 8 || r[0] != 0x08 || r[1] != cmd {
		return nil, fmt.Errorf("respuesta inesperada % X", r[:min(len(r), 8)])
	}
	return r, nil
}

func (kb *vialKeyboard) setMode(mode uint16, speed, h, s, v byte) error {
	return kb.dev.Write([]byte{0x07, 0x41, byte(mode), byte(mode >> 8), speed, h, s, v})
}

// sync takes or releases the keyboards. Taking switches them to the Direct
// effect; releasing restores the effect they had when we found them.
func (k *Keyboards) sync(want bool) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.kbs) == 0 && want && time.Now().After(k.nextScan) {
		k.nextScan = time.Now().Add(5 * time.Second)
		k.scanLocked()
		k.active = false
	}
	if want == k.active {
		return
	}
	for _, kb := range k.kbs {
		var err error
		if want {
			kb.mode = modeUnknown // frame picks Direct or Solid
		} else {
			err = kb.setMode(binary.LittleEndian.Uint16(kb.prev[:]), kb.prev[2], kb.prev[3], kb.prev[4], kb.prev[5])
		}
		if err != nil {
			log.Printf("teclado %s: %v", kb.name, err)
		}
		kb.sent = nil
	}
	k.active = want
}

func (k *Keyboards) frame(cfg Config, src thumbSource, alpha float64) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.active {
		return
	}
	for i := 0; i < len(k.kbs); i++ {
		if err := k.kbs[i].frame(cfg, src, alpha); err != nil {
			log.Printf("teclado %s desconectado: %v", k.kbs[i].name, err)
			k.kbs[i].dev.Close()
			k.kbs = append(k.kbs[:i], k.kbs[i+1:]...)
			i--
		}
	}
}

func (kb *vialKeyboard) frame(cfg Config, src thumbSource, alpha float64) error {
	cfg = cfg.forDevice(kb.name)
	t, ok := src(kb.name)
	if !ok {
		return nil
	}
	n := len(kb.u)
	whole := adjust(regionColor(t, 0, t.W, cfg.Vivid), cfg.Saturation, cfg.Brightness)
	if len(kb.state) != n {
		kb.state = make([]rgb, n)
		for i := range kb.state {
			kb.state[i] = whole
		}
	}
	out := make([]openrgb.Color, n)
	for i := range out {
		target := whole
		if cfg.Spread && cfg.KeyboardPerKey {
			target = adjust(spotColor(t, kb.u[i], kb.v[i], cfg.Vivid), cfg.Saturation, cfg.Brightness)
		}
		kb.state[i] = lerp(kb.state[i], target, alpha)
		s := kb.state[i]
		out[i] = openrgb.Color{R: uint8(math.Round(s.R)), G: uint8(math.Round(s.G)), B: uint8(math.Round(s.B))}
	}
	full := kb.sent == nil || time.Since(kb.lastTx) > time.Second

	if cfg.Spread && !cfg.KeyboardPerKey {
		return kb.gradientFrame(cfg, t, alpha, full)
	}

	// Split keyboards (Corne…) only show Direct per-key colors on the half
	// plugged into USB: the firmware syncs the effect and its global color to
	// the other half, not the per-key buffer. So unless the user asked for
	// per-key colors, use the Solid effect, which lights both halves.
	if !(cfg.Spread && cfg.KeyboardPerKey) {
		c := out[0]
		if !full && kb.mode == vialEffectSolid && len(kb.sent) > 0 && kb.sent[0] == c {
			return nil
		}
		h, s, v := toHSV(c)
		if err := kb.setMode(vialEffectSolid, kb.prev[2], h, s, uint8(int(v)*int(kb.maxV)/255)); err != nil {
			return err
		}
		kb.mode = vialEffectSolid
		kb.lastTx = time.Now()
		kb.sent = out
		return nil
	}
	if kb.mode != vialEffectDirect {
		if err := kb.setMode(vialEffectDirect, kb.prev[2], 0, 0, kb.maxV); err != nil {
			return err
		}
		kb.mode = vialEffectDirect
		full = true
	}
	// Send only the chunks that changed, plus a full refresh every second.
	for first := 0; first < n; first += vialChunk {
		end := min(first+vialChunk, n)
		if !full && equalColors(out[first:end], kb.sent[first:end]) {
			continue
		}
		pkt := []byte{0x07, 0x42, byte(first), byte(first >> 8), byte(end - first)}
		for _, c := range out[first:end] {
			h, s, v := toHSV(c)
			pkt = append(pkt, h, s, uint8(int(v)*int(kb.maxV)/255))
		}
		if err := kb.dev.Write(pkt); err != nil {
			return err
		}
	}
	if full {
		kb.lastTx = time.Now()
	}
	kb.sent = out
	return nil
}

// gradientFrame drives QMK's Gradient Left Right effect, which every half of
// a split keyboard renders itself from the synced mode/speed/HSV:
//
//	hue(x) = baseHue + (scale8(64, speed) * x) >> 5,   x = 0..224
//
// so the hue grows by about 1.75*speed from the left edge to the right one.
// We set baseHue to the left of the region and pick speed so the right edge
// lands on the right of the region. Saturation and brightness are shared.
func (kb *vialKeyboard) gradientFrame(cfg Config, t Thumb, alpha float64, full bool) error {
	edge := max(t.W/4, 1)
	left := adjust(regionColor(t, 0, edge, cfg.Vivid), cfg.Saturation, cfg.Brightness)
	right := adjust(regionColor(t, t.W-edge, t.W, cfg.Vivid), cfg.Saturation, cfg.Brightness)
	if kb.sent == nil || kb.mode != vialEffectGradientLR {
		kb.gradL, kb.gradR = left, right
	}
	kb.gradL = lerp(kb.gradL, left, alpha)
	kb.gradR = lerp(kb.gradR, right, alpha)

	h, s, v, speed := gradientParams(toColor(kb.gradL), toColor(kb.gradR))
	params := [4]uint8{h, s, v, speed}
	if !full && kb.mode == vialEffectGradientLR && params == kb.gradSent {
		return nil
	}
	if err := kb.setMode(vialEffectGradientLR, speed, h, s, uint8(int(v)*int(kb.maxV)/255)); err != nil {
		return err
	}
	kb.mode = vialEffectGradientLR
	kb.gradSent = params
	kb.lastTx = time.Now()

	// What the firmware will show, for the preview window.
	out := make([]openrgb.Color, len(kb.x))
	for i, x := range kb.x {
		out[i] = hsvToColor(gradientHue(h, speed, x), s, v)
	}
	kb.sent = out
	return nil
}

// gradientParams picks the effect's base HSV and speed for a left→right
// gradient between two colors.
func gradientParams(left, right openrgb.Color) (h, s, v, speed uint8) {
	hl, sl, vl := toHSV(left)
	hr, sr, vr := toHSV(right)
	s = uint8((int(sl) + int(sr)) / 2)
	v = uint8((int(vl) + int(vr)) / 2)
	// The effect can only increase hue (wrapping at 256). Going "up" from the
	// left hue to the right one is fine for short hops; when the short way is
	// backwards, a full trip round the wheel would paint a rainbow, so use a
	// flat color at the midpoint instead.
	d := int(hr-hl) & 0xFF
	if sl < 40 || sr < 40 { // a grey end has no meaningful hue
		d = 0
	}
	if d > 128 {
		mid := uint8(int(hl) + (d-256)/2)
		return mid, s, v, 0
	}
	sp := math.Round(float64(d) / 1.75)
	return hl, s, v, uint8(math.Min(sp, 255))
}

// gradientHue reproduces QMK's GRADIENT_LEFT_RIGHT hue for an LED at x.
func gradientHue(base, speed, x uint8) uint8 {
	scale := uint16(64) * uint16(speed) >> 8 // scale8(64, speed)
	return base + uint8((scale*uint16(x))>>5)
}

func toColor(c rgb) openrgb.Color {
	return openrgb.Color{R: uint8(math.Round(c.R)), G: uint8(math.Round(c.G)), B: uint8(math.Round(c.B))}
}

// hsvToColor converts QMK 0-255 HSV to RGB.
func hsvToColor(h, s, v uint8) openrgb.Color {
	r, g, b := hsv(float64(h)/256, float64(s)/255, float64(v)/255)
	return openrgb.Color{R: uint8(r * 255), G: uint8(g * 255), B: uint8(b * 255)}
}

func equalColors(a, b []openrgb.Color) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// toHSV converts to QMK's 0-255 HSV.
func toHSV(c openrgb.Color) (h, s, v uint8) {
	r, g, b := float64(c.R), float64(c.G), float64(c.B)
	mx := math.Max(r, math.Max(g, b))
	mn := math.Min(r, math.Min(g, b))
	if mx == 0 {
		return 0, 0, 0
	}
	d := mx - mn
	var hue float64
	switch {
	case d == 0:
		hue = 0
	case mx == r:
		hue = math.Mod((g-b)/d, 6)
	case mx == g:
		hue = (b-r)/d + 2
	default:
		hue = (r-g)/d + 4
	}
	hue /= 6
	if hue < 0 {
		hue++
	}
	return uint8(math.Round(hue*255)) % 255, uint8(math.Round(d / mx * 255)), uint8(mx)
}

func (k *Keyboards) views() (status string, out []DeviceView) {
	if k == nil {
		return "iniciando…", nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, kb := range k.kbs {
		out = append(out, DeviceView{Name: kb.name, Used: k.active,
			Zones: []openrgb.Zone{{Name: "Teclas", NumLEDs: len(kb.u)}}, Colors: append([]openrgb.Color(nil), kb.sent...)})
	}
	return k.status, out
}

// Close restores each keyboard's own effect.
func (k *Keyboards) Close() {
	if k == nil {
		return
	}
	k.sync(false)
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, kb := range k.kbs {
		kb.dev.Close()
	}
	k.kbs = nil
}
