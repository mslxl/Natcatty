package phoneshell

import (
	"errors"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// addPaneToTab drives the real gesture: the terminal page's Add action opens
// the Host picker in the Sessions stack, and the chosen Host becomes another
// Pane of the same Tab.
func (f *phoneFixture) addPaneToTab(t *testing.T, tabLabel, hostLabel string) {
	t.Helper()
	if err := f.tester.Click("Add pane to " + tabLabel); err != nil {
		t.Fatalf("open the pane picker: %v", err)
	}
	f.settle()
	if err := f.tester.Click("Add " + hostLabel + " as a pane"); err != nil {
		t.Fatalf("add %q as a pane: %v", hostLabel, err)
	}
	f.settle()
}

// liveScreens counts attached terminal screens across every Pane, so a test
// can pin the one-live-view rule directly.
func (f *phoneFixture) liveScreens() int {
	f.shell.sessions.mu.Lock()
	defer f.shell.sessions.mu.Unlock()
	live := 0
	for _, tab := range f.shell.sessions.tabs {
		for _, pane := range tab.panes {
			if pane.screen != nil {
				live++
			}
		}
	}
	return live
}

// TestAddPaneSwitchFocusReplaysHiddenOutput is the ticket's core story: a Tab
// holds two independent Sessions, switching focus within the Tab moves the one
// live view, the hidden Pane keeps running, and returning replays what it
// produced without a new dial.
func TestAddPaneSwitchFocusReplaysHiddenOutput(t *testing.T) {
	opener := &fakeOpener{}
	opener.queueOutput("prod output\r\n", "console output\r\n")
	fixture := newPhoneFixture(t, phoneHosts(), opener)

	prod := fixture.openSession(t, "prod box")
	fixture.await(t, "the first Pane's output", func() bool {
		return fixture.tester.HasText("prod output")
	})

	if err := fixture.tester.Click("Add pane to prod box"); err != nil {
		t.Fatalf("open the pane picker: %v", err)
	}
	fixture.settle()
	if got := fixture.shell.Router(Sessions).Path(); got != "/session/tab-1/new-pane" {
		t.Fatalf("the pane picker is at %q, want /session/tab-1/new-pane", got)
	}
	if err := fixture.tester.Click("Add console as a pane"); err != nil {
		t.Fatalf("add the second Pane: %v", err)
	}
	fixture.settle()
	fixture.await(t, "the second Pane", func() bool {
		return opener.count() == 2 && opener.liveViews() == 1
	})
	console := opener.last()
	fixture.await(t, "the second Pane's output", func() bool {
		return fixture.tester.HasText("console output")
	})

	// One Tab still, now holding two Panes.
	fixture.shell.sessions.mu.Lock()
	if got := len(fixture.shell.sessions.tabs); got != 1 {
		fixture.shell.sessions.mu.Unlock()
		t.Fatalf("two Panes opened %d Tabs, want 1", got)
	}
	if got := len(fixture.shell.sessions.tabs[0].panes); got != 2 {
		fixture.shell.sessions.mu.Unlock()
		t.Fatalf("the Tab holds %d Panes, want 2", got)
	}
	fixture.shell.sessions.mu.Unlock()
	if got := fixture.liveScreens(); got != 1 {
		t.Fatalf("%d live screens, want the one focused Pane's", got)
	}

	// The hidden Pane keeps running: output arriving while it is not focused
	// is not drawn, and the transport was never ended.
	prod.emit("prod kept running\r\n")
	fixture.tester.Frame()
	if fixture.tester.HasText("prod kept running") {
		t.Fatal("a hidden Pane's output was drawn on the focused Pane's screen")
	}
	if prod.isEnded() {
		t.Fatal("hiding the first Pane ended its Session")
	}

	// Switching focus moves the one view and replays the hidden Pane's output
	// without dialing again. A second Attach over a live view would fail, so a
	// ready Pane also proves no second view was created.
	if err := fixture.tester.Click("Show prod box"); err != nil {
		t.Fatalf("switch to the first Pane: %v", err)
	}
	fixture.await(t, "the first Pane's replay", func() bool {
		return prod.isAttached() && !console.isAttached() &&
			opener.count() == 2 && fixture.tester.HasText("prod kept running")
	})
	if prod.isEnded() || console.isEnded() {
		t.Fatal("switching focus ended a Session")
	}
	if got := fixture.liveScreens(); got != 1 {
		t.Fatalf("%d live screens after switching, want 1", got)
	}

	// And back: the first Pane's Session stays alive and hibernated.
	if err := fixture.tester.Click("Show console"); err != nil {
		t.Fatalf("switch back to the second Pane: %v", err)
	}
	fixture.await(t, "the second Pane's return", func() bool {
		return console.isAttached() && !prod.isAttached() &&
			opener.count() == 2 && fixture.tester.HasText("console output")
	})
	if prod.isEnded() || console.isEnded() {
		t.Fatal("switching back ended a Session")
	}
	if got := fixture.liveScreens(); got != 1 {
		t.Fatalf("%d live screens after switching back, want 1", got)
	}
}

// TestClosingPaneRecomputesFocus closes a middle Pane and pins that focus moves
// to a remaining Pane, that only the closed Pane's Session ends, and that the
// Tab itself stays.
func TestClosingPaneRecomputesFocus(t *testing.T) {
	opener := &fakeOpener{}
	opener.queueOutput("prod output\r\n", "console output\r\n", "udp output\r\n")
	fixture := newPhoneFixture(t, phoneHosts(), opener)

	prod := fixture.openSession(t, "prod box")
	fixture.await(t, "the first Pane", func() bool { return fixture.tester.HasText("prod output") })
	fixture.addPaneToTab(t, "prod box", "console")
	fixture.await(t, "the second Pane", func() bool { return opener.count() == 2 })
	fixture.addPaneToTab(t, "console", "udp box")
	fixture.await(t, "the third Pane", func() bool { return opener.count() == 3 && opener.liveViews() == 1 })
	second := fixture.openerHandle(1)
	third := fixture.openerHandle(2)

	// Focus the middle Pane, then close it.
	if err := fixture.tester.Click("Show console"); err != nil {
		t.Fatalf("focus the middle Pane: %v", err)
	}
	fixture.await(t, "the middle Pane", func() bool { return second.isAttached() })
	if err := fixture.tester.Click("Close session console"); err != nil {
		t.Fatalf("close the focused Pane: %v", err)
	}
	fixture.tester.Frame()
	if err := fixture.tester.Click("Close"); err != nil {
		t.Fatalf("confirm the Pane close: %v", err)
	}
	fixture.settle()
	fixture.await(t, "the Pane to close", func() bool { return second.isEnded() })

	// Focus moved to the Pane that took its place (the third), which resumes.
	// The first Pane and the Tab survive.
	fixture.await(t, "the recomputed focus", func() bool {
		return third.isAttached() && fixture.tester.HasText("udp output")
	})
	if prod.isEnded() {
		t.Fatal("closing one Pane ended another Session")
	}
	fixture.shell.sessions.mu.Lock()
	tabs := len(fixture.shell.sessions.tabs)
	panes := len(fixture.shell.sessions.tabs[0].panes)
	focus := fixture.shell.sessions.tabs[0].focus
	fixture.shell.sessions.mu.Unlock()
	if tabs != 1 || panes != 2 {
		t.Fatalf("after closing one Pane the Tab holds %d Tab(s) and %d Pane(s)", tabs, panes)
	}
	if string(focus) != "pane-tab-1-3" {
		t.Fatalf("focus after closing the middle Pane = %q, want the third Pane", focus)
	}
	if got := opener.count(); got != 3 {
		t.Fatalf("closing a Pane dialed; opens = %d, want 3", got)
	}
}

// openerHandle returns the nth fake Session the opener created.
func (f *phoneFixture) openerHandle(n int) *fakeHandle {
	f.opener.mu.Lock()
	defer f.opener.mu.Unlock()
	if n < 0 || n >= len(f.opener.handles) {
		return nil
	}
	return f.opener.handles[n]
}

// TestClosingLastPaneLeavesEmptyMemoryOnlyTab pins the empty Tab: it stays with
// its new-session entry, and it never reaches the persisted payload.
func TestClosingLastPaneLeavesEmptyMemoryOnlyTab(t *testing.T) {
	store := &fakeSessionStore{}
	opener := &fakeOpener{output: "closing\r\n"}
	fixture := newPhoneFixtureWith(t, phoneHosts(), opener, WithSessionStore(store))
	handle := fixture.openSession(t, "prod box")
	fixture.await(t, "the terminal", func() bool { return fixture.tester.HasText("closing") })

	if err := fixture.tester.Click("Close session prod box"); err != nil {
		t.Fatalf("close the Pane: %v", err)
	}
	fixture.tester.Frame()
	if err := fixture.tester.Click("Close"); err != nil {
		t.Fatalf("confirm the close: %v", err)
	}
	fixture.settle()
	fixture.await(t, "the Pane to close", func() bool { return handle.isEnded() })

	if got := fixture.shell.Router(Sessions).Path(); got != "/session/tab-1" {
		t.Fatalf("after closing the last Pane, path = %q, want the empty Tab", got)
	}
	if !fixture.tester.HasText("This Tab is empty.") || !fixture.tester.HasText("New session") {
		t.Fatalf("the empty Tab lost its new-session entry; texts: %q", fixture.tester.Texts())
	}
	views := fixture.shell.sessions.list()
	if len(views) != 1 || !views[0].empty || views[0].status != tabEmpty {
		t.Fatalf("the empty Tab is not listed: %+v", views)
	}

	// The empty Tab is memory-only: backgrounding writes no Tab at all.
	fixture.shell.Background()
	fixture.tester.Frame()
	if got := len(store.last().Tabs); got != 0 {
		t.Fatalf("the empty Tab was persisted: %d Tab(s)", got)
	}
	fixture.shell.Foreground()
	fixture.tester.Frame()
	if got := len(fixture.shell.sessions.list()); got != 1 {
		t.Fatalf("the empty Tab did not survive a background cycle: %d", got)
	}
}

// TestEmptyTabNewSessionAddsPaneToTheSameTab proves the new-session entry adds
// a Pane back to the empty Tab rather than starting a second Tab.
func TestEmptyTabNewSessionAddsPaneToTheSameTab(t *testing.T) {
	opener := &fakeOpener{output: "fresh\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	first := fixture.openSession(t, "prod box")
	fixture.await(t, "the terminal", func() bool { return fixture.tester.HasText("fresh") })

	if err := fixture.tester.Click("Close session prod box"); err != nil {
		t.Fatalf("close the Pane: %v", err)
	}
	fixture.tester.Frame()
	if err := fixture.tester.Click("Close"); err != nil {
		t.Fatalf("confirm the close: %v", err)
	}
	fixture.settle()
	fixture.await(t, "the Pane to close", func() bool { return first.isEnded() })

	if err := fixture.tester.Click("New session"); err != nil {
		t.Fatalf("open the new-session entry: %v", err)
	}
	fixture.settle()
	if err := fixture.tester.Click("Add console as a pane"); err != nil {
		t.Fatalf("add the new Pane: %v", err)
	}
	fixture.settle()
	fixture.await(t, "the new Pane", func() bool {
		return opener.count() == 2 && opener.liveViews() == 1
	})
	fixture.await(t, "the new Pane's output", func() bool {
		return fixture.tester.HasText("fresh")
	})
	if got := fixture.shell.Router(Sessions).Path(); got != "/session/tab-1" {
		t.Fatalf("the new session opened at %q, want the same Tab", got)
	}
	fixture.shell.sessions.mu.Lock()
	tabs := len(fixture.shell.sessions.tabs)
	panes := len(fixture.shell.sessions.tabs[0].panes)
	fixture.shell.sessions.mu.Unlock()
	if tabs != 1 || panes != 1 {
		t.Fatalf("the new session left %d Tab(s) and %d Pane(s), want the same Tab holding 1", tabs, panes)
	}
}

// TestLegacySinglePaneRestoreKeepsPaneIdentity pins the no-migration path: a
// record written by an earlier phone build restores as one Tab with one Pane,
// and the Pane keeps the incoming identity and directory for ticket 25.
func TestLegacySinglePaneRestoreKeepsPaneIdentity(t *testing.T) {
	hosts := phoneHosts()
	payload := storedTabs(hosts[0])
	payload.Panes[0].Session.Dir = "/srv/legacy"
	store := &fakeSessionStore{payload: payload}
	opener := &fakeOpener{output: "legacy\r\n"}
	fixture := newPhoneFixtureWith(t, hosts, opener, WithSessionStore(store))
	fixture.selectSessions(t)

	if err := fixture.tester.Click("Open session prod box"); err != nil {
		t.Fatalf("restore the dormant entry: %v", err)
	}
	fixture.settle()
	fixture.await(t, "the restored Session", func() bool {
		return opener.count() == 1 && opener.liveViews() == 1 && fixture.tester.HasText("legacy")
	})

	fixture.shell.sessions.mu.Lock()
	tab := fixture.shell.sessions.tabs[0]
	pane := tab.panes[0]
	var (
		paneID = string(pane.id)
		dir    = pane.dir
		hostID = string(pane.host.ID)
		focus  = string(tab.focus)
		count  = len(tab.panes)
	)
	fixture.shell.sessions.mu.Unlock()
	if count != 1 {
		t.Fatalf("the legacy record restored %d Panes, want 1", count)
	}
	if paneID != string(payload.Panes[0].ID) {
		t.Fatalf("restored Pane id = %q, want the record's %q", paneID, payload.Panes[0].ID)
	}
	if dir != "/srv/legacy" {
		t.Fatalf("restored Pane dir = %q, want the record's", dir)
	}
	if focus != paneID {
		t.Fatalf("restored focus = %q, want the only Pane %q", focus, paneID)
	}
	if hostID != string(hosts[0].ID) {
		t.Fatalf("restored HostID = %q, want %q", hostID, hosts[0].ID)
	}
}

// TestFilteredPanesRecomputeFocus pins the filter path ticket 25 will use: a
// Local or serial Pane the phone cannot open leaves the Tab, and when it was
// focused the focus moves to a remaining Pane.
func TestFilteredPanesRecomputeFocus(t *testing.T) {
	tab := &phoneTab{
		id:    "tab-1",
		label: "ops",
		focus: "pane-local",
		panes: []*phonePane{
			{id: "pane-ssh", host: domainvault.Host{ID: "prod", Protocol: domainvault.ProtocolSSH}},
			{id: "pane-local", host: domainvault.Host{ID: "local", Protocol: domainvault.Protocol("local")}},
			{id: "pane-serial", host: domainvault.Host{ID: "serial", Protocol: domainvault.Protocol("serial")}},
		},
	}
	removed := tab.prunePanes(phonePaneRestorable)
	if len(removed) != 2 {
		t.Fatalf("pruned %d Pane(s), want the local and serial ones", len(removed))
	}
	if len(tab.panes) != 1 || tab.panes[0].id != "pane-ssh" {
		t.Fatalf("remaining Panes = %+v, want only the SSH Pane", tab.panes)
	}
	if tab.focus != "pane-ssh" {
		t.Fatalf("focus after filtering = %q, want the remaining Pane", tab.focus)
	}

	// Filtering the whole Tab leaves it empty with no focus, which the view
	// renders as the new-session entry.
	tab.prunePanes(func(*phonePane) bool { return false })
	if len(tab.panes) != 0 || tab.focus != "" {
		t.Fatalf("an emptied Tab = %d Pane(s), focus %q", len(tab.panes), tab.focus)
	}
}

// TestPaneSwitcherAppearsOnlyForMultiplePanes keeps the single-Session page
// uncluttered: the switcher is the Tab-internal tab bar, so it appears when
// there is something to switch between.
func TestPaneSwitcherAppearsOnlyForMultiplePanes(t *testing.T) {
	opener := &fakeOpener{}
	opener.queueOutput("prod output\r\n", "console output\r\n")
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	fixture.openSession(t, "prod box")
	fixture.await(t, "the terminal", func() bool { return fixture.tester.HasText("prod output") })
	if fixture.tester.HasText("Show prod box") {
		t.Fatalf("a one-Pane Tab shows a pane switcher; texts: %q", fixture.tester.Texts())
	}
	fixture.addPaneToTab(t, "prod box", "console")
	fixture.await(t, "the second Pane", func() bool { return opener.count() == 2 })
	fixture.await(t, "the switcher", func() bool {
		return fixture.tester.HasText("Show prod box") && fixture.tester.HasText("Show console")
	})
}

// TestClosingTabEndsEveryPane pins the other close gesture: the Sessions list
// row still closes the whole Tab, and with it every Pane's independent
// Session.
func TestClosingTabEndsEveryPane(t *testing.T) {
	opener := &fakeOpener{}
	opener.queueOutput("prod output\r\n", "console output\r\n")
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	prod := fixture.openSession(t, "prod box")
	fixture.await(t, "the first Pane", func() bool { return fixture.tester.HasText("prod output") })
	fixture.addPaneToTab(t, "prod box", "console")
	fixture.await(t, "the second Pane", func() bool { return opener.count() == 2 && opener.liveViews() == 1 })
	console := fixture.openerHandle(1)

	fixture.backToSessionsRoot(t)
	// A hibernated Tab closes without confirmation, ending every Pane.
	if err := fixture.tester.Click("Close session console"); err != nil {
		t.Fatalf("close the Tab: %v", err)
	}
	fixture.settle()
	fixture.await(t, "both Panes to end", func() bool {
		return prod.isEnded() && console.isEnded()
	})
	if got := len(fixture.shell.sessions.tabs); got != 0 {
		t.Fatalf("closing the Tab left %d Tab(s)", got)
	}
	if !fixture.tester.HasText("No sessions yet. Open a Host from the Hosts tab.") {
		t.Fatalf("the closed Tab is still listed; texts: %q", fixture.tester.Texts())
	}
}

// TestSwitchingFocusIsIdempotent pins that repeated frames over a focused
// multi-Pane Tab never create a second view or detach the live one.
func TestSwitchingFocusIsIdempotent(t *testing.T) {
	opener := &fakeOpener{}
	opener.queueOutput("prod output\r\n", "console output\r\n")
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	prod := fixture.openSession(t, "prod box")
	fixture.addPaneToTab(t, "prod box", "console")
	fixture.await(t, "the second Pane", func() bool { return opener.count() == 2 && opener.liveViews() == 1 })
	console := opener.last()
	for i := 0; i < 5; i++ {
		fixture.tester.Frame()
		fixture.tester.Advance(10 * time.Millisecond)
		if !console.isAttached() || prod.isAttached() {
			t.Fatalf("frame %d moved the live view (prod=%v console=%v)",
				i, prod.isAttached(), console.isAttached())
		}
		if got := fixture.liveScreens(); got != 1 {
			t.Fatalf("frame %d left %d live screens", i, got)
		}
	}
}

// gapRestorePayload is the record a launch leaves when the only surviving Tab
// was not the first: the saved Tab identity has a gap, so the Pane identity
// inside it ("pane-"+saved Tab id) does not match the Tab this launch
// renumbers it into.
func gapRestorePayload(host domainvault.Host) domainworkspace.Payload {
	payload := domainworkspace.EmptyPayload()
	pane := model.PaneID("pane-tab-2")
	payload.Tabs = append(payload.Tabs, domainworkspace.RestoredTab{
		ID:     model.TabID("tab-2"),
		Number: 2,
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
	return payload
}

// assertUniquePaneIDs pins the global identity invariant: no two Panes in the
// launch may share an identity.
func assertUniquePaneIDs(t *testing.T, fixture *phoneFixture) {
	t.Helper()
	fixture.shell.sessions.mu.Lock()
	defer fixture.shell.sessions.mu.Unlock()
	seen := make(map[model.PaneID]bool)
	for _, tab := range fixture.shell.sessions.tabs {
		for _, pane := range tab.panes {
			if seen[pane.id] {
				t.Fatalf("Pane identity %q is held by more than one Pane", pane.id)
			}
			seen[pane.id] = true
		}
	}
}

// TestPaneIDsAreNeverRecycled pins the stale-outcome guard: after a Pane
// closes, the Tab mints a new identity rather than handing the old one to its
// replacement.
func TestPaneIDsAreNeverRecycled(t *testing.T) {
	host := phoneHosts()[0]
	tabs := &sessionTabs{}
	tab, first := tabs.begin(host)
	firstID := first.id
	tabs.removePane(tab.id, string(firstID))

	second, ok := tabs.addPane(tab.id, host)
	if !ok {
		t.Fatal("the Tab rejected a Pane after its last one closed")
	}
	if second.id == firstID {
		t.Fatalf("the reopened Pane reused the closed Pane's identity %q", firstID)
	}
	if got := string(tabs.tabs[0].focus); got != string(second.id) {
		t.Fatalf("focus = %q, want the reopened Pane %q", got, second.id)
	}
}

// TestRestoredPaneIdentityIsNotRecycledAfterClose covers the same residual for
// a Pane that arrived from the persisted tree: its sequence is reserved, so a
// replacement Pane never inherits its identity and a stale outcome stays dead.
func TestRestoredPaneIdentityIsNotRecycledAfterClose(t *testing.T) {
	host := phoneHosts()[0]
	descriptor := descriptorOf(host, hostName(host), "")
	tabs := &sessionTabs{}
	tabs.adoptRestored([]RestoredTab{{
		ID: "tab-1",
		Root: domainworkspace.NewSplitNode("split-1", domainworkspace.Vertical, 50,
			domainworkspace.NewPaneNode("pane-tab-1"),
			domainworkspace.NewPaneNode("pane-tab-1-2")),
		Focus: "pane-tab-1",
		Panes: []RestoredPane{
			{ID: "pane-tab-1", Title: hostName(host), Descriptor: descriptor},
			{ID: "pane-tab-1-2", Title: hostName(host), Descriptor: descriptor},
		},
	}}, func(string) (domainvault.Host, bool) { return host, true })

	tabs.removePane("tab-1", "pane-tab-1-2")
	replacement, ok := tabs.addPane("tab-1", host)
	if !ok {
		t.Fatal("the Tab rejected a Pane after closing a restored one")
	}
	if replacement.id == "pane-tab-1-2" {
		t.Fatalf("the replacement reused the closed restored identity %q", replacement.id)
	}

	// A stale outcome for the closed restored Pane cannot reach the new one.
	stale := newFakeHandle("stale output\r\n")
	tabs.finish("tab-1", "pane-tab-1-2", stale, nil)
	if !stale.isEnded() {
		t.Fatal("a stale dial outcome kept its Session open")
	}
	tabs.mu.Lock()
	handle := replacement.handle
	tabs.mu.Unlock()
	if handle != nil {
		t.Fatal("a stale dial outcome installed a handle on the replacement Pane")
	}
}

// regression: a dial started for a Pane that has since closed must not install
// its Session on the Pane that replaced it in the same Tab. The stale handle is
// ended and the new Pane stays untouched.
func TestStaleOutcomeCannotReachAReopenedPane(t *testing.T) {
	host := phoneHosts()[0]
	tabs := &sessionTabs{}
	tab, first := tabs.begin(host)
	firstID := string(first.id)
	tabs.removePane(tab.id, firstID)
	second, ok := tabs.addPane(tab.id, host)
	if !ok {
		t.Fatal("the Tab rejected the replacement Pane")
	}

	// The removed Pane's dial finishes after its replacement is connecting.
	stale := newFakeHandle("stale output\r\n")
	tabs.finish(tab.id, firstID, stale, nil)

	if !stale.isEnded() {
		t.Fatal("a stale dial outcome kept its Session open")
	}
	tabs.mu.Lock()
	handle := second.handle
	status := second.status
	tabs.mu.Unlock()
	if handle != nil {
		t.Fatal("a stale dial outcome installed a handle on the reopened Pane")
	}
	if status != tabConnecting {
		t.Fatalf("the reopened Pane status = %v, want still connecting", status)
	}
}

// TestRestoredPaneIdentityDoesNotCollideWithANewTab is the save/restore gap
// regression: a saved Pane keeps "pane-tab-2" while this launch starts a new
// Tab, so the new Tab's first Pane must not mint the same identity. The new
// Pane has to connect to its own Host, and the restored Pane keeps its own
// identity and Session.
func TestRestoredPaneIdentityDoesNotCollideWithANewTab(t *testing.T) {
	hosts := phoneHosts()
	store := &fakeSessionStore{payload: gapRestorePayload(hosts[0])}
	opener := &fakeOpener{output: "console output\r\n"}
	opener.queueOutput("restored output\r\n", "console output\r\n")
	fixture := newPhoneFixtureWith(t, hosts, opener, WithSessionStore(store))
	fixture.selectSessions(t)

	fixture.shell.sessions.mu.Lock()
	restored := fixture.shell.sessions.tabs[0].panes[0]
	restoredID := string(restored.id)
	fixture.shell.sessions.mu.Unlock()
	if restoredID != "pane-tab-2" {
		t.Fatalf("the restored Pane id = %q, want the record's pane-tab-2", restoredID)
	}

	// Opening a Host starts a new Tab. Its Pane must get a fresh identity: the
	// restored Pane already owns pane-tab-2.
	fixture.openHostRow(t, "console")
	fixture.settle()
	fixture.await(t, "the new Pane's open to settle", func() bool {
		fixture.shell.sessions.mu.Lock()
		defer fixture.shell.sessions.mu.Unlock()
		if len(fixture.shell.sessions.tabs) != 2 {
			return false
		}
		created := fixture.shell.sessions.tabs[1].panes[0]
		old := fixture.shell.sessions.tabs[0].panes[0]
		return created.status == tabReady && created.screen != nil &&
			old.status == tabHibernated && old.handle != nil
	})

	fixture.shell.sessions.mu.Lock()
	old := fixture.shell.sessions.tabs[0].panes[0]
	created := fixture.shell.sessions.tabs[1].panes[0]
	var (
		createdID     = string(created.id)
		createdHandle = created.handle
		createdStatus = created.status
		oldID         = string(old.id)
		oldHandle     = old.handle
		oldStatus     = old.status
	)
	fixture.shell.sessions.mu.Unlock()

	if createdID == restoredID {
		t.Fatalf("the new Tab reused the restored Pane id %q", createdID)
	}
	if createdHandle == nil || createdStatus != tabReady {
		t.Fatalf("the new Pane did not connect: status=%v handle=%v",
			createdStatus, createdHandle != nil)
	}
	if oldID != restoredID || oldHandle == nil || oldStatus != tabHibernated {
		t.Fatalf("opening a new Tab disturbed the restored Pane: id=%q status=%v handle=%v",
			oldID, oldStatus, oldHandle != nil)
	}
	opened := opener.opened()
	if len(opened) != 2 {
		t.Fatalf("the launch dialed %d Sessions, want the restored and the new Host", len(opened))
	}
	if opened[0].ID != hosts[0].ID || opened[1].ID != hosts[1].ID {
		t.Fatalf("dialed Hosts %q, want the restored %q then the new %q",
			[]string{string(opened[0].ID), string(opened[1].ID)}, hosts[0].ID, hosts[1].ID)
	}
	if got := opener.liveViews(); got != 1 {
		t.Fatalf("%d live views, want the new Pane's one", got)
	}
	// findPane and launch resolve by Tab and identity: the new Pane's identity
	// must never resolve to the restored Pane.
	fixture.shell.sessions.mu.Lock()
	resolved := fixture.shell.sessions.findPane(model.PaneID(createdID))
	fixture.shell.sessions.mu.Unlock()
	if resolved != created {
		t.Fatalf("findPane(%q) returned another Tab's Pane", createdID)
	}
	assertUniquePaneIDs(t, fixture)
}

// TestRestoredAndNewPanesKeepExactlyOneLiveView visits the restored dormant Tab
// and a new Tab in turn: the phone moves its one live view between them, both
// Sessions stay alive, and the identities remain distinct.
func TestRestoredAndNewPanesKeepExactlyOneLiveView(t *testing.T) {
	hosts := phoneHosts()
	store := &fakeSessionStore{payload: gapRestorePayload(hosts[0])}
	opener := &fakeOpener{}
	opener.queueOutput("prod output\r\n", "console output\r\n")
	fixture := newPhoneFixtureWith(t, hosts, opener, WithSessionStore(store))
	fixture.selectSessions(t)

	// Restore the dormant Pane: it reconnects and owns the one live view.
	if err := fixture.tester.Click("Open session prod box"); err != nil {
		t.Fatalf("restore the dormant Pane: %v", err)
	}
	fixture.settle()
	fixture.await(t, "the restored Pane", func() bool {
		return opener.count() == 1 && opener.liveViews() == 1
	})
	restored := opener.last()
	// Return to the list, which hibernates the restored Session without ending
	// it, so the next open starts from the root again.
	fixture.backToSessionsRoot(t)
	fixture.await(t, "the restored Pane to hibernate", func() bool {
		return opener.liveViews() == 0
	})

	// A new Tab's Pane must get a fresh identity and dial its own Host.
	fixture.openHostRow(t, "console")
	fixture.settle()
	fixture.await(t, "the second Pane's open", func() bool {
		fixture.shell.sessions.mu.Lock()
		defer fixture.shell.sessions.mu.Unlock()
		if len(fixture.shell.sessions.tabs) != 2 {
			return false
		}
		pane := fixture.shell.sessions.tabs[1].panes[0]
		return pane.status == tabReady && pane.handle != nil
	})
	fixture.await(t, "the second Pane's one view", func() bool {
		return opener.liveViews() == 1 && fixture.liveScreens() == 1
	})
	console := opener.last()

	// Visiting both Tabs moves the one live view and ends neither Session.
	fixture.backToSessionsRoot(t)
	fixture.await(t, "the new Pane to hibernate", func() bool {
		return opener.liveViews() == 0
	})
	if err := fixture.tester.Click("Open session prod box"); err != nil {
		t.Fatalf("visit the restored Tab: %v", err)
	}
	fixture.settle()
	fixture.await(t, "the restored Pane resumed", func() bool {
		return restored.isAttached() && !console.isAttached() && fixture.liveScreens() == 1
	})
	fixture.backToSessionsRoot(t)
	if err := fixture.tester.Click("Open session console"); err != nil {
		t.Fatalf("visit the new Tab: %v", err)
	}
	fixture.settle()
	fixture.await(t, "the new Pane resumed", func() bool {
		return console.isAttached() && !restored.isAttached() && fixture.liveScreens() == 1
	})
	if restored.isEnded() || console.isEnded() {
		t.Fatal("visiting both Tabs ended a Session")
	}
	if got := opener.count(); got != 2 {
		t.Fatalf("visiting both Tabs dialed; opens = %d, want 2", got)
	}
	assertUniquePaneIDs(t, fixture)
}

// TestAdoptRestoredKeepsPaneIdentitiesUnique pins the restore half of the
// invariant: two records carrying the same Pane identity cannot both keep it.
func TestAdoptRestoredKeepsPaneIdentitiesUnique(t *testing.T) {
	host := phoneHosts()[0]
	descriptor := domainworkspace.SessionDescriptor{
		Kind:     domainworkspace.SessionSSH,
		Label:    hostName(host),
		HostID:   host.ID,
		Hostname: host.Hostname,
		Port:     host.Port,
		Username: host.Username,
	}
	tabs := &sessionTabs{}
	root := domainworkspace.NewPaneNode("pane-tab-2")
	tabs.adoptRestored([]RestoredTab{
		{
			ID: "tab-one", Title: "one", Root: root, Focus: "pane-tab-2",
			Panes: []RestoredPane{{ID: "pane-tab-2", Title: "one", Descriptor: descriptor}},
		},
		{
			ID: "tab-two", Title: "two", Root: root, Focus: "pane-tab-2",
			Panes: []RestoredPane{{ID: "pane-tab-2", Title: "two", Descriptor: descriptor}},
		},
	}, func(string) (domainvault.Host, bool) { return host, true })

	seen := make(map[model.PaneID]bool)
	for _, tab := range tabs.tabs {
		for _, pane := range tab.panes {
			if seen[pane.id] {
				t.Fatalf("adopted Pane identity %q twice", pane.id)
			}
			seen[pane.id] = true
		}
	}
	if len(seen) != 2 {
		t.Fatalf("adopted %d Panes, want 2", len(seen))
	}
}

// TestFinishEndsAReplacedLiveHandle pins that a successful outcome landing on a
// Pane that already owns a handle ends the old transport instead of dropping it
// on the floor.
func TestFinishEndsAReplacedLiveHandle(t *testing.T) {
	opener := &fakeOpener{output: "first\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	first := fixture.openSession(t, "prod box")
	fixture.await(t, "the live Pane", func() bool { return first.isAttached() })

	replacement := newFakeHandle("replacement\r\n")
	fixture.shell.sessions.finish("tab-1", "pane-tab-1", replacement, nil)
	if !first.isEnded() {
		t.Fatal("finish replaced a live handle without ending its transport")
	}
	fixture.shell.sessions.mu.Lock()
	pane := fixture.shell.sessions.tabs[0].panes[0]
	got := pane.handle
	fixture.shell.sessions.mu.Unlock()
	if got != replacement {
		t.Fatalf("the Pane holds %p, want the replacement %p", got, replacement)
	}
}

// TestFinishEndsAReplacedHandleOnFailure pins the failure half of the same
// rule: a failed outcome must still end a handle the Pane already held.
func TestFinishEndsAReplacedHandleOnFailure(t *testing.T) {
	opener := &fakeOpener{output: "first\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	first := fixture.openSession(t, "prod box")
	fixture.await(t, "the live Pane", func() bool { return first.isAttached() })

	fixture.shell.sessions.finish("tab-1", "pane-tab-1", nil, errors.New("dial refused"))
	if !first.isEnded() {
		t.Fatal("a failed finish dropped a live handle without ending it")
	}
	fixture.shell.sessions.mu.Lock()
	pane := fixture.shell.sessions.tabs[0].panes[0]
	got := pane.handle
	status := pane.status
	fixture.shell.sessions.mu.Unlock()
	if got != nil {
		t.Fatal("the failed Pane kept a handle")
	}
	if status != tabFailed {
		t.Fatalf("the failed Pane status = %v, want failed", status)
	}
}

// TestFocusedPaneRepairsAStaleFocus pins the focus invariant: a focus naming a
// Pane that is not in the Tab falls back to the first Pane and records it.
func TestFocusedPaneRepairsAStaleFocus(t *testing.T) {
	tab := &phoneTab{
		id:    "tab-1",
		focus: "pane-gone",
		panes: []*phonePane{
			{id: "pane-1"},
			{id: "pane-2"},
		},
	}
	pane := tab.focusedPane()
	if pane == nil || pane.id != "pane-1" {
		t.Fatalf("focusedPane = %v, want the first Pane", pane)
	}
	if tab.focus != "pane-1" {
		t.Fatalf("focus after the fallback = %q, want the first Pane", tab.focus)
	}
}
