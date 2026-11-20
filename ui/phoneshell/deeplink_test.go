package phoneshell

import (
	"context"
	"errors"
	"testing"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// readyShell marks the shell as loaded and shown, as the phone composition
// root does after Load and Show. A deep link delivered before this point is
// buffered; after it, the link is applied.
func readyShell(f *phoneFixture) { f.shell.Ready() }

// sshHosts is phoneHosts plus a second plain SSH Host, so a deep link can
// address a Host other than the first one.
func sshHosts() []domainvault.Host {
	return append(phoneHosts(), domainvault.Host{
		ID: "web", Label: "web box", Hostname: "web.example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	})
}

// awaitTrue polls frames and real time until cond holds, with a hard deadline,
// for tests that drive a Shell without the phoneFixture tester.
func awaitTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestDeepLinkMatchesSavedHostAndOpensItsTab is the running-app path: a link
// naming a saved Host's user, host, and port resolves to that one Host and
// opens it, with no extra confirmation.
func TestDeepLinkMatchesSavedHostAndOpensItsTab(t *testing.T) {
	opener := &fakeOpener{output: "linked session\r\n"}
	fixture := newPhoneFixture(t, sshHosts(), opener)
	readyShell(fixture)

	if err := fixture.shell.HandleDeepLink("ssh://deploy@PROD.example.com:2222"); err != nil {
		t.Fatalf("HandleDeepLink: %v", err)
	}
	fixture.await(t, "the linked Session", func() bool {
		return opener.count() == 1 && fixture.tester.HasText("linked session")
	})

	opened := opener.opened()
	if len(opened) != 1 {
		t.Fatalf("opened %d Hosts, want the one the link names", len(opened))
	}
	if opened[0].ID != "prod" {
		t.Fatalf("opened Host %q, want the matching saved Host %q", opened[0].ID, "prod")
	}
	if fixture.shell.Selected() != Sessions {
		t.Fatalf("selected section = %v, want Sessions", fixture.shell.Selected())
	}
	if got := fixture.shell.Router(Sessions).Path(); got != "/session/tab-1" {
		t.Fatalf("Sessions path = %q, want the new Tab's terminal page", got)
	}
}

// TestDeepLinkForAnotherPortAsksToCreate keeps the matcher strict: a link for
// a different port is a different endpoint and must not open the saved Host.
func TestDeepLinkForAnotherPortAsksToCreate(t *testing.T) {
	opener := &fakeOpener{}
	fixture := newPhoneFixture(t, sshHosts(), opener)
	readyShell(fixture)

	if err := fixture.shell.HandleDeepLink("ssh://deploy@prod.example.com:2200"); err != nil {
		t.Fatalf("HandleDeepLink: %v", err)
	}
	if got := opener.count(); got != 0 {
		t.Fatalf("a link for another port opened %d Host(s)", got)
	}
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/new" {
		t.Fatalf("Hosts path = %q, want the create form", got)
	}
}

// TestDeepLinkBeforeReadyWaitsForLoadAndReady is the cold-start order: a link
// can arrive before the Host list is loaded, and must be applied only once the
// shell has loaded and shown, matching against the loaded Hosts.
func TestDeepLinkBeforeReadyWaitsForLoadAndReady(t *testing.T) {
	store := &fakeHostStore{hosts: sshHosts()}
	opener := &fakeOpener{output: "cold start\r\n"}
	shell := New(
		WithHosts(store),
		WithSessionOpener(opener),
		WithScreenFactory(fakeScreens),
		WithSettings(newFakeSettings()),
	)
	// The link is the one that launched the app: it arrives before Load/Show.
	if err := shell.HandleDeepLink("ssh://deploy@prod.example.com:2222"); err != nil {
		t.Fatalf("HandleDeepLink before ready: %v", err)
	}
	if got := opener.count(); got != 0 {
		t.Fatalf("a link was applied before the shell was ready: %d open(s)", got)
	}
	if tabs := shell.sessions.list(); len(tabs) != 0 {
		t.Fatalf("a link created %d Tab(s) before ready", len(tabs))
	}

	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := opener.count(); got != 0 {
		t.Fatalf("Load applied the buffered link before Show/Ready: %d open(s)", got)
	}
	shell.Ready()
	defer shell.Close()
	awaitTrue(t, "the buffered link to open its Tab", func() bool { return opener.count() == 1 })

	opened := opener.opened()
	if len(opened) != 1 || opened[0].ID != "prod" {
		t.Fatalf("buffered link opened %+v, want the prod Host", opened)
	}
}

// TestMalformedDeepLinkReportsAndLeavesNavigation covers the error path: the
// link is rejected with an actionable message and the current page is kept.
func TestMalformedDeepLinkReportsAndLeavesNavigation(t *testing.T) {
	opener := &fakeOpener{}
	fixture := newPhoneFixture(t, sshHosts(), opener)
	readyShell(fixture)
	fixture.shell.Select(Hosts)
	fixture.shell.Router(Hosts).Push("/host/prod")
	fixture.settle()

	for _, raw := range []string{"ssh://bad:port", "https://example.com", "ssh://"} {
		err := fixture.shell.HandleDeepLink(raw)
		if err == nil {
			t.Fatalf("%q was accepted, want an error", raw)
		}
		fixture.tester.Frame()
		if got := fixture.shell.Selected(); got != Hosts {
			t.Fatalf("%q moved to section %v, want Hosts", raw, got)
		}
		if got := fixture.shell.Router(Hosts).Path(); got != "/host/prod" {
			t.Fatalf("%q moved the Hosts route to %q", raw, got)
		}
		if !fixture.tester.HasText("could not open that link") &&
			!fixture.tester.HasText("does not support that link") {
			t.Fatalf("%q showed no actionable error; texts: %q", raw, fixture.tester.Texts())
		}
	}
	if got := opener.count(); got != 0 {
		t.Fatalf("malformed links opened %d Host(s)", got)
	}
}

// TestDeepLinkWhileSessionOpenSelectsOrAddsTabWithoutDisturbingIt covers the
// running-app case with a live Tab: the same Host selects its existing Tab,
// and a different Host adds one, while the first Session stays alive.
func TestDeepLinkWhileSessionOpenSelectsOrAddsTabWithoutDisturbingIt(t *testing.T) {
	opener := &fakeOpener{output: "first session\r\n"}
	fixture := newPhoneFixture(t, sshHosts(), opener)
	readyShell(fixture)
	first := fixture.openSession(t, "prod box")
	firstRoute := fixture.shell.Router(Sessions).Path()

	// The same Host is already open: select its Tab instead of opening again.
	if err := fixture.shell.HandleDeepLink("ssh://deploy@prod.example.com:2222"); err != nil {
		t.Fatalf("HandleDeepLink (same Host): %v", err)
	}
	fixture.tester.Frame()
	if got := opener.count(); got != 1 {
		t.Fatalf("a second link opened %d Sessions, want the existing one", got)
	}
	if got := fixture.shell.Router(Sessions).Path(); got != firstRoute {
		t.Fatalf("the same-Host link moved the route to %q, want %q", got, firstRoute)
	}
	if first.isEnded() {
		t.Fatal("selecting an existing Tab ended its Session")
	}

	// A different saved Host adds a Tab. The first Session is not ended or
	// replaced; it hibernates, exactly as leaving its page does.
	if err := fixture.shell.HandleDeepLink("ssh://root@web.example.com"); err != nil {
		t.Fatalf("HandleDeepLink (new Host): %v", err)
	}
	fixture.await(t, "the linked second Session", func() bool {
		return opener.count() == 2 && opener.liveViews() == 1
	})
	if first.isEnded() {
		t.Fatal("the link ended the open Session")
	}
	if got := len(opener.opened()); got != 2 || opener.opened()[1].ID != "web" {
		t.Fatalf("opened %+v, want the web Host after the first", opener.opened())
	}
	if got := len(fixture.shell.sessions.list()); got != 2 {
		t.Fatalf("Sessions list has %d Tabs, want both", got)
	}
	if got := fixture.shell.Router(Sessions).Path(); got != "/session/tab-2" {
		t.Fatalf("Sessions path = %q, want the linked Tab", got)
	}
	// The phone holds one live terminal view; the first Session's view was
	// released, not its transport ended.
	if first.isAttached() {
		t.Fatal("the hidden first Session kept the phone's live view")
	}
	if first.isEnded() {
		t.Fatal("the hidden first Session was ended")
	}
}

// TestUnknownHostDeepLinkOffersToCreateIt covers an unmatched link: the Hosts
// section asks for a Host with the link's endpoint, and creating it lands on
// the new Host's page, one tap from Connect.
func TestUnknownHostDeepLinkOffersToCreateIt(t *testing.T) {
	creator := &fakeHostCreator{created: domainvault.Host{
		ID: "created", Label: "alice@new.example.com", Hostname: "new.example.com",
		Port: 2200, Username: "alice", Protocol: domainvault.ProtocolSSH,
	}}
	fixture := newPhoneFixtureWith(t, sshHosts(), &fakeOpener{}, WithHostCreator(creator))
	readyShell(fixture)

	if err := fixture.shell.HandleDeepLink("ssh://alice@new.example.com:2200"); err != nil {
		t.Fatalf("HandleDeepLink: %v", err)
	}
	fixture.settle()
	if got := fixture.shell.Selected(); got != Hosts {
		t.Fatalf("selected = %v, want Hosts", got)
	}
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/new" {
		t.Fatalf("Hosts path = %q, want the create form", got)
	}
	if !fixture.tester.HasText("alice@new.example.com:2200") {
		t.Fatalf("create form does not name the link's endpoint; texts: %q", fixture.tester.Texts())
	}
	form := fixture.shell.hosts.form
	if form == nil {
		t.Fatal("the create form has no draft")
	}
	if form.hostname != "new.example.com" || form.username != "alice" || form.port != "2200" {
		t.Fatalf("prefilled form = %+v, want the link's user, host, and port", form)
	}

	if err := fixture.tester.Click("Create Host"); err != nil {
		t.Fatalf("Create Host: %v", err)
	}
	fixture.settle()
	got := creator.last()
	if got.Hostname != "new.example.com" || got.Username != "alice" || got.Port != 2200 ||
		got.Protocol != domainvault.ProtocolSSH {
		t.Fatalf("created draft = %+v, want the link's endpoint", got)
	}
	if creator.calls != 1 {
		t.Fatalf("Create called %d times, want 1", creator.calls)
	}
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/created" {
		t.Fatalf("Hosts path = %q, want the new Host's page", got)
	}
	if !fixture.tester.HasText("Connect to alice@new.example.com") {
		t.Fatalf("new Host page has no Connect action; texts: %q", fixture.tester.Texts())
	}
}

// fakeHostCreator records the drafts it creates and returns a fixed Host.
type fakeHostCreator struct {
	created domainvault.Host
	err     error
	drafts  []domainvault.Draft
	calls   int
}

func (c *fakeHostCreator) Create(_ context.Context, draft domainvault.Draft) (domainvault.Host, error) {
	c.calls++
	c.drafts = append(c.drafts, draft)
	if c.err != nil {
		return domainvault.Host{}, c.err
	}
	created := c.created
	created.Label = firstNonEmpty(created.Label, draft.Label)
	created.Hostname = firstNonEmpty(created.Hostname, draft.Hostname)
	created.Username = draft.Username
	created.Port = draft.Port
	created.Protocol = draft.Protocol
	return created, nil
}

func (c *fakeHostCreator) last() domainvault.Draft {
	if len(c.drafts) == 0 {
		return domainvault.Draft{}
	}
	return c.drafts[len(c.drafts)-1]
}

// TestCreateHostFailureShowsActionableError keeps a failed creation on the
// form, with the reason and a retry, instead of navigating away.
func TestCreateHostFailureShowsActionableError(t *testing.T) {
	creator := &fakeHostCreator{err: errors.New("vault: conflict: duplicate endpoint")}
	fixture := newPhoneFixtureWith(t, sshHosts(), &fakeOpener{}, WithHostCreator(creator))
	readyShell(fixture)

	if err := fixture.shell.HandleDeepLink("ssh://alice@new.example.com:2200"); err != nil {
		t.Fatalf("HandleDeepLink: %v", err)
	}
	fixture.settle()
	if err := fixture.tester.Click("Create Host"); err != nil {
		t.Fatalf("Create Host: %v", err)
	}
	fixture.settle()
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/new" {
		t.Fatalf("a failed create moved to %q, want the form", got)
	}
	if !fixture.tester.HasText("Could not create the Host") {
		t.Fatalf("a failed create showed no error; texts: %q", fixture.tester.Texts())
	}
}
