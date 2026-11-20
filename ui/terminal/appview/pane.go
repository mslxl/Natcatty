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
	// OwnView reports, every frame, whether it was drawn by the terminal's own
	// view rather than the pane's cell grid (see Images), and whether the
	// replay is a full-screen program's alternate screen. That view sizes the
	// terminal to its own box, so nothing else may size it too, and leaving
	// the screen is when the pictures that asked for it are done.
	OwnView func(pictures, alt bool)
	// Images reports that the running full-screen program draws pictures with
	// the graphics protocol. A terminal snapshot holds text alone, so the pane's
	// cell grid can replay a program's screen but never its pictures: a program
	// that placed one is drawn with the terminal's own view instead. Other
	// full-screen programs keep the cell grid, with the pane's selection,
	// scrolling and mouse handling.
	Images bool
	// Key identifies the live terminal container across frames and terminal
	// recreation (for example Resume). A stable key keeps the text-input
	// element's focus when the flow inserts a finished command block.
	Key any
	// Click is called when the pointer goes down on a finished command's row
	// or block. A Lens body (selection, a section toggle, the Native/Raw
	// switch) must not own the keyboard: Terminal's flow is still the shell's
	// input surface, so the Session requests the terminal's focus again.
	Click func()
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
	// TextInput is the terminal's connection to the system's input methods: a
	// committed composition is text the program reads, and a marked one is
	// only drawn by the system's candidate window. It must stay the same
	// pointer across frames, because MyGo ends a composition when the client
	// changes, and it should send through whatever terminal is attached now
	// (Resume replaces it). See takesPlatformTextInput for why a terminal
	// declares text input at all.
	TextInput ui.TextInputClient
	// Selection is the live screen's text selection, dragged with the pointer
	// and copied with the platform's Copy gesture. The caller owns it because
	// the Pane is rebuilt every frame. It is nil when the caller offers no
	// selection.
	Selection *Selection
	// OpenLink is asked to open the link a Command+click names in the live
	// screen: an OSC 8 hyperlink, a URL printed as text, or a printed file
	// path. It returns whether it took the link. A URL it declines opens in
	// the system's browser; a file path it declines opens nothing, as
	// Terminal's Options.OpenLink leaves paths to the app. Nil opens every
	// URL and no path.
	OpenLink func(link string, path bool) bool
	// Hover is the link Command is held over in the live screen. The caller
	// owns it because the Pane is rebuilt every frame: the Pane writes it as
	// the pointer moves and reads it to underline the link. Nil keeps the
	// underline off and nothing else.
	Hover *Hover
}

// Hover is the link the pointer with Command held rests over: what the link
// opens, whether it is a file path, and the cells' rectangle in DIPs relative
// to the Pane. A zero Hover is no link.
type Hover struct {
	Link string
	Path bool
	At   ui.Rect
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
// flowPadY is the flow's vertical margin inside the scroll container. It is
// the scroll element's padding, so it frames the viewport rather than adding
// to what is scrolled: a viewport with nothing to scroll reports MaxY 0.
const flowPadY = 10

func Pane(c *ui.Context, store *model.Store, engine *lens.Engine, term *terminal.Terminal, size Size, images []lens.Image, scroll *ui.ScrollState, followState *Follow, ripple *effects.Ripple, bell Bell, tail Tail, cursor Cursor, theme *terminal.Theme, hooks Hooks, onCopy func(string)) ui.Element {
	blocks := store.Snapshot()
	// Measure the cell before anything is built: the rows, the cursor and the
	// pointer all use it, and MeasureText lays the text out in this same frame.
	measureCell(c)
	// Replay the live screen once, before the layout: a full-screen program
	// that placed a picture is drawn by the terminal's own view (see Live), and
	// that view paints the program's background itself. The flow's padding ring
	// would leave a strip of the pane's colour around it, so the program gets
	// the whole pane instead.
	showCursor := cursor.Blink == false || time.Now().UnixMilli()/500%2 == 0
	frame := ScreenFrameWithCursorTheme(term, size, showCursor, cursor, theme)
	if !frame.CursorVisible() {
		showCursor = false
	}
	pictures := frame.AltScreen() && frame.sc != nil && hooks.Images
	// The position is read when the scroll element is built, so the follow decision
	// has to be made before it (MyGo applies a written Y at that frame's layout).
	followState.Update(scroll)
	// A command no lens claims draws cells where the live grid's rows were. They take
	// the grid's measured row height, the same cell every live row has.
	blockview.CellRowHeight = cellHeight
	// The flow's breathing room belongs to the scroll container, not to the
	// content it scrolls. With it on the inner column the content was always
	// 20 DIP taller than the viewport, even for a single prompt: the flow
	// could scroll nowhere yet showed a scrollbar and, worse, the pinned end
	// (MaxY) and the offset the layout allowed disagreed by those 20 DIP, so
	// Follow decided the reader had scrolled up and stopped following the
	// live tail.
	// A full-screen program draws its own screen, background included. Keeping
	// the flow's vertical margin around it left a strip of the pane's colour
	// above and below the program's (yazi's black), so that view gets the whole
	// pane and the scroll drops the margin.
	s := ui.Scroll(c).Grow(1).FillWidth()
	if !pictures {
		s = s.PaddingY(flowPadY)
	}
	if scroll != nil {
		s = s.TrackScroll(scroll)
	}
	// The flow's scroll is the innermost input handler for the finished
	// command rows (the live box below has its own handler), so a press on a
	// block reaches this before the widget asks about its click. Report it
	// and leave the event alone: the block still selects and its buttons still
	// work, and the Session gives the keyboard back to the shell.
	if hooks.Click != nil {
		s.HandleInput(func(ev ui.InputEvent) bool {
			// Only the primary button: a secondary press opens the text menu
			// and must leave the keyboard where it was.
			if ev.Kind == ui.InputPointerDown && ev.Button == 0 {
				hooks.Click()
			}
			return false
		})
	}
	// The viewport is the scroll's content box: its padding is the flow's
	// margin, outside the part the content is measured against. Measuring the
	// border box here made the live grid one row taller than the space that
	// exists, which put the last row (where the prompt and the typed line sit)
	// below the flow's reachable end.
	var viewport ui.Rect
	if r := s.LayoutBounds(); r.W > 0 {
		if pictures {
			// The program's view paints its own background: it is the whole
			// scroll box, margins included, so that background reaches the
			// pane's edges.
			noteAvailable(r.W, r.H-2*flowPadY)
			viewport = r
		} else if r.H > 2*flowPadY {
			noteAvailable(r.W, r.H-2*flowPadY)
			viewport = ui.Rect{X: r.X, Y: r.Y + flowPadY, W: r.W, H: r.H - 2*flowPadY}
		}
	}
	s.Children(func() {
		// Keep the live terminal as the viewport-sized last child. Without a
		// finite height on this flow column, the self-drawn input box shrinks to
		// the prompt's rows; clicking the empty terminal area then lands outside
		// the focusable element and the next key appears to be lost.
		col := ui.Column(c).Gap(6).FillHeight()
		if !pictures {
			col = col.PaddingX(flowPadY)
		}
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
					blockview.RenderCommand(c, store, engine, &blocks[i], viewport, onCopy)
				})
			}
			Live(c, frame, showCursor, term, size, images, followState, ripple, bell, tail, cursor, theme, viewport.H, renderHistory && len(blocks) > 0, hooks)
		})
	})
	return s.Label("Terminal")
}

// Live draws the terminal's screen as rows of the flow and takes the keyboard for
// it: typing is translated into bytes the program reads.
// livePad is the pane's padding around its rows: the terminal's background reaches
// it, so the ripple's coordinates include it too.
const livePad = 6

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

// CursorReplayRow is where the cursor is counted from the replay's first row,
// including the rows the replay cap dropped above the grid, and whether the
// snapshot is an alternate screen. Unlike CursorCell's row, this one keeps
// naming the same line while output pushes the capped window down: it is what
// the shell integration records at OSC 133;B, since the prompt sits still
// while the rows above it scroll out of the replay.
func CursorReplayRow(snapshot []byte, size Size) (row int, alt, ok bool) {
	if len(snapshot) == 0 || size.Cols <= 0 || size.Rows <= 0 {
		return 0, false, false
	}
	sc := parseScreen(snapshot, size.Cols, size.Rows, false)
	_, row, _ = sc.Lines()
	return row + sc.dropped, sc.alt, true
}

// ReduceMotion reports whether the desktop asks for less motion, which Terminal reads as
// prefers-reduced-motion to turn the ripple and its animation off. A variable so a
// test can stand in for the desktop.
var ReduceMotion = func(c *ui.Context) bool { return c.Preferences().ReduceMotion }

// Live draws the terminal's screen as rows of the flow and takes the keyboard for
// it: typing is translated into bytes the program reads. follow is the flow's
// following state: a wheel towards the start detaches it, so the place the reader
// scrolled to stays put (see Follow). The caller replays the screen (frame) and
// decides the cursor blink once, before laying the flow out: whether a full-screen
// program placed a picture decides how much of the pane the live view takes, so
// the same replay has to be known before the layout is built.
func Live(c *ui.Context, frame Frame, showCursor bool, term *terminal.Terminal, size Size, images []lens.Image, follow *Follow, ripple *effects.Ripple, bell Bell, tail Tail, cursor Cursor, theme *terminal.Theme, viewportHeight float32, hasHistory bool, hooks Hooks) {
	if term == nil {
		return
	}
	measureCell(c)
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
	c.AnimationFrame()
	// A selection is a range of replay rows. Switching between the main and
	// alternate screens puts different text on those rows, so a selection made
	// on one screen must not survive into the other: copying it would take the
	// wrong characters.
	if sel := hooks.Selection; sel != nil {
		sel.SyncScreen(frame.AltScreen())
	}
	// A full-screen program owns the alternate screen and draws a screen of its
	// own. The cell grid below replays the terminal's text, and a terminal
	// snapshot carries no image placements, so pictures the program paints with
	// the graphics protocol would be invisible there (yazi's preview). A
	// program that placed one gets the plugin's own view, which draws the
	// screen with its pictures, its cursor and its scrolling as Terminal does;
	// every other full-screen program keeps the pane's replay.
	pictures := frame.AltScreen() && frame.sc != nil && hooks.Images
	if hooks.OwnView != nil {
		hooks.OwnView(pictures, frame.AltScreen())
	}
	if pictures {
		_, bg := TermColors(c, theme)
		key := hooks.Key
		if key == nil {
			key = term
		}
		// No rounded padding here: the terminal's own view paints the program's
		// background across the box it is given, and a full-screen program
		// expects that background to reach the pane's edges (yazi paints the
		// whole screen black; a ring of the flow's colour around it is wrong).
		outer := ui.Column(c.Key(key)).Gap(0).FillWidth().Grow(1).Background(bg)
		if !hasHistory {
			outer.MinHeight(viewportHeight)
		}
		if hooks.Drag != nil {
			hooks.Drag(FileDragOver(outer))
		}
		if dropped := DroppedFiles(outer); len(dropped) > 0 && hooks.Drop != nil {
			hooks.Drop(dropped)
		}
		outer.Children(func() {
			view := terminal.View(c, term).Height(viewportHeight)
			// The same focus policy as the cell grid below: take the one
			// frame a caller asks for, and keep the keyboard while it is
			// already here. The view is a different element from the grid's
			// input box, so a switch between them loses focus; the caller
			// asks for it again on the frame that switches (OwnView).
			logicalFocus := view.FocusWithin()
			if hooks.Focus || (hooks.Refocus && !logicalFocus) ||
				(hooks.KeepFocus && logicalFocus) {
				view.Focus()
			}
			if hooks.Focused != nil {
				hooks.Focused(view.FocusWithin())
			}
		})
		return
	}
	// PromptRow is an absolute row of this replay: the boundary mirror keeps
	// the same scrollback, so its recorded row indexes the same line, and the
	// frame's dropped count is how far the replay cap moved the window above
	// it. Subtracting it keeps the crop on the prompt while output scrolls the
	// window down without a new OSC 133;B (typing a wrapped command after a
	// long scrollback). Deriving the row from the cursor instead moved the live
	// area onto the prompt's last line whenever the prompt had more than one,
	// which hid the rest of the prompt and drew the cursor above the line being
	// typed.
	if tail.PromptRowKnown && frame.sc != nil {
		tail.PromptRow -= frame.sc.dropped
	}
	from := tailFrom(frame.Lines, frame.CursorRow, tail)
	if frame.AltScreen() {
		// A full-screen program (vim, less, nano, emacs, a pager) owns the
		// alternate screen, whose snapshot carries that screen alone, with no
		// scrollback. The shell integration's prompt row and a running
		// command's echo name rows of the main screen that are not in this
		// replay: cropping to them cut the program's screen off above the
		// cursor, so the rows below it were all that was drawn and typing on
		// them ran out of the window with nothing to scroll back to. The
		// program draws the whole screen itself, so the live area is the
		// whole replay.
		from = 0
		bell.Col, bell.Row = -1, -1
	}
	lines, cursorRow := frame.Lines[from:], frame.CursorRow-from
	// A mirror/visible desync (a clear-screen, a replay) can put the cursor
	// outside the cropped rows; lastContentRow indexes lines and must not see
	// a negative row.
	if cursorRow < 0 {
		cursorRow = 0
	}
	if cursorRow >= len(lines) {
		cursorRow = len(lines) - 1
	}
	// The screen is always as tall as the grid, but the live area is only as tall as
	// what it shows: trailing blank rows would push the blocks above out of view
	// (Terminal's live line is the prompt, not 45 blank rows).
	if last := lastContentRow(lines, cursorRow); last >= 0 && last+1 < len(lines) {
		lines = lines[:last+1]
	}
	// The selection is the Pane's own (see Selection): the live rows carry it
	// as a background, the way Terminal fills its selection behind the text.
	if sel := hooks.Selection; sel != nil && !sel.Empty() && frame.sc != nil {
		lines = frame.SelectedLines(from, len(lines), *sel, SelectionColor(c, theme))
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
	var flash float32
	if ripple != nil && ripple.Running(now) {
		age := ripple.Age(now)
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
		hooks.Drag(FileDragOver(outer))
	}
	dropped := DroppedFiles(outer)
	if len(dropped) > 0 && hooks.Drop != nil {
		hooks.Drop(dropped)
	}
	// The pointer names a grid cell: the rows are painted one measured cell
	// tall, so row and column arithmetic is exact whatever glyph the program
	// wrote in a cell.
	rowSize := float32(0)
	if uiTheme := c.Theme(); uiTheme != nil {
		rowSize = uiTheme.FontSize
	}
	// linkUnder resolves the link at a cell of the live rows: an OSC 8
	// hyperlink, a URL printed as text, or, when the caller opens paths, a
	// printed file path. It returns the cells the link spans, x0 up to x1, so
	// the pane can underline exactly those and the caller can preview them.
	linkUnder := func(row, col int) (link string, path bool, x0, x1 int) {
		global := row + from
		if uri, ok := frame.LinkAt(global, col); ok && uri != "" {
			x0, x1 = col, col+1
			for x0 > 0 {
				if prev, ok := frame.LinkAt(global, x0-1); !ok || prev != uri {
					break
				}
				x0--
			}
			for x1 < size.Cols {
				if next, ok := frame.LinkAt(global, x1); !ok || next != uri {
					break
				}
				x1++
			}
			return uri, false, x0, x1
		}
		if row < 0 || row >= len(lines) {
			return "", false, 0, 0
		}
		if uri, x0, x1, ok := urlAtRow(lines[row], col); ok {
			return uri, false, x0, x1
		}
		if hooks.OpenLink != nil {
			if p, x0, x1, ok := pathAtRow(lines[row], col); ok {
				return p, true, x0, x1
			}
		}
		return "", false, 0, 0
	}
	// setHover recomputes the link Command is held over from a point in the
	// grid's own coordinates, records it for the caller and the underline, and
	// returns what a press at that point would open. A program that takes the
	// mouse keeps Command for itself, and a drag that is selecting keeps the
	// press, so neither offers a link.
	setHover := func(mods ui.Modifiers, x, y float32) (string, bool) {
		next := Hover{}
		if mods&ui.Cmd != 0 && !frame.Mouse.Enabled() &&
			!(hooks.Selection != nil && hooks.Selection.Dragging()) {
			if col, row, ok := pointerCell(lines, x, y, size.Cols); ok {
				if link, path, x0, x1 := linkUnder(row, col); link != "" {
					next = Hover{
						Link: link,
						Path: path,
						At: ui.Rect{
							// The live grid sits flowPadY inside the Pane and
							// livePad inside that box, which is the origin the
							// grid's input coordinates are relative to.
							X: flowPadY + livePad + float32(x0)*cellWidth,
							Y: flowPadY + livePad + float32(row)*cellHeight,
							W: float32(x1-x0) * cellWidth,
							H: cellHeight,
						},
					}
				}
			}
		}
		if hooks.Hover != nil && next != *hooks.Hover {
			*hooks.Hover = next
			c.Invalidate()
		}
		return next.Link, next.Path
	}
	// Command is read every frame, so pressing or releasing it with the
	// pointer still underlines or un-underlines the link, as Terminal does.
	if hooks.Hover != nil && !frame.AltScreen() {
		if x, y, over := outer.PointerPosition(); over {
			setHover(c.Modifiers(), x-livePad, y-livePad)
		} else if hooks.Hover.Link != "" {
			setHover(0, 0, 0)
		}
	}
	outer.Children(func() {
		// Not Selectable: MyGo paints and copies a paragraph selection only
		// while that paragraph holds the keyboard, and this box must hold it
		// for typing to reach the program. The live text is selected by the
		// Pane instead (see Selection), which keeps both. The caller requests
		// Focus for one frame when the terminal becomes the active target;
		// steady frames leave focus with the control the user chose, so
		// Compose, menus, and dialogs remain usable.
		//
		// No ScrollIntoView on this box: mygo scrolls the containers around an element
		// "as little as shows it", and called every frame it "would keep the user from
		// scrolling it away" (ui.ScrollIntoView's own doc). Because the live box is
		// taller than the viewport, it pinned the flow to the box's top (Y=22) and the
		// wheel could not move it at all. The cursor row below is what keeps the line
		// being typed visible, and Follow pins the end while the reader is there.
		// Keep printable text on the terminal's own input path: MyGo delivers it
		// as InputText and InputBytes forwards it to the program. Where the
		// window only interprets keys into text for an element that declares a
		// text input (AppKit), the keyboard would otherwise deliver key downs
		// and no text at all, so the Session supplies an input client there.
		// Windows and GTK send the layout's characters without one and keep
		// their ordinary path, which is why the client is platform-gated rather
		// than added on every platform as the native terminal view's TextCaret is.
		//
		// Keep the input node keyed inside the keyed live container. Finished Lens
		// blocks are inserted before this node; without its own key, that sibling
		// insertion changes the node identity and drops keyboard focus even though
		// the live container itself is stable.
		box := ui.Column(c.Key("terminal-input")).Gap(0).FillWidth().FillHeight().Focusable()
		if client := hooks.TextInput; client != nil && takesPlatformTextInput {
			box = box.HandleTextInput(client)
		}
		logicalFocus := box.FocusWithin()
		if hooks.Focus || (hooks.Refocus && !logicalFocus) ||
			(hooks.KeepFocus && logicalFocus) {
			box.Focus()
		}
		if hooks.Focused != nil {
			hooks.Focused(box.FocusWithin())
		}

		box.HandleInput(func(ev ui.InputEvent) bool {
			// A platform may commit the characters a key types separately
			// from its key down. A key this package already encoded must not
			// send those characters too: the duplicate would land after
			// whatever the key did. Tell the text-input client which keys
			// carry their own text; the InputText path below and the
			// client's ReplaceText both drop the duplicate.
			if echo, ok := hooks.TextInput.(keyEcho); ok {
				switch ev.Kind {
				case ui.InputKeyDown:
					_, encoded := keyBytes(ev)
					echo.NoteKey(encoded)
				case ui.InputKeyUp:
					echo.NoteKeyUp()
				case ui.InputText:
					if echo.TakeEcho() {
						return true
					}
				}
			}
			if hooks.Input != nil && hooks.Input(ev) {
				return true
			}
			// A pointer event names one cell, whatever the handler below does
			// with it; resolving it once keeps the five paths agreeing.
			var col, row int
			cellOK := false
			switch ev.Kind {
			case ui.InputPointerDown, ui.InputPointerUp, ui.InputPointerMove, ui.InputScroll:
				col, row, cellOK = pointerCell(lines, ev.X, ev.Y, size.Cols)
			}
			// A wheel towards the start stops the flow following its end, as a
			// terminal's does: the place the reader scrolled to stays put. Reading
			// back at the end follows again (Follow.Update sees it there).
			if ev.Kind == ui.InputScroll && ev.DY < 0 && follow != nil {
				follow.Detach()
			}
			// Where the pointer is, for a drop's ripple: Terminal ripples where the files
			// land, not at the cursor.
			if ripple != nil && cellOK {
				ripple.PointerAt(col, row, ui.Point{X: livePad + ev.X, Y: livePad + ev.Y})
			}
			// Command over a link underlines it and a click opens it, before a
			// program that takes the mouse sees the press (setHover offers no
			// link while the program tracks the mouse, so the report below
			// keeps that press). A printed archive name goes to the caller's
			// OpenLink; a URL it does not take opens in the system browser,
			// and a file path it declines opens nothing, as Terminal does.
			if ev.Kind == ui.InputPointerDown || ev.Kind == ui.InputPointerMove {
				link, path := setHover(ev.Mods, ev.X, ev.Y)
				if ev.Kind == ui.InputPointerDown && ev.Button == 0 && link != "" {
					if open := hooks.OpenLink; open == nil || !open(link, path) {
						if !path {
							c.OpenURL(link)
						}
					}
					return true
				}
			}
			// A program that asked for the mouse gets the pointer instead of the
			// flow scrolling under it. Shift is the terminal's own selection
			// gesture, as in Terminal and Ghostty: it skips the program, so text
			// can be copied out of vim or nano, which report the mouse.
			if frame.Mouse.Enabled() && !(ev.Mods&ui.Shift != 0 && ev.Kind != ui.InputScroll) && cellOK {
				// The program counts rows from its own screen, not from
				// the replay's first scrollback row: a main-screen program
				// with mouse reporting would otherwise receive a row
				// shifted by the scrollback above it.
				screenRow := row + from
				if frame.sc != nil {
					screenRow -= frame.sc.top
				}
				if seq := MouseSeq(frame.Mouse, ev, col, screenRow); len(seq) > 0 {
					term.Send(seq)
					c.Invalidate()
					return true
				}
			}
			// The live screen's selection is the Pane's own (see Selection):
			// a program that asked for the mouse keeps the pointer, and the
			// modifier that extends a selection is offered when none exists.
			if sel := hooks.Selection; sel != nil {
				switch ev.Kind {
				case ui.InputPointerDown:
					// Shift is the Pane's selection gesture even while a
					// program asks for the mouse: the report above skipped it.
					// Without the modifier, a program that reports the mouse
					// keeps the pointer, as in Terminal and Ghostty.
					if ev.Button == 0 && (ev.Mods&ui.Shift != 0 || !frame.Mouse.Enabled()) && cellOK {
						switch {
						case ev.Clicks >= 3:
							sel.SelectLine(frame.sc, from+row)
						case ev.Clicks == 2:
							sel.SelectWord(frame.sc, from+row, col)
						case ev.Mods&ui.Shift != 0 && !sel.Empty():
							sel.Drag(from+row, col)
						default:
							sel.Start(from+row, col)
						}
						c.Invalidate()
						return true
					}
				case ui.InputPointerMove:
					if sel.Dragging() {
						if cellOK {
							if from+row != sel.Caret().Row || col != sel.Caret().Col {
								sel.Drag(from+row, col)
								c.Invalidate()
							}
						}
						return true
					}
				case ui.InputPointerUp:
					if sel.Dragging() {
						sel.Release()
						c.Invalidate()
						return true
					}
				case ui.InputKeyDown, ui.InputCommand:
					if SelectAllKey(ev) {
						sel.SelectAll(frame.sc)
						c.Invalidate()
						return true
					}
					if CopyKey(ev) {
						if text := frame.SelectionText(*sel); text != "" {
							// An input handler runs outside the frame, where
							// Context.WriteClipboard does nothing; Services
							// reaches the window's clipboard at any time.
							c.Services().WriteClipboard(text)
							c.Invalidate()
							return true
						}
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
			// Every row is one measured cell tall and starts at its index times
			// that cell. A row holding a fallback glyph (an emoji, a Nerd Font
			// icon) no longer lays out taller than the others: the glyph is
			// centered in its cell, so the rows below, the pointer mapping and
			// the cursor stay on the grid.
			cursorShown := showCursor && frame.CursorCol >= 0 && cursorRow >= 0 && cursorRow < len(lines)
			if cursorShown {
				cursor.Style = frame.CursorStyle(cursor.Style)
			}
			for i, spans := range lines {
				if i != cursorRow {
					gridRow(c, spans, rowSize)
					continue
				}
				// The wrapper is the cursor overlay's containing block, so the
				// overlay uses the row's own top instead of index arithmetic.
				// Keep it free of padding and border: absolute insets resolve
				// against the padding-box edge. Build it even while the cursor
				// blinks off or the reader scrolled away, so the flow does not
				// jump when the overlay appears.
				rowBox := ui.Column(c).Gap(0).FillWidth().Height(cellHeight)
				rowBox.Children(func() {
					gridRow(c, spans, rowSize)
					// Keep the whole cursor cell visible while following.
					if following {
						rowBox.ScrollIntoView()
					}
					if cursorShown {
						renderCursor(c, cursor, float32(frame.CursorCol)*cellWidth, theme)
					}
				})
			}
			if hooks.Hover != nil && hooks.Hover.Link != "" && hooks.Hover.At.W > 0 {
				// The link Command is held over underlines, over exactly the
				// cells the pane resolved. The Hover rectangle is relative to
				// the Pane; this box sits flowPadY in from the Pane and livePad
				// inside the terminal's rounded background.
				at := hooks.Hover.At
				underline := ui.Color{}
				if uiTheme := c.Theme(); uiTheme != nil {
					underline = uiTheme.Accent
				}
				ui.Box(c).Absolute().
					Left(at.X - flowPadY - livePad).
					Top(at.Y - flowPadY - livePad + cellHeight - 2).
					Width(at.W).Height(2).
					Background(underline).PassThrough()
			}
		})
	})
	// A file dropped on the pane ripples where the pointer was, as Terminal's drop
	// does. It starts once the rows are built, so the cell and the point come from
	// the grid rather than from a guess.
	if len(dropped) > 0 && ripple != nil && !reduced {
		// A drag from another app does not deliver pointer moves to the pane, so
		// the last cell the pointer named may be where it was before the drag.
		// A native drag updates the pointer position the element tracks, so the
		// drop reads it fresh; the file-only channel carries no point, and the
		// earlier move is all there is. The zero position is only the "never
		// tracked" default.
		if x, y, _ := outer.PointerPosition(); x > 0 || y > 0 {
			if col, row, ok := pointerCell(lines, x-livePad, y-livePad, size.Cols); ok {
				ripple.PointerAt(col, row, ui.Point{X: x, Y: y})
			}
		}
		if ripple.StartDrop(now) {
			c.AnimationFrame()
		}
	}
	// The wave bends the whole pane, its background and padding too, as
	// Terminal's body does. It is applied once the rows are built: the bell's
	// cell centre is the one they were laid out at, and a drop may have started
	// above, from the pointer's own point.
	if ripple != nil && !reduced && ripple.Running(now) {
		origin, exact := ripple.Origin()
		if !exact {
			cellX, cellY := ripple.Cell()
			origin = cellOrigin(cellX, cellY, size.Cols)
		}
		outer.Effect(ripple.Effect(now, origin))
	}
}

// renderCursor draws the overlay as a child of the row that contains the cell,
// which is the context the caller builds it in. Its top is therefore the row's
// own top, and every row is one cell tall. Its left is the cell's place in the
// grid: col times the measured cell width. The cursor itself stays one cell
// tall, as a terminal's cursor does.
func renderCursor(c *ui.Context, cursor Cursor, left float32, theme *terminal.Theme) {
	if cellWidth <= 0 || cellHeight <= 0 {
		return
	}
	color := cursor.Color
	if color == (ui.Color{}) {
		color, _ = TermColors(c, theme)
	}
	top := float32(0)
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
	if len(lines) == 0 {
		return -1
	}
	if cursorRow < 0 {
		cursorRow = 0
	}
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
	// PromptRow is where the shell's current prompt run starts, counted from
	// the replay's first row before the replay cap drops the oldest rows above
	// the grid (see CursorReplayRow). Live translates it into the frame's own
	// rows, so it keeps naming the prompt while output pushes the capped window
	// down. It comes from the shell integration's OSC 133;B boundary, so
	// arbitrary prompt text does not need to be inspected.
	PromptRow          int
	PromptRowKnown     bool
	PromptAfterCommand bool
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
	// A prompt run starts at or above the line being typed: the prompt is
	// printed, then the input follows it. A boundary below the cursor therefore
	// cannot name this run — the shell cleared and repainted the screen without
	// a fresh OSC 133;B, or the frame's replay rolled to a new origin — and
	// cropping there left the redrawn prompt and the input out of the layout,
	// with nothing to scroll to. Start at the cursor, which always shows the
	// line being typed.
	if row := tail.PromptRow; row >= 0 && row < len(lines) {
		if row > cursorRow {
			return cursorRow
		}
		return row
	}
	// The recorded boundary names a row this replay does not have: the prompt
	// scrolled above the replay cap's window, the screen was cleared to a new
	// origin, or a full-screen program's snapshot has no scrollback at all. The
	// replay is then the whole screen, and cropping it to the cursor row would
	// hide every row above the cursor, where the program's own UI is.
	return 0
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

// AltScreen reports whether the replay is a full-screen program's alternate
// screen. Such a screen has no scrollback: its first row is the program's first
// row, not the replay's first row of history.
func (f Frame) AltScreen() bool {
	return f.sc != nil && f.sc.alt
}

// CursorVisible reports whether the program shows the cursor (DECSET ?25).
// Programs hide it while they redraw, and the live cursor overlay must follow:
// a block left at the last position looks like a cursor the program removed.
func (f Frame) CursorVisible() bool {
	return f.sc != nil && f.sc.cursorOn
}

// CursorStyle is the shape the program asked for with DECSCUSR, or fallback
// when it did not ask for one, as the application's cursor preference does.
func (f Frame) CursorStyle(fallback terminal.CursorStyle) terminal.CursorStyle {
	if f.sc != nil && f.sc.cursorShapeSet {
		return f.sc.cursorShape
	}
	return fallback
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
