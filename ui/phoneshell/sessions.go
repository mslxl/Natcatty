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

// SessionHandle is one live terminal Session the phone can read, write, and
// resize. It is what the Session service's attached view provides; the phone
// UI never sees a transport type.
type SessionHandle interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	// Done closes when the Session ends, whether by a remote exit or a local
	// close.
	Done() <-chan struct{}
}

// SessionOpener opens a saved Host as a Session and attaches the terminal
// view the phone draws. The application wires it to the same Session service
// the desktop uses.
type SessionOpener interface {
	OpenHost(ctx context.Context, host domainvault.Host) (SessionHandle, error)
}

// TerminalScreen is one live terminal attached to a Session handle. It is an
// interface so a test can drive output and typing without the native
// libghostty-vt library, which ordinary `go test` runs do not load.
type TerminalScreen interface {
	// View draws the terminal for the current frame.
	View(c *ui.Context) ui.Element
	// Close detaches the screen. The Session handle's Close ends the Session.
	Close() error
}

// ScreenFactory builds the terminal over a Session handle. The phone root uses
// the libghostty-vt screen; a test injects a fake.
type ScreenFactory func(conn io.ReadWriteCloser) (TerminalScreen, error)

// tabStatus is the lifecycle of one Sessions Tab.
type tabStatus int

const (
	tabConnecting tabStatus = iota
	tabReady
	tabFailed
)

// phoneTab is one open Session Tab: its Host, its connection state, and, once
// connected, the terminal screen drawn over its handle.
type phoneTab struct {
	id     string
	host   domainvault.Host
	label  string
	status tabStatus
	// problem is the actionable failure text shown when status is tabFailed.
	problem string
	handle  SessionHandle
	screen  TerminalScreen
}

// sessionTabs owns the Sessions section's Tabs. Each Tab is the app's one
// live Session; the page behind /session/{id} draws it.
type sessionTabs struct {
	mu        sync.Mutex
	opener    SessionOpener
	newScreen ScreenFactory
	launch    func(*phoneTab)
	// show pushes one Tab's terminal page on the Sessions router.
	show func(id string)
	// close ends one Tab and leaves the page it was showing.
	close func(id string)
	tabs  []*phoneTab
	next  int
}

// errNoOpener is the failure a phone without a Session service reports when a
// Host is tapped. It names the missing capability so the error is actionable.
var errNoOpener = errors.New("this build has no Session service")

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
// row in the Sessions list. A success stores the handle; the terminal screen
// is built on the next frame, on the UI thread.
func (t *sessionTabs) finish(id string, handle SessionHandle, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tab := t.find(id)
	if tab == nil {
		if handle != nil {
			_ = handle.Close()
		}
		return
	}
	if err != nil || handle == nil {
		if handle != nil {
			_ = handle.Close()
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

// viewOf returns one Tab's state, building its terminal screen the first time
// a frame shows a ready Tab. Building here is what keeps ScreenFactory on the
// main thread.
func (t *sessionTabs) viewOf(id string) (tabView, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.viewOfLocked(id)
}

// tabStatusOf reports one Tab's status without building its screen, for the
// list rows.
func (t *sessionTabs) tabStatusOf(id string) (tabStatus, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tab := t.find(id)
	if tab == nil {
		return tabFailed, false
	}
	return tab.status, true
}

// retry reopens a failed Tab under the same identity, so the page the user is
// on stays put while the connection is tried again.
func (t *sessionTabs) retry(id string) {
	t.mu.Lock()
	tab := t.find(id)
	if tab == nil {
		t.mu.Unlock()
		return
	}
	screen, handle := tab.screen, tab.handle
	tab.screen, tab.handle = nil, nil
	tab.status = tabConnecting
	tab.problem = ""
	launch := t.launch
	t.mu.Unlock()
	// Close the old attempt outside the lock: the Session service may block.
	if screen != nil {
		_ = screen.Close()
	}
	if handle != nil {
		_ = handle.Close()
	}
	if launch != nil {
		launch(tab)
	}
}

// remove closes and forgets one Tab. Closing the handle detaches the view and
// ends the Session; closing the screen first releases the terminal. Both run
// outside the lock: they reach the Session service, which must not stall a
// frame that is removing a Tab.
func (t *sessionTabs) remove(id string) bool {
	t.mu.Lock()
	for i, tab := range t.tabs {
		if tab.id != id {
			continue
		}
		t.tabs = append(t.tabs[:i], t.tabs[i+1:]...)
		t.mu.Unlock()
		if tab.screen != nil {
			_ = tab.screen.Close()
		}
		if tab.handle != nil {
			_ = tab.handle.Close()
		}
		return true
	}
	t.mu.Unlock()
	return false
}

// closeAll ends every Tab. The app calls it on quit.
func (t *sessionTabs) closeAll() {
	t.mu.Lock()
	tabs := t.tabs
	t.tabs = nil
	t.mu.Unlock()
	for _, tab := range tabs {
		if tab.screen != nil {
			_ = tab.screen.Close()
		}
		if tab.handle != nil {
			_ = tab.handle.Close()
		}
	}
}

// list returns the Tabs a list row should show: every open Tab, plus a
// connecting one. A failed Tab is the error page's subject, not an entry.
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
						ui.Text(c, tabStatusText(view)).FontSize(12).
							TextColor(c.Theme().TextMuted).SingleLine()
					})
					if kit.IconButton(c, icons.X, "Close session "+view.label).Clicked() {
						t.remove(view.id)
						return
					}
				})
				if row.Clicked() {
					if t.show != nil {
						t.show(view.id)
					}
				}
			}
		})
	})
}

// drawSession renders one Tab's page: the connecting hint, the actionable
// error, or the live terminal.
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
	switch view.status {
	case tabConnecting:
		ui.Column(c).Fill().Padding(16).Gap(10).Children(func() {
			kit.EmptyHint(c, "Connecting to "+view.label+"…")
			// The open runs off the UI thread; poll until its outcome lands.
			c.After(15 * time.Millisecond)
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
				ui.Text(c, view.label).Grow(1).MinWidth(0).SingleLine().Bold()
				if kit.IconButton(c, icons.X, "Close session "+view.label).Clicked() {
					if closeTab := t.close; closeTab != nil {
						closeTab(view.id)
					}
				}
			})
			view.screen.View(c).Fill()
		})
	}
}

// viewOfLocked is viewOf with the lock already held.
func (t *sessionTabs) viewOfLocked(id string) (tabView, bool) {
	tab := t.find(id)
	if tab == nil {
		return tabView{}, false
	}
	if tab.status == tabReady && tab.screen == nil && tab.handle != nil {
		if t.newScreen == nil {
			tab.status = tabFailed
			tab.problem = connectProblem(tab.label,
				errors.New("the terminal screen is not available on this device"))
		} else if screen, err := t.newScreen(tab.handle); err != nil {
			_ = tab.handle.Close()
			tab.handle = nil
			tab.status = tabFailed
			tab.problem = connectProblem(tab.label, err)
		} else {
			tab.screen = screen
		}
	}
	return tabView{
		id: tab.id, label: tab.label, host: tab.host,
		status: tab.status, problem: tab.problem, screen: tab.screen,
	}, true
}

// tabStatusText is the second line of a Sessions row.
func tabStatusText(view tabView) string {
	switch view.status {
	case tabConnecting:
		return "Connecting…"
	case tabFailed:
		return "Connection failed"
	}
	parts := []string{"Connected"}
	if protocol := protocolOf(view.host); protocol != domainvault.ProtocolSSH {
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
