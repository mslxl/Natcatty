package phoneshell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// fakeEditorStore is the Vault write surface the Host editor binds. It is the
// same seam the composition root passes (application/vault.Service): Host
// CRUD, the Key, Identity, and Known Host lists, and the credential source the
// Copy action reads.
type fakeEditorStore struct {
	*fakeHostStore

	keys       []domainvault.Key
	identities []domainvault.Identity
	known      []domainconn.KnownHost

	credentials   map[model.HostID]appvault.HostCredentials
	credentialErr map[model.HostID]error

	createErr error
	updateErr error
	deleteErr error

	// failListAfterWrite makes every List after a successful write fail, which
	// is the save-succeeded-but-refresh-failed case.
	failListAfterWrite bool
	wrote              bool
	listCalls          int

	createdDrafts []domainvault.Draft
	createdCount  int
	updatedHosts  []editorHostUpdate
	deletedHosts  []model.HostID
}

type editorHostUpdate struct {
	ID    model.HostID
	Draft domainvault.Draft
}

func newFakeEditorStore(hosts []domainvault.Host, groups []domainvault.Group) *fakeEditorStore {
	return &fakeEditorStore{
		fakeHostStore: &fakeHostStore{hosts: hosts, groups: groups},
		credentials:   map[model.HostID]appvault.HostCredentials{},
		credentialErr: map[model.HostID]error{},
	}
}

func (s *fakeEditorStore) List(ctx context.Context) ([]domainvault.Host, error) {
	s.listCalls++
	if s.failListAfterWrite && s.wrote {
		return nil, errors.New("vault: list unavailable")
	}
	return s.fakeHostStore.List(ctx)
}

func (s *fakeEditorStore) Create(_ context.Context, draft domainvault.Draft) (domainvault.Host, error) {
	s.createdDrafts = append(s.createdDrafts, draft)
	normalized, err := domainvault.NormalizeDraft(draft)
	if err != nil {
		return domainvault.Host{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	draft = normalized
	if s.createErr != nil {
		return domainvault.Host{}, s.createErr
	}
	s.createdCount++
	host := domainvault.Host{
		ID:               model.HostID(fmt.Sprintf("created-%d", s.createdCount)),
		Label:            draft.Label,
		Hostname:         draft.Hostname,
		Port:             draft.Port,
		Username:         draft.Username,
		Protocol:         draft.Protocol,
		Group:            draft.Group,
		Tags:             draft.Tags,
		Notes:            draft.Notes,
		Pinned:           draft.Pinned,
		StartupCommand:   draft.StartupCommand,
		IdentityID:       draft.IdentityID,
		KeyID:            draft.KeyID,
		AuthMethod:       draft.AuthMethod,
		UseSSHAgent:      draft.UseSSHAgent,
		RequiresMFA:      draft.RequiresMFA,
		HasPassword:      draft.Password != "",
		HasTOTP:          draft.TOTP != "",
		ProxyProfileID:   draft.ProxyProfileID,
		NetworkProfileID: draft.NetworkProfileID,
	}
	if host.Label == "" {
		host.Label = host.Hostname
	}
	s.hosts = append(s.hosts, host)
	s.wrote = true
	return host, nil
}

func (s *fakeEditorStore) Update(_ context.Context, id model.HostID, draft domainvault.Draft) (domainvault.Host, error) {
	s.updatedHosts = append(s.updatedHosts, editorHostUpdate{ID: id, Draft: draft})
	normalized, err := domainvault.NormalizeDraft(draft)
	if err != nil {
		return domainvault.Host{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	draft = normalized
	if s.updateErr != nil {
		return domainvault.Host{}, s.updateErr
	}
	for i := range s.hosts {
		if s.hosts[i].ID != id {
			continue
		}
		host := s.hosts[i]
		host.Label = draft.Label
		host.Hostname = draft.Hostname
		host.Port = draft.Port
		host.Username = draft.Username
		host.Protocol = draft.Protocol
		host.Group = draft.Group
		host.Tags = draft.Tags
		host.Notes = draft.Notes
		host.Pinned = draft.Pinned
		host.StartupCommand = draft.StartupCommand
		host.IdentityID = draft.IdentityID
		host.KeyID = draft.KeyID
		host.AuthMethod = draft.AuthMethod
		host.UseSSHAgent = draft.UseSSHAgent
		host.RequiresMFA = draft.RequiresMFA
		if draft.Password != "" {
			host.HasPassword = true
		}
		if draft.TOTP != "" {
			host.HasTOTP = true
		}
		if draft.ClearTOTP {
			host.HasTOTP = false
		}
		s.hosts[i] = host
		s.wrote = true
		return host, nil
	}
	return domainvault.Host{}, errors.New("host not found")
}

func (s *fakeEditorStore) Delete(_ context.Context, id model.HostID) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deletedHosts = append(s.deletedHosts, id)
	kept := s.hosts[:0]
	for _, host := range s.hosts {
		if host.ID != id {
			kept = append(kept, host)
		}
	}
	s.hosts = kept
	return nil
}

func (s *fakeEditorStore) ListKeys(context.Context) ([]domainvault.Key, error) {
	return append([]domainvault.Key(nil), s.keys...), nil
}

func (s *fakeEditorStore) CreateKey(context.Context, domainvault.KeyDraft) (domainvault.Key, error) {
	return domainvault.Key{}, errors.New("unused")
}

func (s *fakeEditorStore) UpdateKey(context.Context, model.KeyID, domainvault.KeyDraft) (domainvault.Key, error) {
	return domainvault.Key{}, errors.New("unused")
}

func (s *fakeEditorStore) DeleteKey(context.Context, model.KeyID) error { return nil }

func (s *fakeEditorStore) ListIdentities(context.Context) ([]domainvault.Identity, error) {
	return append([]domainvault.Identity(nil), s.identities...), nil
}

func (s *fakeEditorStore) CreateIdentity(context.Context, domainvault.IdentityDraft) (domainvault.Identity, error) {
	return domainvault.Identity{}, errors.New("unused")
}

func (s *fakeEditorStore) UpdateIdentity(context.Context, model.IdentityID, domainvault.IdentityDraft) (domainvault.Identity, error) {
	return domainvault.Identity{}, errors.New("unused")
}

func (s *fakeEditorStore) DeleteIdentity(context.Context, model.IdentityID) error { return nil }

func (s *fakeEditorStore) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	return append([]domainconn.KnownHost(nil), s.known...), nil
}

func (s *fakeEditorStore) DeleteKnownHost(context.Context, string) error { return nil }

func (s *fakeEditorStore) HostCredentials(_ context.Context, id model.HostID) (appvault.HostCredentials, error) {
	if err := s.credentialErr[id]; err != nil {
		return appvault.HostCredentials{}, err
	}
	return s.credentials[id], nil
}

// newEditorFixture builds the shell over one Vault write surface, the way the
// phone composition root does: the same service is the Host list, the Vault's
// Key and Identity lists, the Host creator, and the credential source.
func newEditorFixture(t *testing.T, hosts []domainvault.Host, groups []domainvault.Group) (*phoneFixture, *fakeEditorStore) {
	t.Helper()
	store := newFakeEditorStore(hosts, groups)
	opener := &fakeOpener{output: "editor session\r\n"}
	settings := newFakeSettings()
	shell := New(
		WithHosts(store),
		WithVault(store),
		WithHostCreator(store),
		WithSessionOpener(opener),
		WithScreenFactory(fakeScreens),
		WithSettings(settings),
	)
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	fixture := &phoneFixture{
		shell: shell, store: store.fakeHostStore, opener: opener,
		settings: settings, hosts: hosts, context: context.Background(),
	}
	shell.Select(Hosts)
	fixture.tester = ui.NewTester(func(c *ui.Context) {
		shell.View(shell.Selected())(c)
	}, 390, 844)
	fixture.tester.Frame()
	return fixture, store
}

func editorHost(id, label, hostname string) domainvault.Host {
	return domainvault.Host{
		ID: model.HostID(id), Label: label, Hostname: hostname,
		Protocol: domainvault.ProtocolSSH,
	}
}

func openNewHostEditor(t *testing.T, f *phoneFixture) {
	t.Helper()
	if err := f.tester.Click("New Host"); err != nil {
		t.Fatalf("click New Host: %v", err)
	}
	f.settle()
	if got := f.shell.Router(Hosts).Path(); got != createHostPath {
		t.Fatalf("Hosts path = %q, want %q", got, createHostPath)
	}
}

func typeInto(t *testing.T, tt *ui.Tester, label, text string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatalf("focus %q: %v", label, err)
	}
	tt.Frame()
	tt.Type(text)
	tt.Frame()
}

func click(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatalf("click %q: %v (texts %q)", label, err, tt.Texts())
	}
	tt.Frame()
}

func fillManualHost(t *testing.T, f *phoneFixture, label, hostname, user string) {
	t.Helper()
	if label != "" {
		typeInto(t, f.tester, "Label", label)
	}
	typeInto(t, f.tester, "Hostname", hostname)
	if user != "" {
		typeInto(t, f.tester, "Username", user)
	}
}

// TestHostEditorGroupsAndManualSave covers the manual editor: the basic
// connection fields are always visible, Authentication, Jump chain, and
// Advanced options are collapsible, and the primary action saves.
func TestHostEditorGroupsAndManualSave(t *testing.T) {
	fixture, store := newEditorFixture(t, []domainvault.Host{
		editorHost("prod", "prod box", "prod.example.com"),
	}, nil)
	openNewHostEditor(t, fixture)

	for _, label := range []string{"Label", "Hostname", "Port", "Username", "Protocol",
		"Authentication", "Jump chain", "Advanced options"} {
		if !fixture.tester.HasText(label) {
			t.Fatalf("editor does not show %q; texts: %q", label, fixture.tester.Texts())
		}
	}
	if fixture.tester.HasText("TOTP") {
		t.Fatalf("Authentication should start collapsed; texts: %q", fixture.tester.Texts())
	}
	click(t, fixture.tester, "Authentication")
	if !fixture.tester.HasText("TOTP") {
		t.Fatalf("Authentication did not expand; texts: %q", fixture.tester.Texts())
	}
	click(t, fixture.tester, "Authentication")
	if fixture.tester.HasText("TOTP") {
		t.Fatalf("Authentication did not collapse; texts: %q", fixture.tester.Texts())
	}

	fillManualHost(t, fixture, "staging", "staging.example.com", "deploy")
	editor := fixture.shell.hosts.editor
	if editor == nil {
		t.Fatal("the Host editor is not wired")
	}
	if editor.unmatchedLink {
		t.Fatal("the manual editor leads with Save and connect; want Save")
	}
	click(t, fixture.tester, "Save")
	fixture.settle()

	if len(store.createdDrafts) != 1 {
		t.Fatalf("create calls = %d, want 1", len(store.createdDrafts))
	}
	created := store.createdDrafts[0]
	if created.Hostname != "staging.example.com" || created.Username != "deploy" ||
		created.Label != "staging" || created.Protocol != domainvault.ProtocolSSH {
		t.Fatalf("created draft = %+v", created)
	}
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/created-1" {
		t.Fatalf("Hosts path after Save = %q, want the new Host's page", got)
	}
	if got := fixture.opener.count(); got != 0 {
		t.Fatalf("Save opened %d Session(s); want none", got)
	}
	if !fixture.tester.HasText("Connect to staging") {
		t.Fatalf("new Host page has no Connect action; texts: %q", fixture.tester.Texts())
	}
}

// TestManualHostSaveAndConnect proves the secondary action opens the newly
// saved Host.
func TestManualHostSaveAndConnect(t *testing.T) {
	fixture, _ := newEditorFixture(t, []domainvault.Host{
		editorHost("prod", "prod box", "prod.example.com"),
	}, nil)
	openNewHostEditor(t, fixture)
	fillManualHost(t, fixture, "staging", "staging.example.com", "deploy")

	click(t, fixture.tester, "Save and connect")
	fixture.await(t, "the new Host's Session", func() bool {
		return fixture.opener.count() == 1
	})
	opened := fixture.opener.opened()
	if len(opened) != 1 || opened[0].Hostname != "staging.example.com" {
		t.Fatalf("opened %+v, want the new Host", opened)
	}
	if got := fixture.shell.Selected(); got != Sessions {
		t.Fatalf("selected section = %v, want Sessions", got)
	}
}

// TestDeepLinkMatchedHostOpensEditorPrefilled covers a link naming a saved
// Host: the same record opens in the editor and saving updates it rather than
// creating a second Host.
func TestDeepLinkMatchedHostOpensEditorPrefilled(t *testing.T) {
	host := editorHost("prod", "prod box", "prod.example.com")
	host.Port = 2222
	host.Username = "deploy"
	fixture, store := newEditorFixture(t, []domainvault.Host{host}, nil)
	readyShell(fixture)

	if err := fixture.shell.HandleDeepLink("ssh://deploy@prod.example.com:2222"); err != nil {
		t.Fatalf("HandleDeepLink: %v", err)
	}
	fixture.settle()

	if got := fixture.shell.Router(Hosts).Path(); got != "/host/prod/edit" {
		t.Fatalf("Hosts path = %q, want the matched Host's editor", got)
	}
	editor := fixture.shell.hosts.editor
	form := editor.state.Form()
	if form.ID != "prod" || form.Hostname != "prod.example.com" ||
		form.Username != "deploy" || form.Port != "2222" {
		t.Fatalf("prefilled form = %+v, want the linked Host's record", form)
	}
	if editor.unmatchedLink {
		t.Fatal("a matched link leads with Save and connect; want Save")
	}
	if got := fixture.opener.count(); got != 0 {
		t.Fatalf("opening the editor opened %d Session(s)", got)
	}

	click(t, fixture.tester, "Save")
	fixture.settle()
	if len(store.createdDrafts) != 0 {
		t.Fatalf("matched link created %d Host(s); want an update", len(store.createdDrafts))
	}
	if len(store.updatedHosts) != 1 || store.updatedHosts[0].ID != "prod" {
		t.Fatalf("updates = %+v, want the prod record", store.updatedHosts)
	}
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/prod" {
		t.Fatalf("Hosts path after Save = %q, want the Host detail", got)
	}
	if got := fixture.opener.count(); got != 0 {
		t.Fatalf("Save opened %d Session(s); want none", got)
	}
}

// TestDeepLinkMatchedHostSaveAndConnect proves the secondary action on a
// matched link opens the edited record.
func TestDeepLinkMatchedHostSaveAndConnect(t *testing.T) {
	host := editorHost("prod", "prod box", "prod.example.com")
	host.Port = 2222
	host.Username = "deploy"
	fixture, _ := newEditorFixture(t, []domainvault.Host{host}, nil)
	readyShell(fixture)
	if err := fixture.shell.HandleDeepLink("ssh://deploy@prod.example.com:2222"); err != nil {
		t.Fatalf("HandleDeepLink: %v", err)
	}
	fixture.settle()

	click(t, fixture.tester, "Save and connect")
	fixture.await(t, "the edited Host's Session", func() bool {
		return fixture.opener.count() == 1
	})
	opened := fixture.opener.opened()
	if len(opened) != 1 || opened[0].ID != "prod" {
		t.Fatalf("opened %+v, want the prod Host", opened)
	}
}

// TestDeepLinkUnmatchedLeadsWithSaveAndConnect covers an unknown endpoint: the
// editor is prefilled and Save and connect is drawn first.
func TestDeepLinkUnmatchedLeadsWithSaveAndConnect(t *testing.T) {
	fixture, store := newEditorFixture(t, []domainvault.Host{
		editorHost("prod", "prod box", "prod.example.com"),
	}, nil)
	readyShell(fixture)

	if err := fixture.shell.HandleDeepLink("ssh://alice@new.example.com:2200"); err != nil {
		t.Fatalf("HandleDeepLink: %v", err)
	}
	fixture.settle()

	if got := fixture.shell.Router(Hosts).Path(); got != createHostPath {
		t.Fatalf("Hosts path = %q, want the create editor", got)
	}
	editor := fixture.shell.hosts.editor
	form := editor.state.Form()
	if form.Hostname != "new.example.com" || form.Username != "alice" || form.Port != "2200" {
		t.Fatalf("prefilled form = %+v, want the link's endpoint", form)
	}
	if !editor.unmatchedLink {
		t.Fatal("an unmatched link must lead with Save and connect")
	}
	texts := fixture.tester.Texts()
	connectAt, saveAt := -1, -1
	for i, text := range texts {
		switch text {
		case "Save and connect":
			connectAt = i
		case "Save":
			saveAt = i
		}
	}
	if connectAt < 0 || saveAt < 0 || connectAt > saveAt {
		t.Fatalf("action order in %q, want Save and connect before Save", texts)
	}

	click(t, fixture.tester, "Save and connect")
	fixture.await(t, "the linked Session", func() bool {
		return fixture.opener.count() == 1
	})
	if len(store.createdDrafts) != 1 {
		t.Fatalf("create calls = %d, want 1", len(store.createdDrafts))
	}
	if got := store.createdDrafts[0]; got.Hostname != "new.example.com" ||
		got.Username != "alice" || got.Port != 2200 {
		t.Fatalf("created draft = %+v, want the link's endpoint", got)
	}
	if opened := fixture.opener.opened(); len(opened) != 1 ||
		opened[0].Hostname != "new.example.com" {
		t.Fatalf("opened %+v, want the new Host", opened)
	}
}

// TestDeepLinkUnmatchedSaveStaysDisconnected proves the secondary action
// creates the Host without dialing it.
func TestDeepLinkUnmatchedSaveStaysDisconnected(t *testing.T) {
	fixture, store := newEditorFixture(t, []domainvault.Host{
		editorHost("prod", "prod box", "prod.example.com"),
	}, nil)
	readyShell(fixture)
	if err := fixture.shell.HandleDeepLink("ssh://alice@new.example.com:2200"); err != nil {
		t.Fatalf("HandleDeepLink: %v", err)
	}
	fixture.settle()

	click(t, fixture.tester, "Save")
	fixture.settle()
	if len(store.createdDrafts) != 1 {
		t.Fatalf("create calls = %d, want 1", len(store.createdDrafts))
	}
	if got := fixture.opener.count(); got != 0 {
		t.Fatalf("Save opened %d Session(s); want none", got)
	}
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/created-1" {
		t.Fatalf("Hosts path = %q, want the created Host", got)
	}
}

// TestDeepLinkMatchesWithInheritedPortAndUsername covers the shared effective
// Host matcher: a Host whose non-default port and username come from its Host
// Group still matches the link that names that endpoint.
func TestDeepLinkMatchesWithInheritedPortAndUsername(t *testing.T) {
	groups := []domainvault.Group{{
		ID: "g1", Path: "prod", Username: "deploy", Port: 2222,
	}}
	hosts := []domainvault.Host{{
		ID: "prod", Label: "prod box", Hostname: "prod.example.com",
		Protocol: domainvault.ProtocolSSH, GroupID: "g1", AuthMethod: domainvault.HostAuthAuto,
	}}
	fixture, _ := newEditorFixture(t, hosts, groups)
	readyShell(fixture)

	if err := fixture.shell.HandleDeepLink("ssh://deploy@prod.example.com:2222"); err != nil {
		t.Fatalf("HandleDeepLink: %v", err)
	}
	fixture.settle()
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/prod/edit" {
		t.Fatalf("Hosts path = %q, want the inherited endpoint's editor", got)
	}
	if got := fixture.shell.hosts.editor.state.Form().ID; got != "prod" {
		t.Fatalf("editor form ID = %q, want the matched record", got)
	}
}

// TestHostEditorRefreshFailureClosesAndNeverConnects is the write-succeeded
// but list-refresh-failed case: the editor closes, typed credentials are
// dropped, the list is read again, and no Session opens.
func TestHostEditorRefreshFailureClosesAndNeverConnects(t *testing.T) {
	fixture, store := newEditorFixture(t, []domainvault.Host{
		editorHost("prod", "prod box", "prod.example.com"),
	}, nil)
	store.failListAfterWrite = true
	openNewHostEditor(t, fixture)
	fillManualHost(t, fixture, "staging", "staging.example.com", "deploy")
	click(t, fixture.tester, "Authentication")
	typeInto(t, fixture.tester, "Password", "secret")
	typeInto(t, fixture.tester, "TOTP", "JBSWY3DPEHPK3PXP")

	before := store.listCalls
	click(t, fixture.tester, "Save")
	fixture.settle()

	if len(store.createdDrafts) != 1 {
		t.Fatalf("create calls = %d, want the write to have succeeded", len(store.createdDrafts))
	}
	if store.listCalls <= before {
		t.Fatalf("list reads = %d (was %d), want the list read again", store.listCalls, before)
	}
	editor := fixture.shell.hosts.editor
	if editor.state.Editing() {
		t.Fatal("the editor stayed open after a refresh failure")
	}
	form := editor.state.Form()
	if form.Password != "" || form.TOTP != "" {
		t.Fatalf("typed credentials survived: password %q totp %q", form.Password, form.TOTP)
	}
	if got := fixture.shell.Router(Hosts).Path(); got != "/" {
		t.Fatalf("Hosts path = %q, want the closed editor's root", got)
	}
	if got := fixture.opener.count(); got != 0 {
		t.Fatalf("a refresh failure connected %d Host(s); want none", got)
	}
	if !fixture.tester.HasText("Could not load Hosts.") {
		t.Fatalf("list failure is not shown; texts: %q", fixture.tester.Texts())
	}
}

// TestHostEditorValidationFailureKeepsDraft covers an invalid form: the editor
// stays open with everything the user typed, and a corrected retry saves.
func TestHostEditorValidationFailureKeepsDraft(t *testing.T) {
	fixture, store := newEditorFixture(t, []domainvault.Host{
		editorHost("prod", "prod box", "prod.example.com"),
	}, nil)
	openNewHostEditor(t, fixture)
	typeInto(t, fixture.tester, "Label", "staging")
	typeInto(t, fixture.tester, "Username", "deploy")

	click(t, fixture.tester, "Save")
	fixture.settle()
	editor := fixture.shell.hosts.editor
	if !editor.state.Editing() {
		t.Fatal("a validation failure closed the editor")
	}
	if got := editor.state.Form().Username; got != "deploy" {
		t.Fatalf("typed Username = %q, want it kept", got)
	}
	if len(store.hosts) != 1 {
		t.Fatalf("an invalid form added a Host: %+v", store.hosts)
	}
	if got := fixture.opener.count(); got != 0 {
		t.Fatalf("an invalid form connected %d Host(s)", got)
	}
	if !fixture.tester.HasText("Enter the Host's address.") {
		t.Fatalf("validation error is not shown; texts: %q", fixture.tester.Texts())
	}

	typeInto(t, fixture.tester, "Hostname", "retry.example.com")
	click(t, fixture.tester, "Save")
	fixture.settle()
	if len(store.createdDrafts) != 2 {
		t.Fatalf("attempts = %d, want the invalid try and the retry", len(store.createdDrafts))
	}
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/created-1" {
		t.Fatalf("Hosts path after retry = %q, want the new Host", got)
	}
}

// TestHostEditorCredentialLockKeepsDraft covers a locked credential store: the
// draft and typed values stay, no connection opens, and an unlocked retry
// saves.
func TestHostEditorCredentialLockKeepsDraft(t *testing.T) {
	fixture, store := newEditorFixture(t, []domainvault.Host{
		editorHost("prod", "prod box", "prod.example.com"),
	}, nil)
	store.createErr = appvault.ErrSecretsLocked
	openNewHostEditor(t, fixture)
	fillManualHost(t, fixture, "staging", "staging.example.com", "deploy")

	click(t, fixture.tester, "Save")
	fixture.settle()
	editor := fixture.shell.hosts.editor
	if !editor.state.Editing() {
		t.Fatal("a locked credential store closed the editor")
	}
	if got := editor.state.Form().Hostname; got != "staging.example.com" {
		t.Fatalf("typed Hostname = %q, want it kept", got)
	}
	if !fixture.tester.HasText("The credential store is locked. Unlock it and try again.") {
		t.Fatalf("lock error is not shown; texts: %q", fixture.tester.Texts())
	}
	if got := fixture.opener.count(); got != 0 {
		t.Fatalf("a locked credential store connected %d Host(s)", got)
	}

	store.createErr = nil
	click(t, fixture.tester, "Save")
	fixture.settle()
	if len(store.createdDrafts) != 2 {
		t.Fatalf("attempts = %d, want the failed try and the retry", len(store.createdDrafts))
	}
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/created-1" {
		t.Fatalf("Hosts path after retry = %q, want the new Host", got)
	}
}

// TestHostEditorWriteFailureKeepsDraft covers a storage failure: the same
// retained draft and retry behavior applies.
func TestHostEditorWriteFailureKeepsDraft(t *testing.T) {
	fixture, store := newEditorFixture(t, []domainvault.Host{
		editorHost("prod", "prod box", "prod.example.com"),
	}, nil)
	store.createErr = errors.New("disk full")
	openNewHostEditor(t, fixture)
	fillManualHost(t, fixture, "staging", "staging.example.com", "deploy")

	click(t, fixture.tester, "Save")
	fixture.settle()
	editor := fixture.shell.hosts.editor
	if !editor.state.Editing() {
		t.Fatal("a write failure closed the editor")
	}
	if got := editor.state.Form().Hostname; got != "staging.example.com" {
		t.Fatalf("typed Hostname = %q, want it kept", got)
	}
	if !fixture.tester.HasText("The Vault could not save the Host. Try again.") {
		t.Fatalf("write error is not shown; texts: %q", fixture.tester.Texts())
	}
	if got := fixture.opener.count(); got != 0 {
		t.Fatalf("a write failure connected %d Host(s)", got)
	}

	store.createErr = nil
	click(t, fixture.tester, "Save")
	fixture.settle()
	if len(store.createdDrafts) != 2 {
		t.Fatalf("attempts = %d, want the failed try and the retry", len(store.createdDrafts))
	}
}

// TestHostEditorInvalidTOTPExpandsAuthentication covers a failed TOTP
// submission: the collapsed Authentication group opens itself and shows the
// error where the value was typed.
func TestHostEditorInvalidTOTPExpandsAuthentication(t *testing.T) {
	fixture, store := newEditorFixture(t, []domainvault.Host{
		editorHost("prod", "prod box", "prod.example.com"),
	}, nil)
	openNewHostEditor(t, fixture)
	typeInto(t, fixture.tester, "Hostname", "staging.example.com")
	click(t, fixture.tester, "Authentication")
	typeInto(t, fixture.tester, "TOTP", "not-base32!!")
	click(t, fixture.tester, "Authentication")
	if fixture.tester.HasText("TOTP") {
		t.Fatalf("Authentication did not collapse; texts: %q", fixture.tester.Texts())
	}

	click(t, fixture.tester, "Save")
	fixture.settle()
	editor := fixture.shell.hosts.editor
	if !editor.state.Editing() {
		t.Fatal("an invalid TOTP closed the editor")
	}
	if !editor.authOpen {
		t.Fatal("an invalid TOTP left Authentication collapsed")
	}
	if !fixture.tester.HasText("TOTP") {
		t.Fatalf("Authentication is not expanded; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("The authenticator is not a valid TOTP secret or otpauth:// URI.") {
		t.Fatalf("TOTP error is not shown; texts: %q", fixture.tester.Texts())
	}
	if got := editor.state.Form().TOTP; got != "not-base32!!" {
		t.Fatalf("typed TOTP = %q, want it kept", got)
	}
	if len(store.hosts) != 1 {
		t.Fatalf("an invalid form added a Host: %+v", store.hosts)
	}
}

// TestHostDetailCopyBoundaries covers Copy hostname and Copy credentials. The
// password is trimmed at both ends, and a Host whose selected Identity has no
// password reports none instead of falling back to the Host's own password.
func TestHostDetailCopyBoundaries(t *testing.T) {
	hosts := []domainvault.Host{
		editorHost("prod", "prod box", "prod.example.com"),
		{
			ID: "ident", Label: "identity box", Hostname: "ident.example.com",
			Username: "deploy", Protocol: domainvault.ProtocolSSH,
			IdentityID: "identity-1", HasPassword: true,
		},
	}
	fixture, store := newEditorFixture(t, hosts, nil)
	store.credentials["prod"] = appvault.HostCredentials{Username: "deploy", Password: "  s3cret  "}
	// The application service returns the Identity's own password, which has
	// no fallback to the Host's. An Identity without a passphrase therefore
	// yields an empty password.
	store.credentials["ident"] = appvault.HostCredentials{Username: "deploy", Password: ""}

	click(t, fixture.tester, "Details for prod box")
	fixture.settle()
	click(t, fixture.tester, "Copy hostname")
	if got := fixture.tester.Clipboard(); got != "prod.example.com" {
		t.Fatalf("clipboard = %q, want the hostname", got)
	}
	click(t, fixture.tester, "Copy credentials")
	want := "host: prod.example.com\nusername: deploy\npassword: s3cret"
	if got := fixture.tester.Clipboard(); got != want {
		t.Fatalf("clipboard = %q, want %q (the password trimmed)", got, want)
	}

	if err := fixture.tester.Click("Back"); err != nil {
		t.Fatalf("Back to Hosts: %v", err)
	}
	fixture.settle()
	click(t, fixture.tester, "Details for identity box")
	fixture.settle()
	fixture.tester.SetClipboard("unchanged")
	click(t, fixture.tester, "Copy credentials")
	if got := fixture.tester.Clipboard(); got != "unchanged" {
		t.Fatalf("clipboard = %q, want it untouched without a password", got)
	}
	if !fixture.tester.HasText("No stored password to copy.") {
		t.Fatalf("the missing password is not explained; texts: %q", fixture.tester.Texts())
	}
	if got := strings.Join(fixture.tester.Texts(), "\n"); strings.Contains(got, "s3cret") {
		t.Fatalf("copy fell back to another Host's password: %q", got)
	}
}
