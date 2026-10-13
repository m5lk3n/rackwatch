package main

import (
	"image"
	"math/rand/v2"
	"time"

	"github.com/gdamore/tcell/v2"
	ui "github.com/metaspartan/gotui"
)

// saverFrame is how often the screensaver animates.
const saverFrame = 80 * time.Millisecond

// matrixPalette colors the screensaver: black, then a near-white head and its
// trail fading from bright to dark green. Only slots 0-7 are drawn with, since
// the Linux console shows 8-15 as bold; those repeat 0-7 so bold changes nothing.
var matrixPalette = [16]uint32{
	0x000000, 0xd8ffd8, 0x5cff7a, 0x00e639, 0x00b82e, 0x008c23, 0x006118, 0x003b0e,
	0x000000, 0xd8ffd8, 0x5cff7a, 0x00e639, 0x00b82e, 0x008c23, 0x006118, 0x003b0e,
}

// matrixGlyphs are the half-width katakana and digits of the film's digital
// rain. Linux console fonts lack katakana, so consoleGlyphs stands in there.
var (
	matrixGlyphs  = []rune("ｦｱｲｳｴｵｶｷｸｹｺｻｼｽｾｿﾀﾁﾂﾃﾄﾅﾆﾇﾈﾉﾊﾋﾌﾍﾎﾏﾐﾑﾒﾓﾔﾕﾖﾗﾘﾙﾚﾛﾜﾝ0123456789Z:.=*+-<>|")
	consoleGlyphs = []rune("0123456789ABCDEFZ:.=*+-<>|$#@%&?")
)

// matrixRain is the screensaver: columns of green glyphs raining down the
// screen, as in The Matrix.
type matrixRain struct {
	ui.Block
	th     *theme
	styles []ui.Style // the head, then the trail from bright to dark
	glyphs []rune
	rng    *rand.Rand
	cells  [][]rune // [x][y]; a few change each frame so the trails flicker
	drops  []drop   // one per column
}

// drop is the falling trail in one column.
type drop struct {
	head  float64 // row of the leading glyph; negative while waiting above the screen
	speed float64 // rows per frame
	trail int     // glyphs including the head
}

func newMatrixRain(console bool) *matrixRain {
	m := &matrixRain{Block: *ui.NewBlock(), th: &theme{palette: &matrixPalette}, glyphs: matrixGlyphs, rng: rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))}
	m.Border = false
	if console {
		m.glyphs = consoleGlyphs
	}
	c := tcell.PaletteColor
	m.styles = []ui.Style{ui.NewStyle(c(1), c(0), ui.ModifierBold)}
	for slot := 2; slot <= 7; slot++ {
		m.styles = append(m.styles, ui.NewStyle(c(slot), c(0)))
	}
	if !console {
		for i, st := range m.styles {
			m.styles[i].Fg, m.styles[i].Bg = m.th.rgb(st.Fg), m.th.rgb(st.Bg)
		}
	}
	return m
}

// fit sizes the rain to the rect, starting it afresh when the size changes.
func (m *matrixRain) fit() {
	r := m.GetRect()
	if len(m.drops) == r.Dx() && (len(m.cells) == 0 || len(m.cells[0]) == r.Dy()) {
		return
	}
	m.cells = make([][]rune, r.Dx())
	m.drops = make([]drop, r.Dx())
	for x := range m.cells {
		m.cells[x] = make([]rune, r.Dy())
		for y := range m.cells[x] {
			m.cells[x][y] = m.glyph()
		}
		m.respawn(x, r.Dy())
	}
}

func (m *matrixRain) glyph() rune { return m.glyphs[m.rng.IntN(len(m.glyphs))] }

// respawn starts a new drop in column x somewhere above the screen, so the
// columns don't all start falling at once.
func (m *matrixRain) respawn(x, height int) {
	m.drops[x] = drop{
		head:  -m.rng.Float64() * float64(2*height),
		speed: 0.3 + 0.7*m.rng.Float64(),
		trail: max(4, height/2+m.rng.IntN(height+1)),
	}
}

// step advances the animation by one frame.
func (m *matrixRain) step() {
	m.fit()
	height := m.GetRect().Dy()
	for x := range m.drops {
		d := &m.drops[x]
		d.head += d.speed
		if int(d.head)-d.trail >= height {
			m.respawn(x, height)
		}
		for y := range m.cells[x] {
			if m.rng.IntN(50) == 0 {
				m.cells[x][y] = m.glyph()
			}
		}
	}
}

func (m *matrixRain) Draw(buf *ui.Buffer) {
	m.fit()
	r := m.GetRect()
	buf.Fill(ui.NewCell(' ', m.styles[len(m.styles)-1]), r)
	for x, d := range m.drops {
		head := int(d.head)
		for y := max(0, head-d.trail+1); y <= min(r.Dy()-1, head); y++ {
			dist := head - y
			style := m.styles[0]
			if dist > 0 {
				style = m.styles[1+(dist-1)*(len(m.styles)-1)/d.trail]
			}
			buf.SetCell(ui.NewCell(m.cells[x][y], style), image.Pt(r.Min.X+x, r.Min.Y+y))
		}
	}
}
