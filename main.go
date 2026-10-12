package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	ui "github.com/metaspartan/gotui"
)

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

	if err := ui.Init(); err != nil {
		return fmt.Errorf("initializing terminal: %w", err)
	}
	defer ui.Close()

	dash := newDashboard(cfg, hosts)
	render := func() {
		w, h := ui.TerminalDimensions()
		dash.SetRect(0, 0, w, h)
		dash.now = time.Now()
		ui.Render(dash)
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
		}
	}
}
