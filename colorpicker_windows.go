package main

import (
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procChooseColorW = windows.NewLazySystemDLL("comdlg32.dll").NewProc("ChooseColorW")

type chooseColor struct {
	StructSize   uint32
	Owner        uintptr
	Instance     uintptr
	Result       uint32
	CustomColors *[16]uint32
	Flags        uint32
	CustData     uintptr
	Hook         uintptr
	TemplateName *uint16
}

const (
	ccRGBInit  = 0x1
	ccFullOpen = 0x2
	ccAnyColor = 0x100
)

var (
	pickerMu     sync.Mutex
	customColors [16]uint32 // remembered between dialogs, like Windows apps do
)

// PickColor shows the standard Windows color dialog starting at initial.
// It blocks until the user picks a color (ok=true) or cancels.
func PickColor(initial rgb) (c rgb, ok bool) {
	pickerMu.Lock()
	defer pickerMu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	cc := chooseColor{
		Result:       uint32(uint8(initial.R)) | uint32(uint8(initial.G))<<8 | uint32(uint8(initial.B))<<16,
		CustomColors: &customColors,
		Flags:        ccRGBInit | ccFullOpen | ccAnyColor,
	}
	cc.StructSize = uint32(unsafe.Sizeof(cc))
	if r, _, _ := procChooseColorW.Call(uintptr(unsafe.Pointer(&cc))); r == 0 {
		return rgb{}, false
	}
	return rgb{float64(cc.Result & 0xFF), float64(cc.Result >> 8 & 0xFF), float64(cc.Result >> 16 & 0xFF)}, true
}
