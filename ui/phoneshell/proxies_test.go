package phoneshell

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// openProxies loads a shell over a Vault fake and opens Vault > Proxies.
func openProxies(t *testing.T, opts ...Option) (*Shell, *ui.Tester, *fakeVault) {
	t.Helper()
	store := &fakeVault{}
	options := []Option{WithVault(store)}
	options = append(options, opts...)
	shell := New(options...)
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := openSectionPage(t, shell, Vault, "Proxies")
	return shell, tt, store
}

// TestProxiesListAddsEditsAndDeletesProfiles drives the Proxies page through
// the shared Vault service: a Profile is created, edited, and deleted, and the
// service sees each exact draft.
func TestProxiesListAddsEditsAndDeletesProfiles(t *testing.T) {
	const command = `relay --target %h:%p  --label 'a  b' --literal '%%'`
	_, tt, store := openProxies(t)

	click(t, tt, "Add profile")
	click(t, tt, "ProxyCommand")
	typeInto(t, tt, "Label", "phone command")
	typeInto(t, tt, "Command", command)
	click(t, tt, "Save profile")

	if len(store.proxies) != 1 {
		t.Fatalf("stored profiles = %+v, want one", store.proxies)
	}
	created := store.proxies[0]
	if created.Label != "phone command" || created.Config.Type != domainvault.ProxyCommand {
		t.Fatalf("created profile = %+v", created)
	}
	if created.Config.Command != command {
		t.Fatalf("created command = %q, want %q", created.Config.Command, command)
	}
	if !tt.HasText("phone command") || !tt.HasText("ProxyCommand · "+command) {
		t.Fatalf("created profile is not listed; texts: %v", tt.Texts())
	}

	click(t, tt, "Edit profile phone command")
	typeInto(t, tt, "Label", " v2")
	click(t, tt, "Save profile")
	if len(store.proxies) != 1 || store.proxies[0].Label != "phone command v2" {
		t.Fatalf("edited profiles = %+v, want the renamed Profile", store.proxies)
	}
	if len(store.attemptedProxies) != 2 {
		t.Fatalf("service drafts = %d, want create and update", len(store.attemptedProxies))
	}
	if got := store.attemptedProxies[1]; got.Label != "phone command v2" ||
		got.Config.Command != command {
		t.Fatalf("update draft = %+v, want the edited label and unchanged command", got)
	}

	click(t, tt, "Delete profile phone command v2")
	click(t, tt, "Delete")
	if len(store.proxies) != 0 {
		t.Fatalf("profiles after delete = %+v, want none", store.proxies)
	}
	if !tt.HasText("No Proxy Profiles yet. Profiles added on the desktop appear here.") {
		t.Fatalf("empty state is missing; texts: %v", tt.Texts())
	}
}

// TestProxiesCommandProfileStaysSavableWhenPlatformCannotExecute proves the
// platform gate is explained but never disables the configuration: the page
// shows the ticket 47 reason and the Profile still saves.
func TestProxiesCommandProfileStaysSavableWhenPlatformCannotExecute(t *testing.T) {
	const reason = "ProxyCommand is unavailable on this platform"
	_, tt, store := openProxies(t, WithProxyCommandCapability(ProxyCommandCapability{
		Supported: false,
		Reason:    reason,
	}))

	if !tt.HasText(reason + ". You can still save and sync ProxyCommand profiles.") {
		t.Fatalf("the unsupported reason is not shown; texts: %v", tt.Texts())
	}

	click(t, tt, "Add profile")
	click(t, tt, "ProxyCommand")
	if !tt.HasText(reason + ". You can still save and sync this profile.") {
		t.Fatalf("the editor does not repeat the unsupported reason; texts: %v", tt.Texts())
	}
	typeInto(t, tt, "Label", "gated command")
	typeInto(t, tt, "Command", "gated-command %h %p")
	click(t, tt, "Save profile")

	if len(store.proxies) != 1 || store.proxies[0].Config.Command != "gated-command %h %p" {
		t.Fatalf("a gated platform did not save the Profile: %+v", store.proxies)
	}
}

// TestHostEditorCreatesInlineProxyCommand creates a Host with an inline
// ProxyCommand through the Advanced group, so the phone can define the
// transport rather than only preserve one synced from the desktop.
func TestHostEditorCreatesInlineProxyCommand(t *testing.T) {
	const command = "relay --target %h:%p --new  flag"
	fixture, store := newEditorFixture(t, []domainvault.Host{
		editorHost("prod", "prod box", "prod.example.com"),
	}, nil)
	openNewHostEditor(t, fixture)
	fillManualHost(t, fixture, "inline new", "new.example.com", "deploy")

	tt := fixture.tester
	click(t, tt, "Advanced options")
	tt.Scroll(195, 400, 0, 900)
	tt.Frame()
	click(t, tt, "Inline ProxyCommand")
	tt.Scroll(195, 400, 0, 900)
	tt.Frame()
	typeInto(t, tt, "Inline command", command)
	click(t, tt, "Save")
	fixture.settle()

	if len(store.createdDrafts) != 1 {
		t.Fatalf("create calls = %d, want one", len(store.createdDrafts))
	}
	draft := store.createdDrafts[0]
	if draft.Proxy == nil || draft.Proxy.Type != domainvault.ProxyCommand ||
		draft.Proxy.Command != command {
		t.Fatalf("created proxy = %+v, want the inline command %q", draft.Proxy, command)
	}
	if draft.ProxyProfileID != "" || draft.DirectProxy {
		t.Fatalf("an inline create also wrote profile %q / direct %v", draft.ProxyProfileID, draft.DirectProxy)
	}
}

// TestHostEditorPreservesInlineProxyCommandVerbatim edits only the Label of a
// Host that owns an inline ProxyCommand. The draft handed to the shared Vault
// service must carry the same command bytes.
func TestHostEditorPreservesInlineProxyCommandVerbatim(t *testing.T) {
	const command = `relay --target %h:%p  --label 'a  b' --literal '%%'`
	host := editorHost("inline", "inline box", "inline.example.com")
	host.Proxy = &domainvault.ProxyConfig{Type: domainvault.ProxyCommand, Command: command}
	fixture, store := newEditorFixture(t, []domainvault.Host{host}, nil)

	click(t, fixture.tester, "Details for inline box")
	fixture.settle()
	click(t, fixture.tester, "Edit")
	fixture.settle()
	click(t, fixture.tester, "Advanced options")

	editor := fixture.shell.hosts.editor
	if editor.proxyChoice != inlineProxyChoice {
		t.Fatalf("proxy choice = %q, want the inline command", editor.proxyChoice)
	}
	value, ok := fixture.tester.Access("Inline command")
	if !ok {
		t.Fatalf("the Inline command field is missing; texts: %v", fixture.tester.Texts())
	}
	if editor.inlineProxy.Command != command {
		t.Fatalf("Inline command field = %q, want %q", editor.inlineProxy.Command, command)
	}
	_ = value

	typeInto(t, fixture.tester, "Label", " v2")
	click(t, fixture.tester, "Save")
	fixture.settle()

	if len(store.updatedHosts) != 1 {
		t.Fatalf("update calls = %d, want one", len(store.updatedHosts))
	}
	draft := store.updatedHosts[0].Draft
	if draft.Label != "inline box v2" {
		t.Fatalf("Label = %q, want the edited value", draft.Label)
	}
	if draft.Proxy == nil || draft.Proxy.Type != domainvault.ProxyCommand {
		t.Fatalf("saved proxy = %+v, want the inline command", draft.Proxy)
	}
	if draft.Proxy.Command != command {
		t.Fatalf("saved command = %q, want it verbatim (%q)", draft.Proxy.Command, command)
	}
	if draft.ProxyProfileID != "" || draft.DirectProxy {
		t.Fatalf("an inline save also wrote profile %q / direct %v", draft.ProxyProfileID, draft.DirectProxy)
	}
}

// TestHostEditorExplainsDanglingProfileAndSavesAnother covers a Profile
// deleted on the desktop while the Host still references it: the editor shows
// a fixable prompt and saving a valid Profile clears the stale reference.
func TestHostEditorExplainsDanglingProfileAndSavesAnother(t *testing.T) {
	host := editorHost("prod", "prod box", "prod.example.com")
	host.ProxyProfileID = "gone"
	fixture, store := newEditorFixture(t, []domainvault.Host{host}, nil)
	store.proxies = []domainvault.ProxyProfile{{
		ID: "office", Label: "office",
		Config: domainvault.ProxyConfig{Type: domainvault.ProxySOCKS5, Host: "socks.example.com", Port: 1080},
	}}

	click(t, fixture.tester, "Details for prod box")
	fixture.settle()
	click(t, fixture.tester, "Edit")
	fixture.settle()
	click(t, fixture.tester, "Advanced options")

	if !fixture.tester.HasText("Profile no longer exists; choose another or clear it.") {
		t.Fatalf("the dangling reference is not explained; texts: %v", fixture.tester.Texts())
	}

	// Bring the Advanced group's proxy radios into the viewport before
	// tapping one; the action bar below stays reachable without scrolling.
	tt := fixture.tester
	tt.Scroll(195, 400, 0, 1200)
	tt.Frame()
	click(t, tt, "office")
	click(t, tt, "Save")
	fixture.settle()

	if len(store.updatedHosts) != 1 {
		t.Fatalf("update calls = %d, want one", len(store.updatedHosts))
	}
	draft := store.updatedHosts[0].Draft
	if draft.ProxyProfileID != "office" {
		t.Fatalf("saved Profile reference = %q, want office", draft.ProxyProfileID)
	}
	if draft.Proxy != nil {
		t.Fatalf("saving a Profile kept an inline proxy: %+v", draft.Proxy)
	}
}

// TestGroupProxyEditorWritesProfileAndInheritance drives the Host Group proxy
// editor: the group gains a command Profile through the shared Vault service,
// and its other defaults are carried through untouched.
func TestGroupProxyEditorWritesProfileAndInheritance(t *testing.T) {
	groups := []domainvault.Group{{
		ID: "g-prod", Name: "prod", Path: "prod",
		Username: "deploy", Port: 2200,
	}}
	store := &fakeGroupStore{
		fakeHostStore: &fakeHostStore{groups: groups},
		proxies: []domainvault.ProxyProfile{{
			ID: "relay", Label: "relay",
			Config: domainvault.ProxyConfig{
				Type: domainvault.ProxyCommand, Command: "relay --target %h:%p",
			},
		}},
	}
	shell := New(WithVault(store), WithHosts(store))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := openSectionPage(t, shell, Vault, "Hosts")

	click(t, tt, "Group prod")
	if !tt.HasText("Proxy for prod") {
		t.Fatalf("the group proxy editor did not open; texts: %v", tt.Texts())
	}
	click(t, tt, "relay")
	click(t, tt, "Save group proxy")

	if len(store.updates) != 1 {
		t.Fatalf("group updates = %d, want one", len(store.updates))
	}
	draft := store.updates[0]
	if draft.ProxyProfileID != "relay" || draft.DirectProxy {
		t.Fatalf("saved group proxy = %q direct=%v, want relay", draft.ProxyProfileID, draft.DirectProxy)
	}
	if draft.Username != "deploy" || draft.Port != 2200 {
		t.Fatalf("a proxy-only save changed other defaults: %+v", draft)
	}
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), "Proxy: relay") {
		t.Fatalf("the group row does not show the saved Profile; texts: %v", tt.Texts())
	}
}

// fakeGroupStore adds the Host Group write half to the Hosts fake and serves
// Proxy Profiles, the same combination the application Vault service has.
type fakeGroupStore struct {
	*fakeHostStore
	proxies []domainvault.ProxyProfile
	updates []domainvault.GroupDraft
}

func (s *fakeGroupStore) UpdateGroup(_ context.Context, id string, draft domainvault.GroupDraft) (domainvault.Group, error) {
	s.updates = append(s.updates, draft)
	for i := range s.fakeHostStore.groups {
		if s.fakeHostStore.groups[i].ID != id && s.fakeHostStore.groups[i].Path != id {
			continue
		}
		s.fakeHostStore.groups[i].ProxyProfileID = draft.ProxyProfileID
		s.fakeHostStore.groups[i].DirectProxy = draft.DirectProxy
		return s.fakeHostStore.groups[i], nil
	}
	return domainvault.Group{ID: "g-prod", Path: draft.Path, ProxyProfileID: draft.ProxyProfileID}, nil
}

func (s *fakeGroupStore) ListProxyProfiles(context.Context) ([]domainvault.ProxyProfile, error) {
	return append([]domainvault.ProxyProfile(nil), s.proxies...), nil
}

func (s *fakeGroupStore) CreateProxyProfile(context.Context, domainvault.ProxyProfileDraft) (domainvault.ProxyProfile, error) {
	return domainvault.ProxyProfile{}, nil
}

func (s *fakeGroupStore) UpdateProxyProfile(context.Context, string, domainvault.ProxyProfileDraft) (domainvault.ProxyProfile, error) {
	return domainvault.ProxyProfile{}, nil
}

func (s *fakeGroupStore) DeleteProxyProfile(context.Context, string) error { return nil }

// The VaultStore half the page family loads. Only the Proxy Profiles take part
// in this test; the other lists are empty.
func (s *fakeGroupStore) ListKeys(context.Context) ([]domainvault.Key, error) { return nil, nil }

func (s *fakeGroupStore) CreateKey(context.Context, domainvault.KeyDraft) (domainvault.Key, error) {
	return domainvault.Key{}, nil
}

func (s *fakeGroupStore) UpdateKey(context.Context, model.KeyID, domainvault.KeyDraft) (domainvault.Key, error) {
	return domainvault.Key{}, nil
}

func (s *fakeGroupStore) DeleteKey(context.Context, model.KeyID) error { return nil }

func (s *fakeGroupStore) ListIdentities(context.Context) ([]domainvault.Identity, error) {
	return nil, nil
}

func (s *fakeGroupStore) CreateIdentity(context.Context, domainvault.IdentityDraft) (domainvault.Identity, error) {
	return domainvault.Identity{}, nil
}

func (s *fakeGroupStore) UpdateIdentity(context.Context, model.IdentityID, domainvault.IdentityDraft) (domainvault.Identity, error) {
	return domainvault.Identity{}, nil
}

func (s *fakeGroupStore) DeleteIdentity(context.Context, model.IdentityID) error { return nil }

func (s *fakeGroupStore) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	return nil, nil
}

func (s *fakeGroupStore) DeleteKnownHost(context.Context, string) error { return nil }
