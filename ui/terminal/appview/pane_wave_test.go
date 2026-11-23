package appview

import (
	"fmt"
	"image/color"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/model"
	"github.com/mslxl/natcatty/ui/terminal/effects"
)

// waveRecorder stands in for the ripple's shader: it notes the origin it was
// built with and the box it was applied to, and draws nothing.
type waveRecorder struct {
	origin  ui.Point
	built   bool
	painted bool
	box     ui.Rect
}

func (w *waveRecorder) EffectMargin() ui.Point { return ui.Point{} }

func (w *waveRecorder) PaintEffect(_ *ui.Painter, box ui.Rect, _ *ui.Bitmap, _ ui.Point) {
	w.painted = true
	w.box = box
}

// newWavePane is a pane with a finished command above the live prompt: the live
// grid is a small box at the flow's end, so a wave that stays inside it covers
// only a fraction of the pane.
func newWavePane(t *testing.T) (*terminal.Terminal, *model.Store, Size) {
	t.Helper()
	term, err := terminal.New(terminal.Options{Conn: newNopConn(), Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	t.Cleanup(func() { _ = term.Close() })
	size := Size{Cols: 40, Rows: 8}
	term.Resize(size.Cols, size.Rows)
	term.Feed([]byte("PROMPT> "))
	store := model.NewStore(nil)
	store.Begin(model.Cmd{Line: "echo done", Typed: "echo done"})
	store.Append([]byte("\ndone\r\n"))
	store.End(0)
	return term, store, size
}

// A bell's wave starts at the cursor and covers the whole pane, as Terminal's
// body ripples: the live grid's box moves with the flow, and the wave used to be
// applied to that box alone, leaving every finished block above it still.
func TestPaneBellWaveStartsAtTheCursorAndCoversTheWholePane(t *testing.T) {
	term, store, size := newWavePane(t)
	var scroll ui.ScrollState
	var follow Follow
	var ring effects.Ripple
	bell := Bell{Col: -1, Row: -1}
	hooks := Hooks{Key: "live-a"}

	var spy waveRecorder
	oldWave := paneWave
	paneWave = func(_ *effects.Ripple, _ time.Time, origin ui.Point) ui.Effect {
		spy.origin, spy.built = origin, true
		return &spy
	}
	defer func() { paneWave = oldWave }()

	// Elements expire after their pass, so the pane's box is read as it is
	// built, from the previous frame's layout. The pane sits below a chrome row
	// and beside a rail, as it does in a Session, so an origin left in window
	// coordinates rather than the pane's is off by that chrome and rail.
	var paneBox ui.Rect
	pass := 0
	rekeyEveryPass := false
	tester := ui.NewTester(func(c *ui.Context) {
		pass++
		if rekeyEveryPass {
			hooks.Key = fmt.Sprintf("live-rekey-%d", pass)
		}
		ui.Row(c).Fill().Children(func() {
			ui.Box(c).Width(40).FillHeight()
			ui.Column(c).Grow(1).FillHeight().Children(func() {
				ui.Box(c).Height(30).FillWidth()
				paneBox = Pane(c, store, nil, term, size, nil, &scroll, &follow, &ring, bell,
					Tail{PromptRow: 0, PromptRowKnown: true}, Cursor{}, nil, hooks, func(string) {}).LayoutBounds()
			})
		})
	}, 900, 430)
	tester.SetLocale("en")
	tester.Frame()
	tester.Frame()
	if spy.built {
		t.Fatal("a wave was built before any bell")
	}

	bell = Bell{Fired: true, Col: 2, Row: 0}
	tester.Frame()
	if !spy.built || !spy.painted {
		t.Fatalf("the bell's wave was not drawn (built %v, painted %v)", spy.built, spy.painted)
	}
	bell = Bell{Col: -1, Row: -1}
	// One more frame keeps the layout settled, so the box read while building is
	// the one the wave's frame was painted at.
	tester.Frame()

	// It is applied to the flow's scroll — the whole pane — not to the live grid
	// at the flow's end.
	if spy.box != paneBox {
		t.Fatalf("the wave was applied to %v, want the whole pane %v", spy.box, paneBox)
	}

	// Its centre is the bell's cell — the second column of the live prompt's
	// first row — in the pane's own coordinates, wherever the flow has moved
	// that row.
	prompt, ok := findTextPrefix(tester, "PROMPT>")
	if !ok {
		t.Fatalf("the live prompt is not drawn: %v", tester.Texts())
	}
	cellW, cellH := CellSize()
	want := ui.Point{
		X: prompt.X + 2.5*cellW - paneBox.X,
		Y: prompt.Y + 0.5*cellH - paneBox.Y,
	}
	if dx := spy.origin.X - want.X; dx < -1 || dx > 1 {
		t.Fatalf("the wave starts at x=%v, want the cursor cell's centre %v (pane at %v)", spy.origin.X, want, paneBox)
	}
	if dy := spy.origin.Y - want.Y; dy < -1 || dy > 1 {
		t.Fatalf("the wave starts at y=%v, want the cursor cell's centre %v (pane at %v)", spy.origin.Y, want, paneBox)
	}

	// A live grid that was just re-keyed (a tab switch, a pane focus change) has
	// no layout yet. A tester frame settles through several passes, so the key
	// is changed every pass: the box is new in each, and the wave must stay off
	// rather than be centred on a corner it never had. With a stable key again
	// the box is laid out, and the wave returns to the cursor.
	spy = waveRecorder{}
	rekeyEveryPass = true
	tester.Frame()
	if spy.built {
		t.Fatalf("a wave was drawn from a live grid with no layout yet: origin %v", spy.origin)
	}
	rekeyEveryPass = false
	hooks.Key = "live-settled"
	tester.Frame()
	if !spy.built || !spy.painted {
		t.Fatal("the wave did not come back after the live grid was laid out")
	}
	if dx := spy.origin.X - want.X; dx < -1 || dx > 1 {
		t.Fatalf("after the re-key the wave starts at x=%v, want %v", spy.origin.X, want)
	}
	if dy := spy.origin.Y - want.Y; dy < -1 || dy > 1 {
		t.Fatalf("after the re-key the wave starts at y=%v, want %v", spy.origin.Y, want)
	}
}

// The bell's ring is the pane's, as Terminal draws it (tnBell): it wraps the
// whole pane, including the rows above the live grid, and not just the live
// grid's box.
func TestPaneBellRingCoversTheWholePane(t *testing.T) {
	term, store, size := newWavePane(t)
	oldMotion := ReduceMotion
	ReduceMotion = func(*ui.Context) bool { return true }
	defer func() { ReduceMotion = oldMotion }()

	var scroll ui.ScrollState
	var follow Follow
	var ring effects.Ripple
	bell := Bell{Col: -1, Row: -1}
	var paneBox ui.Rect
	tester := ui.NewTester(func(c *ui.Context) {
		paneBox = Pane(c, store, nil, term, size, nil, &scroll, &follow, &ring, bell,
			Tail{PromptRow: 0, PromptRowKnown: true}, Cursor{}, nil, Hooks{}, func(string) {}).LayoutBounds()
	}, 900, 400)
	tester.Frame()

	// The pane's top edge is above the finished block, outside the live grid.
	x, y := int(paneBox.X+paneBox.W/2), int(paneBox.Y)
	before := tester.Image().RGBAAt(x, y)

	bell = Bell{Fired: true, Col: 2, Row: 0}
	tester.Frame()
	bell = Bell{Col: -1, Row: -1}
	after := tester.Image().RGBAAt(x, y)

	lum := func(c color.RGBA) int { return (int(c.R) + int(c.G) + int(c.B)) / 3 }
	if diff := lum(after) - lum(before); diff > -25 && diff < 25 {
		t.Fatalf("the bell's ring is not on the pane's top edge: %v before, %v after", before, after)
	}
}
