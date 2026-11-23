package phoneshell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	domainmodel "github.com/mslxl/natcatty/domain/model"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
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
// returned handle does not attach a terminal view: the focused Pane of the
// visible Tab attaches one, which is what keeps the phone to one live view at
// a time.
type SessionOpener interface {
	OpenHost(ctx context.Context, host domainvault.Host) (SessionHandle, error)
}

// ResumeKeyOpener is implemented by an opener that can reattach a restored
// resumable Session. The key is the opaque, non-secret locator a workspace
// descriptor carries; the credentials it names stay in the encrypted secret
// store. An opener that does not implement it still opens the Host normally.
type ResumeKeyOpener interface {
	OpenHostWithResume(ctx context.Context, host domainvault.Host, resumeKey string) (SessionHandle, error)
}

// ResumePreservingHandle is implemented by a handle that can end a Session
// while keeping the credentials a relaunch needs to reattach it. The app calls
// it when it quits; an explicit Tab close uses End, which clears them.
type ResumePreservingHandle interface {
	EndPreservingResume() error
}

// SessionStore persists the Tabs a relaunch should list. The payload is the
// same Workspace restore payload the desktop writes — the shared split tree with
// its Tab and Pane IDs, focus paths, ratios, and each Pane's Dir — so the
// application wires it to the same SQLite Workspace row. A Shell without a
// store simply forgets its Tabs at exit.
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
	// SelectionText is what the terminal's selection holds, if it has one.
	// The page puts it on the system clipboard, through MyGo's Services.
	SelectionText() (string, bool)
	// PasteData pastes the system clipboard's data through the terminal's
	// own paste path, which wraps it in a bracketed paste when the program
	// asks for one, so a multi-line paste does not run as commands.
	PasteData(d transfer.Data)
	// SetAppearance applies an Appearance setting change to the live
	// terminal without recreating the view.
	SetAppearance(a TerminalAppearance)
	// InScrollback reports whether the viewport shows the scrollback above
	// the live screen.
	InScrollback() bool
	// ToggleScrollback enters the scrollback one screen up, or returns to
	// the live screen.
	ToggleScrollback()
	// Close detaches the screen. The Session handle's Close ends the Session.
	Close() error
}

// ScreenFactory builds the terminal over a Session view with the terminal
// appearance the shared settings currently describe. The phone root uses the
// libghostty-vt screen; a test injects a fake.
type ScreenFactory func(conn io.ReadWriteCloser, appearance TerminalAppearance) (TerminalScreen, error)

// tabStatus is the lifecycle of one Pane. The list tells them apart: a Pane
// that is visible is connected and owns the phone's one live terminal view; a
// Pane that is not visible is hibernated (transport alive, view released); a
// Pane whose resume failed is reconnecting.
type tabStatus int

const (
	tabConnecting tabStatus = iota
	tabReady
	tabHibernated
	tabReconnecting
	tabFailed
	// tabDormant is a Pane a previous launch left open. It owns no connection
	// until the user taps it (or a deep link makes it visible); then it is
	// restored under the same Tab identity.
	tabDormant
	// tabEmpty is a Tab with no Panes left. It exists only in memory: closing
	// the last Pane keeps the Tab and its new-session entry, and the next
	// launch does not restore it.
	tabEmpty
)

// phonePane is one Pane of a Tab. It owns one Session: its Host, its
// connection state, and, while it is the focused Pane of the visible Tab, the
// terminal screen drawn over its view. Panes of the same Tab are independent:
// one can connect, hibernate, or end without the others.
type phonePane struct {
	// id is the Pane's stable identity. It is minted monotonically within its
	// Tab and never recycled, so a stale dial outcome cannot structurally match
	// a later Pane in the same Tab. Restored Panes keep the incoming identity
	// unless another live Pane already holds it.
	id    domainmodel.PaneID
	host  domainvault.Host
	label string
	// descriptor is the non-secret Session description written back to the
	// shared Workspace. A restored Pane keeps the desktop's descriptor,
	// including fields the phone does not interpret.
	descriptor domainworkspace.SessionDescriptor
	// dir is the working directory the Pane's terminal last reported (OSC 7),
	// or the one a restored descriptor carried. It is persisted with the split
	// tree and replayed when the Session reopens.
	dir    string
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
	// restoring marks an open that reconnects a dormant Pane from a previous
	// launch. Its failure returns the Pane to dormant with the error, rather
	// than discarding an entry the user can retry.
	restoring bool
	// reopening marks an automatic foreground reconnect of a Pane whose
	// Session no longer runs. Its failure keeps the Pane reconnecting and
	// retryable instead of dropping it from the list.
	reopening bool
	// accessory holds the bar's Ctrl and Alt latches for this Pane.
	accessory accessoryState
	// resumeKey is an EternalTerminal Pane's opaque locator for its saved
	// session credentials. The credentials themselves live only in the
	// encrypted secret store; this key is what the restore payload carries so
	// a relaunch reattaches instead of starting a second remote shell.
	resumeKey string
	// tools is this Pane's Tools surface state. It lives on the Pane, so a
	// focus switch cannot carry one Pane's overlay into another, and closing
	// the Pane discards the overlay with it.
	tools toolsState
}

// phoneTab is one Workspace Tab: the shared recursive split tree, its ordered
// Panes, and the Pane that has focus. The phone renders only the focused Pane;
// the tree is kept whole so the same Tab opens unchanged on the desktop, and a
// tablet renders its visible Panes. A Tab with no Panes is an empty Tab, kept in
// memory so its new-session entry stays where the user left it.
type phoneTab struct {
	id    string
	label string
	// number, titleNumber, autoTitle, generatedTitle, and copy are the shared
	// Tab title metadata. They round-trip unchanged even though the phone does
	// not edit them.
	number         int
	titleNumber    int
	autoTitle      bool
	generatedTitle bool
	copy           int
	// root is the shared split tree. A nil root is an empty Tab: closing the
	// last Pane leaves it in memory and it is never persisted.
	root  *domainworkspace.Node
	panes []*phonePane
	focus domainmodel.PaneID
	// nextPane is this Tab's monotonic Pane sequence. It only moves forward, so
	// a closed Pane's identity is never handed to a later Pane.
	nextPane int
}

// paneOf returns the Pane with id, or nil.
func (tab *phoneTab) paneOf(id domainmodel.PaneID) *phonePane {
	for _, pane := range tab.panes {
		if pane.id == id {
			return pane
		}
	}
	return nil
}

// paneIndex returns the index of the Pane with id, or -1.
func (tab *phoneTab) paneIndex(id domainmodel.PaneID) int {
	for i, pane := range tab.panes {
		if pane.id == id {
			return i
		}
	}
	return -1
}

// orderPanes sorts the Pane slice into the split tree's leaf order, so the
// switcher and the tree never disagree about which Pane comes first. Panes the
// tree does not mention are kept at the end rather than dropped.
func (tab *phoneTab) orderPanes() {
	if tab.root == nil {
		return
	}
	order := domainworkspace.CollectPaneIDs(tab.root)
	ordered := make([]*phonePane, 0, len(tab.panes))
	for _, id := range order {
		if pane := tab.paneOf(id); pane != nil {
			ordered = append(ordered, pane)
		}
	}
	for _, pane := range tab.panes {
		keep := true
		for _, placed := range ordered {
			if placed == pane {
				keep = false
				break
			}
		}
		if keep {
			ordered = append(ordered, pane)
		}
	}
	tab.panes = ordered
}

// removeFromTree drops one Pane from the split tree. Removing the only Pane
// leaves a nil root, which is the in-memory empty Tab.
func (tab *phoneTab) removeFromTree(id domainmodel.PaneID) {
	if tab.root == nil {
		return
	}
	if root, err := domainworkspace.RemovePane(tab.root, id); err == nil {
		tab.root = root
	}
}

// captureDir records the working directory the Pane's terminal last reported.
// It runs before a screen is detached, so a hibernated Pane keeps the directory
// its Session last announced.
func (pane *phonePane) captureDir() { pane.captureDirFrom(pane.screen) }

// captureDirFrom records one detached screen's directory before it closes.
func (pane *phonePane) captureDirFrom(screen TerminalScreen) {
	if screen == nil {
		return
	}
	reporter, ok := screen.(interface{ Dir() string })
	if !ok {
		return
	}
	dir := strings.TrimSpace(reporter.Dir())
	if dir == "" {
		return
	}
	pane.dir = dir
	pane.descriptor.Dir = dir
}

// focusedPane returns the Pane the phone renders, or nil for an empty Tab.
func (tab *phoneTab) focusedPane() *phonePane {
	if len(tab.panes) == 0 {
		return nil
	}
	if pane := tab.paneOf(tab.focus); pane != nil {
		return pane
	}
	// A focus that is not in the Tab (a filtered or closed Pane) falls back to
	// the first remaining Pane and repairs the invariant, so a later lookup by
	// focus cannot keep falling back.
	tab.focus = tab.panes[0].id
	return tab.panes[0]
}

// focusPane moves focus to id, reporting whether the Pane is in this Tab. The
// Pane that loses focus closes its Tools surface: the surface belongs to the
// focused Pane, so it never reappears on a Pane the user did not just open it
// on. The caller holds the session lock.
func (tab *phoneTab) focusPane(id domainmodel.PaneID) bool {
	if tab.paneOf(id) == nil {
		return false
	}
	if tab.focus != id {
		if previous := tab.paneOf(tab.focus); previous != nil {
			previous.tools.open = false
			previous.tools.pending = nil
		}
	}
	tab.focus = id
	return true
}

// recomputeFocus chooses the focus after the Pane at removedIndex left the
// Tab. It prefers the Pane that took the removed Pane's place, then the last
// one; an empty Tab has no focus.
func (tab *phoneTab) recomputeFocus(removedIndex int) {
	if len(tab.panes) == 0 {
		tab.focus = ""
		return
	}
	index := removedIndex
	if index < 0 {
		index = 0
	}
	if index >= len(tab.panes) {
		index = len(tab.panes) - 1
	}
	tab.focus = tab.panes[index].id
}

// prunePanes drops the Panes keep rejects and repairs the focus when the
// focused Pane was one of them. It is how a filtered Pane (a Local or serial
// Pane the phone cannot open) leaves a Tab without taking its focus hostage.
// The returned Panes are the ones removed; the caller ends them.
func (tab *phoneTab) prunePanes(keep func(*phonePane) bool) []*phonePane {
	if keep == nil {
		return nil
	}
	removed := make([]*phonePane, 0)
	kept := make([]*phonePane, 0, len(tab.panes))
	focusRemovedAt := -1
	for i, pane := range tab.panes {
		if keep(pane) {
			kept = append(kept, pane)
			continue
		}
		removed = append(removed, pane)
		if pane.id == tab.focus {
			focusRemovedAt = i
		}
	}
	if len(removed) == 0 {
		return nil
	}
	tab.panes = kept
	for _, pane := range removed {
		tab.removeFromTree(pane.id)
	}
	if focusRemovedAt >= 0 {
		tab.recomputeFocus(focusRemovedAt)
	}
	return removed
}

// live reports whether any Pane has a live or starting Session, which is what
// the close confirmation asks about.
func (tab *phoneTab) live() bool {
	for _, pane := range tab.panes {
		if pane.status == tabReady || pane.status == tabConnecting {
			return true
		}
	}
	return false
}

// listable reports whether the Tab belongs in the Sessions list. An empty Tab
// is listed so its new-session entry stays reachable; a Tab whose only Pane
// failed to open is the error page's subject, not an entry (a Tab with other
// Panes keeps its list row).
func (tab *phoneTab) listable() bool {
	if len(tab.panes) == 0 {
		return true
	}
	if len(tab.panes) == 1 {
		return tab.panes[0].status != tabFailed
	}
	return true
}

// phonePaneRestorable reports whether the phone can open a Pane's Session at
// all. Local shells run on the desktop and serial devices have no phone
// adapter, so both are filtered out; a Tab keeps the remaining Panes and
// repairs its focus. RestoreTabs applies the same rule to a persisted tree so a
// desktop layout keeps its structure around the Panes a phone can open.
func phonePaneRestorable(pane *phonePane) bool {
	return pane != nil && pane.host.IsRemote()
}

// sessionTabs owns the Sessions section's Tabs and their Panes. The page
// behind /session/{id} draws the focused Pane of one Tab.
type sessionTabs struct {
	mu        sync.Mutex
	opener    SessionOpener
	newScreen ScreenFactory
	// appearance reads the current Appearance document for a new screen and
	// for a live re-style. Nil means the shared defaults.
	appearance func() TerminalAppearance
	// settings reads the terminal policy the phone's paste path enforces.
	// Nil means no paste guard, which is the original's default.
	settings SettingsService
	// history backs the Tools surface's Command History tool. Nil means the
	// surface still opens, but that tool reports why it is unavailable.
	history CommandHistoryService
	// browser backs the Tools surface's Browser tool. Nil means the entry is
	// absent and the catalog states this build has no browser.
	browser BrowserProvider
	// notes backs the Tools surface's Notes tool. It is the same record set
	// the Vault section lists; each Pane's toolsState holds its own selection
	// and draft. Nil means the tool reports why it is unavailable.
	notes *notePages
	// window is the Sessions window a Browser's WebViews belong to. It is nil
	// until Show created the windows.
	window func() *mygo.Window
	// prompts holds the trust and credential prompts the Panes answer.
	prompts *Prompts
	// launch opens one Pane's Session off the UI thread. It is addressed by the
	// Pane's Tab as well as its identity: an identity is unique across Tabs, but
	// the Tab is what proves the open belongs to the Pane it was started for.
	launch func(tabID, paneID string)
	// show pushes one Tab's terminal page on the Sessions router.
	show func(id string)
	// close ends one Tab and leaves the page it was showing.
	close func(id string)
	// confirmClose reports whether a live Session needs confirmation before it
	// is closed. It reads the Security preference live, so the Settings toggle
	// applies to the next close.
	confirmClose func() bool
	// pendingClose is the Tab or Pane waiting for close confirmation, if any.
	pendingClose *closeRequest
	tabs         []*phoneTab
	next         int
	// nextSplit is the split-identity sequence. It only moves forward, so a
	// restored split id is never handed to a later split.
	nextSplit int
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

// closeRequest names what a pending close confirmation would end: a whole Tab
// when paneID is empty, or one Pane of a Tab.
type closeRequest struct {
	tabID  string
	paneID string
}

// errNoOpener is the failure a phone without a Session service reports when a
// Host is tapped. It names the missing capability so the error is actionable.
var errNoOpener = errors.New("this build has no Session service")

// endedProblem is shown when a Session ends on the far end: the Pane stays,
// actionable, and reopens the Host under the same Tab.
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

// paneView is one Pane's state as a frame reads it.
type paneView struct {
	id      string
	label   string
	host    domainvault.Host
	status  tabStatus
	problem string
}

// tabView is one Tab's state as a frame reads it. The status, problem, screen,
// and accessory belong to the focused Pane, which is what the phone renders.
type tabView struct {
	id      string
	label   string
	host    domainvault.Host
	status  tabStatus
	problem string
	screen  TerminalScreen
	// accessory is the page's latch state, which outlives a frame.
	accessory *accessoryState
	// tools is the focused Pane's Tools surface state, which outlives a
	// frame and belongs to that Pane alone.
	tools *toolsState
	// panes lists every Pane of the Tab, in order, and focus names the one the
	// viewport shows.
	panes []paneView
	focus string
	// empty marks a Tab with no Panes left.
	empty bool
}

// begin records a connecting Tab with one Pane and returns both. The caller
// starts the open.
func (t *sessionTabs) begin(host domainvault.Host) (*phoneTab, *phonePane) {
	t.mu.Lock()
	if t.next == 0 {
		t.next = 1
	}
	tab := &phoneTab{id: t.newTabIDLocked(), label: hostName(host)}
	pane := t.newPaneLocked(tab, host)
	t.attachPaneLocked(tab, pane)
	tab.focus = pane.id
	tab.number = len(t.tabs) + 1
	t.tabs = append(t.tabs, tab)
	t.mu.Unlock()
	// The Tab exists the moment it is opened: an app suspended mid-connect
	// still lists it after a relaunch.
	t.persist()
	return tab, pane
}

// addPane appends one Host as a new Pane of an existing Tab and focuses it.
// The caller starts the open. It reports false for a Tab that has closed.
func (t *sessionTabs) addPane(tabID string, host domainvault.Host) (*phonePane, bool) {
	t.mu.Lock()
	tab := t.find(tabID)
	if tab == nil {
		t.mu.Unlock()
		return nil, false
	}
	pane := t.newPaneLocked(tab, host)
	t.attachPaneLocked(tab, pane)
	// Moving focus to the new Pane closes the previous Pane's Tools surface,
	// exactly as the switcher's focus move does.
	tab.focusPane(pane.id)
	t.mu.Unlock()
	t.persist()
	return pane, true
}

// newPaneLocked builds a connecting Pane for tab. The caller holds t.mu.
func (t *sessionTabs) newPaneLocked(tab *phoneTab, host domainvault.Host) *phonePane {
	label := hostName(host)
	resumeKey := ""
	if protocolOf(host) == domainvault.ProtocolET {
		// A new ET Session needs a locator before it opens: the adapter saves
		// the credentials under it, and the same key must survive into the
		// next launch's restore payload.
		resumeKey = appsession.NewETResumeKey()
	}
	descriptor := descriptorOf(host, label, resumeKey)
	return &phonePane{
		id:         t.paneIDLocked(tab),
		host:       host,
		label:      label,
		descriptor: descriptor,
		dir:        descriptor.Dir,
		status:     tabConnecting,
		resumeKey:  resumeKey,
	}
}

// attachPaneLocked adds a new Pane to the Tab's split tree beside its focused
// Pane and keeps the Pane slice in tree order. The caller holds t.mu. An empty
// Tab becomes a root Pane; a Tab with a tree gains a split, so the phone-written
// tree is the same shape the desktop restores.
func (t *sessionTabs) attachPaneLocked(tab *phoneTab, pane *phonePane) {
	if tab.root == nil || len(tab.panes) == 0 {
		tab.root = domainworkspace.NewPaneNode(pane.id)
	} else {
		target := tab.focus
		if target == "" || !domainworkspace.ContainsPane(tab.root, target) {
			target = domainworkspace.CollectPaneIDs(tab.root)[0]
		}
		if root, err := domainworkspace.SplitAt(
			tab.root, target, pane.id, t.splitIDLocked(), domainworkspace.SideRight,
		); err == nil {
			tab.root = root
		}
	}
	tab.panes = append(tab.panes, pane)
	tab.orderPanes()
}

// newTabIDLocked mints the next Tab identity. Tab identities are not recycled:
// a finished open addressed to a closed Tab can never land on a new one.
func (t *sessionTabs) newTabIDLocked() string {
	if t.next == 0 {
		t.next = 1
	}
	for {
		id := fmt.Sprintf("tab-%d", t.next)
		t.next++
		if t.find(id) == nil {
			return id
		}
	}
}

// reserveTabIDLocked adopts a persisted Tab identity when this launch does not
// hold it, and otherwise mints a fresh one. The Tab sequence is bumped past a
// numeric adopted identity so a later new Tab cannot collide with it.
func (t *sessionTabs) reserveTabIDLocked(preferred domainmodel.TabID) string {
	if preferred != "" && t.find(string(preferred)) == nil {
		if n, err := strconv.Atoi(strings.TrimPrefix(string(preferred), "tab-")); err == nil {
			if n >= t.next {
				t.next = n + 1
			}
		}
		return string(preferred)
	}
	return t.newTabIDLocked()
}

// splitIDLocked mints a split identity that no open tree holds. Split
// identities only move forward.
func (t *sessionTabs) splitIDLocked() domainmodel.SplitID {
	for {
		t.nextSplit++
		id := domainmodel.SplitID(fmt.Sprintf("split-%d", t.nextSplit))
		if !t.splitExistsLocked(id) {
			return id
		}
	}
}

func (t *sessionTabs) splitExistsLocked(id domainmodel.SplitID) bool {
	var walk func(*domainworkspace.Node) bool
	walk = func(n *domainworkspace.Node) bool {
		if n == nil || n.IsPane() {
			return false
		}
		if n.SplitID == id {
			return true
		}
		return walk(n.First) || walk(n.Second)
	}
	for _, tab := range t.tabs {
		if walk(tab.root) {
			return true
		}
	}
	return false
}

// paneIDLocked mints a Pane identity that is stable and unique across every
// Tab, not only within its own. The sequence is per Tab and monotonic: a
// closed Pane's identity is never minted again, so a stale dial outcome cannot
// find a reopened Pane by (Tab, identity). A restored Pane keeps the incoming
// identity a previous launch wrote; the collision loop only moves past one that
// is already held. The first Pane keeps the earlier phone shape ("pane-"+tab
// id), so existing single-Pane records and tests read back the same way.
func (t *sessionTabs) paneIDLocked(tab *phoneTab) domainmodel.PaneID {
	base := "pane-" + tab.id
	for {
		tab.nextPane++
		id := domainmodel.PaneID(base)
		if tab.nextPane > 1 {
			id = domainmodel.PaneID(fmt.Sprintf("%s-%d", base, tab.nextPane))
		}
		if t.findPane(id) == nil {
			return id
		}
	}
}

// reservePaneSequence moves the Tab's Pane sequence past an identity a
// restored Pane holds. Without it, closing a restored Pane and adding another
// could mint the same identity, and a stale dial outcome could reach the new
// Pane.
func (tab *phoneTab) reservePaneSequence(id domainmodel.PaneID) {
	base := "pane-" + tab.id
	value := id
	if string(value) == base {
		if tab.nextPane < 1 {
			tab.nextPane = 1
		}
		return
	}
	suffix, ok := strings.CutPrefix(string(value), base+"-")
	if !ok {
		return
	}
	n, err := strconv.Atoi(suffix)
	if err != nil || n <= tab.nextPane {
		return
	}
	tab.nextPane = n
}

// uniquePaneIDLocked adopts preferred when no Tab already holds it, and
// otherwise mints a free variant. adoptRestored uses it so two incoming Panes
// (or an incoming Pane and one already adopted) can never share an identity,
// which would let a later launch resolve to the wrong Tab's Pane.
func (t *sessionTabs) uniquePaneIDLocked(preferred domainmodel.PaneID) domainmodel.PaneID {
	if preferred != "" && t.findPane(preferred) == nil {
		return preferred
	}
	base := string(preferred)
	if base == "" {
		base = "pane"
	}
	for n := 1; ; n++ {
		id := domainmodel.PaneID(fmt.Sprintf("%s-%d", base, n))
		if t.findPane(id) == nil {
			return id
		}
	}
}

// finish records the open's outcome for one Pane, resolved by both its Tab and
// its identity. The Tab is structural, not defensive: an identity collision can
// never make one Tab's open finish another Tab's Pane. A failure keeps the Pane
// only long enough to show its error on the pushed page; it is not an open
// Session and never a row in the Sessions list. A success stores the handle;
// the visible Pane's view is attached by syncVisible, on the UI thread.
//
// A Pane that already owns a handle when a new outcome lands would leak the old
// transport if it were overwritten; that handle is ended instead, outside the
// lock.
func (t *sessionTabs) finish(tabID, paneID string, handle SessionHandle, err error) {
	t.mu.Lock()
	tab := t.find(tabID)
	var pane *phonePane
	if tab != nil {
		pane = tab.paneOf(domainmodel.PaneID(paneID))
	}
	if pane == nil {
		t.mu.Unlock()
		if handle != nil {
			_ = handle.End()
		}
		return
	}
	cancel := pane.cancel
	pane.cancel = nil
	restoring, reopening := pane.restoring, pane.reopening
	pane.restoring, pane.reopening = false, false
	// Whatever handle the Pane held before this outcome must not be dropped
	// silently. The retry paths already end and clear it, so this is normally
	// nil; when it is not, it is a replaced transport that must be ended.
	stale := pane.handle
	pane.handle = nil
	if err != nil || handle == nil {
		if handle != nil {
			_ = handle.End()
		}
		if err == nil {
			err = errNoOpener
		}
		switch {
		case restoring:
			// A dormant Pane stays an entry: its failure is shown on the page
			// and the Pane remains retryable.
			pane.status = tabDormant
			pane.problem = restoreProblem(pane.label, err)
		case reopening:
			// The Session the foreground tried to reconnect is gone; keep the
			// Pane in the list so the user can still retry it.
			pane.status = tabReconnecting
			pane.problem = connectProblem(pane.label, err)
		default:
			pane.status = tabFailed
			pane.problem = connectProblem(pane.label, err)
		}
	} else {
		pane.handle = handle
		pane.status = tabReady
		pane.problem = ""
	}
	if stale == handle {
		// The same handle landed twice (a duplicate outcome): keep it, do not
		// end the Session this outcome just installed.
		stale = nil
	}
	t.mu.Unlock()
	// The dial context is only live for the open; releasing it here also
	// resolves any prompt the attempt abandoned.
	if cancel != nil {
		cancel()
	}
	if stale != nil {
		_ = stale.End()
	}
	// The outcome decides whether the Pane is an entry a relaunch lists.
	t.persist()
}

// attachCancel records the in-flight open's cancel function, resolved by the
// Pane's Tab and identity. A Pane that was removed while its open was starting
// is cancelled immediately.
func (t *sessionTabs) attachCancel(tabID, paneID string, cancel context.CancelFunc) {
	t.mu.Lock()
	tab := t.find(tabID)
	var pane *phonePane
	if tab != nil {
		pane = tab.paneOf(domainmodel.PaneID(paneID))
	}
	if pane == nil {
		t.mu.Unlock()
		cancel()
		return
	}
	previous := pane.cancel
	pane.cancel = cancel
	t.mu.Unlock()
	if previous != nil {
		previous()
	}
}

// syncVisible is the one place the lifecycle rule is applied: the focused
// Pane of the Tab visible from the current route is resumed (or attached for
// the first time), and every other Pane — other Tabs' Panes and the visible
// Tab's hidden Panes — is hibernated. It never closes a Session. Every
// navigation path — back, a section switch, a deep link, a Pane switch —
// reaches it through Shell.syncLiveView, so none of them can invent its own
// rule.
//
// Phase 1 releases every other Pane's live view before phase 2 attaches the
// visible one, so the phone never holds two terminal views at once and a Pane
// already attached never gets a second view.
func (t *sessionTabs) syncVisible(visibleTab string) {
	type suspended struct {
		screen TerminalScreen
		handle SessionHandle
		pane   *phonePane
	}
	var released []suspended

	t.mu.Lock()
	visiblePane := domainmodel.PaneID("")
	if tab := t.find(visibleTab); tab != nil {
		if pane := tab.focusedPane(); pane != nil {
			visiblePane = pane.id
		}
	}
	for _, tab := range t.tabs {
		for _, pane := range tab.panes {
			if pane.id == visiblePane || pane.status != tabReady {
				continue
			}
			released = append(released, suspended{screen: pane.screen, handle: pane.handle, pane: pane})
			pane.screen, pane.conn = nil, nil
			pane.status = tabHibernated
			pane.problem = ""
		}
	}
	t.mu.Unlock()

	// Closing the screen detaches its view. Hibernate then marks the Session
	// hibernated, keeping its transport and replay for Resume.
	for _, item := range released {
		item.pane.captureDir()
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
			if item.pane.status == tabHibernated {
				item.pane.status = tabReconnecting
				item.pane.problem = "The Session ended while it was hidden. Open the Host again to reconnect."
			}
			t.mu.Unlock()
		default:
		}
	}

	t.mu.Lock()
	tab := t.find(visibleTab)
	if tab == nil {
		t.mu.Unlock()
		return
	}
	pane := tab.focusedPane()
	if pane == nil {
		// An empty Tab renders its new-session entry; there is no view to
		// attach.
		t.mu.Unlock()
		return
	}
	resuming := false
	switch pane.status {
	case tabHibernated:
		resuming = true
	case tabDormant:
		// A Pane from a previous launch reconnects when it becomes visible,
		// under its existing identity. A failed restore keeps its problem and
		// waits for the user's explicit retry, so a frame cannot loop on it.
		if pane.problem != "" {
			t.mu.Unlock()
			return
		}
		pane.status = tabConnecting
		pane.restoring = true
		paneID := string(pane.id)
		tabID := tab.id
		launch := t.launch
		t.mu.Unlock()
		if launch != nil {
			launch(tabID, paneID)
		}
		return
	case tabReady:
		// The initial attach happens once, when the Pane first becomes
		// visible. A screen that already exists means the view is live — unless
		// the Session ended on the far end since the last frame, in which case
		// the stale screen is released and the Pane becomes retryable.
		if pane.screen != nil {
			if !sessionEnded(pane.handle) {
				t.mu.Unlock()
				return
			}
			screen := pane.screen
			pane.screen, pane.conn = nil, nil
			pane.status = tabReconnecting
			pane.problem = endedProblem
			t.mu.Unlock()
			if screen != nil {
				_ = screen.Close()
			}
			return
		}
	default:
		// Connecting, reconnecting (waiting for an explicit retry), or failed
		// Panes are not attached here.
		t.mu.Unlock()
		return
	}
	handle := pane.handle
	label := pane.label
	if handle == nil {
		t.mu.Unlock()
		return
	}
	if sessionEnded(handle) {
		// The Session ended on the far end. Do not claim a view over a dead
		// connection; leave the Pane retryable instead.
		pane.status = tabReconnecting
		pane.problem = endedProblem
		t.mu.Unlock()
		return
	}
	// A resume shows "Reconnecting…" until it succeeds; an initial attach is
	// still part of connecting.
	if resuming {
		pane.status = tabReconnecting
	} else {
		pane.status = tabConnecting
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
	if t.findPane(pane.id) != pane ||
		(pane.status != tabReconnecting && pane.status != tabConnecting) {
		// The Pane was closed while the view was being attached.
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
		t.failAttach(pane, label, resuming, err)
		return
	}
	pane.conn = conn
	t.mu.Unlock()

	screen, err := t.buildScreen(conn)

	t.mu.Lock()
	if t.findPane(pane.id) != pane ||
		(pane.status != tabReconnecting && pane.status != tabConnecting) {
		// The Pane was closed while its screen was being built.
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
		t.failAttach(pane, label, resuming, err)
		return
	}
	pane.conn = nil
	pane.screen = screen
	pane.status = tabReady
	pane.problem = ""
	t.mu.Unlock()
}

// buildScreen builds the terminal for one view. ScreenFactory runs on the UI
// thread; syncVisible is called from a frame, so that holds here too. The
// appearance is read at build time, so a terminal opened after a Settings
// change already shows it.
func (t *sessionTabs) buildScreen(conn io.ReadWriteCloser) (TerminalScreen, error) {
	if t.newScreen == nil {
		return nil, errors.New("the terminal screen is not available on this device")
	}
	return t.newScreen(conn, t.currentAppearance())
}

// currentAppearance reads the live Appearance document. The provider is
// installed by the shell from the shared preferences service, so a change is
// visible without recreating the shell.
func (t *sessionTabs) currentAppearance() TerminalAppearance {
	if t.appearance == nil {
		return DefaultTerminalAppearance()
	}
	return t.appearance()
}

// applyAppearance restyles every live terminal, as the desktop root does when
// the Appearance preference changes. Only the focused Pane of the visible Tab
// has a screen; a hibernated Pane picks the new appearance up when its view is
// rebuilt.
func (t *sessionTabs) applyAppearance() {
	appearance := t.currentAppearance()
	t.mu.Lock()
	screens := make([]TerminalScreen, 0, len(t.tabs))
	for _, tab := range t.tabs {
		for _, pane := range tab.panes {
			if pane.screen != nil {
				screens = append(screens, pane.screen)
			}
		}
	}
	t.mu.Unlock()
	for _, screen := range screens {
		screen.SetAppearance(appearance)
	}
}

// pasteNeedsConfirmation reports whether the phone's paste path must ask
// before sending text. It reads the shared terminal settings each time, so a
// Settings toggle applies to the next paste without reopening anything.
func (t *sessionTabs) pasteNeedsConfirmation(text string) bool {
	if t.settings == nil {
		return false
	}
	settings := t.settings.Terminal()
	return domainterminal.ShouldConfirmPaste(text, settings.PasteProtection, settings.PasteMinLines)
}

// failAttach records a failed attach or resume. A failed resume leaves the
// Pane reconnecting and retryable; a failed initial attach is a connection
// failure with an actionable message.
func (t *sessionTabs) failAttach(pane *phonePane, label string, resuming bool, err error) {
	t.mu.Lock()
	pane.conn = nil
	var handle SessionHandle
	if resuming {
		pane.status = tabReconnecting
		pane.problem = resumeProblem(label, err)
	} else {
		pane.status = tabFailed
		pane.problem = connectProblem(label, err)
		handle = pane.handle
		pane.handle = nil
	}
	t.mu.Unlock()
	// End outside the lock: the Session service may block.
	if handle != nil {
		_ = handle.End()
	}
}

// viewOfLocked returns one Tab's state. It never builds a screen: only
// syncVisible attaches a view, so a hidden Pane cannot claim one by accident.
func (t *sessionTabs) viewOfLocked(id string) (tabView, bool) {
	tab := t.find(id)
	if tab == nil {
		return tabView{}, false
	}
	return t.viewOfTabLocked(tab), true
}

// viewOfTabLocked snapshots one Tab for a frame. The caller holds t.mu.
func (t *sessionTabs) viewOfTabLocked(tab *phoneTab) tabView {
	view := tabView{
		id:    tab.id,
		label: tab.label,
		panes: make([]paneView, 0, len(tab.panes)),
	}
	for _, pane := range tab.panes {
		view.panes = append(view.panes, paneView{
			id: string(pane.id), label: pane.label, host: pane.host,
			status: pane.status, problem: pane.problem,
		})
	}
	focused := tab.focusedPane()
	if focused == nil {
		view.empty = true
		view.status = tabEmpty
		return view
	}
	view.label = focused.label
	view.host = focused.host
	view.status = focused.status
	view.problem = focused.problem
	view.screen = focused.screen
	view.accessory = &focused.accessory
	view.tools = &focused.tools
	view.focus = string(focused.id)
	return view
}

// retry reopens the focused Pane of a failed or reconnecting Tab under the
// same identity, so the page the user is on stays put while the connection is
// tried again.
func (t *sessionTabs) retry(id string) {
	t.mu.Lock()
	tab := t.find(id)
	if tab == nil {
		t.mu.Unlock()
		return
	}
	pane := tab.focusedPane()
	if pane == nil {
		t.mu.Unlock()
		return
	}
	screen, handle := pane.screen, pane.handle
	cancel := pane.cancel
	pane.screen, pane.conn, pane.handle, pane.cancel = nil, nil, nil, nil
	// A retry of a dormant or reconnecting Pane is a restore of an existing
	// entry: a failure must leave it in the list, not discard it.
	pane.restoring, pane.reopening = false, false
	switch pane.status {
	case tabDormant:
		pane.restoring = true
	case tabReconnecting:
		pane.reopening = true
	}
	pane.status = tabConnecting
	pane.problem = ""
	paneID := string(pane.id)
	if t.pendingClose != nil && t.pendingClose.tabID == id && t.pendingClose.paneID == paneID {
		t.pendingClose = nil
	}
	launch := t.launch
	t.mu.Unlock()
	// Close the old attempt outside the lock: the Session service may block.
	if cancel != nil {
		cancel()
	}
	pane.captureDirFrom(screen)
	if screen != nil {
		_ = screen.Close()
	}
	if handle != nil {
		_ = handle.End()
	}
	if launch != nil {
		launch(id, paneID)
	}
}

// restore starts the open of a dormant Pane under its existing identity. It is
// the deliberate retry: any earlier failure text is cleared before the new
// attempt. It does nothing for a Pane that is not dormant.
func (t *sessionTabs) restore(id string) {
	t.mu.Lock()
	tab := t.find(id)
	if tab == nil {
		t.mu.Unlock()
		return
	}
	pane := tab.focusedPane()
	if pane == nil || pane.status != tabDormant {
		t.mu.Unlock()
		return
	}
	pane.status = tabConnecting
	pane.problem = ""
	pane.restoring = true
	paneID := string(pane.id)
	launch := t.launch
	t.mu.Unlock()
	if launch != nil {
		launch(id, paneID)
	}
}

// reconnectVisible reopens the focused Pane of the visible Tab when its
// Session is gone. The foreground calls it after syncVisible, so a resume that
// failed — the transport died while the app was suspended — becomes a fresh
// connection without the user acting. A Pane that is not reconnecting is left
// alone.
func (t *sessionTabs) reconnectVisible(id string) {
	if id == "" {
		return
	}
	t.mu.Lock()
	tab := t.find(id)
	if tab == nil {
		t.mu.Unlock()
		return
	}
	pane := tab.focusedPane()
	if pane == nil || pane.status != tabReconnecting {
		t.mu.Unlock()
		return
	}
	screen, handle := pane.screen, pane.handle
	pane.screen, pane.conn, pane.handle = nil, nil, nil
	pane.status = tabConnecting
	pane.problem = ""
	pane.reopening = true
	paneID := string(pane.id)
	launch := t.launch
	t.mu.Unlock()
	// The old Session no longer runs; release it outside the lock, then open
	// the Host again under the same Tab.
	pane.captureDirFrom(screen)
	if screen != nil {
		_ = screen.Close()
	}
	if handle != nil {
		_ = handle.End()
	}
	if launch != nil {
		launch(id, paneID)
	}
}

// focusPane moves the focus of one Tab to another of its Panes. The lifecycle
// rule runs on the next frame: the old Pane hibernates and the new one resumes
// with its replayed output, without a new connection.
func (t *sessionTabs) focusPane(tabID, paneID string) {
	t.mu.Lock()
	tab := t.find(tabID)
	if tab == nil || !tab.focusPane(domainmodel.PaneID(paneID)) {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	t.persist()
}

// requestClose is the close action a Sessions list row uses. It closes the
// whole Tab, ending every Pane it holds. A live Tab asks for confirmation when
// the Security preference requires it; a hibernated Tab has no live view, so
// closing it proceeds directly.
func (t *sessionTabs) requestClose(id string) {
	t.mu.Lock()
	tab := t.find(id)
	if tab == nil {
		t.mu.Unlock()
		return
	}
	live := tab.live()
	confirm := live && t.confirmClose != nil && t.confirmClose()
	if confirm {
		t.pendingClose = &closeRequest{tabID: id}
		t.mu.Unlock()
		return
	}
	t.pendingClose = nil
	closeTab := t.close
	t.mu.Unlock()
	if closeTab != nil {
		closeTab(id)
		return
	}
	t.remove(id)
}

// requestPaneClose is the close action the terminal page's header uses. It
// closes the focused Pane only; the Tab, and its new-session entry, stay when
// the last Pane goes.
func (t *sessionTabs) requestPaneClose(tabID, paneID string) {
	t.mu.Lock()
	tab := t.find(tabID)
	if tab == nil {
		t.mu.Unlock()
		return
	}
	pane := tab.paneOf(domainmodel.PaneID(paneID))
	if pane == nil {
		t.mu.Unlock()
		return
	}
	live := pane.status == tabReady || pane.status == tabConnecting
	confirm := live && t.confirmClose != nil && t.confirmClose()
	if confirm {
		t.pendingClose = &closeRequest{tabID: tabID, paneID: paneID}
		t.mu.Unlock()
		return
	}
	t.pendingClose = nil
	t.mu.Unlock()
	t.removePane(tabID, paneID)
}

// resolvePendingClose answers the inline confirmation. Confirmed ends the
// pending Tab or Pane; cancelled leaves it open.
func (t *sessionTabs) resolvePendingClose(confirmed bool) {
	t.mu.Lock()
	request := t.pendingClose
	t.pendingClose = nil
	closeTab := t.close
	t.mu.Unlock()
	if !confirmed || request == nil {
		return
	}
	if request.paneID != "" {
		t.removePane(request.tabID, request.paneID)
		return
	}
	if closeTab != nil {
		closeTab(request.tabID)
		return
	}
	t.remove(request.tabID)
}

// pendingCloseForTab reports whether one Tab is waiting for a Tab close
// confirmation.
func (t *sessionTabs) pendingCloseForTab(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pendingClose != nil && t.pendingClose.tabID == id && t.pendingClose.paneID == ""
}

// pendingCloseForPane reports whether one Pane is waiting for a Pane close
// confirmation.
func (t *sessionTabs) pendingCloseForPane(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pendingClose != nil && t.pendingClose.paneID == id
}

// remove closes and forgets one Tab, ending every Pane it holds. Closing the
// screens detaches the views; End disconnects and ends the Sessions. All of it
// runs outside the lock: they reach the Session service, which must not stall
// a frame that is removing a Tab.
func (t *sessionTabs) remove(id string) bool {
	t.mu.Lock()
	for i, tab := range t.tabs {
		if tab.id != id {
			continue
		}
		t.tabs = append(t.tabs[:i], t.tabs[i+1:]...)
		if t.pendingClose != nil && t.pendingClose.tabID == id {
			t.pendingClose = nil
		}
		panes := tab.panes
		t.mu.Unlock()
		for _, pane := range panes {
			if pane.cancel != nil {
				pane.cancel()
			}
			if pane.screen != nil {
				_ = pane.screen.Close()
			}
			if pane.handle != nil {
				_ = pane.handle.End()
			}
			// A Browser opened from this Pane's Tools surface holds a
			// dedicated channel lease; closing the Tab must release it.
			pane.tools.closeBrowser()
		}
		// A closed Tab is no longer a Session a relaunch lists.
		t.persist()
		return true
	}
	t.mu.Unlock()
	return false
}

// removePane closes one Pane and keeps its Tab, even when it was the last
// Pane: that is what leaves an empty Tab with its new-session entry. Focus
// moves to the Pane that took its place.
func (t *sessionTabs) removePane(tabID, paneID string) bool {
	t.mu.Lock()
	tab := t.find(tabID)
	if tab == nil {
		t.mu.Unlock()
		return false
	}
	index := tab.paneIndex(domainmodel.PaneID(paneID))
	if index < 0 {
		t.mu.Unlock()
		return false
	}
	pane := tab.panes[index]
	tab.panes = append(tab.panes[:index], tab.panes[index+1:]...)
	tab.removeFromTree(pane.id)
	if tab.focus == pane.id {
		tab.recomputeFocus(index)
	}
	if tab.root != nil {
		tab.orderPanes()
	}
	if t.pendingClose != nil && t.pendingClose.paneID == paneID {
		t.pendingClose = nil
	}
	t.mu.Unlock()
	if pane.cancel != nil {
		pane.cancel()
	}
	if pane.screen != nil {
		_ = pane.screen.Close()
	}
	if pane.handle != nil {
		_ = pane.handle.End()
	}
	// The Pane's Browser, if any, held a dedicated channel lease of its own.
	pane.tools.closeBrowser()
	// An empty Tab is deliberately not persisted; payloadOf omits it.
	t.persist()
	return true
}

// discardFailed forgets a failed Pane whose open is its only surface. It is
// not a Session: leaving the error page drops it. A Pane that is live or
// hibernated is never discarded here.
func (t *sessionTabs) discardFailed(id string) {
	t.mu.Lock()
	tab := t.find(id)
	if tab == nil {
		t.mu.Unlock()
		return
	}
	pane := tab.focusedPane()
	if pane == nil || pane.status != tabFailed {
		t.mu.Unlock()
		return
	}
	paneID := string(pane.id)
	only := len(tab.panes) == 1
	t.mu.Unlock()
	if only {
		// The Tab never held a Session, so it is not an empty Tab worth
		// keeping.
		t.remove(id)
		return
	}
	t.removePane(id, paneID)
}

// closeAll ends every Pane. The app calls it on quit. The Tabs are persisted
// first: quitting is not closing a Session, so the next launch still lists
// them as dormant entries. A resumable handle keeps the credentials the next
// launch's restore needs; every other handle ends exactly as End does.
func (t *sessionTabs) closeAll() {
	t.mu.Lock()
	tabs := t.tabs
	payload := payloadOf(tabs)
	t.tabs = nil
	t.pendingClose = nil
	t.mu.Unlock()
	for _, tab := range tabs {
		for _, pane := range tab.panes {
			if pane.cancel != nil {
				pane.cancel()
			}
			if pane.screen != nil {
				_ = pane.screen.Close()
			}
			if pane.handle != nil {
				endPreservingResume(pane.handle)
			}
			pane.tools.closeBrowser()
		}
	}
	t.savePayload(payload)
}

// endPreservingResume ends a handle while keeping what a relaunch needs to
// reattach it. Handles without resume state end normally.
func endPreservingResume(handle SessionHandle) {
	if preserving, ok := handle.(ResumePreservingHandle); ok {
		_ = preserving.EndPreservingResume()
		return
	}
	_ = handle.End()
}

// adoptRestored rebuilds the Tabs a previous launch left open, keeping each
// Tab's shared split tree, ratios, focus, and Pane identities. It then starts
// one restore per restorable Pane, including Panes of other Tabs and Panes the
// route does not show; each Pane keeps its own entry, so one failure leaves the
// others running and stays retryable on its own.
//
// Each Tab resolves its Host from the freshly loaded Vault list, so a renamed
// or reconfigured Host is opened as it is now. A Host that is gone keeps the
// descriptor's identity and fails actionably when restored.
//
// A restored Tab keeps its incoming identity when this launch does not hold it;
// otherwise it is renumbered. The incoming Pane identity is reserved against
// every Pane already in this launch: the next minted identity can never reuse a
// restored one, and a restored identity can never appear twice. When a
// collision forces a new identity, the split tree is remapped with it so the
// tree and the Panes never disagree.
func (t *sessionTabs) adoptRestored(restored []RestoredTab, lookup func(string) (domainvault.Host, bool)) {
	type pendingLaunch struct{ tabID, paneID string }
	launches := make([]pendingLaunch, 0, len(restored))

	t.mu.Lock()
	for _, persisted := range restored {
		descriptors := make(map[domainmodel.PaneID]domainworkspace.SessionDescriptor, len(persisted.Panes))
		entries := make(map[domainmodel.PaneID]RestoredPane, len(persisted.Panes))
		for _, entry := range persisted.Panes {
			descriptors[entry.ID] = entry.Descriptor
			entries[entry.ID] = entry
		}
		root := restorableRoot(persisted.Root, descriptors)
		if root == nil {
			continue
		}
		tab := &phoneTab{
			id:             t.reserveTabIDLocked(persisted.ID),
			label:          persisted.Title,
			number:         persisted.Number,
			titleNumber:    persisted.TitleNumber,
			autoTitle:      persisted.AutoTitle,
			generatedTitle: persisted.GeneratedTitle,
			copy:           persisted.Copy,
			root:           root,
		}
		if tab.number <= 0 {
			tab.number = len(t.tabs) + 1
		}
		mapping := make(map[domainmodel.PaneID]domainmodel.PaneID)
		panes := make(map[domainmodel.PaneID]*phonePane, len(persisted.Panes))
		for _, id := range domainworkspace.CollectPaneIDs(root) {
			entry := entries[id]
			paneID := t.uniquePaneIDLocked(id)
			tab.reservePaneSequence(paneID)
			if paneID != id {
				mapping[id] = paneID
			}
			host, ok := lookup(string(entry.Descriptor.HostID))
			if !ok {
				host = restoredHost(entry)
			}
			label := entry.Title
			if strings.TrimSpace(label) == "" {
				label = restoredTitle(entry.Descriptor)
			}
			panes[paneID] = &phonePane{
				id:         paneID,
				host:       host,
				label:      label,
				descriptor: entry.Descriptor,
				dir:        entry.Descriptor.Dir,
				status:     tabDormant,
				restoring:  true,
				resumeKey:  entry.Descriptor.ResumeKey,
			}
		}
		tab.root = domainworkspace.RemapPanes(root, mapping)
		for _, id := range domainworkspace.CollectPaneIDs(tab.root) {
			if pane := panes[id]; pane != nil {
				tab.panes = append(tab.panes, pane)
			}
		}
		if len(tab.panes) == 0 {
			continue
		}
		focus := persisted.Focus
		if remapped, ok := mapping[focus]; ok {
			focus = remapped
		}
		if focus == "" || !domainworkspace.ContainsPane(tab.root, focus) {
			focus = domainworkspace.CollectPaneIDs(tab.root)[0]
		}
		tab.focus = focus
		if strings.TrimSpace(tab.label) == "" {
			tab.label = tab.focusedPane().label
		}
		t.tabs = append(t.tabs, tab)
		for _, pane := range tab.panes {
			launches = append(launches, pendingLaunch{tabID: tab.id, paneID: string(pane.id)})
		}
	}
	t.mu.Unlock()

	// Every restorable Pane gets its own restore, in Tab then tree order. The
	// launch runs off the UI thread; finish records each outcome by Tab and
	// identity, so a failed Pane stays dormant while the others connect.
	if t.launch == nil {
		return
	}
	for _, launch := range launches {
		t.launch(launch.tabID, launch.paneID)
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
// Workspace restore format. It writes the full split tree — Pane and Tab IDs,
// split structure and ratios, each Tab's focus path, and every Pane's Session
// descriptor including its Dir. An empty Tab (a nil root) is memory-only and is
// omitted, so it cannot come back after a relaunch.
//
// A Pane whose fresh open failed is not a Session and is removed from the saved
// tree; a dormant Pane whose restore failed stays, so its entry survives and
// remains retryable. The same payload is what the desktop reads, so a
// phone-saved Workspace opens with the same structure, focus, and ratios.
func payloadOf(tabs []*phoneTab) domainworkspace.Payload {
	payload := domainworkspace.EmptyPayload()
	for _, tab := range tabs {
		for _, pane := range tab.panes {
			// The visible Pane's screen is the only place a new directory can
			// be observed; hidden Panes keep the value captured when detached.
			pane.captureDir()
		}
		root := savedRoot(tab)
		if root == nil {
			continue
		}
		number := tab.number
		if number <= 0 {
			number = len(payload.Tabs) + 1
		}
		payload.Tabs = append(payload.Tabs, domainworkspace.RestoredTab{
			ID:             domainmodel.TabID(tab.id),
			Number:         number,
			TitleNumber:    tab.titleNumber,
			Title:          tab.label,
			AutoTitle:      tab.autoTitle,
			GeneratedTitle: tab.generatedTitle,
			Copy:           tab.copy,
			Root:           root,
			FocusPath:      domainworkspace.FocusPath(root, tab.focus),
		})
		for _, id := range domainworkspace.CollectPaneIDs(root) {
			pane := tab.paneOf(id)
			if pane == nil {
				continue
			}
			descriptor := pane.descriptor
			descriptor.Dir = pane.dir
			payload.Panes = append(payload.Panes, domainworkspace.RestoredPane{
				ID:      id,
				Session: descriptor,
			})
		}
	}
	if len(payload.Tabs) == 0 {
		payload.ActiveTab = -1
	} else {
		payload.ActiveTab = 0
	}
	return payload
}

// savedRoot copies one Tab's split tree with the Panes that are not Sessions
// pruned out. It returns nil when nothing is left, which omits the Tab from the
// payload instead of persisting an empty tree.
func savedRoot(tab *phoneTab) *domainworkspace.Node {
	if tab.root == nil {
		return nil
	}
	root := domainworkspace.Clone(tab.root)
	if root == nil {
		return nil
	}
	for _, id := range domainworkspace.CollectPaneIDs(root) {
		pane := tab.paneOf(id)
		if pane != nil && pane.status != tabFailed && pane.descriptor.Valid() {
			continue
		}
		if pruned, err := domainworkspace.RemovePane(root, id); err == nil {
			root = pruned
		}
	}
	if root == nil || len(domainworkspace.CollectPaneIDs(root)) == 0 {
		return nil
	}
	return root
}

// descriptorOf describes one open phone Pane for restore. It captures no
// credential or output, exactly as the desktop descriptor does. resumeKey, when
// set, is the opaque locator an EternalTerminal Session's saved credentials
// answer to.
func descriptorOf(host domainvault.Host, label, resumeKey string) domainworkspace.SessionDescriptor {
	return domainworkspace.SessionDescriptor{
		Kind:      sessionKindOf(host),
		Label:     label,
		HostID:    host.ID,
		Hostname:  host.Hostname,
		Port:      host.Port,
		Username:  host.Username,
		ResumeKey: resumeKey,
	}
}

// restoredHost rebuilds the endpoint of a Host that is no longer in the
// Vault. The restore still fails actionably through the Session service, and
// the row keeps enough identity to tell the user what could not be opened.
func restoredHost(pane RestoredPane) domainvault.Host {
	descriptor := pane.Descriptor
	return domainvault.Host{
		ID:       descriptor.HostID,
		Label:    firstNonEmpty(descriptor.CustomName, descriptor.Label, pane.Title),
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
// hibernated one and an empty one. A Tab whose only Pane failed to open is the
// error page's subject, not an entry.
func (t *sessionTabs) list() []tabView {
	t.mu.Lock()
	defer t.mu.Unlock()
	views := make([]tabView, 0, len(t.tabs))
	for _, tab := range t.tabs {
		if !tab.listable() {
			continue
		}
		views = append(views, t.viewOfTabLocked(tab))
	}
	return views
}

// tabLabel returns one Tab's own label, or "" when it has closed. The new-pane
// picker uses it to name the Tab it is adding to.
func (t *sessionTabs) tabLabel(id string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	tab := t.find(id)
	if tab == nil {
		return ""
	}
	return tab.label
}

func (t *sessionTabs) find(id string) *phoneTab {
	for _, tab := range t.tabs {
		if tab.id == id {
			return tab
		}
	}
	return nil
}

// findPane returns the Pane with id across every Tab. The caller holds t.mu.
func (t *sessionTabs) findPane(id domainmodel.PaneID) *phonePane {
	for _, tab := range t.tabs {
		if pane := tab.paneOf(id); pane != nil {
			return pane
		}
	}
	return nil
}

// tabForHost returns the identity of the Tab holding an open Pane for one
// saved Host and moves that Tab's focus to the Pane. A failed open is not an
// open Session, so it is skipped: a link for the same Host starts a fresh
// attempt instead of selecting an error page. The caller uses this to select
// an existing Tab rather than stacking a duplicate.
func (t *sessionTabs) tabForHost(id domainmodel.HostID) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, tab := range t.tabs {
		for _, pane := range tab.panes {
			if pane.status == tabFailed {
				continue
			}
			if pane.host.ID == id {
				tab.focusPane(pane.id)
				return tab.id, true
			}
		}
	}
	return "", false
}

// drawRoot lists the open Tabs. Tapping one pushes its terminal page; its
// close action ends the whole Tab.
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
					// reconnects the persisted Pane, clearing an earlier
					// failure so the attempt is not blocked by stale text.
					if view.status == tabDormant {
						t.restore(view.id)
					}
				}
				t.drawTabPendingClose(c, view)
			}
		})
	})
}

// drawSession renders one Tab's page: the connecting hint, the actionable
// error, the reconnecting notice, the live focused Pane, or an empty Tab's
// new-session entry. addPane opens the Host picker that adds a Pane to this
// Tab.
func (t *sessionTabs) drawSession(c *ui.Context, id string, retry, back, addPane func()) {
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
	if view.empty {
		t.drawEmptyTab(c, view, addPane, back)
		return
	}
	// A close requested from the page asks for confirmation above it, so the
	// user keeps seeing the terminal while deciding.
	t.drawPanePendingClose(c, view)
	switch view.status {
	case tabConnecting:
		ui.Column(c).Fill().Padding(16).Gap(10).Children(func() {
			kit.EmptyHint(c, "Connecting to "+view.label+"…")
			// A host-key or credential challenge is drawn here, on the page of
			// the Pane that raised it. Rejecting or cancelling a prompt closes
			// the Pane, so no attempt is left open behind the user.
			if t.prompts != nil {
				t.prompts.HostKey.drawHostKey(c, view.focus, back)
				t.prompts.Auth.drawAuth(c, view.focus, back)
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
				// again reopens the same Pane, Back leaves it in the list.
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
			toolsOpen := view.tools != nil && view.tools.open
			header := ui.Row(c).FillWidth().Padding(6, 12).Gap(8).AlignItems(ui.Center).
				Background(c.Theme().Surface)
			header.Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, view.label).SingleLine().Bold()
					statusLine(c, view)
				})
				toolsLabel := "Tools for " + view.label
				if toolsOpen {
					toolsLabel = "Close tools for " + view.label
				}
				if kit.IconButton(c, icons.Command, toolsLabel).Clicked() && view.tools != nil {
					if toolsOpen {
						t.closeTools(view.tools)
					} else {
						t.openTools(view.tools)
					}
				}
				if kit.IconButton(c, icons.Plus, "Add pane to "+view.label).Clicked() && addPane != nil {
					addPane()
				}
				if kit.IconButton(c, icons.X, "Close session "+view.label).Clicked() {
					t.requestPaneClose(view.id, view.focus)
				}
			})
			// The Tools surface is drawn in place of the terminal, but only for
			// a Pane that is ready: a connecting Pane's host-key and credential
			// prompts own the page, and no overlay may cover or block them. The
			// terminal screen itself stays attached, so closing the surface
			// returns to the same Pane at the same terminal position.
			if toolsOpen {
				t.drawTools(c, view)
				return
			}
			// The Pane switcher is the phone's Tab-internal tabs: one chip per
			// Pane, the focused one chosen. Switching never reconnects.
			if len(view.panes) > 1 {
				t.drawPaneSwitcher(c, view)
			}
			// The bar goes above the keyboard, inside the safe area the
			// window root already applies, and the terminal takes what is left.
			accessoryBar(c, view.accessory, view.screen, t.pasteNeedsConfirmation)
			view.screen.View(c).Fill()
		})
	}
}

// drawPaneSwitcher renders one Tab's Panes as a segmented control and moves
// focus on a tap.
func (t *sessionTabs) drawPaneSwitcher(c *ui.Context, view tabView) {
	chosen := 0
	segments := make([]kit.Segment, len(view.panes))
	for i, pane := range view.panes {
		if pane.id == view.focus {
			chosen = i
		}
		segments[i] = kit.Segment{
			Key:   "pane-switch-" + pane.id,
			Label: "Show " + pane.label,
		}
	}
	index := chosen
	if kit.Segmented(c, kit.SegmentedText, &index, "Panes", segments) &&
		index >= 0 && index < len(view.panes) {
		t.focusPane(view.id, view.panes[index].id)
	}
}

// drawEmptyTab renders a Tab whose last Pane closed. It keeps the Tab in
// place and offers the new-session entry that opens a Host as its next Pane.
func (t *sessionTabs) drawEmptyTab(c *ui.Context, view tabView, addPane, back func()) {
	ui.Column(c).Fill().Padding(16).Gap(12).Children(func() {
		kit.EmptyHint(c, "This Tab is empty.")
		if kit.PrimaryButton(c, icons.Plus, "New session").Clicked() && addPane != nil {
			addPane()
		}
		if kit.GhostButton(c, icons.X, "Back to Sessions").Clicked() && back != nil {
			back()
		}
	})
}

// drawTabPendingClose shows the inline confirmation for a whole Tab, if it is
// the Tab waiting for one.
func (t *sessionTabs) drawTabPendingClose(c *ui.Context, view tabView) {
	if !t.pendingCloseForTab(view.id) {
		return
	}
	t.drawCloseConfirm(c, view.label)
}

// drawPanePendingClose shows the inline confirmation for the focused Pane, if
// it is the Pane waiting for one.
func (t *sessionTabs) drawPanePendingClose(c *ui.Context, view tabView) {
	if !t.pendingCloseForPane(view.focus) {
		return
	}
	t.drawCloseConfirm(c, view.label)
}

// drawCloseConfirm renders the shared close confirmation. ConfirmBar reports
// both buttons this frame.
func (t *sessionTabs) drawCloseConfirm(c *ui.Context, label string) {
	cancelled, confirmed := kit.ConfirmBar(c,
		"Close "+label+"?",
		func() {
			ui.Text(c, "The Session will disconnect.").FontSize(12).TextColor(c.Theme().TextMuted)
		},
		"Keep open", "Close")
	switch {
	case cancelled:
		t.resolvePendingClose(false)
	case confirmed:
		t.resolvePendingClose(true)
	}
}

// statusLine is the second line of a Sessions row: the lifecycle state and the
// endpoint, so two Sessions for the same Host label can still be told apart.
func statusLine(c *ui.Context, view tabView) {
	color := c.Theme().Accent
	switch view.status {
	case tabHibernated, tabDormant, tabEmpty:
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
	if view.status == tabEmpty {
		return "Empty · new session"
	}
	identity := tabIdentity(view.host)
	text := ""
	switch view.status {
	case tabConnecting:
		text = "Connecting… · " + identity
	case tabHibernated:
		text = "Hibernated · " + identity
	case tabDormant:
		text = "Dormant · " + identity
	case tabReconnecting:
		text = "Reconnecting… · " + identity
	case tabFailed:
		text = "Connection failed · " + identity
	default:
		text = "Connected · " + identity
	}
	if len(view.panes) > 1 {
		text += fmt.Sprintf(" · %d panes", len(view.panes))
	}
	return text
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
// sentence. The entry is kept, so the user's next step is the same Pane again.
func restoreProblem(label string, err error) string {
	reason := strings.TrimSpace(err.Error())
	if reason == "" {
		reason = "the Session could not be reopened"
	}
	return fmt.Sprintf("Could not restore %s: %s. Check the Host and network, then try again.", label, reason)
}
