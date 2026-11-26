package phoneshell

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
)

// fakeNotificationSessionIDs binds a fake handle to the application SessionID
// a notification hook reports. The fake handle owns no real Session, so the
// mapping stands in for phoneSessionHandle.SessionID in the app package.
var fakeNotificationSessionIDs sync.Map

func bindNotificationSession(handle *fakeHandle, id model.SessionID) {
	fakeNotificationSessionIDs.Store(handle, id)
}

// SessionID makes the fake handle a SessionIdentity, the seam the shell uses to
// resolve a notification's runtime SessionID to its Tab and Pane.
func (h *fakeHandle) SessionID() model.SessionID {
	value, ok := fakeNotificationSessionIDs.Load(h)
	if !ok {
		return ""
	}
	id, _ := value.(model.SessionID)
	return id
}

// TestNotificationClickShowsTheOwningTabAndPane covers the foreground click: a
// notification for one Pane of a split Tab selects that Tab and focuses the
// Pane, so the phone's one live terminal view moves there.
func TestNotificationClickShowsTheOwningTabAndPane(t *testing.T) {
	opener := &fakeOpener{}
	opener.queueOutput("prod output\r\n", "console output\r\n")
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	// The production root calls Ready after Load and Show; the click gestures
	// below are post-startup clicks.
	fixture.shell.Ready()

	prod := fixture.openSession(t, "prod box")
	fixture.addPaneToTab(t, "prod box", "console")
	console := fixture.openerHandle(1)
	bindNotificationSession(prod, "session-prod")
	bindNotificationSession(console, "session-console")

	route, ok := fixture.shell.RouteForSession("session-prod")
	if !ok {
		t.Fatal("the shell did not resolve the Pane's SessionID")
	}
	if route.Tab != "tab-1" || route.Pane != "pane-tab-1" {
		t.Fatalf("route = %+v, want tab-1/pane-tab-1", route)
	}
	if _, ok := fixture.shell.RouteForSession("session-unknown"); ok {
		t.Fatal("an unowned SessionID resolved to a route")
	}

	// The second Pane has focus; the click must move it back to the first.
	fixture.backToSessionsRoot(t)
	fixture.await(t, "the background Tab", func() bool {
		return !prod.isAttached() && !console.isAttached()
	})
	if !fixture.shell.ShowSessionRoute(route) {
		t.Fatal("ShowSessionRoute rejected a live route")
	}
	fixture.settle()
	fixture.await(t, "the notified Pane", func() bool {
		return prod.isAttached() && !console.isAttached()
	})

	if fixture.shell.Selected() != Sessions {
		t.Fatalf("selected section = %v, want Sessions", fixture.shell.Selected())
	}
	if got := fixture.shell.Router(Sessions).Path(); got != "/session/tab-1" {
		t.Fatalf("route path = %q, want /session/tab-1", got)
	}
	fixture.shell.sessions.mu.Lock()
	focus := fixture.shell.sessions.tabs[0].focus
	fixture.shell.sessions.mu.Unlock()
	if focus != route.Pane {
		t.Fatalf("focused Pane = %q, want %q", focus, route.Pane)
	}
}

// TestNotificationClickBeforeReadyLandsOnRestoredPane covers the click that
// launched the process after the system ended it. The click arrives before Load
// and Show, so the shell buffers it; Ready applies it after the workspace
// restore recreated the persisted Tab and Pane.
func TestNotificationClickBeforeReadyLandsOnRestoredPane(t *testing.T) {
	hosts := phoneHosts()
	opener := &fakeOpener{output: "restored\r\n"}
	store := &fakeSessionStore{payload: storedTabs(hosts[0])}
	shell := New(
		WithHosts(&fakeHostStore{hosts: hosts}),
		WithSessionOpener(opener),
		WithScreenFactory(fakeScreens),
		WithSessionStore(store),
	)

	route := SessionRoute{Tab: "saved-tab-prod", Pane: "saved-pane-prod"}
	if !shell.ShowSessionRoute(route) {
		t.Fatal("ShowSessionRoute rejected a route before Ready")
	}
	// The buffered click must not navigate before the Tab exists.
	if got := shell.Router(Sessions).Path(); got != "/" {
		t.Fatalf("a buffered click moved the route to %q before Load", got)
	}

	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	shell.Ready()
	tester := ui.NewTester(func(c *ui.Context) {
		shell.View(shell.Selected())(c)
	}, 390, 844)
	tester.Frame()

	deadline := time.Now().Add(5 * time.Second)
	for shell.Router(Sessions).Path() != "/session/saved-tab-prod" || opener.liveViews() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the buffered click did not reach the restored Pane; path %q, live views %d",
				shell.Router(Sessions).Path(), opener.liveViews())
		}
		tester.Frame()
		tester.Advance(20 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
	if shell.Selected() != Sessions {
		t.Fatalf("selected section = %v, want Sessions", shell.Selected())
	}
	shell.sessions.mu.Lock()
	focus := shell.sessions.tabs[0].focus
	shell.sessions.mu.Unlock()
	if focus != route.Pane {
		t.Fatalf("focused Pane = %q, want %q", focus, route.Pane)
	}
}

// TestNotificationClickWhileBackgroundedResumesOnForeground covers the app in
// the background: the click updates the route but cannot attach a view while
// the app is suspended. Returning to the foreground resumes that Pane with no
// further user action.
func TestNotificationClickWhileBackgroundedResumesOnForeground(t *testing.T) {
	opener := &fakeOpener{output: "keep running\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	fixture.shell.Ready()
	handle := fixture.openSession(t, "prod box")
	bindNotificationSession(handle, "session-bg")
	route, ok := fixture.shell.RouteForSession("session-bg")
	if !ok {
		t.Fatal("the shell did not resolve the Pane's SessionID")
	}

	fixture.backToSessionsRoot(t)
	fixture.shell.Background()
	fixture.tester.Frame()
	if handle.isAttached() {
		t.Fatal("backgrounding kept the terminal view attached")
	}

	if !fixture.shell.ShowSessionRoute(route) {
		t.Fatal("ShowSessionRoute rejected a backgrounded route")
	}
	fixture.tester.Frame()
	if handle.isAttached() {
		t.Fatal("a click while backgrounded attached the terminal view")
	}

	fixture.shell.Foreground()
	fixture.await(t, "the resumed notified Pane", func() bool {
		return handle.isAttached()
	})
	if got := fixture.shell.Router(Sessions).Path(); got != "/session/tab-1" {
		t.Fatalf("route path = %q, want /session/tab-1", got)
	}
}

// TestNotificationRouteForClosedTabIsIgnored pins that a stale notification
// click does nothing instead of landing on a blank or wrong page.
func TestNotificationRouteForClosedTabIsIgnored(t *testing.T) {
	fixture := newPhoneFixture(t, phoneHosts(), &fakeOpener{})
	fixture.shell.Ready()
	if fixture.shell.ShowSessionRoute(SessionRoute{Tab: "tab-missing", Pane: "pane-missing"}) {
		t.Fatal("ShowSessionRoute accepted a route to a Tab that does not exist")
	}
	if got := fixture.shell.Router(Sessions).Path(); got != "/" {
		t.Fatalf("a stale click moved the route to %q", got)
	}
}

// TestNotificationRouteToClosedPaneShowsSurvivingTab pins the split-Tab case: a
// notification can outlive the Pane it named. The click still lands on the
// surviving Tab, but it must not resurrect the closed Session, invent a
// replacement Pane, or move focus away from the Pane that remains.
func TestNotificationRouteToClosedPaneShowsSurvivingTab(t *testing.T) {
	opener := &fakeOpener{}
	opener.queueOutput("prod output\r\n", "console output\r\n")
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	fixture.shell.Ready()

	prod := fixture.openSession(t, "prod box")
	fixture.addPaneToTab(t, "prod box", "console")

	// The user closes the second Pane after the notification was posted. The
	// first Pane survives and takes the focus.
	const closedPane = "pane-tab-1-2"
	if !fixture.shell.sessions.removePane("tab-1", closedPane) {
		t.Fatal("the fixture did not close the second Pane")
	}
	fixture.settle()

	opensBefore := opener.count()
	fixture.backToSessionsRoot(t)
	fixture.await(t, "the surviving Pane to detach", func() bool {
		return !prod.isAttached()
	})

	route := SessionRoute{Tab: "tab-1", Pane: model.PaneID(closedPane)}
	if !fixture.shell.ShowSessionRoute(route) {
		t.Fatal("ShowSessionRoute rejected a route whose Pane closed but whose Tab survives")
	}
	fixture.settle()

	if fixture.shell.Selected() != Sessions {
		t.Fatalf("selected section = %v, want Sessions", fixture.shell.Selected())
	}
	if got := fixture.shell.Router(Sessions).Path(); got != "/session/tab-1" {
		t.Fatalf("route path = %q, want /session/tab-1", got)
	}
	fixture.shell.sessions.mu.Lock()
	focus := fixture.shell.sessions.tabs[0].focus
	panes := len(fixture.shell.sessions.tabs[0].panes)
	fixture.shell.sessions.mu.Unlock()
	if focus != "pane-tab-1" {
		t.Fatalf("focused Pane = %q, want the surviving pane-tab-1", focus)
	}
	if panes != 1 {
		t.Fatalf("the Tab holds %d Panes, want the one survivor", panes)
	}
	if got := opener.count(); got != opensBefore {
		t.Fatalf("the click opened a Session: opens %d, want %d", got, opensBefore)
	}
	fixture.await(t, "the surviving Pane to attach", func() bool {
		return prod.isAttached()
	})
}
