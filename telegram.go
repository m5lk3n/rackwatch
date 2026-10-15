package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// telegramAPI is the Bot API's base URL; tests point it at a local server.
var telegramAPI = "https://api.telegram.org"

const (
	// sendTimeout bounds one sendMessage call.
	sendTimeout = 15 * time.Second
	// sendRetry is how long to wait after a failed send before trying again.
	sendRetry = time.Minute
)

// alerter sends a Telegram message when a host goes down (STALE or DOWN on the
// dashboard) and when it recovers. It remembers what it last reported for each
// host rather than reacting to changes, so an outage that starts during quiet
// hours is reported when they end, and one that also ends during them isn't.
type alerter struct {
	cfg      telegramConfig
	send     func(ctx context.Context, text string) error
	reported []bool      // per host: the last message sent said it was down
	since    []time.Time // per host: when the reported outage started
	inFlight []bool
	retryAt  time.Time
	lastErr  error // why the last send failed; nil once one succeeds
	results  chan alertResult
}

type alertResult struct {
	host  int
	down  bool
	since time.Time
	err   error
}

func newAlerter(cfg telegramConfig, hosts int) *alerter {
	return &alerter{
		cfg:      cfg,
		send:     cfg.sendMessage,
		reported: make([]bool, hosts),
		since:    make([]time.Time, hosts),
		inFlight: make([]bool, hosts),
		results:  make(chan alertResult, hosts),
	}
}

// check starts a send for every host whose status differs from what was last
// reported. Sends run in the background; their results arrive on a.results.
func (a *alerter) check(now time.Time, hosts []*hostState, staleAfter time.Duration) {
	if a.cfg.quiet(now) || now.Before(a.retryAt) {
		return
	}
	for i, h := range hosts {
		st := h.status(now, staleAfter)
		down := st == statusStale || st == statusDown
		if st == statusWaiting || down == a.reported[i] || a.inFlight[i] {
			continue
		}
		since := a.since[i]
		if down {
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
			a.results <- alertResult{host: i, down: down, since: since, err: a.send(ctx, text)}
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
	a.reported[r.host] = r.down
	a.since[r.host] = r.since
}

func alertText(h *hostState, st hostStatus, now, since time.Time) string {
	reason := ""
	if h.lastErr != nil {
		reason = ": " + shortErr(h.lastErr)
	}
	switch st {
	case statusDown:
		return fmt.Sprintf("⛔ %s is down%s", h.cfg.Name, reason)
	case statusStale:
		return fmt.Sprintf("⚠️ %s is not reachable, no data since %s%s", h.cfg.Name, since.Format("15:04"), reason)
	}
	return fmt.Sprintf("✅ %s is back up after %s", h.cfg.Name, now.Sub(since).Round(time.Minute))
}

// quiet reports whether t, in the local time of this machine, falls within the
// quiet hours. The period may wrap past midnight; equal ends disable it.
func (t telegramConfig) quiet(now time.Time) bool {
	return within(now, t.QuietFrom, t.QuietUntil)
}

func (t telegramConfig) sendMessage(ctx context.Context, text string) error {
	form := url.Values{"chat_id": {string(t.ChatID)}, "text": {text}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		telegramAPI+"/bot"+t.BotToken+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("invalid bot_token")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// The request URL contains the bot token, so keep it out of the error.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body); err != nil {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !body.OK && body.Description != "" {
		return errors.New(body.Description)
	}
	if !body.OK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
