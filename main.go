// rgblights mirrors the colors of a screen region onto your PC's RGB
// lighting through OpenRGB's SDK server.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	appName = "RGB Lights"
	runKey  = `Software\Microsoft\Windows\CurrentVersion\Run`
	runName = "rgblights"
)

func main() {
	enableDPIAwareness()

	// Single instance.
	name, _ := windows.UTF16PtrFromString("Local\\rgblights-single-instance")
	if _, err := windows.CreateMutex(nil, false, name); err == windows.ERROR_ALREADY_EXISTS {
		return
	}

	if err := os.MkdirAll(configDir(), 0o755); err == nil {
		if f, err := os.OpenFile(filepath.Join(configDir(), "rgblights.log"),
			os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
			log.SetOutput(f)
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Printf("config: %v (usando valores por defecto)", err)
	}
	app := &trayApp{}
	app.engine = NewEngine(cfg, app.setStatus)
	systray.Run(app.onReady, func() {})
}

type trayApp struct {
	engine *Engine
	cancel context.CancelFunc

	iconOn, iconOff []byte

	mStatus, mPause, mVivid, mSpread, mAutostart *systray.MenuItem
	mKeyPerKey                                   *systray.MenuItem
	mDevices                                     *systray.MenuItem
	slots                                        []*deviceSlot
	slotsMu                                      sync.Mutex
	brightness                                   map[float64]*systray.MenuItem
	smoothing                                    map[int]*systray.MenuItem

	selecting sync.Mutex
}

func (a *trayApp) onReady() {
	a.iconOn, a.iconOff = makeIcon(false), makeIcon(true)
	systray.SetTitle(appName)
	systray.SetTooltip(appName)

	a.mStatus = systray.AddMenuItem("Buscando OpenRGB…", "")
	a.mStatus.Disable()
	mPreview := systray.AddMenuItem("Ver colores y dispositivos…", "Vista en vivo de lo que se envía a tus luces")
	systray.SetOnTapped(func() { ShowPreview(a.engine) }) // left click on the icon
	mTest := systray.AddMenuItem("Probar luces (rojo → verde → azul → blanco)", "Comprueba qué LEDs responden")
	systray.AddSeparator()

	mSelect := systray.AddMenuItem("Seleccionar región general…", "Arrastra un rectángulo sobre la pantalla")
	mFull := systray.AddMenuItem("Usar pantalla principal completa", "")
	systray.AddSeparator()

	a.mPause = systray.AddMenuItemCheckbox("Pausar", "", false)
	a.mVivid = systray.AddMenuItemCheckbox("Colores vivos", "Da más peso a los píxeles con color que a los grises", false)
	a.mSpread = systray.AddMenuItemCheckbox("Degradado por LED (izquierda → derecha)",
		"Reparte la región a lo largo de los LEDs de cada dispositivo", false)
	a.mKeyPerKey = systray.AddMenuItemCheckbox("Teclado Vial: color por tecla (en teclados partidos solo se ve una mitad)",
		"Con degradado: cada tecla su color. Apagado: un color en todo el teclado, en ambas mitades", false)

	mBright := systray.AddMenuItem("Brillo", "")
	a.brightness = map[float64]*systray.MenuItem{}
	for _, b := range []float64{0.25, 0.5, 0.75, 1} {
		item := mBright.AddSubMenuItemCheckbox(fmt.Sprintf("%d%%", int(b*100)), "", false)
		a.brightness[b] = item
		go a.onClick(item, func() { a.engine.Update(func(c *Config) { c.Brightness = b }) })
	}

	mSmooth := systray.AddMenuItem("Suavizado", "")
	a.smoothing = map[int]*systray.MenuItem{}
	for _, s := range []struct {
		label string
		ms    int
	}{{"Instantáneo", 0}, {"Rápido", 60}, {"Normal", 120}, {"Lento", 300}} {
		item := mSmooth.AddSubMenuItemCheckbox(s.label, "", false)
		a.smoothing[s.ms] = item
		go a.onClick(item, func() { a.engine.Update(func(c *Config) { c.SmoothingMs = s.ms }) })
	}
	systray.AddSeparator()

	a.buildDeviceMenu()
	mReconnect := systray.AddMenuItem("Reconectar a OpenRGB", "Vuelve a detectar dispositivos")
	mConfig := systray.AddMenuItem("Abrir archivo de configuración", "")
	mReload := systray.AddMenuItem("Recargar configuración", "")
	a.mAutostart = systray.AddMenuItemCheckbox("Iniciar con Windows", "", autostartEnabled())
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Salir", "")

	go a.onClick(mPreview, func() { ShowPreview(a.engine) })
	go a.onClick(mSelect, a.selectRegion)
	go a.onClick(mTest, a.engine.StartTest)
	go a.onClick(mFull, func() { a.engine.Update(func(c *Config) { c.Region = nil }) })
	go a.onClick(a.mPause, func() { a.engine.Update(func(c *Config) { c.Paused = !c.Paused }) })
	go a.onClick(a.mVivid, func() { a.engine.Update(func(c *Config) { c.Vivid = !c.Vivid }) })
	go a.onClick(a.mSpread, func() { a.engine.Update(func(c *Config) { c.Spread = !c.Spread }) })
	go a.onClick(a.mKeyPerKey, func() { a.engine.Update(func(c *Config) { c.KeyboardPerKey = !c.KeyboardPerKey }) })
	go a.onClick(mReconnect, a.engine.Reconnect)
	go a.onClick(mConfig, func() { openFile(configPath()) })
	go a.onClick(mReload, a.reloadConfig)
	go a.onClick(a.mAutostart, a.toggleAutostart)
	go func() {
		<-mQuit.ClickedCh
		a.cancel()
		systray.Quit()
	}()

	a.syncMenu()
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	go a.engine.Run(ctx)

	for _, arg := range os.Args[1:] {
		if arg == "-preview" {
			ShowPreview(a.engine)
		}
	}

	// First run: no region chosen yet, offer the selector straight away.
	if a.engine.Config().Region == nil {
		if _, err := os.Stat(filepath.Join(configDir(), ".welcomed")); err != nil {
			os.WriteFile(filepath.Join(configDir(), ".welcomed"), nil, 0o644)
			go a.selectRegion()
		}
	}
}

// onClick runs f for every click of item and refreshes the menu checkmarks.
func (a *trayApp) onClick(item *systray.MenuItem, f func()) {
	for range item.ClickedCh {
		f()
		a.syncMenu()
	}
}

func setChecked(item *systray.MenuItem, on bool) {
	if on {
		item.Check()
	} else {
		item.Uncheck()
	}
}

// syncMenu makes the menu reflect the current configuration.
func (a *trayApp) syncMenu() {
	c := a.engine.Config()
	setChecked(a.mPause, c.Paused)
	setChecked(a.mVivid, c.Vivid)
	setChecked(a.mSpread, c.Spread)
	setChecked(a.mKeyPerKey, c.KeyboardPerKey)
	for b, item := range a.brightness {
		setChecked(item, b == c.Brightness)
	}
	for ms, item := range a.smoothing {
		setChecked(item, ms == c.SmoothingMs)
	}
	if c.Paused {
		systray.SetIcon(a.iconOff)
	} else {
		systray.SetIcon(a.iconOn)
	}
}

func (a *trayApp) setStatus(s Status) {
	var text string
	switch {
	case s.Connected && len(s.Devices) == 0:
		text = "OpenRGB conectado · sin dispositivos"
	case s.Connected:
		text = fmt.Sprintf("OpenRGB conectado · %d dispositivo(s)", len(s.Devices))
	default:
		text = "Sin conexión a OpenRGB (reintentando…)"
	}
	a.mStatus.SetTitle(text)
	tip := appName + "\n" + text
	if !s.Connected && s.Err != "" {
		tip = appName + "\n" + s.Err
	}
	if len(tip) > 120 {
		tip = tip[:120]
	}
	systray.SetTooltip(tip)
}

// deviceSlot is one entry of the "Región por dispositivo" submenu.
type deviceSlot struct {
	item, choose, reset *systray.MenuItem
	color, follow       *systray.MenuItem
	name                string // device currently shown in this slot
}

const maxDeviceSlots = 24

func (a *trayApp) buildDeviceMenu() {
	a.mDevices = systray.AddMenuItem("Dispositivos", "Región o color fijo de cada dispositivo")
	for i := 0; i < maxDeviceSlots; i++ {
		s := &deviceSlot{item: a.mDevices.AddSubMenuItem("", "")}
		s.choose = s.item.AddSubMenuItem("Elegir región…", "")
		s.reset = s.item.AddSubMenuItem("Usar la región general", "")
		s.color = s.item.AddSubMenuItem("Color fijo…", "Un color sólido en lugar de seguir la pantalla")
		s.follow = s.item.AddSubMenuItem("Seguir la pantalla", "Quita el color fijo")
		s.item.Hide()
		a.slots = append(a.slots, s)
		go a.onClick(s.color, func() {
			if name := a.slotName(s); name != "" {
				a.pickColorFor(name)
			}
		})
		go a.onClick(s.follow, func() {
			if name := a.slotName(s); name != "" {
				a.engine.Update(func(c *Config) { delete(c.DeviceColors, name) })
				a.refreshDeviceMenu()
			}
		})
		go a.onClick(s.choose, func() {
			if name := a.slotName(s); name != "" {
				a.selectRegionFor(name)
			}
		})
		go a.onClick(s.reset, func() {
			if name := a.slotName(s); name != "" {
				a.engine.Update(func(c *Config) { delete(c.DeviceRegions, name) })
			}
		})
	}
	// Devices come and go (OpenRGB reconnects, iCUE starts later): refresh.
	go func() {
		for {
			a.refreshDeviceMenu()
			time.Sleep(2 * time.Second)
		}
	}()
}

func (a *trayApp) slotName(s *deviceSlot) string {
	a.slotsMu.Lock()
	defer a.slotsMu.Unlock()
	return s.name
}

func (a *trayApp) refreshDeviceMenu() {
	snap := a.engine.Snapshot()
	cfg := a.engine.Config()
	a.slotsMu.Lock()
	defer a.slotsMu.Unlock()
	for i, s := range a.slots {
		if i >= len(snap.Devices) {
			if s.name != "" {
				s.item.Hide()
				s.name = ""
			}
			continue
		}
		name := snap.Devices[i].Name
		title := name
		_, fixed := cfg.fixedColor(name)
		if r, ok := cfg.DeviceRegions[name]; ok {
			title = fmt.Sprintf("%s  — región propia %d×%d", name, r.W, r.H)
			s.reset.Enable()
		} else {
			title = name + "  — región general"
			s.reset.Disable()
		}
		if fixed {
			title = fmt.Sprintf("%s  — color fijo %s", name, cfg.DeviceColors[name])
			s.follow.Enable()
		} else {
			s.follow.Disable()
		}
		s.name = name
		s.item.SetTitle(title)
		s.item.Show()
	}
}

// pickColorFor pins a device to a color chosen in the Windows color dialog.
func (a *trayApp) pickColorFor(device string) {
	start, ok := a.engine.Config().fixedColor(device)
	if !ok {
		start = rgb{255, 255, 255}
	}
	c, ok := PickColor(start)
	if !ok {
		return
	}
	a.engine.Update(func(cfg *Config) {
		if cfg.DeviceColors == nil {
			cfg.DeviceColors = map[string]string{}
		}
		cfg.DeviceColors[device] = hexColor(c)
	})
	log.Printf("color fijo %q: %s", device, hexColor(c))
	a.refreshDeviceMenu()
}

func (a *trayApp) selectRegion() { a.selectRegionFor("") }

// selectRegionFor picks the general region (device "") or a device's own.
func (a *trayApp) selectRegionFor(device string) {
	if !a.selecting.TryLock() {
		return
	}
	defer a.selecting.Unlock()

	hint := "Arrastra para elegir la región cuyos colores irán a tus luces"
	if device != "" {
		hint = "Región para: " + device
	}
	// Stop sampling while the dark overlay is on screen.
	a.engine.Hold(true)
	defer a.engine.Hold(false)
	r, ok := SelectRegion(hint + "\nEsc o clic derecho para cancelar")
	if !ok {
		return
	}
	a.engine.Update(func(c *Config) {
		if device == "" {
			c.Region = regionOf(r)
			return
		}
		if c.DeviceRegions == nil {
			c.DeviceRegions = map[string]Region{}
		}
		c.DeviceRegions[device] = *regionOf(r)
	})
	log.Printf("región %q: %v", device, r)
	a.refreshDeviceMenu()
	ShowPreview(a.engine) // show the result right away
}

func (a *trayApp) reloadConfig() {
	c, err := loadConfig()
	if err != nil {
		log.Printf("config: %v", err)
		systray.SetTooltip(appName + "\nError en config.json: " + err.Error())
		return
	}
	old := a.engine.Config()
	a.engine.Update(func(cur *Config) { *cur = c })
	if old.Server != c.Server || strings.Join(old.Devices, "\x00") != strings.Join(c.Devices, "\x00") {
		a.engine.Reconnect()
	}
}

func openFile(path string) {
	verb, _ := windows.UTF16PtrFromString("open")
	p, _ := windows.UTF16PtrFromString(path)
	if err := windows.ShellExecute(0, verb, p, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		// No app associated with .json: fall back to Notepad.
		np, _ := windows.UTF16PtrFromString("notepad.exe")
		windows.ShellExecute(0, verb, np, p, nil, windows.SW_SHOWNORMAL)
	}
}

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runName)
	return err == nil
}

func (a *trayApp) toggleAutostart() {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		log.Printf("autostart: %v", err)
		return
	}
	defer k.Close()
	if autostartEnabled() {
		err = k.DeleteValue(runName)
	} else {
		var exe string
		if exe, err = os.Executable(); err == nil {
			err = k.SetStringValue(runName, `"`+exe+`"`)
		}
	}
	if err != nil {
		log.Printf("autostart: %v", err)
	}
	setChecked(a.mAutostart, autostartEnabled())
}
