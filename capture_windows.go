package main

import (
	"errors"
	"image"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	procGetDC                  = user32.NewProc("GetDC")
	procReleaseDC              = user32.NewProc("ReleaseDC")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procSetStretchBltMode      = gdi32.NewProc("SetStretchBltMode")
	procStretchBlt             = gdi32.NewProc("StretchBlt")
)

const (
	srccopy     = 0x00CC0020
	captureblt  = 0x40000000
	halftone    = 4
	smXVirtual  = 76
	smYVirtual  = 77
	smCXVirtual = 78
	smCYVirtual = 79
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// virtualScreen returns the bounding box of all monitors in physical pixels.
func virtualScreen() image.Rectangle {
	m := func(i uintptr) int {
		v, _, _ := procGetSystemMetrics.Call(i)
		return int(int32(v))
	}
	x, y := m(smXVirtual), m(smYVirtual)
	return image.Rect(x, y, x+m(smCXVirtual), y+m(smCYVirtual))
}

// Capturer grabs a screen region and shrinks it to a small thumbnail using
// GDI's HALFTONE stretch, which averages pixels in hardware-friendly C code.
// Working on a thumbnail keeps the CPU cost tiny regardless of region size.
type Capturer struct {
	tw, th int
	memDC  uintptr
	bmp    uintptr
	old    uintptr
	pix    []byte // BGRA, top-down
}

// NewCapturer creates a capturer producing tw x th thumbnails.
func NewCapturer(tw, th int) (*Capturer, error) {
	screen, _, _ := procGetDC.Call(0)
	if screen == 0 {
		return nil, errors.New("GetDC failed")
	}
	defer procReleaseDC.Call(0, screen)
	c := &Capturer{tw: tw, th: th, pix: make([]byte, tw*th*4)}
	c.memDC, _, _ = procCreateCompatibleDC.Call(screen)
	c.bmp, _, _ = procCreateCompatibleBitmap.Call(screen, uintptr(tw), uintptr(th))
	if c.memDC == 0 || c.bmp == 0 {
		c.Close()
		return nil, errors.New("could not create capture bitmap")
	}
	c.old, _, _ = procSelectObject.Call(c.memDC, c.bmp)
	procSetStretchBltMode.Call(c.memDC, halftone)
	return c, nil
}

// Grab captures r (virtual-screen coordinates) and returns the thumbnail as
// BGRA bytes, tw*th*4 long. The slice is reused by the next call.
func (c *Capturer) Grab(r image.Rectangle) ([]byte, error) {
	screen, _, _ := procGetDC.Call(0)
	if screen == 0 {
		return nil, errors.New("GetDC failed")
	}
	ok, _, _ := procStretchBlt.Call(c.memDC, 0, 0, uintptr(c.tw), uintptr(c.th),
		screen, uintptr(r.Min.X), uintptr(r.Min.Y), uintptr(r.Dx()), uintptr(r.Dy()),
		srccopy|captureblt)
	procReleaseDC.Call(0, screen)
	if ok == 0 {
		return nil, errors.New("StretchBlt failed")
	}
	bi := bitmapInfoHeader{Width: int32(c.tw), Height: -int32(c.th), Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	// GetDIBits wants the bitmap deselected from any DC.
	procSelectObject.Call(c.memDC, c.old)
	n, _, _ := procGetDIBits.Call(c.memDC, c.bmp, 0, uintptr(c.th),
		uintptr(unsafe.Pointer(&c.pix[0])), uintptr(unsafe.Pointer(&bi)), 0)
	procSelectObject.Call(c.memDC, c.bmp)
	if n == 0 {
		return nil, errors.New("GetDIBits failed")
	}
	return c.pix, nil
}

// Close frees GDI resources.
func (c *Capturer) Close() {
	if c.memDC != 0 && c.old != 0 {
		procSelectObject.Call(c.memDC, c.old)
	}
	if c.bmp != 0 {
		procDeleteObject.Call(c.bmp)
	}
	if c.memDC != 0 {
		procDeleteDC.Call(c.memDC)
	}
}
