package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	ui "github.com/metaspartan/gotui"
)

// version is set at build time via -ldflags "-X main.version=..." (see Makefile).
var version = "dev"

func main() {
	configPath := flag.String("config", "config.toml", "path to the TOML config file")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rackwatch:", err)
		os.Exit(1)
	}
	if err := run(cfg); err != nil {
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

	dash := newDashboard(cfg, hosts)
	saver := newMatrixRain(console)
	saverOn := false
	render := func() {
		w, h := ui.TerminalDimensions()
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
	// key or the mouse is used. Nil channels stay quiet while it's off.
	var saverStart, saverEnd, frames <-chan time.Time
	var frameTicker *time.Ticker
	if cfg.Screensaver.Enabled {
		t := time.NewTicker(cfg.Screensaver.Interval.Duration)
		defer t.Stop()
		saverStart = t.C
	}
	stopSaver := func() {
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

	ticker := time.NewTicker(cfg.refresh())
	defer ticker.Stop()
	events := ui.PollEvents()
	for {
		select {
		case e := <-events:
			if saverOn && e.Type != ui.ResizeEvent && e.ID != "<C-c>" {
				stopSaver()
				render()
				continue
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
			render()
		case <-ticker.C:
			pollAll()
			render()
		case <-saverStart:
			startSaver()
			render()
		case <-frames:
			saver.step()
			render()
		case <-saverEnd:
			stopSaver()
			render()
		}
	}
}
