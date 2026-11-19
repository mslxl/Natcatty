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

	domainmodel "github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
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

// SessionStore persists the Tabs a relaunch should list. The payload is the
// same Workspace restore payload the desktop writes, so the phone's flat
// workspace reuses the shared format and FlattenRestored; the application
// wires it to the shared SQLite Workspace row. A Shell without a store simply
// forgets its Tabs at exit.
type SessionStore interface {
	Load(ctx context.Context) (domainworkspace.Payload, error)
	Save(ctx context.Context, payload domainworkspace.Payload) error
}

// TerminalScreen is one live terminal attached to a Session view. It is an
// interface so a test can drive output and typing without the native
// libghostty-vt library, which ordinary `go test` runs do not load.
type TerminalScreen interface {
	// View draws the terminal for the current frame.
	View(c *ui.Context) ui.Element
	// SendKey types one key as the accessory bar's, through the terminal's
	// own encoder. It carries no text: the key and its modifiers decide.
	SendKey(key ui.Key, mods ui.Modifiers)
	// Close detaches the screen. The Session handle's Close ends the Session.
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
	// tabDormant is a Tab a previous launch left open. It owns no connection
	// until the user taps it (or a deep link makes it visible); then it is
	// restored under the same Tab identity.
	tabDormant
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
	// cancel aborts the in-flight open, including any queued host-key or
	// credential prompt. It is nil before the open starts and after it ends.
	cancel context.CancelFunc
	// restoring marks an open that reconnects a dormant Tab from a previous
	// launch. Its failure returns the Tab to dormant with the error, rather
	// than discarding an entry the user can retry.
	restoring bool
	// reopening marks an automatic foreground reconnect of a Tab whose
	// Session no longer runs. Its failure keeps the Tab reconnecting and
	// retryable instead of dropping it from the list.
	reopening bool
	// accessory holds the bar's Ctrl and Alt latches for this Tab.
	accessory accessoryState
}

// sessionTabs owns the Sessions section's Tabs. Each Tab is the app's one
// Session; the page behind /session/{id} draws it.
type sessionTabs struct {
	mu        sync.Mutex
	opener    SessionOpener
	newScreen ScreenFactory
	// prompts holds the trust and credential prompts the Tabs answer.
	prompts *Prompts
	launch  func(*phoneTab)
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
	// store persists the Tabs a relaunch lists. nil means this Shell forgets
	// its Tabs at exit; it never fails navigation.
	store SessionStore
	// restored records that this launch's persisted Tabs were read once, so a
	// reload cannot duplicate them.
	restored bool
	// saveMu serializes persistence snapshots, so an open's outcome cannot
	// overwrite a newer snapshot written by the UI.
	saveMu sync.Mutex
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
	// accessory is the page's latch state, which outlives a frame.
	accessory *accessoryState
}

// begin records a connecting Tab and returns it. The caller starts the open.
func (t *sessionTabs) begin(host domainvault.Host) *phoneTab {
	t.mu.Lock()
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
	t.mu.Unlock()
	// The Tab exists the moment it is opened: an app suspended mid-connect
	// still lists it after a relaunch.
	t.persist()
	return tab
}

// finish records the open's outcome. A failure keeps the Tab only long enough
// to show its error on the pushed page; it is not an open Session and never a
// row in the Sessions list. A success stores the handle; the visible Tab's
// view is attached by syncVisible, on the UI thread.
func (t *sessionTabs) finish(id string, handle SessionHandle, err error) {
	t.mu.Lock()
	tab := t.find(id)
	if tab == nil {
		t.mu.Unlock()
		if handle != nil {
			_ = handle.End()
		}
		return
	}
	cancel := tab.cancel
	tab.cancel = nil
	restoring, reopening := tab.restoring, tab.reopening
	tab.restoring, tab.reopening = false, false
	if err != nil || handle == nil {
		if handle != nil {
			_ = handle.End()
		}
		if err == nil {
			err = errNoOpener
		}
		tab.handle = nil
		switch {
		case restoring:
			// A dormant Tab stays an entry: its failure is shown on the page
			// and the Tab remains retryable.
			tab.status = tabDormant
			tab.problem = restoreProblem(tab.label, err)
		case reopening:
			// The Session the foreground tried to reconnect is gone; keep
			// the Tab in the list so the user can still retry it.
			tab.status = tabReconnecting
			tab.problem = connectProblem(tab.label, err)
		default:
			tab.status = tabFailed
			tab.problem = connectProblem(tab.label, err)
		}
	} else {
		tab.handle = handle
		tab.status = tabReady
		tab.problem = ""
	}
	t.mu.Unlock()
	// The dial context is only live for the open; releasing it here also
	// resolves any prompt the attempt abandoned.
	if cancel != nil {
		cancel()
	}
	// The outcome decides whether the Tab is an entry a relaunch lists.
	t.persist()
}

// attachCancel records the in-flight open's cancel function. A Tab that was
// removed while its open was starting is cancelled immediately.
func (t *sessionTabs) attachCancel(id string, cancel context.CancelFunc) {
	t.mu.Lock()
	tab := t.find(id)
	if tab == nil {
		t.mu.Unlock()
		cancel()
		return
	}
	previous := tab.cancel
	tab.cancel = cancel
	t.mu.Unlock()
	if previous != nil {
		previous()
	}
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
	case tabDormant:
		// A Tab from a previous launch reconnects when it becomes visible,
		// under its existing identity. A failed restore keeps its problem and
		// waits for the user's explicit retry, so a frame cannot loop on it.
		if tab.problem != "" {
			t.mu.Unlock()
			return
		}
		tab.status = tabConnecting
		tab.restoring = true
		launch := t.launch
		t.mu.Unlock()
		if launch != nil {
			launch(tab)
		}
		return
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
		accessory: &tab.accessory,
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
	cancel := tab.cancel
	tab.screen, tab.conn, tab.handle, tab.cancel = nil, nil, nil, nil
	// A retry of a dormant or reconnecting Tab is a restore of an existing
	// entry: a failure must leave it in the list, not discard it.
	tab.restoring, tab.reopening = false, false
	switch tab.status {
	case tabDormant:
		tab.restoring = true
	case tabReconnecting:
		tab.reopening = true
	}
	tab.status = tabConnecting
	tab.problem = ""
	if t.pendingClose == id {
		t.pendingClose = ""
	}
	launch := t.launch
	t.mu.Unlock()
	// Close the old attempt outside the lock: the Session service may block.
	if cancel != nil {
		cancel()
	}
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

// restore starts the open of a dormant Tab under its existing identity. It is
// the deliberate retry: any earlier failure text is cleared before the new
// attempt. It does nothing for a Tab that is not dormant.
func (t *sessionTabs) restore(id string) {
	t.mu.Lock()
	tab := t.find(id)
	if tab == nil || tab.status != tabDormant {
		t.mu.Unlock()
		return
	}
	tab.status = tabConnecting
	tab.problem = ""
	tab.restoring = true
	launch := t.launch
	t.mu.Unlock()
	if launch != nil {
		launch(tab)
	}
}

// reconnectVisible reopens the visible Tab when its Session is gone. The
// foreground calls it after syncVisible, so a resume that failed — the
// transport died while the app was suspended — becomes a fresh connection
// without the user acting. A Tab that is not reconnecting is left alone.
func (t *sessionTabs) reconnectVisible(id string) {
	if id == "" {
		return
	}
	t.mu.Lock()
	tab := t.find(id)
	if tab == nil || tab.status != tabReconnecting {
		t.mu.Unlock()
		return
	}
	screen, handle := tab.screen, tab.handle
	tab.screen, tab.conn, tab.handle = nil, nil, nil
	tab.status = tabConnecting
	tab.problem = ""
	tab.reopening = true
	launch := t.launch
	t.mu.Unlock()
	// The old Session no longer runs; release it outside the lock, then open
	// the Host again under the same Tab.
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
		cancel := tab.cancel
		tab.cancel = nil
		t.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if tab.screen != nil {
			_ = tab.screen.Close()
		}
		if tab.handle != nil {
			_ = tab.handle.End()
		}
		// A closed Tab is no longer a Session a relaunch lists.
		t.persist()
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

// closeAll ends every Tab. The app calls it on quit. The Tabs are persisted
// first: quitting is not closing a Session, so the next launch still lists
// them as dormant entries.
func (t *sessionTabs) closeAll() {
	t.mu.Lock()
	tabs := t.tabs
	cancels := make([]context.CancelFunc, len(tabs))
	for i, tab := range tabs {
		cancels[i] = tab.cancel
		tab.cancel = nil
	}
	payload := payloadOf(tabs)
	t.tabs = nil
	t.pendingClose = ""
	t.mu.Unlock()
	for i, tab := range tabs {
		if cancels[i] != nil {
			cancels[i]()
		}
		if tab.screen != nil {
			_ = tab.screen.Close()
		}
		if tab.handle != nil {
			_ = tab.handle.End()
		}
	}
	t.savePayload(payload)
}

// adoptRestored adds the Tabs a previous launch left open as dormant entries,
// in the order FlattenRestored produced them. Each Tab resolves its Host from
// the freshly loaded Vault list, so a renamed or reconfigured Host is opened
// as it is now; a Host that is gone keeps the descriptor's identity and fails
// actionably when restored.
func (t *sessionTabs) adoptRestored(restored []RestoredSession, lookup func(string) (domainvault.Host, bool)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, session := range restored {
		host, ok := lookup(string(session.Descriptor.HostID))
		if !ok {
			host = restoredHost(session)
		}
		if t.next == 0 {
			t.next = 1
		}
		t.tabs = append(t.tabs, &phoneTab{
			id:     fmt.Sprintf("tab-%d", t.next),
			host:   host,
			label:  session.Title,
			status: tabDormant,
		})
		t.next++
	}
}

// persist snapshots the open Tabs and stores them. The snapshot and the save
// are serialized, so an open's outcome cannot overwrite a newer layout.
func (t *sessionTabs) persist() {
	t.saveMu.Lock()
	defer t.saveMu.Unlock()
	if t.store == nil {
		return
	}
	t.mu.Lock()
	payload := payloadOf(t.tabs)
	t.mu.Unlock()
	_ = t.store.Save(context.Background(), payload)
}

// savePayload stores a snapshot taken before the Tabs were cleared.
func (t *sessionTabs) savePayload(payload domainworkspace.Payload) {
	t.saveMu.Lock()
	defer t.saveMu.Unlock()
	if t.store == nil {
		return
	}
	_ = t.store.Save(context.Background(), payload)
}

// payloadOf snapshots the Tabs that should survive a relaunch in the shared
// Workspace restore format. The phone Workspace is flat, so every Tab is one
// Pane. A Tab whose open failed is not a Session and is omitted; a dormant or
// reconnecting Tab is an entry the user can still restore, so it is kept.
func payloadOf(tabs []*phoneTab) domainworkspace.Payload {
	payload := domainworkspace.EmptyPayload()
	for i, tab := range tabs {
		if tab.status == tabFailed {
			continue
		}
		descriptor := descriptorOf(tab.host, tab.label)
		if !descriptor.Valid() {
			// Without a Host identity and endpoint there is nothing restore
			// could open, so it is not an entry.
			continue
		}
		pane := domainmodel.PaneID("pane-" + tab.id)
		payload.Tabs = append(payload.Tabs, domainworkspace.RestoredTab{
			ID:     domainmodel.TabID(tab.id),
			Number: i + 1,
			Title:  tab.label,
			Root:   domainworkspace.NewPaneNode(pane),
		})
		payload.Panes = append(payload.Panes, domainworkspace.RestoredPane{
			ID:      pane,
			Session: descriptor,
		})
	}
	return payload
}

// descriptorOf describes one open phone Tab for restore. It captures no
// credential or output, exactly as the desktop descriptor does.
func descriptorOf(host domainvault.Host, label string) domainworkspace.SessionDescriptor {
	return domainworkspace.SessionDescriptor{
		Kind:     sessionKindOf(host),
		Label:    label,
		HostID:   host.ID,
		Hostname: host.Hostname,
		Port:     host.Port,
		Username: host.Username,
	}
}

// restoredHost rebuilds the endpoint of a Host that is no longer in the
// Vault. The restore still fails actionably through the Session service, and
// the row keeps enough identity to tell the user what could not be opened.
func restoredHost(session RestoredSession) domainvault.Host {
	descriptor := session.Descriptor
	return domainvault.Host{
		ID:       descriptor.HostID,
		Label:    firstNonEmpty(descriptor.CustomName, descriptor.Label, session.Title),
		Hostname: descriptor.Hostname,
		Port:     descriptor.Port,
		Username: descriptor.Username,
		Protocol: protocolOfKind(descriptor.Kind),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sessionKindOf(host domainvault.Host) domainworkspace.SessionKind {
	switch protocolOf(host) {
	case domainvault.ProtocolTelnet:
		return domainworkspace.SessionTelnet
	case domainvault.ProtocolMosh:
		return domainworkspace.SessionMosh
	case domainvault.ProtocolET:
		return domainworkspace.SessionET
	case domainvault.ProtocolLocal:
		return domainworkspace.SessionLocal
	default:
		return domainworkspace.SessionSSH
	}
}

func protocolOfKind(kind domainworkspace.SessionKind) domainvault.Protocol {
	switch kind {
	case domainworkspace.SessionTelnet:
		return domainvault.ProtocolTelnet
	case domainworkspace.SessionMosh:
		return domainvault.ProtocolMosh
	case domainworkspace.SessionET:
		return domainvault.ProtocolET
	case domainworkspace.SessionLocal:
		return domainvault.ProtocolLocal
	default:
		return domainvault.ProtocolSSH
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

// tabForHost returns the identity of the open Tab already addressing one saved
// Host. A failed open is not an open Session, so it is skipped: a link for the
// same Host starts a fresh attempt instead of selecting an error page. The
// caller uses this to select an existing Tab rather than stacking a duplicate.
func (t *sessionTabs) tabForHost(id domainmodel.HostID) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, tab := range t.tabs {
		if tab.status == tabFailed {
			continue
		}
		if tab.host.ID == id {
			return tab.id, true
		}
	}
	return "", false
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
					// Tapping a dormant entry is the restore gesture: it
					// reconnects the persisted Tab, clearing an earlier
					// failure so the attempt is not blocked by stale text.
					if view.status == tabDormant {
						t.restore(view.id)
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
			// A host-key or credential challenge is drawn here, on the page of
			// the Tab that raised it. Rejecting or cancelling a prompt closes
			// the Tab, so no attempt is left open behind the user.
			if t.prompts != nil {
				t.prompts.HostKey.drawHostKey(c, view.id, back)
				t.prompts.Auth.drawAuth(c, view.id, back)
			}
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
	case tabDormant:
		ui.Column(c).Fill().Padding(16).Gap(12).Children(func() {
			if view.problem != "" {
				// A failed restore reports its cause and keeps the entry: Try
				// again reopens the same Tab, Back leaves it in the list.
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, view.problem, func() {
					if kit.GhostButton(c, icons.RefreshCw, "Try again").Clicked() && retry != nil {
						retry()
					}
				})
			} else {
				kit.EmptyHint(c, "Restoring "+view.label+"…")
				// The open runs off the UI thread; poll until the outcome
				// lands, as a fresh open's connecting page does.
				c.After(15 * time.Millisecond)
			}
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
			// The bar goes above the keyboard, inside the safe area the
			// window root already applies, and the terminal takes what is left.
			accessoryBar(c, view.accessory, view.screen)
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
	case tabHibernated, tabDormant:
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
	case tabDormant:
		return "Dormant · " + identity
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

// restoreProblem turns a failed restore of a dormant entry into an actionable
// sentence. The entry is kept, so the user's next step is the same Tab again.
func restoreProblem(label string, err error) string {
	reason := strings.TrimSpace(err.Error())
	if reason == "" {
		reason = "the Session could not be reopened"
	}
	return fmt.Sprintf("Could not restore %s: %s. Check the Host and network, then try again.", label, reason)
}
