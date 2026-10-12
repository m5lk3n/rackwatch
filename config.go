package main

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/BurntSushi/toml"
)

// sourceLocal is the metrics_source value that reads /proc and /sys on this machine.
const sourceLocal = "local"

type config struct {
	Dashboard  dashboardConfig  `toml:"dashboard"`
	Thresholds thresholdsConfig `toml:"thresholds"`
	Hosts      []hostConfig     `toml:"hosts"`
}

type dashboardConfig struct {
	RefreshSeconds int      `toml:"refresh_seconds"`
	StaleAfter     duration `toml:"stale_after"`
	Theme          string   `toml:"theme"`
	ShowSparklines bool     `toml:"show_sparklines"`
}

type thresholdsConfig struct {
	CPUWarning         float64 `toml:"cpu_warning"`
	MemoryWarning      float64 `toml:"memory_warning"`
	DiskWarning        float64 `toml:"disk_warning"`
	TemperatureWarning float64 `toml:"temperature_warning"`
}

type hostConfig struct {
	Name          string `toml:"name"`
	MetricsSource string `toml:"metrics_source"`
}

// duration lets stale_after be written as a Go duration string like "15s".
type duration struct{ time.Duration }

func (d *duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func defaultConfig() config {
	return config{
		Dashboard: dashboardConfig{
			RefreshSeconds: 2,
			StaleAfter:     duration{15 * time.Second},
			Theme:          defaultTheme,
			ShowSparklines: true,
		},
		Thresholds: thresholdsConfig{
			CPUWarning:         80,
			MemoryWarning:      85,
			DiskWarning:        90,
			TemperatureWarning: 75,
		},
	}
}

func loadConfig(path string) (config, error) {
	cfg := defaultConfig()
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		var perr toml.ParseError
		if errors.As(err, &perr) {
			return cfg, fmt.Errorf("%s:\n%s", path, perr.ErrorWithPosition())
		}
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return cfg, fmt.Errorf("%s: unknown keys %v", path, undecoded)
	}
	return cfg, cfg.validate()
}

func (c config) refresh() time.Duration {
	return time.Duration(c.Dashboard.RefreshSeconds) * time.Second
}

func (c config) validate() error {
	if c.Dashboard.RefreshSeconds < 1 {
		return errors.New("dashboard.refresh_seconds must be at least 1")
	}
	if c.Dashboard.StaleAfter.Duration < c.refresh() {
		return errors.New("dashboard.stale_after must not be shorter than refresh_seconds")
	}
	if _, ok := themes[c.Dashboard.Theme]; !ok {
		return fmt.Errorf("dashboard.theme: unknown theme %q (available: %s)", c.Dashboard.Theme, themeNames())
	}
	if len(c.Hosts) == 0 {
		return errors.New("no [[hosts]] configured")
	}
	for i, h := range c.Hosts {
		if h.Name == "" {
			return fmt.Errorf("hosts[%d]: name is required", i)
		}
		if h.MetricsSource == sourceLocal {
			continue
		}
		u, err := url.Parse(h.MetricsSource)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("host %q: metrics_source must be %q or an http(s) URL, got %q",
				h.Name, sourceLocal, h.MetricsSource)
		}
	}
	return nil
}
