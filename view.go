package main

import (
	"fmt"
	"image"
	"math"
	"time"

	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui"
)

const (
	labelWidth     = 5
	valueWidth     = 7
	minPanelWidth  = 34
	panelHeight    = 2 + int(metricCount) // border + one row per metric
	sparkGapMarker = '·'
)

var metricLabels = [metricCount]string{"CPU", "RAM", "DISK", "TEMP", "NET"}

// dashboard draws the whole screen: a one-line header and a panel per host.
// Hosts are laid out side by side to suit a wide, short display.
type dashboard struct {
	ui.Block
	cfg   config
	th    *theme
	hosts []*hostState
	now   time.Time
}

func newDashboard(cfg config, hosts []*hostState) *dashboard {
	d := &dashboard{Block: *ui.NewBlock(), cfg: cfg, th: themes[cfg.Dashboard.Theme], hosts: hosts}
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
		if h.status(d.now, d.cfg.Dashboard.StaleAfter.Duration) == statusUp {
			up++
		}
	}
	x := r.Min.X
	x = putString(buf, " RACKWATCH ", d.th.title, x, r.Min.Y, r.Max.X)
	upStyle := d.th.value
	if up < len(d.hosts) {
		upStyle = d.th.warn
	}
	x = putString(buf, "  HOSTS ", d.th.label, x, r.Min.Y, r.Max.X)
	putString(buf, fmt.Sprintf("%d/%d UP", up, len(d.hosts)), upStyle, x, r.Min.Y, r.Max.X)

	right := d.now.Format("15:04:05") + "  q quit  r refresh "
	putString(buf, right, d.th.label, r.Max.X-rw.StringWidth(right), r.Min.Y, r.Max.X)
}

func (d *dashboard) drawHost(buf *ui.Buffer, h *hostState, r image.Rectangle) {
	status := h.status(d.now, d.cfg.Dashboard.StaleAfter.Duration)

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
		drawSparkColumn(buf, s/scale, height, style, pt)
	}
}

// drawSparkColumn draws a bar of the given fraction of height rows, growing up
// from bottom. Each row holds eight eighth-block levels.
func drawSparkColumn(buf *ui.Buffer, frac float64, height int, style ui.Style, bottom image.Point) {
	steps := len(ui.BARS) - 1
	// Always draw at least one eighth so zero still shows a baseline.
	level := max(1, int(clamp(frac, 0, 1)*float64(height*steps)+0.5))
	for row := 0; row < height && level > 0; row++ {
		buf.SetCell(ui.NewCell(ui.BARS[min(level, steps)], style), bottom.Sub(image.Pt(0, row)))
		level -= steps
	}
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
