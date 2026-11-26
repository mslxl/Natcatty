package phoneshell

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// fakeSessionStore is the phone's persistence seam: the app backs it with the
// same SQLite Workspace row the desktop uses.
type fakeSessionStore struct {
	mu      sync.Mutex
	payload domainworkspace.Payload
	loadErr error
	saveErr error
	saves   int
}

func (s *fakeSessionStore) Load(context.Context) (domainworkspace.Payload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.payload, s.loadErr
}

func (s *fakeSessionStore) Save(_ context.Context, payload domainworkspace.Payload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payload = payload
	s.saves++
	return s.saveErr
}

func (s *fakeSessionStore) last() domainworkspace.Payload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.payload
}

func (s *fakeSessionStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

// storedTabs builds the restore payload a previous launch would have written:
// one flat Tab and one remote Pane per Host.
func storedTabs(hosts ...domainvault.Host) domainworkspace.Payload {
	payload := domainworkspace.EmptyPayload()
	for i, host := range hosts {
		pane := model.PaneID("saved-pane-" + string(host.ID))
		payload.Tabs = append(payload.Tabs, domainworkspace.RestoredTab{
			ID:     model.TabID("saved-tab-" + string(host.ID)),
			Number: i + 1,
			Title:  hostName(host),
			Root:   domainworkspace.NewPaneNode(pane),
		})
		payload.Panes = append(payload.Panes, domainworkspace.RestoredPane{
			ID: pane,
			Session: domainworkspace.SessionDescriptor{
				Kind:     domainworkspace.SessionSSH,
				Label:    hostName(host),
				HostID:   host.ID,
				Hostname: host.Hostname,
				Port:     host.Port,
				Username: host.Username,
			},
		})
	}
	return payload
}

// selectSessions draws the Sessions root, where dormant entries are listed.
func (f *phoneFixture) selectSessions(t *testing.T) {
	t.Helper()
	if f.shell.Selected() != Sessions {
		f.shell.Select(Sessions)
	}
	f.tester.Frame()
}

func TestColdStartRestoresEveryPersistedPane(t *testing.T) {
	hosts := phoneHosts()
	store := &fakeSessionStore{payload: storedTabs(hosts[0], hosts[1])}
	opener := &fakeOpener{}
	fixture := newPhoneFixtureWith(t, hosts, opener, WithSessionStore(store))
	fixture.selectSessions(t)

	// A cold start starts one restore per restorable Pane, including Tabs the
	// route does not show; with the Sessions list visible they stay hibernated.
	fixture.await(t, "both persisted Panes to restore", func() bool {
		views := fixture.shell.sessions.list()
		return opener.count() == 2 && len(views) == 2 &&
			views[0].status == tabHibernated && views[1].status == tabHibernated
	})
	if got := opener.liveViews(); got != 0 {
		t.Fatalf("%d live views on the Sessions list, want none", got)
	}
	if !fixture.tester.HasText("Hibernated · deploy@prod.example.com:2222") {
		t.Fatalf("the restored SSH Tab is not listed; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("Hibernated · console.example.com:23 · telnet") {
		t.Fatalf("the restored Telnet Tab is not listed; texts: %q", fixture.tester.Texts())
	}
}

func TestTappingARestoredTabShowsTheAlreadyRestoredSession(t *testing.T) {
	hosts := phoneHosts()
	store := &fakeSessionStore{payload: storedTabs(hosts[0])}
	opener := &fakeOpener{output: "restored output\r\n"}
	fixture := newPhoneFixtureWith(t, hosts, opener, WithSessionStore(store))
	fixture.selectSessions(t)

	// The cold start already reopened the persisted Pane; tapping the Tab only
	// shows it, without dialing again.
	fixture.await(t, "the restored Session", func() bool {
		return opener.count() == 1
	})
	if err := fixture.tester.Click("Open session prod box"); err != nil {
		t.Fatalf("tap the restored Tab: %v", err)
	}
	fixture.settle()
	fixture.await(t, "the restored terminal output", func() bool {
		return opener.liveViews() == 1 && fixture.tester.HasText("restored output")
	})
	if got := opener.count(); got != 1 {
		t.Fatalf("opening the restored Tab dialed again; opens = %d, want 1", got)
	}

	// The restore reconnects the persisted identity through the current Vault
	// Host, not a stale copy of it.
	opened := opener.opened()
	if len(opened) != 1 {
		t.Fatalf("restore opened %d Hosts, want 1", len(opened))
	}
	if opened[0].ID != hosts[0].ID || opened[0].Hostname != hosts[0].Hostname ||
		opened[0].Port != hosts[0].Port || opened[0].Protocol != hosts[0].Protocol {
		t.Fatalf("restore opened %+v, want the Vault Host %+v", opened[0], hosts[0])
	}
	if got := len(fixture.shell.sessions.list()); got != 1 {
		t.Fatalf("restoring duplicated the Tab: %d entries", got)
	}
	// The open Tab is what a later backgrounding persists.
	last := store.last()
	if len(last.Tabs) != 1 || len(last.Panes) != 1 {
		t.Fatalf("the restored Tab was not persisted: %+v", last)
	}
	if got := last.Panes[0].Session.HostID; got != hosts[0].ID {
		t.Fatalf("persisted HostID = %q, want %q", got, hosts[0].ID)
	}
}

func TestBackgroundingReleasesTheViewPersistsAndKeepsTheSession(t *testing.T) {
	hosts := phoneHosts()
	store := &fakeSessionStore{}
	opener := &fakeOpener{output: "keep running\r\n"}
	fixture := newPhoneFixtureWith(t, hosts, opener, WithSessionStore(store))
	handle := fixture.openSession(t, "prod box")
	fixture.await(t, "the terminal output", func() bool {
		return fixture.tester.HasText("keep running")
	})

	fixture.shell.Background()
	fixture.tester.Frame()

	// Backgrounding releases the phone's one live view but keeps the Session.
	if handle.isAttached() || opener.liveViews() != 0 {
		t.Fatal("backgrounding kept the terminal view attached")
	}
	if handle.isEnded() {
		t.Fatal("backgrounding ended the Session")
	}
	if got := fixture.shell.Router(Sessions).Path(); got != "/session/tab-1" {
		t.Fatalf("backgrounding moved the route to %q", got)
	}
	views := fixture.shell.sessions.list()
	if len(views) != 1 || views[0].status != tabHibernated {
		t.Fatalf("the backgrounded Tab is not hibernated: %+v", views)
	}

	// The Tab is persisted while its connection stays alive.
	if got := store.saveCount(); got == 0 {
		t.Fatal("backgrounding persisted nothing")
	}
	last := store.last()
	if len(last.Tabs) != 1 || len(last.Panes) != 1 {
		t.Fatalf("backgrounding persisted %d Tab(s), %d Pane(s), want one of each", len(last.Tabs), len(last.Panes))
	}
	if got := last.Panes[0].Session.HostID; got != hosts[0].ID {
		t.Fatalf("persisted HostID = %q, want %q", got, hosts[0].ID)
	}

	// Returning to the foreground resumes the same Session without reopening.
	fixture.shell.Foreground()
	fixture.await(t, "the resumed Session", func() bool {
		return handle.isAttached() && opener.count() == 1
	})
	if handle.isEnded() {
		t.Fatal("returning to the foreground ended the Session")
	}
}

func TestForegroundReconnectsWhenResumeFails(t *testing.T) {
	hosts := phoneHosts()
	opener := &fakeOpener{output: "reconnected\r\n"}
	fixture := newPhoneFixtureWith(t, hosts, opener, WithSessionStore(&fakeSessionStore{}))
	fixture.openSession(t, "prod box")

	fixture.shell.Background()
	fixture.tester.Frame()
	if got := opener.liveViews(); got != 0 {
		t.Fatalf("backgrounding left %d live view(s)", got)
	}

	// The transport died while the app was suspended. The user does nothing:
	// returning to the foreground opens the Host again under the same Tab.
	opener.last().failNextResume(errors.New("transport closed"))
	fixture.shell.Foreground()
	fixture.await(t, "the automatic reconnect", func() bool {
		return opener.count() == 2 && opener.liveViews() == 1
	})
	fixture.await(t, "the reconnected terminal", func() bool {
		return fixture.tester.HasText("reconnected")
	})
	if got := len(fixture.shell.sessions.list()); got != 1 {
		t.Fatalf("the reconnect left %d Tabs, want the original one", got)
	}
	if got := fixture.shell.Router(Sessions).Path(); got != "/session/tab-1" {
		t.Fatalf("the reconnect moved the route to %q", got)
	}
}

func TestFailedRestoreKeepsTheDormantEntryRetryable(t *testing.T) {
	hosts := phoneHosts()
	store := &fakeSessionStore{payload: storedTabs(hosts[0])}
	opener := &fakeOpener{err: errors.New("Host not found in the Vault")}
	fixture := newPhoneFixtureWith(t, hosts, opener, WithSessionStore(store))
	fixture.selectSessions(t)

	if err := fixture.tester.Click("Open session prod box"); err != nil {
		t.Fatalf("tap the dormant Tab: %v", err)
	}
	fixture.await(t, "the restore error", func() bool {
		for _, text := range fixture.tester.Texts() {
			if strings.Contains(text, "Could not restore prod box") &&
				strings.Contains(text, "Host not found in the Vault") {
				return true
			}
		}
		return false
	})

	// The failed restore is not an open Session, and it does not discard the
	// entry: the Tab is still dormant and still persisted.
	if got := opener.count(); got != 0 {
		t.Fatalf("a failed restore produced %d handle(s)", got)
	}
	views := fixture.shell.sessions.list()
	if len(views) != 1 {
		t.Fatalf("a failed restore discarded the entry: %d Tab(s)", len(views))
	}
	if views[0].status != tabDormant {
		t.Fatalf("a failed restore left the Tab %v, want dormant", views[0].status)
	}
	if got := len(store.last().Tabs); got != 1 {
		t.Fatalf("a failed restore dropped the persisted Tab: %d", got)
	}

	// Backing out still lists the entry, so it can be retried deliberately.
	fixture.settle()
	if err := fixture.tester.Click("Back to Sessions"); err != nil {
		t.Fatalf("back from the restore error: %v", err)
	}
	fixture.settle()
	if !fixture.tester.HasText("Dormant · deploy@prod.example.com:2222") {
		t.Fatalf("the retryable entry disappeared from the list; texts: %q", fixture.tester.Texts())
	}

	// The same entry retries once the cause is fixed.
	opener.setErr(nil)
	if err := fixture.tester.Click("Open session prod box"); err != nil {
		t.Fatalf("retry the dormant Tab: %v", err)
	}
	fixture.settle()
	fixture.await(t, "the retried restore", func() bool {
		return opener.count() == 1 && opener.liveViews() == 1
	})
	if got := len(fixture.shell.sessions.list()); got != 1 {
		t.Fatalf("the retried restore duplicated the Tab: %d entries", got)
	}
}

func TestRelaunchListsTabsPersistedByThePreviousRun(t *testing.T) {
	hosts := phoneHosts()
	store := &fakeSessionStore{}
	first := newPhoneFixtureWith(t, hosts, &fakeOpener{output: "before relaunch\r\n"}, WithSessionStore(store))
	first.openSession(t, "prod box")
	first.backToSessionsRoot(t)
	first.await(t, "the hibernated Session", func() bool {
		return first.tester.HasText("Hibernated · deploy@prod.example.com:2222")
	})
	first.shell.Background()
	first.tester.Frame()

	// A relaunch reads the same store: the Tab the previous run had open is
	// reopened automatically, so the Sessions list shows it hibernated.
	secondOpener := &fakeOpener{output: "after relaunch\r\n"}
	second := newPhoneFixtureWith(t, hosts, secondOpener, WithSessionStore(store))
	second.selectSessions(t)

	second.await(t, "the relaunched Pane to restore", func() bool {
		views := second.shell.sessions.list()
		return len(views) == 1 && views[0].status == tabHibernated &&
			secondOpener.count() == 1
	})
	if !second.tester.HasText("Hibernated · deploy@prod.example.com:2222") {
		t.Fatalf("the persisted Tab is not listed after relaunch; texts: %q", second.tester.Texts())
	}
}

// splitRestorePayload is one Tab with a split of two remote Panes beside a
// second Tab with another remote Pane: the cold start must restore every leaf,
// including the ones the Sessions route does not show.
func splitRestorePayload(hosts []domainvault.Host) domainworkspace.Payload {
	payload := domainworkspace.EmptyPayload()
	payload.Tabs = []domainworkspace.RestoredTab{
		{
			ID: "tab-split", Title: "prod/console",
			Root: domainworkspace.NewSplitNode("split-1", domainworkspace.Vertical, 61,
				domainworkspace.NewPaneNode("pane-prod"),
				domainworkspace.NewPaneNode("pane-console")),
			FocusPath: []int{0},
		},
		{
			ID: "tab-other", Title: "udp box",
			Root: domainworkspace.NewPaneNode("pane-udp"),
		},
	}
	for _, entry := range []struct {
		id   model.PaneID
		host domainvault.Host
	}{
		{"pane-prod", hosts[0]},
		{"pane-console", hosts[1]},
		{"pane-udp", hosts[2]},
	} {
		payload.Panes = append(payload.Panes, domainworkspace.RestoredPane{
			ID: entry.id,
			Session: domainworkspace.SessionDescriptor{
				Kind:     sessionKindOf(entry.host),
				Label:    hostName(entry.host),
				HostID:   entry.host.ID,
				Hostname: entry.host.Hostname,
				Port:     entry.host.Port,
				Username: entry.host.Username,
				Dir:      "/srv/" + string(entry.host.ID),
			},
		})
	}
	return payload
}

// TestColdStartRestoresEveryPaneInEveryTab pins the ticket's cold-start rule:
// one restore per restorable Pane, including Panes in a split Tab and Panes of
// Tabs the route does not show.
func TestColdStartRestoresEveryPaneInEveryTab(t *testing.T) {
	hosts := phoneHosts()
	store := &fakeSessionStore{payload: splitRestorePayload(hosts)}
	opener := &fakeOpener{}
	fixture := newPhoneFixtureWith(t, hosts, opener, WithSessionStore(store))
	fixture.selectSessions(t)

	fixture.await(t, "all three persisted Panes to restore", func() bool {
		fixture.shell.sessions.mu.Lock()
		defer fixture.shell.sessions.mu.Unlock()
		if opener.count() != 3 || len(fixture.shell.sessions.tabs) != 2 {
			return false
		}
		restored := 0
		for _, tab := range fixture.shell.sessions.tabs {
			for _, pane := range tab.panes {
				if pane.handle != nil && pane.status == tabHibernated {
					restored++
				}
			}
		}
		return restored == 3
	})
	if got := len(fixture.shell.sessions.tabs[0].panes); got != 2 {
		t.Fatalf("the split Tab restored %d Panes, want 2", got)
	}
	if got := len(fixture.shell.sessions.tabs[1].panes); got != 1 {
		t.Fatalf("the other Tab restored %d Panes, want 1", got)
	}
	if got := opener.liveViews(); got != 0 {
		t.Fatalf("%d live views on the Sessions list, want none", got)
	}
}

// TestFailedRestorePaneRetriesAlone keeps the per-Pane rule: one Pane's failed
// restore leaves its entry in place, the sibling Pane keeps running, and the
// failed Pane reconnects on its own retry without dialing the sibling again.
func TestFailedRestorePaneRetriesAlone(t *testing.T) {
	hosts := phoneHosts()
	store := &fakeSessionStore{payload: splitRestorePayload(hosts)}
	opener := &fakeOpener{}
	opener.setHostErr(hosts[1].ID, errors.New("console is offline"))
	fixture := newPhoneFixtureWith(t, hosts, opener, WithSessionStore(store))
	fixture.selectSessions(t)

	// The sibling restored; the failed one stayed a dormant entry with its
	// problem text. Both Panes are still in the Tab and the payload.
	fixture.await(t, "the healthy Pane and the failed entry", func() bool {
		fixture.shell.sessions.mu.Lock()
		defer fixture.shell.sessions.mu.Unlock()
		tab := fixture.shell.sessions.tabs[0]
		if len(fixture.shell.sessions.tabs) != 2 || len(tab.panes) != 2 {
			return false
		}
		return tab.panes[0].handle != nil && tab.panes[0].status == tabHibernated &&
			tab.panes[1].status == tabDormant && tab.panes[1].problem != ""
	})
	if got := len(store.last().Panes); got != 3 {
		t.Fatalf("the failed restore changed the persisted Panes: %d", got)
	}

	// Visit the Tab, switch to the failed Pane, and retry only it.
	if err := fixture.tester.Click("Open session prod box"); err != nil {
		t.Fatalf("open the split Tab: %v", err)
	}
	fixture.settle()
	fixture.await(t, "the healthy Pane's view", func() bool {
		return opener.liveViews() == 1
	})
	if err := fixture.tester.Click("Show console"); err != nil {
		t.Fatalf("focus the failed Pane: %v", err)
	}
	fixture.settle()
	if !strings.Contains(strings.Join(fixture.tester.Texts(), " "), "Could not restore console") {
		t.Fatalf("the failed Pane has no actionable error; texts: %q", fixture.tester.Texts())
	}
	opener.setHostErr(hosts[1].ID, nil)
	if err := fixture.tester.Click("Try again"); err != nil {
		t.Fatalf("retry the failed Pane: %v", err)
	}
	fixture.await(t, "the failed Pane to reconnect", func() bool {
		return opener.count() == 3 && opener.liveViews() == 1
	})
	fixture.shell.sessions.mu.Lock()
	healthy := fixture.shell.sessions.tabs[0].panes[0]
	failed := fixture.shell.sessions.tabs[0].panes[1]
	healthyHandle, failedHandle := healthy.handle, failed.handle
	fixture.shell.sessions.mu.Unlock()
	healthySession, ok := healthyHandle.(*fakeHandle)
	if !ok || healthySession.isEnded() {
		t.Fatal("the retry ended the healthy sibling's Session")
	}
	failedSession, ok := failedHandle.(*fakeHandle)
	if !ok || !failedSession.isAttached() {
		t.Fatal("the retried Pane did not attach")
	}
	if got := len(fixture.shell.sessions.tabs[0].panes); got != 2 {
		t.Fatalf("the retry changed the Tab to %d Panes", got)
	}
}

func TestClosingATabRemovesItFromPersistence(t *testing.T) {
	hosts := phoneHosts()
	store := &fakeSessionStore{}
	opener := &fakeOpener{output: "close me\r\n"}
	fixture := newPhoneFixtureWith(t, hosts, opener, WithSessionStore(store))
	handle := fixture.openSession(t, "prod box")
	fixture.backToSessionsRoot(t)
	fixture.await(t, "hibernation", func() bool { return !handle.isAttached() })

	if err := fixture.tester.Click("Close session prod box"); err != nil {
		t.Fatalf("close the Tab: %v", err)
	}
	fixture.await(t, "the close", func() bool { return handle.isEnded() })
	if got := len(store.last().Tabs); got != 0 {
		t.Fatalf("a closed Tab is still persisted: %d", got)
	}
}
