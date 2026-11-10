// Package pluginview renders one plugin View Instance with MyGo. It is the
// host half of the native UI ABI: the retained tree from the plugin's UI
// session is drawn with the core widget set, and user input comes back as
// events on that session. Rendering never calls the plugin; the app drains
// queued events later through the container's poll export.
//
// The renderer is immediate-mode over a retained model. Widget state the host
// owns (what is typed, chosen, or scrolled) is kept per node here; the plugin
// owns the tree and changes it with atomic patches, which are picked up on the
// next frame.
package pluginview

import (
	"strconv"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	"github.com/mslxl/natcatty/ui/locales"
)

// Session is the native UI surface the renderer draws and feeds. *appplugin.UISession
// implements it; tests use a recording fake.
type Session interface {
	Tree() *domainnativeui.Tree
	Closed() bool
	Reset()
	Enqueue(event domainnativeui.Event) bool
	EnqueueNode(node domainnativeui.NodeID, event domainnativeui.Event) bool
	SetFocus(node domainnativeui.NodeID) bool
	Focus() domainnativeui.NodeID
	FocusOrder() []domainnativeui.NodeID
	CapturesKeys() bool
	Key(mods domainnativeui.Modifiers, key domainnativeui.Key, down bool) (string, bool)
	Environment() appplugin.UIEnvironment
	SetEnvironment(env appplugin.UIEnvironment)
	PendingEvents() int
	SubscribeUpdates(fn func(*domainnativeui.Tree)) func()
}

var _ Session = (*appplugin.UISession)(nil)

// AssetResolver resolves the packaged asset id of an image node to a MyGo
// image source. The host owns packaged assets; the ABI never carries remote
// URLs. A missing resolver or an unknown id renders the node's alt text
// instead.
type AssetResolver func(src string) (ui.ImageSource, bool)

// FontResolver resolves the packaged asset id of a font property to a family
// name registered with MyGo. A missing resolver or an unknown id leaves the
// text in the host's default family.
type FontResolver func(src string) (family string, ok bool)

// Options customizes a Surface.
type Options struct {
	Environment func() appplugin.UIEnvironment
	Assets      AssetResolver
	Fonts       FontResolver
	OnClose     func()
	OnUpdate    func()
	RunCommand  func(string)
	// ResetOnClose decides whether Close resets the per-fiber UISession. Nil
	// keeps the original single-surface behavior (reset on close). A host with
	// several live instances of one plugin sets it to false while another
	// instance remains, so closing one View cannot tear down another's tree.
	ResetOnClose func() bool
	// Poll drives the guest poll export when events are waiting. The host
	// never calls the guest from an input callback: the renderer notices the
	// queued events at the start of the next frame.
	Poll func()
	// FrameBudget is the guest budget inside one frame. A poll that overruns
	// it makes the renderer skip the next guest frame, leaving queued events
	// coalescing in the bounded queue. Zero uses GuestFrameBudget.
	FrameBudget time.Duration
	// Clock measures the guest budget. It is time.Now unless a test supplies a
	// deterministic clock.
	Clock func() time.Time
	// Webview renders an embedded webview node. The app supplies a widget
	// backed by the sandboxed native page; a host without one shows a
	// placeholder rather than an empty hole.
	Webview func(c *ui.Context, node *domainnativeui.Node) ui.Element
}

// GuestFrameBudget is the per-frame guest budget from the native UI spec: a
// dispatch has 8 ms inside a 16 ms frame, and an over-budget frame is skipped
// rather than delaying the host further.
const GuestFrameBudget = 8 * time.Millisecond

// Option customizes a Surface.
type Option func(*Options)

// WithEnvironment sets the host environment the view follows (locale,
// direction, dark theme, reduced motion, and live host shortcuts). Without it
// the renderer derives locale, direction, theme, and reduced motion from the
// MyGo context.
func WithEnvironment(fn func() appplugin.UIEnvironment) Option {
	return func(o *Options) { o.Environment = fn }
}

// WithAssets sets the packaged-image resolver.
func WithAssets(resolve AssetResolver) Option {
	return func(o *Options) { o.Assets = resolve }
}

// WithFonts sets the packaged-font resolver.
func WithFonts(resolve FontResolver) Option {
	return func(o *Options) { o.Fonts = resolve }
}

// WithOnClose observes the surface being closed, after its handles are
// released.
func WithOnClose(fn func()) Option {
	return func(o *Options) { o.OnClose = fn }
}

// WithResetOnClose lets the host keep the per-fiber UISession alive while
// another View Instance of the same plugin still needs it.
func WithResetOnClose(fn func() bool) Option {
	return func(o *Options) { o.ResetOnClose = fn }
}

// WithUpdate observes every committed tree, so the app can invalidate the
// window when the plugin patches the view asynchronously.
func WithUpdate(fn func()) Option {
	return func(o *Options) { o.OnUpdate = fn }
}

// WithCommandRunner runs a host command that won a keyboard shortcut over the
// view's focused node.
func WithCommandRunner(fn func(string)) Option {
	return func(o *Options) { o.RunCommand = fn }
}

// WithPoll drives the guest poll export when events are queued. The callback
// runs at the start of a frame, never inside the input callback that queued
// the event, so the guest cannot re-enter the UI.
func WithPoll(fn func()) Option {
	return func(o *Options) { o.Poll = fn }
}

// WithFrameBudget sets the guest budget for one frame. A poll longer than
// budget skips the following guest frame; queued events coalesce meanwhile.
func WithFrameBudget(budget time.Duration) Option {
	return func(o *Options) { o.FrameBudget = budget }
}

// WithClock sets the clock the frame budget is measured with. Tests use it to
// make an over-budget poll deterministic.
func WithClock(now func() time.Time) Option {
	return func(o *Options) { o.Clock = now }
}

// WithWebview renders embedded webview nodes with the host's sandboxed page
// widget. The app owns the native page and its bridge; the renderer only gives
// it a place in the layout.
func WithWebview(fn func(c *ui.Context, node *domainnativeui.Node) ui.Element) Option {
	return func(o *Options) { o.Webview = fn }
}

// Surface renders one View Instance.
type Surface struct {
	session     Session
	options     Options
	closeOnce   sync.Once
	unsubscribe func()

	// UI-thread state.
	focused      bool
	restoreFocus domainnativeui.NodeID
	pendingFocus domainnativeui.NodeID
	observed     domainnativeui.NodeID
	// frameEnv is the environment of the frame being built. A focused node
	// that captures keys registers this frame's host shortcut chords on
	// itself, so MyGo routes them to the node before the window accelerator.
	frameEnv appplugin.UIEnvironment

	lastWidth, lastHeight float32
	renderedOnce          bool

	texts    map[domainnativeui.NodeID]*string
	bools    map[domainnativeui.NodeID]*bool
	numbers  map[domainnativeui.NodeID]*float64
	choices  map[domainnativeui.NodeID]*int
	seen     map[domainnativeui.NodeID]string
	lists    map[domainnativeui.NodeID]*ui.ListState
	grids    map[domainnativeui.NodeID]*gridState
	virtuals map[domainnativeui.NodeID]*virtualState
	hover    map[domainnativeui.NodeID]bool

	// richSelections fingerprints each rich text's selected parts so a
	// changed selection queues one event. animations tracks the host-driven
	// transition target and whether the previous frame moved the node.
	richSelections map[domainnativeui.NodeID]string
	animations     map[domainnativeui.NodeID]*animationState

	// frameBudget and now measure one guest poll. An over-budget poll skips
	// the next guest frame.
	frameBudget time.Duration
	now         func() time.Time
	skipFrame   bool

	// scrollFrame counts built frames. MyGo 0.7.4 offers a scroll to every
	// handler in the hit chain until one takes it, so an ancestor would
	// report the same innermost gesture again; one frame keeps the first
	// observation of a gesture and its node.
	scrollFrame uint64
	scrollSeen  scrollStamp
}

// scrollStamp identifies the last scroll observation of a frame: the
// innermost handler runs first, and the handlers around it see the same
// deltas.
type scrollStamp struct {
	frame    uint64
	dx, dy   float32
	precise  bool
	momentum bool
	valid    bool
}

// New returns a surface over session. The session's commits are observed for
// redraws through WithUpdate.
func New(session Session, optionList ...Option) *Surface {
	options := Options{}
	for _, option := range optionList {
		option(&options)
	}
	surface := &Surface{
		session:        session,
		options:        options,
		texts:          make(map[domainnativeui.NodeID]*string),
		bools:          make(map[domainnativeui.NodeID]*bool),
		numbers:        make(map[domainnativeui.NodeID]*float64),
		choices:        make(map[domainnativeui.NodeID]*int),
		seen:           make(map[domainnativeui.NodeID]string),
		lists:          make(map[domainnativeui.NodeID]*ui.ListState),
		grids:          make(map[domainnativeui.NodeID]*gridState),
		virtuals:       make(map[domainnativeui.NodeID]*virtualState),
		hover:          make(map[domainnativeui.NodeID]bool),
		richSelections: make(map[domainnativeui.NodeID]string),
		animations:     make(map[domainnativeui.NodeID]*animationState),
	}
	surface.frameBudget = options.FrameBudget
	if surface.frameBudget == 0 {
		surface.frameBudget = GuestFrameBudget
	}
	surface.now = options.Clock
	if surface.now == nil {
		surface.now = time.Now
	}
	if session != nil {
		surface.unsubscribe = session.SubscribeUpdates(func(*domainnativeui.Tree) {
			if options.OnUpdate != nil {
				options.OnUpdate()
			}
		})
	}
	return surface
}

// View draws the view without taking the keyboard, as a Pane that is not
// focused does.
func (s *Surface) View(c *ui.Context) ui.Element {
	return s.view(c, false)
}

// ViewFocused draws the view and, when focused, lets the host focus and
// keyboard routing reach the plugin. Blur queues blur for the focused node;
// regaining focus restores it, so switching Tabs keeps the view's state.
func (s *Surface) ViewFocused(c *ui.Context, focused bool) ui.Element {
	return s.view(c, focused)
}

// Close releases the view's node and listener handles and stops observing
// commits. It is idempotent.
func (s *Surface) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		if s.unsubscribe != nil {
			s.unsubscribe()
			s.unsubscribe = nil
		}
		if s.session != nil && (s.options.ResetOnClose == nil || s.options.ResetOnClose()) {
			s.session.Reset()
		}
		if s.options.OnClose != nil {
			s.options.OnClose()
		}
	})
}

func (s *Surface) view(c *ui.Context, focused bool) ui.Element {
	s.scrollFrame++
	s.followFocus(focused)
	if s.skipFrame {
		// The previous guest poll overran its budget. Skip this frame's guest
		// work; queued events stay coalesced for the next one.
		s.skipFrame = false
	} else {
		s.pollPending()
	}
	env := s.followEnvironment(c)
	s.frameEnv = env
	s.followViewport(c)

	root := ui.Column(c).Fill()
	if env.Direction == appplugin.UIDirectionRTL {
		root = root.Direction(ui.RightToLeft)
	} else {
		root = root.Direction(ui.LeftToRight)
	}
	tree := s.session.Tree()
	s.shortcuts(c, env)
	s.observed = 0
	root = root.Children(func() {
		if tree == nil || tree.Root == 0 {
			ui.Text(c, "").Invisible()
			return
		}
		s.renderNode(c, tree, tree.Root)
	})
	s.applyObservedFocus()
	return root
}

// pollPending gives the guest a chance to drain queued events and commit the
// patches they produce. It runs before the tree snapshot is read, so a patch
// committed by this poll is drawn in the same frame. The guest call never runs
// inside the input callback that queued the event.
func (s *Surface) pollPending() {
	if s.options.Poll == nil || s.session == nil {
		return
	}
	if s.session.PendingEvents() == 0 {
		return
	}
	start := s.now()
	s.options.Poll()
	if s.frameBudget > 0 && s.now().Sub(start) > s.frameBudget {
		s.skipFrame = true
	}
}

// followFocus synchronizes the host-owned focus with the Pane's keyboard
// focus. A pane that loses focus blurs the node and remembers it; the pane
// that gains focus restores it.
func (s *Surface) followFocus(focused bool) {
	if s.focused == focused {
		return
	}
	s.focused = focused
	if !focused {
		if focus := s.session.Focus(); focus != 0 {
			s.restoreFocus = focus
			s.session.SetFocus(0)
		}
		return
	}
	target := s.restoreFocus
	if target == 0 {
		if order := s.session.FocusOrder(); len(order) > 0 {
			target = order[0]
		}
	}
	if target != 0 && s.session.SetFocus(target) {
		s.pendingFocus = target
	}
}

// followEnvironment hands the host's locale, direction, theme, reduced motion,
// and shortcuts to the session. A locale or theme change queues the matching
// session-wide event.
func (s *Surface) followEnvironment(c *ui.Context) appplugin.UIEnvironment {
	env := s.session.Environment()
	if s.options.Environment != nil {
		env = s.options.Environment()
	} else {
		env.Locale = c.Locale()
		if c.Direction() == ui.RightToLeft {
			env.Direction = appplugin.UIDirectionRTL
		} else {
			env.Direction = appplugin.UIDirectionLTR
		}
		env.Dark = c.Theme().Dark
		env.ReduceMotion = c.Preferences().ReduceMotion
	}
	s.session.SetEnvironment(env)
	return env
}

// followViewport reports a changed view size as the session-wide resize event.
func (s *Surface) followViewport(c *ui.Context) {
	width, height := c.Size()
	if s.renderedOnce && width == s.lastWidth && height == s.lastHeight {
		return
	}
	s.renderedOnce = true
	s.lastWidth, s.lastHeight = width, height
	s.session.Enqueue(domainnativeui.Event{
		Kind: domainnativeui.EventResize, Width: width, Height: height,
	})
}

// applyObservedFocus moves host focus to the node MyGo focused, as a click or
// an arrow key inside a widget does. Host traversal moves MyGo focus instead.
func (s *Surface) applyObservedFocus() {
	if s.observed == 0 || s.observed == s.session.Focus() || !s.focused {
		return
	}
	if s.session.SetFocus(s.observed) {
		s.pendingFocus = 0
	}
}

func (s *Surface) renderNode(c *ui.Context, tree *domainnativeui.Tree, id domainnativeui.NodeID) ui.Element {
	node, ok := tree.Nodes[id]
	if !ok || node == nil || node.BoolProp(domainnativeui.PropHidden, false) {
		return ui.Element{}
	}
	nc := c.Key(nodeKey{id: id})
	var element ui.Element
	switch node.Kind {
	case domainnativeui.NodeContainer:
		element = s.container(nc, tree, node)
	case domainnativeui.NodeText:
		element = s.textNode(nc, node)
	case domainnativeui.NodeButton:
		element = s.button(nc, tree, node)
	case domainnativeui.NodeInput:
		element = s.input(nc, node)
	case domainnativeui.NodeCheckbox:
		element = s.checkbox(nc, node, false)
	case domainnativeui.NodeRadio:
		element = s.checkbox(nc, node, true)
	case domainnativeui.NodeSelect:
		element = s.selectNode(nc, tree, node)
	case domainnativeui.NodeSlider:
		element = s.slider(nc, node)
	case domainnativeui.NodeProgress:
		element = s.progress(nc, node)
	case domainnativeui.NodeBadge:
		element = ui.Badge(nc, node.Text)
	case domainnativeui.NodeDivider:
		element = ui.Divider(nc)
	case domainnativeui.NodeImage:
		element = s.image(nc, tree, node)
	case domainnativeui.NodeList:
		element = s.list(nc, tree, node)
	case domainnativeui.NodeTable:
		element = s.table(nc, tree, node)
	case domainnativeui.NodeTree:
		element = s.tree(nc, tree, node)
	case domainnativeui.NodeTabs:
		element = s.tabs(nc, tree, node)
	case domainnativeui.NodeTooltip:
		element = s.tooltip(nc, tree, node)
	case domainnativeui.NodeDialog:
		element = s.dialog(nc, tree, node)
	case domainnativeui.NodeMenu:
		element = s.menu(nc, tree, node)
	case domainnativeui.NodeToast:
		element = s.toast(nc, node)
	case domainnativeui.NodeDataGrid:
		element = s.dataGrid(nc, tree, node)
	case domainnativeui.NodeGraph:
		element = s.graph(nc, tree, node)
	case domainnativeui.NodePlanGraph:
		element = s.planGraph(nc, tree, node)
	case domainnativeui.NodeCanvas:
		element = s.canvas(nc, tree, node)
	case domainnativeui.NodeRichText:
		element = s.richText(nc, tree, node)
	case domainnativeui.NodeVirtualList:
		element = s.virtualList(nc, tree, node)
	case domainnativeui.NodeVirtualItem:
		element = s.virtualItem(nc, tree, node)
	case domainnativeui.NodeWebview:
		if s.options.Webview != nil {
			element = s.options.Webview(nc, node)
		} else {
			element = ui.Text(nc, locales.In(nc.Locale()).PluginWebviewEmbeddedUnavailable())
		}
	case domainnativeui.NodeDataColumn, domainnativeui.NodeDataRow,
		domainnativeui.NodeGraphNode, domainnativeui.NodeGraphEdge,
		domainnativeui.NodePlanNode, domainnativeui.NodeCanvasShape,
		domainnativeui.NodeTextSpan:
		// These kinds are rendered by their parent and never stand alone.
		element = ui.Text(nc, node.Text)
	default:
		element = ui.Text(nc, "")
	}
	element = s.decorate(nc, tree, node, element)
	element = s.applyTransition(node, element)
	element = s.bindFocus(nc, node, element)
	element = s.bindInput(nc, node, element)
	element = s.bindKeyCapture(node, element)
	return element
}

func (s *Surface) container(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	var element ui.Element
	switch domainnativeui.Layout(node.IntProp(domainnativeui.PropLayout, int64(domainnativeui.LayoutColumn))) {
	case domainnativeui.LayoutRow:
		element = ui.Row(c)
	case domainnativeui.LayoutBox:
		element = ui.Box(c)
	case domainnativeui.LayoutGrid:
		element = ui.Grid(c).Columns(min(3, max(1, len(node.Children))))
	default:
		element = ui.Column(c)
	}
	if gap := node.FloatProp(domainnativeui.PropGap, 0); gap > 0 {
		element = element.Gap(float32(gap))
	}
	children := append([]domainnativeui.NodeID(nil), node.Children...)
	element = element.Children(func() {
		for _, child := range children {
			s.renderNode(c, tree, child)
		}
	})
	return element
}

func (s *Surface) textNode(c *ui.Context, node *domainnativeui.Node) ui.Element {
	element := ui.Text(c, node.Text)
	if size := node.FloatProp(domainnativeui.PropFontSize, 0); size > 0 {
		element = element.FontSize(float32(size))
	}
	switch node.IntProp(domainnativeui.PropFontWeight, 0) {
	case 1:
		element = element.Bold()
	}
	return element
}

func (s *Surface) button(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	element := ui.Button(c, accessibleLabel(tree, node))
	if node.BoolProp(domainnativeui.PropDisabled, false) {
		element = element.Disabled(true)
	}
	if element.Clicked() {
		s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventClick})
	}
	if element.DoubleClicked() {
		s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventDoubleClick})
	}
	if element.RightClicked() {
		s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventContextMenu})
	}
	return element
}

func (s *Surface) input(c *ui.Context, node *domainnativeui.Node) ui.Element {
	valuePtr := s.syncedText(node, node.StringProp(domainnativeui.PropValue))
	var element ui.Element
	if node.BoolProp(domainnativeui.PropMultiline, false) {
		element = ui.TextArea(c, valuePtr)
	} else {
		element = ui.TextInput(c, valuePtr)
	}
	if placeholder := node.StringProp(domainnativeui.PropPlaceholder); placeholder != "" {
		element = element.Placeholder(placeholder)
	}
	if node.BoolProp(domainnativeui.PropReadOnly, false) {
		element = element.ReadOnly(true)
	}
	if node.BoolProp(domainnativeui.PropDisabled, false) {
		element = element.Disabled(true)
	}
	if element.Changed() {
		s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventInput, Text: *valuePtr})
	}
	if element.Submitted() {
		s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventSubmit, Text: *valuePtr})
	}
	return element
}

func (s *Surface) checkbox(c *ui.Context, node *domainnativeui.Node, radio bool) ui.Element {
	checkedPtr := s.syncedBool(node, node.BoolProp(domainnativeui.PropChecked, false))
	label := node.StringProp(domainnativeui.PropLabel)
	var element ui.Element
	if radio {
		element = ui.Radio(c, checkedPtr, true, label)
	} else {
		element = ui.Checkbox(c, checkedPtr, label)
	}
	if node.BoolProp(domainnativeui.PropDisabled, false) {
		element = element.Disabled(true)
	}
	if element.Changed() {
		index := int32(0)
		if *checkedPtr {
			index = 1
		}
		s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventChange, Index: index})
	}
	return element
}

func (s *Surface) selectNode(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	options := make([]string, 0, len(node.Children))
	for _, child := range node.Children {
		if option, ok := tree.Nodes[child]; ok && option != nil {
			options = append(options, option.Text)
		}
	}
	valuePtr := s.syncedText(node, node.StringProp(domainnativeui.PropValue))
	if len(options) == 0 {
		return ui.Text(c, *valuePtr)
	}
	element := ui.Select(c, valuePtr, options)
	if node.BoolProp(domainnativeui.PropDisabled, false) {
		element = element.Disabled(true)
	}
	if element.Changed() {
		s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventChange, Text: *valuePtr})
	}
	return element
}

func (s *Surface) slider(c *ui.Context, node *domainnativeui.Node) ui.Element {
	lo := node.FloatProp(domainnativeui.PropMin, 0)
	hi := node.FloatProp(domainnativeui.PropMax, 100)
	if hi <= lo {
		hi = lo + 1
	}
	valuePtr := s.syncedNumber(node, parseFloat(node.StringProp(domainnativeui.PropValue), lo))
	element := ui.Slider(c, valuePtr, lo, hi)
	if node.BoolProp(domainnativeui.PropDisabled, false) {
		element = element.Disabled(true)
	}
	if element.Changed() {
		s.emit(node.ID, domainnativeui.Event{
			Kind: domainnativeui.EventChange,
			Text: strconv.FormatFloat(*valuePtr, 'g', -1, 64),
		})
	}
	return element
}

func (s *Surface) progress(c *ui.Context, node *domainnativeui.Node) ui.Element {
	lo := node.FloatProp(domainnativeui.PropMin, 0)
	hi := node.FloatProp(domainnativeui.PropMax, 100)
	value := parseFloat(node.StringProp(domainnativeui.PropValue), lo)
	fraction := 0.0
	if hi > lo {
		fraction = (value - lo) / (hi - lo)
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	return ui.Progress(c, fraction)
}

func (s *Surface) image(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	alt := node.StringProp(domainnativeui.PropAlt)
	if alt == "" {
		alt = accessibleLabel(tree, node)
	}
	if s.options.Assets != nil {
		if source, ok := s.options.Assets(node.StringProp(domainnativeui.PropSrc)); ok {
			return ui.Image(c, source)
		}
	}
	// A packaged asset this host cannot resolve shows its alt text rather than
	// a broken image. Images never load from a URL, and an image with no alt
	// is decorative, so its placeholder shows no text.
	return ui.Row(c).Height(48).AlignItems(ui.Center).Justify(ui.Center).
		Background(c.Theme().Surface).Radius(c.Theme().Radius).Children(func() {
		if alt != "" {
			ui.Text(c, alt).FontSize(11).TextColor(c.Theme().TextMuted)
		}
	})
}

func (s *Surface) list(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	state := s.listState(node)
	s.syncListChoice(node, state)
	previous := *state.Selected
	rows := len(node.Children)
	children := append([]domainnativeui.NodeID(nil), node.Children...)
	disabled := node.BoolProp(domainnativeui.PropDisabled, false)
	element := ui.List(c, state, rows, func(row int) {
		rowElement := s.renderNode(c, tree, children[row])
		if disabled {
			return
		}
		rowElement = rowElement.Drag(listDrag{Node: node.ID, Row: row})
		if rowElement.Clicked() {
			s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventClick, Index: int32(row)})
			// The row content is this row's drag source, so it takes the
			// pointer press ahead of the list's row box and the list itself
			// never sees the click. Choose the row here, as the list does
			// for a row that is not a drag source.
			if state.Selected != nil {
				*state.Selected = row
			}
		}
		if rowElement.DoubleClicked() {
			s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventDoubleClick, Index: int32(row)})
		}
		if rowElement.RightClicked() {
			s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventContextMenu, Index: int32(row)})
		}
		if rowElement.Hovered() {
			s.emitHover(node.ID)
		}
		if _, _, ok := rowElement.Dragged(); ok {
			s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventDrag, Index: int32(row)})
		}
	})
	if disabled {
		element = element.Disabled(true)
	}
	if element.Changed() || *state.Selected != previous {
		s.emit(node.ID, domainnativeui.Event{
			Kind: domainnativeui.EventSelection, Index: int32(*state.Selected),
		})
	}
	if source, ok := ui.Drop[listDrag](element); ok {
		s.emit(node.ID, domainnativeui.Event{
			Kind: domainnativeui.EventDrop, Index: int32(source.Row), Related: domainnativeui.IDFromNode(source.Node),
		})
	}
	if element.Hovered() {
		s.emitHover(node.ID)
	}
	return s.bindScroll(node, element)
}

func (s *Surface) table(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	state := s.listState(node)
	s.syncListChoice(node, state)
	columns := int(node.IntProp(domainnativeui.PropColumns, 1))
	if columns < 1 {
		columns = 1
	}
	headers := make([]ui.TableColumn, columns)
	for i := range headers {
		headers[i] = ui.TableColumn{Title: strconv.Itoa(i + 1), Fixed: true}
	}
	rows := len(node.Children)
	children := append([]domainnativeui.NodeID(nil), node.Children...)
	element := ui.Table(c, state, headers, rows, func(row, column int) {
		rowNode, ok := tree.Nodes[children[row]]
		if !ok || rowNode == nil || column >= len(rowNode.Children) {
			return
		}
		s.renderNode(c, tree, rowNode.Children[column])
	})
	if node.BoolProp(domainnativeui.PropDisabled, false) {
		element = element.Disabled(true)
	}
	if element.Changed() {
		s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventSelection, Index: int32(*state.Selected)})
	}
	if element.Submitted() {
		s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventSubmit, Index: int32(*state.Selected)})
	}
	return element
}

func (s *Surface) tree(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	children := append([]domainnativeui.NodeID(nil), node.Children...)
	disabled := node.BoolProp(domainnativeui.PropDisabled, false)
	element := ui.Tree(c, func() {
		for i, child := range children {
			s.treeItem(c, tree, node, child, i, disabled)
		}
	})
	if disabled {
		element = element.Disabled(true)
	}
	return element
}

func (s *Surface) treeItem(c *ui.Context, tree *domainnativeui.Tree,
	parent *domainnativeui.Node, id domainnativeui.NodeID, index int, disabled bool) {
	item, ok := tree.Nodes[id]
	if !ok || item == nil || item.BoolProp(domainnativeui.PropHidden, false) {
		return
	}
	openPtr := s.syncedBool(item, item.BoolProp(domainnativeui.PropExpanded, false))
	wasOpen := *openPtr
	label := item.Text
	if label == "" {
		label = item.StringProp(domainnativeui.PropLabel)
	}
	children := append([]domainnativeui.NodeID(nil), item.Children...)
	element := ui.TreeItem(c.Key(nodeKey{id: item.ID}), label, openPtr, func() {
		for _, child := range children {
			s.treeItem(c, tree, item, child, index, disabled)
		}
	})
	if !disabled {
		if element.Clicked() {
			s.emit(parent.ID, domainnativeui.Event{Kind: domainnativeui.EventSelection, Index: int32(index)})
		}
		if element.DoubleClicked() {
			s.emit(parent.ID, domainnativeui.Event{Kind: domainnativeui.EventDoubleClick, Index: int32(index)})
		}
		if element.RightClicked() {
			s.emit(parent.ID, domainnativeui.Event{Kind: domainnativeui.EventContextMenu, Index: int32(index)})
		}
		if *openPtr != wasOpen {
			s.emit(item.ID, domainnativeui.Event{Kind: domainnativeui.EventChange, Index: boolIndex(*openPtr)})
		}
	}
}

func (s *Surface) tabs(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	labels := make([]string, 0, len(node.Children))
	for _, child := range node.Children {
		if label, ok := tree.Nodes[child]; ok && label != nil {
			labels = append(labels, label.Text)
		}
	}
	if len(labels) == 0 {
		labels = []string{node.StringProp(domainnativeui.PropLabel)}
	}
	selectedPtr := s.syncedChoice(node, int(node.IntProp(domainnativeui.PropSelected, 0)))
	if *selectedPtr >= len(labels) {
		*selectedPtr = len(labels) - 1
	}
	if *selectedPtr < 0 {
		*selectedPtr = 0
	}
	element := ui.Tabs(c, selectedPtr, labels...)
	if node.BoolProp(domainnativeui.PropDisabled, false) {
		element = element.Disabled(true)
	}
	if element.Changed() {
		s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventChange, Index: int32(*selectedPtr)})
	}
	return element
}

func (s *Surface) tooltip(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	text := node.Text
	if text == "" {
		text = node.StringProp(domainnativeui.PropLabel)
	}
	if len(node.Children) == 0 {
		element := ui.Text(c, text)
		element = element.Tooltip(text)
		return element
	}
	anchor := s.renderNode(c, tree, node.Children[0])
	return anchor.Tooltip(text)
}

func (s *Surface) dialog(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	children := append([]domainnativeui.NodeID(nil), node.Children...)
	return ui.Column(c).Padding(16).Gap(10).Radius(c.Theme().Radius).
		Background(c.Theme().Surface).BorderWidth(1).BorderColor(c.Theme().Border).
		Children(func() {
			if node.Text != "" {
				ui.Text(c, node.Text).Bold().FontSize(14)
			}
			for _, child := range children {
				s.renderNode(c, tree, child)
			}
		})
}

func (s *Surface) menu(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	children := append([]domainnativeui.NodeID(nil), node.Children...)
	element := ui.MenuButton(c, accessibleLabel(tree, node), func(menu *ui.Menu) {
		for index, child := range children {
			item, ok := tree.Nodes[child]
			if !ok || item == nil {
				continue
			}
			itemLabel := item.Text
			if itemLabel == "" {
				itemLabel = item.StringProp(domainnativeui.PropLabel)
			}
			index := index
			if menu.Item(itemLabel).Chosen() {
				s.emit(node.ID, domainnativeui.Event{
					Kind: domainnativeui.EventClick, Index: int32(index), Text: itemLabel,
				})
			}
		}
	})
	if node.BoolProp(domainnativeui.PropDisabled, false) {
		element = element.Disabled(true)
	}
	return element
}

func (s *Surface) toast(c *ui.Context, node *domainnativeui.Node) ui.Element {
	text := node.Text
	if text == "" {
		text = node.StringProp(domainnativeui.PropLabel)
	}
	return ui.Row(c).Padding(8, 12).Gap(8).Radius(c.Theme().Radius).
		Background(c.Theme().Inverse).AlignItems(ui.Center).Children(func() {
		ui.Text(c, text).FontSize(12).TextColor(c.Theme().InverseText)
	})
}

// decorate applies the generic layout, visual, and accessibility properties
// shared by every kind. Logical start/end edges mirror under RTL in MyGo.
func (s *Surface) decorate(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node, element ui.Element) ui.Element {
	if value := node.FloatProp(domainnativeui.PropWidth, 0); value > 0 {
		element = element.Width(float32(value))
	}
	if value := node.FloatProp(domainnativeui.PropHeight, 0); value > 0 {
		element = element.Height(float32(value))
	}
	if value := node.FloatProp(domainnativeui.PropGrow, 0); value > 0 {
		element = element.Grow(float32(value))
	}
	// Physical top/bottom are set before the logical start/end edges, because
	// the shorthand clears them and the logical edges must win under RTL.
	top := float32(node.FloatProp(domainnativeui.PropPaddingTop, 0))
	bottom := float32(node.FloatProp(domainnativeui.PropPaddingBottom, 0))
	if top > 0 || bottom > 0 {
		element = element.Padding(top, 0, bottom, 0)
	}
	element = element.PaddingStart(float32(node.FloatProp(domainnativeui.PropPaddingStart, 0)))
	element = element.PaddingEnd(float32(node.FloatProp(domainnativeui.PropPaddingEnd, 0)))
	marginTop := float32(node.FloatProp(domainnativeui.PropMarginTop, 0))
	marginBottom := float32(node.FloatProp(domainnativeui.PropMarginBottom, 0))
	if marginTop > 0 || marginBottom > 0 {
		element = element.Margin(marginTop, 0, marginBottom, 0)
	}
	element = element.MarginStart(float32(node.FloatProp(domainnativeui.PropMarginStart, 0)))
	element = element.MarginEnd(float32(node.FloatProp(domainnativeui.PropMarginEnd, 0)))
	if token, ok := node.Prop(domainnativeui.PropBackgroundToken); ok {
		element = element.Background(tokenColor(c, domainnativeui.Token(token.Int)))
	}
	if token, ok := node.Prop(domainnativeui.PropBorderToken); ok {
		element = element.BorderWidth(1).BorderColor(tokenColor(c, domainnativeui.Token(token.Int)))
	}
	if value := node.FloatProp(domainnativeui.PropRadius, 0); value > 0 {
		element = element.Radius(float32(value))
	}
	if value := node.FloatProp(domainnativeui.PropOpacity, 1); value >= 0 && value < 1 {
		element = element.Opacity(float32(value))
	}
	if token, ok := node.Prop(domainnativeui.PropTextToken); ok {
		element = element.TextColor(tokenColor(c, domainnativeui.Token(token.Int)))
	}
	if tooltip := node.StringProp(domainnativeui.PropTooltip); tooltip != "" {
		element = element.Tooltip(tooltip)
	}
	if asset := node.StringProp(domainnativeui.PropFontAsset); asset != "" {
		if family, ok := s.fontFamily(asset); ok {
			element = element.Font(family)
		}
	}
	if label := accessibleLabel(tree, node); label != "" {
		element = element.Label(label)
	}
	if role := mygoRole(domainnativeui.Role(node.IntProp(domainnativeui.PropRole, 0))); role != ui.RoleAuto {
		element = element.Role(role)
	}
	return element
}

// accessibleLabel returns the node's own label, or the text of the node named
// by labelled_by, so the visible and assistive names agree. The host resolves
// every PropRef to a guest node id when it applies the batch, so the lookup is
// always by id here.
func accessibleLabel(tree *domainnativeui.Tree, node *domainnativeui.Node) string {
	if label := node.StringProp(domainnativeui.PropLabel); label != "" {
		return label
	}
	value, ok := node.Prop(domainnativeui.PropLabelledBy)
	if !ok || value.Kind != domainnativeui.PropRef {
		return ""
	}
	id, ok := value.Ref.NodeID()
	if !ok {
		return ""
	}
	target, ok := tree.Nodes[id]
	if !ok || target == nil {
		return ""
	}
	if target.Text != "" {
		return target.Text
	}
	return target.StringProp(domainnativeui.PropLabel)
}

// bindFocus follows the host's focus for one focusable node: a pending host
// focus moves MyGo focus, and MyGo focusing the node (a click or arrow key)
// is reported back as host focus.
func (s *Surface) bindFocus(c *ui.Context, node *domainnativeui.Node, element ui.Element) ui.Element {
	if !domainnativeui.Focusable(s.session.Tree(), node.ID) {
		return element
	}
	element = element.Focusable()
	if s.pendingFocus == node.ID {
		element = element.Focus()
		s.pendingFocus = 0
	}
	if element.Focused() {
		s.observed = node.ID
	}
	return element
}

// bindInput routes keys, IME text, scroll, and hover over a node. Host
// shortcuts are registered as MyGo shortcuts in view() and consume the key
// before it reaches here; a node that captures keys keeps them.
func (s *Surface) bindInput(c *ui.Context, node *domainnativeui.Node, element ui.Element) ui.Element {
	element = element.HandleInput(func(ev ui.InputEvent) bool {
		switch ev.Kind {
		case ui.InputKeyDown, ui.InputKeyUp:
			if key, ok := mygoKeyToNative(ev.Key); ok {
				if ev.Kind == ui.InputKeyDown && ev.Key == ui.KeyTab &&
					(ev.Mods == 0 || ev.Mods == ui.Shift) && !s.session.CapturesKeys() {
					s.moveFocus(ev.Mods == ui.Shift)
					return true
				}
				down := ev.Kind == ui.InputKeyDown
				// The host shortcut already ran through the MyGo window
				// shortcut registered in shortcuts(); consume the key so
				// the plugin does not also see it. Never run the command
				// here, or one press would run it twice.
				command, _ := s.session.Key(nativeMods(ev.Mods), key, down)
				if command != "" {
					return true
				}
			}
		case ui.InputScroll:
			var x, y float32
			if node.Kind == domainnativeui.NodeCanvas {
				if px, py, over := element.PointerPosition(); over {
					x, y = px, py
				}
			}
			s.emitScroll(node.ID, ev, x, y)
		}
		return false
	})
	element = element.ContextMenu(func(*ui.Menu) {
		s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventContextMenu})
	})
	if element.Hovered() {
		s.emitHover(node.ID)
	} else {
		delete(s.hover, node.ID)
	}
	return element
}

// bindKeyCapture makes a focused node that declares key capture win the host
// shortcut chords. MyGo claims a key for the innermost element around the
// focus before the window's shortcuts, so registering the same chords on the
// node routes them there rather than to the app's window accelerator. The
// handler delivers the press to the guest as key_down; the release arrives
// through bindInput, whose routing rule lets a capturing node keep it.
//
// Without this, a window accelerator would claim the chord in MyGo's
// keyDown before HandleInput saw it, and the capturing node could never
// receive the key at all.
func (s *Surface) bindKeyCapture(node *domainnativeui.Node, element ui.Element) ui.Element {
	if !element.Focused() || !node.BoolProp(domainnativeui.PropKeyCapture, false) {
		return element
	}
	for _, shortcut := range s.frameEnv.Shortcuts {
		key, ok := nativeKeyToMygo(shortcut.Key)
		if !ok {
			continue
		}
		native := shortcut
		element = element.OnShortcut(mygoMods(shortcut.Mods), key, func() {
			s.session.Key(native.Mods, native.Key, true)
		})
	}
	return element
}

// bindScroll reports wheel movement over a scrollable widget without taking
// the scroll itself, so the widget keeps scrolling.
func (s *Surface) bindScroll(node *domainnativeui.Node, element ui.Element) ui.Element {
	return element.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputScroll {
			s.emitScroll(node.ID, ev, 0, 0)
		}
		return false
	})
}

// emitScroll reports one wheel gesture once per frame. MyGo 0.7.4 hands a
// scroll to every handler in the hit chain until one takes it; these handlers
// only observe it (so the widget keeps scrolling), which would otherwise
// report the same gesture once per ancestor with the same node or a container.
// The first, innermost handler reports it, and the identical deliveries that
// follow in the same frame are the same gesture.
func (s *Surface) emitScroll(nodeID domainnativeui.NodeID, ev ui.InputEvent, x, y float32) {
	if s.scrollSeen.valid && s.scrollSeen.frame == s.scrollFrame &&
		s.scrollSeen.dx == ev.DX && s.scrollSeen.dy == ev.DY &&
		s.scrollSeen.precise == ev.Precise && s.scrollSeen.momentum == ev.Momentum {
		return
	}
	s.scrollSeen = scrollStamp{
		frame: s.scrollFrame, dx: ev.DX, dy: ev.DY,
		precise: ev.Precise, momentum: ev.Momentum, valid: true,
	}
	s.emit(nodeID, domainnativeui.Event{
		Kind: domainnativeui.EventScroll, DX: ev.DX, DY: ev.DY, X: x, Y: y,
	})
}

// moveFocus advances host focus by the tab traversal order.
func (s *Surface) moveFocus(backward bool) {
	order := s.session.FocusOrder()
	if len(order) == 0 {
		return
	}
	index := -1
	current := s.session.Focus()
	for i, id := range order {
		if id == current {
			index = i
			break
		}
	}
	next := 0
	if backward {
		if index <= 0 {
			next = len(order) - 1
		} else {
			next = index - 1
		}
	} else if index >= 0 {
		next = (index + 1) % len(order)
	}
	if s.session.SetFocus(order[next]) {
		s.pendingFocus = order[next]
	}
}

func (s *Surface) runCommand(command string) {
	if s.options.RunCommand != nil {
		s.options.RunCommand(command)
	}
}

func (s *Surface) emit(node domainnativeui.NodeID, event domainnativeui.Event) {
	s.session.EnqueueNode(node, event)
}

func (s *Surface) emitHover(node domainnativeui.NodeID) {
	if s.hover[node] {
		return
	}
	s.hover[node] = true
	s.emit(node, domainnativeui.Event{Kind: domainnativeui.EventHover})
}

// syncedText returns the stable host-side value of a text widget, refreshed
// when the plugin patches the tree value. The local value survives frames in
// which the plugin has not answered yet, so typing never stutters.
func (s *Surface) syncedText(node *domainnativeui.Node, value string) *string {
	pointer := s.texts[node.ID]
	if pointer == nil {
		pointer = new(string)
		s.texts[node.ID] = pointer
	}
	if fingerprint := "s:" + value; s.seen[node.ID] != fingerprint {
		s.seen[node.ID] = fingerprint
		*pointer = value
	}
	return pointer
}

func (s *Surface) syncedBool(node *domainnativeui.Node, value bool) *bool {
	pointer := s.bools[node.ID]
	if pointer == nil {
		pointer = new(bool)
		s.bools[node.ID] = pointer
	}
	if fingerprint := "b:" + strconv.FormatBool(value); s.seen[node.ID] != fingerprint {
		s.seen[node.ID] = fingerprint
		*pointer = value
	}
	return pointer
}

func (s *Surface) syncedNumber(node *domainnativeui.Node, value float64) *float64 {
	pointer := s.numbers[node.ID]
	if pointer == nil {
		pointer = new(float64)
		s.numbers[node.ID] = pointer
	}
	if fingerprint := "f:" + strconv.FormatFloat(value, 'g', -1, 64); s.seen[node.ID] != fingerprint {
		s.seen[node.ID] = fingerprint
		*pointer = value
	}
	return pointer
}

func (s *Surface) syncedChoice(node *domainnativeui.Node, value int) *int {
	pointer := s.choices[node.ID]
	if pointer == nil {
		pointer = new(int)
		s.choices[node.ID] = pointer
	}
	if fingerprint := "i:" + strconv.Itoa(value); s.seen[node.ID] != fingerprint {
		s.seen[node.ID] = fingerprint
		*pointer = value
	}
	return pointer
}

func (s *Surface) listState(node *domainnativeui.Node) *ui.ListState {
	state := s.lists[node.ID]
	if state == nil {
		selected := new(int)
		*selected = -1
		state = &ui.ListState{Selected: selected}
		s.lists[node.ID] = state
	}
	return state
}

// syncListChoice mirrors a patched selected row into the List state.
func (s *Surface) syncListChoice(node *domainnativeui.Node, state *ui.ListState) {
	selected := -1
	if value, ok := node.Prop(domainnativeui.PropSelected); ok && value.Kind == domainnativeui.PropInt {
		selected = int(value.Int)
	}
	if fingerprint := "i:" + strconv.Itoa(selected); s.seen[node.ID] != fingerprint {
		s.seen[node.ID] = fingerprint
		*state.Selected = selected
	}
}

type nodeKey struct{ id domainnativeui.NodeID }

type listDrag struct {
	Node domainnativeui.NodeID
	Row  int
}

// fontFamily resolves a packaged font asset. A missing resolver or an
// unknown asset leaves the host's default family in place.
func (s *Surface) fontFamily(asset string) (string, bool) {
	if s == nil || s.options.Fonts == nil || asset == "" {
		return "", false
	}
	return s.options.Fonts(asset)
}

func parseFloat(text string, def float64) float64 {
	if text == "" {
		return def
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return def
	}
	return value
}

func boolIndex(value bool) int32 {
	if value {
		return 1
	}
	return 0
}

func nativeMods(mods ui.Modifiers) domainnativeui.Modifiers {
	var out domainnativeui.Modifiers
	if mods&ui.Shift != 0 {
		out |= domainnativeui.ModShift
	}
	if mods&ui.Ctrl != 0 {
		out |= domainnativeui.ModControl
	}
	if mods&ui.Alt != 0 {
		out |= domainnativeui.ModAlt
	}
	if mods&ui.Super != 0 {
		out |= domainnativeui.ModMeta
	}
	return out
}

func mygoMods(mods domainnativeui.Modifiers) ui.Modifiers {
	var out ui.Modifiers
	if mods&domainnativeui.ModShift != 0 {
		out |= ui.Shift
	}
	if mods&domainnativeui.ModControl != 0 {
		out |= ui.Ctrl
	}
	if mods&domainnativeui.ModAlt != 0 {
		out |= ui.Alt
	}
	if mods&domainnativeui.ModMeta != 0 {
		out |= ui.Super
	}
	return out
}

func mygoKeyToNative(key ui.Key) (domainnativeui.Key, bool) {
	switch key {
	case ui.KeyUnknown:
		return 0, false
	case ui.KeyTab:
		return domainnativeui.KeyTab, true
	case ui.KeyEnter:
		return domainnativeui.KeyEnter, true
	case ui.KeyEscape:
		return domainnativeui.KeyEscape, true
	case ui.KeySpace:
		return domainnativeui.KeySpace, true
	case ui.KeyBackspace:
		return domainnativeui.KeyBackspace, true
	case ui.KeyDelete:
		return domainnativeui.KeyDelete, true
	case ui.KeyLeft:
		return domainnativeui.KeyLeft, true
	case ui.KeyRight:
		return domainnativeui.KeyRight, true
	case ui.KeyUp:
		return domainnativeui.KeyUp, true
	case ui.KeyDown:
		return domainnativeui.KeyDown, true
	case ui.KeyHome:
		return domainnativeui.KeyHome, true
	case ui.KeyEnd:
		return domainnativeui.KeyEnd, true
	case ui.KeyPageUp:
		return domainnativeui.KeyPageUp, true
	case ui.KeyPageDown:
		return domainnativeui.KeyPageDown, true
	case ui.KeyInsert:
		return domainnativeui.KeyInsert, true
	case ui.KeyMinus:
		return domainnativeui.KeyMinus, true
	case ui.KeyEqual:
		return domainnativeui.KeyEqual, true
	case ui.KeyComma:
		return domainnativeui.KeyComma, true
	case ui.KeyPeriod:
		return domainnativeui.KeyPeriod, true
	case ui.KeySlash:
		return domainnativeui.KeySlash, true
	case ui.KeySemicolon:
		return domainnativeui.KeySemicolon, true
	case ui.KeyQuote:
		return domainnativeui.KeyQuote, true
	case ui.KeyBracketLeft:
		return domainnativeui.KeyBracketLeft, true
	case ui.KeyBracketRight:
		return domainnativeui.KeyBracketRight, true
	case ui.KeyBackslash:
		return domainnativeui.KeyBackslash, true
	case ui.KeyBackquote:
		return domainnativeui.KeyGrave, true
	}
	if key >= ui.KeyF1 && key <= ui.KeyF12 {
		return domainnativeui.KeyF1 + domainnativeui.Key(key-ui.KeyF1), true
	}
	if key >= ui.Key0 && key <= ui.Key9 {
		return domainnativeui.Key0 + domainnativeui.Key(key-ui.Key0), true
	}
	if key >= ui.KeyA && key <= ui.KeyZ {
		return domainnativeui.KeyA + domainnativeui.Key(key-ui.KeyA), true
	}
	return 0, false
}

func nativeKeyToMygo(key domainnativeui.Key) (ui.Key, bool) {
	switch key {
	case domainnativeui.KeyTab:
		return ui.KeyTab, true
	case domainnativeui.KeyEnter:
		return ui.KeyEnter, true
	case domainnativeui.KeyEscape:
		return ui.KeyEscape, true
	case domainnativeui.KeySpace:
		return ui.KeySpace, true
	case domainnativeui.KeyBackspace:
		return ui.KeyBackspace, true
	case domainnativeui.KeyDelete:
		return ui.KeyDelete, true
	case domainnativeui.KeyLeft:
		return ui.KeyLeft, true
	case domainnativeui.KeyRight:
		return ui.KeyRight, true
	case domainnativeui.KeyUp:
		return ui.KeyUp, true
	case domainnativeui.KeyDown:
		return ui.KeyDown, true
	case domainnativeui.KeyHome:
		return ui.KeyHome, true
	case domainnativeui.KeyEnd:
		return ui.KeyEnd, true
	case domainnativeui.KeyPageUp:
		return ui.KeyPageUp, true
	case domainnativeui.KeyPageDown:
		return ui.KeyPageDown, true
	case domainnativeui.KeyInsert:
		return ui.KeyInsert, true
	}
	if key >= domainnativeui.KeyF1 && key <= domainnativeui.KeyF12 {
		return ui.KeyF1 + ui.Key(key-domainnativeui.KeyF1), true
	}
	if key >= domainnativeui.Key0 && key <= domainnativeui.Key9 {
		return ui.Key0 + ui.Key(key-domainnativeui.Key0), true
	}
	if key >= domainnativeui.KeyA && key <= domainnativeui.KeyZ {
		return ui.KeyA + ui.Key(key-domainnativeui.KeyA), true
	}
	letters := map[domainnativeui.Key]ui.Key{
		domainnativeui.KeyMinus:        ui.KeyMinus,
		domainnativeui.KeyEqual:        ui.KeyEqual,
		domainnativeui.KeyComma:        ui.KeyComma,
		domainnativeui.KeyPeriod:       ui.KeyPeriod,
		domainnativeui.KeySlash:        ui.KeySlash,
		domainnativeui.KeySemicolon:    ui.KeySemicolon,
		domainnativeui.KeyQuote:        ui.KeyQuote,
		domainnativeui.KeyBracketLeft:  ui.KeyBracketLeft,
		domainnativeui.KeyBracketRight: ui.KeyBracketRight,
		domainnativeui.KeyBackslash:    ui.KeyBackslash,
		domainnativeui.KeyGrave:        ui.KeyBackquote,
	}
	return letters[key], letters[key] != 0
}

func tokenColor(c *ui.Context, token domainnativeui.Token) ui.Color {
	theme := c.Theme()
	switch token {
	case domainnativeui.TokenBackground:
		return theme.Background
	case domainnativeui.TokenSurface:
		return theme.Surface
	case domainnativeui.TokenSurfaceHover:
		return theme.SurfaceHover
	case domainnativeui.TokenSurfacePressed:
		return theme.SurfacePressed
	case domainnativeui.TokenBorder:
		return theme.Border
	case domainnativeui.TokenText:
		return theme.Text
	case domainnativeui.TokenTextMuted:
		return theme.TextMuted
	case domainnativeui.TokenAccent:
		return theme.Accent
	case domainnativeui.TokenAccentText:
		return theme.AccentText
	case domainnativeui.TokenDanger:
		return theme.Danger
	case domainnativeui.TokenWarning:
		return theme.Warning
	case domainnativeui.TokenSuccess:
		return theme.Success
	case domainnativeui.TokenSelection:
		return theme.Selection
	case domainnativeui.TokenFocus:
		return theme.Focus
	case domainnativeui.TokenInverse:
		return theme.Inverse
	case domainnativeui.TokenInverseText:
		return theme.InverseText
	}
	return theme.Text
}

func mygoRole(role domainnativeui.Role) ui.Role {
	switch role {
	case domainnativeui.RoleButton:
		return ui.RoleButton
	case domainnativeui.RoleText:
		return ui.RoleText
	case domainnativeui.RoleHeading:
		return ui.RoleHeading
	case domainnativeui.RoleTextField:
		return ui.RoleTextField
	case domainnativeui.RoleCheckbox:
		return ui.RoleCheckBox
	case domainnativeui.RoleRadio:
		return ui.RoleRadio
	case domainnativeui.RoleSlider:
		return ui.RoleSlider
	case domainnativeui.RoleProgress:
		return ui.RoleProgress
	case domainnativeui.RoleList:
		return ui.RoleList
	case domainnativeui.RoleListItem:
		return ui.RoleListItem
	case domainnativeui.RoleTable:
		return ui.RoleTable
	case domainnativeui.RoleTree:
		return ui.RoleTree
	case domainnativeui.RoleTab:
		return ui.RoleTab
	case domainnativeui.RoleTabList:
		return ui.RoleTabList
	case domainnativeui.RoleDialog:
		return ui.RoleDialog
	case domainnativeui.RoleMenu:
		return ui.RoleMenu
	case domainnativeui.RoleMenuItem:
		return ui.RoleMenuItem
	case domainnativeui.RoleImage:
		return ui.RoleImage
	case domainnativeui.RoleTooltip:
		return ui.RoleTooltip
	case domainnativeui.RoleStatus:
		return ui.RoleStatus
	}
	return ui.RoleAuto
}

// shortcuts registers the live host shortcuts for the frame. They win over a
// focused widget, as MyGo runs element shortcuts before widget input. A node
// that captures keys registers the same chords on itself in bindKeyCapture,
// so the press reaches the plugin instead. Without a command runner this
// surface leaves the shortcuts to the window's own accelerator table, rather
// than swallowing a press it cannot dispatch.
func (s *Surface) shortcuts(c *ui.Context, env appplugin.UIEnvironment) {
	if !s.focused || s.session.CapturesKeys() || s.options.RunCommand == nil {
		return
	}
	for _, shortcut := range env.Shortcuts {
		key, ok := nativeKeyToMygo(shortcut.Key)
		if !ok {
			continue
		}
		command := shortcut.Command
		c.OnShortcut(mygoMods(shortcut.Mods), key, func() {
			s.runCommand(command)
		})
	}
}

// NativeShortcut converts one MyGo chord to the native UI environment's
// shortcut description. The app builds the environment from its live window
// accelerator tables with it; a key the ABI cannot name reports false.
func NativeShortcut(mods ui.Modifiers, key ui.Key, command string) (domainnativeui.Shortcut, bool) {
	nativeKey, ok := mygoKeyToNative(key)
	if !ok {
		return domainnativeui.Shortcut{}, false
	}
	return domainnativeui.Shortcut{Mods: nativeMods(mods), Key: nativeKey, Command: command}, true
}
