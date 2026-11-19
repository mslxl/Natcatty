package appview

import (
	"math"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens/parse"
)

// The live screen is painted cell by cell: every row is one cell tall and every
// glyph stands in the cell the program wrote it to (col*cellWidth,
// row*cellHeight). The text engine still shapes each run, so fallback fonts,
// ligatures and combining marks look right, but it does not lay the row out as a
// paragraph: no bidi reordering, no line boxes of their own height, and no drawn
// pen positions to invert. A right-to-left letter stays in its logical cell, as
// a terminal stores it, and a wide rune fills the two columns it owns with its
// glyph centered in them. See ADR-0021 for why the whole-row path was rejected.
//
// gridRow builds one screen row as a box painted by paintGridRow. Its Label is
// the row's text, which keeps the flow's accessibility text and the text the
// tests find frame to frame exactly as the RichText rows carried it.
func gridRow(c *ui.Context, spans []ui.Span, size float32) ui.Element {
	text := rowTextOf(spans)
	if text == "" {
		// A blank row still carries one blank, as the RichText rows did, so a
		// flow of blank rows keeps a line for each.
		text = " "
	}
	e := ui.Box(c).FillWidth().Height(cellHeight).Clip().Label(text)
	e.Draw(func(p *ui.Painter, r ui.Rect) {
		paintGridRow(p, r, spans, size)
	})
	return e
}

// paintGridRow paints the runs and backgrounds of one row at their cells.
func paintGridRow(p *ui.Painter, r ui.Rect, spans []ui.Span, size float32) {
	if cellWidth <= 0 || cellHeight <= 0 {
		return
	}
	m := ui.Font{Family: "monospace", Size: size}.Metrics()
	// Center the monospace metrics in the row, as a line box centers its font:
	// cellHeight includes the paragraph's line height, which can be a little
	// taller than the glyphs.
	baseline := (cellHeight-(m.Ascent+m.Descent))/2 + m.Ascent
	col := 0
	for _, sp := range spans {
		runes := []rune(sp.Text)
		if len(runes) == 0 {
			continue
		}
		widths := make([]int, len(runes))
		total := 0
		for i, ru := range runes {
			w := parse.RuneWidth(ru)
			if w < 0 {
				w = 0
			}
			widths[i] = w
			total += w
		}
		if total <= 0 {
			continue
		}
		if sp.Background != (ui.Color{}) {
			p.Fill(ui.Rect{
				X: r.X + float32(col)*cellWidth,
				Y: r.Y,
				W: float32(total) * cellWidth,
				H: cellHeight,
			}, sp.Background, 0)
		}
		paintGridSpan(p, r, sp, runes, widths, col, size, baseline)
		paintGridDeco(p, r, sp, col, total, baseline, size)
		col += total
	}
}

// paintGridSpan shapes a run and places each cluster at its starting column,
// centered in the cells it owns. Glyphs of one cluster (a ligature, a base and
// its marks) keep their places relative to each other, and a fallback glyph
// narrower or wider than a cell is centered in its cell rather than shifting the
// columns after it. A glyph too wide for its cells is drawn smaller, as Ghostty
// draws emoji.
func paintGridSpan(p *ui.Painter, r ui.Rect, sp ui.Span, runes []rune, widths []int, baseCol int, size, baseline float32) {
	font := spanFont(sp, size)
	shaped := shapeSpan(sp.Text, font)
	if len(shaped) == 0 {
		return
	}
	color := sp.Color
	if color == (ui.Color{}) {
		color = p.Theme().Text
	}
	prefix := make([]int, len(runes)+1)
	for i, w := range widths {
		prefix[i+1] = prefix[i] + w
	}
	for i := 0; i < len(shaped); {
		first := shaped[i].Cluster
		if first < 0 {
			first = 0
		}
		if first >= len(runes) {
			first = len(runes) - 1
		}
		col := baseCol + prefix[first]
		last := first
		x0, x1 := shaped[i].X, shaped[i].X+shaped[i].Advance
		j := i
		for {
			g := shaped[j]
			if stop := min(g.Cluster+max(g.Runes, 1), len(runes)); stop-1 > last {
				last = stop - 1
			}
			x0, x1 = min(x0, g.X), max(x1, g.X+g.Advance)
			if j+1 >= len(shaped) {
				break
			}
			next := shaped[j+1].Cluster
			if next < 0 {
				next = 0
			}
			if next >= len(runes) {
				next = len(runes) - 1
			}
			// The next glyph starts another cell, so this cluster has ended.
			if baseCol+prefix[next] != col {
				break
			}
			j++
		}
		group := shaped[i : j+1]
		room := float32(prefix[last+1]-prefix[first]) * cellWidth
		if room <= 0 {
			room = cellWidth
		}
		if x1 > x0 {
			if f := room / (x1 - x0); f < 0.95 {
				small := font
				small.Size = font.Size * max(f, 0.5)
				if glyphs := shapeSpan(string(runes[first:last+1]), small); len(glyphs) > 0 {
					group = glyphs
					x0, x1 = glyphs[0].X, glyphs[0].X+glyphs[0].Advance
					for _, g := range glyphs {
						x0, x1 = min(x0, g.X), max(x1, g.X+g.Advance)
					}
				}
			}
			off := (room - (x1 - x0)) / 2
			if math.Abs(float64(off)) < 0.5/float64(p.Scale()) {
				off = 0
			}
			cellX := r.X + float32(col)*cellWidth
			out := make([]ui.Glyph, len(group))
			for k, g := range group {
				g.X = cellX + off + (g.X - x0)
				out[k] = g
			}
			p.Glyphs(out, 0, r.Y+baseline, color)
		}
		i = j + 1
	}
}

// paintGridDeco draws a run's underline and strikethrough across its cells, so a
// program's rule covers the blanks it wrote it over too.
func paintGridDeco(p *ui.Painter, r ui.Rect, sp ui.Span, col, total int, baseline, size float32) {
	if !sp.Underline && !sp.Strikethrough {
		return
	}
	color := sp.DecorationColor
	if color == (ui.Color{}) {
		color = sp.Color
	}
	if color == (ui.Color{}) {
		color = p.Theme().Text
	}
	thick := sp.DecorationThickness
	if thick <= 0 {
		thick = 1
	}
	x0 := r.X + float32(col)*cellWidth
	w := float32(total) * cellWidth
	if sp.Underline {
		y := r.Y + baseline + 1.5
		if sp.WavyUnderline {
			p.StrokePolyline(wavyLine(x0, x0+w, y), thick, color)
		} else {
			p.Fill(ui.Rect{X: x0, Y: y, W: w, H: thick}, color, 0)
		}
	}
	if sp.Strikethrough {
		m := ui.Font{Family: "monospace", Size: size}.Metrics()
		y := r.Y + baseline - m.Ascent*0.32
		p.Fill(ui.Rect{X: x0, Y: y, W: w, H: thick}, color, 0)
	}
}

// wavyLine is a wavy underline from x0 to x1 at y: the segments of the wave a
// program asked for with SGR 21.
func wavyLine(x0, x1, y float32) [][2]float32 {
	const step = 4.0
	out := make([][2]float32, 0, int((x1-x0)/step)+2)
	up := true
	for x := x0; x <= x1; x += step {
		yy := y
		if up {
			yy = y + 1
		}
		out = append(out, [2]float32{x, yy})
		up = !up
	}
	if last := out[len(out)-1]; last[0] < x1 {
		out = append(out, [2]float32{x1, y})
	}
	return out
}

// shapeKey names one shaped run: the text and every font choice that changes its
// glyphs.
type shapeKey struct {
	text     string
	family   string
	size     float32
	weight   int
	italic   bool
	features string
	lang     string
}

var shapedRuns = struct {
	sync.Mutex
	m map[shapeKey][]ui.Glyph
}{m: make(map[shapeKey][]ui.Glyph)}

// shapeSpan shapes a run once and keeps the glyphs for the frames that draw the
// same text, as ui.Shape caches nothing itself. The returned glyphs are shared:
// callers must not change them in place.
func shapeSpan(text string, f ui.Font) []ui.Glyph {
	k := shapeKey{text: text, family: f.Family, size: f.Size, weight: f.Weight, italic: f.Italic, features: f.Features, lang: f.Lang}
	shapedRuns.Lock()
	glyphs, ok := shapedRuns.m[k]
	shapedRuns.Unlock()
	if ok {
		return glyphs
	}
	glyphs = ui.Shape(text, f)
	shapedRuns.Lock()
	// A screen's worth of text is far below this; clearing keeps a long session
	// of changing output from growing the cache without bound.
	if len(shapedRuns.m) >= 4096 {
		clear(shapedRuns.m)
	}
	shapedRuns.m[k] = glyphs
	shapedRuns.Unlock()
	return glyphs
}
