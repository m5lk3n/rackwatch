package main

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gdamore/tcell/v2"
	ui "github.com/metaspartan/gotui"
)

const defaultTheme = "default"

// theme holds every style the dashboard draws with.
type theme struct {
	// palette, if set, gives RGB values for the 16 color slots the styles use.
	palette *[16]uint32

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
	defaultTheme: defaultDark(),
	"archaic":    archaicDark(),
	"fortuna":    fortunaDark(),
	"gaia":       gaiaDark(),
	"hinterland": hinterlandDark(),
	"noir":       noirDark(),
}

func themeNames() string {
	names := make([]string, 0, len(themes))
	for n := range themes {
		names = append(names, n)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// defaultDark is inspired by VS Code's Dark 2026 theme: light grey text on
// charcoal, faint borders, its steel blue accent for the header and sparklines,
// and its chart colors for status. The colors follow the Prot16 slot layout.
func defaultDark() *theme {
	return prot16Dark([16]uint32{
		0x191a1b, 0xc93c37, 0x86cf86, 0xe0b97f, 0x57a3f8, 0xad80d7, 0x48a0c7, 0x3994bc,
		0x2a2b2c, 0xcd861a, 0x707070, 0x444444, 0x8c8c8c, 0xc39be6, 0xbbbebf, 0xffffff,
	})
}

// archaicDark is the dark variant of Protesilaos Stavrou's Archaic scheme
// (https://protesilaos.com/archaic), earthy colors inspired by ancient wall
// paintings.
func archaicDark() *theme {
	return prot16Dark([16]uint32{
		0x2e2926, 0xab5637, 0x688a38, 0xa97906, 0x4884ad, 0xa25763, 0x368876, 0xeee1c8,
		0x3e3936, 0xc76d26, 0x5e5138, 0x7e7976, 0x9e9178, 0x8a6bb5, 0xaea9a6, 0xfef1d8,
	})
}

// fortunaDark is the dark variant of Protesilaos Stavrou's Fortuna scheme
// (https://protesilaos.com/fortuna), high contrast with bright, playful colors.
func fortunaDark() *theme {
	return prot16Dark([16]uint32{
		0x453731, 0xda5447, 0x78ad28, 0xdb9d0b, 0x3894cd, 0xdc6788, 0x16adac, 0xe2e1df,
		0x554741, 0xdc7926, 0x5d6348, 0x837858, 0x78849c, 0xaa8bee, 0x98b4ac, 0xf2f1ef,
	})
}

// gaiaDark is the dark variant of Protesilaos Stavrou's Gaia scheme
// (https://protesilaos.com/gaia), low-to-medium contrast with an earthy feel.
func gaiaDark() *theme {
	return prot16Dark([16]uint32{
		0x2d2e2f, 0xa67336, 0x7c8e2d, 0xad9f4d, 0x6c9cba, 0xbd9198, 0x5d9d7c, 0xdbddcf,
		0x3d3e3f, 0xa9880b, 0x5b6c4a, 0x786c4d, 0x7b8d9d, 0x8a8db7, 0x94ae9f, 0xebeddf,
	})
}

// hinterlandDark is the dark variant of Protesilaos Stavrou's Hinterland scheme
// (https://protesilaos.com/hinterland), high contrast, inspired by mountain
// ranges and rural regions in early spring.
func hinterlandDark() *theme {
	return prot16Dark([16]uint32{
		0x12171b, 0x983f16, 0x3d814c, 0xb09020, 0x217ab3, 0xad5c5b, 0x01917b, 0xe9e8ec,
		0x22272b, 0x9e6c3d, 0x3d463f, 0x60604f, 0x768592, 0x7d6cab, 0x8aa0a4, 0xf9f8fc,
	})
}

// noirDark is the dark variant of Protesilaos Stavrou's Noir scheme
// (https://protesilaos.com/noir), low contrast, inspired by nights in the
// wilderness. Unlike the other schemes, its slots 7 and 15 are near-black, so
// the styles that would use them take other slots.
func noirDark() *theme {
	th := prot16Dark([16]uint32{
		0x081614, 0xa2744a, 0x358664, 0x76924c, 0x4a78a6, 0xa28d7c, 0x358092, 0x292e28,
		0x182624, 0x90842a, 0x758f74, 0x7c7d59, 0x758c9b, 0x7f7dad, 0x728d8c, 0x191e18,
	})
	c := tcell.PaletteColor
	const bg, red, sand, sage, olive = 0, 1, 5, 10, 11
	th.title = ui.NewStyle(c(bg), c(sand), ui.ModifierBold)
	th.host = ui.NewStyle(c(sage), c(bg), ui.ModifierBold)
	th.muted = ui.NewStyle(c(olive), c(bg))
	th.down = ui.NewStyle(c(bg), c(red), ui.ModifierBold)
	return th
}

// prot16Dark builds a theme from the dark variant of a Prot16 scheme
// (https://github.com/protesilaos/prot16). The schemes share a slot layout, so
// the styles name slots and the palette gives their RGB values.
func prot16Dark(palette [16]uint32) *theme {
	c := tcell.PaletteColor
	const (
		bg, red, green, yellow, cyan, highlight = 0, 1, 2, 3, 6, 7
		orange, muted, text2, label, text, fg0  = 9, 10, 11, 12, 14, 15
	)
	return &theme{
		palette: &palette,
		base:    ui.NewStyle(c(text2), c(bg)),
		title:   ui.NewStyle(c(bg), c(highlight), ui.ModifierBold),
		label:   ui.NewStyle(c(label), c(bg)),
		value:   ui.NewStyle(c(text), c(bg)),
		warn:    ui.NewStyle(c(orange), c(bg), ui.ModifierBold),
		spark:   ui.NewStyle(c(cyan), c(bg)),
		muted:   ui.NewStyle(c(muted), c(bg)),
		host:    ui.NewStyle(c(fg0), c(bg), ui.ModifierBold),
		up:      ui.NewStyle(c(bg), c(green), ui.ModifierBold),
		stale:   ui.NewStyle(c(bg), c(yellow), ui.ModifierBold),
		down:    ui.NewStyle(c(fg0), c(red), ui.ModifierBold),
		errText: ui.NewStyle(c(orange), c(bg)),
	}
}

// forTerminal returns the theme ready to draw with.
//
// On the Linux console, tcell knows only colors 0-7 and would swap 8-15 for
// their nearest dim color; the console shows those as bold instead, so bright
// colors become their dim slot plus bold, and dim colors lose any bold. A
// theme with a palette loads it into the console's slots (setConsolePalette).
//
// Elsewhere, a theme with a palette gets its RGB values directly, since
// terminal emulators have palettes of their own.
func (th *theme) forTerminal(console bool) *theme {
	out := *th
	for _, st := range out.styles() {
		switch {
		case console:
			if st.Fg >= tcell.ColorGray && st.Fg <= tcell.ColorWhite {
				st.Fg -= 8
				st.Modifier |= ui.ModifierBold
			} else {
				st.Modifier &^= ui.ModifierBold
			}
		case th.palette != nil:
			st.Fg, st.Bg = th.rgb(st.Fg), th.rgb(st.Bg)
		}
	}
	return &out
}

func (th *theme) rgb(c ui.Color) ui.Color {
	i := int(c - tcell.ColorBlack)
	if i < 0 || i >= len(th.palette) {
		return c
	}
	p := th.palette[i]
	return ui.NewRGBColor(int32(p>>16&0xff), int32(p>>8&0xff), int32(p&0xff))
}

func (th *theme) styles() []*ui.Style {
	return []*ui.Style{&th.base, &th.title, &th.label, &th.value, &th.warn, &th.spark, &th.muted, &th.host, &th.up, &th.stale, &th.down, &th.errText}
}

// setConsolePalette loads the theme's palette into the Linux console and
// returns a function that restores the console's default palette.
func (th *theme) setConsolePalette(w io.Writer) (restore func()) {
	if th.palette == nil {
		return func() {}
	}
	for i, p := range th.palette {
		fmt.Fprintf(w, "\x1b]P%x%06x", i, p)
	}
	return func() { fmt.Fprint(w, "\x1b]R") }
}
