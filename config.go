package main

import (
	"encoding/json"
	"errors"
	"image"
	"io/fs"
	"os"
	"path/filepath"
)

// Region is a screen rectangle in virtual-screen pixels.
type Region struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

func (r Region) Rect() image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H) }

func regionOf(r image.Rectangle) *Region {
	return &Region{X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy()}
}

// Config is persisted as JSON in %APPDATA%\rgblights\config.json.
type Config struct {
	// Server is the OpenRGB SDK server address.
	Server string `json:"server"`
	// Region to sample; null means the whole primary monitor.
	Region *Region `json:"region"`
	// DeviceRegions gives a device its own region, keyed by the device name
	// shown in the app. Devices not listed use Region.
	DeviceRegions map[string]Region `json:"device_regions,omitempty"`
	// FPS is how many times per second the screen is sampled.
	FPS int `json:"fps"`
	// SmoothingMs is the fade time constant; 0 = instant changes.
	SmoothingMs int `json:"smoothing_ms"`
	// Brightness scales the output, 0..1.
	Brightness float64 `json:"brightness"`
	// Saturation boosts colorfulness; 1 = unchanged.
	Saturation float64 `json:"saturation"`
	// Vivid weights colorful pixels more than grey/dark ones.
	Vivid bool `json:"vivid"`
	// Spread maps the region left-to-right across each device's LEDs
	// instead of painting every LED the same color.
	Spread bool `json:"spread"`
	// Devices limits output to controllers whose name contains one of these
	// strings (case-insensitive). Empty = all devices.
	Devices []string `json:"devices"`
	// ZoneSizes sets the LED count of resizable zones by zone name, e.g.
	// {"Addressable RGB Header 1": 24}. Zones left at 0 LEDs get 60.
	ZoneSizes map[string]int `json:"zone_sizes,omitempty"`
	// DisableCorsair turns off control of Corsair devices through iCUE's SDK.
	DisableCorsair bool `json:"disable_corsair"`
	// KeyboardPerKey sends a color per key to Vial keyboards (with Spread).
	// Split keyboards then only light the half plugged into USB.
	KeyboardPerKey bool `json:"keyboard_per_key"`
	// DisableKeyboards turns off control of Vial (VialRGB) keyboards.
	DisableKeyboards bool `json:"disable_keyboards"`
	// Paused stops updating the lights.
	Paused bool `json:"paused"`
}

func defaultConfig() Config {
	return Config{
		Server:      "127.0.0.1:6742",
		FPS:         30,
		SmoothingMs: 120,
		Brightness:  1,
		Saturation:  1.3,
		Vivid:       true,
	}
}

// normalize fills in missing or out-of-range values.
func (c *Config) normalize() {
	d := defaultConfig()
	if c.Server == "" {
		c.Server = d.Server
	}
	if c.FPS <= 0 || c.FPS > 120 {
		c.FPS = d.FPS
	}
	if c.SmoothingMs < 0 {
		c.SmoothingMs = 0
	}
	if c.Brightness <= 0 || c.Brightness > 1 {
		c.Brightness = 1
	}
	if c.Saturation <= 0 || c.Saturation > 3 {
		c.Saturation = d.Saturation
	}
	if c.Region != nil && (c.Region.W < 1 || c.Region.H < 1) {
		c.Region = nil
	}
}

func configDir() string {
	if d := os.Getenv("RGBLIGHTS_CONFIG_DIR"); d != "" {
		return d
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "rgblights")
}

func configPath() string { return filepath.Join(configDir(), "config.json") }

func loadConfig() (Config, error) {
	c := defaultConfig()
	b, err := os.ReadFile(configPath())
	if errors.Is(err, fs.ErrNotExist) {
		return c, saveConfig(c)
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return defaultConfig(), err
	}
	c.normalize()
	return c, nil
}

func saveConfig(c Config) error {
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := configPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, configPath())
}
