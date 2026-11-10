package main

import (
	"context"
	"fmt"
	"time"
)

const (
	// sendTimeout bounds one send.
	sendTimeout = 15 * time.Second
	// sendRetry is how long to wait after a failed send before trying again.
	sendRetry = time.Minute
)

// alerter sends a message through one service, like Telegram or ntfy, when a host goes STALE, when it goes DOWN
// (including from STALE) and when it recovers. It remembers what it last reported for each
// host rather than reacting to changes, so an outage that starts during quiet
// hours is reported when they end, and one that also ends during them isn't.
type alerter struct {
	name     string // shown in the header with the error when a send fails
	quiet    func(now time.Time) bool
	send     func(ctx context.Context, text string) error
	reported []hostStatus // per host: the status the last message sent; statusUp or zero when none
	since    []time.Time  // per host: when the reported outage started
	inFlight []bool
	retryAt  time.Time
	lastErr  error // why the last send failed; nil once one succeeds
	results  chan<- alertResult
}

type alertResult struct {
	alerter *alerter
	host    int
	status  hostStatus
	since   time.Time
	err     error
}

// newAlerter returns an alerter that sends with send outside the quiet hours
// from..until and delivers each send's result on results, which should have
// room for one per host.
func newAlerter(name string, from, until clockTime, send func(ctx context.Context, text string) error,
	hosts int, results chan<- alertResult) *alerter {
	return &alerter{
		name:     name,
		quiet:    func(now time.Time) bool { return within(now, from, until) },
		send:     send,
		reported: make([]hostStatus, hosts),
		since:    make([]time.Time, hosts),
		inFlight: make([]bool, hosts),
		results:  results,
	}
}

// newAlerters returns an alerter for each enabled service. Their results all
// arrive on the returned channel; apply each to its r.alerter.
func newAlerters(cfg config, hosts int) ([]*alerter, <-chan alertResult) {
	var alerters []*alerter
	results := make(chan alertResult, 2*hosts)
	if t := cfg.Telegram; t.Enabled {
		alerters = append(alerters, newAlerter("TELEGRAM", t.QuietFrom, t.QuietUntil, t.sendMessage, hosts, results))
	}
	if n := cfg.Ntfy; n.Enabled {
		alerters = append(alerters, newAlerter("NTFY", n.QuietFrom, n.QuietUntil, n.publish, hosts, results))
	}
	if alerters == nil {
		return nil, nil
	}
	return alerters, results
}

// check starts a send for every host whose status differs from what was last
// reported. Sends run in the background; their results arrive on a.results.
func (a *alerter) check(now time.Time, hosts []*hostState, staleAfter, downAfter time.Duration) {
	if a.quiet(now) || now.Before(a.retryAt) {
		return
	}
	for i, h := range hosts {
		st := h.status(now, staleAfter, downAfter)
		last := a.reported[i]
		if last == statusWaiting {
			last = statusUp
		}
		if st == statusWaiting || st == last || a.inFlight[i] {
			continue
		}
		since := a.since[i]
		if st != statusUp && last == statusUp {
			since = h.lastOK
			if since.IsZero() {
				since = now
			}
		}
		text := alertText(h, st, now, since)
		a.inFlight[i] = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
			defer cancel()
			a.results <- alertResult{alerter: a, host: i, status: st, since: since, err: a.send(ctx, text)}
		}()
	}
}

func (a *alerter) apply(r alertResult, now time.Time) {
	a.inFlight[r.host] = false
	if r.err != nil {
		a.lastErr = r.err
		a.retryAt = now.Add(sendRetry)
		return
	}
	a.lastErr = nil
	a.reported[r.host] = r.status
	a.since[r.host] = r.since
}

func alertText(h *hostState, st hostStatus, now, since time.Time) string {
	reason := ""
	if h.lastErr != nil {
		reason = ": " + shortErr(h.lastErr)
	}
	switch {
	case st == statusDown && h.lastOK.IsZero():
		return fmt.Sprintf("⛔ %s is DOWN, never reached since rackwatch started%s", h.cfg.Name, reason)
	case st == statusDown:
		return fmt.Sprintf("⛔ %s went from STALE to DOWN, no data since %s (%s)%s",
			h.cfg.Name, clock(since, now), now.Sub(since).Round(time.Minute), reason)
	case st == statusStale:
		return fmt.Sprintf("⚠️ %s is not reachable, no data since %s%s", h.cfg.Name, clock(since, now), reason)
	}
	return fmt.Sprintf("✅ %s is back up after %s", h.cfg.Name, now.Sub(since).Round(time.Minute))
}

// clock formats t as a time of day, with the date when it isn't on now's day.
func clock(t, now time.Time) string {
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}
