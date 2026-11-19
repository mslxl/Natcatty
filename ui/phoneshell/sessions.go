package phoneshell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// SessionHandle is one open Session the phone keeps. It owns the transport and
// the Session's state; the terminal view is attached only while the Session is
// visible. The handle is deliberately not a connection: a terminal screen
// reads and writes the connection Attach or Resume returns, and closing that
// connection detaches the view without ending the Session.
type SessionHandle interface {
	// Attach claims the Session's one live terminal view and returns the
	// connection a screen reads and writes. A second Attach while a view is
	// attached fails instead of creating another view.
	Attach() (io.ReadWriteCloser, error)
	// Hibernate releases the live view, if any, and keeps the transport and
	// every application-level field. It reports false for a Session that has
	// already ended.
	Hibernate() bool
	// Resume reattaches the view, replaying what the Session produced while
	// hibernated, and returns the connection for the new terminal.
	Resume() (io.ReadWriteCloser, error)
	// Done closes when the Session ends, whether by a remote exit or End.
	Done() <-chan struct{}
	// End disconnects and ends the Session. Only an explicit close calls it.
	End() error
}

// SessionOpener opens a saved Host as a Session and returns its handle. The
// application wires it to the same Session service the desktop uses. The
// returned handle does not attach a terminal view: the visible Tab attaches
// one, which is what keeps the phone to one live view at a time.
type SessionOpener interface {
	OpenHost(ctx context.Context, host domainvault.Host) (SessionHandle, error)
}

// TerminalScreen is one live terminal attached to a Session view. It is an
// interface so a test can drive output and typing without the native
// libghostty-vt library, which ordinary `go test` runs do not load.
type TerminalScreen interface {
	// View draws the terminal for the current frame.
	View(c *ui.Context) ui.Element
	// Close releases the terminal. It closes the connection it was built
	// over, which detaches the Session's view; it never ends the Session.
	Close() error
}

// ScreenFactory builds the terminal over a Session view. The phone root uses
// the libghostty-vt screen; a test injects a fake.
type ScreenFactory func(conn io.ReadWriteCloser) (TerminalScreen, error)

// tabStatus is the lifecycle of one Sessions Tab. The list tells them apart:
// a Tab that is visible is connected and owns the phone's one live terminal
// view; a Tab that is not visible is hibernated (transport alive, view
// released); a Tab whose resume failed is reconnecting.
type tabStatus int

const (
	tabConnecting tabStatus = iota
	tabReady
	tabHibernated
	tabReconnecting
	tabFailed
)

// phoneTab is one open Session Tab: its Host, its connection state, and, while
// it is visible, the terminal screen drawn over its one view. The phone
// Workspace is flat, so a Tab has exactly one Session and one Pane.
type phoneTab struct {
	id     string
	host   domainvault.Host
	label  string
	status tabStatus
	// problem is the actionable failure text shown when status is tabFailed
	// or tabReconnecting.
	problem string
	handle  SessionHandle
	// conn is the view while the screen is being built. Once the screen
	// exists it owns the connection; the screen's Close detaches it.
	conn   io.ReadWriteCloser
	screen TerminalScreen
}

// sessionTabs owns the Sessions section's Tabs. Each Tab is the app's one
// Session; the page behind /session/{id} draws it.
type sessionTabs struct {
	mu        sync.Mutex
	opener    SessionOpener
	newScreen ScreenFactory
	launch    func(*phoneTab)
	// show pushes one Tab's terminal page on the Sessions router.
	show func(id string)
	// close ends one Tab and leaves the page it was showing.
	close func(id string)
	// confirmClose reports whether a live Tab needs confirmation before it is
	// closed. It reads the Security preference live, so the Settings toggle
	// applies to the next close.
	confirmClose func() bool
	// pendingClose is the Tab waiting for close confirmation, if any.
	pendingClose string
	tabs         []*phoneTab
	next         int
}

// errNoOpener is the failure a phone without a Session service reports when a
// Host is tapped. It names the missing capability so the error is actionable.
var errNoOpener = errors.New("this build has no Session service")

// endedProblem is shown when a Session ends on the far end: the Tab stays,
// actionable, and reopens the Host under the same identity.
const endedProblem = "The Session ended. Open the Host again to reconnect."

// sessionEnded reports whether a handle's Session has already ended. It never
// blocks: Done is closed by the Session's teardown.
func sessionEnded(handle SessionHandle) bool {
	if handle == nil {
		return true
	}
	select {
	case <-handle.Done():
		return true
	default:
		return false
	}
}

// tabView is one Tab's state as a frame reads it.
type tabView struct {
	id      string
	label   string
	host    domainvault.Host
	status  tabStatus
	problem string
	screen  TerminalScreen
}

// begin records a connecting Tab and returns it. The caller starts the open.
func (t *sessionTabs) begin(host domainvault.Host) *phoneTab {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.next == 0 {
		t.next = 1
	}
	tab := &phoneTab{
		id:     fmt.Sprintf("tab-%d", t.next),
		host:   host,
		label:  hostName(host),
		status: tabConnecting,
	}
	t.next++
	t.tabs = append(t.tabs, tab)
	return tab
}

// finish records the open's outcome. A failure keeps the Tab only long enough
// to show its error on the pushed page; it is not an open Session and never a
// row in the Sessions list. A success stores the handle; the visible Tab's
// view is attached by syncVisible, on the UI thread.
func (t *sessionTabs) finish(id string, handle SessionHandle, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tab := t.find(id)
	if tab == nil {
		if handle != nil {
			_ = handle.End()
		}
		return
	}
	if err != nil || handle == nil {
		if handle != nil {
			_ = handle.End()
		}
		if err == nil {
			err = errNoOpener
		}
		tab.status = tabFailed
		tab.problem = connectProblem(tab.label, err)
		tab.handle = nil
		return
	}
	tab.handle = handle
	tab.status = tabReady
	tab.problem = ""
}

// syncVisible is the one place the lifecycle rule is applied: the Session
// visible from the current route is resumed (or attached for the first time),
// and every other Session is hibernated. It never closes a Session. Every
// navigation path — back, a section switch, a deep link — reaches it through
// Shell.syncLiveView, so none of them can invent its own rule.
//
// Phase 1 releases every other Tab's live view before phase 2 attaches the
// visible one, so the phone never holds two terminal views at once.
func (t *sessionTabs) syncVisible(visible string) {
	type suspended struct {
		screen TerminalScreen
		handle SessionHandle
		tab    *phoneTab
	}
	var released []suspended

	t.mu.Lock()
	for _, tab := range t.tabs {
		if tab.id == visible || tab.status != tabReady {
			continue
		}
		released = append(released, suspended{screen: tab.screen, handle: tab.handle, tab: tab})
		tab.screen, tab.conn = nil, nil
		tab.status = tabHibernated
		tab.problem = ""
	}
	t.mu.Unlock()

	// Closing the screen detaches its view. Hibernate then marks the Session
	// hibernated, keeping its transport and replay for Resume.
	for _, item := range released {
		if item.screen != nil {
			_ = item.screen.Close()
		}
		if item.handle == nil {
			continue
		}
		if item.handle.Hibernate() {
			continue
		}
		// Hibernate reports false for a Session that is no longer running or
		// that another surface already hibernated. Only the first is a problem
		// the user must act on.
		select {
		case <-item.handle.Done():
			t.mu.Lock()
			if item.tab.status == tabHibernated {
				item.tab.status = tabReconnecting
				item.tab.problem = "The Session ended while it was hidden. Open the Host again to reconnect."
			}
			t.mu.Unlock()
		default:
		}
	}

	t.mu.Lock()
	tab := t.find(visible)
	if tab == nil {
		t.mu.Unlock()
		return
	}
	resuming := false
	switch tab.status {
	case tabHibernated:
		resuming = true
	case tabReady:
		// The initial attach happens once, when the Tab first becomes
		// visible. A screen that already exists means the view is live — unless
		// the Session ended on the far end since the last frame, in which case
		// the stale screen is released and the Tab becomes retryable.
		if tab.screen != nil {
			if !sessionEnded(tab.handle) {
				t.mu.Unlock()
				return
			}
			screen := tab.screen
			tab.screen, tab.conn = nil, nil
			tab.status = tabReconnecting
			tab.problem = endedProblem
			t.mu.Unlock()
			if screen != nil {
				_ = screen.Close()
			}
			return
		}
	default:
		// Connecting, reconnecting (waiting for an explicit retry), or failed
		// Tabs are not attached here.
		t.mu.Unlock()
		return
	}
	handle := tab.handle
	label := tab.label
	if handle == nil {
		t.mu.Unlock()
		return
	}
	if sessionEnded(handle) {
		// The Session ended on the far end. Do not claim a view over a dead
		// connection; leave the Tab retryable instead.
		tab.status = tabReconnecting
		tab.problem = endedProblem
		t.mu.Unlock()
		return
	}
	// A resume shows "Reconnecting…" until it succeeds; an initial attach is
	// still part of connecting.
	if resuming {
		tab.status = tabReconnecting
	} else {
		tab.status = tabConnecting
	}
	t.mu.Unlock()

	var (
		conn io.ReadWriteCloser
		err  error
	)
	if resuming {
		conn, err = handle.Resume()
	} else {
		conn, err = handle.Attach()
	}

	t.mu.Lock()
	if tab.status != tabReconnecting && tab.status != tabConnecting {
		// The Tab was closed while the view was being attached.
		t.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		return
	}
	if err != nil {
		t.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		t.failAttach(tab, label, resuming, err)
		return
	}
	tab.conn = conn
	t.mu.Unlock()

	screen, err := t.buildScreen(conn)

	t.mu.Lock()
	if tab.status != tabReconnecting && tab.status != tabConnecting {
		// The Tab was closed while its screen was being built.
		t.mu.Unlock()
		if screen != nil {
			_ = screen.Close()
		} else if conn != nil {
			_ = conn.Close()
		}
		return
	}
	if err != nil {
		t.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		t.failAttach(tab, label, resuming, err)
		return
	}
	tab.conn = nil
	tab.screen = screen
	tab.status = tabReady
	tab.problem = ""
	t.mu.Unlock()
}

// buildScreen builds the terminal for one view. ScreenFactory runs on the UI
// thread; syncVisible is called from a frame, so that holds here too.
func (t *sessionTabs) buildScreen(conn io.ReadWriteCloser) (TerminalScreen, error) {
	if t.newScreen == nil {
		return nil, errors.New("the terminal screen is not available on this device")
	}
	return t.newScreen(conn)
}

// failAttach records a failed attach or resume. A failed resume leaves the Tab
// reconnecting and retryable; a failed initial attach is a connection failure
// with an actionable message.
func (t *sessionTabs) failAttach(tab *phoneTab, label string, resuming bool, err error) {
	t.mu.Lock()
	tab.conn = nil
	var handle SessionHandle
	if resuming {
		tab.status = tabReconnecting
		tab.problem = resumeProblem(label, err)
	} else {
		tab.status = tabFailed
		tab.problem = connectProblem(label, err)
		handle = tab.handle
		tab.handle = nil
	}
	t.mu.Unlock()
	// End outside the lock: the Session service may block.
	if handle != nil {
		_ = handle.End()
	}
}

// viewOfLocked returns one Tab's state. It never builds a screen: only
// syncVisible attaches a view, so a hidden Tab cannot claim one by accident.
func (t *sessionTabs) viewOfLocked(id string) (tabView, bool) {
	tab := t.find(id)
	if tab == nil {
		return tabView{}, false
	}
	return tabView{
		id: tab.id, label: tab.label, host: tab.host,
		status: tab.status, problem: tab.problem, screen: tab.screen,
	}, true
}

// retry reopens a failed or reconnecting Tab under the same identity, so the
// page the user is on stays put while the connection is tried again.
func (t *sessionTabs) retry(id string) {
	t.mu.Lock()
	tab := t.find(id)
	if tab == nil {
		t.mu.Unlock()
		return
	}
	screen, handle := tab.screen, tab.handle
	tab.screen, tab.conn, tab.handle = nil, nil, nil
	tab.status = tabConnecting
	tab.problem = ""
	if t.pendingClose == id {
		t.pendingClose = ""
	}
	launch := t.launch
	t.mu.Unlock()
	// Close the old attempt outside the lock: the Session service may block.
	if screen != nil {
		_ = screen.Close()
	}
	if handle != nil {
		_ = handle.End()
	}
	if launch != nil {
		launch(tab)
	}
}

// requestClose is the close action every surface uses. A live Tab asks for
// confirmation when the Security preference requires it; a hibernated Tab has
// no live view, so closing it proceeds directly.
func (t *sessionTabs) requestClose(id string) {
	t.mu.Lock()
	tab := t.find(id)
	if tab == nil {
		t.mu.Unlock()
		return
	}
	live := tab.status == tabReady || tab.status == tabConnecting
	confirm := live && t.confirmClose != nil && t.confirmClose()
	if confirm {
		t.pendingClose = id
		t.mu.Unlock()
		return
	}
	t.pendingClose = ""
	closeTab := t.close
	t.mu.Unlock()
	if closeTab != nil {
		closeTab(id)
		return
	}
	t.remove(id)
}

// resolvePendingClose answers the inline confirmation for one Tab. Confirmed
// ends the Tab; cancelled leaves it open.
func (t *sessionTabs) resolvePendingClose(id string, confirmed bool) {
	t.mu.Lock()
	if t.pendingClose != id {
		t.mu.Unlock()
		return
	}
	t.pendingClose = ""
	closeTab := t.close
	t.mu.Unlock()
	if !confirmed {
		return
	}
	if closeTab != nil {
		closeTab(id)
		return
	}
	t.remove(id)
}

// pendingCloseFor reports whether one Tab is waiting for close confirmation.
func (t *sessionTabs) pendingCloseFor(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pendingClose == id
}

// remove closes and forgets one Tab. Closing the screen detaches the view;
// End disconnects and ends the Session. Both run outside the lock: they reach
// the Session service, which must not stall a frame that is removing a Tab.
func (t *sessionTabs) remove(id string) bool {
	t.mu.Lock()
	for i, tab := range t.tabs {
		if tab.id != id {
			continue
		}
		t.tabs = append(t.tabs[:i], t.tabs[i+1:]...)
		if t.pendingClose == id {
			t.pendingClose = ""
		}
		t.mu.Unlock()
		if tab.screen != nil {
			_ = tab.screen.Close()
		}
		if tab.handle != nil {
			_ = tab.handle.End()
		}
		return true
	}
	t.mu.Unlock()
	return false
}

// discardFailed forgets a Tab whose open failed. It is not a Session: the
// error page is its only surface, and leaving that page drops it. A live or
// hibernated Tab is never discarded here.
func (t *sessionTabs) discardFailed(id string) {
	t.mu.Lock()
	tab := t.find(id)
	if tab == nil || tab.status != tabFailed {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	t.remove(id)
}

// closeAll ends every Tab. The app calls it on quit.
func (t *sessionTabs) closeAll() {
	t.mu.Lock()
	tabs := t.tabs
	t.tabs = nil
	t.pendingClose = ""
	t.mu.Unlock()
	for _, tab := range tabs {
		if tab.screen != nil {
			_ = tab.screen.Close()
		}
		if tab.handle != nil {
			_ = tab.handle.End()
		}
	}
}

// list returns the Tabs a list row should show: every open Tab, including a
// hibernated one. A failed Tab is the error page's subject, not an entry.
func (t *sessionTabs) list() []tabView {
	t.mu.Lock()
	defer t.mu.Unlock()
	views := make([]tabView, 0, len(t.tabs))
	for _, tab := range t.tabs {
		if tab.status == tabFailed {
			continue
		}
		views = append(views, tabView{
			id: tab.id, label: tab.label, host: tab.host,
			status: tab.status, problem: tab.problem,
		})
	}
	return views
}

func (t *sessionTabs) find(id string) *phoneTab {
	for _, tab := range t.tabs {
		if tab.id == id {
			return tab
		}
	}
	return nil
}

// drawRoot lists the open Tabs. Tapping one pushes its terminal page.
func (t *sessionTabs) drawRoot(c *ui.Context) {
	width := pageContentWidth(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			views := t.list()
			kit.SectionBar(c, "Open Sessions", nil)
			if len(views) == 0 {
				kit.EmptyHint(c, "No sessions yet. Open a Host from the Hosts tab.")
				return
			}
			for _, view := range views {
				view := view
				row := kit.ListRow(c).Label("Open session " + view.label)
				row.Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, view.label).FontWeight(600).SingleLine()
						statusLine(c, view)
					})
					if kit.IconButton(c, icons.X, "Close session "+view.label).Clicked() {
						t.requestClose(view.id)
						return
					}
				})
				if row.Clicked() {
					if t.show != nil {
						t.show(view.id)
					}
				}
				t.drawPendingClose(c, view)
			}
		})
	})
}

// drawSession renders one Tab's page: the connecting hint, the actionable
// error, the reconnecting notice, or the live terminal.
func (t *sessionTabs) drawSession(c *ui.Context, id string, retry func(), back func()) {
	t.mu.Lock()
	view, ok := t.viewOfLocked(id)
	t.mu.Unlock()
	if !ok {
		ui.Column(c).Fill().Padding(16).Gap(12).Children(func() {
			kit.EmptyHint(c, "This Session has closed.")
			if kit.GhostButton(c, icons.X, "Back to Sessions").Clicked() && back != nil {
				back()
			}
		})
		return
	}
	// A close requested from the page asks for confirmation above it, so the
	// user keeps seeing the terminal while deciding.
	t.drawPendingClose(c, view)
	switch view.status {
	case tabConnecting:
		ui.Column(c).Fill().Padding(16).Gap(10).Children(func() {
			kit.EmptyHint(c, "Connecting to "+view.label+"…")
			// The open runs off the UI thread; poll until its outcome lands.
			c.After(15 * time.Millisecond)
		})
	case tabReconnecting:
		ui.Column(c).Fill().Padding(16).Gap(12).Children(func() {
			kit.Notice(c, icons.TriangleAlert, c.Theme().Warning, view.problem, func() {
				if kit.GhostButton(c, icons.RefreshCw, "Try again").Clicked() && retry != nil {
					retry()
				}
			})
			if kit.GhostButton(c, icons.X, "Back to Sessions").Clicked() && back != nil {
				back()
			}
		})
	case tabFailed:
		ui.Column(c).Fill().Padding(16).Gap(12).Children(func() {
			kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, view.problem, func() {
				if kit.GhostButton(c, icons.RefreshCw, "Try again").Clicked() && retry != nil {
					retry()
				}
			})
			if kit.GhostButton(c, icons.X, "Back to Sessions").Clicked() && back != nil {
				back()
			}
		})
	default:
		if view.screen == nil {
			kit.EmptyHint(c, "The terminal is not ready.")
			return
		}
		ui.Column(c).Fill().Children(func() {
			header := ui.Row(c).FillWidth().Padding(6, 12).Gap(8).AlignItems(ui.Center).
				Background(c.Theme().Surface)
			header.Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, view.label).SingleLine().Bold()
					statusLine(c, view)
				})
				if kit.IconButton(c, icons.X, "Close session "+view.label).Clicked() {
					t.requestClose(view.id)
				}
			})
			view.screen.View(c).Fill()
		})
	}
}

// drawPendingClose shows the inline confirmation for one Tab, if it is the
// Tab waiting for one. ConfirmBar reports both buttons this frame.
func (t *sessionTabs) drawPendingClose(c *ui.Context, view tabView) {
	if !t.pendingCloseFor(view.id) {
		return
	}
	cancelled, confirmed := kit.ConfirmBar(c,
		"Close "+view.label+"?",
		func() {
			ui.Text(c, "The Session will disconnect.").FontSize(12).TextColor(c.Theme().TextMuted)
		},
		"Keep open", "Close")
	switch {
	case cancelled:
		t.resolvePendingClose(view.id, false)
	case confirmed:
		t.resolvePendingClose(view.id, true)
	}
}

// statusLine is the second line of a Sessions row: the lifecycle state and the
// endpoint, so two Sessions for the same Host label can still be told apart.
func statusLine(c *ui.Context, view tabView) {
	color := c.Theme().Accent
	switch view.status {
	case tabHibernated:
		color = c.Theme().TextMuted
	case tabConnecting, tabReconnecting:
		color = c.Theme().Warning
	}
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		kit.StatusDot(c, color)
		ui.Text(c, tabStatusText(view)).FontSize(12).
			TextColor(c.Theme().TextMuted).SingleLine()
	})
}

// tabStatusText names the lifecycle of a Sessions row and identifies the Host.
func tabStatusText(view tabView) string {
	identity := tabIdentity(view.host)
	switch view.status {
	case tabConnecting:
		return "Connecting… · " + identity
	case tabHibernated:
		return "Hibernated · " + identity
	case tabReconnecting:
		return "Reconnecting… · " + identity
	case tabFailed:
		return "Connection failed · " + identity
	}
	return "Connected · " + identity
}

// tabIdentity is the endpoint that distinguishes two Sessions with the same
// label, plus the protocol when it is not plain SSH.
func tabIdentity(host domainvault.Host) string {
	parts := []string{hostEndpoint(host)}
	if protocol := protocolOf(host); protocol != domainvault.ProtocolSSH {
		parts = append(parts, string(protocol))
	}
	return strings.Join(parts, " · ")
}

// connectProblem turns a failed open into an actionable sentence. It keeps the
// typed error's own text, so a host-key, credential, or network failure says
// what went wrong, and adds the user's next step.
func connectProblem(label string, err error) string {
	reason := strings.TrimSpace(err.Error())
	if reason == "" {
		reason = "the connection closed before a shell opened"
	}
	return fmt.Sprintf("Could not connect to %s: %s. Check the Host's address, credentials, and network, then try again.",
		label, reason)
}

// resumeProblem turns a failed resume into an actionable sentence. The
// Session's transport may have ended while it was hidden.
func resumeProblem(label string, err error) string {
	reason := strings.TrimSpace(err.Error())
	if reason == "" {
		reason = "the Session is no longer running"
	}
	return fmt.Sprintf("Could not resume %s: %s. Open the Host again to reconnect.", label, reason)
}
