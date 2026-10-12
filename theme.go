package main

import (
	"slices"
	"strings"

	ui "github.com/metaspartan/gotui"
)

const defaultTheme = "default"

// theme holds every style the dashboard draws with.
type theme struct {
	base    ui.Style // background fill and borders
	title   ui.Style // RACKWATCH header badge
	label   ui.Style
	value   ui.Style
	warn    ui.Style // values and sparkline bars at or over a threshold
	spark   ui.Style
	muted   ui.Style // stale values and gaps
	host    ui.Style
	up      ui.Style
	stale   ui.Style
	down    ui.Style
	errText ui.Style
}

var themes = map[string]*theme{
	defaultTheme: c64Screen(),
	"brotkasten": brotkasten(),
}

func themeNames() string {
	names := make([]string, 0, len(themes))
	for n := range themes {
		names = append(names, n)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

func rgb(hex uint32) ui.Color {
	return ui.NewRGBColor(int32(hex>>16&0xff), int32(hex>>8&0xff), int32(hex&0xff))
}

// c64Screen is the C64 boot screen: light blue on blue, using the Pepto palette.
func c64Screen() *theme {
	var (
		blue       = rgb(0x352879)
		lightBlue  = rgb(0x6c5eb5)
		cyan       = rgb(0x70a4b2)
		white      = rgb(0xffffff)
		lightGreen = rgb(0x9ad284)
		yellow     = rgb(0xb8c76f)
		red        = rgb(0x68372b)
		lightGrey  = rgb(0x959595)
		grey       = rgb(0x6c6c6c)
	)
	return &theme{
		base:    ui.NewStyle(lightBlue, blue),
		title:   ui.NewStyle(blue, lightBlue, ui.ModifierBold),
		label:   ui.NewStyle(lightGrey, blue),
		value:   ui.NewStyle(lightGreen, blue),
		warn:    ui.NewStyle(yellow, blue, ui.ModifierBold),
		spark:   ui.NewStyle(cyan, blue),
		muted:   ui.NewStyle(grey, blue),
		host:    ui.NewStyle(white, blue, ui.ModifierBold),
		up:      ui.NewStyle(blue, lightGreen, ui.ModifierBold),
		stale:   ui.NewStyle(blue, yellow, ui.ModifierBold),
		down:    ui.NewStyle(white, red, ui.ModifierBold),
		errText: ui.NewStyle(yellow, blue),
	}
}

// brotkasten is the original "breadbin" C64 case: dark brown keycaps with
// cream legends, a beige-brown housing, tan function keys and the red power LED.
func brotkasten() *theme {
	var (
		keycap  = rgb(0x33291f) // dark brown keys, used as background
		housing = rgb(0x9c8f78) // beige-brown case, used for borders
		casing  = rgb(0xc4b597) // lighter case highlight
		fkey    = rgb(0xc9a66b) // tan function keys
		legend  = rgb(0xede3c7) // cream key legends
		label   = rgb(0xb8aa8c)
		amber   = rgb(0xf0b040)
		olive   = rgb(0x9fae6b)
		led     = rgb(0xb0392b) // power LED red
		shadow  = rgb(0x6e6253)
	)
	return &theme{
		base:    ui.NewStyle(housing, keycap),
		title:   ui.NewStyle(keycap, casing, ui.ModifierBold),
		label:   ui.NewStyle(label, keycap),
		value:   ui.NewStyle(legend, keycap),
		warn:    ui.NewStyle(amber, keycap, ui.ModifierBold),
		spark:   ui.NewStyle(fkey, keycap),
		muted:   ui.NewStyle(shadow, keycap),
		host:    ui.NewStyle(legend, keycap, ui.ModifierBold),
		up:      ui.NewStyle(keycap, olive, ui.ModifierBold),
		stale:   ui.NewStyle(keycap, amber, ui.ModifierBold),
		down:    ui.NewStyle(legend, led, ui.ModifierBold),
		errText: ui.NewStyle(amber, keycap),
	}
}
