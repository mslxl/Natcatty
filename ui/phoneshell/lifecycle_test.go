package phoneshell

import (
	"errors"
	"strings"
	"testing"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// openHostRow taps one Host in the Hosts section. The tap switches to Sessions
// and pushes the new Tab's terminal page, exactly as the app does.
func (f *phoneFixture) openHostRow(t *testing.T, label string) {
	t.Helper()
	if f.shell.Selected() != Hosts {
		f.shell.Select(Hosts)
		f.tester.Frame()
	}
	if got := f.shell.Router(Hosts).Path(); got != "/" {
		t.Fatalf("Hosts path = %q, want its root to tap %q", got, label)
	}
	if err := f.tester.Click("Open " + label); err != nil {
		t.Fatalf("open %q: %v", label, err)
	}
	f.tester.Frame()
}

// backToSessionsRoot backs out of the current terminal page and returns to the
// Sessions list. It never closes a Session.
func (f *phoneFixture) backToSessionsRoot(t *testing.T) {
	t.Helper()
	if got := f.shell.Router(Sessions).Path(); got != "/" {
		if err := f.tester.Click("Back"); err != nil {
			t.Fatalf("back from %q: %v", got, err)
		}
		f.settle()
	}
	if f.shell.Selected() != Sessions {
		f.shell.Select(Sessions)
	}
	f.tester.Frame()
}

// openSession opens one Host and waits until its Tab owns the phone's one live
// view.
func (f *phoneFixture) openSession(t *testing.T, label string) *fakeHandle {
	t.Helper()
	before := f.opener.count()
	f.openHostRow(t, label)
	f.await(t, "the Session to attach", func() bool {
		return f.opener.count() == before+1 && f.opener.liveViews() == 1
	})
	f.settle()
	return f.opener.last()
}

func TestSessionsSectionShowsIdentityAndLifecycleState(t *testing.T) {
	opener := &fakeOpener{output: "session output\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)

	// The visible Session's page says it is connected and identifies the
	// endpoint.
	fixture.openSession(t, "prod box")
	fixture.await(t, "the connected status", func() bool {
		return fixture.tester.HasText("Connected · deploy@prod.example.com:2222")
	})

	// Leaving the page hibernates it; the list says so and keeps the identity.
	fixture.backToSessionsRoot(t)
	fixture.await(t, "the hibernated row", func() bool {
		return fixture.tester.HasText("Hibernated · deploy@prod.example.com:2222")
	})
	if got := opener.liveViews(); got != 0 {
		t.Fatalf("the list left %d live views attached, want none", got)
	}

	// Opening a second Session hibernates the first and shows the second
	// connected; the list distinguishes the two endpoints afterwards.
	fixture.openSession(t, "console")
	if !fixture.tester.HasText("Connected · console.example.com:23 · telnet") {
		t.Fatalf("the visible Tab is not reported connected; texts: %q", fixture.tester.Texts())
	}
	if opener.last().isEnded() {
		t.Fatal("opening a second Session ended the first")
	}
	fixture.backToSessionsRoot(t)
	fixture.await(t, "both hibernated rows", func() bool {
		return fixture.tester.HasText("Hibernated · deploy@prod.example.com:2222") &&
			fixture.tester.HasText("Hibernated · console.example.com:23 · telnet")
	})
	if got := opener.ended(); got != 0 {
		t.Fatalf("navigation ended %d Session(s), want none", got)
	}
}

func TestTabStatusTextNamesEveryLifecycleState(t *testing.T) {
	host := domainvault.Host{
		ID: "prod", Hostname: "prod.example.com", Port: 2222,
		Username: "deploy", Protocol: domainvault.ProtocolSSH,
	}
	cases := []struct {
		status tabStatus
		want   string
	}{
		{tabConnecting, "Connecting… · deploy@prod.example.com:2222"},
		{tabReady, "Connected · deploy@prod.example.com:2222"},
		{tabHibernated, "Hibernated · deploy@prod.example.com:2222"},
		{tabDormant, "Dormant · deploy@prod.example.com:2222"},
		{tabReconnecting, "Reconnecting… · deploy@prod.example.com:2222"},
		{tabFailed, "Connection failed · deploy@prod.example.com:2222"},
	}
	for _, tc := range cases {
		if got := tabStatusText(tabView{host: host, status: tc.status}); got != tc.want {
			t.Errorf("tabStatusText(%v) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestLeavingHibernatesAndReturningResumesWithScrollback(t *testing.T) {
	opener := &fakeOpener{output: "scrollback line\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)

	handle := fixture.openSession(t, "prod box")
	fixture.await(t, "the terminal output", func() bool {
		return fixture.tester.HasText("scrollback line")
	})

	fixture.backToSessionsRoot(t)
	fixture.await(t, "hibernation", func() bool {
		return !handle.isAttached() && fixture.tester.HasText("Hibernated · deploy@prod.example.com:2222")
	})
	if handle.isEnded() {
		t.Fatal("leaving the page ended the Session")
	}
	if got := opener.liveViews(); got != 0 {
		t.Fatalf("a hibernated Session kept %d live view(s)", got)
	}

	// Returning resumes the same Session; the fake screen reads the Session's
	// retained output, so the earlier line is visible again.
	if err := fixture.tester.Click("Open session prod box"); err != nil {
		t.Fatalf("return to the Session: %v", err)
	}
	fixture.await(t, "the resumed terminal", func() bool {
		return handle.isAttached() && fixture.tester.HasText("scrollback line")
	})
	if fixture.opener.count() != 1 {
		t.Fatalf("returning opened %d Sessions, want the original one", fixture.opener.count())
	}
	if handle.isEnded() {
		t.Fatal("resuming ended the Session")
	}
}

func TestNavigationNeverClosesASession(t *testing.T) {
	opener := &fakeOpener{output: "keep me\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)

	first := fixture.openSession(t, "prod box")
	fixture.backToSessionsRoot(t)
	second := fixture.openSession(t, "console")
	fixture.backToSessionsRoot(t)

	assertOpen := func(where string) {
		t.Helper()
		if got := opener.ended(); got != 0 {
			t.Fatalf("%s ended %d Session(s)", where, got)
		}
		if first.isEnded() || second.isEnded() {
			t.Fatalf("%s ended one of the Sessions", where)
		}
		if got := len(fixture.shell.sessions.list()); got != 2 {
			t.Fatalf("%s left %d open Tab(s), want 2", where, got)
		}
	}

	// Back-navigation and section switches.
	for _, sec := range []Section{Hosts, Vault, Settings, Sessions} {
		fixture.shell.Select(sec)
		fixture.tester.Frame()
		assertOpen("switching sections")
	}
	// A deep link pushes the Session route directly (ticket 18 wires the URL
	// handler; the rule this ticket owns is that the push is not a close).
	fixture.shell.Select(Sessions)
	fixture.shell.Router(Sessions).Push("/session/tab-1")
	fixture.tester.Frame()
	fixture.settle()
	assertOpen("a deep link")
	fixture.await(t, "the deep-linked Session", func() bool {
		return first.isAttached() && opener.liveViews() == 1
	})
	if second.isAttached() {
		t.Fatal("a deep link left the other Tab's view attached")
	}

	// Leaving again still never closes.
	fixture.backToSessionsRoot(t)
	assertOpen("back after a deep link")
}

func TestOnlyOneLiveTerminalViewAcrossTabs(t *testing.T) {
	opener := &fakeOpener{output: "one view\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)

	first := fixture.openSession(t, "prod box")
	if got := opener.liveViews(); got != 1 {
		t.Fatalf("live views after the first open = %d, want 1", got)
	}
	fixture.backToSessionsRoot(t)
	if first.isAttached() || opener.liveViews() != 0 {
		t.Fatal("a hidden Session kept its view attached")
	}

	second := fixture.openSession(t, "console")
	if first.isAttached() || !second.isAttached() || opener.liveViews() != 1 {
		t.Fatalf("views after switching Tabs: first=%v second=%v total=%d",
			first.isAttached(), second.isAttached(), opener.liveViews())
	}
	fixture.backToSessionsRoot(t)

	// Switching back moves the one view to the other Tab.
	if err := fixture.tester.Click("Open session prod box"); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	fixture.await(t, "the resumed view", func() bool {
		return first.isAttached() && !second.isAttached() && opener.liveViews() == 1
	})
}

func TestExplicitCloseConfirmsWhenLive(t *testing.T) {
	opener := &fakeOpener{output: "closing\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	handle := fixture.openSession(t, "prod box")

	if err := fixture.tester.Click("Close session prod box"); err != nil {
		t.Fatalf("request close: %v", err)
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText("Close prod box?") {
		t.Fatalf("a live Session closed without confirmation; texts: %q", fixture.tester.Texts())
	}
	if handle.isEnded() {
		t.Fatal("the Session ended before the confirmation")
	}

	// Cancelling keeps the Session and its page.
	if err := fixture.tester.Click("Keep open"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	fixture.tester.Frame()
	if handle.isEnded() {
		t.Fatal("cancelling the confirmation ended the Session")
	}
	if got := fixture.shell.Router(Sessions).Path(); got != "/session/tab-1" {
		t.Fatalf("cancelling left the page at %q", got)
	}

	// Confirming disconnects, removes the Tab, and leaves its page.
	if err := fixture.tester.Click("Close session prod box"); err != nil {
		t.Fatalf("request close again: %v", err)
	}
	fixture.tester.Frame()
	if err := fixture.tester.Click("Close"); err != nil {
		t.Fatalf("confirm close: %v", err)
	}
	fixture.settle()
	fixture.await(t, "the close", func() bool { return handle.isEnded() })
	if got := opener.liveViews(); got != 0 {
		t.Fatalf("a closed Session kept %d live view(s)", got)
	}
	if got := fixture.shell.Router(Sessions).Path(); got != "/" {
		t.Fatalf("after closing, path = %q, want the Sessions list", got)
	}
	if !fixture.tester.HasText("No sessions yet. Open a Host from the Hosts tab.") {
		t.Fatalf("the closed Tab is still listed; texts: %q", fixture.tester.Texts())
	}
}

func TestExplicitCloseOfHibernatedSessionDoesNotConfirm(t *testing.T) {
	opener := &fakeOpener{output: "hidden\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	handle := fixture.openSession(t, "prod box")
	fixture.backToSessionsRoot(t)
	fixture.await(t, "hibernation", func() bool { return !handle.isAttached() })

	if err := fixture.tester.Click("Close session prod box"); err != nil {
		t.Fatalf("close hibernated Tab: %v", err)
	}
	fixture.await(t, "the hibernated Tab to close", func() bool { return handle.isEnded() })
	if fixture.tester.HasText("Close prod box?") {
		t.Fatal("a hibernated Session asked for close confirmation")
	}
	if !fixture.tester.HasText("No sessions yet. Open a Host from the Hosts tab.") {
		t.Fatalf("the closed Tab is still listed; texts: %q", fixture.tester.Texts())
	}
}

func TestCloseConfirmationFollowsTheSecurityPreference(t *testing.T) {
	opener := &fakeOpener{output: "preference\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	fixture.settings.prefs.Security.ConfirmCloseSessions = false
	handle := fixture.openSession(t, "prod box")

	if err := fixture.tester.Click("Close session prod box"); err != nil {
		t.Fatalf("close with the preference off: %v", err)
	}
	fixture.await(t, "the close", func() bool { return handle.isEnded() })
	if fixture.tester.HasText("Close prod box?") {
		t.Fatal("the Security preference did not suppress the confirmation")
	}
}

func TestResumeFailureShowsReconnectingAndRetries(t *testing.T) {
	opener := &fakeOpener{output: "reconnect me\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	handle := fixture.openSession(t, "prod box")
	fixture.backToSessionsRoot(t)
	fixture.await(t, "hibernation", func() bool { return !handle.isAttached() })

	handle.failNextResume(errors.New("transport closed"))
	if err := fixture.tester.Click("Open session prod box"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	fixture.await(t, "the resume failure", func() bool {
		return fixture.tester.HasText("Could not resume prod box: transport closed. Open the Host again to reconnect.")
	})

	// Backing out leaves the Tab retryable: it is reconnecting, not closed.
	fixture.settle()
	if err := fixture.tester.Click("Back to Sessions"); err != nil {
		t.Fatalf("back from a reconnecting Session: %v", err)
	}
	fixture.settle()
	if handle.isEnded() {
		t.Fatal("a failed resume ended the Session")
	}
	fixture.await(t, "the reconnecting row", func() bool {
		return fixture.tester.HasText("Reconnecting… · deploy@prod.example.com:2222")
	})

	// Try again opens the Host afresh under the same Tab.
	if err := fixture.tester.Click("Open session prod box"); err != nil {
		t.Fatalf("open the reconnecting Tab: %v", err)
	}
	fixture.settle()
	if err := fixture.tester.Click("Try again"); err != nil {
		t.Fatalf("retry the Session: %v", err)
	}
	fixture.await(t, "the retried Session", func() bool {
		return opener.count() == 2 && opener.liveViews() == 1 && fixture.tester.HasText("reconnect me")
	})
}

// TestSessionEndingWhileVisibleBecomesRetryable covers the far end: a Session
// that exits while its page is showing releases its stale screen, reports the
// end, and can be reopened under the same Tab.
func TestSessionEndingWhileVisibleBecomesRetryable(t *testing.T) {
	opener := &fakeOpener{output: "remote exit\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	handle := fixture.openSession(t, "prod box")

	if err := handle.End(); err != nil {
		t.Fatalf("end the Session: %v", err)
	}
	fixture.await(t, "the ended Session", func() bool {
		return fixture.tester.HasText("The Session ended. Open the Host again to reconnect.")
	})
	if got := opener.liveViews(); got != 0 {
		t.Fatalf("an ended Session kept %d live view(s)", got)
	}
	if got := len(fixture.shell.sessions.list()); got != 1 {
		t.Fatalf("the ended Tab was dropped from the list: %d", got)
	}

	fixture.settle()
	if err := fixture.tester.Click("Try again"); err != nil {
		t.Fatalf("reopen the ended Session: %v", err)
	}
	fixture.await(t, "the reopened Session", func() bool {
		return opener.count() == 2 && opener.liveViews() == 1
	})
}

// TestPhoneExposesNoSplitDetachOrMoveActions pins the flat phone Workspace:
// one Tab per Session, one Pane per Tab, and no split, detach, or move action
// anywhere the phone renders.
func TestPhoneExposesNoSplitDetachOrMoveActions(t *testing.T) {
	opener := &fakeOpener{output: "flat\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	fixture.openSession(t, "prod box")
	fixture.settle()

	forbidden := []string{"split", "detach", "move to tab", "move-to-tab", "duplicate", "pane"}
	check := func(where string) {
		t.Helper()
		for _, text := range fixture.tester.Texts() {
			lower := strings.ToLower(text)
			for _, word := range forbidden {
				if strings.Contains(lower, word) {
					t.Fatalf("%s exposes %q in %q", where, word, text)
				}
			}
		}
	}
	check("the terminal page")

	// One Tab has exactly one Session and one Pane by construction: a phoneTab
	// holds a single Host and a single Session handle.
	fixture.shell.sessions.mu.Lock()
	for _, tab := range fixture.shell.sessions.tabs {
		if tab.host.ID == "" || tab.handle == nil {
			fixture.shell.sessions.mu.Unlock()
			t.Fatalf("a phone Tab is not one Session: %+v", tab)
		}
	}
	if got := len(fixture.shell.sessions.tabs); got != 1 {
		fixture.shell.sessions.mu.Unlock()
		t.Fatalf("one Host opened %d Tabs, want 1", got)
	}
	fixture.shell.sessions.mu.Unlock()

	fixture.backToSessionsRoot(t)
	check("the Sessions list")
	for _, sec := range []Section{Hosts, Vault, Settings} {
		fixture.shell.Select(sec)
		fixture.tester.Frame()
		check("another section")
	}
}

// TestVisibleSessionFollowsTheRoute pins the one place the rule reads the
// route: only the top /session/{id} page of the selected Sessions section is
// visible, and no other path names a Session.
func TestVisibleSessionFollowsTheRoute(t *testing.T) {
	shell := New()
	router := shell.Router(Sessions)

	shell.Select(Sessions)
	if got := shell.visibleSession(); got != "" {
		t.Fatalf("visible Session at / = %q, want none", got)
	}
	router.Push("/session/tab-7")
	if got := shell.visibleSession(); got != "tab-7" {
		t.Fatalf("visible Session = %q, want tab-7", got)
	}
	shell.Select(Hosts)
	if got := shell.visibleSession(); got != "" {
		t.Fatalf("a Session is visible from another section: %q", got)
	}
	shell.Select(Sessions)
	router.Push("/session/tab-8")
	if got := shell.visibleSession(); got != "tab-8" {
		t.Fatalf("visible Session = %q, want the top page tab-8", got)
	}
	router.Back()
	if got := shell.visibleSession(); got != "tab-7" {
		t.Fatalf("visible Session after back = %q, want tab-7", got)
	}
	router.Push("/session/")
	if got := shell.visibleSession(); got != "" {
		t.Fatalf("an empty Session id is visible: %q", got)
	}
	router.Push("/session/tab-9/")
	if got := shell.visibleSession(); got != "tab-9" {
		t.Fatalf("a trailing slash hid the Session: %q, want tab-9", got)
	}
	router.Push("/diagnostic")
	if got := shell.visibleSession(); got != "" {
		t.Fatalf("a non-Session page is visible: %q", got)
	}
}

// TestSyncLiveViewIsIdempotent pins that repeated frames do not thrash a
// Session: the visible one stays attached and a hidden one stays hibernated.
func TestSyncLiveViewIsIdempotent(t *testing.T) {
	opener := &fakeOpener{output: "steady\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	handle := fixture.openSession(t, "prod box")

	for i := 0; i < 5; i++ {
		fixture.tester.Frame()
		if !handle.isAttached() {
			t.Fatalf("frame %d detached the visible Session", i)
		}
	}
	if got := opener.liveViews(); got != 1 {
		t.Fatalf("live views = %d after repeated frames, want 1", got)
	}
}
