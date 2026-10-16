package main

import (
	"fmt"
	"image"
	"math"
	"os"
	"regexp"
	"time"

	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui"
)

const (
	labelWidth     = 5
	valueWidth     = 7
	minPanelWidth  = 33
	panelHeight    = 2 + int(metricCount) // border + one row per metric
	sparkGapMarker = '·'
)

const (
	sparkGlyphsAuto    = "auto"
	sparkGlyphsEighths = "eighths"
	sparkGlyphsHalves  = "halves"
	sparkGlyphsLine    = "line"
)

// sparkGlyphSet is how a sparkline column is drawn: glyphs are the levels
// within one row from lowest to highest, and fill puts full rows under the top.
type sparkGlyphSet struct {
	glyphs []rune
	fill   bool
}

// sparkGlyphSets are the ways to draw sparklines. The Linux console's fonts
// lack the eighth blocks (they show as a diamond), so "line" draws only the
// top of each bar with _ - ¯, which every console font has.
var sparkGlyphSets = map[string]sparkGlyphSet{
	sparkGlyphsEighths: {glyphs: ui.BARS[1:], fill: true},
	sparkGlyphsHalves:  {glyphs: []rune{'▄', '█'}, fill: true},
	sparkGlyphsLine:    {glyphs: []rune{'_', '-', '¯'}},
}

// sparkGlyphs resolves the spark_glyphs setting, picking line on the Linux console.
func sparkGlyphs(setting string) sparkGlyphSet {
	if setting == sparkGlyphsAuto {
		setting = sparkGlyphsEighths
		if onLinuxConsole() {
			setting = sparkGlyphsLine
		}
	}
	return sparkGlyphSets[setting]
}

// vtDevice matches the Linux virtual consoles, e.g. /dev/tty1, as opposed to
// the /dev/pts/N of terminal emulators, SSH and tmux.
var vtDevice = regexp.MustCompile(`^/dev/(tty[0-9]+|console)$`)

// onLinuxConsole reports whether output goes to a Linux virtual console. It
// checks the device as well as TERM, since TERM is often set wrongly there.
func onLinuxConsole() bool {
	if os.Getenv("TERM") == "linux" {
		return true
	}
	dev, err := os.Readlink("/proc/self/fd/1")
	return err == nil && vtDevice.MatchString(dev)
}

var metricLabels = [metricCount]string{"CPU", "RAM", "DISK", "TEMP", "NET"}

// dashboard draws the whole screen: a one-line header and a panel per host.
// Hosts are laid out side by side to suit a wide, short display.
type dashboard struct {
	ui.Block
	cfg    config
	th     *theme
	spark  sparkGlyphSet
	hosts  []*hostState
	alerts *alerter // nil without Telegram
	now    time.Time
	// restartArmed shows the restart button asking for the confirming tap.
	restartArmed bool
	// buttons are where the header's buttons were drawn last, for taps.
	buttons []headerButton
}

func newDashboard(cfg config, hosts []*hostState) *dashboard {
	d := &dashboard{Block: *ui.NewBlock(), cfg: cfg, th: themes[cfg.Dashboard.Theme].forTerminal(onLinuxConsole()), spark: sparkGlyphs(cfg.Dashboard.SparkGlyphs), hosts: hosts}
	d.Border = false
	return d
}

func (d *dashboard) Draw(buf *ui.Buffer) {
	r := d.GetRect()
	buf.Fill(ui.NewCell(' ', d.th.base), r)
	if r.Dx() <= 0 || r.Dy() <= 0 {
		return
	}
	d.drawHeader(buf, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+1))

	n := len(d.hosts)
	cols := max(1, min(n, r.Dx()/minPanelWidth))
	rows := (n + cols - 1) / cols
	body := image.Rect(r.Min.X, r.Min.Y+1, r.Max.X, r.Max.Y)
	cellH := max(panelHeight, body.Dy()/rows)
	if d.cfg.Dashboard.Condensed {
		// One row per metric; panels don't stretch to fill the screen.
		cellH = panelHeight
	}
	for i, h := range d.hosts {
		col, row := i%cols, i/cols
		x0 := body.Min.X + col*body.Dx()/cols
		x1 := body.Min.X + (col+1)*body.Dx()/cols
		y0 := body.Min.Y + row*cellH
		if y0 >= body.Max.Y {
			break
		}
		d.drawHost(buf, h, image.Rect(x0, y0, x1, min(y0+cellH, body.Max.Y)))
	}
}

func (d *dashboard) drawHeader(buf *ui.Buffer, r image.Rectangle) {
	up := 0
	for _, h := range d.hosts {
		if h.status(d.now, d.cfg.Dashboard.StaleAfter.Duration, d.cfg.Dashboard.DownAfter.Duration) == statusUp {
			up++
		}
	}
	x := r.Min.X
	x = putString(buf, " RACKWATCH ", d.th.title, x, r.Min.Y, r.Max.X)
	x = putString(buf, " "+version, d.th.label, x, r.Min.Y, r.Max.X)
	upStyle := d.th.value
	if up < len(d.hosts) {
		upStyle = d.th.warn
	}
	restart, restartStyle := "[ restart ]", d.th.label
	if d.restartArmed {
		restart, restartStyle = "[ restart? ]", d.th.warn
	}
	const refresh, gap = "[ refresh ]", "    "
	clock := d.now.Format("15:04:05") + gap
	rightX := r.Max.X - rw.StringWidth(clock+restart+gap+refresh+" ")
	x = putString(buf, "  HOSTS ", d.th.label, x, r.Min.Y, rightX)
	x = putString(buf, fmt.Sprintf("%d/%d UP", up, len(d.hosts)), upStyle, x, r.Min.Y, rightX)
	if d.alerts != nil && d.alerts.lastErr != nil {
		x = putString(buf, "  TELEGRAM ", d.th.label, x, r.Min.Y, rightX)
		putString(buf, shortErr(d.alerts.lastErr), d.th.warn, x, r.Min.Y, rightX-1)
	}
	x = putString(buf, clock, d.th.label, rightX, r.Min.Y, r.Max.X)
	d.buttons = d.buttons[:0]
	for _, b := range []struct {
		label  string
		style  ui.Style
		button touchButton
	}{{restart, restartStyle, buttonRestart}, {refresh, d.th.label, buttonRefresh}} {
		start := x
		x = putString(buf, b.label, b.style, x, r.Min.Y, r.Max.X)
		if x > start {
			d.buttons = append(d.buttons, headerButton{image.Rect(start, r.Min.Y, x, r.Min.Y+1), b.button})
		}
		x = putString(buf, gap, d.th.label, x, r.Min.Y, r.Max.X)
	}
}

func (d *dashboard) drawHost(buf *ui.Buffer, h *hostState, r image.Rectangle) {
	status := h.status(d.now, d.cfg.Dashboard.StaleAfter.Duration, d.cfg.Dashboard.DownAfter.Duration)

	b := ui.NewBlock()
	b.BorderRounded = true
	b.BorderStyle = d.th.base
	b.SetRect(r.Min.X, r.Min.Y, r.Max.X, r.Max.Y)
	b.Draw(buf)
	if r.Dx() < 6 || r.Dy() < 3 {
		return
	}

	// Top border: host name on the left, status badge on the right.
	badge, badgeStyle := d.statusBadge(h, status, d.now)
	badgeX := r.Max.X - 2 - rw.StringWidth(badge)
	putString(buf, " "+h.cfg.Name+" ", d.th.host, r.Min.X+1, r.Min.Y, badgeX-1)
	putString(buf, badge, badgeStyle, badgeX, r.Min.Y, r.Max.X-1)

	// Bottom border: why the last poll failed, if it did.
	if h.lastErr != nil {
		putString(buf, " "+shortErr(h.lastErr)+" ", d.th.errText, r.Min.X+2, r.Max.Y-1, r.Max.X-2)
	}

	// Split the panel into one band per metric; taller bands get taller
	// sparklines. Leftover rows go to the first metrics (CPU, RAM).
	inner := b.Inner
	fresh := status == statusUp
	bandH, extra := inner.Dy()/int(metricCount), inner.Dy()%int(metricCount)
	y := inner.Min.Y
	for k := metricKind(0); k < metricCount && y < inner.Max.Y; k++ {
		height := max(1, bandH)
		if int(k) < extra {
			height++
		}
		y += height
		d.drawMetric(buf, h, k, fresh, inner.Min.X+1, y-1, height, inner.Max.X-1)
	}
}

// drawMetric draws label and value on row y and a sparkline rising height rows from it.
func (d *dashboard) drawMetric(buf *ui.Buffer, h *hostState, k metricKind, fresh bool, x, y, height, maxX int) {
	v := h.current.get(k)
	threshold := d.threshold(k)

	valStyle := d.th.value
	switch {
	case !fresh:
		valStyle = d.th.muted
	case v >= threshold:
		valStyle = d.th.warn
	}
	x = putString(buf, fmt.Sprintf("%-*s", labelWidth, metricLabels[k]), d.th.label, x, y, maxX)
	x = putString(buf, fmt.Sprintf("%*s", valueWidth, formatMetric(k, v)), valStyle, x, y, maxX)

	if !d.cfg.Dashboard.ShowSparklines {
		return
	}
	x++
	width := maxX - x
	if width <= 0 {
		return
	}
	samples := h.history[k].last(width)
	scale := sparkScale(k, samples)
	// Right-align so the newest sample always sits at the panel edge.
	x += width - len(samples)
	for i, s := range samples {
		pt := image.Pt(x+i, y)
		if math.IsNaN(s) {
			buf.SetCell(ui.NewCell(sparkGapMarker, d.th.muted), pt)
			continue
		}
		style := d.th.spark
		switch {
		case !fresh:
			style = d.th.muted
		case s >= threshold:
			style = d.th.warn
		}
		drawSparkColumn(buf, d.spark, s/scale, height, style, pt)
	}
}

// drawSparkColumn draws a bar of the given fraction of height rows, growing up
// from bottom. Each row holds len(set.glyphs) levels.
func drawSparkColumn(buf *ui.Buffer, set sparkGlyphSet, frac float64, height int, style ui.Style, bottom image.Point) {
	steps := len(set.glyphs)
	// Always draw at least one step so zero still shows a baseline.
	level := max(1, int(clamp(frac, 0, 1)*float64(height*steps)+0.5))
	top := (level - 1) / steps
	if set.fill {
		for row := 0; row < top; row++ {
			buf.SetCell(ui.NewCell(set.glyphs[steps-1], style), bottom.Sub(image.Pt(0, row)))
		}
	}
	buf.SetCell(ui.NewCell(set.glyphs[(level-1)%steps], style), bottom.Sub(image.Pt(0, top)))
}

// threshold returns the warning level for a metric, or +Inf if it has none.
func (d *dashboard) threshold(k metricKind) float64 {
	t := d.cfg.Thresholds
	switch k {
	case metricCPU:
		return t.CPUWarning
	case metricMem:
		return t.MemoryWarning
	case metricDisk:
		return t.DiskWarning
	case metricTemp:
		return t.TemperatureWarning
	}
	return math.Inf(1)
}

func (d *dashboard) statusBadge(h *hostState, s hostStatus, now time.Time) (string, ui.Style) {
	switch s {
	case statusUp:
		return " UP ", d.th.up
	case statusStale:
		return fmt.Sprintf(" STALE %s ", now.Sub(h.lastOK).Truncate(time.Second)), d.th.stale
	case statusDown:
		return " DOWN ", d.th.down
	}
	return " ... ", d.th.label
}

// sparkScale picks the value that maps to a full-height bar. Percentages use a
// fixed scale so heights are comparable across hosts; network auto-scales.
func sparkScale(k metricKind, samples []float64) float64 {
	switch k {
	case metricTemp:
		return max(100, maxFinite(samples))
	case metricNet:
		return max(1024, maxFinite(samples))
	}
	return 100
}

func maxFinite(vs []float64) float64 {
	m := 0.0
	for _, v := range vs {
		if !math.IsNaN(v) && v > m {
			m = v
		}
	}
	return m
}

func formatMetric(k metricKind, v float64) string {
	if math.IsNaN(v) {
		return "--"
	}
	switch k {
	case metricTemp:
		return fmt.Sprintf("%.0f°C", v)
	case metricNet:
		return formatRate(v)
	}
	return fmt.Sprintf("%.0f%%", v)
}

func formatRate(bps float64) string {
	units := []string{"B", "K", "M", "G", "T"}
	i := 0
	for bps >= 1000 && i < len(units)-1 {
		bps /= 1024
		i++
	}
	if bps < 10 && i > 0 {
		return fmt.Sprintf("%.1f%s/s", bps, units[i])
	}
	return fmt.Sprintf("%.0f%s/s", bps, units[i])
}

// putString writes s starting at x, clipped at maxX, and returns the next x.
func putString(buf *ui.Buffer, s string, style ui.Style, x, y, maxX int) int {
	for _, r := range s {
		w := rw.RuneWidth(r)
		if x+w > maxX {
			break
		}
		buf.SetCell(ui.NewCell(r, style), image.Pt(x, y))
		x += w
	}
	return x
}
