// Package appview lays the window out the way Terminal does: the terminal's own screen
// is replayed from the plugin's snapshot into ordinary rows, so command cells and
// terminal lines live in one scroll space instead of a terminal pane plus a block
// pane. Terminal draws its own grid for the same reason.
package appview

import (
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
	"github.com/mslxl/natcatty/ui/terminal/blockview"
	"github.com/mslxl/natcatty/ui/terminal/effects"
	"github.com/mslxl/natcatty/ui/terminal/lensview"
)

// Size is the live grid: the terminal is resized to it, and its snapshot is replayed
// at the same size so cursor addressing lands where the program meant it.
type Size struct{ Cols, Rows int }

// Hooks keep application-specific terminal behavior attached while the pane
// owns the visible grid. They are deliberately small: the pane still handles
// VT input, mouse reporting, scrolling and URL hit testing itself.
type Hooks struct {
	Input func(ui.InputEvent) bool
	Drag  func(bool)
	Drop  func([]string)
	// Focused reports whether the live input element held the keyboard after
	// this Pane was built. The Session uses it to distinguish an async flow
	// rebuild from an intentional focus change to Compose or a menu.
	Focused func(bool)
	// Key identifies the live terminal container across frames and terminal
	// recreation (for example Resume). A stable key keeps the text-input
	// element's focus when the flow inserts a finished command block.
	Key any
	// Focus requests the one frame in which the Pane should take keyboard
	// focus. A steady frame leaves focus with controls such as Compose.
	Focus bool
	// Refocus repairs a retained terminal whose input node lost focus while
	// the active pane was rebuilt (for example when a Lens row is inserted).
	// It is only consulted when the node was focused on the previous frame;
	// another control that intentionally took focus therefore keeps it.
	Refocus bool
	// KeepFocus preserves an already focused terminal on every frame. The
	// current box's logical focus is checked before requesting it, so a click
	// into Compose or another control is respected. FocusWithin deliberately
	// does not include native window focus: the logical owner must survive a
	// temporary window blur so the window-focus callback can restore input.
	// Focus() only updates MyGo's in-memory focus id; it does not call the
	// native window focus API.
	KeepFocus bool
}

// Cursor describes the application cursor preference that the native terminal
// widget normally paints for us. The self-drawn grid needs the same information.
type Cursor struct {
	Style terminal.CursorStyle
	Color ui.Color
	Blink bool
}

// Pane draws one flow: each finished command's prompt line and block (or, for a
// command no lens claimed, plain cells), then the live screen as the last thing
// in the scroll.
func Pane(c *ui.Context, store *model.Store, engine *lens.Engine, term *terminal.Terminal, size Size, images []lens.Image, scroll *ui.ScrollState, followState *Follow, ripple *effects.Ripple, bell Bell, tail Tail, cursor Cursor, theme *terminal.Theme, hooks Hooks, onCopy func(string)) ui.Element {
	blocks := store.Snapshot()
	// The position is read when the scroll element is built, so the follow decision
	// has to be made before it (MyGo applies a written Y at that frame's layout).
	followState.Update(scroll)
	// A command no lens claims draws cells where the live grid's rows were. They take
	// the grid's measured row height (last frame's; the font does not change), so a
	// multi-line output keeps its rows instead of shrinking under the prompt.
	blockview.CellRowHeight = cellHeight
	s := ui.Scroll(c).Grow(1).FillWidth()
	if scroll != nil {
		s = s.TrackScroll(scroll)
	}
	var viewportHeight float32
	if r := s.Bounds(); r.W > 0 && r.H > 0 {
		noteAvailable(r.W, r.H)
		viewportHeight = r.H
	}
	s.Children(func() {
		// Keep the live terminal as the viewport-sized last child. Without a
		// finite height on this flow column, the self-drawn input box shrinks to
		// the prompt's rows; clicking the empty terminal area then lands outside
		// the focusable element and the next key appears to be lost.
		col := ui.Column(c).Gap(6).Padding(10).FillHeight()
		col.Children(func() {
			renderHistory := tail.PromptRowKnown
			for i := range blocks {
				// A command that is still running lives in the terminal: its output is
				// the live screen below, so a cell for it would show the same bytes
				// twice. Terminal swaps the cell in when the command ends, which is what
				// ClearOnExit does here (the screen is emptied as the flow gains the
				// cell).
				if !blocks[i].Exited || !renderHistory {
					continue
				}
				// The live grid sits `livePad` inside its rounded background; a
				// finished command's rows replace that grid, so they need the same
				// inset or the prompt jumps left and up the moment the command ends
				// (Terminal draws its grid and its blocks on one left edge). The block's
				// own 10 px body padding then sits inside it, as `.sf-block`'s does.
				cell := ui.Column(c).Padding(livePad, 0, 0, livePad).FillWidth()
				cell.Children(func() {
					blockview.RenderCommand(c, store, engine, &blocks[i], onCopy)
				})
			}
			Live(c, term, size, images, followState, ripple, bell, tail, cursor, theme, viewportHeight, renderHistory && len(blocks) > 0, hooks)
		})
	})
	return s.Label("Terminal")
}

// Live draws the terminal's screen as rows of the flow and takes the keyboard for
// it: typing is translated into bytes the program reads.
// livePad is the pane's padding around its rows: the terminal's background reaches
// it, so the ripple's coordinates include it too.
const livePad = 6

// rippleOrigin is where a cell's centre lies in the pane's own coordinates. The
// shader's positions start at the pane's box corner, which is outside the padding,
// while the cells are counted inside it.
func rippleOrigin(cellX, cellY int) ui.Point {
	return ui.Point{
		X: livePad + (float32(cellX)+0.5)*cellWidth,
		Y: livePad + (float32(cellY)+0.5)*cellHeight,
	}
}

// Bell is one frame's bell: whether one rang this frame, and the cell the cursor was
// at when it did. The cell is taken from a snapshot captured inside the terminal's
// bell callback: sampling the next frame's snapshot can catch the shell's line editor
// mid-repaint, with the cursor already moved to column 0, which put the wave on the
// pane's left edge.
type Bell struct {
	Fired    bool
	Col, Row int
}

// CursorCell is where the cursor is in a terminal snapshot, and whether there is one.
func CursorCell(snapshot []byte, size Size) (col, row int, ok bool) {
	if len(snapshot) == 0 || size.Cols <= 0 || size.Rows <= 0 {
		return 0, 0, false
	}
	sc := parseScreen(snapshot, size.Cols, size.Rows, false)
	_, row, col = sc.Lines()
	return col, row, true
}

// ReduceMotion reports whether the desktop asks for less motion, which Terminal reads as
// prefers-reduced-motion to turn the ripple and its animation off. A variable so a
// test can stand in for the desktop.
var ReduceMotion = func(c *ui.Context) bool { return c.Preferences().ReduceMotion }

// Live draws the terminal's screen as rows of the flow and takes the keyboard for
// it: typing is translated into bytes the program reads. follow is the flow's
// following state: a wheel towards the start detaches it, so the place the reader
// scrolled to stays put (see Follow).
func Live(c *ui.Context, term *terminal.Terminal, size Size, images []lens.Image, follow *Follow, ripple *effects.Ripple, bell Bell, tail Tail, cursor Cursor, theme *terminal.Theme, viewportHeight float32, hasHistory bool, hooks Hooks) {
	if term == nil {
		return
	}
	if len(images) > 0 {
		ui.Column(c).Gap(6).Padding(0, 0, 6, 0).Children(func() {
			for _, im := range images {
				lensview.RenderInto(c, im, "live-image", nil)
			}
		})
	}
	following := follow != nil && follow.Pinned()
	// The cursor blinks, as a terminal's does: half a second on, half off. Asking
	// for an animation frame keeps the frames coming while the view is idle.
	showCursor := cursor.Blink == false || time.Now().UnixMilli()/500%2 == 0
	c.AnimationFrame()
	frame := ScreenFrameWithCursorTheme(term, size, showCursor, cursor, theme)
	from := tailFrom(frame.Lines, frame.CursorRow, tail)
	lines, cursorRow := frame.Lines[from:], frame.CursorRow-from
	// The screen is always as tall as the grid, but the live area is only as tall as
	// what it shows: trailing blank rows would push the blocks above out of view
	// (Terminal's live line is the prompt, not 45 blank rows).
	if last := lastContentRow(lines, cursorRow); last >= 0 && last+1 < len(lines) {
		lines = lines[:last+1]
	}
	// Rows below `from` are the ones the blocks above already show; the live area
	// draws only the tail, so a finished command does not appear twice.
	// The live screen has the selected Natcatty terminal scheme's colours. Its
	// background is the same background the native terminal used to paint.
	fg, bg := TermColors(c, theme)
	applyDefaults(lines, fg)

	// A bell ripples out from the cursor, as Terminal's does: the program rang it, so
	// the wave starts where the cursor is and runs for its second.
	now := c.Now()
	// Terminal turns both the ripple and its animation off when the desktop asks for
	// less motion, and leaves the ring standing while the bell is on.
	reduced := ReduceMotion(c)
	if bell.Fired && ripple != nil {
		col, row := bell.Col, bell.Row-from
		if col < 0 || row < 0 {
			col, row = frame.CursorCol, cursorRow
		}
		ripple.Start(effects.Bell, col, row, now)
	}
	var fx ui.Effect
	var flash float32
	if ripple != nil && ripple.Running(now) {
		age := ripple.Age(now)
		cellX, cellY := ripple.Cell()
		if !reduced {
			fx = ripple.Effect(now, rippleOrigin(cellX, cellY))
		}
		flash = ripple.Flash(age)
		if reduced && flash > 0 {
			flash = 1
		}
		c.AnimationFrame()
	}

	// The padded background is an outer box, so the input coordinates of the rows
	// below are relative to the grid itself (mouse reporting and link clicks need
	// that).
	key := hooks.Key
	if key == nil {
		key = term
	}
	// Grow fills the space left after finished command rows. Keeping a fixed
	// viewport minimum when history blocks precede it makes the live grid consume
	// another full viewport; the unified scroll then pins to the live tail and
	// hides those blocks above the fold. An empty flow still gets the minimum so
	// clicking its blank area focuses the terminal, while a populated flow lets
	// the live child grow into the space that remains after its history.
	outer := ui.Column(c.Key(key)).Gap(0).FillWidth().Grow(1).Background(bg).Radius(4).Padding(livePad)
	if !hasHistory {
		outer.MinHeight(viewportHeight)
	}
	if hooks.Drag != nil {
		hooks.Drag(outer.FileDragOver())
	}
	dropped := outer.DroppedFiles()
	if len(dropped) > 0 && hooks.Drop != nil {
		hooks.Drop(dropped)
	}
	// The whole pane bends, its background and padding too, as Terminal's body does.
	if fx != nil {
		outer.Effect(fx)
	}
	// A file dropped on the pane ripples where the pointer was, as Terminal's drop does.
	if len(dropped) > 0 && ripple != nil && !reduced {
		if ripple.StartDrop(now) {
			c.AnimationFrame()
		}
	}
	outer.Children(func() {
		// The cell measure sits inside this gap-less box: as a child of the flow it
		// would take a column gap of its own and push the first row down 6 px, and
		// that gap vanished the frame a block replaced the live rows (the command
		// jumped up when it ended).
		measureCells(c)
		// Selectable, so the live text can be dragged out with the mouse the way a
		// terminal's output can. The caller requests Focus for one frame when the
		// terminal becomes the active target; steady frames leave focus with the
		// control the user chose, so Compose, menus, and dialogs remain usable.
		//
		// No ScrollIntoView on this box: mygo scrolls the containers around an element
		// "as little as shows it", and called every frame it "would keep the user from
		// scrolling it away" (ui.ScrollIntoView's own doc). Because the live box is
		// taller than the viewport, it pinned the flow to the box's top (Y=22) and the
		// wheel could not move it at all. The cursor row below is what keeps the line
		// being typed visible, and Follow pins the end while the reader is there.
		// Keep printable text on the ordinary terminal input path, exactly as
		// mygo-flow does. Do not add TextCaret here: on Windows that opts the
		// window into the native IME text context. The terminal is a raw key
		// consumer, and MyGo's normal Windows key path already emits InputText.
		// Keep the input node keyed inside the keyed live container. Finished Lens
		// blocks are inserted before this node; without its own key, that sibling
		// insertion changes the node identity and drops keyboard focus even though
		// the live container itself is stable.
		box := ui.Column(c.Key("terminal-input")).Gap(0).FillWidth().FillHeight().Selectable().Focusable()
		logicalFocus := box.FocusWithin()
		if hooks.Focus || (hooks.Refocus && !logicalFocus) ||
			(hooks.KeepFocus && logicalFocus) {
			box.Focus()
		}
		if hooks.Focused != nil {
			hooks.Focused(box.FocusWithin())
		}

		box.HandleInput(func(ev ui.InputEvent) bool {
			if hooks.Input != nil && hooks.Input(ev) {
				return true
			}
			// A wheel towards the start stops the flow following its end, as a
			// terminal's does: the place the reader scrolled to stays put. Reading
			// back at the end follows again (Follow.Update sees it there).
			if ev.Kind == ui.InputScroll && ev.DY < 0 && follow != nil {
				follow.Detach()
			}
			// Where the pointer is, for a drop's ripple: Terminal ripples where the files
			// land, not at the cursor.
			if ripple != nil {
				switch ev.Kind {
				case ui.InputPointerDown, ui.InputPointerUp, ui.InputPointerMove, ui.InputScroll:
					if col, row, ok := cellAt(ev.X, ev.Y); ok {
						ripple.Pointer(col, row)
					}
				}
			}
			// Control-click opens a hyperlink under the pointer, as Terminal does (it
			// takes Command on macOS).
			if ev.Kind == ui.InputPointerDown && ev.Mods&ui.Ctrl != 0 {
				if col, row, ok := cellAt(ev.X, ev.Y); ok {
					if uri, ok := frame.LinkAt(row+from, col); ok {
						c.OpenURL(uri)
						return true
					}
					if row+from < len(frame.Lines) {
						if uri, ok := URLAt(frame.Lines[row+from], col); ok {
							c.OpenURL(uri)
							return true
						}
					}
				}
			}
			// A program that asked for the mouse gets the pointer instead of the
			// flow scrolling under it.
			if frame.Mouse.Enabled() {
				if col, row, ok := cellAt(ev.X, ev.Y); ok {
					if seq := MouseSeq(frame.Mouse, ev, col, row+from); len(seq) > 0 {
						term.Send(seq)
						c.Invalidate()
						return true
					}
				}
			}
			if b := InputBytes(ev); len(b) > 0 {
				term.Send(b)
				c.Invalidate()
				return true
			}
			return false
		})

		if flash > 0 {
			// Terminal's tnBell: an inset ring over the content and a hint of ink,
			// fading out in 0.5 s, and taking no clicks.
			ink, tint := ui.RGBA(0, 0, 0, 0.55*flash), ui.RGBA(0, 0, 0, 0.04*flash)
			if c.Theme().Dark {
				ink, tint = ui.RGBA(255, 255, 255, 0.55*flash), ui.RGBA(255, 255, 255, 0.04*flash)
			}
			ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough().Radius(4).
				Border(1.5, ink).Background(tint)
		}
		box.Children(func() {
			for i, spans := range lines {
				// Keep the line being typed visible while following.
				isCursor := following && i == cursorRow
				if len(spans) == 0 {
					row := ui.Text(c, " ").Font("monospace").SingleLine()
					if isCursor {
						row.ScrollIntoView()
					}
					continue
				}
				row := ui.RichText(c, spans...).SingleLine()
				if isCursor {
					row.ScrollIntoView()
				}
			}
			if showCursor && frame.CursorCol >= 0 && cursorRow >= 0 && cursorRow < len(lines) {
				renderCursor(c, cursor, frame.CursorCol, cursorRow, theme)
			}
		})
	})
}

func renderCursor(c *ui.Context, cursor Cursor, col, row int, theme *terminal.Theme) {
	if cellWidth <= 0 || cellHeight <= 0 {
		return
	}
	color := cursor.Color
	if color == (ui.Color{}) {
		color, _ = TermColors(c, theme)
	}
	left := float32(col) * cellWidth
	top := float32(row) * cellHeight
	width, height := cellWidth, cellHeight
	switch cursor.Style {
	case terminal.CursorBar:
		width = 2
	case terminal.CursorUnderline:
		top += cellHeight - 2
		height = 2
	}
	ui.Box(c).Absolute().Left(left).Top(top).Width(width).Height(height).
		Background(color).PassThrough()
}

// lastContentRow is the last row that has text, or the cursor's row when it is
// below: the live area ends there, not at the bottom of the grid.
func lastContentRow(lines [][]ui.Span, cursorRow int) int {
	last := cursorRow
	for i := len(lines) - 1; i > cursorRow; i-- {
		for _, sp := range lines[i] {
			if strings.TrimSpace(sp.Text) != "" {
				return i
			}
		}
	}
	return last
}

// Tail says which part of the terminal belongs to the live area: Terminal turns a
// finished command's rows into its block, so the pane must not draw them again.
// PromptRow is supplied by the shell integration; Running is the command whose
// output is still streaming ("" when nothing runs).
type Tail struct {
	Running string
	// PromptRow is the screen row where the shell's current prompt run starts.
	// It comes from the shell integration's OSC 133;B boundary, so arbitrary
	// prompt text does not need to be inspected.
	PromptRow      int
	PromptRowKnown bool
}

// tailFrom is the first screen row the live area shows: the prompt line when idle,
// the running command's own line while it streams, and the cursor row when neither
// can be found.
func tailFrom(lines [][]ui.Span, cursorRow int, tail Tail) int {
	at := func(i int) string {
		var b strings.Builder
		for _, sp := range lines[i] {
			b.WriteString(sp.Text)
		}
		return b.String()
	}
	if cursorRow >= len(lines) {
		cursorRow = len(lines) - 1
	}
	if cursorRow < 0 {
		return 0
	}
	// Without an exact shell-integration boundary, the terminal is rendered as
	// one raw screen. Cropping it would require guessing from prompt text and
	// can hide real rows or duplicate Lens history.
	if !tail.PromptRowKnown {
		return 0
	}
	if tail.Running != "" {
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.Contains(strings.TrimRight(at(i), " "), tail.Running) {
				return i
			}
		}
	}
	// A shell integration can tell us the prompt boundary without exposing the
	// prompt text at all. Prompts may contain timestamps, colors, random IDs, or
	// no conventional marker.
	if tail.PromptRowKnown {
		if row := tail.PromptRow; row >= 0 && row < len(lines) {
			return row
		}
	}
	// Without an exact OSC 133 prompt boundary there is no safe way to infer
	// which rows belong to the live shell. Keep the terminal cursor row as the
	// conservative boundary; never inspect or guess from prompt text.
	return cursorRow
}

// Screen replays the terminal's snapshot at the given size.
// Frame is one replay of the live screen: its rows, where the cursor is, and the
// pointer reporting the program asked for.
type Frame struct {
	Lines     [][]ui.Span
	CursorRow int
	CursorCol int
	Mouse     mouseMode

	sc *screen
}

// ScreenFrame replays the terminal's snapshot at the given size.
func ScreenFrame(term *terminal.Terminal, size Size, showCursor bool) Frame {
	return ScreenFrameWithCursor(term, size, showCursor, Cursor{})
}

// ScreenFrameWithCursor replays the terminal snapshot and applies the
// application's cursor color/style to the cursor cell.
func ScreenFrameWithCursor(term *terminal.Terminal, size Size, showCursor bool, cursor Cursor) Frame {
	return ScreenFrameWithCursorTheme(term, size, showCursor, cursor, nil)
}

// ScreenFrameWithCursorTheme replays the terminal snapshot using the supplied
// Natcatty terminal theme for default colors and ANSI palette.
func ScreenFrameWithCursorTheme(term *terminal.Terminal, size Size, showCursor bool, cursor Cursor, theme *terminal.Theme) Frame {
	if term == nil {
		return Frame{}
	}
	sc := parseScreenWithTheme(term.Snapshot(), size.Cols, size.Rows, showCursor, cursor, theme)
	lines, row, col := sc.Lines()
	return Frame{Lines: lines, CursorRow: row, CursorCol: col, Mouse: sc.Mouse(), sc: sc}
}

// LinkAt returns the hyperlink of a cell of the replayed screen.
func (f Frame) LinkAt(row, col int) (string, bool) {
	if f.sc == nil {
		return "", false
	}
	return f.sc.LinkAt(row, col)
}

// Screen is ScreenFrame without the modes, for callers that only draw.
func Screen(term *terminal.Terminal, size Size, showCursor bool) (lines [][]ui.Span, cursorRow, cursorCol int) {
	f := ScreenFrame(term, size, showCursor)
	return f.Lines, f.CursorRow, f.CursorCol
}

// PromptText is the prompt this app draws for a directory; the prompt row above
// each block draws it (see model.PromptText), outside the block's chrome.
func PromptText(dir string) string { return model.PromptText(dir) }
