package vault

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type fakeGroups struct {
	items   []domainvault.Group
	deleted []string
	err     error
}

func (f *fakeGroups) ListGroups(context.Context) ([]domainvault.Group, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]domainvault.Group(nil), f.items...), nil
}

func (f *fakeGroups) GetGroup(_ context.Context, path string) (domainvault.Group, error) {
	if f.err != nil {
		return domainvault.Group{}, f.err
	}
	for _, group := range f.items {
		if group.Path == path {
			return group, nil
		}
	}
	return domainvault.Group{}, ErrNotFound
}

func (f *fakeGroups) SaveGroup(_ context.Context, group domainvault.Group) error {
	if f.err != nil {
		return f.err
	}
	for i, existing := range f.items {
		if existing.Path == group.Path {
			f.items[i] = group
			return nil
		}
	}
	f.items = append(f.items, group)
	return nil
}

func (f *fakeGroups) DeleteGroup(_ context.Context, path string) error {
	if f.err != nil {
		return f.err
	}
	for i, group := range f.items {
		if group.Path == path {
			f.items = append(f.items[:i], f.items[i+1:]...)
			f.deleted = append(f.deleted, path)
			return nil
		}
	}
	return ErrNotFound
}

type fakeProxies struct {
	items   []domainvault.ProxyProfile
	deleted []string
	err     error
	nextID  int
}

func (f *fakeProxies) ListProxyProfiles(context.Context) ([]domainvault.ProxyProfile, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]domainvault.ProxyProfile(nil), f.items...), nil
}

func (f *fakeProxies) GetProxyProfile(_ context.Context, id string) (domainvault.ProxyProfile, error) {
	if f.err != nil {
		return domainvault.ProxyProfile{}, f.err
	}
	for _, profile := range f.items {
		if profile.ID == id {
			return profile, nil
		}
	}
	return domainvault.ProxyProfile{}, ErrNotFound
}

func (f *fakeProxies) SaveProxyProfile(_ context.Context, profile domainvault.ProxyProfile) error {
	if f.err != nil {
		return f.err
	}
	for i, existing := range f.items {
		if existing.ID == profile.ID {
			f.items[i] = profile
			return nil
		}
	}
	f.items = append(f.items, profile)
	return nil
}

func (f *fakeProxies) DeleteProxyProfile(_ context.Context, id string) error {
	if f.err != nil {
		return f.err
	}
	for i, profile := range f.items {
		if profile.ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			f.deleted = append(f.deleted, id)
			return nil
		}
	}
	return ErrNotFound
}

type fakeKnownHosts struct {
	items   []domainconn.KnownHost
	deleted []string
	err     error
}

func (f *fakeKnownHosts) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]domainconn.KnownHost(nil), f.items...), nil
}

func (f *fakeKnownHosts) DeleteKnownHost(_ context.Context, id string) error {
	if f.err != nil {
		return f.err
	}
	for i, record := range f.items {
		if record.ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			f.deleted = append(f.deleted, id)
			return nil
		}
	}
	return ErrNotFound
}

func newEntityService(t *testing.T, hosts *fakeHosts, groups *fakeGroups,
	proxies *fakeProxies, known *fakeKnownHosts) *Service {
	t.Helper()
	var ids int
	return NewService(hosts,
		WithIDGenerator(func() model.HostID {
			ids++
			return model.HostID("host-" + strconv.Itoa(ids))
		}),
		WithClock(func() time.Time { return time.Unix(1700000000, 0).UTC() }),
		WithGroups(groups),
		WithProxyProfiles(proxies),
		WithKnownHosts(known),
		WithProxyProfileIDGenerator(func() string {
			ids++
			return "proxy-" + strconv.Itoa(ids)
		}),
	)
}

func TestCreateGroupNormalizesAndPersists(t *testing.T) {
	repo := &fakeHosts{}
	groups := &fakeGroups{}
	service := newEntityService(t, repo, groups, &fakeProxies{}, &fakeKnownHosts{})

	group, err := service.CreateGroup(context.Background(), domainvault.GroupDraft{
		Path:           " prod\\web ",
		Username:       " deploy ",
		Port:           2222,
		ProxyProfileID: " proxy-1 ",
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if group.Path != "prod/web" || group.Username != "deploy" ||
		group.Port != 2222 || group.ProxyProfileID != "proxy-1" {
		t.Fatalf("group = %+v", group)
	}
	if group.OrderKey != 1000 {
		t.Fatalf("OrderKey = %d, want 1000", group.OrderKey)
	}
	if len(groups.items) != 1 {
		t.Fatalf("saved groups = %d, want 1", len(groups.items))
	}

	if _, err := service.CreateGroup(context.Background(), domainvault.GroupDraft{Path: "prod/web"}); err == nil {
		t.Fatal("CreateGroup accepted a duplicate path")
	} else {
		var typed *Error
		if !errors.As(err, &typed) || typed.Kind != KindConflict {
			t.Fatalf("duplicate error = %v, want conflict", err)
		}
	}
}

func TestDeleteGroupMovesHostsToRootAndKeepsThem(t *testing.T) {
	repo := &fakeHosts{hosts: []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22, Protocol: domainvault.ProtocolSSH, Group: "prod/web"},
		{ID: "host-2", Label: "db", Hostname: "db.example.com", Port: 22, Protocol: domainvault.ProtocolSSH, Group: "dev"},
	}}
	groups := &fakeGroups{items: []domainvault.Group{
		{Path: "prod"},
		{Path: "prod/web"},
		{Path: "prod/web/internal"},
		{Path: "dev"},
	}}
	service := newEntityService(t, repo, groups, &fakeProxies{}, &fakeKnownHosts{})

	if err := service.DeleteGroup(context.Background(), "prod/web"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if len(repo.hosts) != 2 {
		t.Fatalf("Hosts after group delete = %d, want both kept", len(repo.hosts))
	}
	if repo.hosts[0].Group != "" {
		t.Fatalf("Host group = %q, want the Vault root", repo.hosts[0].Group)
	}
	if repo.hosts[1].Group != "dev" {
		t.Fatalf("unrelated Host group = %q, want dev", repo.hosts[1].Group)
	}
	paths := map[string]bool{}
	for _, group := range groups.items {
		paths[group.Path] = true
	}
	if paths["prod/web"] || paths["prod/web/internal"] {
		t.Fatalf("groups after delete = %v, want prod/web and its descendant removed", paths)
	}
	if !paths["prod"] || !paths["dev"] {
		t.Fatalf("groups after delete = %v, want ancestors and siblings kept", paths)
	}
}

func TestAssignHostGroupMovesOneHostAndRejectsAnUnknownGroup(t *testing.T) {
	repo := &fakeHosts{hosts: []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22, Protocol: domainvault.ProtocolSSH},
	}}
	groups := &fakeGroups{items: []domainvault.Group{{Path: "prod"}}}
	service := newEntityService(t, repo, groups, &fakeProxies{}, &fakeKnownHosts{})

	if err := service.AssignHostGroup(context.Background(), "host-1", " prod/ "); err != nil {
		t.Fatalf("AssignHostGroup: %v", err)
	}
	if repo.hosts[0].Group != "prod" {
		t.Fatalf("Group = %q, want prod", repo.hosts[0].Group)
	}
	if repo.saves != 1 {
		t.Fatalf("saves = %d, want 1", repo.saves)
	}

	err := service.AssignHostGroup(context.Background(), "host-1", "missing")
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindValidation {
		t.Fatalf("unknown group error = %v, want validation", err)
	}
	if repo.hosts[0].Group != "prod" {
		t.Fatal("a rejected move changed the Host")
	}

	if err := service.AssignHostGroup(context.Background(), "host-1", ""); err != nil {
		t.Fatalf("AssignHostGroup root: %v", err)
	}
	if repo.hosts[0].Group != "" {
		t.Fatalf("Group = %q, want root", repo.hosts[0].Group)
	}
}

// TestAssignHostGroupAcceptsAnImplicitGroupPath covers the Vault list's
// visible drop targets: a path Hosts already reference has no saved Group
// record, but moving a Host there must agree with what the list displays.
func TestAssignHostGroupAcceptsAnImplicitGroupPath(t *testing.T) {
	repo := &fakeHosts{hosts: []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22,
			Protocol: domainvault.ProtocolSSH, Group: "prod/web"},
		{ID: "host-2", Label: "db", Hostname: "db.example.com", Port: 22,
			Protocol: domainvault.ProtocolSSH},
	}}
	groups := &fakeGroups{items: []domainvault.Group{{Path: "prod"}}}
	service := newEntityService(t, repo, groups, &fakeProxies{}, &fakeKnownHosts{})

	if err := service.AssignHostGroup(context.Background(), "host-2", "prod/web"); err != nil {
		t.Fatalf("AssignHostGroup to an implicit group: %v", err)
	}
	if repo.hosts[1].Group != "prod/web" {
		t.Fatalf("Group = %q, want the implicit path", repo.hosts[1].Group)
	}
}

// TestAssignHostGroupAcceptsALevelAboveASavedGroup covers the card the Vault
// lists for prod when only prod/web is saved: dropping a Host on it moves the
// Host there, while a path nothing leads to stays invalid.
func TestAssignHostGroupAcceptsALevelAboveASavedGroup(t *testing.T) {
	repo := &fakeHosts{hosts: []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22,
			Protocol: domainvault.ProtocolSSH},
	}}
	groups := &fakeGroups{items: []domainvault.Group{{Path: "prod/web"}}}
	service := newEntityService(t, repo, groups, &fakeProxies{}, &fakeKnownHosts{})

	if err := service.AssignHostGroup(context.Background(), "host-1", "prod"); err != nil {
		t.Fatalf("AssignHostGroup to the level above a saved group: %v", err)
	}
	if repo.hosts[0].Group != "prod" {
		t.Fatalf("Group = %q, want prod", repo.hosts[0].Group)
	}
	if err := service.AssignHostGroup(context.Background(), "host-1", "staging"); err == nil {
		t.Fatal("AssignHostGroup to a path nothing leads to succeeded")
	}
}

func TestGroupDefaultsApplyToAnEffectiveHostAndHostWins(t *testing.T) {
	repo := &fakeHosts{hosts: []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22,
			Protocol: domainvault.ProtocolSSH, Group: "prod/web",
			IdentityID: "identity-own"},
	}}
	groups := &fakeGroups{items: []domainvault.Group{
		{Path: "prod", Username: "deploy", Port: 2222, IdentityID: "identity-group",
			UseSSHAgent: true},
	}}
	service := newEntityService(t, repo, groups, &fakeProxies{}, &fakeKnownHosts{})

	effective, err := service.Get(context.Background(), "host-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// The Host owns an Identity, so the group's manual username must not be
	// mixed into its credential bundle; unrelated defaults still apply.
	if effective.Username != "" || effective.Port != 22 {
		t.Fatalf("effective = %+v, want an unmixed identity and the Host port", effective)
	}
	if effective.IdentityID != "identity-own" {
		t.Fatalf("IdentityID = %q, want the Host's own", effective.IdentityID)
	}
	if !effective.UseSSHAgent {
		t.Fatal("the group's UseSSHAgent default did not apply")
	}
	if repo.hosts[0].Username != "" || repo.hosts[0].UseSSHAgent {
		t.Fatalf("stored Host changed: %+v", repo.hosts[0])
	}

	// With no credential of its own, the same Host inherits the group username.
	repo.hosts[0].IdentityID = ""
	effective, err = service.Get(context.Background(), "host-1")
	if err != nil {
		t.Fatalf("Get without an own Identity: %v", err)
	}
	if effective.Username != "deploy" || effective.Port != 22 {
		t.Fatalf("effective = %+v, want the group username and the Host port", effective)
	}
}

func TestAnEmptyHostPortInheritsTheGroupPort(t *testing.T) {
	repo := &fakeHosts{hosts: []domainvault.Host{
		{ID: "inherited", Label: "inherited", Hostname: "one.example.com",
			Protocol: domainvault.ProtocolSSH, Group: "prod"},
		{ID: "own", Label: "own", Hostname: "two.example.com", Port: 2200,
			Protocol: domainvault.ProtocolSSH, Group: "prod"},
		{ID: "root", Label: "root", Hostname: "three.example.com",
			Protocol: domainvault.ProtocolSSH},
	}}
	groups := &fakeGroups{items: []domainvault.Group{{Path: "prod", Port: 2222}}}
	service := newEntityService(t, repo, groups, &fakeProxies{}, &fakeKnownHosts{})

	inherited, err := service.Get(context.Background(), "inherited")
	if err != nil {
		t.Fatalf("Get inherited: %v", err)
	}
	if inherited.Port != 2222 {
		t.Fatalf("inherited port = %d, want the group's 2222", inherited.Port)
	}
	own, err := service.Get(context.Background(), "own")
	if err != nil {
		t.Fatalf("Get own: %v", err)
	}
	if own.Port != 2200 {
		t.Fatalf("own port = %d, want the Host's 2200 to win", own.Port)
	}
	root, err := service.Get(context.Background(), "root")
	if err != nil {
		t.Fatalf("Get root: %v", err)
	}
	if root.Port != 22 {
		t.Fatalf("root port = %d, want the SSH default 22", root.Port)
	}
	if repo.hosts[0].Port != 0 || repo.hosts[2].Port != 0 {
		t.Fatalf("stored ports changed: %+v", repo.hosts)
	}
}

// TestABlankPortAndAnExplicitDefaultAreNotDuplicatesInAPortGroup guards the
// duplicate check: the blank Host resolves to the group's 2222 while the
// explicit Host resolves to 22, so rejecting the second as a duplicate would
// make a valid endpoint unreachable.
func TestABlankPortAndAnExplicitDefaultAreNotDuplicatesInAPortGroup(t *testing.T) {
	repo := &fakeHosts{}
	groups := &fakeGroups{items: []domainvault.Group{{Path: "prod", Port: 2222}}}
	service := newEntityService(t, repo, groups, &fakeProxies{}, &fakeKnownHosts{})

	inherited, err := service.Create(context.Background(), domainvault.Draft{
		Label: "inherited", Hostname: "example.com", Username: "root", Group: "prod",
	})
	if err != nil {
		t.Fatalf("Create inherited: %v", err)
	}
	explicit, err := service.Create(context.Background(), domainvault.Draft{
		Label: "explicit", Hostname: "example.com", Port: 22, Username: "root", Group: "prod",
	})
	if err != nil {
		t.Fatalf("Create explicit: %v", err)
	}

	effectiveInherited, err := service.Get(context.Background(), inherited.ID)
	if err != nil {
		t.Fatalf("Get inherited: %v", err)
	}
	if effectiveInherited.Port != 2222 {
		t.Fatalf("inherited port = %d, want the group's 2222", effectiveInherited.Port)
	}
	effectiveExplicit, err := service.Get(context.Background(), explicit.ID)
	if err != nil {
		t.Fatalf("Get explicit: %v", err)
	}
	if effectiveExplicit.Port != 22 {
		t.Fatalf("explicit port = %d, want the Host's 22", effectiveExplicit.Port)
	}
	if len(repo.hosts) != 2 {
		t.Fatalf("hosts = %d, want both endpoints", len(repo.hosts))
	}
}

func TestRenameGroupRemapsHostsAndDescendantGroups(t *testing.T) {
	repo := &fakeHosts{hosts: []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22, Protocol: domainvault.ProtocolSSH, Group: "prod/web"},
		{ID: "host-2", Label: "db", Hostname: "db.example.com", Port: 22, Protocol: domainvault.ProtocolSSH, Group: "prod/web/internal"},
	}}
	groups := &fakeGroups{items: []domainvault.Group{
		{Path: "prod/web", Username: "old"},
		{Path: "prod/web/internal"},
	}}
	service := newEntityService(t, repo, groups, &fakeProxies{}, &fakeKnownHosts{})

	updated, err := service.UpdateGroup(context.Background(), "prod/web", domainvault.GroupDraft{
		Path: "prod/app", Username: "new",
	})
	if err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	if updated.Path != "prod/app" || updated.Username != "new" {
		t.Fatalf("updated group = %+v", updated)
	}
	if repo.hosts[0].Group != "prod/app" || repo.hosts[1].Group != "prod/app/internal" {
		t.Fatalf("Host groups = %q / %q", repo.hosts[0].Group, repo.hosts[1].Group)
	}
	paths := map[string]bool{}
	for _, group := range groups.items {
		paths[group.Path] = true
	}
	if paths["prod/web"] || !paths["prod/app"] || !paths["prod/app/internal"] {
		t.Fatalf("groups after rename = %v", paths)
	}

	_, err = service.UpdateGroup(context.Background(), "prod/app", domainvault.GroupDraft{Path: "prod/app/internal/deeper"})
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindValidation {
		t.Fatalf("moving a group inside itself = %v, want validation", err)
	}
}

func TestProxyProfileCRUDAndDeleteClearsReferences(t *testing.T) {
	repo := &fakeHosts{hosts: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH, ProxyProfileID: "proxy-1",
	}}}
	groups := &fakeGroups{items: []domainvault.Group{{Path: "prod", ProxyProfileID: "proxy-1"}}}
	proxies := &fakeProxies{}
	service := newEntityService(t, repo, groups, proxies, &fakeKnownHosts{})

	profile, err := service.CreateProxyProfile(context.Background(), domainvault.ProxyProfileDraft{
		Label:  " office ",
		Config: domainvault.ProxyConfig{Type: domainvault.ProxyHTTP, Host: " proxy.example.com ", Port: 8080},
	})
	if err != nil {
		t.Fatalf("CreateProxyProfile: %v", err)
	}
	if profile.Label != "office" || profile.Config.Host != "proxy.example.com" ||
		profile.Config.Port != 8080 {
		t.Fatalf("profile = %+v", profile)
	}

	profile, err = service.UpdateProxyProfile(context.Background(), profile.ID, domainvault.ProxyProfileDraft{
		Label:  "office socks",
		Config: domainvault.ProxyConfig{Type: domainvault.ProxySOCKS5, Host: "socks.example.com", Port: 1080},
	})
	if err != nil {
		t.Fatalf("UpdateProxyProfile: %v", err)
	}
	if profile.Label != "office socks" || profile.Config.Type != domainvault.ProxySOCKS5 {
		t.Fatalf("updated profile = %+v", profile)
	}

	// Point the fixtures at the real generated id before deleting.
	repo.hosts[0].ProxyProfileID = profile.ID
	groups.items[0].ProxyProfileID = profile.ID
	if err := service.DeleteProxyProfile(context.Background(), profile.ID); err != nil {
		t.Fatalf("DeleteProxyProfile: %v", err)
	}
	if len(proxies.deleted) != 1 || proxies.deleted[0] != profile.ID {
		t.Fatalf("deleted = %v", proxies.deleted)
	}
	if repo.hosts[0].ProxyProfileID != "" || groups.items[0].ProxyProfileID != "" {
		t.Fatalf("references after delete = %q / %q",
			repo.hosts[0].ProxyProfileID, groups.items[0].ProxyProfileID)
	}
	if len(repo.hosts) != 1 {
		t.Fatal("deleting a Proxy Profile deleted a Host")
	}
}

func TestEffectiveHostMaterializesTheSelectedProxyProfile(t *testing.T) {
	repo := &fakeHosts{hosts: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH, ProxyProfileID: "proxy-1",
	}}}
	proxies := &fakeProxies{items: []domainvault.ProxyProfile{{
		ID: "proxy-1", Label: "office",
		Config: domainvault.ProxyConfig{Type: domainvault.ProxySOCKS5, Host: "socks.example.com", Port: 1080},
	}}}
	service := newEntityService(t, repo, &fakeGroups{}, proxies, &fakeKnownHosts{})

	effective, err := service.Get(context.Background(), "host-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if effective.Proxy == nil || effective.Proxy.Type != domainvault.ProxySOCKS5 ||
		effective.Proxy.Host != "socks.example.com" || effective.Proxy.Port != 1080 {
		t.Fatalf("effective Proxy = %+v, want the selected profile", effective.Proxy)
	}
	if repo.hosts[0].Proxy != nil {
		t.Fatal("materializing a profile wrote it onto the stored Host")
	}

	repo.hosts[0].Proxy = &domainvault.ProxyConfig{
		Type: domainvault.ProxyHTTP, Host: "manual.example.com", Port: 3128,
	}
	effective, err = service.Get(context.Background(), "host-1")
	if err != nil {
		t.Fatalf("Get with manual proxy: %v", err)
	}
	if effective.Proxy == nil || effective.Proxy.Host != "manual.example.com" {
		t.Fatalf("effective Proxy = %+v, want the manual proxy to win", effective.Proxy)
	}
}

func TestKnownHostsListAndDelete(t *testing.T) {
	known := &fakeKnownHosts{items: []domainconn.KnownHost{
		{ID: "kh-1", Hostname: "example.com", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "abc"},
	}}
	service := newEntityService(t, &fakeHosts{}, &fakeGroups{}, &fakeProxies{}, known)

	records, err := service.ListKnownHosts(context.Background())
	if err != nil {
		t.Fatalf("ListKnownHosts: %v", err)
	}
	if len(records) != 1 || records[0].ID != "kh-1" {
		t.Fatalf("Known Hosts = %+v", records)
	}
	if err := service.DeleteKnownHost(context.Background(), "kh-1"); err != nil {
		t.Fatalf("DeleteKnownHost: %v", err)
	}
	if len(known.items) != 0 {
		t.Fatal("DeleteKnownHost left the record")
	}
	if err := service.DeleteKnownHost(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing delete = %v, want ErrNotFound", err)
	}
}
