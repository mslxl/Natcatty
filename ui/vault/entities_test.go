package vault

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// entityHosts is a Host repository fake that carries the fields these tests
// exercise, including the Proxy Profile reference and the Host Group.
type entityHosts struct {
	items  []domainvault.Host
	nextID int
}

func (f *entityHosts) List(context.Context) ([]domainvault.Host, error) {
	return append([]domainvault.Host(nil), f.items...), nil
}

func (f *entityHosts) Create(_ context.Context, draft domainvault.Draft) (domainvault.Host, error) {
	normalized, err := domainvault.NormalizeDraft(draft)
	if err != nil {
		return domainvault.Host{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	f.nextID++
	host := domainvault.Host{
		ID:       model.HostID("host-" + strconv.Itoa(f.nextID)),
		Label:    normalized.Label,
		Hostname: normalized.Hostname,
		Port:     normalized.Port,
		Protocol: normalized.Protocol,
	}
	applyEntityDraft(&host, normalized)
	f.items = append(f.items, host)
	return host, nil
}

func (f *entityHosts) Update(_ context.Context, id model.HostID, draft domainvault.Draft) (domainvault.Host, error) {
	normalized, err := domainvault.NormalizeDraft(draft)
	if err != nil {
		return domainvault.Host{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	host, ok := f.find(id)
	if !ok {
		return domainvault.Host{}, appvault.ErrNotFound
	}
	applyEntityDraft(&host, normalized)
	f.replace(host)
	return host, nil
}

func (f *entityHosts) Delete(_ context.Context, id model.HostID) error {
	for i, host := range f.items {
		if host.ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			return nil
		}
	}
	return appvault.ErrNotFound
}

func (f *entityHosts) find(id model.HostID) (domainvault.Host, bool) {
	for _, host := range f.items {
		if host.ID == id {
			return host, true
		}
	}
	return domainvault.Host{}, false
}

func (f *entityHosts) replace(host domainvault.Host) {
	for i := range f.items {
		if f.items[i].ID == host.ID {
			f.items[i] = host
			return
		}
	}
	f.items = append(f.items, host)
}

func applyEntityDraft(host *domainvault.Host, draft domainvault.Draft) {
	host.Label = draft.Label
	host.Hostname = draft.Hostname
	host.Port = draft.Port
	host.Username = draft.Username
	host.Protocol = draft.Protocol
	host.Group = draft.Group
	host.Tags = draft.Tags
	host.Notes = draft.Notes
	host.Pinned = draft.Pinned
	host.IdentityID = draft.IdentityID
	host.KeyID = draft.KeyID
	host.AuthMethod = draft.AuthMethod
	host.UseSSHAgent = draft.UseSSHAgent
	host.RequiresMFA = draft.RequiresMFA
	host.ProxyProfileID = draft.ProxyProfileID
}

type entityGroups struct {
	items  []domainvault.Group
	hosts  *entityHosts
	nextID int
}

func (f *entityGroups) ListGroups(context.Context) ([]domainvault.Group, error) {
	return append([]domainvault.Group(nil), f.items...), nil
}

func (f *entityGroups) CreateGroup(_ context.Context, draft domainvault.GroupDraft) (domainvault.Group, error) {
	normalized, err := domainvault.NormalizeGroupDraft(draft)
	if err != nil {
		return domainvault.Group{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	if _, ok := f.find(normalized.Path); ok {
		return domainvault.Group{}, &appvault.Error{Kind: appvault.KindConflict}
	}
	f.nextID++
	group := domainvault.Group{Path: normalized.Path, OrderKey: int64(f.nextID) * 1000}
	applyEntityGroupDraft(&group, normalized)
	f.items = append(f.items, group)
	return group, nil
}

func (f *entityGroups) UpdateGroup(_ context.Context, path string, draft domainvault.GroupDraft) (domainvault.Group, error) {
	normalized, err := domainvault.NormalizeGroupDraft(draft)
	if err != nil {
		return domainvault.Group{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	group, ok := f.find(domainvault.NormalizeGroup(path))
	if !ok {
		return domainvault.Group{}, appvault.ErrNotFound
	}
	oldPath := group.Path
	applyEntityGroupDraft(&group, normalized)
	f.replace(group)
	if group.Path != oldPath {
		for i := range f.items {
			f.items[i].Path = domainvault.ReplaceGroupPrefix(f.items[i].Path, oldPath, group.Path)
		}
		for i := range f.hosts.items {
			f.hosts.items[i].Group = domainvault.ReplaceGroupPrefix(f.hosts.items[i].Group, oldPath, group.Path)
		}
	}
	return group, nil
}

func (f *entityGroups) DeleteGroup(_ context.Context, path string) error {
	root := domainvault.NormalizeGroup(path)
	matched := false
	kept := f.items[:0]
	for _, group := range f.items {
		if domainvault.IsGroupAtOrBelow(group.Path, root) {
			matched = true
			continue
		}
		kept = append(kept, group)
	}
	f.items = kept
	for i := range f.hosts.items {
		if domainvault.IsGroupAtOrBelow(f.hosts.items[i].Group, root) {
			f.hosts.items[i].Group = ""
			matched = true
		}
	}
	if !matched {
		return appvault.ErrNotFound
	}
	return nil
}

func (f *entityGroups) AssignHostGroup(_ context.Context, id model.HostID, groupPath string) error {
	path := domainvault.NormalizeGroup(groupPath)
	if path != "" {
		if _, ok := f.find(path); !ok {
			return &appvault.Error{Kind: appvault.KindValidation}
		}
	}
	host, ok := f.hosts.find(id)
	if !ok {
		return appvault.ErrNotFound
	}
	host.Group = path
	f.hosts.replace(host)
	return nil
}

func (f *entityGroups) find(path string) (domainvault.Group, bool) {
	for _, group := range f.items {
		if group.Path == path {
			return group, true
		}
	}
	return domainvault.Group{}, false
}

func (f *entityGroups) replace(group domainvault.Group) {
	for i := range f.items {
		if f.items[i].Path == group.Path {
			f.items[i] = group
			return
		}
	}
	f.items = append(f.items, group)
}

func applyEntityGroupDraft(group *domainvault.Group, draft domainvault.GroupDraft) {
	group.Path = draft.Path
	group.Notes = draft.Notes
	group.Username = draft.Username
	group.Port = draft.Port
	group.Protocol = draft.Protocol
	group.AuthMethod = draft.AuthMethod
	group.IdentityID = draft.IdentityID
	group.KeyID = draft.KeyID
	group.UseSSHAgent = draft.UseSSHAgent
	group.RequiresMFA = draft.RequiresMFA
	group.ProxyProfileID = draft.ProxyProfileID
}

type entityProxies struct {
	items  []domainvault.ProxyProfile
	hosts  *entityHosts
	groups *entityGroups
	nextID int
}

func (f *entityProxies) ListProxyProfiles(context.Context) ([]domainvault.ProxyProfile, error) {
	return append([]domainvault.ProxyProfile(nil), f.items...), nil
}

func (f *entityProxies) CreateProxyProfile(_ context.Context, draft domainvault.ProxyProfileDraft) (domainvault.ProxyProfile, error) {
	normalized, err := domainvault.NormalizeProxyProfileDraft(draft)
	if err != nil {
		return domainvault.ProxyProfile{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	f.nextID++
	profile := domainvault.ProxyProfile{
		ID: "proxy-" + strconv.Itoa(f.nextID), Label: normalized.Label,
		Config: normalized.Config, OrderKey: int64(f.nextID) * 1000,
	}
	f.items = append(f.items, profile)
	return profile, nil
}

func (f *entityProxies) UpdateProxyProfile(_ context.Context, id string, draft domainvault.ProxyProfileDraft) (domainvault.ProxyProfile, error) {
	normalized, err := domainvault.NormalizeProxyProfileDraft(draft)
	if err != nil {
		return domainvault.ProxyProfile{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	for i := range f.items {
		if f.items[i].ID == id {
			f.items[i].Label = normalized.Label
			f.items[i].Config = normalized.Config
			return f.items[i], nil
		}
	}
	return domainvault.ProxyProfile{}, appvault.ErrNotFound
}

func (f *entityProxies) DeleteProxyProfile(_ context.Context, id string) error {
	found := false
	kept := f.items[:0]
	for _, profile := range f.items {
		if profile.ID == id {
			found = true
			continue
		}
		kept = append(kept, profile)
	}
	f.items = kept
	if !found {
		return appvault.ErrNotFound
	}
	for i := range f.hosts.items {
		if f.hosts.items[i].ProxyProfileID == id {
			f.hosts.items[i].ProxyProfileID = ""
		}
	}
	for i := range f.groups.items {
		if f.groups.items[i].ProxyProfileID == id {
			f.groups.items[i].ProxyProfileID = ""
		}
	}
	return nil
}

type entityKnownHosts struct {
	items []domainconn.KnownHost
}

func (f *entityKnownHosts) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	return append([]domainconn.KnownHost(nil), f.items...), nil
}

func (f *entityKnownHosts) DeleteKnownHost(_ context.Context, id string) error {
	for i, record := range f.items {
		if record.ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			return nil
		}
	}
	return appvault.ErrNotFound
}

func newEntityView(t *testing.T) (*vaultstate.State, *entityHosts, *entityGroups, *entityProxies, *entityKnownHosts, *ui.Tester) {
	t.Helper()
	hosts := &entityHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web-1", Hostname: "10.0.0.1", Port: 22,
		Protocol: domainvault.ProtocolSSH,
	}}}
	groups := &entityGroups{hosts: hosts}
	proxies := &entityProxies{hosts: hosts, groups: groups}
	known := &entityKnownHosts{}
	state := vaultstate.NewState(hosts,
		vaultstate.WithGroups(groups),
		vaultstate.WithProxyProfiles(proxies),
		vaultstate.WithKnownHosts(known),
	)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Children(func() {
			View(state)(c)
		})
	}, 1400, 1200)
	return state, hosts, groups, proxies, known, tt
}

func textsContain(tt *ui.Tester, want string) bool {
	for _, text := range tt.Texts() {
		if strings.Contains(text, want) {
			return true
		}
	}
	return false
}

func TestVaultViewCreatesAEditsAndDeletesAGroupWithoutDeletingItsHosts(t *testing.T) {
	m := locales.In("en")
	state, hosts, groups, _, _, tt := newEntityView(t)

	if err := tt.Click(m.VaultGroupNew()); err != nil {
		t.Fatalf("click New Group: %v", err)
	}
	tt.Frame()
	if !tt.HasText(m.VaultGroupFormCreateTitle()) {
		t.Fatalf("group editor is not open, texts %q", tt.Texts())
	}
	state.GroupForm().Path = "prod/web"
	state.GroupForm().Username = "deploy"
	if err := state.SaveGroup(); err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}
	tt.Frame()

	if len(groups.items) != 1 || groups.items[0].Path != "prod/web" {
		t.Fatalf("groups = %+v", groups.items)
	}
	if !textsContain(tt, "Host Groups (1)") || !textsContain(tt, "prod/web") {
		t.Fatalf("view does not list the group, texts %q", tt.Texts())
	}
	// The Host has no username; the group default is the effective value.
	if !textsContain(tt, "deploy") {
		t.Fatalf("group default is not shown on the member Host, texts %q", tt.Texts())
	}

	// Drag the Host row onto the group row. The drop handler calls
	// MoveHostToGroup through the same drag payload the view builds.
	hostRect, ok := tt.Find("web-1")
	if !ok {
		t.Fatalf("Host row not found, texts %q", tt.Texts())
	}
	groupRect, ok := tt.Find("prod/web")
	if !ok {
		t.Fatalf("group row not found, texts %q", tt.Texts())
	}
	tt.Press(hostRect.X+hostRect.W/2, hostRect.Y+hostRect.H/2)
	tt.Move(groupRect.X+groupRect.W/2, groupRect.Y+groupRect.H/2)
	tt.Release(groupRect.X+groupRect.W/2, groupRect.Y+groupRect.H/2)
	tt.Frame()
	if hosts.items[0].Group != "prod/web" {
		t.Fatalf("Host Group after drop = %q, want prod/web", hosts.items[0].Group)
	}

	// Delete the group; the Host survives and returns to the root.
	if err := tt.Click(m.VaultHostDelete()); err != nil {
		t.Fatalf("click group Delete: %v", err)
	}
	tt.Frame()
	if !textsContain(tt, "Delete Host Group") {
		t.Fatalf("group delete confirmation is missing, texts %q", tt.Texts())
	}
	if err := tt.Click(m.VaultHostDelete()); err != nil {
		t.Fatalf("confirm group Delete: %v", err)
	}
	tt.Frame()
	if len(groups.items) != 0 {
		t.Fatalf("groups after delete = %+v", groups.items)
	}
	if len(hosts.items) != 1 || hosts.items[0].Label != "web-1" {
		t.Fatalf("Hosts after group delete = %+v, want web-1 kept", hosts.items)
	}
	if hosts.items[0].Group != "" {
		t.Fatalf("Host Group after delete = %q, want the Vault root", hosts.items[0].Group)
	}
}

func TestVaultHostEditorSelectsAProxyProfile(t *testing.T) {
	m := locales.In("en")
	state, hosts, _, proxies, _, tt := newEntityView(t)

	if err := tt.Click(m.VaultProxyNew()); err != nil {
		t.Fatalf("click New Proxy Profile: %v", err)
	}
	tt.Frame()
	if !tt.HasText(m.VaultProxyFormCreateTitle()) {
		t.Fatalf("proxy editor is not open, texts %q", tt.Texts())
	}
	state.ProxyForm().Label = "office"
	state.ProxyForm().Type = string(domainvault.ProxySOCKS5)
	state.ProxyForm().Host = "socks.example.com"
	state.ProxyForm().Port = "1080"
	if err := state.SaveProxyProfile(); err != nil {
		t.Fatalf("SaveProxyProfile: %v", err)
	}
	tt.Frame()
	if len(proxies.items) != 1 {
		t.Fatalf("profiles = %+v", proxies.items)
	}
	if !textsContain(tt, "Proxy Profiles (1)") || !textsContain(tt, "SOCKS5 socks.example.com:1080") {
		t.Fatalf("view does not list the profile, texts %q", tt.Texts())
	}

	state.BeginEdit("host-1")
	tt.Frame()
	if !tt.HasText(m.VaultHostProxyProfile()) {
		t.Fatalf("Host editor has no Proxy Profile field, texts %q", tt.Texts())
	}
	state.Form().ProxyProfileID = proxies.items[0].ID
	if err := state.Save(); err != nil {
		t.Fatalf("Save Host: %v", err)
	}
	tt.Frame()
	if hosts.items[0].ProxyProfileID != proxies.items[0].ID {
		t.Fatalf("Host ProxyProfileID = %q", hosts.items[0].ProxyProfileID)
	}
	effective, ok := state.EffectiveHost("host-1")
	if !ok || effective.Proxy == nil || effective.Proxy.Host != "socks.example.com" ||
		effective.Proxy.Port != 1080 {
		t.Fatalf("effective Host = %+v, want the selected profile materialized", effective.Proxy)
	}

	// Deleting the profile clears the Host reference and keeps the Host.
	state.RequestDeleteProxyProfile(proxies.items[0].ID)
	tt.Frame()
	if !textsContain(tt, "Delete") {
		t.Fatalf("delete confirmation missing, texts %q", tt.Texts())
	}
	if err := tt.Click(m.VaultHostDelete()); err != nil {
		t.Fatalf("confirm profile Delete: %v", err)
	}
	tt.Frame()
	if len(proxies.items) != 0 {
		t.Fatalf("profiles after delete = %+v", proxies.items)
	}
	if len(hosts.items) != 1 || hosts.items[0].ProxyProfileID != "" {
		t.Fatalf("Host after profile delete = %+v, want the reference cleared", hosts.items)
	}
}

func TestVaultProxyRowSummarizesTheEndpointWithoutCredentials(t *testing.T) {
	state, _, _, proxies, _, tt := newEntityView(t)
	proxies.items = []domainvault.ProxyProfile{{
		ID: "proxy-1", Label: "office",
		Config: domainvault.ProxyConfig{
			Type: domainvault.ProxyHTTP, Host: "proxy.example.com", Port: 8080,
			Username: "alice", Password: "secret",
		},
	}}
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt.Frame()

	if !textsContain(tt, "HTTP proxy.example.com:8080") {
		t.Fatalf("the row does not show the type and endpoint, texts %q", tt.Texts())
	}
	if textsContain(tt, "alice") || textsContain(tt, "secret") {
		t.Fatalf("the row leaked a credential, texts %q", tt.Texts())
	}
}

func TestVaultViewListsTrustedKnownHostsAndRemovesTrust(t *testing.T) {
	m := locales.In("en")
	state, _, _, _, known, tt := newEntityView(t)
	known.items = []domainconn.KnownHost{
		{ID: "kh-1", Hostname: "example.com", Port: 22, KeyType: "ssh-ed25519",
			Fingerprint: "SHA256:abc"},
		{ID: "kh-2", Hostname: "other.example.com", Port: 2222, KeyType: "ecdsa-sha2-nistp256",
			Fingerprint: "SHA256:def"},
	}
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt.Frame()

	if !textsContain(tt, "Known Hosts (2)") || !tt.HasText("example.com:22") ||
		!tt.HasText("other.example.com:2222") {
		t.Fatalf("Known Hosts are not listed, texts %q", tt.Texts())
	}
	if !tt.HasText(m.VaultKnownHostTrusted()) {
		t.Fatalf("trust state is not shown, texts %q", tt.Texts())
	}
	if !textsContain(tt, "ssh-ed25519 · SHA256:abc") {
		t.Fatalf("key type and fingerprint are not shown, texts %q", tt.Texts())
	}

	if err := tt.Click(m.VaultKnownHostRemove()); err != nil {
		t.Fatalf("click Remove trust: %v", err)
	}
	tt.Frame()
	if !textsContain(tt, "Remove trust for example.com:22") {
		t.Fatalf("remove confirmation missing, texts %q", tt.Texts())
	}
	if err := tt.Click(m.VaultHostDelete()); err != nil {
		t.Fatalf("confirm remove trust: %v", err)
	}
	tt.Frame()
	if len(known.items) != 1 || known.items[0].ID != "kh-2" {
		t.Fatalf("Known Hosts after remove = %+v", known.items)
	}
	if state.KnownHostCount() != 1 || !textsContain(tt, "Known Hosts (1)") {
		t.Fatalf("view did not refresh the trust list, texts %q", tt.Texts())
	}
}

func TestDeletingAGroupDoesNotDeleteHostsWhenTheGroupIsNested(t *testing.T) {
	state, hosts, groups, _, _, tt := newEntityView(t)
	hosts.items[0].Group = "prod/web/internal"
	groups.items = []domainvault.Group{{Path: "prod"}, {Path: "prod/web"}, {Path: "prod/web/internal"}}
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt.Frame()

	state.RequestDeleteGroup("prod/web")
	if err := state.ConfirmDeleteGroup(); err != nil {
		t.Fatalf("ConfirmDeleteGroup: %v", err)
	}
	tt.Frame()
	if len(hosts.items) != 1 || hosts.items[0].Group != "" {
		t.Fatalf("Host after deleting a nested group = %+v", hosts.items)
	}
	if len(groups.items) != 1 || groups.items[0].Path != "prod" {
		t.Fatalf("groups after delete = %+v, want only prod", groups.items)
	}
}
