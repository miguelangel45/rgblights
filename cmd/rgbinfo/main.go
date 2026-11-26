// rgbinfo lists the RGB devices OpenRGB exposes and can flash them to check
// that software control works.
//
//	go run ./cmd/rgbinfo            # list devices, zones and LED counts
//	go run ./cmd/rgbinfo -flash     # cycle red/green/blue/white on every device
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"rgblights/openrgb"
)

var deviceTypes = []string{"Placa base", "RAM", "GPU", "Cooler", "Tira LED", "Teclado", "Ratón",
	"Alfombrilla", "Auriculares", "Soporte auriculares", "Gamepad", "Luz", "Altavoz", "Virtual",
	"Almacenamiento", "Caja", "Micrófono", "Accesorio", "Teclado numérico"}

func main() {
	addr := flag.String("server", "127.0.0.1:6742", "OpenRGB SDK server")
	flash := flag.Bool("flash", false, "cycle solid colors on every device")
	hold := flag.Duration("hold", 2*time.Second, "how long each flash color stays")
	resize := flag.String("resize", "", "resize a zone: device:zone:leds, e.g. 0:1:60")
	flag.Parse()

	c, err := openrgb.Dial(*addr, "rgbinfo", 2*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo conectar a OpenRGB en %s: %v\n", *addr, err)
		os.Exit(1)
	}
	defer c.Close()
	if *resize != "" {
		var dev, zone, n int
		if _, err := fmt.Sscanf(*resize, "%d:%d:%d", &dev, &zone, &n); err != nil {
			fmt.Fprintln(os.Stderr, "formato: -resize dispositivo:zona:leds")
			os.Exit(2)
		}
		if err := c.ResizeZone(dev, zone, n); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		time.Sleep(300 * time.Millisecond)
	}
	ctrls, err := c.Controllers()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(ctrls) == 0 {
		fmt.Println("OpenRGB no detecta ningún dispositivo.")
	}
	for _, d := range ctrls {
		typ := "?"
		if int(d.Type) >= 0 && int(d.Type) < len(deviceTypes) {
			typ = deviceTypes[d.Type]
		}
		mode := "?"
		if d.Active >= 0 && d.Active < len(d.Modes) {
			mode = d.Modes[d.Active]
		}
		fmt.Printf("[%d] %s (%s) — %d LEDs, modo actual: %s\n", d.Index, d.Name, typ, d.NumLEDs, mode)
		fmt.Printf("     modos: %v\n", d.Modes)
		for zi, z := range d.Zones {
			extra := ""
			if z.MinLEDs != z.MaxLEDs {
				extra = fmt.Sprintf("  (redimensionable %d–%d)", z.MinLEDs, z.MaxLEDs)
				if z.NumLEDs == 0 {
					extra += "  ⚠ 0 LEDs: ajusta el tamaño en OpenRGB (Resize)"
				}
			}
			fmt.Printf("     zona %d %-28q %3d LEDs%s\n", zi, z.Name, z.NumLEDs, extra)
		}
	}

	if !*flash {
		return
	}
	fmt.Println("\nDestello de prueba: rojo, verde, azul, blanco …")
	for _, d := range ctrls {
		if d.NumLEDs > 0 {
			c.SetCustomMode(d.Index)
		}
	}
	for _, col := range []openrgb.Color{{R: 255}, {G: 255}, {B: 255}, {R: 255, G: 255, B: 255}} {
		fmt.Printf("  %v\n", col)
		for _, d := range ctrls {
			if d.NumLEDs == 0 {
				continue
			}
			leds := make([]openrgb.Color, d.NumLEDs)
			for i := range leds {
				leds[i] = col
			}
			if err := c.UpdateLEDs(d.Index, leds); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		time.Sleep(*hold)
	}
}
