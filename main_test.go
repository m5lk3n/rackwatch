package main

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
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
	if cfg.Dashboard.StaleAfter.Duration != 15*time.Second || len(cfg.Hosts) != 3 {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

// TestRender draws the dashboard at roughly the rack display's size
// (1424×280 px ≈ 178×17 cells with an 8×16 font) and logs it; run with -v to see it.
func TestRender(t *testing.T) {
	cfg, err := loadConfig("config.toml.example")
	if err != nil {
		t.Fatal(err)
	}
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
	// rack-01: healthy with history; rack-02: went away a while ago; rack-03: never reachable.
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
	hosts[2].apply(pollResult{err: errors.New("unknown host rack-03.internal")})

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
	for _, want := range []string{"RACKWATCH", "1/3 UP", "rack-01", " UP ", "STALE", "DOWN", "unknown host", "timeout", "61%", "90%"} {
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
	cfg := defaultConfig()
	cfg.Hosts = []hostConfig{{Name: "a", MetricsSource: sourceLocal}}
	cfg.Dashboard.Theme = "amiga"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "brotkasten") {
		t.Errorf("unknown theme should list available themes, got %v", err)
	}
}
