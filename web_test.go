package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebConfig(t *testing.T) {
	cfg := defaultConfig()
	cfg.Hosts = []hostConfig{{Name: "x", MetricsSource: sourceLocal}}
	cfg.Web.Enabled = true
	for listen, ok := range map[string]bool{
		":8080":          true,
		"127.0.0.1:8080": true,
		"[::1]:9000":     true,
		"8080":           false,
		"localhost":      false,
		"":               false,
	} {
		cfg.Web.Listen = listen
		if err := cfg.validate(); (err == nil) != ok {
			t.Errorf("listen %q: err = %v, want ok = %v", listen, err, ok)
		}
	}
	cfg.Web.Enabled = false
	cfg.Web.Listen = "nonsense"
	if err := cfg.validate(); err != nil {
		t.Errorf("disabled web with a bad listen address: %v", err)
	}
}

func testDashboard(t *testing.T) *dashboard {
	t.Helper()
	cfg := defaultConfig()
	cfg.Hosts = []hostConfig{{Name: "up"}, {Name: "stale"}, {Name: "down"}, {Name: "waiting"}}
	cfg.Dashboard.StaleAfter.Duration = 30 * time.Second
	hosts := make([]*hostState, len(cfg.Hosts))
	for i, hc := range cfg.Hosts {
		hosts[i] = newHostState(hc)
	}
	now := time.Now()
	sample := func(at time.Time, cpu float64) rawSample {
		return rawSample{at: at, cpuTotal: cpu, memTotal: 100, memAvail: 10, diskSize: 100, diskAvail: 50,
			tempC: math.NaN(), netBytes: 1000}
	}
	hosts[0].apply(pollResult{sample: sample(now.Add(-2*time.Second), 0)})
	hosts[0].apply(pollResult{sample: sample(now, 100)})
	hosts[1].apply(pollResult{sample: sample(now.Add(-time.Minute), 0)})
	hosts[1].apply(pollResult{err: context.DeadlineExceeded})
	hosts[2].apply(pollResult{err: errors.New("connection refused")})
	d := newDashboard(cfg, hosts)
	d.alerts = []*alerter{{name: "NTFY", lastErr: errors.New("HTTP 403 Forbidden")}}
	return d
}

func TestWebSnapshot(t *testing.T) {
	d := testDashboard(t)
	s := newWebSnapshot(d, time.Now())

	var got []string
	for _, h := range s.Hosts {
		got = append(got, h.Status)
	}
	if want := "UP STALE DOWN WAITING"; strings.Join(got, " ") != want {
		t.Errorf("statuses = %v, want %s", got, want)
	}
	if sf := s.Hosts[1].StaleFor; sf < 59 || sf > 61 {
		t.Errorf("stale_for = %v, want about 60", sf)
	}
	if e := s.Hosts[1].Error; e != "timeout" {
		t.Errorf("stale host error = %q, want timeout", e)
	}

	up := s.Hosts[0].Metrics
	if up[metricMem].Text != "90%" || *up[metricMem].Value != 90 {
		t.Errorf("RAM = %q/%v, want 90%%", up[metricMem].Text, *up[metricMem].Value)
	}
	if up[metricTemp].Value != nil || up[metricTemp].Text != "--" {
		t.Errorf("unknown temperature = %q/%v, want --/null", up[metricTemp].Text, up[metricTemp].Value)
	}
	if h := up[metricCPU].History; len(h) != 2 || h[0] != nil || *h[1] != 100 {
		t.Errorf("CPU history = %v, want [null 100]", h)
	}
	if s.Threshold[metricNet] != nil || *s.Threshold[metricCPU] != 80 {
		t.Errorf("thresholds = %v, want CPU 80 and none for NET", s.Threshold)
	}
	if len(s.Alerts) != 1 || s.Alerts[0].Name != "NTFY" {
		t.Errorf("alerts = %v, want the failing NTFY", s.Alerts)
	}
	if s.Theme["bg"] != "#191a1b" {
		t.Errorf("theme bg = %q, want the default theme's #191a1b", s.Theme["bg"])
	}

	// NaN and infinities must not break the encoding.
	if _, err := json.Marshal(s); err != nil {
		t.Fatal(err)
	}

	d.cfg.Dashboard.ShowSparklines = false
	if h := newWebSnapshot(d, time.Now()).Hosts[0].Metrics[metricCPU].History; h != nil {
		t.Errorf("history without sparklines = %v, want none", h)
	}
}

func TestWebServer(t *testing.T) {
	w := newWebServer()
	srv := httptest.NewServer(w.srv.Handler)
	defer srv.Close()

	get := func(path, etag string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = res.Body.Close() })
		return res
	}

	if res := get("/api/state", ""); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("state before the first snapshot: %s, want 503", res.Status)
	}

	res := get("/", "")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "RACKWATCH") {
		t.Errorf("index: %s, want the dashboard page", res.Status)
	}
	for path, ctype := range map[string]string{
		"/favicon.svg":          "image/svg+xml",
		"/favicon.ico":          "image/",
		"/apple-touch-icon.png": "image/png",
	} {
		if res := get(path, ""); res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), ctype) {
			t.Errorf("%s: %s %q, want 200 %s", path, res.Status, res.Header.Get("Content-Type"), ctype)
		}
	}
	if res := get("/index.html", ""); res.StatusCode != http.StatusNotFound {
		t.Errorf("/index.html: %s, want 404 (only the icons are served as files)", res.Status)
	}
	if res := get("/nope", ""); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path: %s, want 404", res.Status)
	}

	w.publish(newWebSnapshot(testDashboard(t), time.Now()))
	res = get("/api/state", "")
	var s webSnapshot
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	if len(s.Hosts) != 4 || s.Hosts[0].Name != "up" {
		t.Errorf("state hosts = %v", s.Hosts)
	}
	etag := res.Header.Get("ETag")
	if res := get("/api/state", etag); res.StatusCode != http.StatusNotModified {
		t.Errorf("state with a current ETag: %s, want 304", res.Status)
	}
	w.publish(newWebSnapshot(testDashboard(t), time.Now().Add(time.Second)))
	if res := get("/api/state", etag); res.StatusCode != http.StatusOK {
		t.Errorf("state with an old ETag: %s, want 200", res.Status)
	}

	if res := get("/api/refresh", ""); res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET refresh: %s, want 405", res.Status)
	}
	for range 2 { // a second request while one is pending must not block
		res, err := http.Post(srv.URL+"/api/refresh", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Errorf("POST refresh: %s, want 204", res.Status)
		}
	}
	select {
	case <-w.refresh:
	default:
		t.Error("POST refresh didn't ask for a refresh")
	}
}

func TestWebServerPortTaken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if err := newWebServer().start(ln.Addr().String()); err == nil {
		t.Errorf("server on the taken %s started, want an error", ln.Addr())
	}
}
