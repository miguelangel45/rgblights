// vialprobe checks whether a Vial/QMK keyboard exposes VialRGB lighting.
//
//	go run ./cmd/vialprobe [-vid 6401 -pid 45d4]
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"strconv"

	"rgblights/hid"
)

func main() {
	vidS := flag.String("vid", "6401", "vendor id (hex)")
	pidS := flag.String("pid", "45d4", "product id (hex)")
	flag.Parse()
	vid, _ := strconv.ParseUint(*vidS, 16, 16)
	pid, _ := strconv.ParseUint(*pidS, 16, 16)

	infos, err := hid.Find(uint16(vid), uint16(pid))
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	var raw *hid.Info
	for i, in := range infos {
		fmt.Printf("interfaz: %q page=0x%04X usage=0x%02X in=%d out=%d\n", in.Product, in.UsagePage, in.Usage, in.InputLen, in.OutputLen)
		if in.UsagePage == 0xFF60 && in.Usage == 0x61 {
			raw = &infos[i]
		}
	}
	if raw == nil {
		fmt.Println("No hay interfaz raw HID de VIA/Vial (0xFF60).")
		os.Exit(1)
	}
	d, err := hid.Open(*raw)
	if err != nil {
		fmt.Println("abrir:", err)
		os.Exit(1)
	}
	defer d.Close()

	ask := func(label string, req ...byte) []byte {
		r, err := d.Request(req)
		if err != nil {
			fmt.Printf("%s: %v\n", label, err)
			return nil
		}
		fmt.Printf("%s: % X\n", label, r[:min(len(r), 16)])
		return r
	}
	ask("VIA versión de protocolo", 0x01)
	info := ask("VialRGB info (0x40)", 0x08, 0x40)
	if info != nil && info[0] == 0x08 {
		fmt.Printf("  → versión VialRGB %d, brillo máx %d\n", binary.LittleEndian.Uint16(info[2:]), info[4])
	}
	mode := ask("VialRGB modo (0x41)", 0x08, 0x41)
	if mode != nil && mode[0] == 0x08 {
		fmt.Printf("  → efecto %d, velocidad %d, HSV(%d,%d,%d)\n", binary.LittleEndian.Uint16(mode[2:]), mode[4], mode[5], mode[6], mode[7])
	}
	n := ask("VialRGB nº LEDs (0x43)", 0x08, 0x43)
	if n != nil && n[0] == 0x08 {
		fmt.Printf("  → %d LEDs\n", binary.LittleEndian.Uint16(n[2:]))
	}
	sup := ask("VialRGB efectos soportados (0x42)", 0x08, 0x42, 0, 0)
	if sup != nil && sup[0] == 0x08 {
		fmt.Print("  → efectos:")
		for i := 2; i+1 < len(sup); i += 2 {
			v := binary.LittleEndian.Uint16(sup[i:])
			if v == 0xFFFF {
				break
			}
			fmt.Print(" ", v)
		}
		fmt.Println("  (1 = Direct)")
	}
}
