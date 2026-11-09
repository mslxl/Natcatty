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
	// Mirror the store: identity and tree shape are stored, paths derive.
	materialized := domainvault.MaterializeGroupPaths(append([]domainvault.Group(nil), f.items...))
	// Adopt the derived identity of a fixture that predates IDs, so a later
	// SaveGroup updates that record instead of adding a second one.
	for _, group := range materialized {
		for index := range f.items {
			if f.items[index].ID == "" && domainvault.NormalizeGroup(f.items[index].Path) == group.Path {
				f.items[index] = group
			}
		}
	}
	return materialized, nil
}

func (f *fakeGroups) GetGroup(ctx context.Context, id string) (domainvault.Group, error) {
	groups, err := f.ListGroups(ctx)
	if err != nil {
		return domainvault.Group{}, err
	}
	for _, group := range groups {
		if group.ID == id {
			return group, nil
		}
	}
	return domainvault.Group{}, ErrNotFound
}

func (f *fakeGroups) SaveGroup(_ context.Context, group domainvault.Group) error {
	if f.err != nil {
		return f.err
	}
	if group.ID == "" {
		group.ID = domainvault.GroupIDFromPath(group.Path)
	}
	for i, existing := range f.items {
		if existing.ID == group.ID {
			f.items[i] = group
			return nil
		}
	}
	// A fixture written before IDs existed has its identity derived from its
	// path; adopt the record instead of adding a second one.
	for i, existing := range f.items {
		if existing.ID == "" && domainvault.NormalizeGroup(existing.Path) == domainvault.NormalizeGroup(group.Path) {
			f.items[i] = group
			return nil
		}
	}
	f.items = append(f.items, group)
	return nil
}

func (f *fakeGroups) DeleteGroup(_ context.Context, id string) error {
	if f.err != nil {
		return f.err
	}
	for i, group := range f.items {
		if group.ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			f.deleted = append(f.deleted, id)
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
	// The parent level is created first, so it takes the first order slot.
	if group.OrderKey != 2000 {
		t.Fatalf("OrderKey = %d, want 2000", group.OrderKey)
	}
	if len(groups.items) != 2 {
		t.Fatalf("saved groups = %d, want the parent and the group", len(groups.items))
	}
	parent, ok := domainvault.GroupByPath(groups.items, "prod")
	if !ok || parent.ID == "" {
		t.Fatalf("parent record = %+v, want a stable prod identity", parent)
	}
	if group.ID == "" || group.Name != "web" || group.ParentID != parent.ID {
		t.Fatalf("group identity = %+v, want a stable ID under prod", group)
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
		{ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22, Protocol: domainvault.ProtocolSSH,
			Group: "prod/web", GroupID: "g-web"},
		{ID: "host-2", Label: "db", Hostname: "db.example.com", Port: 22, Protocol: domainvault.ProtocolSSH,
			Group: "dev", GroupID: "g-dev"},
	}}
	groups := &fakeGroups{items: []domainvault.Group{
		{ID: "g-prod", Name: "prod", Path: "prod"},
		{ID: "g-web", ParentID: "g-prod", Name: "web", Path: "prod/web"},
		{ID: "g-internal", ParentID: "g-web", Name: "internal", Path: "prod/web/internal"},
		{ID: "g-dev", Name: "dev", Path: "dev"},
	}}
	service := newEntityService(t, repo, groups, &fakeProxies{}, &fakeKnownHosts{})

	// Deleting addresses the group by its stable ID, never by its path.
	if err := service.DeleteGroup(context.Background(), "g-web"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if len(repo.hosts) != 2 {
		t.Fatalf("Hosts after group delete = %d, want both kept", len(repo.hosts))
	}
	if repo.hosts[0].GroupID != "" || repo.hosts[0].Group != "" {
		t.Fatalf("Host membership = %q/%q, want the Vault root", repo.hosts[0].GroupID, repo.hosts[0].Group)
	}
	if repo.hosts[1].GroupID != "g-dev" {
		t.Fatalf("unrelated Host group = %q, want g-dev", repo.hosts[1].GroupID)
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
	groups := &fakeGroups{items: []domainvault.Group{{ID: "g-prod", Name: "prod", Path: "prod"}}}
	service := newEntityService(t, repo, groups, &fakeProxies{}, &fakeKnownHosts{})

	if err := service.AssignHostGroup(context.Background(), "host-1", "g-prod"); err != nil {
		t.Fatalf("AssignHostGroup: %v", err)
	}
	if repo.hosts[0].GroupID != "g-prod" {
		t.Fatalf("GroupID = %q, want g-prod", repo.hosts[0].GroupID)
	}
	if repo.saves != 1 {
		t.Fatalf("saves = %d, want 1", repo.saves)
	}

	err := service.AssignHostGroup(context.Background(), "host-1", "missing")
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindValidation {
		t.Fatalf("unknown group error = %v, want validation", err)
	}
	if repo.hosts[0].GroupID != "g-prod" {
		t.Fatal("a rejected move changed the Host")
	}

	if err := service.AssignHostGroup(context.Background(), "host-1", ""); err != nil {
		t.Fatalf("AssignHostGroup root: %v", err)
	}
	if repo.hosts[0].GroupID != "" {
		t.Fatalf("GroupID = %q, want the Vault root", repo.hosts[0].GroupID)
	}
}

// A path is not an identity: the API refuses one, so a caller cannot invent a
// membership that no tree node owns.
func TestAssignHostGroupRejectsAPathInsteadOfAnID(t *testing.T) {
	repo := &fakeHosts{hosts: []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22, Protocol: domainvault.ProtocolSSH},
	}}
	groups := &fakeGroups{items: []domainvault.Group{{ID: "g-prod", Name: "prod", Path: "prod"}}}
	service := newEntityService(t, repo, groups, &fakeProxies{}, &fakeKnownHosts{})

	err := service.AssignHostGroup(context.Background(), "host-1", "prod")
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindValidation {
		t.Fatalf("AssignHostGroup(path) = %v, want validation", err)
	}
	if repo.hosts[0].GroupID != "" {
		t.Fatal("a rejected move changed the Host")
	}
}

// A rename updates the group row alone: members keep their stable ID and the
// tree derives the new path, so no Host is rewritten.
func TestRenameKeepsMembershipAndMovesNoHost(t *testing.T) {
	repo := &fakeHosts{hosts: []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22,
			Protocol: domainvault.ProtocolSSH, GroupID: "g-web"},
	}}
	groups := &fakeGroups{items: []domainvault.Group{
		{ID: "g-prod", Name: "prod", Path: "prod"},
		{ID: "g-web", ParentID: "g-prod", Name: "web", Path: "prod/web"},
	}}
	service := newEntityService(t, repo, groups, &fakeProxies{}, &fakeKnownHosts{})
	saves := repo.saves

	if _, err := service.UpdateGroup(context.Background(), "g-web", domainvault.GroupDraft{Path: "prod/app"}); err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	if repo.saves != saves {
		t.Fatalf("the rename wrote %d Hosts", repo.saves-saves)
	}
	if repo.hosts[0].GroupID != "g-web" {
		t.Fatalf("membership = %q, want the same stable ID", repo.hosts[0].GroupID)
	}
	host, err := service.Get(context.Background(), "host-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if host.Group != "prod/app" {
		t.Fatalf("derived path = %q, want prod/app", host.Group)
	}
}

func TestGroupDefaultsApplyToAnEffectiveHostAndHostWins(t *testing.T) {
	repo := &fakeHosts{hosts: []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22,
			Protocol: domainvault.ProtocolSSH, Group: "prod/web", GroupID: "g-web",
			IdentityID: "identity-own"},
	}}
	groups := &fakeGroups{items: []domainvault.Group{
		{ID: "g-prod", Name: "prod", Path: "prod", Username: "deploy", Port: 2222,
			IdentityID: "identity-group", UseSSHAgent: true},
		{ID: "g-web", ParentID: "g-prod", Name: "web", Path: "prod/web"},
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
		{ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22,
			Protocol: domainvault.ProtocolSSH, Group: "prod/web", GroupID: "g-web"},
		{ID: "host-2", Label: "db", Hostname: "db.example.com", Port: 22,
			Protocol: domainvault.ProtocolSSH, Group: "prod/web/internal", GroupID: "g-deep"},
	}}
	groups := &fakeGroups{items: []domainvault.Group{
		{ID: "g-web", Name: "web", Path: "prod/web", Username: "old"},
		{ID: "g-deep", ParentID: "g-web", Name: "internal", Path: "prod/web/internal"},
	}}
	service := newEntityService(t, repo, groups, &fakeProxies{}, &fakeKnownHosts{})

	updated, err := service.UpdateGroup(context.Background(), "g-web", domainvault.GroupDraft{
		Path: "prod/app", Username: "new",
	})
	if err != nil {
		t.Fatalf("UpdateGroup: %v", err)
	}
	parent, ok := domainvault.GroupByPath(groups.items, "prod")
	if !ok || updated.Path != "prod/app" || updated.Username != "new" ||
		updated.ID != "g-web" || updated.Name != "app" || updated.ParentID != parent.ID {
		t.Fatalf("updated group = %+v, want the same ID under the new name", updated)
	}
	// Storage keeps only identity: no Host row is rewritten by the rename.
	if repo.saves != 0 {
		t.Fatalf("the rename wrote %d Hosts", repo.saves)
	}
	if repo.hosts[0].GroupID != "g-web" || repo.hosts[1].GroupID != "g-deep" {
		t.Fatalf("Host group IDs = %q / %q, want them unchanged",
			repo.hosts[0].GroupID, repo.hosts[1].GroupID)
	}
	// Paths derive from the tree, so the descendant follows automatically.
	materialized := domainvault.MaterializeGroupPaths(groups.items)
	byPath := map[string]domainvault.Group{}
	for _, group := range materialized {
		byPath[group.Path] = group
	}
	if _, exists := byPath["prod/web"]; exists {
		t.Fatalf("groups after rename = %v, want the old path removed", byPath)
	}
	renamed, ok := byPath["prod/app"]
	if !ok || renamed.ID != "g-web" || renamed.Name != "app" {
		t.Fatalf("renamed group = %+v", renamed)
	}
	descendant, ok := byPath["prod/app/internal"]
	if !ok || descendant.ID != "g-deep" || descendant.ParentID != "g-web" ||
		descendant.Name != "internal" {
		t.Fatalf("descendant group = %+v", descendant)
	}
	// A Host still resolves the moved path through its stable ID.
	host, err := service.Get(context.Background(), "host-1")
	if err != nil {
		t.Fatalf("Get after rename: %v", err)
	}
	if host.Group != "prod/app" {
		t.Fatalf("Host path = %q, want prod/app", host.Group)
	}

	_, err = service.UpdateGroup(context.Background(), "g-web", domainvault.GroupDraft{Path: "prod/app/internal/deeper"})
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
