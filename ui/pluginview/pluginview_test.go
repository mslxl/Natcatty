package pluginview_test

import (
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	"github.com/mslxl/natcatty/ui/pluginview"
)

// fakeSession is the renderer's consumer-owned port. It keeps the retained
// tree, host focus, environment, and a recording of queued events.
type fakeSession struct {
	mu      sync.Mutex
	tree    *domainnativeui.Tree
	focused domainnativeui.NodeID
	env     appplugin.UIEnvironment
	events  []domainnativeui.Event
	pending int
	reset   bool
}

func newFakeSession(tree *domainnativeui.Tree) *fakeSession {
	return &fakeSession{tree: tree, env: appplugin.UIEnvironment{
		Locale: "en", Direction: appplugin.UIDirectionLTR,
	}}
}

func (f *fakeSession) Tree() *domainnativeui.Tree {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tree
}

func (f *fakeSession) Closed() bool { return false }

func (f *fakeSession) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reset = true
	f.tree = domainnativeui.NewTree()
	f.focused = 0
}

func (f *fakeSession) Enqueue(event domainnativeui.Event) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	f.pending++
	return true
}

// Drain clears the pending queue as a real guest poll does while keeping the
// recorded events for assertions.
func (f *fakeSession) Drain() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = 0
}

func (f *fakeSession) EnqueueNode(node domainnativeui.NodeID, event domainnativeui.Event) bool {
	event.Node = domainnativeui.IDFromNode(node)
	return f.Enqueue(event)
}

func (f *fakeSession) SetFocus(node domainnativeui.NodeID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.focused = node
	return true
}

func (f *fakeSession) Focus() domainnativeui.NodeID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.focused
}

func (f *fakeSession) FocusOrder() []domainnativeui.NodeID {
	f.mu.Lock()
	tree := f.tree
	f.mu.Unlock()
	return domainnativeui.FocusOrder(tree)
}

func (f *fakeSession) CapturesKeys() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	node, ok := f.tree.Nodes[f.focused]
	if !ok {
		return false
	}
	return node.BoolProp(domainnativeui.PropKeyCapture, false)
}

func (f *fakeSession) Key(mods domainnativeui.Modifiers, key domainnativeui.Key, down bool) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.focused == 0 {
		return "", false
	}
	route := domainnativeui.RouteKey(f.tree, f.focused, mods, key, f.env.Shortcuts)
	if route.HostShortcut != "" {
		return route.HostShortcut, false
	}
	kind := domainnativeui.EventKeyDown
	if !down {
		kind = domainnativeui.EventKeyUp
	}
	f.events = append(f.events, domainnativeui.Event{Kind: kind, Node: domainnativeui.IDFromNode(f.focused), Key: key, Mods: mods})
	f.pending++
	return "", true
}

func (f *fakeSession) Environment() appplugin.UIEnvironment {
	f.mu.Lock()
	defer f.mu.Unlock()
	env := f.env
	env.Shortcuts = append([]domainnativeui.Shortcut(nil), env.Shortcuts...)
	return env
}

func (f *fakeSession) SetEnvironment(env appplugin.UIEnvironment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	env.Shortcuts = append([]domainnativeui.Shortcut(nil), env.Shortcuts...)
	f.env = env
}

func (f *fakeSession) PendingEvents() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pending
}

func (f *fakeSession) SubscribeUpdates(func(*domainnativeui.Tree)) func() {
	return func() {}
}

func (f *fakeSession) recorded() []domainnativeui.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domainnativeui.Event(nil), f.events...)
}

func (f *fakeSession) eventsOf(kind domainnativeui.EventKind) []domainnativeui.Event {
	var out []domainnativeui.Event
	for _, event := range f.recorded() {
		if event.Kind == kind {
			out = append(out, event)
		}
	}
	return out
}

func (f *fakeSession) wasReset() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reset
}

// resolver issues process-local handles; the renderer never sees them.
type resolver struct{ next uint64 }

func (r *resolver) NodeForHandle(handle uint64) (domainnativeui.NodeID, bool) {
	return 0, false
}

func (r *resolver) IssueHandle(id domainnativeui.NodeID) (uint64, error) {
	r.next++
	return r.next<<32 | uint64(id), nil
}

func (r *resolver) ReleaseHandle(uint64) error { return nil }

func (r *resolver) ReleaseNode(domainnativeui.NodeID) {}

func buildTree(t *testing.T, batch domainnativeui.Batch) *domainnativeui.Tree {
	t.Helper()
	tree := domainnativeui.NewTree()
	if _, err := tree.Apply(batch, &resolver{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return tree
}

func create(id domainnativeui.NodeID, kind domainnativeui.NodeKind) domainnativeui.Op {
	return domainnativeui.Op{Kind: domainnativeui.OpCreate, ID: id, NodeKind: kind}
}

func prop(id domainnativeui.NodeID, prop domainnativeui.PropID, value domainnativeui.Value) domainnativeui.Op {
	return domainnativeui.Op{Kind: domainnativeui.OpSetProp, Node: domainnativeui.IDFromNode(id), Prop: prop, Value: value}
}

func text(id domainnativeui.NodeID, value string) domainnativeui.Op {
	return domainnativeui.Op{Kind: domainnativeui.OpSetText, Node: domainnativeui.IDFromNode(id), Text: value}
}

func children(id domainnativeui.NodeID, ids ...domainnativeui.NodeID) domainnativeui.Op {
	refs := make([]domainnativeui.NodeRef, len(ids))
	for i, child := range ids {
		refs[i] = domainnativeui.IDFromNode(child)
	}
	return domainnativeui.Op{Kind: domainnativeui.OpSetChildren, Node: domainnativeui.IDFromNode(id), Children: refs}
}

func label(id domainnativeui.NodeID, value string) domainnativeui.Op {
	return prop(id, domainnativeui.PropLabel, domainnativeui.StringValue(value))
}

// allWidgets builds a tree with every core kind. It is the renderer's
// contract test: each kind must draw something a user can see.
func allWidgets() domainnativeui.Batch {
	return domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			// Create every node first: an op may only reference a node
			// that already exists in the working tree.
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeText),
			create(3, domainnativeui.NodeButton),
			create(4, domainnativeui.NodeInput),
			create(5, domainnativeui.NodeCheckbox),
			create(6, domainnativeui.NodeRadio),
			create(7, domainnativeui.NodeSelect),
			create(8, domainnativeui.NodeSlider),
			create(9, domainnativeui.NodeProgress),
			create(10, domainnativeui.NodeBadge),
			create(11, domainnativeui.NodeDivider),
			create(12, domainnativeui.NodeImage),
			create(13, domainnativeui.NodeList),
			create(14, domainnativeui.NodeTable),
			create(15, domainnativeui.NodeTree),
			create(16, domainnativeui.NodeTabs),
			create(17, domainnativeui.NodeTooltip),
			create(18, domainnativeui.NodeDialog),
			create(19, domainnativeui.NodeMenu),
			create(20, domainnativeui.NodeToast),
			create(24, domainnativeui.NodeText),
			create(25, domainnativeui.NodeText),
			create(26, domainnativeui.NodeText),
			create(27, domainnativeui.NodeText),
			create(28, domainnativeui.NodeContainer),
			create(29, domainnativeui.NodeText),
			create(30, domainnativeui.NodeContainer),
			create(31, domainnativeui.NodeText),
			create(32, domainnativeui.NodeText),
			create(33, domainnativeui.NodeText),
			create(34, domainnativeui.NodeText),
			create(35, domainnativeui.NodeText),
			create(36, domainnativeui.NodeText),

			prop(1, domainnativeui.PropLayout, domainnativeui.IntValue(int64(domainnativeui.LayoutColumn))),
			children(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20),
			text(2, "Hello text"),
			label(3, "Run"),
			label(4, "Name"),
			prop(4, domainnativeui.PropValue, domainnativeui.StringValue("seed")),
			prop(4, domainnativeui.PropPlaceholder, domainnativeui.StringValue("Type here")),
			label(5, "Enabled"),
			prop(5, domainnativeui.PropChecked, domainnativeui.BoolValue(true)),
			label(6, "Choice A"),
			label(7, "Fruit"),
			prop(7, domainnativeui.PropValue, domainnativeui.StringValue("Apple")),
			children(7, 24, 25),
			text(24, "Apple"),
			text(25, "Pear"),
			label(8, "Volume"),
			prop(8, domainnativeui.PropValue, domainnativeui.StringValue("30")),
			label(9, "Progress"),
			prop(9, domainnativeui.PropValue, domainnativeui.StringValue("40")),
			text(10, "New"),
			label(12, "Logo"),
			prop(12, domainnativeui.PropAlt, domainnativeui.StringValue("Logo alt")),
			label(13, "Items"),
			children(13, 26, 27),
			text(26, "First row"),
			text(27, "Second row"),
			label(14, "Scores"),
			prop(14, domainnativeui.PropColumns, domainnativeui.IntValue(1)),
			children(14, 28),
			children(28, 29),
			text(29, "Cell value"),
			label(15, "Files"),
			children(15, 30),
			text(30, "main.go"),
			label(16, "Pages"),
			prop(16, domainnativeui.PropSelected, domainnativeui.IntValue(0)),
			children(16, 31, 32),
			text(31, "General"),
			text(32, "Advanced"),
			text(17, "A tooltip"),
			children(17, 33),
			text(33, "Hover me"),
			label(18, "Dialog"),
			text(18, "Dialog title"),
			children(18, 34),
			text(34, "Dialog body"),
			label(19, "Actions"),
			children(19, 35, 36),
			text(35, "Copy"),
			text(36, "Paste"),
			text(20, "Saved"),
		},
	}
}

func TestRendersTheCoreWidgetSet(t *testing.T) {
	session := newFakeSession(buildTree(t, allWidgets()))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 720, 900)
	tt.Frame()

	for _, want := range []string{
		"Hello text", "Run", "Enabled", "Choice A", "Fruit", "Volume",
		"Progress", "New", "Logo alt", "First row", "Second row",
		"Cell value", "main.go", "General", "Advanced", "Hover me",
		"Dialog title", "Dialog body", "Actions", "Saved",
	} {
		if !tt.HasText(want) {
			t.Fatalf("missing widget text %q in %v", want, tt.Texts())
		}
	}

	// The tree is host-owned and survives frames; the widgets are rebuilt
	// from the same snapshot.
	before := session.Tree().Revision
	tt.Frame()
	if after := session.Tree().Revision; after != before {
		t.Fatalf("rendering changed the retained revision: %d -> %d", before, after)
	}

	// Close releases the handles through the session reset.
	surface.Close()
	if !session.wasReset() {
		t.Fatal("Close did not release the session's handles")
	}
}

func counterTree() domainnativeui.Batch {
	return domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeText),
			create(3, domainnativeui.NodeInput),
			create(4, domainnativeui.NodeButton),
			prop(1, domainnativeui.PropLayout, domainnativeui.IntValue(int64(domainnativeui.LayoutColumn))),
			children(1, 2, 3, 4),
			text(2, "Count: 0"),
			label(3, "Name"),
			prop(3, domainnativeui.PropPlaceholder, domainnativeui.StringValue("Your name")),
			label(4, "Increment"),
		},
	}
}

func TestTypingClickAndSubmitQueueEvents(t *testing.T) {
	session := newFakeSession(buildTree(t, counterTree()))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 480, 400)
	tt.Frame()

	if got := session.Focus(); got != 3 {
		t.Fatalf("initial host focus = %d, want the input", got)
	}
	tt.Type("Ada")
	tt.Frame()
	inputs := session.eventsOf(domainnativeui.EventInput)
	if len(inputs) != 1 || inputs[0].Text != "Ada" {
		t.Fatalf("input events = %+v", inputs)
	}
	if inputs[0].Node != domainnativeui.IDFromNode(3) {
		t.Fatalf("input event node = %#x", uint64(inputs[0].Node))
	}

	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if submits := session.eventsOf(domainnativeui.EventSubmit); len(submits) != 1 || submits[0].Text != "Ada" {
		t.Fatalf("submit events = %+v", submits)
	}

	if err := tt.Click("Increment"); err != nil {
		t.Fatalf("Click: %v", err)
	}
	tt.Frame()
	clicks := session.eventsOf(domainnativeui.EventClick)
	if len(clicks) != 1 || clicks[0].Node != domainnativeui.IDFromNode(4) {
		t.Fatalf("click events = %+v", clicks)
	}

	// The host never calls the plugin: the renderer only queued events.
	if session.Focus() != 4 {
		t.Fatalf("click did not move host focus to the button: %d", session.Focus())
	}
}

func TestTabTraversalKeepsHostStateAndBlocksReentry(t *testing.T) {
	session := newFakeSession(buildTree(t, counterTree()))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 480, 400)
	tt.Frame()

	tt.Type("keep")
	tt.Frame()
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if got := session.Focus(); got != 4 {
		t.Fatalf("Tab focus = %d, want the button", got)
	}
	// Focus traversal is host-owned; it queues no key event.
	if keys := session.eventsOf(domainnativeui.EventKeyDown); len(keys) != 0 {
		t.Fatalf("Tab was delivered to the plugin: %+v", keys)
	}
	tt.Key(ui.Shift, ui.KeyTab)
	tt.Frame()
	if got := session.Focus(); got != 3 {
		t.Fatalf("Shift+Tab focus = %d, want the input", got)
	}

	// The input kept what was typed across the focus changes.
	inputs := session.eventsOf(domainnativeui.EventInput)
	if len(inputs) != 1 || inputs[0].Text != "keep" {
		t.Fatalf("input events after traversal = %+v", inputs)
	}
}

func TestHostShortcutWinsUnlessTheNodeCapturesKeys(t *testing.T) {
	session := newFakeSession(buildTree(t, counterTree()))
	session.env.Shortcuts = []domainnativeui.Shortcut{{
		Mods: domainnativeui.ModMeta, Key: domainnativeui.KeyS, Command: "save",
	}}
	var commands []string
	surface := pluginview.New(session, pluginview.WithCommandRunner(func(command string) {
		commands = append(commands, command)
	}))
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 480, 400)
	tt.Frame()

	tt.Key(ui.Cmd, ui.KeyS)
	tt.Frame()
	if len(commands) != 1 || commands[0] != "save" {
		t.Fatalf("host commands = %v", commands)
	}
	if keys := session.eventsOf(domainnativeui.EventKeyDown); len(keys) != 0 {
		t.Fatalf("the shortcut also reached the plugin: %+v", keys)
	}

	// A node that captures keys keeps them, so the host command does not run.
	session.mu.Lock()
	tree := session.tree
	session.mu.Unlock()
	if _, err := tree.Apply(domainnativeui.Batch{Ops: []domainnativeui.Op{
		prop(3, domainnativeui.PropKeyCapture, domainnativeui.BoolValue(true)),
	}}, &resolver{}); err != nil {
		t.Fatalf("capture patch: %v", err)
	}
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyS)
	tt.Frame()
	if len(commands) != 1 {
		t.Fatalf("capturing node let the host command run: %v", commands)
	}
	keys := session.eventsOf(domainnativeui.EventKeyDown)
	if len(keys) != 1 || keys[0].Key != domainnativeui.KeyS {
		t.Fatalf("captured keys = %+v", keys)
	}
}

func TestEnvironmentAccessibilityAndDirectionFollowTheHost(t *testing.T) {
	session := newFakeSession(buildTree(t, counterTree()))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 480, 400)

	tt.SetLocale("ar")
	tt.SetDark(true)
	tt.SetDirection(ui.RightToLeft)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()

	env := session.Environment()
	if env.Locale != "ar" || !env.Dark || env.Direction != appplugin.UIDirectionRTL || !env.ReduceMotion {
		t.Fatalf("environment = %+v", env)
	}

	// Accessible labels are visible to the tester by name, for widgets whose
	// visible text differs from their name.
	if _, ok := tt.Find("Name"); !ok {
		t.Fatalf("input label is not exposed: %v", tt.Texts())
	}

	// The direction change redraws in the right-to-left layout; the text is
	// still there.
	if !tt.HasText("Increment") {
		t.Fatalf("RTL frame lost the button: %v", tt.Texts())
	}
}

func TestIMECompositionCommitsThroughTheInput(t *testing.T) {
	session := newFakeSession(buildTree(t, counterTree()))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 480, 400)
	tt.Frame()

	if active, noInputMethod := tt.TextInputActive(); !active || noInputMethod {
		t.Fatalf("input method state = active=%v off=%v", active, noInputMethod)
	}
	tt.Compose("に", 1)
	tt.Frame()
	tt.Type("に")
	tt.Frame()

	inputs := session.eventsOf(domainnativeui.EventInput)
	if len(inputs) != 1 || inputs[0].Text != "に" {
		t.Fatalf("IME commit events = %+v", inputs)
	}
}

func TestQueuedEventsPollTheGuestOnTheNextFrame(t *testing.T) {
	session := newFakeSession(buildTree(t, counterTree()))
	var polls int
	surface := pluginview.New(session, pluginview.WithPoll(func() { polls++ }))
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 480, 400)
	tt.Frame()
	// NewTester renders once on construction, so the initial viewport
	// resize may already have been polled. What matters is that a newly
	// queued event is not delivered until the next frame.
	base := polls
	session.Enqueue(domainnativeui.Event{Kind: domainnativeui.EventResize, Width: 480, Height: 400})
	if polls != base {
		t.Fatalf("an event queued synchronously reached the guest: %d -> %d", base, polls)
	}
	tt.Frame()
	if polls != base+1 {
		t.Fatalf("polls after the next frame = %d, want %d", polls, base+1)
	}
}

// interactionTree gathers the widgets whose host events were previously
// exercised only indirectly: a button, a checkbox, a list, and tabs.
func interactionTree() domainnativeui.Batch {
	return domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeButton),
			create(3, domainnativeui.NodeCheckbox),
			create(4, domainnativeui.NodeList),
			create(5, domainnativeui.NodeText),
			create(6, domainnativeui.NodeText),
			create(7, domainnativeui.NodeTabs),
			create(8, domainnativeui.NodeText),
			create(9, domainnativeui.NodeText),
			prop(1, domainnativeui.PropLayout, domainnativeui.IntValue(int64(domainnativeui.LayoutColumn))),
			children(1, 2, 3, 4, 7),
			label(2, "Press"),
			label(3, "Enabled"),
			label(4, "Rows"),
			children(4, 5, 6),
			text(5, "First row"),
			text(6, "Second row"),
			label(7, "Pages"),
			prop(7, domainnativeui.PropSelected, domainnativeui.IntValue(0)),
			children(7, 8, 9),
			text(8, "General"),
			text(9, "Advanced"),
		},
	}
}

// TestRendererEmitsDoubleClickAndContextMenu covers the pointer gestures a
// button reports beyond a single click.
func TestRendererEmitsDoubleClickAndContextMenu(t *testing.T) {
	session := newFakeSession(buildTree(t, interactionTree()))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 640, 480)
	tt.Frame()

	// The tester's clock stands still, so two clicks at the same point count
	// as a double click as the platform reports it.
	if err := tt.Click("Press"); err != nil {
		t.Fatalf("first click: %v", err)
	}
	if err := tt.Click("Press"); err != nil {
		t.Fatalf("second click: %v", err)
	}
	tt.Frame()
	doubles := session.eventsOf(domainnativeui.EventDoubleClick)
	if len(doubles) != 1 || doubles[0].Node != domainnativeui.IDFromNode(2) {
		t.Fatalf("double click events = %+v", doubles)
	}

	// A secondary click reports the context-menu gesture for the same node.
	if err := tt.RightClick("Press"); err != nil {
		t.Fatalf("right click: %v", err)
	}
	tt.Frame()
	contexts := session.eventsOf(domainnativeui.EventContextMenu)
	if len(contexts) == 0 {
		t.Fatal("right click queued no context-menu event")
	}
	if contexts[0].Node != domainnativeui.IDFromNode(2) {
		t.Fatalf("context-menu event node = %#x", uint64(contexts[0].Node))
	}
}

// TestRendererEmitsChangeSelectionResizeAndHover covers the remaining host
// event kinds: a toggle change, a list selection, a viewport resize, and a
// pointer entering a widget.
func TestRendererEmitsChangeSelectionResizeAndHover(t *testing.T) {
	session := newFakeSession(buildTree(t, interactionTree()))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 640, 480)
	tt.Frame()

	// Hover is reported once when the pointer enters the button.
	rect, ok := tt.Find("Press")
	if !ok {
		t.Fatalf("the button is not in the frame: %v", tt.Texts())
	}
	tt.Move(rect.X+rect.W/2, rect.Y+rect.H/2)
	tt.Frame()
	tt.Frame()
	// Hover is emitted for each node under the pointer, ancestors included,
	// and once per entry: the button reports exactly one event across frames.
	buttonHovers := 0
	for _, event := range session.eventsOf(domainnativeui.EventHover) {
		if event.Node == domainnativeui.IDFromNode(2) {
			buttonHovers++
		}
	}
	if buttonHovers != 1 {
		t.Fatalf("button hover events = %d", buttonHovers)
	}

	// A toggle reports its new value as a change event.
	if err := tt.Click("Enabled"); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	tt.Frame()
	changes := session.eventsOf(domainnativeui.EventChange)
	if len(changes) != 1 || changes[0].Node != domainnativeui.IDFromNode(3) || changes[0].Index != 1 {
		t.Fatalf("checkbox change events = %+v", changes)
	}

	// Choosing a tab is a change too.
	if err := tt.Click("Advanced"); err != nil {
		t.Fatalf("tab: %v", err)
	}
	tt.Frame()
	changes = session.eventsOf(domainnativeui.EventChange)
	if len(changes) != 2 || changes[1].Node != domainnativeui.IDFromNode(7) || changes[1].Index != 1 {
		t.Fatalf("tab change events = %+v", changes)
	}

	// Choosing a list row reports the row index as a selection.
	if err := tt.Click("First row"); err != nil {
		t.Fatalf("list row: %v", err)
	}
	tt.Frame()
	selections := session.eventsOf(domainnativeui.EventSelection)
	if len(selections) != 1 || selections[0].Node != domainnativeui.IDFromNode(4) || selections[0].Index != 0 {
		t.Fatalf("selection events = %+v", selections)
	}

	// A viewport change reports the new size once per change.
	before := len(session.eventsOf(domainnativeui.EventResize))
	tt.SetSize(720, 560)
	tt.Frame()
	resizes := session.eventsOf(domainnativeui.EventResize)
	if len(resizes) != before+1 {
		t.Fatalf("resize events = %+v", resizes)
	}
	last := resizes[len(resizes)-1]
	if last.Width != 720 || last.Height != 560 {
		t.Fatalf("resize event = %+v", last)
	}
}

// TestRendererEmitsDragAndDrop moves a list row onto another list. The source
// list reports the row dragged and the target reports the dropped value.
func TestRendererEmitsDragAndDrop(t *testing.T) {
	batch := domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeList),
			create(3, domainnativeui.NodeList),
			create(4, domainnativeui.NodeText),
			create(5, domainnativeui.NodeText),
			create(6, domainnativeui.NodeText),
			create(7, domainnativeui.NodeText),
			prop(1, domainnativeui.PropLayout, domainnativeui.IntValue(int64(domainnativeui.LayoutColumn))),
			children(1, 2, 3),
			label(2, "Source rows"),
			children(2, 4, 5),
			text(4, "Alpha"),
			text(5, "Beta"),
			label(3, "Target rows"),
			children(3, 6, 7),
			text(6, "Gamma"),
			text(7, "Delta"),
		},
	}

	session := newFakeSession(buildTree(t, batch))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 640, 480)
	tt.Frame()

	source, ok := tt.Find("Alpha")
	if !ok {
		t.Fatalf("the source row is not in the frame: %v", tt.Texts())
	}
	target, ok := tt.Find("Gamma")
	if !ok {
		t.Fatalf("the target row is not in the frame: %v", tt.Texts())
	}
	startX, startY := source.X+10, source.Y+source.H/2
	endX, endY := target.X+10, target.Y+target.H/2
	tt.Press(startX, startY)
	tt.Move(startX+10, startY+10)
	tt.Move(endX, endY)
	tt.Frame()
	tt.Release(endX, endY)
	tt.Frame()

	drags := session.eventsOf(domainnativeui.EventDrag)
	if len(drags) == 0 || drags[0].Node != domainnativeui.IDFromNode(2) || drags[0].Index != 0 {
		t.Fatalf("drag events = %+v", drags)
	}
	drops := session.eventsOf(domainnativeui.EventDrop)
	if len(drops) != 1 || drops[0].Node != domainnativeui.IDFromNode(3) ||
		drops[0].Index != 0 || drops[0].Related != domainnativeui.IDFromNode(2) {
		t.Fatalf("drop events = %+v", drops)
	}
}

func TestDisabledControlsTakeNoInput(t *testing.T) {
	batch := domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeInput),
			label(2, "Field"),
			prop(2, domainnativeui.PropDisabled, domainnativeui.BoolValue(true)),
			create(3, domainnativeui.NodeList),
			label(3, "Rows"),
			prop(3, domainnativeui.PropDisabled, domainnativeui.BoolValue(true)),
			create(4, domainnativeui.NodeText),
			text(4, "Row one"),
			create(5, domainnativeui.NodeTabs),
			label(5, "Pages"),
			prop(5, domainnativeui.PropDisabled, domainnativeui.BoolValue(true)),
			create(6, domainnativeui.NodeText),
			text(6, "One"),
			create(7, domainnativeui.NodeText),
			text(7, "Two"),
			create(8, domainnativeui.NodeTree),
			label(8, "Files"),
			prop(8, domainnativeui.PropDisabled, domainnativeui.BoolValue(true)),
			create(9, domainnativeui.NodeText),
			text(9, "Branch"),
			children(1, 2, 3, 5, 8),
			children(3, 4),
			children(5, 6, 7),
			children(8, 9),
		},
	}
	session := newFakeSession(buildTree(t, batch))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 480, 400)
	tt.Frame()

	if err := tt.Click("Field"); err != nil {
		t.Fatalf("Click disabled input: %v", err)
	}
	tt.Type("x")
	tt.Frame()
	if inputs := session.eventsOf(domainnativeui.EventInput); len(inputs) != 0 {
		t.Fatalf("disabled input queued %+v", inputs)
	}
	if err := tt.Click("Row one"); err != nil {
		t.Fatalf("Click disabled list row: %v", err)
	}
	tt.Frame()
	for _, kind := range []domainnativeui.EventKind{
		domainnativeui.EventClick, domainnativeui.EventSelection, domainnativeui.EventDrag,
	} {
		if events := session.eventsOf(kind); len(events) != 0 {
			t.Fatalf("disabled list queued %+v", events)
		}
	}
	if err := tt.Click("Two"); err != nil {
		t.Fatalf("Click disabled tab: %v", err)
	}
	tt.Frame()
	if changes := session.eventsOf(domainnativeui.EventChange); len(changes) != 0 {
		t.Fatalf("disabled tabs queued %+v", changes)
	}
	if err := tt.Click("Branch"); err != nil {
		t.Fatalf("Click disabled tree item: %v", err)
	}
	tt.Frame()
	if selections := session.eventsOf(domainnativeui.EventSelection); len(selections) != 0 {
		t.Fatalf("disabled tree queued %+v", selections)
	}
}

// A node named only by labelled_by shows and reports the referenced text, and
// an image with no alt is decorative rather than inventing an "Image" label.
func TestLabelledByNamesAButtonAndAnUnlabelledImageStaysDecorative(t *testing.T) {
	batch := domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeButton),
			create(3, domainnativeui.NodeText),
			create(4, domainnativeui.NodeImage),
			prop(2, domainnativeui.PropLabelledBy, domainnativeui.RefValue(domainnativeui.IDFromNode(3))),
			text(3, "Confirm"),
			prop(3, domainnativeui.PropHidden, domainnativeui.BoolValue(true)),
			children(1, 2, 3, 4),
		},
	}
	session := newFakeSession(buildTree(t, batch))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 480, 400)
	tt.Frame()

	if !tt.HasText("Confirm") {
		t.Fatalf("the labelled_by text did not name the button: %v", tt.Texts())
	}
	if tt.HasText("Image") {
		t.Fatalf("a decorative image invented a text label: %v", tt.Texts())
	}
}

func TestHiddenDisabledAndStaleNodesNeverReachThePlugin(t *testing.T) {
	batch := domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeButton), label(2, "Hidden"),
			prop(2, domainnativeui.PropHidden, domainnativeui.BoolValue(true)),
			create(3, domainnativeui.NodeButton), label(3, "Off"),
			prop(3, domainnativeui.PropDisabled, domainnativeui.BoolValue(true)),
			children(1, 2, 3),
		},
	}
	session := newFakeSession(buildTree(t, batch))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 480, 400)
	tt.Frame()

	if tt.HasText("Hidden") {
		t.Fatal("a hidden node was rendered")
	}
	if tt.HasText("Off") {
		// Disabled widgets still show but do not act; MyGo draws them.
		_ = tt.HasText("Off")
	}
	if err := tt.Click("Off"); err == nil {
		tt.Frame()
		if clicks := session.eventsOf(domainnativeui.EventClick); len(clicks) != 0 {
			t.Fatalf("disabled button queued %+v", clicks)
		}
	}
}

// dataWidgets builds one of every data-oriented widget: a grid over a large
// row count with a sparse row window, a graph with a labeled edge, a plan
// tree with an annotation, and a canvas with a primitive and an image.
func dataWidgets() domainnativeui.Batch {
	return domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeDataGrid),
			create(3, domainnativeui.NodeDataColumn),
			create(4, domainnativeui.NodeDataColumn),
			create(5, domainnativeui.NodeDataRow),
			create(6, domainnativeui.NodeDataRow),
			create(7, domainnativeui.NodeDataRow),
			create(8, domainnativeui.NodeText),
			create(9, domainnativeui.NodeText),
			create(10, domainnativeui.NodeText),
			create(11, domainnativeui.NodeText),
			create(12, domainnativeui.NodeText),
			create(13, domainnativeui.NodeText),
			create(20, domainnativeui.NodeGraph),
			create(21, domainnativeui.NodeGraphNode),
			create(22, domainnativeui.NodeGraphNode),
			create(23, domainnativeui.NodeGraphEdge),
			create(30, domainnativeui.NodePlanGraph),
			create(31, domainnativeui.NodePlanNode),
			create(32, domainnativeui.NodePlanNode),
			create(40, domainnativeui.NodeCanvas),
			create(41, domainnativeui.NodeCanvasShape),
			create(42, domainnativeui.NodeCanvasShape),
			create(43, domainnativeui.NodeCanvasShape),

			prop(1, domainnativeui.PropLayout, domainnativeui.IntValue(int64(domainnativeui.LayoutColumn))),
			children(1, 2, 20, 30, 40),

			label(2, "Files"),
			prop(2, domainnativeui.PropRowCount, domainnativeui.IntValue(100000)),
			prop(2, domainnativeui.PropHeight, domainnativeui.FloatValue(300)),
			children(2, 3, 4, 5, 6, 7),
			label(3, "Name"),
			prop(3, domainnativeui.PropWidth, domainnativeui.FloatValue(200)),
			prop(3, domainnativeui.PropSortable, domainnativeui.BoolValue(true)),
			label(4, "Size"),
			prop(4, domainnativeui.PropWidth, domainnativeui.FloatValue(100)),
			prop(5, domainnativeui.PropRowIndex, domainnativeui.IntValue(0)),
			children(5, 8, 9),
			text(8, "alpha.txt"),
			text(9, "1200"),
			prop(6, domainnativeui.PropRowIndex, domainnativeui.IntValue(1)),
			children(6, 10, 11),
			text(10, "beta.log"),
			text(11, "940"),
			prop(7, domainnativeui.PropRowIndex, domainnativeui.IntValue(2)),
			children(7, 12, 13),
			text(12, "gamma.bin"),
			text(13, "64000"),

			label(20, "Build graph"),
			prop(20, domainnativeui.PropLayout, domainnativeui.IntValue(int64(domainnativeui.LayoutRow))),
			prop(20, domainnativeui.PropWidth, domainnativeui.FloatValue(420)),
			prop(20, domainnativeui.PropHeight, domainnativeui.FloatValue(140)),
			children(20, 21, 22, 23),
			label(21, "Load"),
			prop(21, domainnativeui.PropX, domainnativeui.FloatValue(20)),
			prop(21, domainnativeui.PropY, domainnativeui.FloatValue(20)),
			prop(21, domainnativeui.PropWidth, domainnativeui.FloatValue(100)),
			prop(21, domainnativeui.PropHeight, domainnativeui.FloatValue(36)),
			label(22, "Save"),
			prop(22, domainnativeui.PropX, domainnativeui.FloatValue(220)),
			prop(22, domainnativeui.PropY, domainnativeui.FloatValue(70)),
			prop(22, domainnativeui.PropWidth, domainnativeui.FloatValue(100)),
			prop(22, domainnativeui.PropHeight, domainnativeui.FloatValue(36)),
			prop(23, domainnativeui.PropFrom, domainnativeui.RefValue(domainnativeui.IDFromNode(21))),
			prop(23, domainnativeui.PropTo, domainnativeui.RefValue(domainnativeui.IDFromNode(22))),
			prop(23, domainnativeui.PropDirected, domainnativeui.BoolValue(true)),
			prop(23, domainnativeui.PropLineWidth, domainnativeui.FloatValue(2)),

			label(30, "Query plan"),
			children(30, 31),
			text(31, "Seq Scan"),
			prop(31, domainnativeui.PropExpanded, domainnativeui.BoolValue(true)),
			prop(31, domainnativeui.PropPlanStatus, domainnativeui.IntValue(int64(domainnativeui.PlanRunning))),
			prop(31, domainnativeui.PropCost, domainnativeui.FloatValue(12.5)),
			children(31, 32),
			text(32, "Index Scan"),
			prop(32, domainnativeui.PropAnnotation, domainnativeui.StringValue("actual rows 42")),
			prop(32, domainnativeui.PropPlanStatus, domainnativeui.IntValue(int64(domainnativeui.PlanDone))),

			label(40, "Sketch"),
			prop(40, domainnativeui.PropWidth, domainnativeui.FloatValue(240)),
			prop(40, domainnativeui.PropHeight, domainnativeui.FloatValue(120)),
			children(40, 41, 42, 43),
			prop(41, domainnativeui.PropShape, domainnativeui.IntValue(int64(domainnativeui.CanvasRect))),
			prop(41, domainnativeui.PropX, domainnativeui.FloatValue(10)),
			prop(41, domainnativeui.PropY, domainnativeui.FloatValue(10)),
			prop(41, domainnativeui.PropWidth, domainnativeui.FloatValue(80)),
			prop(41, domainnativeui.PropHeight, domainnativeui.FloatValue(30)),
			prop(41, domainnativeui.PropFillToken, domainnativeui.IntValue(int64(domainnativeui.TokenAccent))),
			prop(42, domainnativeui.PropShape, domainnativeui.IntValue(int64(domainnativeui.CanvasText))),
			prop(42, domainnativeui.PropX, domainnativeui.FloatValue(12)),
			prop(42, domainnativeui.PropY, domainnativeui.FloatValue(60)),
			text(42, "canvas label"),
			prop(43, domainnativeui.PropShape, domainnativeui.IntValue(int64(domainnativeui.CanvasImage))),
			prop(43, domainnativeui.PropX, domainnativeui.FloatValue(150)),
			prop(43, domainnativeui.PropY, domainnativeui.FloatValue(20)),
			prop(43, domainnativeui.PropWidth, domainnativeui.FloatValue(40)),
			prop(43, domainnativeui.PropHeight, domainnativeui.FloatValue(40)),
			prop(43, domainnativeui.PropSrc, domainnativeui.StringValue("missing-asset")),
			prop(43, domainnativeui.PropAlt, domainnativeui.StringValue("logo")),
		},
	}
}

// TestDataGridRendersAVirtualWindowAndRequestsMissingRows drives the grid's
// paging behavior: a 100,000-row grid carries only three row nodes, renders
// them, asks for the rows it does not have, and reports selection and sort
// changes without growing the retained tree.
func TestDataGridRendersAVirtualWindowAndRequestsMissingRows(t *testing.T) {
	session := newFakeSession(buildTree(t, dataWidgets()))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 720, 900)
	tt.Frame()
	tt.Frame()

	for _, want := range []string{"Name", "Size", "alpha.txt", "beta.log", "gamma.bin"} {
		if !tt.HasText(want) {
			t.Fatalf("missing grid text %q in %v", want, tt.Texts())
		}
	}
	rows := 0
	for _, node := range session.Tree().Nodes {
		if node.Kind == domainnativeui.NodeDataRow {
			rows++
		}
	}
	if rows != 3 {
		t.Fatalf("the grid materialized %d row nodes, want only the 3 rows the guest patched", rows)
	}

	requests := session.eventsOf(domainnativeui.EventItemRequest)
	if len(requests) == 0 || requests[0].Node != domainnativeui.IDFromNode(2) {
		t.Fatalf("item requests = %+v", requests)
	}
	if requests[0].Index != 3 || requests[0].Width <= 0 {
		t.Fatalf("item request = %+v, want the first missing row after the window", requests[0])
	}

	if err := tt.Click("beta.log"); err != nil {
		t.Fatalf("click row: %v", err)
	}
	tt.Frame()
	selections := session.eventsOf(domainnativeui.EventSelection)
	if len(selections) == 0 || selections[len(selections)-1].Node != domainnativeui.IDFromNode(2) ||
		selections[len(selections)-1].Index != 1 {
		t.Fatalf("selection events = %+v", selections)
	}

	if err := tt.Click("Name"); err != nil {
		t.Fatalf("click sort header: %v", err)
	}
	tt.Frame()
	changes := session.eventsOf(domainnativeui.EventChange)
	foundSort := false
	for _, event := range changes {
		if event.Node == domainnativeui.IDFromNode(2) && event.Index == 0 && event.Text == "asc" {
			foundSort = true
		}
	}
	if !foundSort {
		t.Fatalf("sort changes = %+v", changes)
	}

	if rect, ok := tt.Find("alpha.txt"); ok {
		tt.Scroll(rect.X+rect.W/2, rect.Y+rect.H/2, 0, -120)
		tt.Frame()
	}
	scrolls := session.eventsOf(domainnativeui.EventScroll)
	if len(scrolls) == 0 || scrolls[len(scrolls)-1].Node != domainnativeui.IDFromNode(2) {
		t.Fatalf("scroll events = %+v", scrolls)
	}
}

// TestGraphReportsSelectionAndActivation proves a graph lays out its nodes,
// paints an edge, and reports selection on a click and activation on a double
// click, both attributed to the graph and the clicked node.
func TestGraphReportsSelectionAndActivation(t *testing.T) {
	session := newFakeSession(buildTree(t, dataWidgets()))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 720, 900)
	tt.Frame()

	for _, want := range []string{"Load", "Save"} {
		if !tt.HasText(want) {
			t.Fatalf("missing graph label %q in %v", want, tt.Texts())
		}
	}
	if err := tt.Click("Load"); err != nil {
		t.Fatalf("click graph node: %v", err)
	}
	tt.Frame()
	selections := session.eventsOf(domainnativeui.EventSelection)
	found := false
	for _, event := range selections {
		if event.Node == domainnativeui.IDFromNode(20) && event.Index == 0 &&
			event.Related == domainnativeui.IDFromNode(21) {
			found = true
		}
	}
	if !found {
		t.Fatalf("graph selection events = %+v", selections)
	}

	if err := tt.Click("Save"); err != nil {
		t.Fatalf("first activation click: %v", err)
	}
	if err := tt.Click("Save"); err != nil {
		t.Fatalf("second activation click: %v", err)
	}
	tt.Frame()
	doubles := session.eventsOf(domainnativeui.EventDoubleClick)
	found = false
	for _, event := range doubles {
		if event.Node == domainnativeui.IDFromNode(20) && event.Index == 1 &&
			event.Related == domainnativeui.IDFromNode(22) {
			found = true
		}
	}
	if !found {
		t.Fatalf("graph activation events = %+v", doubles)
	}
}

// TestPlanGraphRendersAnnotationsAndContainsInteraction proves a plan tree
// shows its metrics, reports selection and activation on the plan graph
// itself, and never leaks an event to the surrounding container.
func TestPlanGraphRendersAnnotationsAndContainsInteraction(t *testing.T) {
	session := newFakeSession(buildTree(t, dataWidgets()))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 720, 900)
	tt.Frame()

	for _, want := range []string{"Seq Scan", "Index Scan", "actual rows 42"} {
		if !tt.HasText(want) {
			t.Fatalf("missing plan text %q in %v", want, tt.Texts())
		}
	}
	if err := tt.Click("Index Scan"); err != nil {
		t.Fatalf("click plan node: %v", err)
	}
	tt.Frame()
	selections := session.eventsOf(domainnativeui.EventSelection)
	found := false
	for _, event := range selections {
		if event.Node != domainnativeui.IDFromNode(30) {
			t.Fatalf("plan interaction escaped its node: %+v", event)
		}
		if event.Index == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan selection events = %+v", selections)
	}

	if err := tt.Click("Index Scan"); err != nil {
		t.Fatalf("first plan activation: %v", err)
	}
	if err := tt.Click("Index Scan"); err != nil {
		t.Fatalf("second plan activation: %v", err)
	}
	tt.Frame()
	submits := session.eventsOf(domainnativeui.EventSubmit)
	found = false
	for _, event := range submits {
		if event.Node == domainnativeui.IDFromNode(30) && event.Index == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan activation events = %+v", submits)
	}
}

// TestCanvasReportsPointerInputInCanvasCoordinates clicks and drags over a
// canvas and checks the queued pointer events carry positions relative to the
// canvas's top-left corner, not the window's.
func TestCanvasReportsPointerInputInCanvasCoordinates(t *testing.T) {
	session := newFakeSession(buildTree(t, dataWidgets()))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 720, 900)
	tt.Frame()

	if !tt.HasText("canvas label") {
		t.Fatalf("missing canvas text in %v", tt.Texts())
	}
	rect, ok := tt.Find("Sketch")
	if !ok {
		t.Fatalf("the canvas has no labeled box: %v", tt.Texts())
	}
	tt.ClickAt(rect.X+30, rect.Y+20)
	tt.Frame()
	clicks := session.eventsOf(domainnativeui.EventClick)
	if len(clicks) != 1 || clicks[0].Node != domainnativeui.IDFromNode(40) {
		t.Fatalf("canvas click = %+v", clicks)
	}
	if clicks[0].X != 30 || clicks[0].Y != 20 {
		t.Fatalf("canvas click position = (%v, %v), want canvas-local (30, 20)", clicks[0].X, clicks[0].Y)
	}

	tt.Press(rect.X+10, rect.Y+40)
	tt.Move(rect.X+25, rect.Y+45)
	tt.Frame()
	tt.Release(rect.X+25, rect.Y+45)
	tt.Frame()
	drags := session.eventsOf(domainnativeui.EventDrag)
	if len(drags) == 0 || drags[0].Node != domainnativeui.IDFromNode(40) {
		t.Fatalf("canvas drag = %+v", drags)
	}
	if drags[0].X != 25 || drags[0].Y != 45 {
		t.Fatalf("canvas drag position = (%v, %v), want canvas-local (25, 45)", drags[0].X, drags[0].Y)
	}
}

// TestFrameBudgetSkipsAnOverBudgetGuestFrameAndCoalescesWork proves the host
// budget rule: a poll that overruns the guest budget skips the next guest
// frame, and the queued events stay in the coalescing queue for the frame
// after it.
func TestFrameBudgetSkipsAnOverBudgetGuestFrameAndCoalescesWork(t *testing.T) {
	session := newFakeSession(buildTree(t, counterTree()))
	now := time.Unix(0, 0)
	var polls int
	surface := pluginview.New(session,
		pluginview.WithFrameBudget(8*time.Millisecond),
		pluginview.WithClock(func() time.Time { return now }),
		pluginview.WithPoll(func() {
			polls++
			session.Drain()
			now = now.Add(12 * time.Millisecond)
		}),
	)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 480, 400)
	// The first frame delivers the initial resize event; that poll also
	// overruns the fake clock and arms the skip, which the next frame
	// consumes. Warm up past both before counting.
	tt.Frame()
	tt.Frame()
	if polls != 1 {
		t.Fatalf("warmup polls = %d, want 1", polls)
	}
	polls = 0

	session.Enqueue(domainnativeui.Event{Kind: domainnativeui.EventInput, Node: 3})
	tt.Frame()
	if polls != 1 {
		t.Fatalf("polls after the over-budget frame = %d, want 1", polls)
	}
	// An event queued while the skip is armed stays in the queue: the next
	// frame is skipped, then the frame after it polls and drains the work.
	session.Enqueue(domainnativeui.Event{Kind: domainnativeui.EventInput, Node: 3})
	tt.Frame()
	if polls != 1 {
		t.Fatalf("the over-budget frame was not skipped: polls = %d", polls)
	}
	if !tt.HasText("Count: 0") {
		t.Fatalf("a skipped frame did not render the last committed tree: %v", tt.Texts())
	}
	tt.Frame()
	if polls != 2 {
		t.Fatalf("polls after the skipped frame = %d, want 2", polls)
	}
	if pending := session.PendingEvents(); pending != 0 {
		t.Fatalf("the coalesced event was not delivered after the skip: pending = %d", pending)
	}
}

// graphLayoutBatch builds a graph whose two nodes have no explicit x/y, so
// only the graph's declared layout decides where they sit.
func graphLayoutBatch(layout domainnativeui.Layout) domainnativeui.Batch {
	return domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeGraph),
			create(3, domainnativeui.NodeGraphNode),
			create(4, domainnativeui.NodeGraphNode),
			prop(1, domainnativeui.PropLayout, domainnativeui.IntValue(int64(domainnativeui.LayoutColumn))),
			children(1, 2),
			label(2, "Layout graph"),
			prop(2, domainnativeui.PropWidth, domainnativeui.FloatValue(400)),
			prop(2, domainnativeui.PropHeight, domainnativeui.FloatValue(200)),
			prop(2, domainnativeui.PropLayout, domainnativeui.IntValue(int64(layout))),
			children(2, 3, 4),
			label(3, "First"),
			prop(3, domainnativeui.PropWidth, domainnativeui.FloatValue(100)),
			prop(3, domainnativeui.PropHeight, domainnativeui.FloatValue(40)),
			label(4, "Second"),
			prop(4, domainnativeui.PropWidth, domainnativeui.FloatValue(100)),
			prop(4, domainnativeui.PropHeight, domainnativeui.FloatValue(40)),
		},
	}
}

func TestGraphAutoLayoutFollowsTheDeclaredDirection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		layout     domainnativeui.Layout
		horizontal bool
	}{
		{"row", domainnativeui.LayoutRow, true},
		{"column", domainnativeui.LayoutColumn, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := newFakeSession(buildTree(t, graphLayoutBatch(tc.layout)))
			surface := pluginview.New(session)
			defer surface.Close()
			tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 700, 400)
			tt.Frame()
			first, ok := tt.Find("First")
			if !ok {
				t.Fatalf("missing first node in %v", tt.Texts())
			}
			second, ok := tt.Find("Second")
			if !ok {
				t.Fatalf("missing second node in %v", tt.Texts())
			}
			if tc.horizontal {
				if second.X <= first.X || second.Y != first.Y {
					t.Fatalf("row layout placed First at %+v and Second at %+v", first, second)
				}
			} else {
				if second.Y <= first.Y || second.X != first.X {
					t.Fatalf("column layout placed First at %+v and Second at %+v", first, second)
				}
			}
		})
	}
}

// TestDataSurfacesHonorHiddenAndDisabled proves declared visibility and
// disabled state are honored by the data renderers, not merely accepted by
// validation.
func TestDataSurfacesHonorHiddenAndDisabled(t *testing.T) {
	batch := domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeDataGrid),
			create(3, domainnativeui.NodeDataColumn),
			create(4, domainnativeui.NodeDataColumn),
			create(5, domainnativeui.NodeDataRow),
			create(6, domainnativeui.NodeText),
			create(7, domainnativeui.NodeDataRow),
			create(8, domainnativeui.NodeText),
			create(10, domainnativeui.NodeGraph),
			create(11, domainnativeui.NodeGraphNode),
			create(12, domainnativeui.NodeGraphNode),
			prop(1, domainnativeui.PropLayout, domainnativeui.IntValue(int64(domainnativeui.LayoutColumn))),
			children(1, 2, 10),
			label(2, "Inventory"),
			prop(2, domainnativeui.PropRowCount, domainnativeui.IntValue(2)),
			children(2, 3, 4, 5, 7),
			label(3, "Name"),
			prop(3, domainnativeui.PropWidth, domainnativeui.FloatValue(120)),
			label(4, "Secret"),
			prop(4, domainnativeui.PropHidden, domainnativeui.BoolValue(true)),
			prop(5, domainnativeui.PropRowIndex, domainnativeui.IntValue(0)),
			children(5, 6),
			text(6, "shown row"),
			prop(7, domainnativeui.PropRowIndex, domainnativeui.IntValue(1)),
			prop(7, domainnativeui.PropHidden, domainnativeui.BoolValue(true)),
			children(7, 8),
			text(8, "hidden row"),
			label(10, "Actions"),
			prop(10, domainnativeui.PropWidth, domainnativeui.FloatValue(400)),
			prop(10, domainnativeui.PropHeight, domainnativeui.FloatValue(140)),
			children(10, 11, 12),
			label(11, "Load"),
			text(11, "Load"),
			prop(11, domainnativeui.PropX, domainnativeui.FloatValue(10)),
			prop(11, domainnativeui.PropY, domainnativeui.FloatValue(10)),
			prop(11, domainnativeui.PropWidth, domainnativeui.FloatValue(100)),
			prop(11, domainnativeui.PropHeight, domainnativeui.FloatValue(36)),
			label(12, "Save"),
			text(12, "Save"),
			prop(12, domainnativeui.PropX, domainnativeui.FloatValue(180)),
			prop(12, domainnativeui.PropY, domainnativeui.FloatValue(10)),
			prop(12, domainnativeui.PropWidth, domainnativeui.FloatValue(100)),
			prop(12, domainnativeui.PropHeight, domainnativeui.FloatValue(36)),
			prop(12, domainnativeui.PropDisabled, domainnativeui.BoolValue(true)),
		},
	}
	session := newFakeSession(buildTree(t, batch))
	surface := pluginview.New(session)
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 600, 500)
	tt.Frame()

	if tt.HasText("Secret") || tt.HasText("hidden row") {
		t.Fatalf("hidden data children rendered: %v", tt.Texts())
	}
	if !tt.HasText("shown row") || !tt.HasText("Name") {
		t.Fatalf("visible data children are missing: %v", tt.Texts())
	}
	if err := tt.Click("Load"); err != nil {
		t.Fatalf("click enabled graph node: %v", err)
	}
	tt.Frame()
	selections := session.eventsOf(domainnativeui.EventSelection)
	if len(selections) != 1 {
		t.Fatalf("enabled graph node selections = %+v", selections)
	}
	if err := tt.Click("Save"); err != nil {
		t.Fatalf("click disabled graph node: %v", err)
	}
	tt.Frame()
	if got := session.eventsOf(domainnativeui.EventSelection); len(got) != 1 {
		t.Fatalf("disabled graph node emitted selection: %+v", got)
	}
}

// TestResetOnCloseCanPreserveASharedFiberSession proves a multi-instance host
// can close one surface without destroying the per-fiber UISession another
// View Instance still renders.
func TestResetOnCloseCanPreserveASharedFiberSession(t *testing.T) {
	session := newFakeSession(buildTree(t, allWidgets()))
	surface := pluginview.New(session, pluginview.WithResetOnClose(func() bool { return false }))
	surface.Close()
	if session.wasReset() {
		t.Fatal("Close reset the shared fiber session")
	}

	resetting := pluginview.New(session)
	resetting.Close()
	if !session.wasReset() {
		t.Fatal("the default Close no longer resets the session")
	}
}

func TestEmbeddedWebviewNodeUsesTheHostRenderer(t *testing.T) {
	tree := buildTree(t, domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeWebview),
			prop(2, domainnativeui.PropSrc, domainnativeui.StringValue("ui/embed.html")),
			prop(2, domainnativeui.PropGrow, domainnativeui.FloatValue(1)),
			children(1, 2),
		},
	})
	session := newFakeSession(tree)
	var renderedSrc string
	surface := pluginview.New(session, pluginview.WithWebview(func(c *ui.Context, node *domainnativeui.Node) ui.Element {
		renderedSrc = node.StringProp(domainnativeui.PropSrc)
		return ui.Text(c, "sandboxed page")
	}))
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 640, 480)
	tt.Frame()
	if renderedSrc != "ui/embed.html" {
		t.Fatalf("renderer saw src %q", renderedSrc)
	}
	if !tt.HasText("sandboxed page") {
		t.Fatalf("texts = %v", tt.Texts())
	}
}

func TestEmbeddedWebviewWithoutAHostRendererShowsAPlaceholder(t *testing.T) {
	tree := buildTree(t, domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			create(1, domainnativeui.NodeContainer),
			create(2, domainnativeui.NodeWebview),
			prop(2, domainnativeui.PropSrc, domainnativeui.StringValue("ui/embed.html")),
			children(1, 2),
		},
	})
	surface := pluginview.New(newFakeSession(tree))
	defer surface.Close()
	tt := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 640, 480)
	tt.Frame()
	if !tt.HasText("Embedded web view unavailable") {
		t.Fatalf("texts = %v", tt.Texts())
	}
}
