package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	ui "github.com/metaspartan/gotui"
)

const nodeExporterSample = `# HELP node_cpu_seconds_total Seconds the CPUs spent in each mode.
# TYPE node_cpu_seconds_total counter
node_cpu_seconds_total{cpu="0",mode="idle"} 900
node_cpu_seconds_total{cpu="0",mode="iowait"} 10
node_cpu_seconds_total{cpu="0",mode="user"} 90
node_memory_MemTotal_bytes 1.6e+10
node_memory_MemAvailable_bytes 4e+09
node_filesystem_size_bytes{device="/dev/sda1",fstype="ext4",mountpoint="/"} 1000
node_filesystem_free_bytes{device="/dev/sda1",fstype="ext4",mountpoint="/"} 300
node_filesystem_avail_bytes{device="/dev/sda1",fstype="ext4",mountpoint="/"} 200
node_filesystem_size_bytes{device="tmpfs",fstype="tmpfs",mountpoint="/run"} 5
node_hwmon_temp_celsius{chip="platform_coretemp_0",sensor="temp1"} 48
node_hwmon_temp_celsius{chip="platform_coretemp_0",sensor="temp2"} 61.5
node_network_receive_bytes_total{device="eth0"} 1000
node_network_transmit_bytes_total{device="eth0"} 500
node_network_receive_bytes_total{device="lo"} 99999
node_network_receive_bytes_total{device="veth1a2b"} 99999
weird_metric{path="C:\\dir",msg="say \"hi\""} 1
`

func TestParseNodeExporter(t *testing.T) {
	s, err := parseNodeExporter(strings.NewReader(nodeExporterSample))
	if err != nil {
		t.Fatal(err)
	}
	if s.cpuTotal != 1000 || s.cpuIdle != 910 {
		t.Errorf("cpu = %v/%v, want 910/1000", s.cpuIdle, s.cpuTotal)
	}
	if s.diskSize != 1000 || s.diskFree != 300 || s.diskAvail != 200 {
		t.Errorf("disk = %v/%v/%v", s.diskSize, s.diskFree, s.diskAvail)
	}
	if s.tempC != 61.5 {
		t.Errorf("temp = %v, want 61.5", s.tempC)
	}
	if s.netBytes != 1500 {
		t.Errorf("net = %v, want 1500 (lo and veth excluded)", s.netBytes)
	}
}

func TestParseNodeExporterRejectsOtherEndpoints(t *testing.T) {
	if _, err := parseNodeExporter(strings.NewReader("<html>nope</html>\n")); err == nil {
		t.Fatal("expected error for non-node_exporter body")
	}
}

func TestParsePromLabelsEscapes(t *testing.T) {
	_, labels, v, err := parsePromLine(`m{path="C:\\dir",msg="say \"hi\", ok"} 2 1700000000`)
	if err != nil {
		t.Fatal(err)
	}
	if labels["path"] != `C:\dir` || labels["msg"] != `say "hi", ok` || v != 2 {
		t.Errorf("got %q %v", labels, v)
	}
}

func TestDerive(t *testing.T) {
	t0 := time.Unix(0, 0)
	prev := rawSample{at: t0, cpuIdle: 900, cpuTotal: 1000, netBytes: 0}
	cur := rawSample{
		at: t0.Add(2 * time.Second), cpuIdle: 950, cpuTotal: 1100,
		memTotal: 100, memAvail: 25, diskSize: 1000, diskFree: 300, diskAvail: 200,
		tempC: 55, netBytes: 2048,
	}
	m := derive(&prev, cur)
	if m.cpu != 50 || m.mem != 75 || m.net != 1024 || m.temp != 55 {
		t.Errorf("got %+v", m)
	}
	if want := 100 * 700.0 / 900; math.Abs(m.disk-want) > 1e-9 {
		t.Errorf("disk = %v, want %v", m.disk, want)
	}

	// First sample and counter resets give no rate rather than garbage.
	if m := derive(nil, cur); !math.IsNaN(m.cpu) || !math.IsNaN(m.net) {
		t.Errorf("first sample: got %+v", m)
	}
	if m := derive(&cur, prev); !math.IsNaN(m.cpu) || !math.IsNaN(m.net) {
		t.Errorf("counter reset: got %+v", m)
	}
}

func TestRing(t *testing.T) {
	r := newRing(3)
	if got := r.last(5); len(got) != 0 {
		t.Fatalf("empty ring returned %v", got)
	}
	for i := 1; i <= 5; i++ {
		r.push(float64(i))
	}
	got := r.last(5)
	if len(got) != 3 || got[0] != 3 || got[2] != 5 {
		t.Errorf("last = %v, want [3 4 5]", got)
	}
	if got := r.last(2); got[0] != 4 || got[1] != 5 {
		t.Errorf("last(2) = %v, want [4 5]", got)
	}
}

func TestUnreachableHostKeepsLastSnapshot(t *testing.T) {
	up := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !up {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(nodeExporterSample))
	}))
	defer srv.Close()

	h := newHostState(hostConfig{Name: "rack-02", MetricsSource: srv.URL})
	pollOnce := func() {
		s, err := h.collect(context.Background())
		h.apply(pollResult{sample: s, err: err})
	}
	staleAfter := 15 * time.Second

	pollOnce()
	if st := h.status(time.Now(), staleAfter); st != statusUp {
		t.Fatalf("status = %v, want up", st)
	}
	up = false
	pollOnce()
	if h.lastErr == nil || h.current.mem != 75 {
		t.Fatalf("after failure: err=%v mem=%v, want error and kept snapshot", h.lastErr, h.current.mem)
	}
	if st := h.status(time.Now().Add(time.Minute), staleAfter); st != statusStale {
		t.Errorf("status = %v, want stale", st)
	}
	if got := h.history[metricMem].last(2); got[0] != 75 || !math.IsNaN(got[1]) {
		t.Errorf("history = %v, want [75 NaN]", got)
	}
}

func TestNeverReachableHostIsDown(t *testing.T) {
	h := newHostState(hostConfig{Name: "rack-03", MetricsSource: "http://127.0.0.1:1/metrics"})
	s, err := h.collect(context.Background())
	h.apply(pollResult{sample: s, err: err})
	if st := h.status(time.Now(), time.Minute); st != statusDown {
		t.Errorf("status = %v, want down", st)
	}
	if msg := shortErr(h.lastErr); strings.Contains(msg, "http://") {
		t.Errorf("shortErr kept the URL: %q", msg)
	}
	if shortErr(context.DeadlineExceeded) != "timeout" {
		t.Error("deadline should read as timeout")
	}
	if shortErr(errors.New("x")) != "x" {
		t.Error("plain errors should pass through")
	}
}

func TestConfigFile(t *testing.T) {
	cfg, err := loadConfig("config.toml.example")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dashboard.StaleAfter.Duration != 300*time.Second || cfg.Dashboard.MarginTop != 1 || cfg.Dashboard.MarginLeft != 5 || cfg.Dashboard.MarginRight != 4 || len(cfg.Hosts) != 3 {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestConfigHostLimit(t *testing.T) {
	cfg := defaultConfig()
	for i := range maxHosts + 1 {
		cfg.Hosts = append(cfg.Hosts, hostConfig{Name: fmt.Sprintf("h%d", i), MetricsSource: sourceLocal})
	}
	if err := cfg.validate(); err == nil {
		t.Errorf("%d hosts accepted, want error", len(cfg.Hosts))
	}
	cfg.Hosts = cfg.Hosts[:maxHosts]
	if err := cfg.validate(); err != nil {
		t.Errorf("%d hosts rejected: %v", len(cfg.Hosts), err)
	}
}

func TestConfigEnabled(t *testing.T) {
	f := false
	tr := true
	hosts := []hostConfig{
		{Name: "default"},
		{Name: "on", Enabled: &tr},
		{Name: "off", Enabled: &f},
	}
	got := enabledHosts(hosts)
	if len(got) != 2 || got[0].Name != "default" || got[1].Name != "on" {
		t.Errorf("enabledHosts = %+v, want default and on", got)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	const toml = `
[[hosts]]
name = "off"
metrics_source = "not a url"
enabled = false

[[hosts]]
name = "on"
metrics_source = "local"
enabled = true
`
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hosts) != 1 || cfg.Hosts[0].Name != "on" {
		t.Errorf("hosts = %+v, want only on", cfg.Hosts)
	}

	if err := os.WriteFile(path, []byte("[[hosts]]\nname = \"x\"\nmetrics_source = \"local\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Error("config with every host disabled accepted, want error")
	}
}

// TestRender draws the dashboard at roughly the rack display's size
// (1424×280 px ≈ 178×17 cells with an 8×16 font) and logs it; run with -v to see it.
func TestRender(t *testing.T) {
	cfg, err := loadConfig("config.toml.example")
	if err != nil {
		t.Fatal(err)
	}
	// rackbox-02's last good poll is 42s old; keep that STALE whatever the example sets.
	cfg.Dashboard.StaleAfter.Duration = 30 * time.Second
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	screen.SetSize(178, 17)
	ui.Screen = screen
	defer func() { screen.Fini(); ui.Screen = nil }()

	hosts := make([]*hostState, len(cfg.Hosts))
	for i, hc := range cfg.Hosts {
		hosts[i] = newHostState(hc)
	}
	now := time.Now()
	t0 := now.Add(-time.Minute)
	// rackbox-01: healthy with history; rackbox-02: went away a while ago; rackbox-03: never reachable.
	for i := 0; i < 40; i++ {
		at := t0.Add(time.Duration(i) * 2 * time.Second)
		hosts[0].apply(pollResult{sample: rawSample{
			at: at, cpuTotal: float64(i * 100), cpuIdle: float64(i*100) - 20 - 70*math.Abs(math.Sin(float64(i)/4)),
			memTotal: 100, memAvail: 39, diskSize: 100, diskAvail: 50, tempC: 50 + float64(i%10),
			netBytes: float64(i*i) * 1e6,
		}})
		if i < 10 {
			hosts[1].apply(pollResult{sample: rawSample{at: at, cpuTotal: float64(i * 100), memTotal: 100, memAvail: 10, tempC: math.NaN(), netBytes: math.NaN()}})
		} else {
			hosts[1].apply(pollResult{err: context.DeadlineExceeded})
		}
	}
	hosts[2].apply(pollResult{err: errors.New("unknown host")})

	d := newDashboard(cfg, hosts)
	d.SetRect(0, 0, 178, 17)
	d.now = now
	ui.Render(d)

	cells, w, h := screen.GetContents()
	var out strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if r := cells[y*w+x].Runes; len(r) > 0 {
				out.WriteRune(r[0])
			}
		}
		out.WriteByte('\n')
	}
	text := out.String()
	t.Log("\n" + text)
	d.cfg.Dashboard.Condensed = true
	ui.Render(d)
	cells, w, _ = screen.GetContents()
	var condensed strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if r := cells[y*w+x].Runes; len(r) > 0 {
				condensed.WriteRune(r[0])
			}
		}
		condensed.WriteByte('\n')
	}
	t.Log("condensed:\n" + condensed.String())
	if lines := strings.Split(condensed.String(), "\n"); !strings.Contains(lines[2], "CPU") || !strings.Contains(lines[6], "NET") || !strings.Contains(lines[7], "╰") {
		t.Errorf("condensed render should put CPU..NET on rows 2..6 and close the panel on row 7")
	}
	for _, want := range []string{"RACKWATCH", "1/3 UP", "rackbox-01", " UP ", "STALE", "DOWN", "unknown host", "timeout", "61%", "90%"} {
		if !strings.Contains(text, want) {
			t.Errorf("render is missing %q", want)
		}
	}
}

func TestCollectLocal(t *testing.T) {
	a, err := collectLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	b, err := collectLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := derive(&a, b)
	if m.mem <= 0 || m.mem > 100 || m.disk <= 0 || m.disk > 100 {
		t.Errorf("implausible local metrics: %+v", m)
	}
	t.Logf("local: %+v", m)
}

func TestThemes(t *testing.T) {
	for name, th := range themes {
		if th.base.Bg == th.value.Fg || th.base.Bg == th.warn.Fg {
			t.Errorf("theme %q: text color equals background", name)
		}
	}
	// Themes must survive the Linux console: 16 foreground and 8 background colors.
	for name, th := range themes {
		for _, st := range th.styles() {
			if st.Fg < tcell.ColorBlack || st.Fg > tcell.ColorWhite || st.Bg < tcell.ColorBlack || st.Bg > tcell.ColorSilver {
				t.Errorf("theme %q: style %+v uses colors the Linux console lacks", name, st)
			}
		}
	}
	// On the console, bright colors are drawn as their dim slot plus bold.
	con := themes[defaultTheme].forTerminal(true)
	for _, st := range con.styles() {
		if st.Fg > tcell.ColorSilver || st.Bg > tcell.ColorSilver {
			t.Errorf("console style %+v uses a color tcell can't send to TERM=linux", *st)
		}
	}
	if w := con.warn; w.Fg != tcell.ColorMaroon || w.Modifier&ui.ModifierBold == 0 {
		t.Errorf("console warn = %+v, want slot 1 + bold (slot 9)", w)
	}
	if st := themes[defaultTheme].forTerminal(false).value; !st.Fg.IsRGB() {
		t.Errorf("emulator value color %v should be RGB from the palette", st.Fg)
	}
	cfg := defaultConfig()
	cfg.Hosts = []hostConfig{{Name: "a", MetricsSource: sourceLocal}}
	cfg.Dashboard.Theme = "amiga"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "fortuna") {
		t.Errorf("unknown theme should list available themes, got %v", err)
	}
}

func TestSparkGlyphs(t *testing.T) {
	t.Setenv("TERM", "linux")
	if got := string(sparkGlyphs(sparkGlyphsAuto).glyphs); got != "_-¯" {
		t.Errorf("auto on the Linux console = %q, want line", got)
	}
	for dev, want := range map[string]bool{"/dev/tty1": true, "/dev/tty12": true, "/dev/console": true, "/dev/pts/3": false, "/dev/ttyS0": false} {
		if got := vtDevice.MatchString(dev); got != want {
			t.Errorf("vtDevice(%q) = %v, want %v", dev, got, want)
		}
	}

	// 70% of two rows in halves is 2.8 → 3 half-rows: a full block under a half block.
	buf := ui.NewBuffer(image.Rect(0, 0, 1, 2))
	drawSparkColumn(buf, sparkGlyphs(sparkGlyphsHalves), 0.7, 2, ui.StyleClear, image.Pt(0, 1))
	if top, bottom := buf.GetCell(image.Pt(0, 0)).Rune, buf.GetCell(image.Pt(0, 1)).Rune; top != '▄' || bottom != '█' {
		t.Errorf("column = %q over %q, want ▄ over █", top, bottom)
	}

	// The same 70% as a line is level 4 of 6: only the lowest mark in the top row.
	buf = ui.NewBuffer(image.Rect(0, 0, 1, 2))
	drawSparkColumn(buf, sparkGlyphs(sparkGlyphsLine), 0.7, 2, ui.StyleClear, image.Pt(0, 1))
	if top, bottom := buf.GetCell(image.Pt(0, 0)).Rune, buf.GetCell(image.Pt(0, 1)).Rune; top != '_' || bottom != ' ' {
		t.Errorf("column = %q over %q, want _ over empty", top, bottom)
	}
}

func TestScreensaverConfig(t *testing.T) {
	cfg := defaultConfig()
	cfg.Hosts = []hostConfig{{Name: "a", MetricsSource: sourceLocal}}
	if s := cfg.Screensaver; !s.Enabled || s.Interval.Duration != 10*time.Minute || s.Duration.Duration != 10*time.Second {
		t.Errorf("default screensaver = %+v, want enabled every 10m for 10s", s)
	}
	cfg.Screensaver.Duration.Duration = cfg.Screensaver.Interval.Duration
	if err := cfg.validate(); err == nil {
		t.Error("screensaver running as long as its interval accepted, want error")
	}
	cfg.Screensaver.Enabled = false
	if err := cfg.validate(); err != nil {
		t.Errorf("disabled screensaver validated: %v", err)
	}
}

// TestScreensaver animates the matrix rain at the rack display's size and logs
// the last frame; run with -v to see it.
func TestScreensaver(t *testing.T) {
	for _, console := range []bool{false, true} {
		m := newMatrixRain(console)
		m.SetRect(0, 0, 178, 17)
		for range 60 {
			m.step()
		}
		buf := ui.NewBuffer(m.GetRect())
		m.Draw(buf)

		var out strings.Builder
		heads, glyphs := 0, 0
		for y := 0; y < 17; y++ {
			for x := 0; x < 178; x++ {
				c := buf.GetCell(image.Pt(x, y))
				out.WriteRune(c.Rune)
				if c.Rune != ' ' {
					glyphs++
				}
				if c.Style == m.styles[0] {
					heads++
				}
				if console && (c.Style.Fg > tcell.ColorSilver || c.Style.Bg > tcell.ColorSilver) {
					t.Fatalf("console style %+v uses a color tcell can't send to TERM=linux", c.Style)
				}
				if !console && c.Rune != ' ' && !c.Style.Fg.IsRGB() {
					t.Fatalf("emulator style %+v should be RGB", c.Style)
				}
			}
			out.WriteByte('\n')
		}
		t.Logf("console=%v:\n%s", console, out.String())
		if heads == 0 || glyphs < 178*17/10 {
			t.Errorf("console=%v: %d heads and %d glyphs on screen, want rain", console, heads, glyphs)
		}
	}
}

func TestTelegramQuietHours(t *testing.T) {
	at := func(hh, mm int) time.Time { return time.Date(2026, 10, 13, hh, mm, 0, 0, time.Local) }
	cfg := defaultConfig().Telegram
	for _, c := range []struct {
		hh, mm int
		quiet  bool
	}{{22, 59, false}, {23, 0, true}, {3, 0, true}, {7, 59, true}, {8, 0, false}, {12, 0, false}} {
		if got := cfg.quiet(at(c.hh, c.mm)); got != c.quiet {
			t.Errorf("default quiet at %02d:%02d = %v, want %v", c.hh, c.mm, got, c.quiet)
		}
	}
	cfg.QuietFrom, cfg.QuietUntil = 9*60, 17*60
	if !cfg.quiet(at(12, 0)) || cfg.quiet(at(17, 0)) || cfg.quiet(at(8, 59)) {
		t.Error("09:00-17:00 quiet hours wrong")
	}
	cfg.QuietUntil = cfg.QuietFrom
	if cfg.quiet(at(9, 0)) {
		t.Error("equal quiet_from and quiet_until should disable quiet hours")
	}
}

func TestTelegramConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write := func(telegram string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(telegram+"\n[[hosts]]\nname = \"a\"\nmetrics_source = \"local\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("[telegram]\nenabled = true\nbot_token = \"123:abc\"\nchat_id = -1001234\nquiet_from = \"22:30\"\nquiet_until = \"06:15\"\n")
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if tg := cfg.Telegram; tg.ChatID != "-1001234" || tg.QuietFrom != 22*60+30 || tg.QuietUntil != 6*60+15 {
		t.Errorf("telegram = %+v", tg)
	}

	write("[telegram]\nenabled = true\nbot_token = \"123:abc\"\nchat_id = \"@rack\"\n")
	if cfg, err := loadConfig(path); err != nil || cfg.Telegram.ChatID != "@rack" || cfg.Telegram.QuietFrom != 23*60 {
		t.Errorf("string chat_id: %v, %+v", err, cfg.Telegram)
	}

	for _, bad := range []string{
		"[telegram]\nenabled = true\nchat_id = 1\n",
		"[telegram]\nenabled = true\nbot_token = \"123:abc\"\n",
		"[telegram]\nquiet_from = \"11pm\"\n",
	} {
		write(bad)
		if _, err := loadConfig(path); err == nil {
			t.Errorf("accepted %q, want error", bad)
		}
	}

	write("[telegram]\nenabled = false\n")
	if _, err := loadConfig(path); err != nil {
		t.Errorf("disabled telegram without token rejected: %v", err)
	}
}

func TestAlerter(t *testing.T) {
	hosts := []*hostState{
		newHostState(hostConfig{Name: "rack-01", MetricsSource: sourceLocal}),
		newHostState(hostConfig{Name: "rack-02", MetricsSource: sourceLocal}),
	}
	staleAfter := 5 * time.Minute
	noon := time.Date(2026, 10, 13, 12, 0, 0, 0, time.Local)
	a := newAlerter(defaultConfig().Telegram, len(hosts))
	var sent []string
	var fail error
	a.send = func(_ context.Context, text string) error {
		sent = append(sent, text)
		return fail
	}
	// check sends in the background; wait for and apply every result.
	check := func(now time.Time) {
		t.Helper()
		a.check(now, hosts, staleAfter)
		for i := range a.inFlight {
			if a.inFlight[i] {
				a.apply(<-a.results, now)
			}
		}
	}
	ok := func(h *hostState, at time.Time) { h.apply(pollResult{sample: rawSample{at: at}}) }
	failed := func(h *hostState) { h.apply(pollResult{err: errors.New("connection refused")}) }

	// Waiting hosts and healthy ones aren't reported.
	check(noon)
	ok(hosts[0], noon)
	check(noon)
	if len(sent) != 0 {
		t.Fatalf("sent %q for healthy hosts", sent)
	}

	// Never reached: reported at once. A single failure isn't reported until stale.
	failed(hosts[1])
	failed(hosts[0])
	check(noon.Add(time.Minute))
	if len(sent) != 1 || !strings.Contains(sent[0], "rack-02 is down: connection refused") {
		t.Fatalf("sent %q, want rack-02 down", sent)
	}
	check(noon.Add(6 * time.Minute))
	if len(sent) != 2 || !strings.Contains(sent[1], "rack-01 is not reachable, no data since 12:00: connection refused") {
		t.Fatalf("sent %q, want rack-01 unreachable", sent)
	}
	check(noon.Add(7 * time.Minute))
	if len(sent) != 2 {
		t.Fatalf("sent %q, want no repeat", sent)
	}

	// Recovery.
	ok(hosts[0], noon.Add(20*time.Minute))
	check(noon.Add(20 * time.Minute))
	if len(sent) != 3 || !strings.Contains(sent[2], "rack-01 is back up after 20m") {
		t.Fatalf("sent %q, want rack-01 back up", sent)
	}

	// A failed send shows in lastErr and is retried after sendRetry.
	fail = errors.New("Unauthorized")
	ok(hosts[1], noon.Add(21*time.Minute))
	check(noon.Add(21 * time.Minute))
	if a.lastErr == nil || len(sent) != 4 {
		t.Fatalf("lastErr = %v after failed send", a.lastErr)
	}
	fail = nil
	check(noon.Add(21*time.Minute + 30*time.Second))
	if len(sent) != 4 {
		t.Fatal("retried before sendRetry")
	}
	check(noon.Add(22 * time.Minute))
	if len(sent) != 5 || a.lastErr != nil || !strings.Contains(sent[4], "rack-02 is back up") {
		t.Fatalf("sent %q, lastErr %v, want rack-02 back up", sent, a.lastErr)
	}

	// Quiet hours: an outage that ends in them is never reported; one still
	// going when they end is reported then.
	night := time.Date(2026, 10, 14, 1, 0, 0, 0, time.Local)
	ok(hosts[0], night)
	ok(hosts[1], night)
	failed(hosts[0])
	failed(hosts[1])
	check(night.Add(time.Hour))
	ok(hosts[0], night.Add(2*time.Hour))
	check(night.Add(2 * time.Hour))
	if len(sent) != 5 {
		t.Fatalf("sent %q during quiet hours", sent[5:])
	}
	ok(hosts[0], night.Add(7*time.Hour))
	check(night.Add(7 * time.Hour))
	if len(sent) != 6 || !strings.Contains(sent[5], "rack-02 is not reachable, no data since 01:00") {
		t.Fatalf("sent %q, want only rack-02 at 08:00", sent[5:])
	}
}

func TestTelegramSendMessage(t *testing.T) {
	var path, chat, text string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, chat, text = r.URL.Path, r.FormValue("chat_id"), r.FormValue("text")
		if chat == "bad" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()
	defer func(api string) { telegramAPI = api }(telegramAPI)
	telegramAPI = srv.URL

	cfg := telegramConfig{BotToken: "123:secret", ChatID: "-42"}
	if err := cfg.sendMessage(context.Background(), "🔴 rack-02 is down"); err != nil {
		t.Fatal(err)
	}
	if path != "/bot123:secret/sendMessage" || chat != "-42" || text != "🔴 rack-02 is down" {
		t.Errorf("request = %s chat_id=%s text=%q", path, chat, text)
	}
	cfg.ChatID = "bad"
	if err := cfg.sendMessage(context.Background(), "x"); err == nil || err.Error() != "Bad Request: chat not found" {
		t.Errorf("err = %v, want Telegram's description", err)
	}

	telegramAPI = "http://127.0.0.1:1"
	err := cfg.sendMessage(context.Background(), "x")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("err = %v, want an error without the bot token", err)
	}
}
