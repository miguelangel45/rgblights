package main

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"rgblights/openrgb"
)

var (
	procSetTimer     = user32.NewProc("SetTimer")
	procKillTimer    = user32.NewProc("KillTimer")
	procIsWindow     = user32.NewProc("IsWindow")
	procShowWindowP  = user32.NewProc("ShowWindow")
	procGetDpiForWnd = user32.NewProc("GetDpiForWindow")
)

const (
	wsOverlappedWindow = 0x00CF0000
	wsVisible          = 0x10000000
	wmTimer            = 0x0113
	wmSize             = 0x0005
	cwUseDefault       = 0x80000000
	swRestore          = 9
	dtSingleLine       = 0x20
	dtEndEllipsis      = 0x8000
	dtVCenter          = 0x4
	dtLeft             = 0x0

	bgColor     = 0x00201C1C // BGR
	cardColor   = 0x002E2828
	dimText     = 0x00A0A0A0
	warnText    = 0x0040B0FF
	brightText  = 0x00F0F0F0
	okText      = 0x0080E080
	borderColor = 0x00504848
)

// preview is the "Monitor de colores" window. Only one can be open.
var preview struct {
	sync.Mutex
	hwnd   uintptr
	engine *Engine
	fonts  struct{ normal, bold, big uintptr }
	scale  float64
}

var (
	previewOnce  sync.Once
	previewClass = windows.StringToUTF16Ptr("rgblightsPreview")
)

func registerPreviewClass() {
	cursor, _, _ := procLoadCursorW.Call(0, 32512) // IDC_ARROW
	wc := wndClassEx{
		WndProc:   windows.NewCallback(previewProc),
		Cursor:    cursor,
		ClassName: previewClass,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
}

func makeFont(px int, weight int) uintptr {
	face := windows.StringToUTF16Ptr("Segoe UI")
	f, _, _ := procCreateFontW.Call(uintptr(-int32(px)), 0, 0, 0, uintptr(weight), 0, 0, 0, 0, 0, 0, 5, 0,
		uintptr(unsafe.Pointer(face)))
	return f
}

// ShowPreview opens the live color/device window, or focuses it if it is
// already open. It returns immediately.
func ShowPreview(e *Engine) {
	preview.Lock()
	if preview.hwnd != 0 {
		if ok, _, _ := procIsWindow.Call(preview.hwnd); ok != 0 {
			procShowWindowP.Call(preview.hwnd, swRestore)
			procSetForegroundWindow.Call(preview.hwnd)
			preview.Unlock()
			return
		}
	}
	preview.engine = e
	preview.Unlock()

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		previewOnce.Do(registerPreviewClass)

		title := windows.StringToUTF16Ptr("RGB Lights — colores y dispositivos")
		hwnd, _, _ := procCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(previewClass)), uintptr(unsafe.Pointer(title)),
			wsOverlappedWindow|wsVisible, cwUseDefault, cwUseDefault, 10, 10, 0, 0, 0, 0)
		if hwnd == 0 {
			return
		}
		// Size the window for the monitor's DPI.
		dpi := uintptr(96)
		if procGetDpiForWnd.Find() == nil {
			if d, _, _ := procGetDpiForWnd.Call(hwnd); d != 0 {
				dpi = d
			}
		}
		s := float64(dpi) / 96
		preview.Lock()
		preview.hwnd = hwnd
		preview.scale = s
		preview.fonts.normal = makeFont(int(15*s), 400)
		preview.fonts.bold = makeFont(int(16*s), 600)
		preview.fonts.big = makeFont(int(20*s), 600)
		preview.Unlock()
		procSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(int(620*s)), uintptr(int(520*s)), swpNoMove|swpNoZOrder)
		procSetForegroundWindow.Call(hwnd)
		procSetTimer.Call(hwnd, 1, 66, 0) // ~15 fps refresh

		var m msg
		for {
			ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(ret) <= 0 {
				break
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
		preview.Lock()
		preview.hwnd = 0
		for _, f := range []uintptr{preview.fonts.normal, preview.fonts.bold, preview.fonts.big} {
			procDeleteObject.Call(f)
		}
		preview.Unlock()
	}()
}

var (
	procSetWindowPos = user32.NewProc("SetWindowPos")
)

const (
	swpNoMove   = 0x2
	swpNoZOrder = 0x4
)

func previewProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch message {
	case wmTimer, wmSize:
		procInvalidateRect.Call(hwnd, 0, 0)
		if message == wmTimer {
			return 0
		}
	case wmEraseBkgnd:
		return 1
	case wmPaint:
		paintPreview(hwnd)
		return 0
	case wmDestroy:
		procKillTimer.Call(hwnd, 1)
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return ret
}

func colorref(r, g, b uint8) uintptr { return uintptr(r) | uintptr(g)<<8 | uintptr(b)<<16 }

// painter wraps a DC with a few drawing helpers.
type painter struct {
	dc    uintptr
	scale float64
}

func (p painter) px(v float64) int32 { return int32(v * p.scale) }

func (p painter) fill(r rect, c uintptr) {
	br, _, _ := procCreateSolidBrush.Call(c)
	procFillRect.Call(p.dc, uintptr(unsafe.Pointer(&r)), br)
	procDeleteObject.Call(br)
}

func (p painter) frame(r rect, c uintptr) {
	br, _, _ := procCreateSolidBrush.Call(c)
	procFrameRect.Call(p.dc, uintptr(unsafe.Pointer(&r)), br)
	procDeleteObject.Call(br)
}

func (p painter) text(s string, r rect, font, color uintptr, flags uintptr) {
	old, _, _ := procSelectObject.Call(p.dc, font)
	procSetTextColor.Call(p.dc, color)
	u, _ := windows.UTF16FromString(s)
	procDrawTextW.Call(p.dc, uintptr(unsafe.Pointer(&u[0])), ^uintptr(0), uintptr(unsafe.Pointer(&r)), flags)
	procSelectObject.Call(p.dc, old)
}

func paintPreview(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))

	var client rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))
	w, h := client.Right, client.Bottom
	if w <= 0 || h <= 0 {
		return
	}
	mem, _, _ := procCreateCompatibleDC.Call(hdc)
	bmp, _, _ := procCreateCompatibleBitmap.Call(hdc, uintptr(w), uintptr(h))
	old, _, _ := procSelectObject.Call(mem, bmp)
	defer func() {
		procSelectObject.Call(mem, old)
		procDeleteObject.Call(bmp)
		procDeleteDC.Call(mem)
	}()

	preview.Lock()
	e, fonts, scale := preview.engine, preview.fonts, preview.scale
	preview.Unlock()
	p := painter{mem, scale}
	procSetBkMode.Call(mem, transparentMode)
	p.fill(client, bgColor)

	snap := e.Snapshot()
	margin := p.px(16)
	x0, x1 := margin, w-margin
	y := margin
	line := func(s string, font, color uintptr) {
		lh := p.px(22)
		p.text(s, rect{x0, y, x1, y + lh}, font, color, dtLeft|dtSingleLine|dtVCenter|dtEndEllipsis)
		y += lh
	}

	// Connection + state.
	switch {
	case snap.Connected:
		line("● Conectado a OpenRGB", fonts.bold, okText)
	case snap.Err != "":
		line("● "+snap.Err, fonts.bold, warnText)
	default:
		line("● Buscando OpenRGB…", fonts.bold, warnText)
	}
	state := "Activo"
	if snap.Paused {
		state = "En pausa"
	}
	line(fmt.Sprintf("%s · Región %d×%d en (%d, %d)", state,
		snap.Region.Dx(), snap.Region.Dy(), snap.Region.Min.X, snap.Region.Min.Y), fonts.normal, dimText)
	if snap.CorsairInfo != "" {
		col := uintptr(warnText)
		if snap.CorsairInfo == "iCUE: conectado" {
			col = okText
		}
		line("● Corsair "+snap.CorsairInfo, fonts.normal, col)
	}
	if snap.KeyboardInfo != "" {
		col := uintptr(warnText)
		if snap.KeyboardInfo == "conectado" {
			col = okText
		}
		line("● Teclado Vial: "+snap.KeyboardInfo, fonts.normal, col)
	}
	y += p.px(10)

	// Big swatch with the region color.
	c := snap.RegionColor
	sw := rect{x0, y, x0 + p.px(96), y + p.px(64)}
	p.fill(sw, colorref(uint8(c.R), uint8(c.G), uint8(c.B)))
	p.frame(sw, borderColor)
	tx := sw.Right + p.px(14)
	p.text("Color de la región", rect{tx, y + p.px(6), x1, y + p.px(32)}, fonts.big, brightText, dtLeft|dtSingleLine)
	p.text(fmt.Sprintf("RGB(%d, %d, %d)  ·  #%02X%02X%02X", uint8(c.R), uint8(c.G), uint8(c.B), uint8(c.R), uint8(c.G), uint8(c.B)),
		rect{tx, y + p.px(34), x1, y + p.px(58)}, fonts.normal, dimText, dtLeft|dtSingleLine)
	y = sw.Bottom + p.px(18)

	line(fmt.Sprintf("Dispositivos (%d)", len(snap.Devices)), fonts.big, brightText)
	y += p.px(4)
	if len(snap.Devices) == 0 {
		line("Ninguno. Abre OpenRGB y activa SDK Server → Start Server.", fonts.normal, dimText)
	}

	for _, d := range snap.Devices {
		if y > h {
			break
		}
		top := y
		y += p.px(8)
		x0 += p.px(10)
		x1 -= p.px(10)
		status, col := "", uintptr(brightText)
		if !d.Used {
			status, col = "  (no usado: filtro en config o sin LEDs)", dimText
		}
		line(d.Name+status, fonts.bold, col)
		line(d.Region, fonts.normal, dimText)

		led := 0
		for _, z := range d.Zones {
			info := fmt.Sprintf("%s — %d LEDs", z.Name, z.NumLEDs)
			color := uintptr(dimText)
			resizeHint := z.NumLEDs == 0 && z.MinLEDs != z.MaxLEDs
			if resizeHint {
				info += "  ⚠ sin tamaño configurado"
				color = warnText
			}
			line(info, fonts.normal, color)
			if resizeHint {
				line("En OpenRGB: elige esta zona → Resize → nº de LEDs de tu tira/ventiladores, y luego Reconectar", fonts.normal, dimText)
			}
			if z.NumLEDs > 0 {
				y = p.swatches(d.Colors, led, z.NumLEDs, x0, x1, y)
			}
			led += z.NumLEDs
		}
		x0 -= p.px(10)
		x1 += p.px(10)
		y += p.px(8)
		p.frame(rect{x0, top, x1, y}, borderColor)
		y += p.px(8)
	}

	procBitBlt.Call(hdc, 0, 0, uintptr(w), uintptr(h), mem, 0, 0, srccopy)
}

// swatches draws n LED squares starting at colors[from], wrapping rows.
func (p painter) swatches(colors []openrgb.Color, from, n int, x0, x1, y int32) int32 {
	size := p.px(18)
	gap := p.px(3)
	if per := int((x1 - x0) / (size + gap)); n > per*4 {
		// Lots of LEDs: shrink so they fit in ~4 rows.
		size = max32(p.px(4), (x1-x0)/int32((n+3)/4)-gap)
	}
	x := x0
	for i := 0; i < n; i++ {
		if x+size > x1 {
			x = x0
			y += size + gap
		}
		r := rect{x, y + gap, x + size, y + gap + size}
		if idx := from + i; idx < len(colors) {
			c := colors[idx]
			p.fill(r, colorref(c.R, c.G, c.B))
		} else {
			p.fill(r, cardColor)
		}
		p.frame(r, borderColor)
		x += size + gap
	}
	return y + size + 2*gap
}

func max32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}
