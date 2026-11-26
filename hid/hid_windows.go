// Package hid is a tiny Windows raw-HID helper: find an interface by
// VID/PID/usage page and exchange fixed-size reports with it.
package hid

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	hidDLL                = windows.NewLazySystemDLL("hid.dll")
	procGetPreparsedData  = hidDLL.NewProc("HidD_GetPreparsedData")
	procFreePreparsedData = hidDLL.NewProc("HidD_FreePreparsedData")
	procGetCaps           = hidDLL.NewProc("HidP_GetCaps")
	procGetProduct        = hidDLL.NewProc("HidD_GetProductString")

	guidHID = windows.GUID{Data1: 0x4D1E55B2, Data2: 0xF16F, Data3: 0x11CF,
		Data4: [8]byte{0x88, 0xCB, 0x00, 0x11, 0x11, 0x00, 0x00, 0x30}}
)

type caps struct {
	Usage, UsagePage                uint16
	InputLen, OutputLen, FeatureLen uint16
	_                               [17]uint16
	_                               [10]uint16
}

// Info describes one HID interface.
type Info struct {
	Path      string
	Product   string
	UsagePage uint16
	Usage     uint16
	InputLen  int
	OutputLen int
}

// Find lists present HID interfaces of the given vendor/product.
func Find(vid, pid uint16) ([]Info, error) {
	want := strings.ToLower(fmt.Sprintf("vid_%04x&pid_%04x", vid, pid))
	return find(func(p string) bool { return strings.Contains(strings.ToLower(p), want) })
}

// FindUsage lists every present HID interface with the given usage page and
// usage, from any vendor.
func FindUsage(page, usage uint16) ([]Info, error) {
	all, err := find(func(string) bool { return true })
	var out []Info
	for _, in := range all {
		if in.UsagePage == page && in.Usage == usage {
			out = append(out, in)
		}
	}
	return out, err
}

func find(match func(path string) bool) ([]Info, error) {
	paths, err := windows.CM_Get_Device_Interface_List("", &guidHID, windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, p := range paths {
		if !match(p) {
			continue
		}
		h, err := open(p, 0) // no access needed to query attributes
		if err != nil {
			continue
		}
		info := Info{Path: p}
		var pp uintptr
		if r, _, _ := procGetPreparsedData.Call(uintptr(h), uintptr(unsafe.Pointer(&pp))); r != 0 {
			var c caps
			procGetCaps.Call(pp, uintptr(unsafe.Pointer(&c)))
			procFreePreparsedData.Call(pp)
			info.UsagePage, info.Usage = c.UsagePage, c.Usage
			info.InputLen, info.OutputLen = int(c.InputLen), int(c.OutputLen)
		}
		buf := make([]uint16, 128)
		if r, _, _ := procGetProduct.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2)); r != 0 {
			info.Product = windows.UTF16ToString(buf)
		}
		windows.CloseHandle(h)
		out = append(out, info)
	}
	return out, nil
}

func open(path string, access uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
}

// Device is an open HID interface.
type Device struct {
	h             windows.Handle
	inLen, outLen int
}

// Open opens an interface for reading and writing reports.
func Open(info Info) (*Device, error) {
	if info.InputLen == 0 || info.OutputLen == 0 {
		return nil, errors.New("hid: interface has no input/output reports")
	}
	h, err := open(info.Path, windows.GENERIC_READ|windows.GENERIC_WRITE)
	if err != nil {
		return nil, err
	}
	return &Device{h: h, inLen: info.InputLen, outLen: info.OutputLen}, nil
}

func (d *Device) Close() error { return windows.CloseHandle(d.h) }

func (d *Device) io(write bool, buf []byte, timeout time.Duration) (int, error) {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(ev)
	ov := windows.Overlapped{HEvent: ev}
	var n uint32
	if write {
		err = windows.WriteFile(d.h, buf, &n, &ov)
	} else {
		err = windows.ReadFile(d.h, buf, &n, &ov)
	}
	if err != nil && err != windows.ERROR_IO_PENDING {
		return 0, err
	}
	ms := uint32(timeout / time.Millisecond)
	if ev, _ := windows.WaitForSingleObject(ev, ms); ev != windows.WAIT_OBJECT_0 {
		windows.CancelIoEx(d.h, &ov)
		windows.GetOverlappedResult(d.h, &ov, &n, true)
		return 0, errors.New("hid: timeout")
	}
	if err := windows.GetOverlappedResult(d.h, &ov, &n, false); err != nil {
		return 0, err
	}
	return int(n), nil
}

// Write sends one report. data excludes the report ID (0 is prepended) and
// is zero-padded to the report size.
func (d *Device) Write(data []byte) error {
	buf := make([]byte, d.outLen)
	copy(buf[1:], data)
	_, err := d.io(true, buf, time.Second)
	return err
}

// Read receives one report without the leading report ID.
func (d *Device) Read(timeout time.Duration) ([]byte, error) {
	buf := make([]byte, d.inLen)
	n, err := d.io(false, buf, timeout)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		return buf[1:n], nil
	}
	return nil, nil
}

// Request writes a report and waits for the reply.
func (d *Device) Request(data []byte) ([]byte, error) {
	if err := d.Write(data); err != nil {
		return nil, err
	}
	return d.Read(time.Second)
}
