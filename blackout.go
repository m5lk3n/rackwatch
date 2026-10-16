package main

import (
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	ui "github.com/metaspartan/gotui"
)

// blackoutWake is how long a key or touch shows the dashboard during the
// black-out period, and blackoutCheck how often to look whether it began or ended.
const (
	blackoutWake  = time.Minute
	blackoutCheck = 10 * time.Second
)

// blackPalette is loaded into the Linux console during the black-out period,
// since the themes' darkest slot is only nearly black.
var blackPalette [16]uint32

// active reports whether the screen should be black at now.
func (b blackoutConfig) active(now time.Time) bool {
	return b.Enabled && within(now, b.From, b.Until)
}

// blank is the black screen shown during the black-out period.
type blank struct {
	ui.Block
	th    *theme
	style ui.Style
}

func newBlank(console bool) *blank {
	b := &blank{Block: *ui.NewBlock(), th: &theme{palette: &blackPalette}}
	b.Border = false
	c := tcell.PaletteColor(0)
	b.style = ui.NewStyle(c, c)
	if !console {
		b.style = ui.NewStyle(b.th.rgb(c), b.th.rgb(c))
	}
	return b
}

func (b *blank) Draw(buf *ui.Buffer) {
	buf.Fill(ui.NewCell(' ', b.style), b.GetRect())
}

// blankConsole turns the Linux console's display off, or back on, with
// "setterm --blank force" and "--blank poke". Unlike a black screen this can
// also switch off the backlight, where the display driver supports it. setterm
// works on its stdin, which must be the console rackwatch is running on.
func blankConsole(off bool) {
	arg := "poke"
	if off {
		arg = "force"
	}
	cmd := exec.Command("setterm", "--blank", arg)
	cmd.Stdin = os.Stdin
	// Without setterm, or if it fails, the screen just stays black.
	if out, err := cmd.CombinedOutput(); err != nil {
		blackoutLog.Printf("setterm --blank %s: %v %s", arg, err, out)
	}
}

// drmDPMS matches the files where DRM connectors report whether their output
// is on. A variable so tests can point it elsewhere.
var drmDPMS = "/sys/class/drm/*/dpms"

// displayOn reports whether the output to a connected display is on. During
// the black-out the kernel can turn it back on by itself: any event from an
// input device bound to the console keyboard, even noise from a touchscreen,
// unblanks the console, and a hotplug restores the mode with the output on.
// Without DRM it reports false, as there is nothing to check.
func displayOn() bool {
	paths, _ := filepath.Glob(drmDPMS)
	for _, p := range paths {
		status, err := os.ReadFile(filepath.Join(filepath.Dir(p), "status"))
		if err != nil || strings.TrimSpace(string(status)) != "connected" {
			continue
		}
		if dpms, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(dpms)) == "On" { // #nosec G304 -- p comes from a fixed sysfs glob
			return true
		}
	}
	return false
}

// blackoutLog records when the black-out starts and ends and what woke the
// dashboard.
var blackoutLog = journalLog("blackout: ")

// journalLog logs to stderr, for the journal when running as a service. On a
// terminal it would scribble over the dashboard, so it stays quiet there.
func journalLog(prefix string) *log.Logger {
	if fi, err := os.Stderr.Stat(); err != nil || fi.Mode()&os.ModeCharDevice != 0 {
		return log.New(io.Discard, "", 0)
	}
	return log.New(os.Stderr, prefix, 0)
}

// deliberate reports whether e is someone using the screen: a key, or a
// mouse button or touch. Mouse motion and button releases come without a
// button, and a jittery touchscreen or mouse sends those on its own.
func deliberate(e ui.Event) bool {
	switch e.Type {
	case ui.KeyboardEvent:
		return true
	case ui.MouseEvent:
		return e.ID != "Unknown_Mouse_Button"
	}
	return false
}
