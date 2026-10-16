package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	ui "github.com/metaspartan/gotui"
)

// version and builtBy are set at build time via -ldflags "-X main.version=..."
// and "-X main.builtBy=..." (see Makefile).
var (
	version = "dev"
	builtBy = "dev"
)

func main() {
	configPath := flag.String("config", "config.toml", "path to the TOML config file")
	flag.Parse()

	if flag.Arg(0) == "version" {
		fmt.Printf("rackwatch %s (built by %s)\n", version, builtBy)
		return
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rackwatch:", err)
		os.Exit(1)
	}
	err = run(cfg)
	if errors.Is(err, errRestart) {
		err = restartSelf()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "rackwatch:", err)
		os.Exit(1)
	}
}

func run(cfg config) error {
	hosts := make([]*hostState, len(cfg.Hosts))
	for i, hc := range cfg.Hosts {
		hosts[i] = newHostState(hc)
	}

	console := onLinuxConsole()
	if console {
		// Registered before ui.Close, so it runs after the screen is released.
		defer themes[cfg.Dashboard.Theme].setConsolePalette(os.Stdout)()
	}
	if err := ui.Init(); err != nil {
		return fmt.Errorf("initializing terminal: %w", err)
	}
	defer ui.Close()

	// Telegram alerts are sent in the background, like polls. Without them
	// alertResults stays nil and quiet.
	var alerts *alerter
	var alertResults <-chan alertResult
	if cfg.Telegram.Enabled {
		alerts = newAlerter(cfg.Telegram, len(hosts))
		alertResults = alerts.results
	}
	checkAlerts := func() {
		if alerts != nil {
			alerts.check(time.Now(), hosts, cfg.Dashboard.StaleAfter.Duration, cfg.Dashboard.DownAfter.Duration)
		}
	}

	dash := newDashboard(cfg, hosts)
	dash.alerts = alerts
	saver := newMatrixRain(console)
	saverOn := false
	// The black-out period blanks the screen, and on the Linux console turns
	// the display off; a key or touch shows the dashboard for blackoutWake.
	// Polls and alerts carry on meanwhile.
	black := newBlank(console)
	dark := false           // the black-out screen is showing
	var wakeUntil time.Time // during black-out, a key or touch shows the dashboard until then
	var stopSaver func()
	defer func() {
		if dark && console {
			blankConsole(false)
		}
	}()
	updateDark := func(now time.Time) {
		want := cfg.Blackout.active(now) && !now.Before(wakeUntil)
		if want == dark {
			return
		}
		dark = want
		if dark {
			blackoutLog.Print("screen off")
			stopSaver()
			if console {
				black.th.setConsolePalette(os.Stdout)
				// Draw the black screen before switching the display off: the
				// console draws nothing while blanked, and if the display comes
				// back on by itself it shows what was drawn last.
				w, h := ui.TerminalDimensions()
				black.SetRect(0, 0, w, h)
				ui.Render(black)
				blankConsole(true)
			}
			return
		}
		blackoutLog.Print("screen on")
		// The black screen covers the margins too, which the dashboard doesn't repaint.
		ui.Clear()
		if console {
			blankConsole(false)
			themes[cfg.Dashboard.Theme].setConsolePalette(os.Stdout)
		}
	}
	render := func() {
		updateDark(time.Now())
		w, h := ui.TerminalDimensions()
		if dark {
			black.SetRect(0, 0, w, h)
			ui.Render(black)
			return
		}
		if saverOn {
			saver.SetRect(0, 0, w, h)
			ui.Render(saver)
			return
		}
		// Margins skip rows and columns the physical screen hides behind its bezel.
		m := cfg.Dashboard
		dash.SetRect(m.MarginLeft, m.MarginTop, w-m.MarginRight, h-m.MarginBottom)
		dash.now = time.Now()
		ui.Render(dash)
	}

	// The screensaver starts every interval and runs for duration, or until a
	// key, click or touch. Nil channels stay quiet while it's off.
	var saverStart, saverEnd, frames <-chan time.Time
	var frameTicker *time.Ticker
	if cfg.Screensaver.Enabled {
		t := time.NewTicker(cfg.Screensaver.Interval.Duration)
		defer t.Stop()
		saverStart = t.C
	}
	var blackoutTicks <-chan time.Time
	if cfg.Blackout.Enabled {
		t := time.NewTicker(blackoutCheck)
		defer t.Stop()
		blackoutTicks = t.C
	}
	stopSaver = func() {
		if !saverOn {
			return
		}
		saverOn = false
		frameTicker.Stop()
		saverEnd, frames = nil, nil
		// The rain covers the margins too, which the dashboard doesn't repaint.
		ui.Clear()
		if console {
			// Swap the matrix greens back out for the theme's colors.
			themes[cfg.Dashboard.Theme].setConsolePalette(os.Stdout)
		}
	}
	startSaver := func() {
		if saverOn {
			return
		}
		saverOn = true
		frameTicker = time.NewTicker(saverFrame)
		frames = frameTicker.C
		saverEnd = time.After(cfg.Screensaver.Duration.Duration)
		if console {
			saver.th.setConsolePalette(os.Stdout)
		}
	}

	// Each host has at most one poll in flight, so a slow or unreachable
	// source never blocks the UI or the other hosts, and polls never pile up.
	timeout := max(cfg.refresh(), 2*time.Second)
	results := make(chan pollResult, len(hosts))
	pollAll := func() {
		for i, h := range hosts {
			if !h.inFlight {
				h.poll(i, timeout, results)
			}
		}
	}

	pollAll()
	render()

	// A tap on restart arms it; a second tap before restartEnd fires restarts.
	var touch touchTracker
	var restartEnd <-chan time.Time
	disarm := func() {
		dash.restartArmed = false
		restartEnd = nil
	}

	ticker := time.NewTicker(cfg.refresh())
	defer ticker.Stop()
	events := ui.PollEvents()
	if cfg.Touch.Enabled {
		// Touches come in alongside the terminal's events, as mouse events.
		merged := make(chan ui.Event)
		go func(terminal <-chan ui.Event) {
			for e := range terminal {
				merged <- e
			}
		}(events)
		go readTouchscreen(cfg.Touch, merged)
		events = merged
	}
	for {
		select {
		case e := <-events:
			// Tracked before the screensaver and black-out see the event, so
			// the finger that ends them doesn't also tap a button.
			at, tapped := touch.tap(e)
			if now := time.Now(); deliberate(e) && e.ID != "<C-c>" && cfg.Blackout.active(now) {
				// Keep the dashboard up while it's being used.
				if dark {
					blackoutLog.Printf("woken by %s", e.ID)
				}
				wakeUntil = now.Add(blackoutWake)
				if dark {
					render()
					continue
				}
			}
			if saverOn && deliberate(e) && e.ID != "<C-c>" {
				stopSaver()
				render()
				continue
			}
			if tapped {
				switch buttonAt(dash.buttons, at.X, at.Y) {
				case buttonRestart:
					if dash.restartArmed {
						return errRestart
					}
					dash.restartArmed = true
					restartEnd = time.After(restartConfirm)
				case buttonRefresh:
					disarm()
					pollAll()
				default:
					disarm()
				}
			}
			switch e.ID {
			case "q", "<C-c>", "<Escape>":
				return nil
			case "r":
				pollAll()
			case "<Resize>":
				ui.Clear()
			}
			render()
		case r := <-results:
			hosts[r.host].apply(r)
			checkAlerts()
			render()
		case r := <-alertResults:
			alerts.apply(r, time.Now())
			render()
		case <-ticker.C:
			pollAll()
			checkAlerts()
			render()
		case <-saverStart:
			if !dark {
				startSaver()
			}
			render()
		case <-frames:
			saver.step()
			render()
		case <-saverEnd:
			stopSaver()
			render()
		case <-blackoutTicks:
			render()
			// If the kernel turned the display back on, switch it off again.
			// Poke first: after a hotplug the console still counts itself as
			// blanked and would ignore another force.
			if dark && console && displayOn() {
				blackoutLog.Print("screen came back on by itself, switching it off again")
				blankConsole(false)
				blankConsole(true)
			}
		case <-restartEnd:
			disarm()
			render()
		}
	}
}
