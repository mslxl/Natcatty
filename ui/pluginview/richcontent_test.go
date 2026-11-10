package pluginview_test

// External-behavior tests for ticket 13: rich text, virtual lists, packaged
// assets and fonts, host-driven transitions, and the ticket-12 regressions.

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/font/gofont/goregular"

	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	"github.com/mslxl/natcatty/ui/pluginview"
)

const richParagraph = "Intro <b>literal</b> docs"

func richTextTree() domainnativeui.Batch {
	const linkURL = "https://example.com/guide"
	return domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeRichText),
			create(3, domainnativeui.NodeTextSpan),
			create(4, domainnativeui.NodeTextSpan),
			create(5, domainnativeui.NodeTextSpan),
			prop(1, domainnativeui.PropLayout, domainnativeui.IntValue(int64(domainnativeui.LayoutColumn))),
			children(1, 2),
			prop(2, domainnativeui.PropPaddingStart, domainnativeui.FloatValue(12)),
			children(2, 3, 4, 5),
			text(3, "Intro "),
			prop(3, domainnativeui.PropFontWeight, domainnativeui.IntValue(700)),
			text(4, "<b>literal</b> "),
			prop(4, domainnativeui.PropItalic, domainnativeui.BoolValue(true)),
			prop(4, domainnativeui.PropUnderline, domainnativeui.BoolValue(true)),
			text(5, "docs"),
			prop(5, domainnativeui.PropLink, domainnativeui.StringValue(linkURL)),
			prop(5, domainnativeui.PropFontSize, domainnativeui.FloatValue(28)),
		},
	}
}

func TestRichTextRendersStyledSpansLinksAndSelection(t *testing.T) {
	session := newFakeSession(buildTree(t, richTextTree()))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 480, 320)
	tt.Frame()

	// The paragraph is host-rendered text. Markup-like guest text stays
	// literal: no guest-supplied HTML is ever interpreted.
	if !tt.HasText(richParagraph) {
		t.Fatalf("paragraph shows %v", tt.Texts())
	}
	if !tt.HasText("<b>literal</b>") {
		t.Fatalf("markup-like text was not rendered literally: %v", tt.Texts())
	}

	// The link span carries its own size, so the formatted run is visibly
	// larger than the paragraph's default text.
	link, ok := tt.Find("docs")
	if !ok {
		t.Fatalf("link span is missing: %v", tt.Texts())
	}
	if link.H < 24 {
		t.Fatalf("link span height = %v, want the span's 28pt size", link.H)
	}
	if err := tt.Click("docs"); err != nil {
		t.Fatalf("Click link: %v", err)
	}
	if got := tt.OpenedURLs(); len(got) != 1 || got[0] != "https://example.com/guide" {
		t.Fatalf("opened URLs = %v", got)
	}

	// Selection is host-owned. Selecting the paragraph queues one selection
	// event carrying the selected text, and never forwards markup.
	paragraph, ok := tt.Find(richParagraph)
	if !ok {
		t.Fatalf("paragraph box missing: %v", tt.Texts())
	}
	tt.Press(paragraph.X+4, paragraph.Y+6)
	tt.Move(paragraph.X+paragraph.W-4, paragraph.Y+paragraph.H-6)
	tt.Release(paragraph.X+paragraph.W-4, paragraph.Y+paragraph.H-6)
	tt.Frame()
	selections := session.eventsOf(domainnativeui.EventSelection)
	if len(selections) != 1 {
		t.Fatalf("selection events = %+v, texts %v", selections, tt.Texts())
	}
	if !strings.Contains(selections[0].Text, "<b>literal</b>") ||
		!strings.Contains(selections[0].Text, "docs") {
		t.Fatalf("selected text = %q, want the selected paragraph text", selections[0].Text)
	}
	if selections[0].Node != domainnativeui.IDFromNode(2) {
		t.Fatalf("selection event node = %#x", uint64(selections[0].Node))
	}

	// Rendering the same selection again does not repeat the event.
	tt.Frame()
	if got := session.eventsOf(domainnativeui.EventSelection); len(got) != 1 {
		t.Fatalf("an unchanged selection queued again: %+v", got)
	}
}

func virtualListTree(count int64) domainnativeui.Batch {
	return domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeVirtualList),
			prop(1, domainnativeui.PropLayout, domainnativeui.IntValue(int64(domainnativeui.LayoutColumn))),
			children(1, 2),
			label(2, "Items"),
			prop(2, domainnativeui.PropItemCount, domainnativeui.IntValue(count)),
			prop(2, domainnativeui.PropHeight, domainnativeui.FloatValue(200)),
		},
	}
}

func itemIDs(n int, first domainnativeui.NodeID) []domainnativeui.NodeID {
	ids := make([]domainnativeui.NodeID, n)
	for i := range ids {
		ids[i] = first + domainnativeui.NodeID(i)
	}
	return ids
}

func addVirtualItems(t *testing.T, tree *domainnativeui.Tree, list domainnativeui.NodeID,
	base domainnativeui.NodeID, from, count int) {
	t.Helper()
	ops := make([]domainnativeui.Op, 0, count*3+1)
	all := make([]domainnativeui.NodeID, 0, from+count)
	for i := 0; i < from+count; i++ {
		all = append(all, base+domainnativeui.NodeID(i))
	}
	for i := from; i < from+count; i++ {
		id := base + domainnativeui.NodeID(i)
		ops = append(ops, create(id, domainnativeui.NodeVirtualItem))
		ops = append(ops, prop(id, domainnativeui.PropItemIndex, domainnativeui.IntValue(int64(i))))
		ops = append(ops, text(id, fmt.Sprintf("row %d", i)))
	}
	ops = append(ops, children(list, all...))
	if _, err := tree.Apply(domainnativeui.Batch{Ops: ops}, &resolver{}); err != nil {
		t.Fatalf("adding virtual items: %v", err)
	}
}

func TestVirtualListPullsOnlyVisibleRangesWithBoundedMemory(t *testing.T) {
	const total = 1_000_000
	tree := buildTree(t, virtualListTree(total))
	session := newFakeSession(tree)
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 420, 320)
	tt.Frame()

	requests := session.eventsOf(domainnativeui.EventItemRequest)
	if len(requests) == 0 {
		t.Fatal("the empty virtual list requested no items")
	}
	// The real event queue coalesces repeated requests for one node, so the
	// last request of the first frame is what the guest receives.
	first := requests[len(requests)-1]
	if first.Node != domainnativeui.IDFromNode(2) || first.Index != 0 || first.Width <= 0 || first.Width > 40 {
		t.Fatalf("first item request = %+v, want a small visible window at 0", first)
	}
	wantRequests := len(requests)
	visible := int(first.Width)
	session.Drain()

	// Answer with exactly the requested window. The list shows those rows
	// and asks for nothing more until the window moves.
	addVirtualItems(t, tree, 2, 1000, 0, visible)
	tt.Frame()
	if !tt.HasText(fmt.Sprintf("row %d", visible-1)) {
		t.Fatalf("supplied visible row is not shown: %v", tt.Texts())
	}
	if !tt.HasText("row 0") {
		t.Fatalf("first supplied row is not shown: %v", tt.Texts())
	}
	if got := len(session.eventsOf(domainnativeui.EventItemRequest)); got != wantRequests {
		t.Fatalf("an answered window requested again: %d requests", got)
	}

	// A million-row list keeps only the supplied handful in the retained
	// tree; nothing proportional to the item count is allocated.
	items := 0
	for _, node := range tree.Nodes {
		if node != nil && node.Kind == domainnativeui.NodeVirtualItem {
			items++
		}
	}
	if items != visible || len(tree.Nodes) > 40 {
		t.Fatalf("tree holds %d item nodes in %d nodes for %d rows", items, len(tree.Nodes), total)
	}

	// Clicking a visible row reports the absolute item index.
	if err := tt.Click("row 0"); err != nil {
		t.Fatalf("Click row: %v", err)
	}
	tt.Frame()
	clicks := session.eventsOf(domainnativeui.EventClick)
	if len(clicks) != 1 || clicks[0].Index != 0 {
		t.Fatalf("cell click events = %+v", clicks)
	}
	selections := session.eventsOf(domainnativeui.EventSelection)
	if len(selections) == 0 || selections[len(selections)-1].Index != 0 {
		t.Fatalf("selection events = %+v", selections)
	}
	session.Drain()

	// Scrolling far down requests the new visible window, not the rows in
	// between: the request stays a small contiguous range at a large index.
	tt.Scroll(210, 150, 0, 4000)
	tt.Frame()
	requests = session.eventsOf(domainnativeui.EventItemRequest)
	if len(requests) <= wantRequests {
		t.Fatalf("scrolling produced no new item request: %+v", requests)
	}
	last := requests[len(requests)-1]
	if last.Index <= first.Index || last.Index >= total || last.Width <= 0 || last.Width > 40 {
		t.Fatalf("scrolled item request = %+v", last)
	}
	// A new visible window changes the list's element but not the selected
	// row, so scrolling must not queue another selection.
	if got := session.eventsOf(domainnativeui.EventSelection); len(got) != len(selections) {
		t.Fatalf("scrolling re-emitted the unchanged selection: %+v", got)
	}
}

func animationTree(width float64) domainnativeui.Batch {
	return domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeContainer),
			prop(1, domainnativeui.PropLayout, domainnativeui.IntValue(int64(domainnativeui.LayoutColumn))),
			children(1, 2),
			prop(2, domainnativeui.PropWidth, domainnativeui.FloatValue(width)),
			prop(2, domainnativeui.PropHeight, domainnativeui.FloatValue(40)),
			prop(2, domainnativeui.PropTransitionMs, domainnativeui.IntValue(150)),
			prop(2, domainnativeui.PropTransitionEase, domainnativeui.IntValue(int64(domainnativeui.EaseLinear))),
		},
	}
}

func TestTransitionEndsWithoutCallingTheGuest(t *testing.T) {
	tree := buildTree(t, animationTree(100))
	session := newFakeSession(tree)
	polls := 0
	surface := pluginview.New(session, pluginview.WithPoll(func() {
		polls++
		session.Drain()
	}))
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 420, 220)
	tt.Frame()
	session.Drain()
	polls = 0

	if _, err := tree.Apply(domainnativeui.Batch{Ops: []domainnativeui.Op{
		prop(2, domainnativeui.PropWidth, domainnativeui.FloatValue(260)),
	}}, &resolver{}); err != nil {
		t.Fatalf("width patch: %v", err)
	}
	tt.Frame()
	if ends := session.eventsOf(domainnativeui.EventAnimationEnd); len(ends) != 0 {
		t.Fatalf("animation ended before it ran: %+v", ends)
	}

	// Halfway through, the host is still moving the node and the guest has
	// not been called to drive a frame.
	tt.Advance(75 * time.Millisecond)
	if ends := session.eventsOf(domainnativeui.EventAnimationEnd); len(ends) != 0 {
		t.Fatalf("animation ended early: %+v", ends)
	}
	if polls != 0 {
		t.Fatalf("the guest was polled %d times during the animation", polls)
	}

	tt.Advance(200 * time.Millisecond)
	tt.Frame()
	ends := session.eventsOf(domainnativeui.EventAnimationEnd)
	if len(ends) != 1 || ends[0].Node != domainnativeui.IDFromNode(2) {
		t.Fatalf("animation end events = %+v", ends)
	}
	// The end is reported once, even after later frames.
	tt.Frame()
	if got := session.eventsOf(domainnativeui.EventAnimationEnd); len(got) != 1 {
		t.Fatalf("animation end repeated: %+v", got)
	}
}

func TestTransitionEndsForALayoutPropertyChange(t *testing.T) {
	tree := buildTree(t, animationTree(100))
	session := newFakeSession(tree)
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 420, 220)
	tt.Frame()
	session.Drain()

	// A margin change moves the node even though its width and height stay
	// the same; the host still reports the end once the move settles.
	if _, err := tree.Apply(domainnativeui.Batch{Ops: []domainnativeui.Op{
		prop(2, domainnativeui.PropMarginStart, domainnativeui.FloatValue(24)),
	}}, &resolver{}); err != nil {
		t.Fatalf("margin patch: %v", err)
	}
	tt.Frame()
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	ends := session.eventsOf(domainnativeui.EventAnimationEnd)
	if len(ends) != 1 || ends[0].Node != domainnativeui.IDFromNode(2) {
		t.Fatalf("layout animation end events = %+v", ends)
	}
}

func TestReducedMotionEndsTransitionsImmediately(t *testing.T) {
	tree := buildTree(t, animationTree(100))
	session := newFakeSession(tree)
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 420, 220)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	session.Drain()

	if _, err := tree.Apply(domainnativeui.Batch{Ops: []domainnativeui.Op{
		prop(2, domainnativeui.PropWidth, domainnativeui.FloatValue(260)),
	}}, &resolver{}); err != nil {
		t.Fatalf("width patch: %v", err)
	}
	tt.Frame()
	ends := session.eventsOf(domainnativeui.EventAnimationEnd)
	if len(ends) != 1 || ends[0].Node != domainnativeui.IDFromNode(2) {
		t.Fatalf("reduced-motion animation end events = %+v", ends)
	}
	// Nothing keeps moving in the background.
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if got := session.eventsOf(domainnativeui.EventAnimationEnd); len(got) != 1 {
		t.Fatalf("reduced-motion transition kept running: %+v", got)
	}
}

func graphAndButtonTree() domainnativeui.Batch {
	return domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeGraph),
			create(3, domainnativeui.NodeGraphNode),
			create(4, domainnativeui.NodeButton),
			prop(1, domainnativeui.PropLayout, domainnativeui.IntValue(int64(domainnativeui.LayoutColumn))),
			children(1, 2, 4),
			label(2, "Dependencies"),
			children(2, 3),
			label(3, "Graph node"),
			label(4, "Continue"),
		},
	}
}

func TestGraphNodesAreNotFocusTargets(t *testing.T) {
	session := newFakeSession(buildTree(t, graphAndButtonTree()))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 420, 320)
	tt.Frame()

	// The graph container is a real focusable widget; the node inside it is
	// drawn by the host and must not be a phantom stop in the tab order.
	order := session.FocusOrder()
	if len(order) != 2 || order[0] != 2 || order[1] != 4 {
		t.Fatalf("focus order = %v, want the graph then the button", order)
	}
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if got := session.Focus(); got != 4 {
		t.Fatalf("Tab focus = %d, want the button; a graph node stole focus", got)
	}
	tt.Key(ui.Shift, ui.KeyTab)
	tt.Frame()
	if got := session.Focus(); got != 2 {
		t.Fatalf("Shift+Tab focus = %d, want the graph container", got)
	}
}

func gridTree() domainnativeui.Batch {
	return domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeDataGrid),
			create(3, domainnativeui.NodeDataColumn),
			create(4, domainnativeui.NodeDataColumn),
			create(5, domainnativeui.NodeDataRow),
			create(6, domainnativeui.NodeDataRow),
			create(7, domainnativeui.NodeText),
			create(8, domainnativeui.NodeText),
			create(9, domainnativeui.NodeText),
			create(10, domainnativeui.NodeText),
			prop(1, domainnativeui.PropLayout, domainnativeui.IntValue(int64(domainnativeui.LayoutColumn))),
			children(1, 2),
			label(2, "Files"),
			prop(2, domainnativeui.PropWidth, domainnativeui.FloatValue(400)),
			prop(2, domainnativeui.PropHeight, domainnativeui.FloatValue(220)),
			children(2, 3, 4, 5, 6),
			label(3, "Name"),
			prop(3, domainnativeui.PropSortable, domainnativeui.BoolValue(true)),
			label(4, "Size"),
			prop(4, domainnativeui.PropSortable, domainnativeui.BoolValue(true)),
			prop(5, domainnativeui.PropRowIndex, domainnativeui.IntValue(0)),
			children(5, 7, 8),
			prop(6, domainnativeui.PropRowIndex, domainnativeui.IntValue(1)),
			children(6, 9, 10),
			text(7, "alpha"),
			text(8, "10"),
			text(9, "beta"),
			text(10, "20"),
		},
	}
}

func TestGridSortHeaderDoesNotSelectARow(t *testing.T) {
	session := newFakeSession(buildTree(t, gridTree()))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 520, 360)
	tt.Frame()

	if err := tt.Click("Name"); err != nil {
		t.Fatalf("Click sort header: %v", err)
	}
	tt.Frame()
	changes := session.eventsOf(domainnativeui.EventChange)
	if len(changes) != 1 || changes[0].Index != 0 || changes[0].Text != "asc" {
		t.Fatalf("sort change events = %+v", changes)
	}
	if selections := session.eventsOf(domainnativeui.EventSelection); len(selections) != 0 {
		t.Fatalf("sorting fabricated a selection: %+v", selections)
	}

	// Clicking a real row still selects it.
	if err := tt.Click("alpha"); err != nil {
		t.Fatalf("Click row: %v", err)
	}
	tt.Frame()
	selections := session.eventsOf(domainnativeui.EventSelection)
	if len(selections) != 1 || selections[0].Index != 0 {
		t.Fatalf("row selection events = %+v", selections)
	}
}

func pngBytes(t *testing.T, width, height int, fill color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, fill)
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buffer.Bytes()
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestPackagedAssetsLoadOnlyPackagedImagesAndFonts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "images/logo.png", pngBytes(t, 16, 16, color.RGBA{R: 220, A: 255}))
	writeFile(t, root, "images/icon.svg", []byte(
		`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"><rect width="24" height="24" fill="#e00"/></svg>`))
	writeFile(t, root, "fonts/Go-Regular.ttf", goregular.TTF)
	writeFile(t, root, "images/broken.png", []byte("not a PNG"))
	writeFile(t, root, "images/huge.png", nil)
	if err := os.Truncate(filepath.Join(root, "images/huge.png"), pluginview.MaxPackagedImageBytes+1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	writeFile(t, root, "images/wide.png", pngBytes(t, pluginview.MaxPackagedImageDimension+1, 1, color.RGBA{A: 255}))
	writeFile(t, root, "images/pixelbomb.png", pngBytes(t, 4096, 2049, color.RGBA{A: 255}))
	writeFile(t, root, "images/unsupported.txt", pngBytes(t, 4, 4, color.RGBA{A: 255}))
	writeFile(t, root, "fonts/huge.ttf", nil)
	if err := os.Truncate(filepath.Join(root, "fonts/huge.ttf"), pluginview.MaxPackagedFontBytes+1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// A symlink that escapes the package must not resolve.
	outside := t.TempDir()
	writeFile(t, outside, "outside.png", pngBytes(t, 4, 4, color.RGBA{A: 255}))
	if err := os.Symlink(filepath.Join(outside, "outside.png"), filepath.Join(root, "images/escape.png")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	assets := pluginview.NewPackagedAssets(root)

	first, ok := assets.Resolve("images/logo.png")
	if !ok {
		t.Fatal("packaged PNG did not resolve")
	}
	if bitmap, ok := first.(*ui.Bitmap); !ok {
		t.Fatalf("PNG source = %#v", first)
	} else if w, h := bitmap.Size(); w != 16 || h != 16 {
		t.Fatalf("PNG size = %dx%d", w, h)
	}
	again, ok := assets.Resolve("images/logo.png")
	if !ok || again != first {
		t.Fatalf("the decoded PNG was not cached: %#v, %#v", first, again)
	}
	if svg, ok := assets.Resolve("images/icon.svg"); !ok {
		t.Fatal("packaged SVG did not resolve")
	} else if parsed, ok := svg.(*ui.SVG); !ok {
		t.Fatalf("SVG source = %#v", svg)
	} else if w, h := parsed.Size(); w != 24 || h != 24 {
		t.Fatalf("SVG size = %vx%v", w, h)
	}

	for _, src := range []string{
		"",
		"https://example.com/logo.png",
		"data:image/png;base64,AAAA",
		"images/../logo.png",
		"/etc/passwd.png",
		`images\logo.png`,
		"images/escape.png",
		"images/broken.png",
		"images/huge.png",
		"images/wide.png",
		"images/pixelbomb.png",
		"images/unsupported.txt",
		"images/missing.png",
	} {
		if _, ok := assets.Resolve(src); ok {
			t.Errorf("image id %q was accepted", src)
		}
	}

	family, ok := assets.ResolveFont("fonts/Go-Regular.ttf")
	if !ok || family == "" {
		t.Fatal("packaged font did not resolve")
	}
	if again, ok := assets.ResolveFont("fonts/Go-Regular.ttf"); !ok || again != family {
		t.Fatalf("font family = %q then %q", family, again)
	}
	for _, src := range []string{
		"",
		"https://example.com/font.ttf",
		"fonts/../fonts/Go-Regular.ttf",
		"fonts/missing.ttf",
		"fonts/huge.ttf",
		"fonts/Go-Regular.woff",
		"images/logo.png",
		"images/escape.png",
	} {
		if _, ok := assets.ResolveFont(src); ok {
			t.Errorf("font id %q was accepted", src)
		}
	}
}

func imageTree(src, alt string) domainnativeui.Batch {
	return domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeImage),
			prop(1, domainnativeui.PropLayout, domainnativeui.IntValue(int64(domainnativeui.LayoutColumn))),
			children(1, 2),
			prop(2, domainnativeui.PropSrc, domainnativeui.StringValue(src)),
			prop(2, domainnativeui.PropAlt, domainnativeui.StringValue(alt)),
		},
	}
}

func TestPackagedImageRendersAndRemoteURLFallsBackToAlt(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "logo.png", pngBytes(t, 24, 24, color.RGBA{R: 230, A: 255}))
	assets := pluginview.NewPackagedAssets(root)

	tree := buildTree(t, imageTree("logo.png", "Packaged logo"))
	session := newFakeSession(tree)
	surface := pluginview.New(session, pluginview.WithAssets(assets.Resolve))
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 320, 240)
	tt.Frame()
	if tt.HasText("Packaged logo") {
		t.Fatalf("the packaged image fell back to its alt: %v", tt.Texts())
	}
	img := tt.Image()
	red := 0
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			if c := img.RGBAAt(x, y); c.R > 180 && c.G < 80 && c.B < 80 {
				red++
			}
		}
	}
	if red < 100 {
		t.Fatalf("packaged image painted %d red pixels", red)
	}

	// A remote URL is rejected at the ABI boundary, not merely left to the
	// resolver, and an id the package does not contain falls back to alt.
	if _, err := domainnativeui.NewTree().Apply(
		imageTree("https://example.com/tracker.png", "Remote image"), &resolver{}); err == nil {
		t.Fatal("a remote image URL was accepted into the tree")
	}
	remoteTree := buildTree(t, imageTree("missing.png", "Remote image"))
	remoteSession := newFakeSession(remoteTree)
	remote := pluginview.New(remoteSession, pluginview.WithAssets(assets.Resolve))
	defer remote.Close()
	remoteTester := ui.NewTester(func(c *ui.Context) { remote.ViewFocused(c, true) }, 320, 240)
	remoteTester.Frame()
	if !remoteTester.HasText("Remote image") {
		t.Fatalf("missing image did not fall back to alt: %v", remoteTester.Texts())
	}
}

func TestPackagedFontIsUsedByRichText(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "fonts/Go-Regular.ttf", goregular.TTF)
	assets := pluginview.NewPackagedAssets(root)
	var asked []string
	resolve := func(src string) (string, bool) {
		asked = append(asked, src)
		return assets.ResolveFont(src)
	}

	batch := richTextTree()
	batch.Ops = append(batch.Ops,
		prop(2, domainnativeui.PropFontAsset, domainnativeui.StringValue("fonts/Go-Regular.ttf")))
	session := newFakeSession(buildTree(t, batch))
	surface := pluginview.New(session, pluginview.WithFonts(resolve))
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 480, 320)
	tt.Frame()
	if !tt.HasText(richParagraph) {
		t.Fatalf("paragraph with a packaged font shows %v", tt.Texts())
	}
	if len(asked) == 0 || asked[0] != "fonts/Go-Regular.ttf" {
		t.Fatalf("font resolver was asked for %v", asked)
	}
}
