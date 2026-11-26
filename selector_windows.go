package main

import (
	"image"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procRegisterClassExW           = user32.NewProc("RegisterClassExW")
	procCreateWindowExW            = user32.NewProc("CreateWindowExW")
	procDefWindowProcW             = user32.NewProc("DefWindowProcW")
	procDestroyWindow              = user32.NewProc("DestroyWindow")
	procShowWindow                 = user32.NewProc("ShowWindow")
	procSetForegroundWindow        = user32.NewProc("SetForegroundWindow")
	procSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	procGetMessageW                = user32.NewProc("GetMessageW")
	procTranslateMessage           = user32.NewProc("TranslateMessage")
	procDispatchMessageW           = user32.NewProc("DispatchMessageW")
	procPostQuitMessage            = user32.NewProc("PostQuitMessage")
	procSetCapture                 = user32.NewProc("SetCapture")
	procReleaseCapture             = user32.NewProc("ReleaseCapture")
	procInvalidateRect             = user32.NewProc("InvalidateRect")
	procBeginPaint                 = user32.NewProc("BeginPaint")
	procEndPaint                   = user32.NewProc("EndPaint")
	procFillRect                   = user32.NewProc("FillRect")
	procFrameRect                  = user32.NewProc("FrameRect")
	procDrawTextW                  = user32.NewProc("DrawTextW")
	procLoadCursorW                = user32.NewProc("LoadCursorW")
	procGetClientRect              = user32.NewProc("GetClientRect")
	procSetProcessDpiAwarenessCtx  = user32.NewProc("SetProcessDpiAwarenessContext")
	procCreateSolidBrush           = gdi32.NewProc("CreateSolidBrush")
	procSetBkMode                  = gdi32.NewProc("SetBkMode")
	procSetTextColor               = gdi32.NewProc("SetTextColor")
	procCreateFontW                = gdi32.NewProc("CreateFontW")
)

const (
	wsPopup         = 0x80000000
	wsExLayered     = 0x00080000
	wsExTopmost     = 0x00000008
	wsExToolWindow  = 0x00000080
	lwaColorKey     = 0x1
	lwaAlpha        = 0x2
	wmDestroy       = 0x0002
	wmPaint         = 0x000F
	wmEraseBkgnd    = 0x0014
	wmKeyDown       = 0x0100
	wmMouseMove     = 0x0200
	wmLButtonDown   = 0x0201
	wmLButtonUp     = 0x0202
	wmRButtonDown   = 0x0204
	vkEscape        = 0x1B
	idcCross        = 32515
	swShow          = 5
	dtCenter        = 0x1
	dtWordBreak     = 0x10
	transparentMode = 1

	keyColor   = 0x00010001 // RGB(1,0,1): shown fully transparent
	dimColor   = 0x00000000
	frameColor = 0x0000C8FF // orange-ish (BGR)
	textColor  = 0x00FFFFFF
)

type rect struct{ Left, Top, Right, Bottom int32 }

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	Private uint32
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type paintStruct struct {
	Hdc       uintptr
	Erase     int32
	RcPaint   rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

// enableDPIAwareness makes the process see physical pixels on every monitor
// so selected regions and captures line up on scaled displays.
func enableDPIAwareness() {
	if procSetProcessDpiAwarenessCtx.Find() == nil {
		const perMonitorAwareV2 = ^uintptr(3) // (DPI_AWARENESS_CONTEXT)-4
		procSetProcessDpiAwarenessCtx.Call(perMonitorAwareV2)
	}
}

// selection holds the state of the one overlay that can be open at a time.
var selection struct {
	sync.Mutex // held for the whole lifetime of an overlay

	origin     image.Point // virtual screen origin
	dragging   bool
	start, end image.Point // client coordinates
	result     image.Rectangle
	ok         bool
	brushes    [3]uintptr // dim, key, frame
	font       uintptr
	hint       string
	primaryW   int32
}

var (
	registerOnce sync.Once
	className    = windows.StringToUTF16Ptr("rgblightsSelector")
)

func registerSelectorClass() {
	cursor, _, _ := procLoadCursorW.Call(0, idcCross)
	wc := wndClassEx{
		WndProc:   windows.NewCallback(selectorProc),
		Cursor:    cursor,
		ClassName: className,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	selection.brushes[0], _, _ = procCreateSolidBrush.Call(dimColor)
	selection.brushes[1], _, _ = procCreateSolidBrush.Call(keyColor)
	selection.brushes[2], _, _ = procCreateSolidBrush.Call(frameColor)
	face := windows.StringToUTF16Ptr("Segoe UI")
	selection.font, _, _ = procCreateFontW.Call(^uintptr(31), 0, 0, 0, 600, 0, 0, 0, 0, 0, 0, 5, 0,
		uintptr(unsafe.Pointer(face))) // height -32, weight 600, CLEARTYPE_QUALITY
}

// SelectRegion shows a dimmed full-desktop overlay and lets the user drag a
// rectangle. It blocks until the user finishes (ok=true) or cancels with
// Esc / right click (ok=false). The rectangle is in virtual-screen pixels.
func SelectRegion(hint string) (r image.Rectangle, ok bool) {
	selection.Lock()
	defer selection.Unlock()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	registerOnce.Do(registerSelectorClass)

	vs := virtualScreen()
	selection.origin = vs.Min
	selection.dragging = false
	selection.ok = false
	selection.hint = hint
	w, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN: primary monitor width
	selection.primaryW = int32(w)

	hwnd, _, _ := procCreateWindowExW.Call(
		wsExLayered|wsExTopmost|wsExToolWindow,
		uintptr(unsafe.Pointer(className)), 0, wsPopup,
		uintptr(vs.Min.X), uintptr(vs.Min.Y), uintptr(vs.Dx()), uintptr(vs.Dy()),
		0, 0, 0, 0)
	if hwnd == 0 {
		return image.Rectangle{}, false
	}
	procSetLayeredWindowAttributes.Call(hwnd, keyColor, 120, lwaColorKey|lwaAlpha)
	procShowWindow.Call(hwnd, swShow)
	procSetForegroundWindow.Call(hwnd)

	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	return selection.result, selection.ok
}

func pointFromLParam(lp uintptr) image.Point {
	return image.Pt(int(int16(lp&0xFFFF)), int(int16((lp>>16)&0xFFFF)))
}

func selRect() image.Rectangle {
	return image.Rectangle{selection.start, selection.end}.Canon()
}

func selectorProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch message {
	case wmLButtonDown:
		selection.dragging = true
		selection.start = pointFromLParam(lParam)
		selection.end = selection.start
		procSetCapture.Call(hwnd)
		return 0
	case wmMouseMove:
		if selection.dragging {
			selection.end = pointFromLParam(lParam)
			procInvalidateRect.Call(hwnd, 0, 0)
		}
		return 0
	case wmLButtonUp:
		if selection.dragging {
			selection.dragging = false
			procReleaseCapture.Call()
			selection.end = pointFromLParam(lParam)
			r := selRect()
			if r.Dx() >= 4 && r.Dy() >= 4 {
				selection.result = r.Add(selection.origin)
				selection.ok = true
				procDestroyWindow.Call(hwnd)
			} else {
				procInvalidateRect.Call(hwnd, 0, 0)
			}
		}
		return 0
	case wmRButtonDown:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmKeyDown:
		if wParam == vkEscape {
			procDestroyWindow.Call(hwnd)
		}
		return 0
	case wmEraseBkgnd:
		return 1
	case wmPaint:
		paintSelector(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return ret
}

func paintSelector(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))

	var client rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))
	w, h := client.Right, client.Bottom

	// Double buffer to avoid flicker while dragging.
	mem, _, _ := procCreateCompatibleDC.Call(hdc)
	bmp, _, _ := procCreateCompatibleBitmap.Call(hdc, uintptr(w), uintptr(h))
	old, _, _ := procSelectObject.Call(mem, bmp)
	defer func() {
		procSelectObject.Call(mem, old)
		procDeleteObject.Call(bmp)
		procDeleteDC.Call(mem)
	}()

	procFillRect.Call(mem, uintptr(unsafe.Pointer(&client)), selection.brushes[0])

	if r := selRect(); selection.dragging && !r.Empty() {
		frame := rect{int32(r.Min.X) - 2, int32(r.Min.Y) - 2, int32(r.Max.X) + 2, int32(r.Max.Y) + 2}
		procFillRect.Call(mem, uintptr(unsafe.Pointer(&frame)), selection.brushes[2])
		hole := rect{int32(r.Min.X), int32(r.Min.Y), int32(r.Max.X), int32(r.Max.Y)}
		procFillRect.Call(mem, uintptr(unsafe.Pointer(&hole)), selection.brushes[1])
	}

	// Hint text centred near the top of the primary monitor.
	ox, oy := -int32(selection.origin.X), -int32(selection.origin.Y)
	tr := rect{ox + 40, oy + 40, ox + selection.primaryW - 40, oy + 200}
	procSetBkMode.Call(mem, transparentMode)
	procSetTextColor.Call(mem, textColor)
	oldFont, _, _ := procSelectObject.Call(mem, selection.font)
	text, _ := windows.UTF16FromString(selection.hint)
	procDrawTextW.Call(mem, uintptr(unsafe.Pointer(&text[0])), ^uintptr(0),
		uintptr(unsafe.Pointer(&tr)), dtCenter|dtWordBreak)
	procSelectObject.Call(mem, oldFont)

	procBitBlt.Call(hdc, 0, 0, uintptr(w), uintptr(h), mem, 0, 0, srccopy)
}
