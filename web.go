package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	ui "github.com/metaspartan/gotui"
)

//go:embed web/index.html
var webIndex []byte

// webIcons are the page's icons, made from images/logo.svg.
//
//go:embed web/favicon.svg web/favicon.ico web/apple-touch-icon.png
var webIcons embed.FS

// webLog records the web server starting and failing.
var webLog = journalLog("web")

// webSnapshot is the dashboard as the browser sees it. The main loop builds a
// new one whenever something changes, so HTTP handlers never touch hostState.
type webSnapshot struct {
	Version   string            `json:"version"`
	At        int64             `json:"at"` // when it was taken, Unix milliseconds
	Refresh   int               `json:"refresh_seconds"`
	Labels    []string          `json:"labels"`
	Hosts     []webHost         `json:"hosts"`
	Alerts    []webAlert        `json:"alerts,omitempty"` // alert services whose last send failed
	Theme     map[string]string `json:"theme"`
	Threshold []*float64        `json:"thresholds"` // per metric; null for none
}

type webHost struct {
	Name     string      `json:"name"`
	Status   string      `json:"status"`              // UP, STALE, DOWN or WAITING
	StaleFor float64     `json:"stale_for,omitempty"` // seconds since the last good poll, when STALE
	Error    string      `json:"error,omitempty"`
	Metrics  []webMetric `json:"metrics"`
}

type webMetric struct {
	Text    string     `json:"text"`  // as the terminal shows it, e.g. "42%"
	Value   *float64   `json:"value"` // null when unknown
	Scale   float64    `json:"scale"` // the value of a full-height sparkline bar
	History []*float64 `json:"history"`
}

type webAlert struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// newWebSnapshot captures the dashboard's hosts and alerts at now. It must run
// on the goroutine that owns them.
func newWebSnapshot(d *dashboard, now time.Time) *webSnapshot {
	s := &webSnapshot{
		Version: version,
		At:      now.UnixMilli(),
		Refresh: d.cfg.Dashboard.RefreshSeconds,
		Labels:  metricLabels[:],
		Theme:   webTheme(themes[d.cfg.Dashboard.Theme].forTerminal(false)),
	}
	for k := metricKind(0); k < metricCount; k++ {
		s.Threshold = append(s.Threshold, finite(d.threshold(k)))
	}
	for _, a := range d.alerts {
		if a.lastErr != nil {
			s.Alerts = append(s.Alerts, webAlert{a.name, shortErr(a.lastErr)})
		}
	}
	dc := d.cfg.Dashboard
	for _, h := range d.hosts {
		wh := webHost{Name: h.cfg.Name}
		switch h.status(now, dc.StaleAfter.Duration, dc.DownAfter.Duration) {
		case statusUp:
			wh.Status = "UP"
		case statusStale:
			wh.Status = "STALE"
			wh.StaleFor = now.Sub(h.lastOK).Seconds()
		case statusDown:
			wh.Status = "DOWN"
		default:
			wh.Status = "WAITING"
		}
		if h.lastErr != nil {
			wh.Error = shortErr(h.lastErr)
		}
		for k := metricKind(0); k < metricCount; k++ {
			v := h.current.get(k)
			m := webMetric{Text: formatMetric(k, v), Value: finite(v)}
			if dc.ShowSparklines {
				samples := h.history[k].last(historyLen)
				m.Scale = sparkScale(k, samples)
				m.History = make([]*float64, len(samples))
				for i, x := range samples {
					m.History[i] = finite(x)
				}
			}
			wh.Metrics = append(wh.Metrics, m)
		}
		s.Hosts = append(s.Hosts, wh)
	}
	return s
}

// finite returns v rounded to one decimal, or nil for NaN and infinities,
// which JSON can't carry.
func finite(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	v = math.Round(v*10) / 10
	return &v
}

// webTheme gives the page the colors of the dashboard's theme as CSS colors.
func webTheme(th *theme) map[string]string {
	hex := func(c ui.Color) string { return fmt.Sprintf("#%06x", c.Hex()) }
	return map[string]string{
		"bg":       hex(th.base.Bg),
		"border":   hex(th.base.Fg),
		"title-fg": hex(th.title.Fg),
		"title-bg": hex(th.title.Bg),
		"label":    hex(th.label.Fg),
		"value":    hex(th.value.Fg),
		"warn":     hex(th.warn.Fg),
		"spark":    hex(th.spark.Fg),
		"muted":    hex(th.muted.Fg),
		"host":     hex(th.host.Fg),
		"up-fg":    hex(th.up.Fg),
		"up-bg":    hex(th.up.Bg),
		"stale-fg": hex(th.stale.Fg),
		"stale-bg": hex(th.stale.Bg),
		"down-fg":  hex(th.down.Fg),
		"down-bg":  hex(th.down.Bg),
		"error":    hex(th.errText.Fg),
	}
}

// webServer serves the dashboard to browsers.
type webServer struct {
	snapshot atomic.Pointer[webPayload] // the latest webSnapshot
	refresh  chan struct{}              // a browser asked for a refresh
	srv      *http.Server
}

// webPayload is a webSnapshot encoded for the browser. Snapshots change once
// per poll round, so browsers polling more often mostly get 304 Not Modified.
type webPayload struct {
	json []byte
	etag string
}

func newWebServer() *webServer {
	w := &webServer{refresh: make(chan struct{}, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", w.serveIndex)
	mux.HandleFunc("GET /api/state", w.serveState)
	mux.HandleFunc("POST /api/refresh", w.serveRefresh)
	icons, _ := fs.Sub(webIcons, "web")
	iconServer := http.FileServerFS(icons)
	for _, name := range []string{"favicon.svg", "favicon.ico", "apple-touch-icon.png"} {
		mux.Handle("GET /"+name, iconServer)
	}
	w.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       time.Minute,
	}
	return w
}

// publish makes s what browsers get next.
func (w *webServer) publish(s *webSnapshot) {
	b, err := json.Marshal(s)
	if err != nil {
		webLog.Warn("encoding state", "err", err)
		return
	}
	sum := sha256.Sum256(b)
	w.snapshot.Store(&webPayload{json: b, etag: `"` + hex.EncodeToString(sum[:8]) + `"`})
}

// start listens on addr and serves in the background. It fails right away if
// it can't listen, e.g. when the port is taken.
func (w *webServer) start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("web: %w", err)
	}
	webLog.Info("serving dashboard", "addr", ln.Addr().String())
	go func() {
		if err := w.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			webLog.Warn("server stopped", "err", err)
		}
	}()
	return nil
}

func (w *webServer) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = w.srv.Shutdown(ctx)
}

func (w *webServer) serveIndex(rw http.ResponseWriter, _ *http.Request) {
	setSecurityHeaders(rw)
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = rw.Write(webIndex)
}

func (w *webServer) serveState(rw http.ResponseWriter, r *http.Request) {
	p := w.snapshot.Load()
	if p == nil {
		http.Error(rw, "starting", http.StatusServiceUnavailable)
		return
	}
	setSecurityHeaders(rw)
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("ETag", p.etag)
	if r.Header.Get("If-None-Match") == p.etag {
		rw.WriteHeader(http.StatusNotModified)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	_, _ = rw.Write(p.json)
}

// serveRefresh asks the main loop to poll all hosts now, like the refresh
// button. Requests while one is pending are folded into it.
func (w *webServer) serveRefresh(rw http.ResponseWriter, _ *http.Request) {
	select {
	case w.refresh <- struct{}{}:
	default:
	}
	rw.WriteHeader(http.StatusNoContent)
}

func setSecurityHeaders(rw http.ResponseWriter) {
	h := rw.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
}
