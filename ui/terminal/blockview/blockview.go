// Package blockview draws Terminal's command blocks: one card per command a lens
// claimed, with a hover toolbar and the lens view or the raw output as its body.
// A command no lens claimed is not a block: it prints cells, with its prompt row
// and its output as plain terminal text. The prompt line is a row above either
// (RenderCommand), not part of the card: Terminal's .sf-block has only the bar, the
// body and the foot.
//
// The mapping mirrors findings/mygo-ui-mapping.md: MyGo has no CSS :hover, so
// the toolbar is built only while the block's card is hovered or holds the
// keyboard focus (root.Hovered() || root.FocusWithin()); the [Native][Raw]
// toggle is a ui.Segmented writing a local *int that is written back to the
// store with SetMode; Copy copies the raw output through the caller's onCopy
// (main wires it to mygo.Clipboard.WriteText).
package blockview

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
	"github.com/mslxl/natcatty/ui/terminal/lensview"
	"github.com/mslxl/natcatty/ui/terminal/surface"
)

// maxRawLines caps how many raw lines one frame builds; the full raw text is
// still what Copy copies. Terminal virtualizes the raw list; this is a simpler
// bound because the raw body is a selectable Scroll (a virtualized List cannot
// be selected as one document, as MyGo's text docs note).
const maxRawLines = 500

// maxCellLines is how many lines a plain cell draws before it gets a scroll
// viewport of its own. It keeps an ordinary command (a few lines) as plain flow
// text instead of a nested scroll area.
const maxCellLines = 40

// Render draws every block in the store, in order, each with its prompt line
// above it. viewport is the visible part of the flow in window coordinates
// (the zero Rect when the caller has none); a block's toolbar uses it to stay
// inside the visible range.
func Render(c *ui.Context, store *model.Store, engine *lens.Engine, viewport ui.Rect, onCopy func(string)) {
	blocks := store.Snapshot()
	col := ui.Column(c).Gap(10).FillWidth()
	col.Children(func() {
		for i := range blocks {
			// A running command is still represented by the live terminal
			// below this flow. Terminal swaps its rows into a finished block only
			// after OSC 133;D, so rendering it here would duplicate output.
			if !blocks[i].Exited {
				continue
			}
			RenderCommand(c, store, engine, &blocks[i], viewport, onCopy)
		}
	})
}

// RenderCommand draws one command's cell the way Terminal's flow has it: the prompt
// line as a row of its own, then either the lens block or, for a command no lens
// claimed, plain cells. Terminal builds .sf-block only for a lens's command: its bar
// always carries the lens name and the Native/Raw switch, so a plain command is
// left as terminal output.
func RenderCommand(c *ui.Context, store *model.Store, engine *lens.Engine, b *model.Block, viewport ui.Rect, onCopy func(string)) {
	group := ui.Column(c).Gap(0).FillWidth()
	group.Children(func() {
		renderPromptLine(c, b)
		if b.LensID == "" {
			renderCell(c, store, b)
			renderImages(c, b)
			return
		}
		renderBlock(c, store, engine, b, viewport, onCopy)
	})
}

// renderPromptLine is the row above a block: the prompt and the line the user
// typed. Terminal draws the prompt itself (the shell's PS1 is blank columns) and the
// shell echoes the typed line, so both are terminal content, not block chrome.
// One RichText keeps it a terminal row: a Row of two Texts would be shorter than
// the live grid's cell and the command would jump up a row when it ended.
func renderPromptLine(c *ui.Context, b *model.Block) {
	bc := lensview.BlockColorsFor(c)
	cmd := b.Cmd.Typed
	if cmd == "" {
		cmd = b.Cmd.Line
	}
	if cmd == "" {
		cmd = "(command)"
	}
	ui.RichText(c,
		ui.Span{Text: model.PromptText(b.Cmd.Cwd) + " ", Font: "monospace", Color: bc.T3},
		ui.Span{Text: cmd, Font: "monospace"},
	).SingleLine()
}

// RenderBlock draws one block's card, without its prompt line: the chrome, the
// lens view or the raw output, and the foot. The flow draws the prompt line with
// RenderCommand; a caller that shows a block on its own gets only the block.
func RenderBlock(c *ui.Context, store *model.Store, engine *lens.Engine, b *model.Block, onCopy func(string)) {
	renderBlock(c, store, engine, b, ui.Rect{}, onCopy)
}

func renderBlock(c *ui.Context, store *model.Store, engine *lens.Engine, b *model.Block, viewport ui.Rect, onCopy func(string)) {
	t := c.Theme()
	// Native holds a lens.Node for blocks a lens claimed.
	native, hasNative := b.Native.(lens.Node)
	_ = engine // events are wired by the lensview widgets later; see gaps.

	// Terminal's block is flat: no card background, corner or border. What marks it is a
	// two-pixel bar down its left edge (`.sf-block::before`), which brightens on hover
	// and takes the state's colour while running or failed, plus a foot with badges.
	//
	// `.sf-block`'s 10 px left padding is the rail's gutter, not a body indent: in
	// Terminal the prompt, the body and the live row share one left edge and only the rail
	// steps 10 px out. The card is pulled that far left so its own padding puts the
	// body back on the prompt's edge (the prompt is drawn outside the card).
	card := ui.Column(c).Padding(1, 0, 1, 10).Margin(0, 0, 0, -10).Gap(lensview.InnerGap(c)).FillWidth()

	// The toolbar shows while the pointer is over the card or keyboard focus
	// is inside it: MyGo has no CSS :hover, so this is a per-frame query.
	hovered := card.Hovered() || card.FocusWithin()
	// Terminal's block chrome colours: the two hairlines, the chip surface, and the
	// state colours a running or failed command's bar takes (--live #a86af4 at 40%,
	// --bad at 50%).
	bc := lensview.BlockColorsFor(c)
	bar := bc.L1
	if hovered {
		bar = bc.L3
	}
	if !b.Exited {
		bar = withAlpha(bc.Live, 0.4)
	} else if b.Status != 0 {
		bar = withAlpha(bc.Bad, 0.5)
	}

	card.Children(func() {
		ui.Box(c).Absolute().Left(0).Top(2).Bottom(2).Width(2).Radius(1).Background(bar)
		// Terminal's .sf-block-bar: always mounted, faded in on hover; it is absolutely
		// positioned, so it never takes room in the flow and a block never moves
		// under the pointer.
		renderActions(c, store, b, card, viewport, hasNative, onCopy, hovered)
		renderBody(c, store, b, native, hasNative, engine)
		renderImages(c, b)
		renderFoot(c, b)
		// Terminal keeps a question a program is waiting on visible on the block
		// ("they show … any prompt waiting on you, such as `[y/N]`"). A running
		// block has no Raw yet, so read its partial output.
		raw := b.Raw
		if !b.Exited {
			if partial := store.PartialRaw(b.ID); partial != "" {
				raw = partial
			}
		}
		if p := promptTail(raw, !b.Exited); p != "" && !b.Exited {
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, p).Font("monospace").TextColor(t.Accent).SingleLine()
				ui.Badge(c, "waiting for input").Background(t.Surface).TextColor(t.TextMuted)
			})
		}
	})
}

// promptTail reports the last line when the program looks like it is waiting for
// an answer, and "" otherwise.
func promptTail(raw string, running bool) string {
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if len(lines) == 0 {
		return ""
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" || len(last) > 160 {
		return ""
	}
	low := strings.ToLower(last)
	for _, pat := range []string{"[y/n]", "[y/n]?", "(y/n)", "[yes/no]", "(yes/no)", "[y/n/q]"} {
		if strings.Contains(low, pat) {
			return last
		}
	}
	// A running block is waiting when its last line is a question or offers a
	// bracketed choice. A trailing ":" is NOT enough: progress lines like
	// "Linking target now:" or "done [ok]" would be mistaken for a prompt.
	if running && strings.HasSuffix(last, "?") && len([]rune(last)) <= 200 {
		return last
	}
	if running && strings.HasSuffix(last, "]") {
		// Only a bracketed y/n-style choice counts, as in Terminal's "[y/N]".
		for _, pat := range []string{"[y/n", "[yes", "[no/", "[n/y"} {
			if strings.Contains(low, pat) {
				return last
			}
		}
	}
	for _, w := range []string{"press any key", "hit enter", "press enter"} {
		if strings.Contains(low, w) {
			return last
		}
	}
	for _, w := range []string{"password", "passphrase", "continue", "proceed", "are you sure",
		"overwrite", "confirm", "enter passphrase", "do you want"} {
		if strings.Contains(low, w) &&
			(strings.HasSuffix(last, "?") || strings.HasSuffix(last, ":") || strings.HasSuffix(last, "]")) {
			return last
		}
	}
	return ""
}

// progressTip describes an OSC 9;4 value for the tooltip.
func progressTip(v float64) string {
	if v < 0 {
		return "the program is working (progress unknown)"
	}
	return itoa(int(v*100)) + "%"
}

// renderFoot is the block's foot (Terminal's `.sf-block-foot`): the program's status
// and progress, how the command ended and how long it took, as badges under the
// body. Terminal's block has no head row, so the metadata that used to sit beside the
// command lives here.
func renderFoot(c *ui.Context, b *model.Block) {
	bc := lensview.BlockColorsFor(c)
	t := c.Theme()
	ui.Row(c).Gap(8).Height(bc.LH).Margin(2, 0, 0, 0).Children(func() {
		// Terminal's sf-block-lens shows which lens claimed the command; the hover bar
		// carries its name, and a lens that failed keeps its error on the foot.
		if b.LensErr != "" {
			ui.Badge(c, "lens error").Background(t.Danger).TextColor(t.Background).
				Tooltip(b.LensErr)
		}

		// OSC 9;4 progress and OSC 7501 status, as Terminal shows on the block.
		if b.Progress != nil {
			v := *b.Progress
			pct := v
			if v < 0 {
				pct = -1 // indeterminate
			}
			ui.Box(c).Width(90).Children(func() {
				ui.Progress(c, pct).Tooltip(progressTip(v))
			})
			if v >= 0 {
				ui.Text(c, itoa(int(v*100))+"%").TextColor(t.TextMuted).FontFeatures("tnum")
			}
		}
		if b.StateText != "" {
			ui.Badge(c, b.StateText).Background(t.Surface).TextColor(t.TextMuted)
		}

		if !b.Exited {
			return // a running block's state is its live bar (Terminal's data-state)
		}
		// Terminal's foot badges: the tone's text on a 10% tone fill inside a 22% ring.
		tone := bc.Ok
		if b.Status != 0 {
			tone = bc.Bad
		}
		// .sf-block-foot > .sf-badge: 16 DIP tall, 6 DIP of side padding, 10.5px.
		ui.Badge(c, "exit "+itoa(b.Status)).TextColor(tone).Background(withAlpha(tone, 0.10)).
			InsetShadow(0, 0, 1, 0, withAlpha(tone, 0.22)).
			Height(16).Padding(0, 6).FontSize(10.5)
		el := b.Elapsed()
		if el > 0 {
			ui.Text(c, "took "+el.Round(time.Millisecond).String()).
				TextColor(bc.T4).FontFeatures("tnum").FontSize(11).Font(surface.MonoFamily())
		}
	})
}

// withAlpha returns c at the given opacity.
func withAlpha(c ui.Color, a float32) ui.Color {
	if c.A == 0 {
		c.A = 255
	}
	return ui.RGBA(c.R, c.G, c.B, a*(float32(c.A)/255))
}

// renderActions is Terminal's .sf-block-bar: a pill at the block's top-right holding the
// lens's icon and name, the Native/Raw segmented control and the copy button. It
// lives on an absolute row, so showing it never moves the block.
// The action bar's metrics, and where it sits inside its card.
const (
	actionBarHeight = float32(24)
	actionBarTop    = float32(4) // from the card's padding box, below its border
	actionBarGap    = float32(4) // breathing room below the bar at the card's end
)

// actionBarOffset returns the Top the bar needs inside its card so that its
// window top is the visible top of the block: the bar sticks to the top-right
// of the block's visible range instead of scrolling away with the body.
// Terminal's bar is absolutely positioned against the block, and MyGo has no
// sticky inset, so the offset is recomputed each frame from the card's placed
// box (the previous frame's, from LayoutBounds: Top is a layout-space inset, so
// the border box is the right reference) and the viewport the flow drew into.
// While the block's own top is still visible the bar stays there; once it
// scrolls above the viewport the bar follows it; and it stops before the
// block's end so it never leaves the lens.
func actionBarOffset(viewport, card ui.Rect) float32 {
	if viewport.H <= 0 || card.H <= 0 {
		return actionBarTop
	}
	// The card's top padding is 1: Top is measured from the padding box,
	// which starts one DIP below the border box. While the card's own top is
	// still on screen the bar sits on the card; once that top is above the
	// viewport it sits on the viewport instead.
	anchor := max(card.Y+1, viewport.Y)
	top := anchor + actionBarTop - (card.Y + 1)
	// The padding box is two DIPs shorter than the border box (1 top,
	// 1 bottom); the bar may travel no further than its end.
	if limit := max(actionBarTop, card.H-2-actionBarHeight-actionBarGap); top > limit {
		top = limit
	}
	return top
}

func renderActions(c *ui.Context, store *model.Store, b *model.Block, card ui.Element, viewport ui.Rect, hasNative bool, onCopy func(string), hovered bool) {
	bc := lensview.BlockColorsFor(c)
	// --pop-bg: the surface Terminal's bar sits on, then the fade Terminal animates (0 -> 1
	// over 150ms) and pointer-events off while it is hidden.
	pop := ui.RGBA(255, 255, 255, 0.82)
	if c.Theme().Dark {
		pop = ui.RGBA(0x23, 0x23, 0x27, 0.98)
	}
	target := float32(0)
	if hovered {
		target = 1
	}
	bar := ui.Row(c).Absolute().Right(0).Height(24).Padding(0, 2, 0, 8).Gap(4).
		Radius(8).Background(pop).InsetShadow(0, 0, 0, 1, bc.L2)
	op := bar.AnimateWith("blockbar:"+rawKey(store, b), target, 150*time.Millisecond, ui.EaseOut)
	// Terminal hides the bar with translateY(-2px) and lifts it into place on hover, so
	// it rides down as it fades in.
	bar.Top(actionBarOffset(viewport, card.LayoutBounds()) + 2*op).Opacity(op).Disabled(!hovered)
	bar.Children(func() {
		// .sf-block-lens: an icon and the name in 11px mono, the muted step, cut at
		// 220 DIP.
		// .sf-block-ic: a 14 DIP box holding the lens's 12 DIP drawing, in --t3; a
		// lens whose icon is unknown keeps the generic mark.
		ui.Row(c).Width(14).Height(14).Center().Children(func() {
			if name := surface.IconForLens(b.LensID); name != "" {
				if svg, ok := surface.Icon(name); ok {
					ui.Icon(c, svg).Width(12).Height(12).TextColor(bc.T3)
					return
				}
			}
			ui.Text(c, "▤").TextColor(bc.T3).FontSize(12)
		})
		name := b.LensID
		if name == "" {
			name = "raw"
		}
		ui.Text(c, name).Font(surface.MonoFamily()).TextColor(bc.T3).FontSize(11).
			MaxWidth(220).SingleLine()

		if b.LensID != "" || hasNative {
			renderSegmented(c, store, b, hasNative, bc)
		}

		// .sf-block-copy: a 22x20 icon button, the muted step.
		cp := ui.Row(c).Width(22).Height(20).Radius(5).Center().Focusable()
		cp.Tooltip("Copy the raw output")
		if cp.FocusVisible() {
			cp.InsetShadow(0, 0, 0, 1, bc.Accent).
				Shadow(0, 0, 0, 3, withAlpha(bc.Accent, 0.18))
		}
		cp.OnClick(func() {
			text := b.Raw
			if !b.Exited {
				if partial := store.PartialRaw(b.ID); partial != "" {
					text = partial
				}
			}
			text = strings.TrimRight(text, "\n")
			if onCopy != nil {
				onCopy(text)
			}
			c.Announce("Copied output")
		})
		if cp.Pressed() {
			cp.Scale(0.97, 0.97) // .sf-block-copy:active
		}
		glyph := bc.T3
		if cp.Hovered() {
			// .sf-block-copy:hover { background: var(--l1); color: var(--t1) }
			cp.Background(bc.L1)
			glyph = bc.T1
		}
		ui.Icon(c, surface.CopyIcon()).Width(13).Height(13).TextColor(glyph)
	})
}

// renderSegmented is .sf-block-seg: a 20 DIP pill on the first hairline holding two
// 18 DIP options, the chosen one on the card colour with a hairline ring.
func renderSegmented(c *ui.Context, store *model.Store, b *model.Block, hasNative bool, bc lensview.BlockColors) {
	raw := store.EffectiveView(b) == model.ViewRaw
	seg := ui.Row(c).Height(20).Radius(6).Padding(1).Gap(1).Background(bc.L1)
	seg.Children(func() {
		opt := func(label string, on bool, set func()) {
			e := ui.Row(c).Height(18).Padding(0, 7).Radius(5).Center()
			col := bc.T3
			if e.Hovered() || on {
				col = bc.T1 // .sf-block-opt:hover { color: var(--t1) }
			}
			e.Children(func() { ui.Text(c, label).FontSize(11.5).TextColor(col) })
			if on {
				e.Background(bc.Chip).InsetShadow(0, 0, 0, 1, bc.L2)
			}
			e.Focusable()
			if e.Pressed() {
				e.Scale(0.97, 0.97) // .sf-block-opt:active
			}
			if e.FocusVisible() {
				// .sf-block-opt:focus-visible { box-shadow: 0 0 0 1px var(--accent),
				// 0 0 0 3px rgb(from var(--accent) r g b/18%) }
				e.InsetShadow(0, 0, 0, 1, bc.Accent).
					Shadow(0, 0, 0, 3, withAlpha(bc.Accent, 0.18))
			}
			e.OnClick(func() {
				set()
				c.Invalidate()
			})
		}
		opt("Native", !raw && hasNative, func() { store.SetMode(b.ID, model.ViewNative) })
		opt("Raw", raw, func() { store.SetMode(b.ID, model.ViewRaw) })
	})
}

func renderBody(c *ui.Context, store *model.Store, b *model.Block, native lens.Node, hasNative bool, engine *lens.Engine) {
	if store.EffectiveView(b) == model.ViewNative && hasNative && native != nil {
		ui.Column(c).FillWidth().Children(func() {
			// Empty lines the program printed first stay rows: the live grid showed
			// them, so without them the body jumps up when the command ends. The raw
			// text carries empty lines itself; a native view starts at its first row,
			// so the rows are drawn here.
			for i := 0; i < leadingBlankRows(store, b); i++ {
				// A terminal row, not a surface gap: the line was a grid row while
				// it lived, so the first real row must stay where it was.
				ui.Text(c, " ").Font("monospace").SingleLine()
			}
			// The scope keeps a section's collapse state across frames; the lens
			// rebuilds its tree every frame, so a bare bool would be forgotten. A
			// click a row reports goes back to the lens, which decides what it
			// selects and rebuilds its view.
			lensview.RenderInto(c, native, lensScope(store, b), func(ev lens.Event) {
				if engine != nil {
					engine.Event(b.ID, ev)
				}
				c.Invalidate()
			})
		})
		return
	}
	renderRaw(c, store, b)
}

// leadingBlankRows is how many empty output rows the stream begins with. The stream
// starts with the line break that ended the prompt row, and the tap captures it: a
// command whose output starts directly under its prompt has "\nrow1", not "row1".
// That first break is not a row — only the breaks after it are, so `ping` (which
// prints an empty line first: "\n\nPinging") gets one row and `echo` none. Counting
// the first break as a row made every native view and every plain cell start one row
// too low in the app, while the headless fixtures (which had no leading break) did
// not show it.
func leadingBlankRows(store *model.Store, b *model.Block) int {
	raw := b.Raw
	if !b.Exited {
		raw = store.PartialRaw(b.ID)
	}
	n := 0
	for {
		rest, ok := cutBreak(raw)
		if !ok {
			break
		}
		raw = rest
		n++
	}
	if n > 0 {
		n-- // the prompt row's own line break
	}
	return n
}

// cutBreak removes one line break ("\n" or "\r\n") from the start of s.
func cutBreak(s string) (string, bool) {
	switch {
	case strings.HasPrefix(s, "\r\n"):
		return s[2:], true
	case strings.HasPrefix(s, "\n"):
		return s[1:], true
	}
	return s, false
}

func renderRaw(c *ui.Context, store *model.Store, b *model.Block) {
	t := c.Theme()
	lines, hidden, expanded := rawLines(store, b)
	if lines == nil {
		if !b.Exited {
			ui.Text(c, "(waiting for output)").TextColor(t.TextMuted)
		}
		return
	}
	rawViewport(c, lines)
	renderRawNotice(c, store, b, hidden, expanded)
}

// renderCell is a command no lens claimed: Terminal leaves it as terminal cells, so
// the flow draws the shared prompt row and the raw output without block chrome
// (no rail, hover bar, Native/Raw switch, copy button or foot).
func renderCell(c *ui.Context, store *model.Store, b *model.Block) {
	lines, hidden, expanded := rawLines(store, b)
	if lines == nil {
		return
	}
	if len(lines) <= maxCellLines && hidden == 0 {
		col := ui.Column(c).Selectable()
		col.Children(func() { cellTexts(c, lines) })
	} else {
		textViewport(c, lines, cellTexts)
	}
	renderRawNotice(c, store, b, hidden, expanded)
}

// renderImages draws the inline pictures a command printed, in the block they
// belong to. They are part of the block rather than a strip above the live
// screen, so they sit in the command's output where the program put them and
// scroll away with it instead of lingering.
func renderImages(c *ui.Context, b *model.Block) {
	if len(b.Images) == 0 {
		return
	}
	scope := "block-image:" + strconv.Itoa(b.ID)
	ui.Column(c).Gap(6).Padding(0, 0, 4, 0).FillWidth().Children(func() {
		for _, img := range b.Images {
			lensview.RenderInto(c, lens.Image{Data: img.Data, Path: img.Path, Alt: img.Alt}, scope, nil)
		}
	})
}

// CellRowHeight is one row of the terminal's grid, in DIPs. The flow that draws the
// live grid sets it (the height it measured for a cell); a plain cell's rows were
// grid rows, so they take that exact height even though the theme's wrapped text is
// shorter. Zero leaves the text its natural height.
var CellRowHeight float32

// cellTexts draws a plain command's output as the terminal cells it was: monospace
// at the theme size, on the grid's own row pitch. The rows were grid rows one frame
// earlier, so a shorter line would make a multi-line output drift up and take the
// prompt below it along, exactly as a block that moves when it ends.
func cellTexts(c *ui.Context, lines []string) {
	for _, line := range lines {
		txt := " "
		if line != "" {
			txt = line
		}
		e := ui.Text(c, txt).Font("monospace")
		if CellRowHeight > 0 {
			e = e.FixedLineHeight(CellRowHeight)
		}
		if line == "" {
			e.SingleLine()
		}
	}
}

// rawViewport is the scrollable text area a long raw output gets, inside a block
// (Terminal's `.sf-list-scroll.max`). One selectable container shares the selection
// across its Text descendants, so the text can be copied as one document.
func rawViewport(c *ui.Context, lines []string) { textViewport(c, lines, rawTexts) }

// textViewport is rawViewport over any row drawing: a block's raw body and a plain
// cell have different row metrics but the same scrolling frame.
func textViewport(c *ui.Context, lines []string, draw func(*ui.Context, []string)) {
	box := ui.Box(c).MaxHeight(260).Clip().FillWidth()
	box.Children(func() {
		ui.Scroll(c).Grow(1).Children(func() {
			col := ui.Column(c).Selectable()
			col.Children(func() { draw(c, lines) })
		})
	})
}

// rawLines is the text both a raw block and a plain cell draw: the streamed
// output while a command runs, the finished output after, with Terminal's block-level
// line cap (and whether the user lifted it).
func rawLines(store *model.Store, b *model.Block) (lines []string, hidden int, expanded bool) {
	// A running block has no b.Raw yet (the store only fills it when the command
	// ends), so the streamed lines come from PartialRaw.
	raw := b.Raw
	if !b.Exited {
		if partial := store.PartialRaw(b.ID); partial != "" {
			raw = partial
		}
	}
	raw = strings.TrimRight(raw, "\r\n")
	// The stream opens with the line break that ended the prompt row (see
	// leadingBlankRows): it is not an output row, so dropping it keeps the first
	// entry the row the terminal drew under the prompt.
	if rest, ok := cutBreak(raw); ok {
		raw = rest
	}
	if raw == "" {
		return nil, 0, false
	}
	lines = strings.Split(raw, "\n")
	expanded = rawExpanded(store, b)
	if !expanded && len(lines) > maxRawLines {
		hidden = len(lines) - maxRawLines
		lines = lines[:maxRawLines]
	}
	return lines, hidden, expanded
}

func rawTexts(c *ui.Context, lines []string) {
	t := c.Theme()
	for _, line := range lines {
		if line == "" {
			ui.Text(c, " ")
			continue
		}
		ui.Text(c, line).Font("monospace").FontSize(t.FontSize - 1)
	}
}

// renderRawNotice is Terminal's block-level output-limit affordance: the clickable
// notice expands the whole output (strings 791490-791492).
func renderRawNotice(c *ui.Context, store *model.Store, b *model.Block, hidden int, expanded bool) {
	t := c.Theme()
	if hidden > 0 {
		unit := "lines"
		if hidden == 1 {
			unit = "line"
		}
		notice := ui.Text(c, itoa(hidden)+" "+unit+" over this command's output limit are taken out here · click for the whole output").
			TextColor(t.TextMuted)
		notice.OnClick(func() {
			setRawExpanded(store, b, true)
			c.Invalidate()
		})
		return
	}
	if expanded {
		more := ui.Text(c, "Show fewer lines").TextColor(t.TextMuted)
		more.OnClick(func() {
			setRawExpanded(store, b, false)
			c.Invalidate()
		})
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// rawExpand remembers which raw blocks the user expanded past the line cap.
var (
	rawExpandMu sync.Mutex
	rawExpand   = map[string]bool{}
)

// stateCap bounds the per-block state map, like the lensview one.
const stateCap = 4096

// lensScope keys the lens view's interactive state by the block's run, so a new
// run (after Clear, or in another store) does not inherit a collapsed section.
func lensScope(store *model.Store, b *model.Block) string {
	return "block-" + rawKey(store, b)
}

// rawKey identifies one block run: the store resets its ids on Clear, so the
// block's start time keeps a new run from inheriting an old expansion.
func rawKey(store *model.Store, b *model.Block) string {
	return strconv.FormatUint(store.UID(), 10) + ":" + itoa(b.ID) + "@" +
		strconv.FormatInt(b.Started.UnixNano(), 10)
}

func rawExpanded(store *model.Store, b *model.Block) bool {
	rawExpandMu.Lock()
	defer rawExpandMu.Unlock()
	return rawExpand[rawKey(store, b)]
}

func setRawExpanded(store *model.Store, b *model.Block, on bool) {
	rawExpandMu.Lock()
	defer rawExpandMu.Unlock()
	if on {
		if len(rawExpand) > stateCap {
			rawExpand = map[string]bool{}
		}
		rawExpand[rawKey(store, b)] = true
		return
	}
	delete(rawExpand, rawKey(store, b))
}
