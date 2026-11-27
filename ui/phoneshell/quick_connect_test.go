package phoneshell

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// quickConnectOpener records the Unsaved Hosts the form hands the opener. It
// delegates the actual open to the shared fake, so the handle's lifecycle is
// the one every other phone test uses.
type quickConnectOpener struct {
	*fakeOpener
	mu      sync.Mutex
	targets []domainvault.Host
	secrets []string
}

func (o *quickConnectOpener) OpenTarget(ctx context.Context, host domainvault.Host,
	secret string) (SessionHandle, error) {

	o.mu.Lock()
	o.targets = append(o.targets, host)
	o.secrets = append(o.secrets, secret)
	o.mu.Unlock()
	return o.fakeOpener.OpenHost(ctx, host)
}

func (o *quickConnectOpener) target() (domainvault.Host, string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.targets) == 0 {
		return domainvault.Host{}, "", false
	}
	return o.targets[len(o.targets)-1], o.secrets[len(o.secrets)-1], true
}

func (o *quickConnectOpener) targetCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.targets)
}

// newQuickConnectFixture builds a Sessions-only shell over the recording
// opener. The Sessions home is the starting page, which is where the Quick
// Connect entry lives.
func newQuickConnectFixture(t *testing.T, output string) (*phoneFixture, *quickConnectOpener) {
	t.Helper()
	inner := &fakeOpener{output: output}
	opener := &quickConnectOpener{fakeOpener: inner}
	store := &fakeHostStore{}
	settings := newFakeSettings()
	shell := New(
		WithHosts(store),
		WithSessionOpener(opener),
		WithScreenFactory(fakeScreens),
		WithSettings(settings),
	)
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	fixture := &phoneFixture{
		shell: shell, store: store, opener: inner, settings: settings,
		context: context.Background(),
	}
	fixture.tester = ui.NewTester(func(c *ui.Context) {
		shell.View(shell.Selected())(c)
	}, 390, 844)
	fixture.tester.Frame()
	return fixture, opener
}

// openQuickConnectForm enters the Sessions home's Quick Connect page.
func openQuickConnectForm(t *testing.T, fixture *phoneFixture) {
	t.Helper()
	click(t, fixture.tester, "Quick Connect")
	fixture.settle()
	if got := fixture.shell.Router(Sessions).Path(); got != quickConnectPath {
		t.Fatalf("Sessions path = %q, want the Quick Connect form %q", got, quickConnectPath)
	}
}

// TestQuickConnectOpensAndSelectsTheNewTab proves the valid path: the shared
// parser's target reaches the opener, a new Tab is created, and the shell
// lands on that Tab's session page.
func TestQuickConnectOpensAndSelectsTheNewTab(t *testing.T) {
	fixture, opener := newQuickConnectFixture(t, "quick ready")
	openQuickConnectForm(t, fixture)

	typeInto(t, fixture.tester, "Target", "deploy@example.com:2222")
	click(t, fixture.tester, "Connect")

	fixture.await(t, "the Quick Connect Session to attach", func() bool {
		handle := fixture.opener.last()
		return opener.targetCount() == 1 && handle != nil && handle.isAttached()
	})

	host, secret, ok := opener.target()
	if !ok {
		t.Fatal("the opener never saw the Quick Connect target")
	}
	if host.ID != "" {
		t.Fatalf("Quick Connect Host ID = %q, want an Unsaved Host with no ID", host.ID)
	}
	if host.Hostname != "example.com" || host.Port != 2222 || host.Username != "deploy" {
		t.Fatalf("opened target = %+v, want the parsed deploy@example.com:2222", host)
	}
	if host.Protocol != domainvault.ProtocolSSH {
		t.Fatalf("protocol = %q, want ssh", host.Protocol)
	}
	if secret != "" {
		t.Fatalf("one-shot credential = %q, want none typed", secret)
	}

	// The new Tab's page is the selected page, not the form.
	if got := fixture.shell.Router(Sessions).Path(); !strings.HasPrefix(got, "/session/") {
		t.Fatalf("Sessions path = %q, want the new Tab's session page", got)
	}
	if !fixture.tester.HasText("Connected · deploy@example.com:2222") {
		t.Fatalf("the new Tab's session page is not showing; texts: %q", fixture.tester.Texts())
	}
}

// TestQuickConnectAcceptsTheDesktopURLForms proves the shared parser's URL
// spelling reaches the opener: a telnet URL selects the Telnet protocol and
// keeps its own default port.
func TestQuickConnectAcceptsTheDesktopURLForms(t *testing.T) {
	fixture, opener := newQuickConnectFixture(t, "quick ready")
	openQuickConnectForm(t, fixture)

	typeInto(t, fixture.tester, "Target", "telnet://console.example.com")
	click(t, fixture.tester, "Connect")

	fixture.await(t, "the telnet Quick Connect Session to attach", func() bool {
		handle := fixture.opener.last()
		return opener.targetCount() == 1 && handle != nil && handle.isAttached()
	})
	host, _, _ := opener.target()
	if host.Protocol != domainvault.ProtocolTelnet || host.Hostname != "console.example.com" || host.Port != 23 {
		t.Fatalf("opened target = %+v, want telnet console.example.com:23", host)
	}
}

// TestQuickConnectEmptyTargetStaysOnTheForm covers the empty input: nothing is
// opened, the form remains, and its message says what to enter.
func TestQuickConnectEmptyTargetStaysOnTheForm(t *testing.T) {
	fixture, opener := newQuickConnectFixture(t, "quick ready")
	openQuickConnectForm(t, fixture)

	click(t, fixture.tester, "Connect")
	fixture.tester.Frame()

	if got := fixture.shell.Router(Sessions).Path(); got != quickConnectPath {
		t.Fatalf("Sessions path = %q, want the form to stay open", got)
	}
	if !fixture.tester.HasText("Enter a target to connect, such as user@host:2222 or ssh://user@host.") {
		t.Fatalf("the form does not state the missing target; texts: %q", fixture.tester.Texts())
	}
	if opener.targetCount() != 0 || len(fixture.shell.sessions.list()) != 0 {
		t.Fatalf("an empty target opened %d targets and %d Tabs, want none",
			opener.targetCount(), len(fixture.shell.sessions.list()))
	}
}

// TestQuickConnectNetworkDeviceReachesTheOpener covers the desktop form's SSH
// network-device mode: the checked flag becomes the Unsaved Host's DeviceType.
func TestQuickConnectNetworkDeviceReachesTheOpener(t *testing.T) {
	fixture, opener := newQuickConnectFixture(t, "quick ready")
	openQuickConnectForm(t, fixture)

	typeInto(t, fixture.tester, "Target", "switch.example.com")
	click(t, fixture.tester, "Network device (SSH)")
	click(t, fixture.tester, "Connect")

	fixture.await(t, "the network-device Quick Connect Session to attach", func() bool {
		handle := fixture.opener.last()
		return opener.targetCount() == 1 && handle != nil && handle.isAttached()
	})
	host, _, _ := opener.target()
	if host.DeviceType != domainvault.DeviceTypeNetwork {
		t.Fatalf("DeviceType = %q, want network", host.DeviceType)
	}
}

// TestQuickConnectInvalidTargetIsCorrectable covers a rejected target: the
// form stays with the typed value and an actionable message, and no Tab is
// created. Correcting the value then connects.
func TestQuickConnectInvalidTargetIsCorrectable(t *testing.T) {
	fixture, opener := newQuickConnectFixture(t, "quick ready")
	openQuickConnectForm(t, fixture)

	typeInto(t, fixture.tester, "Target", "console.example.com:99999")
	click(t, fixture.tester, "Connect")
	fixture.tester.Frame()

	if got := fixture.shell.Router(Sessions).Path(); got != quickConnectPath {
		t.Fatalf("Sessions path = %q, want the form to stay open", got)
	}
	if !fixture.tester.HasText("Check the target's host and port (1-65535), then try again.") {
		t.Fatalf("the form does not state the target problem; texts: %q", fixture.tester.Texts())
	}
	if opener.targetCount() != 0 || len(fixture.shell.sessions.list()) != 0 {
		t.Fatalf("an invalid target opened %d targets and %d Tabs, want none",
			opener.targetCount(), len(fixture.shell.sessions.list()))
	}

	// The draft is still there: select it and replace the bad port, which is
	// the correction the message asks for.
	if got := fixture.shell.sessions.quickConnect.target; got != "console.example.com:99999" {
		t.Fatalf("form target = %q, want the typed value kept for correction", got)
	}
	click(t, fixture.tester, "Target")
	fixture.tester.Key(ui.Cmd, ui.KeyA)
	fixture.tester.Type("console.example.com:2222")
	fixture.tester.Frame()
	click(t, fixture.tester, "Connect")

	fixture.await(t, "the corrected target to attach", func() bool {
		handle := fixture.opener.last()
		return opener.targetCount() == 1 && handle != nil && handle.isAttached()
	})
	if got := fixture.shell.Router(Sessions).Path(); !strings.HasPrefix(got, "/session/") {
		t.Fatalf("Sessions path = %q, want the corrected target's session page", got)
	}
}

// TestQuickConnectCancelForgetsTheSecret covers the credential's lifetime: a
// form abandoned with Cancel must not keep the typed password in memory for
// the next visit. The target draft may stay, because it is not a secret.
func TestQuickConnectCancelForgetsTheSecret(t *testing.T) {
	fixture, opener := newQuickConnectFixture(t, "quick ready")
	openQuickConnectForm(t, fixture)

	typeInto(t, fixture.tester, "Target", "deploy@example.com")
	typeInto(t, fixture.tester, "Password (optional)", "hunter2")
	click(t, fixture.tester, "Cancel")
	fixture.tester.Frame()

	if got := fixture.shell.Router(Sessions).Path(); got != "/" {
		t.Fatalf("Sessions path = %q, want the home after Cancel", got)
	}
	if got := fixture.shell.sessions.quickConnect.secret; got != "" {
		t.Fatalf("form secret = %q after Cancel, want it cleared", got)
	}
	if opener.targetCount() != 0 {
		t.Fatalf("Cancel opened %d targets, want none", opener.targetCount())
	}

	openQuickConnectForm(t, fixture)
	if got := fixture.shell.sessions.quickConnect.secret; got != "" {
		t.Fatalf("reopened form secret = %q, want it cleared", got)
	}
}

// TestQuickConnectSecretIsSealedInMemory proves the phone keeps the one-shot
// credential the same way the desktop does: sealed in the encrypted in-memory
// store under the Pane's identity, never as plaintext on the Pane, passed to
// every reopen, and dropped when the Pane closes.
func TestQuickConnectSecretIsSealedInMemory(t *testing.T) {
	fixture, opener := newQuickConnectFixture(t, "quick ready")
	openQuickConnectForm(t, fixture)

	typeInto(t, fixture.tester, "Target", "deploy@example.com")
	typeInto(t, fixture.tester, "Password (optional)", "hunter2")
	click(t, fixture.tester, "Connect")

	fixture.await(t, "the Quick Connect Session to attach", func() bool {
		handle := fixture.opener.last()
		return opener.targetCount() == 1 && handle != nil && handle.isAttached()
	})

	_, secret, ok := opener.target()
	if !ok || secret != "hunter2" {
		t.Fatalf("first open secret = %q, %v; want the typed password", secret, ok)
	}

	// The Pane struct never holds plaintext: the credential lives only in the
	// encrypted store, keyed by the Pane's identity.
	tabs := fixture.shell.sessions
	tabs.mu.Lock()
	var pane *phonePane
	for _, tab := range tabs.tabs {
		for _, candidate := range tab.panes {
			if candidate.unsaved {
				pane = candidate
			}
		}
	}
	tabs.mu.Unlock()
	if pane == nil {
		t.Fatal("the Quick Connect Pane does not exist")
	}
	if !pane.unsavedSecret {
		t.Fatal("the Pane does not record that it holds a one-shot credential")
	}
	stored, ok := tabs.credentials.Get(string(pane.id))
	if !ok || stored != "hunter2" {
		t.Fatalf("stored credential = %q, %v; want the typed password", stored, ok)
	}
	// A reopen of the same Pane offers the credential again.
	fixture.shell.launchPane(paneTabID(t, tabs, pane), string(pane.id))
	fixture.await(t, "the reopen to reach the opener", func() bool {
		return opener.targetCount() == 2
	})
	_, reopened, _ := opener.target()
	if reopened != "hunter2" {
		t.Fatalf("reopen secret = %q, want the stored password reused", reopened)
	}

	// Closing the Pane drops the credential.
	tabs.removePane(paneTabID(t, tabs, pane), string(pane.id))
	if _, ok := tabs.credentials.Get(string(pane.id)); ok {
		t.Fatal("the credential survived the Pane close")
	}
}

func paneTabID(t *testing.T, tabs *sessionTabs, pane *phonePane) string {
	t.Helper()
	tabs.mu.Lock()
	defer tabs.mu.Unlock()
	for _, tab := range tabs.tabs {
		for _, candidate := range tab.panes {
			if candidate == pane {
				return tab.id
			}
		}
	}
	t.Fatal("the Pane has no Tab")
	return ""
}
