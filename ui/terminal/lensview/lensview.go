// Package lensview renders a lens.Node tree with MyGo's native ui widgets.
//
// It is the Go/MyGo counterpart of Terminal's Stencil renderer: Terminal's lenses build
// a view tree (internal/lens), and this package maps every node kind onto the
// closest widget of github.com/egoist/mygo/ui v0.5.5, composing the rest.
//
// A lens view never scrolls inside itself. Lists, ANSI screens, code, diffs and
// Markdown documents take their natural height in the flow, and the terminal's
// own scroll reaches every row; a widget with its own scrollbar split one
// command's output into two scroll regions (ADR-0022). A limit on long content
// belongs to the lens's own Clamp or Overflow node, which states the omitted
// rows and can be lifted, never to a hidden scroll box. A code line, a Markdown
// code block, a Markdown table's cell and display math wrap instead of scrolling
// sideways for the same reason.
//
// Known gaps, kept here because the mapping is deliberately lossy (the frozen
// lens.Node carries no node ids, events or stylesheet):
//
//   - Cards and sections render the collapse state the tree arrives with; the
//     toggle cannot be sent back to the lens (lens.Node has no event ids), so a
//     Collapsible's *bool is a per-frame copy and a click resets on the next
//     lens rebuild.
//   - Table: Col.Priority drops columns when the block is narrow and the cells
//     are measured from their content; TruncateEnd/Start/Middle cut the text
//     ourselves (the ellipsis fits inside the column's cell budget, as Terminal
//     does) because ui.Text only cuts at the end.
//   - Code: the editor buffer/state are cached per scope+position and reused
//     while the text is unchanged, so folds and the selection survive a rebuild;
//     a changed text gets a fresh buffer. Code.Start (first line number) is
//     drawn as our own gutter beside the editor, since editor.State has no start
//     option.
//   - Diff: the node carries unified diff text, not the two sides, so the old
//     and new texts are reconstructed from the patch; each hunk is placed at the
//     line its `@@ -a,b +c,d @@` header names, so unshown regions become context
//     gaps instead of glued hunks. A patch whose hunk headers disagree with its
//     body still renders approximately.
//   - ANSI: the node is a mini terminal (CR/LF/BS/TAB, CSI H/A/B/C/D/G/K/J/s/u,
//     SGR, wide runes); alternate screen, DEC graphics and hyperlink cells are
//     not modelled, and the screen is a fixed 200 columns wide.
//   - Icons: TreeNode.Icon / Item.Icon carry a name; ui.Icon needs a parsed
//     *ui.SVG, so a known name becomes a leading text glyph with its tone
//     (iconGlyph). Names outside that table render nothing.
//   - Key is drawn as a keycap with a background, radius and an inset shadow;
//     ui.Span has no border, so Terminal's `.sf-t-key` 1px outline is approximated.
//   - Diagnostic is a first-class node (lens.Diagnostic) and is rendered the way
//     Terminal's `flow.ui.diagnostic` composes it.
//   - Overflow uses Terminal's wording ("N lines over this command's output limit
//     are taken out here · click for the whole output", strings 791490-791492).
package lensview

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/lens/parse"
	"github.com/mslxl/natcatty/ui/terminal/surface"
	"github.com/mslxl/natcatty/ui/terminal/theme"
)

// Render draws n. Call it from a MyGo view; the tree is rebuilt on every frame
// the way Terminal rebuilds its view, so state lives in the lens, not in elements.
func Render(c *ui.Context, n lens.Node) {
	// An unscoped tree gets a scope of its own: the interactive state (collapse,
	// code expansion, editor buffers) is keyed by scope, and two unrelated trees
	// must never share it. Callers that want a section click to survive the next
	// frame pass a stable scope to RenderScoped.
	renderSelectable(c, n, &renderState{scope: fmt.Sprintf("anon-%d", anonScope.Add(1))})
}

// anonScope numbers the scopes Render hands out.
var anonScope atomic.Uint64

// RenderScoped is Render with a scope for the interactive state that has to
// outlive a frame (section collapse). A lens rebuilds its whole tree every frame,
// so without a key the user's click would be forgotten.
func RenderScoped(c *ui.Context, n lens.Node, scope string) {
	RenderInto(c, n, scope, nil)
}

// Sink receives an event a view asks its lens to handle (Terminal's UiEvent: a list row
// selected, a table row activated). The renderer does not know block ids, so the
// caller supplies the destination.
type Sink func(lens.Event)

// RenderInto renders a view and reports the events its nodes emit through sink.
func RenderInto(c *ui.Context, n lens.Node, scope string, sink Sink) {
	renderSelectable(c, n, &renderState{scope: scope, sink: sink})
}

// renderSelectable wraps the tree in the Selectable container that gives the
// lens view a selection of its own: the Text and RichText descendants share
// one paragraph selection, so a drag or the Copy gesture over the body copies
// the rendered rows instead of landing on the terminal's cell selection. A
// nested Selectable (Markdown, the raw cell) keeps its own scope. The wrapper
// hands the body the terminal's configured font — the same comma-separated
// family list and size the terminal plugin draws with — so a lens view is the
// terminal's own text, not a widget in a different typeface.
func renderSelectable(c *ui.Context, n lens.Node, rs *renderState) {
	family, size := surface.TerminalFont()
	if family == "" {
		family = "monospace" // the terminal plugin's default when unset
	}
	col := ui.Column(c).Selectable().Font(family)
	if size > 0 {
		col = col.FontSize(size)
	}
	col.Children(func() {
		renderWith(c, n, rs)
	})
}

// renderState threads the scope and a content-derived key through one tree walk.
// Keys are built from the node kind and its content (plus an occurrence index for
// duplicates) rather than the walk position, so inserting a sibling above a node
// no longer moves that node's state to a different key.
type renderState struct {
	scope string
	sink  Sink
	seen  map[string]int
}

func (rs *renderState) key(kind, content string) string {
	if rs.seen == nil {
		rs.seen = map[string]int{}
	}
	base := kind + ":" + fingerprint(content)
	n := rs.seen[base]
	rs.seen[base] = n + 1
	return stateKey(rs.scope, base, n)
}

// stateKey is the on-disk name of one node state.
func stateKey(scope, base string, occurrence int) string {
	return scope + "#" + base + ":" + strconv.Itoa(occurrence)
}

// NodeStateKey is the key a node of this kind and content uses inside scope: the
// section and code renderers pass their head text / source text as content. Tests
// use it to drive one node's collapse or expansion state directly.
func NodeStateKey(scope, kind, content string, occurrence int) string {
	return stateKey(scope, kind+":"+fingerprint(content), occurrence)
}

// fingerprint hashes node content so equal nodes share a key base.
func fingerprint(content string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(content))
	return strconv.FormatUint(h.Sum64(), 36)
}

// collapse remembers a section's open state across frames.
var (
	collapseMu sync.Mutex
	collapse   = map[string]bool{}
)

func collapseGet(key string, def bool) bool {
	collapseMu.Lock()
	defer collapseMu.Unlock()
	if v, ok := collapse[key]; ok {
		return v
	}
	return def
}

func collapseSet(key string, open bool) {
	collapseMu.Lock()
	defer collapseMu.Unlock()
	if len(collapse) > stateCap {
		collapse = map[string]bool{}
	}
	collapse[key] = open
}

// selection remembers which row of a list or table the user picked, per list
// instance, the way Terminal's `.sf-item.sel` keeps a listing's selection. The lens is
// told too (Sink), but the highlight is the surface's own state.
var (
	selectMu sync.Mutex
	selected = map[string]string{}
)

func selectGet(key string) string {
	selectMu.Lock()
	defer selectMu.Unlock()
	return selected[key]
}

func selectSet(key, row string) {
	selectMu.Lock()
	defer selectMu.Unlock()
	if len(selected) > stateCap {
		selected = map[string]string{}
	}
	selected[key] = row
}

// filterText returns the text of a list's filter box, kept per list instance so it
// survives the lens rebuilding its tree every frame (Terminal's lenses bind an input
// from flow.ui.el and filter on change).
var (
	filterMu   sync.Mutex
	filterBox  = map[string]*string{}
	filterSeen = map[string]string{}
)

func filterText(key string) *string {
	filterMu.Lock()
	defer filterMu.Unlock()
	if v, ok := filterBox[key]; ok {
		return v
	}
	if len(filterBox) > stateCap {
		filterBox = map[string]*string{}
		filterSeen = map[string]string{}
	}
	v := new(string)
	filterBox[key] = v
	return v
}

// filterNote reports the filter's text to the lens when it changes, as a `change`
// UiEvent, instead of once per frame.
func filterNote(rs *renderState, key string) {
	if rs == nil || rs.sink == nil {
		return
	}
	cur := *filterText(key)
	filterMu.Lock()
	last, seen := filterSeen[key]
	filterSeen[key] = cur
	filterMu.Unlock()
	if !seen || last != cur {
		rs.sink(lens.Event{Action: "change", Value: cur})
	}
}

// filterItems keeps the rows matching q, case-insensitively, over their label,
// detail and value.
func filterItems(items []lens.Item, q string) []lens.Item {
	q = strings.ToLower(q)
	out := make([]lens.Item, 0, len(items))
	for _, it := range items {
		hay := plain(it.Label) + " " + plain(it.Detail) + " " + plain(it.Value)
		if strings.Contains(strings.ToLower(hay), q) {
			out = append(out, it)
		}
	}
	return out
}

// selectedStyle marks the picked row: a 12% accent tint behind a two-pixel accent
// bar on its leading edge, Terminal's accent 10/16% fill and `inset 2px 0 0 var(--accent)`.
func (s style) selectedStyle(c *ui.Context, e ui.Element) ui.Element {
	return e.Background(mix(s.CardBG, s.Accent, 0.12)).
		InsetShadow(2, 0, 0, 0, s.Accent)
}

// stateCap bounds the per-position state maps: block ids and anonymous scopes
// that scroll out of view would otherwise accumulate forever.
const stateCap = 4096

func renderWith(c *ui.Context, n lens.Node, rs *renderState) {
	if n == nil {
		return
	}
	switch n := n.(type) {
	case lens.Text:
		text(c, n.Spans)
	case lens.Column:
		col := ui.Column(c)
		if n.Gap > 0 {
			col = col.Gap(n.Gap)
		} else {
			col = col.Gap(4)
		}
		col.Children(func() {
			for _, ch := range n.Children {
				renderWith(c, ch, rs)
			}
		})
	case lens.Row:
		ui.Row(c).Gap(8).Children(func() {
			for _, ch := range n.Children {
				renderWith(c, ch, rs)
			}
		})
	case lens.Lines:
		renderLines(c, n, rs)
	case lens.Stats:
		renderStats(c, n)
	case lens.Button:
		renderButton(c, n, rs)
	case lens.El:
		renderEl(c, n, rs)
	case lens.TSP:
		renderTSP(c, n, rs)
	case lens.Image:
		renderImage(c, n, rs)
	case lens.Card:
		renderCard(c, n, rs)
	case lens.Section:
		renderSection(c, n, rs)
	case lens.KV:
		renderKV(c, n)
	case lens.Badge:
		renderBadge(c, n)
	case lens.Table:
		renderTable(c, n, rs)
	case lens.Tree:
		renderTree(c, n)
	case lens.List:
		renderList(c, n, rs)
	case lens.Code:
		renderCode(c, n, rs)
	case lens.Diff:
		renderDiff(c, n)
	case lens.Rule:
		renderRule(c, n)
	case lens.Markdown:
		// Markdown is rendered with the same ramp and code surface as the rest of a
		// lens view: one look, whether a node is text or a document.
		ms := styleFor(c)
		md := markdown.DefaultTheme(c.Theme())
		md.Text, md.Muted, md.Strong = ms.T1, ms.T3, ms.T1
		md.Code, md.CodeBackground = ms.T1, ms.CodeBG
		md.Link = ms.Accent
		// The document is part of the flow: its height follows the text, so a
		// `cat README.md` block shows the whole file and the flow's scroll
		// reaches it. A fixed cap here clipped every document at 420 DIP and
		// left no affordance to read the rest. WrapCode wraps a code block's
		// long lines, and markdownBlock wraps a table's cells and display math,
		// instead of giving the document a sideways scroll of its own
		// (ADR-0022).
		opts := &markdown.Options{Theme: md, WrapCode: true}
		opts.Block = markdownBlock
		markdown.View(c, n.Text, opts)
	case lens.Meter:
		renderMeter(c, n)
	case lens.Progress:
		renderProgress(c, n)
	case lens.Bars:
		renderBars(c, n)
	case lens.Spark:
		renderSpark(c, n)
	case lens.TestSummary:
		renderTestSummary(c, n)
	case lens.Overflow:
		renderOverflow(c, n)
	case lens.Diagnostic:
		renderDiagnostic(c, n, rs)
	case lens.ANSI:
		renderANSI(c, n)
	case lens.Key:
		renderKey(c, n)
	default:
		// A node kind this build does not know: show its Go type so a lens
		// adding one is visible instead of silently empty.
		ui.Text(c, fmt.Sprintf("(unsupported lens node %T)", n)).TextColor(c.Theme().TextMuted)
	}
}

// ---------------------------------------------------------------------------
// Text and spans

// text renders spans as plain text, rich text, or links.
func text(c *ui.Context, spans []lens.Span) ui.Element {
	if len(spans) == 0 {
		return ui.Text(c, "")
	}
	hrefs := false
	for _, s := range spans {
		if s.Href != "" {
			hrefs = true
			break
		}
	}
	if len(spans) == 1 {
		s := spans[0]
		if s.Href != "" {
			return styled(c, ui.Link(c, s.Text, s.Href), s.Style)
		}
		if strings.TrimSpace(s.Style) == "" {
			return ui.Text(c, s.Text)
		}
		return ui.RichText(c, span(c, s))
	}
	if !hrefs {
		us := make([]ui.Span, 0, len(spans))
		for _, s := range spans {
			us = append(us, span(c, s))
		}
		return ui.RichText(c, us...)
	}
	// A mix of links and styled text: RichText accepts inline Text and Link
	// children, so a link's span keeps its click target.
	rt := ui.RichText(c)
	rt.Children(func() {
		for _, s := range spans {
			if s.Href != "" {
				styled(c, ui.Link(c, s.Text, s.Href), s.Style)
				continue
			}
			styled(c, ui.Text(c, s.Text), s.Style)
		}
	})
	return rt
}

// span converts one lens span to a ui.Span with the style tokens applied.
func span(c *ui.Context, s lens.Span) ui.Span {
	t := c.Theme()
	out := ui.Span{Text: s.Text}
	if s.Href != "" {
		out.Color = t.Accent
		out.Underline = true
	}
	for _, tok := range strings.Fields(s.Style) {
		switch tok {
		case "muted":
			out.Color = t.TextMuted
		case "strong":
			out.Weight = 600
		case "em":
			out.Italic = true
		case "accent":
			out.Color = t.Accent
		case "success":
			out.Color = t.Success
		case "warning":
			out.Color = t.Warning
		case "error":
			out.Color = t.Danger
		case "info", "active":
			out.Color = t.Accent
		case "pending":
			out.Color = t.Warning
		case "neutral":
			out.Color = t.Text
		case "code":
			out.Font = monoFamily()
			out.Background = t.Surface
			out.Radius = 3
		case "mono":
			out.Font = monoFamily()
		case "num":
			out.Features = "tnum"
		case "path":
			out.Color = t.Accent
		case "key":
			// A keycap: background and rounded corners (no border in Span).
			out.Background = t.Surface
			out.Radius = 4
			out.Features = "tnum"
		case "link":
			out.Color = t.Accent
			out.Underline = true
		case "del":
			out.Strikethrough = true
			out.Color = t.TextMuted
		case "mark":
			out.Background = t.Selection
			out.Radius = 3
		}
	}
	return out
}

// styled applies style tokens to a whole text element (the RichText children
// fallback used when links and styles are mixed).
func styled(c *ui.Context, e ui.Element, style string) ui.Element {
	t := c.Theme()
	for _, tok := range strings.Fields(style) {
		switch tok {
		case "muted":
			e = e.TextColor(t.TextMuted)
		case "strong":
			e = e.FontWeight(600)
		case "em":
			e = e.Italic()
		case "accent":
			e = e.TextColor(t.Accent)
		case "success":
			e = e.TextColor(t.Success)
		case "warning":
			e = e.TextColor(t.Warning)
		case "error":
			e = e.TextColor(t.Danger)
		case "info":
			e = e.TextColor(t.Accent)
		case "mono":
			e = e.Font(monoFamily())
		case "code":
			e = e.Font(monoFamily()).TextBackground(t.Surface)
		case "num":
			e = e.FontFeatures("tnum")
		case "path", "link":
			e = e.TextColor(t.Accent).Underline()
		case "del":
			e = e.Strikethrough().TextColor(t.TextMuted)
		case "key":
			e = e.Font(monoFamily()).TextBackground(t.Surface)
		case "mark":
			e = e.TextBackground(t.Selection)
		}
	}
	return e
}

// monoFamily is the terminal's configured family list, which the lens body's
// monospace runs (code, tables, keys) draw with: they are terminal text, so
// they follow the same font as the grid. With no terminal font reported, the
// resolved --mono stack is the best available stand-in.
func monoFamily() string {
	if family, _ := surface.TerminalFont(); family != "" {
		return family
	}
	return surface.MonoFamily()
}

// emit reports an event to the lens, when the caller supplied a sink.
func emit(rs *renderState, action, value string) {
	if rs == nil || rs.sink == nil {
		return
	}
	rs.sink(lens.Event{Action: action, Value: value})
}

func plain(spans []lens.Span) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.Text)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Containers

func renderCard(c *ui.Context, n lens.Card, rs *renderState) {
	s := styleFor(c)
	// A card collapses when its head is clicked, and remembers that per position:
	// Terminal's `.sf-card.collapsible` does the same with a chevron that turns.
	key := rs.key("card", plain(n.Head))
	open := collapseGet(key, !n.Collapsed)
	// A neutral card's ring is its tone colour at 16% (20% in the dark appearance),
	// which is the neutral ink: --sf-neutral.
	bg, ring := s.CardBG, s.toneRingT(ui.RGBA(0x18, 0x1a, 0x2c, 1))
	if s.Dark {
		ring = s.toneRingT(ui.RGBA(255, 255, 255, 1))
	}
	if col, ok := toneColor(c, n.Tone); ok {
		bg, ring = s.toneFill(col), s.toneRing(col)
	}
	// Terminal's card surface is --sf-card-bg: white at 55% over the background in the
	// light appearance, 2.8% in the dark one, so the card follows the window instead
	// of being its own opaque colour. A toned card lays the tone at 10% instead.
	cardSurface := withAlpha(ui.RGBA(255, 255, 255, 1), s.CardAlpha)
	if bg != s.CardBG {
		cardSurface = bg // the tone fill replaced it above
	}
	box := ui.Column(c).Radius(s.CardR).Background(cardSurface).
		InsetShadow(0, 0, 0, 1, ring).Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.03))
	box.Children(func() {
		// `.sf-card.collapsible > .sf-card-head` is rounded like the card, and takes
		// --sf-shade while the pointer is over it.
		head := ui.Row(c).Gap(8).MinHeight(s.HeadMinH).Padding(4, 10, 4, 12).
			Radius(s.CardR)
		if head.Hovered() {
			shade := withAlpha(s.T1, 0.028)
			if s.Dark {
				shade = withAlpha(s.T1, 0.03)
			}
			head.Background(shade)
		}
		head.OnClick(func() {
			collapseSet(key, !open)
			c.Invalidate()
		})
		head.Children(func() {
			// `.sf-card-ic`: a 16 DIP box with the 12 DIP drawing in --t3; the arrow
			// turns along the tree.
			ui.Row(c).Width(16).Height(16).Center().Children(func() {
				ui.Icon(c, surface.DisclosureArrow(open)).Width(12).Height(12).TextColor(s.T3)
			})
			if n.Icon != "" {
				if svg, ok := surface.Icon(n.Icon); ok {
					ui.Icon(c, svg).Width(12).Height(12).TextColor(s.T3)
				}
			}
			text(c, n.Head)
			// Terminal's CardProps.meta is dim text after the head text.
			if len(n.Meta) > 0 {
				ui.Text(c, "·").FontSize(s.SmallSize).TextColor(s.T4)
				text(c, n.Meta).FontSize(s.SmallSize).TextColor(s.T3)
			}
			// Terminal's CardProps.end names a child drawn at the head's end instead of
			// the body. Our nodes carry ids only where the shape has one (el/node/button).
			if n.End != "" {
				if i := cardEndChild(n.Children, n.End); i >= 0 {
					renderWith(c, n.Children[i], rs)
				}
			}
		})
		if !open {
			return
		}
		// The body sits under the head's icon column, not under its arrow: Terminal's
		// card body is padded 0 12px 10px 36px for exactly that.
		body := ui.Column(c).Gap(s.InnerGap).Padding(0, 12, 10, s.BodyIndent)
		body.Children(func() {
			for i, ch := range n.Children {
				if n.End != "" && i == cardEndChild(n.Children, n.End) {
					continue // drawn at the head's end
				}
				renderWith(c, ch, rs)
			}
		})
	})
	collapseSet(key, open)
}

// cardEndChild finds the child a card's End prop names: nodes carry ids only
// where the shape has one (el's id attribute, node props, a button id).
func cardEndChild(children []lens.Node, id string) int {
	for i, n := range children {
		switch v := n.(type) {
		case lens.El:
			if v.Attrs["id"] == id {
				return i
			}
		case lens.TSP:
			if s, ok := v.Props["id"].(string); ok && s == id {
				return i
			}
		case lens.Button:
			if v.ID == id {
				return i
			}
		}
	}
	return -1
}
func renderSection(c *ui.Context, n lens.Section, rs *renderState) {
	// Terminal's section is a head and a body, both drawn here rather than by a widget:
	//
	//   .sf-section-head { gap:6px; min-height: var(--sf-lh); color: var(--t2); }
	//   .sf-section.collapsible > .sf-section-head { border-radius:6px;
	//                    margin: 0 -4px; padding: 0 4px; }
	//   .sf-section.collapsible > .sf-section-head:hover { background: var(--l1);
	//                    color: var(--t1); }
	//   .sf-section-body { gap: var(--sf-inner-gap); padding: 2px 0 2px 22px;
	//                    box-shadow: inset 1px 0 0 var(--l2); margin-left: 7px; }
	key := rs.key("section", plain(n.Head))
	open := collapseGet(key, !n.Collapsed)
	s := styleFor(c)
	head := ui.Row(c).Gap(6).MinHeight(s.LH).Margin(0, -4, 0, -4).Padding(0, 4).
		Radius(6)
	hovered := head.Hovered()
	if hovered {
		head.Background(s.L1)
	}
	head.OnClick(func() {
		collapseSet(key, !open)
		c.Invalidate()
	})
	head.Children(func() {
		ui.Icon(c, surface.DisclosureArrow(open)).Width(12).Height(12).TextColor(s.T3)
		col := s.T2
		if hovered {
			col = s.T1
		}
		text(c, n.Head).TextColor(col)
	})
	collapseSet(key, open)
	if !open || len(n.Children) == 0 {
		return
	}
	body := ui.Column(c).Gap(s.InnerGap).Padding(2, 0, 2, 22).
		Margin(0, 0, 0, 7).InsetShadow(1, 0, 0, 0, s.L2)
	body.Children(func() {
		for _, ch := range n.Children {
			renderWith(c, ch, rs)
		}
	})
}

func renderKV(c *ui.Context, n lens.KV) {
	s := styleFor(c)
	keyWidth := kvKeyWidth(n)
	col := ui.Column(c).Gap(s.KVRowGap)
	col.Children(func() {
		for _, item := range n.Items {
			row := ui.Row(c).Gap(s.KVColGap)
			row.Children(func() {
				// Keys are the muted step of the ramp and never wrap; values take
				// the rest of the line, as `.sf-kv-k` / `.sf-kv-v` do.
				ui.Box(c).Width(keyWidth).Children(func() {
					text(c, item[0]).TextColor(s.T3).SingleLine()
				})
				text(c, item[1]).Grow(1)
			})
		}
	})
}

// kvKeyWidth sizes the key column from its widest key instead of a fixed width:
// a long key ("editable project location") must not be cut, and a short one must
// not push its value across the block.
func kvKeyWidth(n lens.KV) float32 {
	cells := 0
	for _, item := range n.Items {
		if w := parse.Width(plain(item[0])); w > cells {
			cells = w
		}
	}
	if cells < 4 {
		cells = 4
	}
	if cells > 40 {
		cells = 40
	}
	return float32(cells*charPx + 8)
}

// ---------------------------------------------------------------------------
// Small status widgets

// toneBadge is Terminal's .sf-badge for a semantic tone: the tone's text on its 10% fill
// inside a 22% ring, 18 DIP tall, 11px.
func toneBadge(c *ui.Context, text string, tone ui.Color) {
	ui.Badge(c, text).MinHeight(18).Padding(0, 7).Radius(999).FontSize(11).
		TextColor(tone).Background(withAlpha(tone, 0.10)).
		InsetShadow(0, 0, 1, 0, withAlpha(tone, 0.22))
}

func renderBadge(c *ui.Context, n lens.Badge) {
	s := styleFor(c)
	// Terminal's badge is a pill: 18 DIP tall, 7 DIP side padding, 11 px medium sans,
	// tone text on a 10% tone fill inside a 22% tone ring.
	fg, bg, ring := s.T2, s.ChipBG, s.L2
	if col, ok := toneColor(c, n.Tone); ok {
		fg, bg, ring = col, withAlpha(col, 0.10), withAlpha(col, 0.22)
	}
	// The Row is what keeps the pill inline: in a column its child would be
	// stretched to the block's width, and the 10% tone fill became a full-width band
	// across the block (the ping view's "0% loss" was one).
	ui.Row(c).Children(func() {
		ui.Badge(c, n.Text).MinHeight(18).Padding(0, 7).Radius(999).
			FontSize(s.SmallSize).FontWeight(500).
			TextColor(fg).Background(bg).InsetShadow(0, 0, 1, 0, ring)
	})
}

func renderMeter(c *ui.Context, n lens.Meter) {
	t := c.Theme()
	var levels *ui.MeterLevels
	if n.Thresholds != nil {
		levels = &ui.MeterLevels{Warning: n.Thresholds.Warn, Critical: n.Thresholds.Bad}
	}
	row := ui.Row(c).Gap(10)
	row.Children(func() {
		if len(n.Parts) > 1 {
			// ui.Meter has no stacked parts (T9 gap): draw the parts as a
			// fixed-width bar of colored segments.
			renderMeterParts(c, n.Parts)
		} else {
			v := n.Value
			if len(n.Parts) == 1 && n.Parts[0].Value != 0 {
				v = n.Parts[0].Value
			}
			meterBar(c, v, levels).Grow(1).MinWidth(120)
		}
		if len(n.Label) > 0 {
			text(c, n.Label)
		}
	})
	_ = t
}

// meterBar draws a proportional bar with Terminal's meter colours: .sf-mt-rail is
// --t1 at 9% and .sf-mt-fill --t1 at 42% (a level that reaches warn/bad takes that
// tone instead).
func meterBar(c *ui.Context, ratio float64, levels *ui.MeterLevels) ui.Element {
	st := styleFor(c)
	value := float32(ratio)
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	rail := withAlpha(st.T1, 0.09)
	fill := withAlpha(st.T1, 0.42)
	if levels != nil {
		switch {
		case levels.Critical > 0 && value >= float32(levels.Critical):
			fill = withAlpha(st.Danger, 0.55)
		case levels.Warning > 0 && value >= float32(levels.Warning):
			fill = withAlpha(st.Warning, 0.55)
		}
	}
	row := ui.Row(c).Height(st.MeterH).Radius(st.MeterR).Background(rail).Clip()
	row.Children(func() {
		if value <= 0.001 {
			ui.Box(c).Height(st.MeterH).Grow(1)
			return
		}
		ui.Box(c).Height(st.MeterH).Grow(value).Radius(st.MeterR).Background(fill)
		ui.Box(c).Height(st.MeterH).Grow(1 - value)
	})
	return row
}

func renderMeterParts(c *ui.Context, parts []lens.MeterPart) {
	total := 0.0
	for _, p := range parts {
		total += p.Value
	}
	st := styleFor(c)
	bar := ui.Row(c).Gap(2).Height(st.MeterH).Width(st.MeterW)
	bar.Children(func() {
		if total <= 0 {
			ui.Box(c).Height(st.MeterH).Grow(1).Radius(st.MeterR).Background(withAlpha(st.T1, 0.09))
			return
		}
		for _, p := range parts {
			w := float32(p.Value / total * float64(st.MeterW))
			if w < 1 {
				w = 1
			}
			col, ok := toneColor(c, p.Token)
			if !ok {
				col = c.Theme().Accent
			}
			ui.Box(c).Width(w).Height(6).Radius(3).Background(col)
		}
	})
}

func t2Surface(c *ui.Context) ui.Color { return c.Theme().Surface }

func renderProgress(c *ui.Context, n lens.Progress) {
	v := n.Value
	if !n.Known {
		v = -1 // ui.Progress's indeterminate bar
	}
	row := ui.Row(c).Gap(10)
	row.Children(func() {
		ui.Progress(c, v).Grow(1).MinWidth(120)
		if len(n.Label) > 0 {
			text(c, n.Label)
		}
	})
}

func renderTestSummary(c *ui.Context, n lens.TestSummary) {
	t := c.Theme()
	row := ui.Row(c).Gap(8)
	row.Children(func() {
		if n.Passed > 0 {
			toneBadge(c, fmt.Sprintf("✓ %d", n.Passed), styleFor(c).Success)
		}
		if n.Failed > 0 {
			toneBadge(c, fmt.Sprintf("✗ %d", n.Failed), styleFor(c).Danger)
		}
		if n.Skipped > 0 {
			toneBadge(c, fmt.Sprintf("⋯ %d", n.Skipped), styleFor(c).T3)
		}
		if n.Took != "" {
			ui.Text(c, n.Took).TextColor(t.TextMuted).FontFeatures("tnum")
		}
	})
}

func renderOverflow(c *ui.Context, n lens.Overflow) {
	if n.Hidden <= 0 {
		return
	}
	// Terminal's own wording (strings 791490-791492): the count with "lines over this
	// command's output limit are taken out here", then "click for the whole
	// output". One line, so a long block does not grow a paragraph.
	t := c.Theme()
	unit := "lines"
	if n.Hidden == 1 {
		unit = "line"
	}
	ui.Text(c, fmt.Sprintf("%d %s over this command's output limit are taken out here · click for the whole output",
		n.Hidden, unit)).
		TextColor(t.TextMuted).FontSize(t.FontSize - 1)
}

// renderDiagnostic draws one diagnostic the way Terminal's `flow.ui.diagnostic`
// composes it (strings 801145-801164): a severity icon and code badge, the
// message, a clickable path:line:col, the snippet as code starting at its first
// line, then notes and suggested edits.
func renderDiagnostic(c *ui.Context, d lens.Diagnostic, rs *renderState) {
	t := c.Theme()
	sev := d.Severity
	if sev == "" {
		sev = lens.SeverityError
	}
	tone := diagnosticTone(sev)
	ui.Column(c).Gap(3).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			if col, ok := toneColor(c, tone); ok {
				ui.Text(c, diagnosticIcon(sev)).TextColor(col).FontSize(t.FontSize)
			}
			ui.Text(c, sev).TextColor(t.TextMuted).FontSize(t.FontSize - 1)
			if d.Code != "" {
				b := ui.Badge(c, d.Code)
				if col, ok := toneColor(c, tone); ok {
					b = b.Background(col).TextColor(t.Background)
				}
			}
			if loc := diagnosticLocation(d); loc != "" {
				ui.Text(c, loc).TextColor(t.TextMuted).FontSize(t.FontSize - 1).Selectable()
				ui.Link(c, "open", diagnosticHref(d))
			}
		})
		if d.Message != "" {
			ui.Text(c, d.Message).FontSize(t.FontSize).Selectable()
		}
		if d.Snippet != "" {
			renderCode(c, lens.Code{Text: d.Snippet, Start: d.SnippetFirst, Clamp: 24}, rs)
		}
		for _, note := range d.Notes {
			ui.Text(c, note).TextColor(t.TextMuted).FontSize(t.FontSize - 1)
		}
		for _, s := range d.Suggestions {
			if s.Message != "" {
				ui.Text(c, "suggestion: "+s.Message).TextColor(t.TextMuted).FontSize(t.FontSize - 1)
			}
			if s.Code != "" {
				renderCode(c, lens.Code{Text: s.Code, Start: s.FirstLine, Clamp: 12}, rs)
			}
		}
	})
}

// diagnosticTone maps a severity to the view's tone tokens, as Terminal maps
// `severity` to a tone.
func diagnosticTone(sev string) string {
	switch sev {
	case lens.SeverityWarning:
		return "warning"
	case lens.SeverityNote:
		return "info"
	case lens.SeverityHelp:
		return "accent"
	}
	return "error"
}

func diagnosticIcon(sev string) string {
	switch sev {
	case lens.SeverityWarning:
		return "⚠"
	case lens.SeverityNote:
		return "ℹ"
	case lens.SeverityHelp:
		return "?"
	}
	return "✕"
}

// diagnosticLocation is the "path:line:col" text Terminal prints under the badge.
func diagnosticLocation(d lens.Diagnostic) string {
	p := d.Path
	if p == "" && d.Line == 0 {
		return ""
	}
	if d.Line > 0 {
		p += ":" + itoa(d.Line)
		if d.Col > 0 {
			p += ":" + itoa(d.Col)
		}
	}
	return p
}

func diagnosticHref(d lens.Diagnostic) string {
	if d.Path == "" {
		return ""
	}
	p := strings.ReplaceAll(d.Path, "\\", "/")
	if !strings.HasPrefix(p, "/") && !(len(p) > 1 && p[1] == ':') {
		p = "/" + p
	}
	if len(p) > 1 && p[1] == ':' {
		p = "/" + strings.Replace(p, ":", "%3A", 1)
	}
	if d.Line > 0 {
		p += "#L" + itoa(d.Line)
	}
	return "file://" + p
}

func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func renderKey(c *ui.Context, n lens.Key) {
	t := c.Theme()
	ui.Box(c).Radius(4).Padding(0, 6).Background(t.Surface).
		InsetShadow(0, 0, 1, 0, t.Border).
		Children(func() {
			ui.Text(c, n.Text).Font(monoFamily()).FontFeatures("tnum")
		})
}

// ---------------------------------------------------------------------------
// Table

func renderTable(c *ui.Context, n lens.Table, rs *renderState) {
	if len(n.Cols) == 0 {
		return
	}
	width, _ := c.Size()
	visible, budget := tableLayout(n.Cols, n.Rows, width-80)
	widths := make([]int, 0, len(visible)) // cells per visible column
	for k := range visible {
		widths = append(widths, budget[k])
	}
	// Terminal's table is a header over a hairline and rows of the same column
	// template: 24 DIP header in 11.5 px medium over `--l2`, cells padded 7 DIP and
	// cut with an ellipsis, rows one line-height plus 6 tall.
	s := styleFor(c)
	// A column wide enough for its widest cell, and only the growing columns take
	// the rest of the row: Terminal measures its columns the same way (with a hidden
	// probe row) and leaves the spare width to the column that asks for it, instead
	// of stretching every column in proportion and opening gaps.
	natural := make([]float32, len(visible))
	for k := range visible {
		w := displayWidth(n.Cols[visible[k]].Head)
		for r := range n.Rows {
			if cw := cellWidth(n.Rows[r][visible[k]]); cw > w {
				w = cw
			}
		}
		if w < 4 {
			w = 4
		}
		natural[k] = float32(w)
	}
	columnBox := func(k int) ui.Element {
		col := n.Cols[visible[k]]
		cell := ui.Box(c).Padding(0, s.CellPadX).ClipX()
		if col.Grow > 0 {
			return cell.Grow(natural[k])
		}
		// One cell of slack: a cell of proportional text is not exactly one mono cell wide.
		return cell.Width((natural[k] + 1) * s.CharW)
	}
	head := ui.Row(c).Gap(0).MinHeight(24).Margin(0, -s.CellPadX, 0, -s.CellPadX)
	head.Children(func() {
		for k := range visible {
			col := n.Cols[visible[k]]
			columnBox(k).Children(func() {
				ui.Text(c, col.Head).FontSize(s.SmallSize).FontWeight(500).
					TextColor(s.T3).SingleLine()
			})
		}
	})
	ui.Box(c).FillWidth().Height(1).Background(s.L2).Margin(0, -s.CellPadX, 0, -s.CellPadX)
	if len(n.Rows) == 0 {
		// Terminal's TableOpts.empty: shown muted and centred when there are no rows.
		if len(n.Opts.Empty) > 0 {
			ui.Row(c).FillWidth().Height(s.RowH * 2).Center().Children(func() {
				text(c, n.Opts.Empty).FontSize(s.SmallSize).TextColor(s.T3)
			})
		}
		return
	}
	tableKey := stateKey(rs.scope, "table", 0)
	if len(n.Rows) > 0 && len(visible) > 0 && len(n.Rows[0]) > visible[0] {
		tableKey = stateKey(rs.scope, "table:"+fingerprint(plain(n.Rows[0][visible[0]].Spans)), 0)
	}
	for r := range n.Rows {
		// .sf-tbl-row follows the table's negative margin, and every cell but the
		// last row's carries a --l1 hairline under it.
		row := ui.Row(c).Gap(0).MinHeight(s.RowH).Margin(0, -s.CellPadX, 0, -s.CellPadX)
		if r < len(n.Rows)-1 {
			row.InsetShadow(0, -1, 0, 0, s.L1)
		}
		var opts lens.RowOpts
		if r < len(n.RowOpts) {
			opts = n.RowOpts[r]
		}
		rowID := opts.ID
		if rowID == "" && len(visible) > 0 && len(n.Rows[r]) > visible[0] {
			rowID = plain(n.Rows[r][visible[0]].Spans)
		}
		rowKey := "row:" + fingerprint(rowID)
		selected := (n.Opts.Selected != "" && n.Opts.Selected == rowID) || selectGet(tableKey) == rowKey
		if selected {
			row = s.selectedStyle(c, row)
		}
		if opts.Title != "" {
			row.Tooltip(opts.Title)
		}
		if opts.Disabled {
			// Terminal: disabled rows are dimmed, take no select/activate/menu, and a
			// string reason is the tooltip. The tooltip is all we can honour here.
			if opts.Reason != "" {
				row.Tooltip(opts.Reason)
			}
		} else if n.Opts.Selectable {
			row.OnClick(func() {
				// Terminal sends select on a click and activate on a double click; the ui
				// layer has single clicks only, so a second click on the selected row
				// is the activate gesture.
				if selected {
					emitRow(rs, "activate", n.ID, rowID)
				} else {
					selectSet(tableKey, rowKey)
					emitRow(rs, "select", n.ID, rowID)
				}
				c.Invalidate()
			})
		}
		row.Children(func() {
			for k := range visible {
				i := visible[k]
				col := n.Cols[i]
				columnBox(k).Children(func() {
					renderCellNode(c, n.Rows[r][i], col, widths[k], n.ID, rowID, rs)
				})
			}
			// Terminal's TableRow.menu is a right-click menu; the ui layer has no context
			// menu, so the actions are drawn as small chips at the row's end.
			if !opts.Disabled {
				for _, act := range opts.Menu {
					a := act
					chip := ui.Text(c, a).FontSize(s.SmallSize).TextColor(s.T3).
						Padding(0, 6).Radius(4).Background(s.ChipBG)
					chip.OnClick(func() {
						emitAction(rs, n.ID, rowID, a)
						c.Invalidate()
					})
				}
			}
		})
	}
}

// visibleColumns hides low-priority columns first when the block is narrow.
// ui.TableColumn has no priority field (see findings/mygo-ui-mapping.md), so
// the width each column needs is estimated from its header.
// charPx is the estimated advance of one monospace cell at the default font
// size; the table budget below is expressed in cells and converted back.
const charPx = 7

// tableLayout decides which columns to show (dropping the lowest Priority first)
// and how many cells each gets. It replaces the header-only estimate so
// TruncateStart/TruncateMiddle have a real width to cut at.
func tableLayout(cols []lens.Col, rows [][]lens.Cell, avail float32) ([]int, []int) {
	idx := make([]int, len(cols))
	natural := make([]int, len(cols))
	for i := range idx {
		idx[i] = i
		natural[i] = displayWidth(cols[i].Head)
		for r := range rows {
			if i >= len(rows[r]) {
				continue
			}
			w := cellWidth(rows[r][i])
			if w > natural[i] {
				natural[i] = w
			}
		}
		if natural[i] > 60 {
			natural[i] = 60
		}
		if natural[i] < 4 {
			natural[i] = 4
		}
	}
	total := 0
	for _, w := range natural {
		total += w
	}
	availCells := 0
	if avail > 0 {
		availCells = int(avail / charPx)
	}
	if availCells <= 0 || total <= availCells {
		return idx, natural
	}
	// Not enough room: drop the lowest priority columns first.
	order := append([]int(nil), idx...)
	sort.SliceStable(order, func(a, b int) bool {
		return cols[order[a]].Priority < cols[order[b]].Priority
	})
	hidden := make(map[int]bool, len(order))
	for _, i := range order {
		if total <= availCells {
			break
		}
		total -= natural[i]
		hidden[i] = true
	}
	kept := make([]int, 0, len(idx))
	budget := make([]int, 0, len(idx))
	for _, i := range idx {
		if !hidden[i] {
			kept = append(kept, i)
			budget = append(budget, natural[i])
		}
	}
	if len(kept) == 0 {
		return []int{0}, []int{availCells}
	}
	// Shrink the widest columns (grow columns first) until the table fits.
	for {
		sum := 0
		for _, w := range budget {
			sum += w
		}
		if sum <= availCells {
			break
		}
		worst, worstW := -1, 0
		for k, i := range kept {
			if cols[i].Grow <= 0 && len(kept) > 1 {
				continue
			}
			if budget[k] > worstW {
				worst, worstW = k, budget[k]
			}
		}
		if worst < 0 || budget[worst] <= 4 {
			break
		}
		budget[worst]--
	}
	return kept, budget
}

// displayWidth is the number of cells a string occupies (wide chars count 2).
func displayWidth(s string) int { return parse.Width(s) }

func cellWidth(c lens.Cell) int {
	switch {
	case c.Meter != nil:
		return 12
	case c.Spark != nil:
		return 8 // eight cells wide, as Terminal draws a sparkline cell
	case c.Button != nil:
		return parse.Width(c.Button.Text) + 4
	case c.Badge != nil:
		return parse.Width(c.Badge.Text) + 3
	case c.Text != nil:
		n := 4 // the mark/dot and its gap
		for _, s := range c.Text.Text {
			n += parse.Width(s.Text)
		}
		return n
	}
	n := 0
	for _, s := range c.Spans {
		n += parse.Width(s.Text)
	}
	return n
}

// truncateSpans cuts spans to limit cells using Terminal's truncation mode: end
// keeps the head, start keeps the tail, middle keeps both with an ellipsis.
func truncateSpans(spans []lens.Span, limit int, mode lens.Truncate) []lens.Span {
	if limit <= 0 || len(spans) == 0 {
		return spans
	}
	text := make([]rune, 0, 64)
	styles := make([]lens.Span, 0, len(spans))
	for _, s := range spans {
		for _, r := range s.Text {
			text = append(text, r)
			cp := s
			cp.Text = ""
			styles = append(styles, cp)
		}
	}
	if parse.Width(string(text)) <= limit {
		return spans
	}
	// The ellipsis is part of the budget, as in Terminal's truncation: the result is
	// at most `limit` cells wide.
	var keep []rune
	switch mode {
	case lens.TruncateStart:
		keep = append([]rune("…"), tailCells(text, limit-1)...)
	case lens.TruncateMiddle:
		head := headCells(text, (limit-1)/2)
		tail := tailCells(text, limit-1-len(head))
		keep = append(append(append([]rune{}, head...), '…'), tail...)
	default:
		keep = append(headCells(text, limit-1), '…')
	}
	return []lens.Span{{Text: string(keep), Style: spanStyleAt(styles, 0)}}
}

func headCells(text []rune, n int) []rune {
	out := make([]rune, 0, n)
	w := 0
	for _, r := range text {
		rw := parse.RuneWidth(r)
		if w+rw > n {
			break
		}
		out = append(out, r)
		w += rw
	}
	return out
}

func tailCells(text []rune, n int) []rune {
	out := make([]rune, 0, n)
	w := 0
	for i := len(text) - 1; i >= 0; i-- {
		rw := parse.RuneWidth(text[i])
		if w+rw > n {
			break
		}
		out = append([]rune{text[i]}, out...)
		w += rw
	}
	return out
}

func spanStyleAt(styles []lens.Span, i int) string {
	if i >= 0 && i < len(styles) {
		return styles[i].Style
	}
	return ""
}

func visibleColumns(cols []lens.Col, avail float32) []int {
	idx := make([]int, len(cols))
	for i := range idx {
		idx[i] = i
	}
	if avail <= 0 {
		return idx
	}
	// Higher priority first: keep while the estimate fits.
	sort.SliceStable(idx, func(a, b int) bool {
		return cols[idx[a]].Priority > cols[idx[b]].Priority
	})
	kept := make([]int, 0, len(idx))
	used := float32(0)
	for _, i := range idx {
		need := float32(80 + len(cols[i].Head)*7)
		if used+need > avail && len(kept) > 0 {
			continue
		}
		used += need
		kept = append(kept, i)
	}
	sort.Ints(kept) // back to the tree's order
	return kept
}

func renderMeterCell(c *ui.Context, m lens.MeterCell) {
	var levels *ui.MeterLevels
	if m.Thresholds != nil {
		levels = &ui.MeterLevels{Warning: m.Thresholds.Warn, Critical: m.Thresholds.Bad}
	}
	if len(m.Parts) > 1 {
		renderMeterParts(c, m.Parts)
		return
	}
	meterBar(c, m.Value, levels).MinWidth(60)
}

// renderCellNode draws one table cell, as Terminal's Cell union: spans, a meter,
// text after a mark, a badge, a button or a sparkline.
func renderCellNode(c *ui.Context, cell lens.Cell, col lens.Col, limit int, tableID, rowID string, rs *renderState) {
	switch {
	case cell.Meter != nil:
		renderMeterCell(c, *cell.Meter)
	case cell.Text != nil:
		s := styleFor(c)
		ui.Row(c).Gap(4).Children(func() {
			if cell.Text.Icon != "" && cell.Text.Dot == "" {
				iconTone := s.T3
				if cc, ok := toneColor(c, cell.Text.Tone); ok {
					iconTone = cc
				}
				if svg, ok := surface.Icon(cell.Text.Icon); ok {
					ui.Icon(c, svg).Width(12).Height(12).TextColor(iconTone)
				}
			}
			if cell.Text.Dot != "" {
				dot := s.T3
				if cc, ok := toneColor(c, cell.Text.Dot); ok {
					dot = cc
				}
				ui.Box(c).Width(6).Height(6).Radius(999).Background(dot)
			}
			if len(cell.Text.Text) > 0 {
				sp := cell.Text.Text
				if limit > 0 {
					sp = truncateSpans(sp, limit, col.Truncate)
				}
				text(c, sp).SingleLine().Ellipsis("…")
			}
		})
	case cell.Badge != nil:
		renderBadge(c, lens.Badge{Text: cell.Badge.Text, Tone: cell.Badge.Tone})
	case cell.Button != nil:
		bc := cell.Button
		renderButtonWith(c, lens.Button{
			Text: bc.Text, Icon: bc.Icon, Keys: bc.Keys, Variant: bc.Variant,
			Size: "sm", Disabled: bc.Disabled, Reason: bc.Reason, Act: bc.Act,
		}, tableID, rowID, rs)
	case cell.Spark != nil:
		renderSparkCell(c, *cell.Spark)
	default:
		spans := cell.Spans
		if limit > 0 {
			spans = truncateSpans(spans, limit, col.Truncate)
		}
		switch col.Align {
		case lens.AlignEnd:
			ui.Row(c).Grow(1).Children(func() {
				ui.Spacer(c)
				text(c, spans).SingleLine().Ellipsis("…")
			})
		case lens.AlignCenter:
			ui.Row(c).Grow(1).Children(func() {
				ui.Spacer(c)
				text(c, spans).SingleLine().Ellipsis("…")
				ui.Spacer(c)
			})
		default:
			text(c, spans).SingleLine().Ellipsis("…").FillWidth()
		}
	}
}

// renderSparkCell is Terminal's SparkCell: a line the height of the text, 84 DIP
// wide (about eight cells), with the cell's tone and optional domain.
func renderSparkCell(c *ui.Context, sp lens.SparkCell) {
	if len(sp.Values) < 2 {
		return
	}
	col := c.Theme().Accent
	if cc, ok := toneColor(c, sp.Tone); ok {
		col = cc
	}
	lo, hi := sp.Values[0], sp.Values[0]
	for _, v := range sp.Values {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	if sp.Min != nil && *sp.Min < lo {
		lo = *sp.Min
	}
	if sp.Max != nil && *sp.Max > hi {
		hi = *sp.Max
	}
	if hi == lo {
		hi = lo + 1
	}
	vals := sp.Values
	ui.Box(c).Width(84).Height(14).Draw(func(p *ui.Painter, r ui.Rect) {
		xs := make([]float32, len(vals))
		ys := make([]float32, len(vals))
		n1 := float32(len(vals) - 1)
		for i, v := range vals {
			xs[i] = float32(i) / n1
			ys[i] = float32((v - lo) / (hi - lo))
		}
		p.StrokePath(chart.Line(r, xs, ys, chart.CurveLinear), 1.2, col)
	})
}

// ---------------------------------------------------------------------------
// Tree and list

func renderTree(c *ui.Context, n lens.Tree) {
	var build func(nodes []lens.TreeNode)
	build = func(nodes []lens.TreeNode) {
		for i := range nodes {
			nd := nodes[i]
			open := nd.Open // frame-local: see the package comment on gaps
			var kids func()
			if len(nd.Children) > 0 {
				kids = func() { build(nd.Children) }
			}
			// ui.TreeItem takes a plain label and has no icon slot, so an icon
			// name becomes a leading glyph in the label.
			label := plain(nd.Label)
			if g, _ := iconGlyph(nd.Icon); g != "" {
				label = g + " " + label
			}
			_ = ui.TreeItem(c, label, &open, kids)
		}
	}
	ui.Tree(c, func() { build(n.Nodes) })
}

func renderList(c *ui.Context, n lens.List, rs *renderState) {
	s := styleFor(c)
	// The key is content-derived, not walk-order-derived: the lens rebuilds its
	// tree every frame, so a counter would move the filter and the choice from
	// one list to another.
	listKey := stateKey(rs.scope, "list", 0)
	if len(n.Items) > 0 {
		listKey = stateKey(rs.scope, "list:"+fingerprint(plain(n.Items[0].Label)), 0)
	}
	items := n.Items
	if n.Filter {
		ptr := filterText(listKey)
		// Terminal's .sf-input: --sf-lh + 8 tall, 8 of side padding, on --l1 inside a
		// --l1 ring; focused it goes transparent with the accent ring, and the prompt
		// (.sf-ed-prompt) is the field's placeholder in the muted step.
		ti := ui.TextInput(c, ptr).Placeholder("filter").
			Height(s.LH+8).Padding(0, 8).Radius(6).
			Background(s.L1).InsetShadow(0, 0, 0, 1, s.L1).
			Width(360)
		if ti.Focused() {
			ti.Background(ui.RGBA(0, 0, 0, 0)).
				InsetShadow(0, 0, 0, 1, s.Accent)
		}
		filterNote(rs, listKey)
		if q := strings.TrimSpace(*ptr); q != "" {
			items = filterItems(n.Items, q)
		}
		if len(items) == 0 && len(n.Items) > 0 {
			ui.Text(c, "No matches").TextColor(s.T3).FontSize(s.SmallSize).Padding(2, 10)
			return
		}
	}
	// Rows may repeat a label — a listing can hold two equal names, and an
	// icon-only row repeats its glyph — so the occurrence among equal labels
	// keeps repeated rows apart when one is chosen.
	labels := make([]string, len(items))
	nths := make([]int, len(items))
	seen := make(map[string]int, len(items))
	for i := range items {
		label := plain(items[i].Label)
		labels[i] = label
		nths[i] = seen[label]
		seen[label]++
	}
	// Every row goes into the flow: the list has no scroll box of its own, so
	// the terminal's scroll reaches the end of a long listing. Focus stays with
	// the flow too — the arrow keys scroll the terminal, they do not move a
	// selection inside the widget.
	t := c.Theme()
	ui.Column(c).Gap(0).FillWidth().Children(func() {
		for i := range items {
			it, label, nth := items[i], labels[i], nths[i]
			rowKey := "row:" + fingerprint(label) + ":" + itoa(nth)
			selected := selectGet(listKey) == rowKey
			row := ui.Row(c).Gap(8).MinHeight(s.RowH).Padding(0, 8, 0, 10)
			// Hover is Terminal's `.sf-list .sf-item:hover` hairline tint; the chosen row
			// keeps its accent, which wins over it.
			if row.Hovered() {
				row = row.Background(s.L1)
			}
			if selected {
				row = s.selectedStyle(c, row)
			}
			// A click chooses a row and a second click on the chosen row activates
			// it, as a table's rows do; the lens is told and rebuilds its view.
			row.OnClick(func() {
				if selected {
					emit(rs, "activate", label)
				} else {
					selectSet(listKey, rowKey)
					emit(rs, "select", label)
				}
				c.Invalidate()
			})
			row.Children(func() {
				// Terminal gives list items an icon name (e.g. "failed", "package", a
				// severity); ui has no SVG slot for a name, so a glyph carries it.
				if g, tone := iconGlyph(it.Icon); g != "" {
					st := ui.Text(c, g).Font(monoFamily())
					if col, ok := toneColor(c, tone); ok {
						st = st.TextColor(col)
					}
				}
				text(c, it.Label)
				if len(it.Detail) > 0 {
					ui.Spacer(c)
					ui.Text(c, plain(it.Detail)).TextColor(t.TextMuted)
				}
				if len(it.Value) > 0 {
					ui.Spacer(c)
					text(c, it.Value)
				}
			})
		}
	})
}

// ---------------------------------------------------------------------------
// Markdown

// markdownBlock takes the blocks MyGo's markdown view builds inside a
// ui.ScrollHorizontal and draws them in the flow instead (ADR-0022): a table
// wraps its cells at column widths that fit the view, and display math wraps
// its TeX. markdown.Options has no hook for either, so Options.Block takes
// them over; every other block renders as MyGo renders it.
func markdownBlock(c *ui.Context, d *parser.Document, i int) bool {
	switch d.Block(i).Kind {
	case parser.Table:
		renderMarkdownTable(c, d, i)
		return true
	case parser.MathBlock:
		tex := strings.TrimRight(d.Lines(i), "\n")
		ui.Text(c, tex).Font(monoFamily()).TextColor(c.Theme().TextMuted).
			FillWidth().TextAlign(ui.Center).Selectable()
		return true
	}
	return false
}

// renderMarkdownTable draws a Markdown table as a header over a hairline and
// rows of wrapping cells. The columns are as wide as their widest cell, up to
// a readable cap, and share the view's width when they do not fit, so a wide
// table wraps instead of panning sideways inside the document.
func renderMarkdownTable(c *ui.Context, d *parser.Document, i int) {
	tbl := d.Block(i)
	n := len(tbl.Align)
	if n == 0 {
		return
	}
	var head [][]lens.Span
	var rows [][][]lens.Span
	for r := i + 1; r < tbl.Next; r = d.Block(r).Next {
		line := d.Block(r)
		cells := make([][]lens.Span, 0, n)
		for cell := r + 1; cell < line.Next && len(cells) < n; cell = d.Block(cell).Next {
			cells = append(cells, markdownCellSpans(d, cell))
		}
		for len(cells) < n {
			cells = append(cells, nil)
		}
		if line.Header && head == nil {
			head = cells
			continue
		}
		rows = append(rows, cells)
	}
	if head == nil {
		head = make([][]lens.Span, n)
	}
	s := styleFor(c)
	width, _ := c.Size()
	widths := markdownColWidths(s, width, head, rows)
	ui.Column(c).Selectable().Children(func() {
		markdownTableRow(c, s, head, tbl.Align, widths, true)
		ui.Box(c).FillWidth().Height(1).Background(s.L2)
		for r := range rows {
			row := markdownTableRow(c, s, rows[r], tbl.Align, widths, false)
			if r < len(rows)-1 {
				row.InsetShadow(0, -1, 0, 0, s.L1)
			}
		}
	})
}

// markdownColWidths sizes a table's columns: their content's width in cells,
// capped, sharing the view's width when the sum is too wide.
func markdownColWidths(s style, width float32, head [][]lens.Span, rows [][][]lens.Span) []float32 {
	n := len(head)
	natural := make([]int, n)
	for k := 0; k < n; k++ {
		natural[k] = max(displayWidth(plain(head[k])), 4)
		for r := range rows {
			if k < len(rows[r]) {
				natural[k] = max(natural[k], displayWidth(plain(rows[r][k])))
			}
		}
		natural[k] = min(natural[k], 40)
	}
	total := 0
	for _, w := range natural {
		total += w
	}
	if total == 0 {
		total = n
	}
	scale := float32(1)
	if avail := width - 4*s.CellPadX; width > 0 && float32(total)*s.CharW > avail {
		scale = avail / (float32(total) * s.CharW)
	}
	// A table of many columns in a narrow view still has to fit: the floor
	// gives way before the table runs past the view's width.
	floor := float32(6)
	if width > 0 {
		floor = max(min(floor, (width-4*s.CellPadX)/(float32(n)*s.CharW)), 2)
	}
	out := make([]float32, n)
	for k := range out {
		out[k] = max(float32(natural[k])*s.CharW*scale, floor*s.CharW)
	}
	return out
}

// markdownTableRow draws one row of a Markdown table. A header row is muted,
// strong and small; every cell wraps at its column's width.
func markdownTableRow(c *ui.Context, s style, cells [][]lens.Span, aligns []parser.Align, widths []float32, header bool) ui.Element {
	row := ui.Row(c).Gap(0).AlignItems(ui.Start)
	row.Children(func() {
		for k := range widths {
			var spans []lens.Span
			if k < len(cells) {
				spans = cells[k]
			}
			if header {
				spans = markdownHeaderSpans(spans)
			}
			ui.Box(c).Width(widths[k]).Padding(2, s.CellPadX).ClipX().Children(func() {
				el := text(c, spans).TextAlign(markdownAlign(aligns[k]))
				if header {
					el = el.FontSize(s.SmallSize)
				}
			})
		}
	})
	return row
}

// markdownHeaderSpans makes a header cell strong and muted, as a Markdown
// table's header reads.
func markdownHeaderSpans(spans []lens.Span) []lens.Span {
	out := make([]lens.Span, len(spans))
	for k, s := range spans {
		s.Style = markdownStyle(s.Style, "strong muted")
		out[k] = s
	}
	return out
}

// markdownCellSpans turns a table cell's inlines into lens spans, keeping
// emphasis, code, links and math; an image shows its description, as it does
// within MyGo's text.
func markdownCellSpans(d *parser.Document, cell int) []lens.Span {
	in := d.Inlines(cell)
	var out []lens.Span
	var walk func(from, to int, style, href string)
	walk = func(from, to int, style, href string) {
		for k := from; k < to; k++ {
			n := in[k]
			next := max(n.Next, k+1)
			switch n.Kind {
			case parser.Text, parser.HTML:
				out = append(out, lens.Span{Text: n.Text, Style: style, Href: href})
			case parser.SoftBreak, parser.LineBreak:
				out = append(out, lens.Span{Text: " ", Style: style})
			case parser.Code:
				out = append(out, lens.Span{Text: n.Text, Style: markdownStyle(style, "code"), Href: href})
			case parser.Math:
				out = append(out, lens.Span{Text: n.Text, Style: markdownStyle(style, "code")})
			case parser.Emphasis:
				walk(k+1, next, markdownStyle(style, "em"), href)
			case parser.Strong:
				walk(k+1, next, markdownStyle(style, "strong"), href)
			case parser.Strikethrough:
				walk(k+1, next, markdownStyle(style, "del"), href)
			case parser.Link:
				walk(k+1, next, markdownStyle(style, "link"), n.Dest)
			case parser.Image:
				alt := parser.PlainText(in[k+1 : next])
				if alt == "" {
					alt = "image"
				}
				out = append(out, lens.Span{Text: "[" + alt + "]", Style: markdownStyle(style, "muted")})
			case parser.FootnoteRef:
				out = append(out, lens.Span{Text: "[" + n.Text + "]", Style: markdownStyle(style, "muted")})
			}
			if n.Next > k+1 {
				k = n.Next - 1
			}
		}
	}
	walk(0, len(in), "", "")
	return out
}

// markdownStyle adds a style token to a span's.
func markdownStyle(style, add string) string {
	if style == "" {
		return add
	}
	return style + " " + add
}

// markdownAlign maps a Markdown table column's alignment to the text's.
func markdownAlign(a parser.Align) ui.Align {
	switch a {
	case parser.AlignCenter:
		return ui.Center
	case parser.AlignRight:
		return ui.End
	}
	return ui.Start
}

// ---------------------------------------------------------------------------
// Code and diff

func renderCode(c *ui.Context, n lens.Code, rs *renderState) {
	text := n.Text
	hidden := 0
	// "Show all lines" is clickable and remembered per position (Terminal's code
	// block expands on click, strings 827046-827047).
	key := rs.key("code", itoa(n.Start)+":"+n.Text)
	if n.Clamp > 0 && !expandGet(key) {
		lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		if len(lines) > n.Clamp {
			hidden = len(lines) - n.Clamp
			text = strings.Join(lines[:n.Clamp], "\n")
		}
	}
	s := styleFor(c)
	// The body follows its text. A fixed cap here cut long code silently: the
	// editor inside was clipped with no way to reach the rest. The lens's own
	// Clamp ("N more lines · Show all lines") is where a limit belongs, because
	// it is stated and can be lifted. The editor fits its content, wrapped
	// lines included, and paints no bars (see codeBufferFor), so the body takes
	// the height the editor asks for instead of one counted from line breaks.
	ui.Column(c).Radius(s.CodeR).Background(s.CodeBG).InsetShadow(0, 0, 1, 0, s.L1).Children(func() {
		if n.Lang != "" {
			ui.Row(c).Gap(6).MinHeight(26).Padding(0, 10).Children(func() {
				ui.Text(c, n.Lang).Font(monoFamily()).FontSize(s.SmallSize).
					FontWeight(500).TextColor(s.T3)
			})
			ui.Box(c).FillWidth().Height(1).Background(s.L1)
		}
		ui.Box(c).FillWidth().Padding(4, 0).Children(func() {
			// Keep the editor state across frames: a lens rebuilds its node tree
			// every frame, and a fresh Buffer would drop folds and the selection.
			// The entry is reused while the text is unchanged and replaced when
			// it changes.
			_, st := codeBufferFor(key, text, n.Lang)
			if n.Start > 1 {
				// The editor's gutter numbers from 1, with no offset; a snippet
				// that starts elsewhere gets the numbers drawn beside it, one
				// box per line, as high as the rows the line takes. Folding is
				// off: numbers are drawn for every line, and a fold would hide
				// lines the gutter still counts.
				st.LineNumbers = editor.LineNumbersOff
				st.NoFolding = true
				ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
					renderGutter(c, st, text, n.Start)
					ui.Box(c).Grow(1).Children(func() {
						editor.View(c, st).FillWidth()
					})
				})
				return
			}
			editor.View(c, st).FillWidth()
		})
	})
	if hidden > 0 {
		// Terminal's code block says "N more lines" with a "Show all lines" control
		// (strings 827046-827047); the block-level output-limit sentence is for
		// the command's own truncation, not for a lens clamp.
		unit := "lines"
		if hidden == 1 {
			unit = "line"
		}
		notice := ui.Text(c, fmt.Sprintf("%d more %s · Show all lines", hidden, unit)).
			TextColor(c.Theme().TextMuted).FontSize(c.Theme().FontSize - 1)
		notice.OnClick(func() {
			expandSet(key, true)
			c.Invalidate()
		})
	} else if n.Clamp > 0 && expandGet(key) {
		more := ui.Text(c, "Show fewer lines").
			TextColor(c.Theme().TextMuted).FontSize(c.Theme().FontSize - 1)
		more.OnClick(func() {
			expandSet(key, false)
			c.Invalidate()
		})
	}
}

// renderGutter draws the numbers of a snippet that starts at start, beside an
// editor that numbers from 1. Each number sits in a box as high as the rows
// its line takes, as the editor laid them out, so a soft-wrapped line keeps
// its number on its first row instead of pushing the numbers below it out of
// step.
func renderGutter(c *ui.Context, st *editor.State, text string, start int) {
	th := c.Theme()
	fallback := styleFor(c).LH
	lines := strings.Count(text, "\n") + 1
	ui.Column(c).Gap(0).Children(func() {
		for i := 0; i < lines; i++ {
			h := float32(st.LineTop(i+1) - st.LineTop(i))
			if h <= 0 {
				// The frame that creates the editor has not laid it out yet;
				// one row a line is right until it has.
				h = fallback
			}
			ui.Box(c).Height(h).AlignItems(ui.Start).Padding(2, 0).Children(func() {
				ui.Text(c, itoa(start+i)).Font(monoFamily()).
					FontSize(th.FontSize - 1).FontFeatures("tnum").
					TextColor(th.TextMuted)
			})
		}
	})
}

// renderRule draws Terminal's separator: a hairline, the label in the muted step, and a
// hairline back, or one line when there is no label (`.sf-rule` and its ::before /
// ::after).
func renderRule(c *ui.Context, n lens.Rule) {
	s := styleFor(c)
	ui.Row(c).Gap(10).MinHeight(s.LH).Children(func() {
		ui.Box(c).Grow(1).Height(1).Background(s.L2)
		if sv := plain(n.Label); sv != "" {
			ui.Text(c, sv).FontSize(s.SmallSize).TextColor(s.T3)
			ui.Box(c).Grow(1).Height(1).Background(s.L2)
		}
	})
}

func renderDiff(c *ui.Context, n lens.Diff) {
	if strings.TrimSpace(n.Text) == "" {
		// Terminal's sf-diff-empty shows "No changes" (strings 827051).
		ui.Text(c, "No changes").TextColor(c.Theme().TextMuted)
		return
	}
	s := styleFor(c)
	// Terminal's diff shares the code surface: an 8 DIP corner, the code background, a
	// one-pixel ring, and a 26 DIP monospace head with the path.
	if n.Path != "" {
		ui.Column(c).Radius(s.CodeR).Background(s.CodeBG).InsetShadow(0, 0, 1, 0, s.L1).Children(func() {
			ui.Row(c).Gap(6).MinHeight(26).Padding(0, 10).Children(func() {
				dir, base := splitPath(n.Path)
				if dir != "" {
					ui.Text(c, dir).Font(monoFamily()).FontSize(s.SmallSize).TextColor(s.T3)
				}
				ui.Text(c, base).Font(monoFamily()).FontSize(s.SmallSize).
					FontWeight(500).TextColor(s.T1)
				// Terminal's diff head appends the patch counts: +N on the "ins" tone and
				// −N (U+2212) on the "del" tone.
				if n.Added > 0 {
					ui.Text(c, "+"+itoa(n.Added)).Font(monoFamily()).
						FontSize(s.SmallSize).TextColor(s.Success)
				}
				if n.Removed > 0 {
					ui.Text(c, "−"+itoa(n.Removed)).Font(monoFamily()).
						FontSize(s.SmallSize).TextColor(s.Danger)
				}
			})
			ui.Box(c).FillWidth().Height(1).Background(s.L1)
			// The diff sizes itself and grows with its patch. It used to be
			// bounded to 400 DIP and clipped, which dropped every line past
			// the bound: there is no scroll inside to reach them. The flow's
			// scroll is the scroll.
			ui.Box(c).FillWidth().Children(func() { renderDiffBody(c, n) })
		})
		return
	}
	renderDiffBody(c, n)
}

// splitPath splits a path into its directory (with the separator) and its name, so
// the directories can be muted and the file name emphasised.
func splitPath(p string) (dir, base string) {
	i := strings.LastIndexAny(p, "/\\")
	if i < 0 {
		return "", p
	}
	return p[:i+1], p[i+1:]
}

func renderDiffBody(c *ui.Context, n lens.Diff) {
	oldText, newText := splitUnified(n.Text)
	st := &editor.DiffState{
		Old:            editor.NewBuffer(oldText),
		New:            editor.NewBuffer(newText),
		Layout:         editor.DiffUnified,
		Inline:         editor.DiffInlineWordAlt,
		Indicators:     editor.DiffBars,
		ContextLines:   3,
		ExpansionLines: 20,
	}
	if n.Split {
		st.Layout = editor.DiffSplit
	}
	// Terminal draws its diff itself: .sf-dl rows on the code surface, the gutter in --t4
	// with tabular numerals, the marker two characters wide, an added line on --ok at
	// 7% and a removed one on --bad at 7%; the split layout puts two halves in a
	// .sf-dr row, the first one divided from the second by a hairline.
	_ = st
	if n.Split {
		renderSplitDiff(c, n.Text)
		return
	}
	renderUnifiedDiff(c, n.Text)
}

// renderUnifiedDiff draws the patch with Terminal's diff rules: .sf-dl is --sf-lh tall,
// .sf-dg is the right-aligned gutter in --t4, .sf-dm the two-character marker, and
// .sf-dl.add/.del take their tone at 7% behind it.
func renderUnifiedDiff(c *ui.Context, patch string) {
	s := styleFor(c)
	lines := parseUnifiedLines(patch)
	markEmphasis(lines)
	digits := 1
	for _, ln := range lines {
		if n := ln.number(); n > 0 {
			if d := len(itoa(n)); d > digits {
				digits = d
			}
		}
	}
	gutterW := float32(digits)*s.CharW + 14
	for _, ln := range lines {
		switch {
		case ln.hunk:
			ui.Row(c).MinHeight(s.LH).Padding(0, 10, 0, 10).Children(func() {
				ui.Text(c, ln.text).Font(monoFamily()).FontSize(s.SmallSize).TextColor(s.T4)
			})
			continue
		}
		tone := s.T1
		bg := ui.RGBA(0, 0, 0, 0)
		marker := ln.kind
		switch ln.kind {
		case '+':
			bg = withAlpha(s.Success, 0.07)
			tone = s.Success
		case '-':
			bg = withAlpha(s.Danger, 0.07)
			tone = s.Danger
		}
		row := ui.Row(c).MinHeight(s.LH).Background(bg)
		row.Children(func() {
			num := ""
			if n := ln.number(); n > 0 {
				num = itoa(n)
			}
			ui.Box(c).Width(gutterW).Padding(0, 6, 0, 8).Children(func() {
				ui.Text(c, num).Font(monoFamily()).FontSize(s.SmallSize).
					TextColor(s.T4).FontFeatures("tnum")
			})
			ui.Box(c).Width(2 * s.CharW).Center().Children(func() {
				ui.Text(c, string(marker)).Font(monoFamily()).FontSize(s.SmallSize).TextColor(tone)
			})
			diffText(c, ln, s)
		})
	}
}

// diffText draws a diff line's text, painting the words that changed on the line's
// tone at 20% (22% dark) with a 3px corner, as .sf-em does.
func diffText(c *ui.Context, ln diffLine, s style) ui.Element {
	size := c.Theme().FontSize
	emBg := ui.RGBA(0, 0, 0, 0)
	alpha := float32(0.20)
	if s.Dark {
		alpha = 0.22
	}
	switch ln.kind {
	case '+':
		emBg = withAlpha(s.Success, alpha)
	case '-':
		emBg = withAlpha(s.Danger, alpha)
	}
	// One rich text keeps the spaces: a row of text elements loses them.
	var spans []ui.Span
	for i, t := range words(ln.text) {
		sp := ui.Span{Text: t, Font: monoFamily(), Size: size, Color: s.T1}
		if i < len(ln.emph) && ln.emph[i] && emBg.A > 0 {
			sp.Background = emBg
			sp.Radius = 3
		}
		spans = append(spans, sp)
	}
	return ui.RichText(c, spans...).Grow(1).Padding(0, 12, 0, 0)
}

// diffHalf draws one side of a diff row: a null side is the shaded filler
// (.sf-dl.nil, --sf-shade, which is --t1 at 2.8%, 3% in the dark appearance).
func diffHalf(c *ui.Context, ln *diffLine, s style, divider bool) ui.Element {
	row := ui.Row(c).Grow(1).MinHeight(s.LH)
	if divider {
		row.InsetShadow(-1, 0, 0, 0, s.L1)
	}
	if ln == nil {
		if s.Dark {
			return row.Background(withAlpha(s.T1, 0.03))
		}
		return row.Background(withAlpha(s.T1, 0.028))
	}
	bg := ui.RGBA(0, 0, 0, 0)
	tone := s.T1
	switch ln.kind {
	case '+':
		bg = withAlpha(s.Success, 0.07)
		tone = s.Success
	case '-':
		bg = withAlpha(s.Danger, 0.07)
		tone = s.Danger
	}
	row.Background(bg)
	digits := 4
	if n := ln.number(); n > 0 {
		digits = len(itoa(n))
	}
	gutterW := float32(digits)*s.CharW + 14
	row.Children(func() {
		num := ""
		if n := ln.number(); n > 0 {
			num = itoa(n)
		}
		ui.Box(c).Width(gutterW).Padding(0, 6, 0, 8).Children(func() {
			ui.Text(c, num).Font(monoFamily()).FontSize(s.SmallSize).
				TextColor(s.T4).FontFeatures("tnum")
		})
		ui.Box(c).Width(2 * s.CharW).Center().Children(func() {
			ui.Text(c, string(ln.kind)).Font(monoFamily()).FontSize(s.SmallSize).TextColor(tone)
		})
		diffText(c, *ln, s)
	})
	return row
}

// renderSplitDiff lays a patch out in two halves, as .sf-dr does: a context line
// fills both, and a run of removals is paired row by row with the run of additions
// after it, the side without a line being the shaded null row.
func renderSplitDiff(c *ui.Context, patch string) {
	s := styleFor(c)
	lines := parseUnifiedLines(patch)
	markEmphasis(lines)
	i := 0
	for i < len(lines) {
		if lines[i].hunk {
			ln := lines[i]
			ui.Row(c).MinHeight(s.LH).Padding(0, 10).Children(func() {
				ui.Text(c, ln.text).Font(monoFamily()).FontSize(s.SmallSize).TextColor(s.T4)
			})
			i++
			continue
		}
		if lines[i].kind == ' ' {
			ln := lines[i]
			ui.Row(c).Children(func() {
				diffHalf(c, &ln, s, true)
				diffHalf(c, &ln, s, false)
			})
			i++
			continue
		}
		dstart := i
		for i < len(lines) && lines[i].kind == '-' {
			i++
		}
		dend := i
		astart := i
		for i < len(lines) && lines[i].kind == '+' {
			i++
		}
		aend := i
		n := dend - dstart
		if aend-astart > n {
			n = aend - astart
		}
		for k := 0; k < n; k++ {
			var left, right *diffLine
			if dstart+k < dend {
				left = &lines[dstart+k]
			}
			if astart+k < aend {
				right = &lines[astart+k]
			}
			ui.Row(c).Children(func() {
				diffHalf(c, left, s, true)
				diffHalf(c, right, s, false)
			})
		}
	}
}

// diffLine is one row of a unified patch, numbered from its hunk header. emph marks
// the words that differ from the line's counterpart: Terminal paints those as an inline
// span on the tone at 20% (22% dark) with a 3px corner (`.sf-em`).
type diffLine struct {
	kind byte // '+', '-', ' ' or 0 for a hunk header
	old  int
	new  int
	text string
	hunk bool
	emph []bool
}

// words splits a line into word and space runs, so a word diff can mark the words
// that changed and leave the spaces alone.
func words(s string) []string {
	var out []string
	start := 0
	isSpace := func(b byte) bool { return b == ' ' || b == '\t' }
	for i := 1; i <= len(s); i++ {
		if i == len(s) || isSpace(s[i]) != isSpace(s[start]) {
			out = append(out, s[start:i])
			start = i
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// markWords marks the words on a removed line and its paired added line that differ,
// with a longest-common-subsequence over the word runs.
func markWords(del, add *diffLine) {
	a, b := words(del.text), words(add.text)
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = 1 + dp[i+1][j+1]
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	del.emph = make([]bool, n)
	add.emph = make([]bool, m)
	for i := range del.emph {
		del.emph[i] = true
	}
	for j := range add.emph {
		add.emph[j] = true
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			del.emph[i], add.emph[j] = false, false
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			i++
		default:
			j++
		}
	}
}

// markEmphasis pairs each run of removed lines with the run of added lines after it
// and marks their changed words, as Terminal's inline word diff does.
func markEmphasis(lines []diffLine) {
	for i := 0; i < len(lines); i++ {
		if lines[i].kind != '-' {
			continue
		}
		dstart := i
		for i < len(lines) && lines[i].kind == '-' {
			i++
		}
		dend := i
		astart := i
		for i < len(lines) && lines[i].kind == '+' {
			i++
		}
		aend := i
		for k := 0; k < dend-dstart && k < aend-astart; k++ {
			markWords(&lines[dstart+k], &lines[astart+k])
		}
		i--
	}
}

func (d diffLine) number() int {
	if d.kind == '-' {
		return d.old
	}
	return d.new
}

// parseUnifiedLines numbers the rows of a patch the way its hunk headers say.
func parseUnifiedLines(patch string) []diffLine {
	var out []diffLine
	oldN, newN := 0, 0
	for _, raw := range strings.Split(patch, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		// The rows are laid out as text elements, where a tab has no width; Terminal sets
		// tab-size on the surface, so the tab becomes the same four columns here.
		line = strings.ReplaceAll(line, "\t", "    ")
		if strings.HasPrefix(line, "@@") {
			a, b, ok := parseHunkHeader(line)
			if ok {
				oldN, newN = a, b
			}
			out = append(out, diffLine{hunk: true, text: line})
			continue
		}
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") ||
			strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "\\ ") {
			continue
		}
		if line == "" {
			continue
		}
		switch line[0] {
		case '+':
			out = append(out, diffLine{kind: '+', new: newN, text: line[1:]})
			newN++
		case '-':
			out = append(out, diffLine{kind: '-', old: oldN, text: line[1:]})
			oldN++
		default:
			out = append(out, diffLine{kind: ' ', old: oldN, new: newN, text: line[1:]})
			oldN++
			newN++
		}
	}
	return out
}

// splitUnified reconstructs the old and new sides from unified diff text, since
// the frozen Diff node carries the patch and not the two buffers. Hunks are
// placed at the line numbers their headers name, so the diff view shows the
// unshown regions as context gaps instead of gluing hunks together.
func splitUnified(diff string) (oldText, newText string) {
	var oldLines, newLines []string
	curOld, curNew := 1, 1 // next 1-based line each side expects
	padTo := func(lines *[]string, cur, want int) {
		for len(*lines) < want-1 {
			*lines = append(*lines, "")
		}
		_ = cur
	}
	for _, line := range strings.Split(diff, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "@@"):
			a, c, ok := parseHunkHeader(line)
			if !ok {
				continue
			}
			padTo(&oldLines, curOld, a)
			padTo(&newLines, curNew, c)
			curOld, curNew = a, c
			continue
		case line == "":
			continue
		case strings.HasPrefix(line, "diff "),
			strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "--- "),
			strings.HasPrefix(line, "+++ "),
			strings.HasPrefix(line, "\\"),
			strings.HasPrefix(line, "new file mode"),
			strings.HasPrefix(line, "deleted file mode"),
			strings.HasPrefix(line, "similarity index"),
			strings.HasPrefix(line, "rename "),
			strings.HasPrefix(line, "Binary files"):
			continue
		}
		body := ""
		if len(line) > 1 {
			body = line[1:]
		}
		switch line[0] {
		case '+':
			newLines = append(newLines, body)
			curNew++
		case '-':
			oldLines = append(oldLines, body)
			curOld++
		case ' ':
			oldLines = append(oldLines, body)
			newLines = append(newLines, body)
			curOld++
			curNew++
		}
	}
	return joinLines(oldLines), joinLines(newLines)
}

// parseHunkHeader reads "@@ -a[,b] +c[,d] @@" and returns the 1-based old and
// new start lines.
func parseHunkHeader(s string) (oldStart, newStart int, ok bool) {
	i := strings.IndexByte(s, '-')
	j := strings.IndexByte(s, '+')
	if i < 0 || j < 0 || j < i {
		return 0, 0, false
	}
	oldStart = hunkNumber(s[i+1:])
	if oldStart == 0 {
		oldStart = 1
	}
	newStart = hunkNumber(s[j+1:])
	if newStart == 0 {
		newStart = 1
	}
	return oldStart, newStart, true
}

// hunkNumber reads the leading integer of a hunk range ("12,3" → 12).
func hunkNumber(s string) int {
	n := 0
	seen := false
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			break
		}
		n = n*10 + int(s[i]-'0')
		seen = true
	}
	if !seen {
		return 0
	}
	return n
}

// joinLines joins with newlines, trailing newline included as Text files have.
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// ---------------------------------------------------------------------------
// Charts

func renderBars(c *ui.Context, n lens.Bars) {
	if len(n.Series) == 0 {
		return
	}
	t := c.Theme()
	labels := make([]string, len(n.Series))
	values := make([]float64, len(n.Series))
	for i, s := range n.Series {
		labels[i] = s.Label
		values[i] = s.Value
	}
	lo, hi := chart.Extent(values...)
	// Bars grow from zero: scaling them from the smallest value instead made every
	// bar start below the plot area (zero was under the axis' first tick), so the
	// bars painted over the x labels and into the foot. A chart whose values are all
	// negative keeps its own floor.
	if lo > 0 {
		lo = 0
	}
	if hi == lo {
		hi = lo + 1
	}
	x := chart.NewBand(labels...).Padding(0.5, 0.1)
	y := chart.NewLinear(lo, hi).Nice(4)
	// A bar is a column, not a block: a full-width chart gave each of a ping's few
	// replies a band hundreds of DIPs wide. The figure is only as wide as its bands
	// need (about 34 DIP each, so a bar is ~17), capped so a long series still fits
	// the block instead of growing without bound.
	width := 40 + 34*float32(len(n.Series))
	if width > 520 {
		width = 520
	}
	chart.Plot(c, chart.Axes{Left: y.Ticks(4), Bottom: x.Ticks()}, func(area ui.Element) {
		for i, s := range n.Series {
			chart.Rect(c, x.Band(i), y.Span(0, s.Value)).Background(t.Accent).Radius(3)
		}
	}).Height(120).Gap(6).WidthPercent(100).MaxWidth(width)
}

func renderSpark(c *ui.Context, n lens.Spark) {
	if len(n.Series) < 2 {
		return
	}
	t := c.Theme()
	vals := make([]float64, len(n.Series))
	lo, hi := n.Series[0].Value, n.Series[0].Value
	for i, s := range n.Series {
		vals[i] = s.Value
		if s.Value < lo {
			lo = s.Value
		}
		if s.Value > hi {
			hi = s.Value
		}
	}
	if hi == lo {
		hi = lo + 1
	}
	ui.Box(c).Height(36).FillWidth().Draw(func(p *ui.Painter, r ui.Rect) {
		xs := make([]float32, len(vals))
		ys := make([]float32, len(vals))
		n1 := float32(len(vals) - 1)
		for i, v := range vals {
			xs[i] = float32(i) / n1
			ys[i] = float32((v - lo) / (hi - lo))
		}
		path := chart.Line(r, xs, ys, chart.CurveLinear)
		p.StrokePath(path, 1.5, t.Accent)
	})
}

// ---------------------------------------------------------------------------
// ANSI mini terminal (approximation; see the package comment on gaps)

// The ANSI mini terminal draws in Terminal's own palette for the appearance in use:
// terminal.ansi[16] of the theme Terminal's window draws with (internal/terminaltheme),
// as Terminal's ansi() lens draws in the terminal's own colours.

// ansiWidth is the mini terminal's screen width. Terminal's `ansi(text)` renders a
// small terminal, so carriage returns and cursor moves have to reshape the text
// instead of being printed literally.
const ansiWidth = 200

// ansiCell is one screen cell: a rune and the SGR state it was written with.
type ansiCell struct {
	r  rune
	st sgrState
}

// ansiScreen is the mini terminal behind renderANSI: printable runes advance the
// cursor (wrapping at ansiWidth), and CR/LF/BS/CSI H A B C D G K J m s u move,
// erase or restyle it.
type ansiScreen struct {
	rows     [][]ansiCell
	row, col int
	st       sgrState
	savedRow int
	savedCol int
}

func newANSIScreen() *ansiScreen {
	return &ansiScreen{rows: [][]ansiCell{make([]ansiCell, ansiWidth)}}
}

func (s *ansiScreen) ensureRow(row int) {
	for len(s.rows) <= row {
		s.rows = append(s.rows, make([]ansiCell, ansiWidth))
	}
}

func (s *ansiScreen) put(r rune) {
	w := parse.RuneWidth(r)
	if w <= 0 {
		return // combining marks are dropped in this preview
	}
	if s.col+w > ansiWidth {
		s.col = 0
		s.row++
	}
	s.ensureRow(s.row)
	if s.col < ansiWidth {
		s.rows[s.row][s.col] = ansiCell{r: r, st: s.st}
		for k := 1; k < w && s.col+k < ansiWidth; k++ {
			s.rows[s.row][s.col+k] = ansiCell{} // continuation cell
		}
	}
	s.col += w
}

func (s *ansiScreen) feed(text string) {
	for i := 0; i < len(text); {
		ch := text[i]
		if ch == 0x1b {
			if i+1 < len(text) && text[i+1] == '[' {
				j := i + 2
				for j < len(text) && (text[j] < 0x40 || text[j] > 0x7e) {
					j++
				}
				if j >= len(text) {
					return
				}
				s.csi(text[i+2:j], text[j])
				i = j + 1
				continue
			}
			i += 2 // other escapes are ignored
			continue
		}
		switch ch {
		case '\r':
			s.col = 0
		case '\n':
			s.row++
			s.col = 0
			s.ensureRow(s.row)
		case '\b':
			if s.col > 0 {
				s.col--
			}
		case '\t':
			s.col = (s.col/8 + 1) * 8
			if s.col >= ansiWidth {
				s.col = 0
				s.row++
				s.ensureRow(s.row)
			}
		default:
			r, size := decodeRune(text[i:])
			s.put(r)
			i += size
			continue
		}
		i++
	}
}

func (s *ansiScreen) csi(params string, final byte) {
	nums := ansiParams(params)
	first := 0
	if len(nums) > 0 {
		first = nums[0]
	}
	switch final {
	case 'm':
		s.st.apply(params)
	case 'H', 'f':
		row, col := 1, 1
		if len(nums) > 0 && nums[0] > 0 {
			row = nums[0]
		}
		if len(nums) > 1 && nums[1] > 0 {
			col = nums[1]
		}
		s.row, s.col = row-1, col-1
		if s.row < 0 {
			s.row = 0
		}
		if s.col < 0 {
			s.col = 0
		}
		s.ensureRow(s.row)
	case 'A':
		s.row -= maxInt(1, first)
		if s.row < 0 {
			s.row = 0
		}
	case 'B':
		s.row += maxInt(1, first)
		s.ensureRow(s.row)
	case 'C':
		s.col += maxInt(1, first)
		if s.col >= ansiWidth {
			s.col = ansiWidth - 1
		}
	case 'D':
		s.col -= maxInt(1, first)
		if s.col < 0 {
			s.col = 0
		}
	case 'G':
		s.col = maxInt(1, first) - 1
		if s.col >= ansiWidth {
			s.col = ansiWidth - 1
		}
	case 'K':
		s.ensureRow(s.row)
		switch first {
		case 1: // to the start of the line
			for k := 0; k <= s.col && k < ansiWidth; k++ {
				s.rows[s.row][k] = ansiCell{}
			}
		case 2: // whole line
			s.rows[s.row] = make([]ansiCell, ansiWidth)
		default: // from the cursor to the end
			for k := s.col; k < ansiWidth; k++ {
				s.rows[s.row][k] = ansiCell{}
			}
		}
	case 'J':
		if first == 2 || first == 3 {
			s.rows = [][]ansiCell{make([]ansiCell, ansiWidth)}
			s.row, s.col = 0, 0
		}
	case 's':
		s.savedRow, s.savedCol = s.row, s.col
	case 'u':
		s.row, s.col = s.savedRow, s.savedCol
		s.ensureRow(s.row)
	}
}

// lines renders the screen: one span run per row, trailing blank cells trimmed.
func (s *ansiScreen) lines() [][]ui.Span {
	out := make([][]ui.Span, 0, len(s.rows))
	for _, row := range s.rows {
		last := -1
		for i, cell := range row {
			if cell.r != 0 {
				last = i
			}
		}
		if last < 0 {
			out = append(out, nil)
			continue
		}
		var spans []ui.Span
		runStart := 0
		runSt := row[0].st
		var buf strings.Builder
		flush := func(end int) {
			if buf.Len() > 0 {
				spans = append(spans, ansiSpan(runSt, buf.String()))
				buf.Reset()
			}
			_ = end
		}
		for i := 0; i <= last; i++ {
			cell := row[i]
			if cell.r == 0 && i > 0 && row[i-1].r != 0 && parse.RuneWidth(row[i-1].r) == 2 {
				continue // continuation of a wide rune already written
			}
			if cell.st != runSt {
				flush(runStart)
				runSt = cell.st
				runStart = i
			}
			if cell.r == 0 {
				buf.WriteByte(' ')
			} else {
				buf.WriteRune(cell.r)
			}
		}
		flush(last + 1)
		out = append(out, spans)
	}
	return out
}

func ansiParams(params string) []int {
	if params == "" {
		return nil
	}
	if params[0] == '?' || params[0] == '>' || params[0] == '!' {
		return nil
	}
	parts := strings.Split(params, ";")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			out = append(out, 0)
			continue
		}
		n := 0
		ok := true
		for i := 0; i < len(p); i++ {
			if p[i] < '0' || p[i] > '9' {
				ok = false
				break
			}
			n = n*10 + int(p[i]-'0')
		}
		if !ok {
			return nil
		}
		out = append(out, n)
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func decodeRune(s string) (rune, int) {
	for i := 1; i <= len(s) && i <= 4; i++ {
		if utf8.ValidString(s[:i]) {
			r := []rune(s[:i])
			return r[0], i
		}
	}
	return rune(s[0]), 1
}

func renderANSI(c *ui.Context, n lens.ANSI) {
	scr := newANSIScreen()
	scr.feed(n.Text)
	// The whole screen is in the flow: no capped box and no scroll of its own,
	// so the terminal's scroll reaches every row, however long the program's
	// coloured output was.
	box := ui.Box(c).FillWidth().Background(c.Theme().Surface).Radius(6).Padding(6)
	box.Children(func() {
		col := ui.Column(c).Gap(0).Selectable()
		col.Children(func() {
			for _, spans := range scr.lines() {
				if len(spans) == 0 {
					ui.Text(c, " ")
					continue
				}
				ui.RichText(c, spans...).Font(monoFamily())
			}
		})
	})
}

type sgrState struct {
	fg, bg    ui.Color
	hasFg     bool
	hasBg     bool
	bold      bool
	dim       bool
	italic    bool
	underline bool
	reverse   bool
}

// ansiLine turns one line of SGR-styled text into spans.
func ansiLine(line string) []ui.Span {
	var st sgrState
	var out []ui.Span
	var buf strings.Builder
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		out = append(out, ansiSpan(st, buf.String()))
		buf.Reset()
	}
	for i := 0; i < len(line); {
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			j := i + 2
			for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
				j++
			}
			if j >= len(line) {
				break
			}
			if line[j] == 'm' {
				flush()
				st.apply(line[i+2 : j])
			}
			i = j + 1
			continue
		}
		buf.WriteByte(line[i])
		i++
	}
	flush()
	return out
}

func ansiSpan(st sgrState, text string) ui.Span {
	fg, bg := st.fg, st.bg
	if !st.hasFg {
		fg = ui.Color{}
	}
	if !st.hasBg {
		bg = ui.Color{}
	}
	if st.reverse {
		fg, bg = bg, fg
	}
	sp := ui.Span{Text: text}
	if st.hasFg || st.reverse {
		sp.Color = fg
	}
	if st.hasBg || st.reverse {
		sp.Background = bg
	}
	if st.bold {
		sp.Weight = 600
	}
	if st.dim {
		if sp.Color.A == 0 && sp.Color.R == 0 && sp.Color.G == 0 && sp.Color.B == 0 {
			// SGR 2 with no colour of its own dims the theme's terminal
			// foreground, as the grid does.
			termFg, _ := terminaltheme.Terminal()
			sp.Color = termFg.Alpha(0.6)
		} else {
			sp.Color = sp.Color.Alpha(0.6)
		}
	}
	if st.italic {
		sp.Italic = true
	}
	if st.underline {
		sp.Underline = true
	}
	return sp
}

func (st *sgrState) apply(params string) {
	if params == "" {
		params = "0"
	}
	pal := terminaltheme.ANSI()
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		n, ok := atoi(fields[i])
		if !ok {
			continue
		}
		switch {
		case n == 0:
			*st = sgrState{}
		case n == 1:
			st.bold = true
		case n == 2:
			st.dim = true
		case n == 3:
			st.italic = true
		case n == 4:
			st.underline = true
		case n == 7:
			st.reverse = true
		case n == 22:
			st.bold, st.dim = false, false
		case n == 23:
			st.italic = false
		case n == 24:
			st.underline = false
		case n == 27:
			st.reverse = false
		case n >= 30 && n <= 37:
			st.fg, st.hasFg = pal[n-30], true
		case n == 39:
			st.hasFg = false
		case n >= 40 && n <= 47:
			st.bg, st.hasBg = pal[n-40], true
		case n == 49:
			st.hasBg = false
		case n >= 90 && n <= 97:
			st.fg, st.hasFg = pal[8+n-90], true
		case n >= 100 && n <= 107:
			st.bg, st.hasBg = pal[8+n-100], true
		case n == 38 || n == 48:
			if i+1 >= len(fields) {
				return
			}
			mode, _ := atoi(fields[i+1])
			if mode == 5 && i+2 < len(fields) {
				idx, _ := atoi(fields[i+2])
				col := xtermColor(idx)
				if n == 38 {
					st.fg, st.hasFg = col, true
				} else {
					st.bg, st.hasBg = col, true
				}
				i += 2
			} else if mode == 2 && i+4 < len(fields) {
				r, _ := atoi(fields[i+2])
				g, _ := atoi(fields[i+3])
				b, _ := atoi(fields[i+4])
				col := ui.RGBA(uint8(r), uint8(g), uint8(b), 1)
				if n == 38 {
					st.fg, st.hasFg = col, true
				} else {
					st.bg, st.hasBg = col, true
				}
				i += 4
			}
		}
	}
}

func xtermColor(n int) ui.Color {
	if n < 16 {
		if n < 0 {
			n = 0
		}
		return terminaltheme.ANSI()[n]
	}
	if n < 232 {
		n -= 16
		levels := [6]uint8{0, 95, 135, 175, 215, 255}
		return ui.RGBA(levels[n/36], levels[(n/6)%6], levels[n%6], 1)
	}
	v := uint8(8 + 10*(n-232))
	return ui.RGBA(v, v, v, 1)
}

func atoi(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

// ---------------------------------------------------------------------------
// Colors

// toneColor maps a lens tone token to a theme color.
// renderLines stacks children with no gap: Terminal's flow.ui.lines, the lines of
// one text stream (col adds small gaps between its children).
func renderLines(c *ui.Context, n lens.Lines, rs *renderState) {
	ui.Column(c).Gap(0).Children(func() {
		for _, ch := range n.Children {
			renderWith(c, ch, rs)
		}
	})
}

// renderStats draws Terminal's stats well: a tile per StatItem, side by side, with
// the label, the large figure, an optional sub line, a tone and a tooltip.
func renderStats(c *ui.Context, n lens.Stats) {
	s := styleFor(c)
	ui.Row(c).Gap(10).Children(func() {
		for _, it := range n.Items {
			tone := s.T1
			if col, ok := toneColor(c, it.Tone); ok {
				tone = col
			}
			tile := ui.Column(c).Gap(2).Padding(6, 10).Radius(s.CardR).
				Background(s.CardBG).InsetShadow(0, 0, 1, 0, s.L2)
			if it.Title != "" {
				tile.Tooltip(it.Title)
			}
			tile.Children(func() {
				if len(it.Label) > 0 {
					text(c, it.Label).FontSize(s.SmallSize).TextColor(s.T3).SingleLine()
				}
				text(c, it.Value).FontSize(s.SmallSize * 1.6).FontWeight(600).
					TextColor(tone).SingleLine()
				if len(it.Sub) > 0 {
					text(c, it.Sub).FontSize(s.SmallSize).TextColor(s.T3).SingleLine()
				}
			})
		}
	})
}

// renderButton draws Terminal's flow.ui.button: default/primary/quiet/danger, md or
// sm, a pressed toggle, disabled (with its reason as the tooltip) and busy.
// A click emits {ev = "action", act = Act}.
func renderButton(c *ui.Context, n lens.Button, rs *renderState) {
	renderButtonWith(c, n, "", "", rs)
}

// renderButtonWith draws a button whose click carries the table and row ids
// (Terminal's ButtonCell sends {ev = "action", id = <table>, act, item = <row>}).
func renderButtonWith(c *ui.Context, n lens.Button, id, item string, rs *renderState) {
	s := styleFor(c)
	fg, bg, ring := s.T1, s.ChipBG, s.L2
	switch n.Variant {
	case "primary":
		fg, bg, ring = ui.RGBA(255, 255, 255, 1), s.Accent, s.Accent
	case "danger":
		if col, ok := toneColor(c, "error"); ok {
			fg, bg, ring = col, withAlpha(col, 0.10), withAlpha(col, 0.22)
		}
	case "quiet":
		bg, ring = ui.RGBA(0, 0, 0, 0), ui.RGBA(0, 0, 0, 0)
	}
	if n.Disabled || n.Busy {
		fg = s.T3
	}
	var height float32 = 28
	if n.Size == "sm" {
		height = 22
	}
	btn := ui.Row(c).Gap(6).MinHeight(height).Padding(0, 10).Radius(6).
		Background(bg).InsetShadow(0, 0, 1, 0, ring)
	if n.On {
		btn = btn.Background(withAlpha(s.Accent, 0.14))
	}
	label := n.Text
	if n.Busy {
		label = "…"
	}
	btn.Children(func() {
		if n.Icon != "" {
			if svg, ok := surface.Icon(n.Icon); ok {
				ui.Icon(c, svg).Width(12).Height(12).TextColor(fg)
			}
		}
		if label != "" {
			ui.Text(c, label).FontSize(s.SmallSize).FontWeight(500).TextColor(fg)
		}
		for _, k := range n.Keys {
			ui.Text(c, k).FontSize(s.SmallSize-1).TextColor(s.T3).
				Padding(0, 3).Radius(3).Background(s.ChipBG)
		}
	})
	if n.Title != "" {
		btn.Tooltip(n.Title)
	}
	if n.Disabled && n.Reason != "" {
		btn.Tooltip(n.Reason)
	}
	if !n.Disabled && !n.Busy && n.Act != "" {
		act := n.Act
		btn.OnClick(func() {
			emitAction(rs, id, item, act)
			c.Invalidate()
		})
	}
}

// renderEl draws Terminal's HTML-subset element: a button as a button, an input as
// its read-only control, and any other tag as a stack of its text and children.
func renderEl(c *ui.Context, n lens.El, rs *renderState) {
	switch n.Tag {
	case "button":
		renderButton(c, lens.Button{
			Text: n.Text, Title: n.Attrs["title"], Act: n.Attrs["act"],
			Disabled: n.Disabled,
		}, rs)
	case "input":
		s := styleFor(c)
		row := ui.Row(c).Gap(6)
		row.Children(func() {
			box := ui.Box(c).Width(14).Height(14).InsetShadow(0, 0, 1, 0, s.L3)
			if n.Input == "radio" {
				box = box.Radius(999)
			} else {
				box = box.Radius(3)
			}
			if n.Checked {
				box = box.Background(s.Accent).TextColor(ui.RGBA(255, 255, 255, 1))
				box.Children(func() { ui.Text(c, "✓").FontSize(s.SmallSize - 2) })
			}
			if n.Disabled {
				box = box.Background(s.ChipBG)
			}
			label := n.Text
			if label == "" {
				label = n.Name
			}
			if label != "" {
				ui.Text(c, label).FontSize(s.SmallSize).TextColor(s.T1)
			}
		})
	default:
		s := styleFor(c)
		ui.Column(c).Gap(2).Children(func() {
			if n.Text != "" {
				ui.Text(c, n.Text).FontSize(s.SmallSize).TextColor(s.T1)
			}
			for _, ch := range n.Children {
				renderWith(c, ch, rs)
			}
		})
	}
}

// renderTSP draws Terminal's generic flow.ui.node: a tabs strip lays its children
// in a row, an overlay is its body, and any other kind stacks its children (or
// shows the kind when it has none).
func renderTSP(c *ui.Context, n lens.TSP, rs *renderState) {
	s := styleFor(c)
	switch n.Kind {
	case "tabs":
		ui.Row(c).Gap(4).Children(func() {
			for _, ch := range n.Children {
				renderWith(c, ch, rs)
			}
		})
	case "overlay":
		ui.Column(c).Gap(s.InnerGap).Children(func() {
			for _, ch := range n.Children {
				renderWith(c, ch, rs)
			}
		})
	default:
		ui.Column(c).Gap(4).Children(func() {
			if len(n.Children) == 0 {
				ui.Text(c, n.Kind).FontSize(s.SmallSize).TextColor(s.T3)
				return
			}
			for _, ch := range n.Children {
				renderWith(c, ch, rs)
			}
		})
	}
}

// Image rendering: a picture from a file surface or an inline graphics protocol.
// Bitmaps are decoded once per content key (decoding every frame would re-upload
// the texture), and a click opens a lightbox overlay.
var (
	imgCacheMu sync.Mutex
	imgCache   = map[string]*ui.Bitmap{}
	imgOpenMu  sync.Mutex
	imgOpen    = map[string]bool{}
)

func imageKey(n lens.Image) string {
	h := fnv.New64a()
	_, _ = h.Write(n.Data)
	return n.Path + ":" + strconv.FormatUint(h.Sum64(), 16)
}

func bitmapForImage(n lens.Image) (*ui.Bitmap, bool) {
	key := imageKey(n)
	imgCacheMu.Lock()
	if b, ok := imgCache[key]; ok {
		imgCacheMu.Unlock()
		return b, true
	}
	imgCacheMu.Unlock()
	b, err := ui.DecodeBitmap(n.Data)
	if err != nil {
		return nil, false
	}
	imgCacheMu.Lock()
	if len(imgCache) > 64 {
		imgCache = map[string]*ui.Bitmap{}
	}
	imgCache[key] = b
	imgCacheMu.Unlock()
	return b, true
}

func imageOpenGet(key string) bool {
	imgOpenMu.Lock()
	defer imgOpenMu.Unlock()
	return imgOpen[key]
}

func imageOpenSet(key string, open bool) {
	imgOpenMu.Lock()
	defer imgOpenMu.Unlock()
	if len(imgOpen) > stateCap {
		imgOpen = map[string]bool{}
	}
	if open {
		imgOpen[key] = true
	} else {
		delete(imgOpen, key)
	}
}

// renderImage draws a picture bounded to 480x360 DIP and a caption; clicking it
// opens Terminal's lightbox (a dimmed full-window overlay with the picture scaled to
// fit).
func renderImage(c *ui.Context, n lens.Image, rs *renderState) {
	s := styleFor(c)
	bmp, ok := bitmapForImage(n)
	if !ok {
		ui.Text(c, "Image (cannot decode)").FontSize(s.SmallSize).TextColor(s.T3)
		return
	}
	key := stateKey(rs.scope, "img:"+imageKey(n), 0)
	bw, bh := bmp.Size()
	fw, fh := imageFit(float32(bw), float32(bh), 480, 360)
	ui.Column(c).Gap(4).Children(func() {
		pic := ui.Image(c, bmp).Width(fw).Height(fh)
		pic.OnClick(func() {
			imageOpenSet(key, true)
			c.Invalidate()
		})
		caption := n.Path
		if caption == "" {
			caption = n.Alt
		}
		if caption != "" {
			ui.Text(c, caption).FontSize(s.SmallSize).TextColor(s.T3)
		}
	})
	if !imageOpenGet(key) {
		return
	}
	sw, sh := c.Size()
	ui.Overlay(c, func() {
		back := ui.Box(c).Absolute().Left(0).Top(0).Width(sw).Height(sh).
			Background(ui.RGBA(0, 0, 0, 0.72))
		back.OnClick(func() {
			imageOpenSet(key, false)
			c.Invalidate()
		})
		back.Children(func() {
			ui.Column(c).Fill().Center().Children(func() {
				lw, lh := imageFit(float32(bw), float32(bh), sw-80, sh-80)
				ui.Image(c, bmp).Width(lw).Height(lh)
				if n.Path != "" {
					ui.Text(c, n.Path).FontSize(s.SmallSize).TextColor(ui.RGBA(255, 255, 255, 1))
				}
			})
		})
	})
}

// emitAction sends Terminal”s action event: {ev = "action", id, act, item}.
// imageFit scales an image to fit inside maxW x maxH, keeping its aspect.
func imageFit(w, h, maxW, maxH float32) (float32, float32) {
	if w <= 0 || h <= 0 {
		return 1, 1
	}
	s := float32(1)
	if w > maxW {
		s = maxW / w
	}
	if h*s > maxH {
		s = maxH / h
	}
	return w * s, h * s
}
func emitAction(rs *renderState, id, item, act string) {
	if rs == nil || rs.sink == nil {
		return
	}
	rs.sink(lens.Event{Action: "action", ID: id, Item: item, Act: act})
}

// emitRow sends Terminal's row event: {ev = "select"|"activate", id, item}.
func emitRow(rs *renderState, action, id, item string) {
	if rs == nil || rs.sink == nil {
		return
	}
	rs.sink(lens.Event{Action: action, ID: id, Item: item})
}

func toneColor(c *ui.Context, tone string) (ui.Color, bool) {
	t := c.Theme()
	switch strings.TrimSpace(tone) {
	case "neutral", "":
		return t.Surface, false
	case "accent", "info":
		return t.Accent, true
	case "success", "ok", "good":
		return t.Success, true
	case "warning", "warn":
		return t.Warning, true
	case "error", "danger", "bad":
		return t.Danger, true
	case "muted":
		return t.TextMuted, true
	case "pending":
		return t.Warning, true
	case "active":
		return t.Accent, true
	}
	return t.Surface, false
}

// mix blends a toward b; used for a card's tone tint.
func mix(a, b ui.Color, amount float32) ui.Color {
	r := float32(a.R) + (float32(b.R)-float32(a.R))*amount
	g := float32(a.G) + (float32(b.G)-float32(a.G))*amount
	bl := float32(a.B) + (float32(b.B)-float32(a.B))*amount
	al := a.A
	if al == 0 {
		al = 255
	}
	return ui.RGBA(uint8(r), uint8(g), uint8(bl), float32(al)/255)
}

// codeState is one code block's editor, kept between frames so folds and the
// selection survive a lens rebuild.
type codeState struct {
	text string
	lang string
	buf  *editor.Buffer
	st   *editor.State
}

var (
	codeMu     sync.Mutex
	codeState_ = map[string]*codeState{}
)

// codeMaxEntries bounds the cache: blocks that scroll out of view leave entries
// behind, so it is dropped wholesale once it grows past a screenful of blocks.
const codeMaxEntries = 512

// codeBufferFor returns the editor buffer and state for one code block, reusing
// the previous frame's when the text did not change.
func codeBufferFor(key, text, lang string) (*editor.Buffer, *editor.State) {
	codeMu.Lock()
	defer codeMu.Unlock()
	if cs, ok := codeState_[key]; ok && cs.text == text && cs.lang == lang {
		return cs.buf, cs.st
	}
	if len(codeState_) > codeMaxEntries {
		codeState_ = map[string]*codeState{}
	}
	buf := editor.NewBuffer(text)
	if lang != "" {
		native := lang
		if native == "go" || native == "golang" {
			// gofmt names the language after the file extension.
			native = "go"
		}
		buf.SetLanguage(editor.LanguageFor("x." + native))
	}
	// FitContent sizes the editor to its lines and SoftWrap keeps a long line
	// in the flow rather than behind a sideways scroll: a lens view never
	// scrolls inside itself, nothing overflows to draw a bar, and the
	// terminal's scroll reaches every line. The wheel over the code goes to
	// the flow.
	st := &editor.State{
		Buffer: buf, ReadOnly: true,
		FitContent: true, SoftWrap: true,
	}
	codeState_[key] = &codeState{text: text, lang: lang, buf: buf, st: st}
	return buf, st
}

// iconGlyph maps the icon names the lenses set onto text glyphs, with the tone
// the glyph should use. Terminal draws SVGs; a name is all the frozen tree carries,
// so the glyph is the closest equivalent that needs no asset pipeline.
func iconGlyph(name string) (glyph, tone string) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "none":
		return "", ""
	case "failed", "fail", "error", "cross", "x":
		return "✕", "error"
	case "warning", "warn":
		return "⚠", "warning"
	case "note", "info":
		return "ℹ", "info"
	case "success", "ok", "check", "passed", "pass":
		return "✓", "success"
	case "package", "pkg", "module":
		return "▣", ""
	case "file":
		return "▤", ""
	case "directory", "dir", "folder":
		return "▸", ""
	case "git", "commit":
		return "◆", ""
	case "image":
		return "▨", ""
	}
	return "", ""
}

// expand remembers which code blocks the user expanded past their clamp.
var (
	expandMu sync.Mutex
	expanded = map[string]bool{}
)

func expandGet(key string) bool {
	expandMu.Lock()
	defer expandMu.Unlock()
	return expanded[key]
}

func expandSet(key string, on bool) {
	expandMu.Lock()
	defer expandMu.Unlock()
	if on {
		if len(expanded) > stateCap {
			expanded = map[string]bool{}
		}
		expanded[key] = true
		return
	}
	delete(expanded, key)
}
