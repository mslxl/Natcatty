// Package quake owns the Dropdown Panel's own Workspace: a flat list of
// terminal Tabs, one Session and Surface each, with no split panes and no
// persistence. It is window-scoped state, as ADR 0006 requires, and it is
// deliberately memory-only: hiding and showing the Panel or switching modes
// keeps the live Sessions, but an app restart starts from a fresh local Shell.
package quake

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/motion"
)

// ErrNoSessions reports a Quake workspace without a Session opener or surface
// factory, so it cannot open a Tab.
var ErrNoSessions = errors.New("quake: no session opener")

// Surface is the terminal surface a Quake Tab shows. It has the same shape as
// the shell's Surface, so the app reuses its one Session surface factory.
type Surface interface {
	View(c *ui.Context) ui.Element
	Close()
}

// FocusedSurface is implemented by a Surface whose terminal takes the
// keyboard only when its Tab is the active one. The Quake view renders the
// active Tab through it every frame; MyGo prunes an unmounted subtree's
// element state, so focus has to be requested again after a switch.
type FocusedSurface interface {
	ViewFocused(c *ui.Context, focused bool) ui.Element
}

// TerminalFontSetter is implemented by a Surface whose terminal restyles
// without being recreated.
type TerminalFontSetter interface {
	SetTerminalFont(family string, size float32)
}

// TerminalCursorSetter is implemented by a Surface whose terminal takes a new
// cursor shape and blink preference without being recreated.
type TerminalCursorSetter interface {
	SetTerminalCursor(style domainpreferences.CursorStyle, blink bool)
}

// TerminalThemeSetter is implemented by a Surface whose terminal recolors
// without being recreated.
type TerminalThemeSetter interface {
	SetTerminalTheme(preset domaintheme.TerminalTheme)
}

// SessionOpener opens the Session a Quake Tab shows. The app implements it
// over the shared Session service, so a Quake Tab has the same transport,
// logging, Command History, and notification path as a main-window Tab;
// tests substitute a fake.
type SessionOpener interface {
	OpenLocal() (*appsession.Session, error)
	OpenHost(domainvault.Host) (*appsession.Session, error)
}

// HostSource lists the saved Hosts a Host picker offers. The app backs it
// with the Vault; tests provide a fake, so the view never reads storage.
type HostSource interface {
	Hosts() []domainvault.Host
}

// SurfaceFactory builds the terminal surface for a Session. The app wires the
// same factory the main shell uses; tests wire a fake.
type SurfaceFactory func(*appsession.Session) (Surface, error)

// Tab is one Quake Tab's live state. Label carries a Host label for a remote
// Tab; a local Shell leaves it empty and the view names it by Number. HostID
// is the saved Host a remote Tab opened, or empty for a local Shell.
type Tab struct {
	Number  int
	Label   string
	HostID  model.HostID
	Session *appsession.Session
	Surface Surface
}

// TabInfo is the immutable description the tab strip renders.
type TabInfo struct {
	Index  int
	Number int
	Label  string
	HostID model.HostID
	Active bool
}

// State is one Dropdown Panel's Quake workspace. The Tab list is mutated on
// the UI thread only. A Host open runs off that thread, as the main shell's
// does, and queues its outcome for the next Flush; the queue is the only
// state a connection goroutine touches, under mu.
type State struct {
	opener   SessionOpener
	surfaces SurfaceFactory
	redraw   func()
	// empty is called after the last Tab closes, so the app can hide the
	// Panel. The next Activate creates a new local Shell.
	empty func()
	// hosts backs the Host picker. A nil source leaves the picker with the
	// local Shell entry and an empty Host list.
	hosts HostSource
	// motion is the live Pane and Tab motion setting. The motion runtime
	// pulls it at each event, so a preference change reaches an already-open
	// Panel. Nil keeps the defaults: motion enabled.
	motion func() domainpreferences.Motion
	tabs   []Tab
	// active is the index of the active Tab, or -1 when there is none.
	active int
	// nextNumber names local Tabs the way the main shell does, starting at 1.
	nextNumber int
	// pickerOpen is the Host picker's visible state and pickerQuery its
	// filter. Both are transient and never persist.
	pickerOpen  bool
	pickerQuery string
	// problem is the last failed open, for the view to explain.
	problem error
	// mu guards the pending Host opens handed over by connection goroutines.
	// The rest of the state stays UI-thread-only.
	mu sync.Mutex
	// pendingHosts holds completed Host opens that Flush has not applied yet.
	pendingHosts []hostOpen
	// closed reports that Shutdown ran, so a Host open that finishes later
	// closes its Session instead of attaching an orphaned Tab.
	closed bool
	// motionEvent is the latest layout-changing Tab event and motionSeq its
	// monotonic stamp. The bar and the body consume it independently: each
	// keeps its own last sequence, so the strip's progress never drives the
	// body and a second visible renderer plays its own copy.
	motionEvent motion.Event
	motionSeq   uint64
	// barUnrendered and viewUnrendered record that the bar or the body built
	// no frame since its last one, because the Panel's current route (another
	// route or a hidden Panel) bypassed it. Each runtime consumes its own
	// notice on its next frame and warps, so an event the user never saw is not
	// replayed. They are view-local: the bar and body keep separate runtimes.
	barUnrendered  bool
	viewUnrendered bool
}

// quakeTabID is the motion identity of a Quake Tab. A Quake Tab has no
// model.TabID: its number is stable for the Tab's whole life and is already the
// strip item's key.
func quakeTabID(number int) model.TabID {
	return model.TabID("quake-tab-" + strconv.Itoa(number))
}

// tabNumberFromID is the inverse of quakeTabID, or 0 when id is not a Quake
// Tab identity.
func tabNumberFromID(id model.TabID) int {
	value, err := strconv.Atoi(strings.TrimPrefix(string(id), "quake-tab-"))
	if err != nil {
		return 0
	}
	return value
}

// hostOpen is one Host connection that finished off the UI thread. Flush builds
// its Surface and appends its Tab, or records its error for the view.
type hostOpen struct {
	label   string
	hostID  model.HostID
	session *appsession.Session
	err     error
}

// Option customizes a State.
type Option func(*State)

// WithRedraw sets the function called when the workspace changes.
func WithRedraw(fn func()) Option { return func(s *State) { s.redraw = fn } }

// WithEmpty sets the function called when the last Tab closes. The app wires
// it to the Dropdown Panel's hide action.
func WithEmpty(fn func()) Option { return func(s *State) { s.empty = fn } }

// WithHosts installs the saved Hosts the Host picker lists. It is read live,
// so a Vault edit shows on the next frame without rebuilding the Panel.
func WithHosts(source HostSource) Option { return func(s *State) { s.hosts = source } }

// WithMotion supplies the live Pane and Tab motion setting. The motion
// runtime pulls it on every event, so a Settings change applies without
// rebuilding the Panel. Without it the Panel reports the default motion: on.
func WithMotion(motion func() domainpreferences.Motion) Option {
	return func(s *State) { s.motion = motion }
}

// Motion returns the live Pane and Tab motion preferences. It is a pull
// getter: the view reads it at the event, not once at construction.
func (s *State) Motion() domainpreferences.Motion {
	if s.motion == nil {
		return domainpreferences.Defaults().Appearance.Motion
	}
	return s.motion()
}

// MotionEvent returns the latest Quake Tab event and whether the workspace
// has recorded one. Only Tab kinds are ever recorded: a Quake Tab is one
// terminal with no split Panes, so there are no Pane events. Seq is monotonic,
// and each view consumes only the latest event it has not seen.
func (s *State) MotionEvent() (motion.Event, bool) {
	if s.motionSeq == 0 {
		return motion.Event{}, false
	}
	return s.motionEvent, true
}

// NoteBarUnrendered records that the Panel's bar built no frame since its last
// one. The app calls it for every route whose top bar is not the Quake bar (the
// follow route, top-down, or the neutral placeholder) and on every show, so a
// Panel that reappears warps the Tab events recorded while it was hidden. A nil
// State ignores the notice.
func (s *State) NoteBarUnrendered() {
	if s != nil {
		s.barUnrendered = true
	}
}

// NoteViewUnrendered records that the Panel's body built no frame since its
// last one. It is the body's copy of NoteBarUnrendered: the bar and body keep
// separate runtimes and last-seen stamps, so each needs its own notice.
func (s *State) NoteViewUnrendered() {
	if s != nil {
		s.viewUnrendered = true
	}
}

// takeBarUnrendered consumes the bar's notice for the next frame. It is
// one-shot, so an event seen on a later visible frame still animates.
func (s *State) takeBarUnrendered() bool {
	if s == nil {
		return false
	}
	unrendered := s.barUnrendered
	s.barUnrendered = false
	return unrendered
}

// takeViewUnrendered consumes the body's notice for the next frame.
func (s *State) takeViewUnrendered() bool {
	if s == nil {
		return false
	}
	unrendered := s.viewUnrendered
	s.viewUnrendered = false
	return unrendered
}

// recordMotion stamps event with the next sequence and keeps it as the latest.
// The state records events even while motion is off; the view decides to warp.
func (s *State) recordMotion(event motion.Event) {
	s.motionSeq++
	event.Seq = s.motionSeq
	if event.At.IsZero() {
		event.At = time.Now()
	}
	s.motionEvent = event
}

// NewState returns an empty Quake workspace. The first Activate opens the
// local Shell; a new State never restores a previous run's Tabs.
func NewState(opener SessionOpener, surfaces SurfaceFactory, opts ...Option) *State {
	state := &State{opener: opener, surfaces: surfaces, active: -1, nextNumber: 1}
	for _, opt := range opts {
		opt(state)
	}
	return state
}

// Activate ensures the workspace has at least one Tab, opening the first
// local Shell when it is empty. The Panel's show path calls it; because it is
// idempotent, hiding and showing the Panel keeps the same Session and Surface.
func (s *State) Activate() error {
	// A show starts with no transient flyout open. A picker left open when the
	// Panel was hidden must not reappear over a freshly shown workspace.
	s.ClosePicker()
	if len(s.tabs) > 0 {
		return nil
	}
	return s.AddLocalTab()
}

// AddLocalTab opens another local Shell Tab and makes it active.
func (s *State) AddLocalTab() error {
	return s.openTab("", "", func() (*appsession.Session, error) {
		if s.opener == nil {
			return nil, ErrNoSessions
		}
		return s.opener.OpenLocal()
	})
}

// AddHostTab opens a Host in a new Quake Tab and makes it active. The Tab
// carries the Host's label, so the strip marks which Host it connects to. The
// Session is opened through the same SessionOpener port as the local Shell.
//
// Dialing, host-key verification, and password authentication can wait on the
// network or on the user's trust decision, so they run on their own goroutine,
// as the main shell's Host open does. The outcome is applied by the next Frame
// through Flush; a failed connection records the problem instead of appending
// a Tab. This function therefore reports only the configuration error (no
// opener or surface factory), never the connection result.
func (s *State) AddHostTab(host domainvault.Host) error {
	if s.opener == nil || s.surfaces == nil {
		return ErrNoSessions
	}
	// Choosing a Host dismisses the picker even when the open fails, so the
	// error is visible in the body instead of behind the flyout.
	s.ClosePicker()
	s.problem = nil
	label := hostTabLabel(host)
	go func() {
		session, err := s.opener.OpenHost(host)
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			if session != nil {
				_ = session.Close()
			}
			return
		}
		s.pendingHosts = append(s.pendingHosts, hostOpen{
			label:   label,
			hostID:  host.ID,
			session: session,
			err:     err,
		})
		s.mu.Unlock()
		// A new connection outcome needs the frame that shows its Tab.
		s.requestRedraw()
	}()
	return nil
}

// Flush applies Host opens that finished since the last frame. The view calls
// it on the UI thread before rendering, so the Tab list and its Surfaces are
// still built and mutated only there. It is idempotent, so the bar and the
// body may both call it in one frame.
func (s *State) Flush() {
	s.mu.Lock()
	outcomes := s.pendingHosts
	s.pendingHosts = nil
	closed := s.closed
	s.mu.Unlock()

	for _, outcome := range outcomes {
		if closed {
			if outcome.session != nil {
				_ = outcome.session.Close()
			}
			continue
		}
		if outcome.err != nil {
			s.problem = outcome.err
			continue
		}
		surface, err := s.surfaces(outcome.session)
		if err != nil {
			_ = outcome.session.Close()
			s.problem = err
			continue
		}
		tab := Tab{
			Number:  s.nextNumber,
			Label:   outcome.label,
			HostID:  outcome.hostID,
			Session: outcome.session,
			Surface: surface,
		}
		s.tabs = append(s.tabs, tab)
		s.nextNumber++
		s.active = len(s.tabs) - 1
		s.problem = nil
		s.recordMotion(motion.Event{Kind: motion.TabOpen, TabID: quakeTabID(tab.Number)})
	}
}

// openTab is the local Tab-opening path: it resolves the Session through open,
// builds its surface, appends the Tab, and makes it active. A failed open
// records the problem for the view and leaves the Tab list unchanged. Host
// opens cannot use it because their connection must not block the UI thread;
// see AddHostTab and Flush.
func (s *State) openTab(label string, hostID model.HostID, open func() (*appsession.Session, error)) error {
	if s.surfaces == nil {
		return ErrNoSessions
	}
	// Choosing an entry dismisses the picker even when the open fails, so the
	// error is visible in the body instead of behind the flyout.
	s.ClosePicker()
	s.problem = nil
	session, err := open()
	if err != nil {
		s.problem = err
		s.requestRedraw()
		return err
	}
	surface, err := s.surfaces(session)
	if err != nil {
		_ = session.Close()
		s.problem = err
		s.requestRedraw()
		return err
	}
	tab := Tab{
		Number:  s.nextNumber,
		Label:   label,
		HostID:  hostID,
		Session: session,
		Surface: surface,
	}
	s.tabs = append(s.tabs, tab)
	s.nextNumber++
	s.active = len(s.tabs) - 1
	s.recordMotion(motion.Event{Kind: motion.TabOpen, TabID: quakeTabID(tab.Number)})
	s.requestRedraw()
	return nil
}

// hostTabLabel names a remote Tab. A Host without a label is shown by its
// address; a malformed Host with neither falls back to the empty label, and
// the view then names the Tab by its number like a local Shell.
func hostTabLabel(host domainvault.Host) string {
	if label := strings.TrimSpace(host.Label); label != "" {
		return label
	}
	return strings.TrimSpace(host.Hostname)
}

// Hosts returns the saved Hosts the picker lists, in Vault order. It is empty
// when no Host source is wired.
func (s *State) Hosts() []domainvault.Host {
	if s.hosts == nil {
		return nil
	}
	return s.hosts.Hosts()
}

// PickerOpen reports whether the Host picker is showing.
func (s *State) PickerOpen() bool { return s.pickerOpen }

// Query returns the Host picker's current filter text.
func (s *State) Query() string { return s.pickerQuery }

// OpenPicker shows the Host picker with an empty filter.
func (s *State) OpenPicker() {
	if s.pickerOpen && s.pickerQuery == "" {
		return
	}
	s.pickerOpen = true
	s.pickerQuery = ""
	s.requestRedraw()
}

// ClosePicker hides the Host picker without touching the Tabs. It is a no-op
// when the picker is already closed.
func (s *State) ClosePicker() {
	if !s.pickerOpen && s.pickerQuery == "" {
		return
	}
	s.pickerOpen = false
	s.pickerQuery = ""
	s.requestRedraw()
}

// TogglePicker shows or hides the Host picker.
func (s *State) TogglePicker() {
	if s.pickerOpen {
		s.ClosePicker()
		return
	}
	s.OpenPicker()
}

// DismissOverlay handles a close request aimed at the Panel before the Panel
// itself hides. It closes the Host picker and reports true when it consumed
// the request, so the first Escape dismisses the picker and the next one
// hides the Panel.
func (s *State) DismissOverlay() bool {
	if !s.pickerOpen {
		return false
	}
	s.ClosePicker()
	return true
}

// QueryPtr is the Host picker's filter text for the view's search field.
func (s *State) QueryPtr() *string { return &s.pickerQuery }

// Tabs returns the current Tab descriptions in workspace order.
func (s *State) Tabs() []TabInfo {
	tabs := make([]TabInfo, len(s.tabs))
	for index, tab := range s.tabs {
		tabs[index] = TabInfo{
			Index:  index,
			Number: tab.Number,
			Label:  tab.Label,
			HostID: tab.HostID,
			Active: index == s.active,
		}
	}
	return tabs
}

// TabCount reports how many Tabs the workspace holds.
func (s *State) TabCount() int { return len(s.tabs) }

// ActiveIndex returns the active Tab's index, or -1 when the workspace is
// empty.
func (s *State) ActiveIndex() int { return s.active }

// Active returns the active Tab's live Session and Surface.
func (s *State) Active() (Tab, bool) {
	if s.active < 0 || s.active >= len(s.tabs) {
		return Tab{}, false
	}
	return s.tabs[s.active], true
}

// TabAt returns the live Tab at index, including its Session and Surface. The
// tray reads it to report a Quake Tab's connection state, which TabInfo does
// not carry.
func (s *State) TabAt(index int) (Tab, bool) {
	if index < 0 || index >= len(s.tabs) {
		return Tab{}, false
	}
	return s.tabs[index], true
}

// TabForSession returns the Tab that owns id. Notification clicks use it to
// locate a Quake Session when the notification carries only the Session
// identity, not the Tab index.
func (s *State) TabForSession(id model.SessionID) (TabInfo, bool) {
	if id == "" {
		return TabInfo{}, false
	}
	for index, tab := range s.tabs {
		if tab.Session == nil || tab.Session.ID() != id {
			continue
		}
		return TabInfo{
			Index:  index,
			Number: tab.Number,
			Label:  tab.Label,
			HostID: tab.HostID,
			Active: index == s.active,
		}, true
	}
	return TabInfo{}, false
}

// FocusSession makes the Tab that owns id active and reports whether one did.
// It is the notification-click half of the workspace integration: it never
// opens, closes, or reorders a Tab.
func (s *State) FocusSession(id model.SessionID) bool {
	tab, ok := s.TabForSession(id)
	if !ok {
		return false
	}
	s.SelectTab(tab.Index)
	return true
}

// SessionCount returns the running Sessions in the Quake workspace. A Tab
// whose Session ended or was closed does not count, matching the main shell's
// quit count. The quit prompt adds it so a live Quake Session is never
// discarded without the user's confirmation.
func (s *State) SessionCount() int {
	count := 0
	for _, tab := range s.tabs {
		if tab.Session != nil && tab.Session.Snapshot().Status == appsession.StatusRunning {
			count++
		}
	}
	return count
}

// SelectTab makes the Tab at index active. An out-of-range index is a no-op.
func (s *State) SelectTab(index int) {
	if index < 0 || index >= len(s.tabs) || index == s.active {
		return
	}
	// The direction is the target index against the current one; it must be
	// computed before the active index moves. A Tab switch animates the body
	// only and changes the selected fill immediately.
	direction := 1
	if index < s.active {
		direction = -1
	}
	tab := s.tabs[index]
	s.active = index
	s.recordMotion(motion.Event{
		Kind:      motion.TabSwitch,
		TabID:     quakeTabID(tab.Number),
		Direction: direction,
	})
	s.requestRedraw()
}

// CloseTab closes the Tab at index, ends its Surface and Session, and keeps
// the active index valid. Closing the last Tab calls the empty callback so the
// app hides the Panel. It reports whether a Tab was closed.
func (s *State) CloseTab(index int) bool {
	if index < 0 || index >= len(s.tabs) {
		return false
	}
	tab := s.tabs[index]
	wasActive := index == s.active
	// The replacement and its direction are read from the Tab list before the
	// removal. Closing the active Tab moves to its right neighbour, or to the
	// left one when it was last; a non-current close moves no body.
	replacement := 0
	direction := 1
	if len(s.tabs) > 1 {
		// The replacement is the Tab that takes the closing one's place: the
		// right neighbour in the surviving list, or the left one when the
		// closed Tab was last. Reading the old list means an active close in
		// the middle names the old index+1.
		targetIndex := s.active
		switch {
		case index < s.active:
			targetIndex = s.active - 1
		case index == s.active && s.active < len(s.tabs)-1:
			targetIndex = s.active + 1
		case index == s.active:
			targetIndex = len(s.tabs) - 2
		}
		if wasActive {
			replacement = s.tabs[targetIndex].Number
		}
		if targetIndex > index {
			direction = 1
		} else {
			direction = -1
		}
	}
	if tab.Surface != nil {
		tab.Surface.Close()
	}
	if tab.Session != nil {
		_ = tab.Session.Close()
	}
	s.tabs = append(s.tabs[:index], s.tabs[index+1:]...)
	switch {
	case len(s.tabs) == 0:
		s.active = -1
	case index < s.active:
		s.active--
	case index == s.active && s.active >= len(s.tabs):
		// The active Tab was the last one; the new last Tab takes over.
		s.active = len(s.tabs) - 1
	}
	event := motion.Event{
		Kind:      motion.TabClose,
		TabID:     quakeTabID(tab.Number),
		Direction: direction,
	}
	if wasActive && replacement != 0 {
		event.TargetTabID = quakeTabID(replacement)
	}
	s.recordMotion(event)
	s.requestRedraw()
	if len(s.tabs) == 0 && s.empty != nil {
		s.empty()
	}
	return true
}

// Problem returns the last failed open, or nil after a successful one.
func (s *State) Problem() error { return s.problem }

// Shutdown closes every Tab's Surface and Session without hiding the Panel.
// The app calls it on quit, after the Panel has been hidden or destroyed. A
// Host open still dialing closes its Session when it finishes instead of
// attaching a Tab after the workspace is gone.
func (s *State) Shutdown() {
	s.mu.Lock()
	s.closed = true
	pending := s.pendingHosts
	s.pendingHosts = nil
	s.mu.Unlock()
	for _, outcome := range pending {
		if outcome.session != nil {
			_ = outcome.session.Close()
		}
	}
	for _, tab := range s.tabs {
		if tab.Surface != nil {
			tab.Surface.Close()
		}
		if tab.Session != nil {
			_ = tab.Session.Close()
		}
	}
	s.tabs = nil
	s.active = -1
	s.pickerOpen = false
	s.pickerQuery = ""
}

// SetTerminalFont applies a live font change to every open Quake terminal.
func (s *State) SetTerminalFont(family string, size float32) {
	s.eachSurface(func(surface Surface) {
		if setter, ok := surface.(TerminalFontSetter); ok {
			setter.SetTerminalFont(family, size)
		}
	})
}

// SetTerminalCursor applies a live cursor change to every open Quake terminal.
func (s *State) SetTerminalCursor(style domainpreferences.CursorStyle, blink bool) {
	s.eachSurface(func(surface Surface) {
		if setter, ok := surface.(TerminalCursorSetter); ok {
			setter.SetTerminalCursor(style, blink)
		}
	})
}

// SetTerminalTheme applies a live terminal scheme change to every open Quake
// terminal, so the Panel follows the current theme without a restart.
func (s *State) SetTerminalTheme(preset domaintheme.TerminalTheme) {
	s.eachSurface(func(surface Surface) {
		if setter, ok := surface.(TerminalThemeSetter); ok {
			setter.SetTerminalTheme(preset)
		}
	})
}

func (s *State) eachSurface(fn func(Surface)) {
	for _, tab := range s.tabs {
		if tab.Surface != nil {
			fn(tab.Surface)
		}
	}
}

// requestRedraw asks the app for the next frame. It is a no-op without a
// redraw request.
func (s *State) requestRedraw() {
	if s.redraw != nil {
		s.redraw()
	}
}
