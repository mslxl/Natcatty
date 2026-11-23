package phoneshell

import (
	"context"
	"testing"
	"time"

	appvault "github.com/mslxl/natcatty/application/vault"
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

// TestDeepLinkMatchesSavedHostOpensItsEditor is the running-app path: a link
// naming a saved Host's user, host, and port resolves to that one Host and
// opens its record in the editor. Saving updates the record; connecting is a
// second, explicit action.
func TestDeepLinkMatchesSavedHostOpensItsEditor(t *testing.T) {
	fixture, store := newEditorFixture(t, sshHosts(), nil)
	readyShell(fixture)

	if err := fixture.shell.HandleDeepLink("ssh://deploy@PROD.example.com:2222"); err != nil {
		t.Fatalf("HandleDeepLink: %v", err)
	}
	fixture.settle()

	if got := fixture.shell.Router(Hosts).Path(); got != "/host/prod/edit" {
		t.Fatalf("Hosts path = %q, want the matched Host's editor", got)
	}
	editor := fixture.shell.hosts.editor
	if editor == nil || editor.state.Form().ID != "prod" {
		t.Fatalf("editor form ID = %q, want the matching saved Host %q",
			editor.state.Form().ID, "prod")
	}
	if got := fixture.opener.count(); got != 0 {
		t.Fatalf("a matched link opened %d Session(s); want the editor", got)
	}

	click(t, fixture.tester, "Save and connect")
	fixture.await(t, "the linked Session", func() bool {
		return fixture.opener.count() == 1 && fixture.tester.HasText("editor session")
	})
	opened := fixture.opener.opened()
	if len(opened) != 1 || opened[0].ID != "prod" {
		t.Fatalf("opened %d Hosts, want the matching saved Host", len(opened))
	}
	if len(store.updatedHosts) != 1 || store.updatedHosts[0].ID != "prod" {
		t.Fatalf("updates = %+v, want the prod record", store.updatedHosts)
	}
	if fixture.shell.Selected() != Sessions {
		t.Fatalf("selected section = %v, want Sessions", fixture.shell.Selected())
	}
	if got := fixture.shell.Router(Sessions).Path(); got != "/session/tab-1" {
		t.Fatalf("Sessions path = %q, want the new Tab's terminal page", got)
	}
}

// TestDeepLinkForAnotherPortAsksToCreate keeps the matcher strict: a link for
// a different port is a different endpoint and opens the create editor
// prefilled with the link's endpoint.
func TestDeepLinkForAnotherPortAsksToCreate(t *testing.T) {
	fixture, _ := newEditorFixture(t, sshHosts(), nil)
	readyShell(fixture)

	if err := fixture.shell.HandleDeepLink("ssh://deploy@prod.example.com:2200"); err != nil {
		t.Fatalf("HandleDeepLink: %v", err)
	}
	fixture.settle()
	if got := fixture.opener.count(); got != 0 {
		t.Fatalf("a link for another port opened %d Host(s)", got)
	}
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/new" {
		t.Fatalf("Hosts path = %q, want the create editor", got)
	}
	form := fixture.shell.hosts.editor.state.Form()
	if form.Hostname != "prod.example.com" || form.Port != "2200" || form.Username != "deploy" {
		t.Fatalf("prefilled form = %+v, want the link's endpoint", form)
	}
	if !fixture.shell.hosts.editor.unmatchedLink {
		t.Fatal("an unmatched link must lead with Save and connect")
	}
}

// TestDeepLinkBeforeReadyWaitsForLoadAndReady is the cold-start order: a link
// can arrive before the Host list is loaded, and must be applied only once the
// shell has loaded and shown, matching against the loaded Hosts.
func TestDeepLinkBeforeReadyWaitsForLoadAndReady(t *testing.T) {
	store := newFakeEditorStore(sshHosts(), nil)
	opener := &fakeOpener{output: "cold start\r\n"}
	shell := New(
		WithHosts(store),
		WithVault(store),
		WithHostCreator(store),
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
	if got := shell.Router(Hosts).Path(); got != "/" {
		t.Fatalf("Load applied the buffered link before Show/Ready: %q", got)
	}
	shell.Ready()
	defer shell.Close()
	if got := shell.Router(Hosts).Path(); got != "/host/prod/edit" {
		t.Fatalf("buffered link route = %q, want the matched Host's editor", got)
	}
	if got := opener.count(); got != 0 {
		t.Fatalf("the buffered link opened %d Session(s); want the editor", got)
	}
	if got := shell.hosts.editor.state.Form().ID; got != "prod" {
		t.Fatalf("editor form ID = %q, want the prod Host", got)
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

// TestDeepLinkWhileSessionOpenEditsWithoutDisturbingIt covers the running-app
// case with a live Tab: a link opens the matched Host's editor, and neither
// the open Session nor the Tab list is touched. Connecting only follows the
// user's Save and connect.
func TestDeepLinkWhileSessionOpenEditsWithoutDisturbingIt(t *testing.T) {
	fixture, _ := newEditorFixture(t, sshHosts(), nil)
	readyShell(fixture)
	first := fixture.openSession(t, "prod box")

	// The same Host is already open, but the link asks to edit its endpoint.
	if err := fixture.shell.HandleDeepLink("ssh://deploy@prod.example.com:2222"); err != nil {
		t.Fatalf("HandleDeepLink (same Host): %v", err)
	}
	fixture.settle()
	if got := fixture.opener.count(); got != 1 {
		t.Fatalf("the link opened %d Sessions, want the existing one untouched", got)
	}
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/prod/edit" {
		t.Fatalf("Hosts path = %q, want the matched Host's editor", got)
	}
	if first.isEnded() {
		t.Fatal("opening the editor ended the open Session")
	}
	if got := len(fixture.shell.sessions.list()); got != 1 {
		t.Fatalf("Sessions list has %d Tabs, want the one already open", got)
	}

	// A different saved Host replaces the editor page for that record; the
	// first Session still stays alive.
	if err := fixture.shell.HandleDeepLink("ssh://root@web.example.com"); err != nil {
		t.Fatalf("HandleDeepLink (new Host): %v", err)
	}
	fixture.settle()
	if got := fixture.opener.count(); got != 1 {
		t.Fatalf("a second link opened %d Sessions, want none", got)
	}
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/web/edit" {
		t.Fatalf("Hosts path = %q, want the web Host's editor", got)
	}
	if first.isEnded() {
		t.Fatal("the second link ended the open Session")
	}
	if first.isAttached() {
		t.Fatal("the hidden first Session kept the phone's live view")
	}
	if got := len(fixture.shell.sessions.list()); got != 1 {
		t.Fatalf("Sessions list has %d Tabs, want the one already open", got)
	}
}

// TestASecondDeepLinkReplacesTheOpenEditor keeps a link from stacking editor
// routes. Cancelling the second Host's editor must return to the page the
// first editor was opened from, not to a discarded draft.
func TestASecondDeepLinkReplacesTheOpenEditor(t *testing.T) {
	fixture, _ := newEditorFixture(t, sshHosts(), nil)
	readyShell(fixture)

	for _, raw := range []string{
		"ssh://deploy@prod.example.com:2222",
		"ssh://root@web.example.com",
	} {
		if err := fixture.shell.HandleDeepLink(raw); err != nil {
			t.Fatalf("HandleDeepLink(%q): %v", raw, err)
		}
		fixture.settle()
	}
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/web/edit" {
		t.Fatalf("Hosts path = %q, want the second Host's editor", got)
	}

	click(t, fixture.tester, "Cancel")
	fixture.settle()
	if got := fixture.shell.Router(Hosts).Path(); got != "/" {
		t.Fatalf("Cancel returned to %q, want the Hosts root", got)
	}
	if fixture.shell.hosts.editor.state.Editing() {
		t.Fatal("Cancel left the editor editing")
	}
	if got := fixture.opener.count(); got != 0 {
		t.Fatalf("two links and Cancel opened %d Session(s)", got)
	}
}

// TestUnknownHostDeepLinkOffersToCreateIt covers an unmatched link: the Hosts
// section opens the create editor prefilled with the link's endpoint, and its
// leading action saves and connects.
func TestUnknownHostDeepLinkOffersToCreateIt(t *testing.T) {
	fixture, store := newEditorFixture(t, sshHosts(), nil)
	readyShell(fixture)

	if err := fixture.shell.HandleDeepLink("ssh://alice@new.example.com:2200"); err != nil {
		t.Fatalf("HandleDeepLink: %v", err)
	}
	fixture.settle()
	if got := fixture.shell.Selected(); got != Hosts {
		t.Fatalf("selected = %v, want Hosts", got)
	}
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/new" {
		t.Fatalf("Hosts path = %q, want the create editor", got)
	}
	editor := fixture.shell.hosts.editor
	form := editor.state.Form()
	if form.Hostname != "new.example.com" || form.Username != "alice" || form.Port != "2200" {
		t.Fatalf("prefilled form = %+v, want the link's user, host, and port", form)
	}

	click(t, fixture.tester, "Save and connect")
	fixture.await(t, "the new Host's Session", func() bool {
		return fixture.opener.count() == 1
	})
	got := store.createdDrafts
	if len(got) != 1 || got[0].Hostname != "new.example.com" ||
		got[0].Username != "alice" || got[0].Port != 2200 ||
		got[0].Protocol != domainvault.ProtocolSSH {
		t.Fatalf("created drafts = %+v, want the link's endpoint", got)
	}
	if opened := fixture.opener.opened(); len(opened) != 1 ||
		opened[0].Hostname != "new.example.com" {
		t.Fatalf("opened %+v, want the new Host", opened)
	}
}

// TestCreateHostFailureShowsActionableError keeps a failed creation on the
// editor, with the reason and a retry, instead of navigating away.
func TestCreateHostFailureShowsActionableError(t *testing.T) {
	fixture, store := newEditorFixture(t, sshHosts(), nil)
	store.createErr = &appvault.Error{Kind: appvault.KindConflict}
	readyShell(fixture)

	if err := fixture.shell.HandleDeepLink("ssh://alice@new.example.com:2200"); err != nil {
		t.Fatalf("HandleDeepLink: %v", err)
	}
	fixture.settle()
	click(t, fixture.tester, "Save and connect")
	fixture.settle()
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/new" {
		t.Fatalf("a failed create moved to %q, want the editor", got)
	}
	if !fixture.tester.HasText("A Host with this endpoint already exists.") {
		t.Fatalf("a failed create showed no error; texts: %q", fixture.tester.Texts())
	}
	if got := fixture.shell.hosts.editor.state.Form().Hostname; got != "new.example.com" {
		t.Fatalf("the retained Hostname = %q, want the link's address", got)
	}
	if got := fixture.opener.count(); got != 0 {
		t.Fatalf("a failed create opened %d Session(s)", got)
	}
}
