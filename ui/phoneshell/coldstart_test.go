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

func TestColdStartListsPersistedTabsAsDormant(t *testing.T) {
	hosts := phoneHosts()
	store := &fakeSessionStore{payload: storedTabs(hosts[0], hosts[1])}
	opener := &fakeOpener{}
	fixture := newPhoneFixtureWith(t, hosts, opener, WithSessionStore(store))
	fixture.selectSessions(t)

	views := fixture.shell.sessions.list()
	if len(views) != 2 {
		t.Fatalf("cold start listed %d Tabs, want the 2 that were open", len(views))
	}
	for _, view := range views {
		if view.status != tabDormant {
			t.Errorf("restored Tab %q status = %v, want dormant", view.label, view.status)
		}
	}
	if !fixture.tester.HasText("Dormant · deploy@prod.example.com:2222") {
		t.Fatalf("the restored SSH Tab is not listed as dormant; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("Dormant · console.example.com:23 · telnet") {
		t.Fatalf("the restored Telnet Tab is not listed as dormant; texts: %q", fixture.tester.Texts())
	}
	// Restoring is explicit: a cold start opens nothing by itself.
	if got := opener.count(); got != 0 {
		t.Fatalf("cold start opened %d Session(s), want none until a Tab is tapped", got)
	}
}

func TestTappingDormantEntryRestoresItsTabAndReconnects(t *testing.T) {
	hosts := phoneHosts()
	store := &fakeSessionStore{payload: storedTabs(hosts[0])}
	opener := &fakeOpener{output: "restored output\r\n"}
	fixture := newPhoneFixtureWith(t, hosts, opener, WithSessionStore(store))
	fixture.selectSessions(t)

	if err := fixture.tester.Click("Open session prod box"); err != nil {
		t.Fatalf("tap the dormant Tab: %v", err)
	}
	fixture.settle()
	fixture.await(t, "the restored Session", func() bool {
		return opener.count() == 1 && opener.liveViews() == 1
	})
	fixture.await(t, "the restored terminal output", func() bool {
		return fixture.tester.HasText("restored output")
	})

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

	// A relaunch reads the same store: no Session is opened, but the Tab the
	// previous run had open is listed and restorable.
	secondOpener := &fakeOpener{output: "after relaunch\r\n"}
	second := newPhoneFixtureWith(t, hosts, secondOpener, WithSessionStore(store))
	second.selectSessions(t)

	if got := len(second.shell.sessions.list()); got != 1 {
		t.Fatalf("the relaunched shell listed %d Tabs, want the one that was open", got)
	}
	if !second.tester.HasText("Dormant · deploy@prod.example.com:2222") {
		t.Fatalf("the persisted Tab is not listed as dormant after relaunch; texts: %q", second.tester.Texts())
	}
	if got := secondOpener.count(); got != 0 {
		t.Fatalf("relaunch opened %d Session(s) before the user tapped", got)
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
