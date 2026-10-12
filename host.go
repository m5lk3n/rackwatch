package main

import (
	"context"
	"errors"
	"math"
	"net"
	"net/url"
	"time"
)

// historyLen is enough samples to fill a sparkline across a very wide panel.
const historyLen = 512

type hostStatus int

const (
	statusWaiting hostStatus = iota // no attempt finished yet
	statusUp                        // fresh data
	statusStale                     // last good data is older than stale_after
	statusDown                      // never reached
)

// ring is a fixed-size buffer of recent samples. NaN marks a gap.
type ring struct {
	buf  []float64
	next int
	full bool
}

func newRing(n int) *ring { return &ring{buf: make([]float64, n)} }

func (r *ring) push(v float64) {
	r.buf[r.next] = v
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// last returns up to n of the newest samples, oldest first.
func (r *ring) last(n int) []float64 {
	size := r.next
	if r.full {
		size = len(r.buf)
	}
	n = min(n, size)
	out := make([]float64, n)
	start := r.next - n
	for i := range out {
		out[i] = r.buf[(start+i+len(r.buf))%len(r.buf)]
	}
	return out
}

type metricKind int

const (
	metricCPU metricKind = iota
	metricMem
	metricDisk
	metricTemp
	metricNet
	metricCount
)

func (m metrics) get(k metricKind) float64 {
	return [metricCount]float64{m.cpu, m.mem, m.disk, m.temp, m.net}[k]
}

type hostState struct {
	cfg      hostConfig
	collect  collector
	inFlight bool

	prev    *rawSample
	current metrics // last successful snapshot, kept while the source is unreachable
	lastOK  time.Time
	lastErr error
	history [metricCount]*ring
}

func newHostState(cfg hostConfig) *hostState {
	h := &hostState{cfg: cfg, collect: newCollector(cfg.MetricsSource), current: unknownMetrics()}
	for i := range h.history {
		h.history[i] = newRing(historyLen)
	}
	return h
}

type pollResult struct {
	host   int
	sample rawSample
	err    error
}

func (h *hostState) poll(idx int, timeout time.Duration, out chan<- pollResult) {
	h.inFlight = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		s, err := h.collect(ctx)
		out <- pollResult{host: idx, sample: s, err: err}
	}()
}

func (h *hostState) apply(r pollResult) {
	h.inFlight = false
	if r.err != nil {
		h.lastErr = r.err
		for _, hist := range h.history {
			hist.push(math.NaN())
		}
		return
	}
	h.current = derive(h.prev, r.sample)
	h.prev = &r.sample
	h.lastOK = r.sample.at
	h.lastErr = nil
	for k, hist := range h.history {
		hist.push(h.current.get(metricKind(k)))
	}
}

func (h *hostState) status(now time.Time, staleAfter time.Duration) hostStatus {
	switch {
	case h.lastOK.IsZero() && h.lastErr == nil:
		return statusWaiting
	case h.lastOK.IsZero():
		return statusDown
	case now.Sub(h.lastOK) > staleAfter:
		return statusStale
	default:
		return statusUp
	}
}

// shortErr trims the URL and wrapping from network errors so they fit a panel border.
func shortErr(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		err = uerr.Err
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return "unknown host " + dnsErr.Name
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		err = opErr.Err
	}
	return err.Error()
}
