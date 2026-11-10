package main

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// sourceLocal is the metrics_source value that reads /proc and /sys on this machine.
const sourceLocal = "local"

// maxHosts is how many [[hosts]] a config may list: two rows of five panels.
const maxHosts = 10

type config struct {
	Dashboard   dashboardConfig   `toml:"dashboard"`
	Screensaver screensaverConfig `toml:"screensaver"`
	Blackout    blackoutConfig    `toml:"blackout"`
	Thresholds  thresholdsConfig  `toml:"thresholds"`
	Telegram    telegramConfig    `toml:"telegram"`
	Ntfy        ntfyConfig        `toml:"ntfy"`
	Touch       touchConfig       `toml:"touch"`
	Log         logConfig         `toml:"log"`
	Hosts       []hostConfig      `toml:"hosts"`
}

type dashboardConfig struct {
	RefreshSeconds int      `toml:"refresh_seconds"`
	StaleAfter     duration `toml:"stale_after"`
	DownAfter      duration `toml:"down_after"`
	Theme          string   `toml:"theme"`
	ShowSparklines bool     `toml:"show_sparklines"`
	SparkGlyphs    string   `toml:"spark_glyphs"`
	Condensed      bool     `toml:"condensed"`
	MarginTop      int      `toml:"margin_top"`
	MarginBottom   int      `toml:"margin_bottom"`
	MarginLeft     int      `toml:"margin_left"`
	MarginRight    int      `toml:"margin_right"`
}

type screensaverConfig struct {
	Enabled  bool     `toml:"enabled"`
	Interval duration `toml:"interval"` // how often it starts
	Duration duration `toml:"duration"` // how long it runs
}

type blackoutConfig struct {
	Enabled bool      `toml:"enabled"`
	From    clockTime `toml:"from"`  // the screen is black from this time...
	Until   clockTime `toml:"until"` // ...until this one
}

type telegramConfig struct {
	Enabled    bool      `toml:"enabled"`
	BotToken   string    `toml:"bot_token"`
	ChatID     chatID    `toml:"chat_id"`
	QuietFrom  clockTime `toml:"quiet_from"`  // no messages from this time...
	QuietUntil clockTime `toml:"quiet_until"` // ...until this one
}

type ntfyConfig struct {
	Enabled    bool      `toml:"enabled"`
	Server     string    `toml:"server"`   // https://ntfy.sh or a self-hosted server
	Topic      string    `toml:"topic"`    // the topic to publish to
	Token      string    `toml:"token"`    // an access token, for a server with access control...
	Username   string    `toml:"username"` // ...or a user and password
	Password   string    `toml:"password"`
	Priority   int       `toml:"priority"`    // 1 (min) to 5 (max); 0 leaves it to the server (3)
	QuietFrom  clockTime `toml:"quiet_from"`  // no messages from this time...
	QuietUntil clockTime `toml:"quiet_until"` // ...until this one
}

// ntfyTopic is what ntfy accepts as a topic name.
var ntfyTopic = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)

type touchConfig struct {
	Enabled bool   `toml:"enabled"`
	Device  string `toml:"device"`   // the touchscreen's /dev/input/event* device
	SwapXY  bool   `toml:"swap_xy"`  // the touchscreen is turned a quarter against the display
	InvertX bool   `toml:"invert_x"` // left and right are the wrong way round
	InvertY bool   `toml:"invert_y"` // top and bottom are the wrong way round
}

type logConfig struct {
	Debug bool `toml:"debug"` // also log each touch and tap
}

// chatID accepts a numeric chat ID as well as a "@channel" name.
type chatID string

func (c *chatID) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case int64:
		*c = chatID(strconv.FormatInt(v, 10))
	case string:
		*c = chatID(v)
	default:
		return fmt.Errorf("chat_id must be a number or a string, got %T", v)
	}
	return nil
}

// clockTime is a time of day in minutes after midnight, written as "23:00".
type clockTime int

func (c *clockTime) UnmarshalText(b []byte) error {
	t, err := time.Parse("15:04", string(b))
	if err != nil {
		return fmt.Errorf("want a time of day like \"23:00\", got %q", b)
	}
	*c = clockTime(t.Hour()*60 + t.Minute())
	return nil
}

// within reports whether now, in the local time of this machine, falls between
// from and until. The period may wrap past midnight; equal ends make it empty.
func within(now time.Time, from, until clockTime) bool {
	m := clockTime(now.Hour()*60 + now.Minute())
	switch {
	case from == until:
		return false
	case from < until:
		return m >= from && m < until
	default:
		return m >= from || m < until
	}
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
	Enabled       *bool  `toml:"enabled"` // nil means enabled
}

func (h hostConfig) enabled() bool { return h.Enabled == nil || *h.Enabled }

// duration lets stale_after and down_after be written as a Go duration string like "15s".
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
			StaleAfter:     duration{300 * time.Second},
			DownAfter:      duration{12 * time.Hour},
			Theme:          defaultTheme,
			ShowSparklines: true,
			SparkGlyphs:    sparkGlyphsAuto,
		},
		Screensaver: screensaverConfig{
			Enabled:  true,
			Interval: duration{10 * time.Minute},
			Duration: duration{10 * time.Second},
		},
		Blackout: blackoutConfig{
			Enabled: true,
			From:    23 * 60,
			Until:   8 * 60,
		},
		Telegram: telegramConfig{
			QuietFrom:  23 * 60,
			QuietUntil: 8 * 60,
		},
		Ntfy: ntfyConfig{
			Server:     "https://ntfy.sh",
			QuietFrom:  23 * 60,
			QuietUntil: 8 * 60,
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
	if len(cfg.Hosts) == 0 {
		return cfg, errors.New("no [[hosts]] configured")
	}
	cfg.Hosts = enabledHosts(cfg.Hosts)
	if len(cfg.Hosts) == 0 {
		return cfg, errors.New("all [[hosts]] have enabled = false")
	}
	return cfg, cfg.validate()
}

// enabledHosts drops hosts with enabled = false, so they are neither
// validated nor counted against maxHosts.
func enabledHosts(hosts []hostConfig) []hostConfig {
	var out []hostConfig
	for _, h := range hosts {
		if h.enabled() {
			out = append(out, h)
		}
	}
	return out
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
	if c.Dashboard.DownAfter.Duration < c.Dashboard.StaleAfter.Duration {
		return errors.New("dashboard.down_after must not be shorter than stale_after")
	}
	if d := c.Dashboard; d.MarginTop < 0 || d.MarginBottom < 0 || d.MarginLeft < 0 || d.MarginRight < 0 {
		return errors.New("dashboard.margin_* must not be negative")
	}
	if _, ok := sparkGlyphSets[c.Dashboard.SparkGlyphs]; !ok && c.Dashboard.SparkGlyphs != sparkGlyphsAuto {
		return fmt.Errorf("dashboard.spark_glyphs: must be %q, %q, %q or %q, got %q",
			sparkGlyphsAuto, sparkGlyphsEighths, sparkGlyphsHalves, sparkGlyphsLine, c.Dashboard.SparkGlyphs)
	}
	if s := c.Screensaver; s.Enabled && (s.Duration.Duration <= 0 || s.Interval.Duration <= s.Duration.Duration) {
		return errors.New("screensaver.duration must be positive and shorter than screensaver.interval")
	}
	if t := c.Telegram; t.Enabled && (t.BotToken == "" || !strings.Contains(t.BotToken, ":")) {
		return errors.New("telegram.bot_token must be set to the token from @BotFather, like \"123456:ABC-DEF...\"")
	}
	if t := c.Telegram; t.Enabled && t.ChatID == "" {
		return errors.New("telegram.chat_id must be set")
	}
	if n := c.Ntfy; n.Enabled {
		if err := n.validate(); err != nil {
			return fmt.Errorf("ntfy.%w", err)
		}
	}
	if t := c.Touch; t.Enabled && t.Device == "" {
		return errors.New("touch.device must be set, like \"/dev/input/by-id/usb-...-event-if00\"")
	}
	if _, ok := themes[c.Dashboard.Theme]; !ok {
		return fmt.Errorf("dashboard.theme: unknown theme %q (available: %s)", c.Dashboard.Theme, themeNames())
	}
	if len(c.Hosts) == 0 {
		return errors.New("no [[hosts]] configured")
	}
	if len(c.Hosts) > maxHosts {
		return fmt.Errorf("%d [[hosts]] configured, at most %d allowed", len(c.Hosts), maxHosts)
	}
	seen := make(map[string]bool, len(c.Hosts))
	for i, h := range c.Hosts {
		if h.Name == "" {
			return fmt.Errorf("hosts[%d]: name is required", i)
		}
		if seen[h.Name] {
			return fmt.Errorf("host %q: configured more than once", h.Name)
		}
		seen[h.Name] = true
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

func (n ntfyConfig) validate() error {
	u, err := url.Parse(n.Server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("server must be an http(s) URL like \"https://ntfy.sh\", got %q", n.Server)
	}
	if !ntfyTopic.MatchString(n.Topic) {
		return fmt.Errorf("topic must be 1 to 64 letters, digits, - or _, got %q", n.Topic)
	}
	if n.Token != "" && n.Username != "" {
		return errors.New("token and username can't both be set")
	}
	if n.Password != "" && n.Username == "" {
		return errors.New("username must be set with password")
	}
	if n.Priority < 0 || n.Priority > 5 {
		return fmt.Errorf("priority must be 1 (min) to 5 (max), or 0 for the server's default, got %d", n.Priority)
	}
	return nil
}
