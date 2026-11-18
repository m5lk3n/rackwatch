package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	w := newWebServer(nil)
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
	if err := newWebServer(nil).start(ln.Addr().String()); err == nil {
		t.Errorf("server on the taken %s started, want an error", ln.Addr())
	}
}

// rackPicture returns a w × h picture, grey but for yellow cutouts.
func rackPicture(w, h int, cutouts ...image.Rectangle) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{0x80, 0x80, 0x80, 0xff}), image.Point{}, draw.Src)
	for _, c := range cutouts {
		draw.Draw(img, c, image.NewUniform(color.RGBA{0xff, 0xff, 0x00, 0xff}), image.Point{}, draw.Src)
	}
	return img
}

// writeRack writes rackPicture(w, h, cutout) as a PNG and returns its path.
func writeRack(t *testing.T, w, h int, cutout image.Rectangle) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, rackPicture(w, h, cutout)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rack.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRackImage(t *testing.T) {
	bg := color.RGBA{0x19, 0x1a, 0x1b, 0xff}
	cut := image.Rect(30, 40, 230, 90)
	r, err := loadRackImage(writeRack(t, 300, 200, cut), bg)
	if err != nil {
		t.Fatal(err)
	}
	if r.cutout != cut || r.size != image.Pt(300, 200) {
		t.Errorf("cutout %v in %v, want %v in 300×200", r.cutout, r.size, cut)
	}
	if got, want := *r.web(), (webRack{300, 200, 30, 40, 200, 50}); got != want {
		t.Errorf("web() = %+v, want %+v", got, want)
	}
	img, err := jpeg.Decode(bytes.NewReader(r.jpeg))
	if err != nil {
		t.Fatal(err)
	}
	// JPEG shifts colors a little.
	near := func(a, b color.Color) bool {
		ar, ag, ab, _ := a.RGBA()
		br, bg, bb, _ := b.RGBA()
		d := func(x, y uint32) bool { return x-y < 0x0600 || y-x < 0x0600 }
		return d(ar, br) && d(ag, bg) && d(ab, bb)
	}
	if c := img.At(100, 60); !near(c, bg) {
		t.Errorf("cutout filled with %v, want the theme background %v", c, bg)
	}
	if c := img.At(10, 10); !near(c, color.RGBA{0x80, 0x80, 0x80, 0xff}) {
		t.Errorf("outside the cutout = %v, want it unchanged", c)
	}

	for name, path := range map[string]string{
		"no yellow": writeRack(t, 100, 100, image.Rectangle{}),
		"tiny":      writeRack(t, 100, 100, image.Rect(10, 10, 15, 15)),
		"missing":   filepath.Join(t.TempDir(), "nope.png"),
		"not an image": func() string {
			p := filepath.Join(t.TempDir(), "x.png")
			_ = os.WriteFile(p, []byte("hi"), 0o600)
			return p
		}(),
	} {
		if _, err := loadRackImage(path, bg); err == nil {
			t.Errorf("%s: loaded, want an error", name)
		}
	}
	// Two yellow patches far apart aren't one screen.
	if _, err := findCutout(rackPicture(300, 100, image.Rect(0, 0, 30, 30), image.Rect(270, 70, 300, 100))); err == nil {
		t.Error("two separate yellow patches accepted, want an error")
	}
}

func TestWebServerRack(t *testing.T) {
	r, err := loadRackImage(writeRack(t, 300, 200, image.Rect(30, 40, 230, 90)), color.RGBA{A: 0xff})
	if err != nil {
		t.Fatal(err)
	}
	w := newWebServer(r)
	srv := httptest.NewServer(w.srv.Handler)
	defer srv.Close()
	w.publish(newWebSnapshot(testDashboard(t), time.Now()))

	res, err := http.Get(srv.URL + "/rack.jpg")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/jpeg" || !bytes.Equal(body, r.jpeg) {
		t.Errorf("rack.jpg: %s %q, %d bytes; want the picture", res.Status, res.Header.Get("Content-Type"), len(body))
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/rack.jpg", nil)
	req.Header.Set("If-None-Match", res.Header.Get("ETag"))
	if res, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	} else if _ = res.Body.Close(); res.StatusCode != http.StatusNotModified {
		t.Errorf("rack.jpg with its ETag: %s, want 304", res.Status)
	}
	res, err = http.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var s webSnapshot
	_ = json.NewDecoder(res.Body).Decode(&s)
	_ = res.Body.Close()
	if s.Rack == nil || *s.Rack != (webRack{300, 200, 30, 40, 200, 50}) {
		t.Errorf("state rack = %+v, want the picture's cutout", s.Rack)
	}

	// Without web.custom there's no picture to get.
	plain := httptest.NewServer(newWebServer(nil).srv.Handler)
	defer plain.Close()
	res, err = http.Get(plain.URL + "/rack.jpg")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("rack.jpg without web.custom: %s, want 404", res.Status)
	}
}

func TestWebCustomPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	write := func(custom string) config {
		t.Helper()
		toml := "[web]\nenabled = true\ncustom = \"" + custom + "\"\n[[hosts]]\nname = \"x\"\nmetrics_source = \"local\"\n"
		if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	if got, want := write("rack.png").Web.Custom, filepath.Join(dir, "rack.png"); got != want {
		t.Errorf("relative custom = %q, want %q (next to the config)", got, want)
	}
	if got := write("/srv/rack.png").Web.Custom; got != "/srv/rack.png" {
		t.Errorf("absolute custom = %q, want it unchanged", got)
	}
	if got := write("").Web.Custom; got != "" {
		t.Errorf("empty custom = %q, want empty", got)
	}
}

func TestValidateConfig(t *testing.T) {
	if _, err := validateConfig("config.toml.example"); err != nil {
		t.Errorf("config.toml.example: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	toml := "[web]\nenabled = true\ncustom = \"missing.png\"\n[[hosts]]\nname = \"x\"\nmetrics_source = \"local\"\n"
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if _, err := validateConfig(path); err == nil {
		t.Error("missing web.custom picture accepted, want error")
	}
}
