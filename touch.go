package main

import (
	"errors"
	"image"
	"os"
	"syscall"
	"time"

	ui "github.com/metaspartan/gotui"
)

// touchButton is something in the header that a tap or click triggers.
type touchButton int

const (
	buttonNone touchButton = iota
	buttonRestart
	buttonRefresh
)

// restartConfirm is how long a tap on restart waits for the second tap that
// confirms it, so a stray touch can't restart the dashboard.
const restartConfirm = 5 * time.Second

// touchSlack widens each button by this many cells on every side: a finger is
// wide, and an uncalibrated panel can be off by a cell.
const touchSlack = 1

// errRestart makes run return so its cleanup runs before main restarts rackwatch.
var errRestart = errors.New("restart requested")

// headerButton is where a button was last drawn, in screen cells.
type headerButton struct {
	rect   image.Rectangle
	button touchButton
}

// buttonAt reports which button, if any, the cell x, y belongs to.
func buttonAt(buttons []headerButton, x, y int) touchButton {
	p := image.Pt(x, y)
	for _, b := range buttons {
		if p.In(b.rect.Inset(-touchSlack)) {
			return b.button
		}
	}
	return buttonNone
}

// touchTracker turns mouse events into taps. A finger held down, or dragged,
// keeps sending events with the button pressed; only the first one is a tap.
type touchTracker struct {
	down bool
}

// tap reports whether e starts a new press, and where.
func (t *touchTracker) tap(e ui.Event) (image.Point, bool) {
	if e.Type != ui.MouseEvent {
		return image.Point{}, false
	}
	pressed := e.ID == "<MouseLeft>"
	started := pressed && !t.down
	t.down = pressed
	m, ok := e.Payload.(ui.Mouse)
	if !started || !ok {
		return image.Point{}, false
	}
	return image.Pt(m.X, m.Y), true
}

// restartSelf replaces the process with a fresh rackwatch, same arguments.
func restartSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
