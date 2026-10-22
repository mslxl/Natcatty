package vault

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	shellui "github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
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
			// Mirror the application service: a path Hosts already reference
			// is an implicit group and is a valid membership target.
			referenced := false
			for _, host := range f.hosts.items {
				if domainvault.IsGroupAtOrBelow(host.Group, path) {
					referenced = true
					break
				}
			}
			if !referenced {
				return &appvault.Error{Kind: appvault.KindValidation}
			}
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

// entityTransport keeps one fake Session alive so the shell can show the
// production work Tab strip.
type entityTransport struct {
	output chan []byte
	closed chan struct{}
	once   sync.Once
}

func newEntityTransport() *entityTransport {
	return &entityTransport{output: make(chan []byte), closed: make(chan struct{})}
}

func (f *entityTransport) Read(p []byte) (int, error) {
	select {
	case data := <-f.output:
		return copy(p, data), nil
	case <-f.closed:
		return 0, io.EOF
	}
}

func (f *entityTransport) Write(p []byte) (int, error) { return len(p), nil }
func (f *entityTransport) Resize(int, int) error       { return nil }
func (f *entityTransport) Wait() (int, error)          { return 0, nil }

func (f *entityTransport) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

type entityFactory struct{}

func (entityFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return newEntityTransport(), nil
}

type entitySurface struct{}

func (entitySurface) View(c *ui.Context) ui.Element { return ui.Text(c, "terminal") }
func (entitySurface) Close()                        {}

// newProductionTester renders the real shell around the Vault: the fixed
// sidebar and, because the shell opens one work Tab, the tab strip the app
// also shows over the Vault. The Vault therefore gets the same content width
// and height it gets in the production window instead of the full window.
func newProductionTester(t *testing.T, state *vaultstate.State, width, height int) *ui.Tester {
	t.Helper()
	service := appsession.NewService(entityFactory{})
	t.Cleanup(service.CloseAll)
	shell := shellstate.NewState(
		shellstate.WithSessions(service, func(*appsession.Session) (shellstate.Surface, error) {
			return entitySurface{}, nil
		}),
		shellstate.WithVault(View(state)),
	)
	if err := shell.NewTab(); err != nil {
		t.Fatalf("open a work Tab for the production chrome: %v", err)
	}
	shell.ShowVault()

	// Build the shell once, as the app does: a frame closure that builds it
	// again would create fresh view state every frame.
	view := shellui.View(shell)
	tt := ui.NewTester(view, width, height)
	tt.SetLocale("en")
	tt.Frame()
	return tt
}

func newEntityView(t *testing.T) (*vaultstate.State, *entityHosts, *entityGroups, *entityProxies, *entityKnownHosts, *ui.Tester) {
	t.Helper()
	return newProductionEntityView(t, 1400, 1200)
}

// newProductionEntityView renders the Vault inside the production shell, so
// sized tests see the real content column rather than the whole window.
func newProductionEntityView(t *testing.T, width, height int) (*vaultstate.State, *entityHosts, *entityGroups, *entityProxies, *entityKnownHosts, *ui.Tester) {
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
	tt := newProductionTester(t, state, width, height)
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

// scrollToEnd moves the pointer over the window and wheels to the bottom of
// the scroll container under it.
func scrollToEnd(tt *ui.Tester, width, height int) {
	tt.Move(float32(width)/2, float32(height)/2)
	tt.Scroll(float32(width)/2, float32(height)/2, 0, 10000)
	tt.Frame()
}

func assertVisible(t *testing.T, tt *ui.Tester, text string, width, height int) {
	t.Helper()
	rect, ok := tt.Find(text)
	if !ok {
		t.Fatalf("%q is not rendered, texts %q", text, tt.Texts())
	}
	if rect.Y < -0.5 || rect.Y+rect.H > float32(height)+0.5 ||
		rect.X < -0.5 || rect.X+rect.W > float32(width)+0.5 {
		t.Fatalf("%q is outside the %dx%d window: %+v", text, width, height, rect)
	}
}

// summaryLineCount reports how many visual lines the given summary tokens
// occupy, counting a token that wraps inside itself. Tokens on the same line
// share their top edge.
func summaryLineCount(t *testing.T, tt *ui.Tester, tokens []string) int {
	t.Helper()
	type group struct {
		y     float32
		lines int
	}
	var groups []group
	lineHeight := float32(0)
	rects := make([]ui.Rect, 0, len(tokens))
	for _, token := range tokens {
		rect, ok := tt.Find(token)
		if !ok || rect.W <= 0 {
			t.Fatalf("summary token %q is not laid out, texts %q", token, tt.Texts())
		}
		rects = append(rects, rect)
		if lineHeight == 0 || (rect.H > 0 && rect.H < lineHeight) {
			lineHeight = rect.H
		}
	}
	total := 0
	for _, rect := range rects {
		lines := int((rect.H + lineHeight/2) / lineHeight)
		if lines < 1 {
			lines = 1
		}
		merged := false
		for i := range groups {
			if rect.Y-groups[i].y < 1 && groups[i].y-rect.Y < 1 {
				if lines > groups[i].lines {
					groups[i].lines = lines
				}
				merged = true
				break
			}
		}
		if !merged {
			groups = append(groups, group{y: rect.Y, lines: lines})
		}
	}
	for _, g := range groups {
		total += g.lines
	}
	return total
}

func assertNoHorizontalOverflow(t *testing.T, tt *ui.Tester, width, height int) {
	t.Helper()
	for _, text := range tt.Texts() {
		rect, ok := tt.Find(text)
		if !ok || rect.W <= 0 || rect.H <= 0 {
			continue
		}
		if rect.X < -0.5 || rect.X+rect.W > float32(width)+0.5 {
			t.Fatalf("%dx%d: %q overflows horizontally: %+v", width, height, text, rect)
		}
	}
}

var editorSizes = []struct{ width, height int }{{1100, 720}, {720, 480}}

func TestHostEditorScrollsToItsActionsAtBothSizes(t *testing.T) {
	m := locales.In("en")
	for _, size := range editorSizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			state, _, _, _, _, tt := newProductionEntityView(t, size.width, size.height)

			state.BeginEdit("host-1")
			tt.Frame()
			scrollToEnd(tt, size.width, size.height)
			assertVisible(t, tt, m.VaultHostPinned(), size.width, size.height)
			assertVisible(t, tt, m.VaultHostSave(), size.width, size.height)
			assertVisible(t, tt, m.VaultHostCancel(), size.width, size.height)
			assertNoHorizontalOverflow(t, tt, size.width, size.height)

			if err := tt.Click(m.VaultHostCancel()); err != nil {
				t.Fatalf("click Cancel: %v", err)
			}
			tt.Frame()
			if state.Editing() {
				t.Fatal("Cancel did not close the Host editor")
			}

			state.BeginEdit("host-1")
			tt.Frame()
			scrollToEnd(tt, size.width, size.height)
			if err := tt.Click(m.VaultHostSave()); err != nil {
				t.Fatalf("click Save: %v", err)
			}
			tt.Frame()
			if state.Editing() {
				t.Fatal("Save did not close the Host editor")
			}
		})
	}
}

func TestHostEditorScrollsWithTheKeyboard(t *testing.T) {
	m := locales.In("en")
	state, _, _, _, _, tt := newProductionEntityView(t, 720, 480)
	state.BeginEdit("host-1")
	tt.Frame()
	tt.Move(360, 240)
	tt.Key(0, ui.KeyEnd)
	tt.Frame()
	assertVisible(t, tt, m.VaultHostPinned(), 720, 480)
	assertVisible(t, tt, m.VaultHostSave(), 720, 480)
}

func TestGroupEditorScrollsToItsActionsAtBothSizes(t *testing.T) {
	m := locales.In("en")
	for _, size := range editorSizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			state, _, groups, _, _, tt := newProductionEntityView(t, size.width, size.height)

			state.BeginCreateGroup()
			state.GroupForm().Path = "prod2"
			tt.Frame()
			scrollToEnd(tt, size.width, size.height)
			assertVisible(t, tt, m.VaultHostRequiresMFA(), size.width, size.height)
			assertVisible(t, tt, m.VaultHostSave(), size.width, size.height)
			assertNoHorizontalOverflow(t, tt, size.width, size.height)
			if err := tt.Click(m.VaultHostSave()); err != nil {
				t.Fatalf("click Save: %v", err)
			}
			tt.Frame()
			if state.EditingGroup() {
				t.Fatal("Save did not close the Host Group editor")
			}
			if len(groups.items) != 1 || groups.items[0].Path != "prod2" {
				t.Fatalf("groups = %+v", groups.items)
			}

			state.BeginCreateGroup()
			tt.Frame()
			scrollToEnd(tt, size.width, size.height)
			if err := tt.Click(m.VaultHostCancel()); err != nil {
				t.Fatalf("click Cancel: %v", err)
			}
			tt.Frame()
			if state.EditingGroup() {
				t.Fatal("Cancel did not close the Host Group editor")
			}
		})
	}
}

func TestProxyEditorScrollsToItsActionsAtBothSizes(t *testing.T) {
	m := locales.In("en")
	for _, size := range editorSizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			state, _, _, proxies, _, tt := newProductionEntityView(t, size.width, size.height)

			state.BeginCreateProxyProfile()
			state.ProxyForm().Label = "office"
			state.ProxyForm().Host = "socks.example.com"
			tt.Frame()
			scrollToEnd(tt, size.width, size.height)
			assertVisible(t, tt, m.VaultHostSave(), size.width, size.height)
			assertNoHorizontalOverflow(t, tt, size.width, size.height)
			if err := tt.Click(m.VaultHostSave()); err != nil {
				t.Fatalf("click Save: %v", err)
			}
			tt.Frame()
			if state.EditingProxyProfile() {
				t.Fatal("Save did not close the Proxy Profile editor")
			}
			if len(proxies.items) != 1 || proxies.items[0].Label != "office" {
				t.Fatalf("profiles = %+v", proxies.items)
			}

			state.BeginCreateProxyProfile()
			tt.Frame()
			scrollToEnd(tt, size.width, size.height)
			if err := tt.Click(m.VaultHostCancel()); err != nil {
				t.Fatalf("click Cancel: %v", err)
			}
			tt.Frame()
			if state.EditingProxyProfile() {
				t.Fatal("Cancel did not close the Proxy Profile editor")
			}
		})
	}
}

func TestHostPortBlankInheritsTheGroupPortInTheEditor(t *testing.T) {
	m := locales.In("en")
	state, hosts, groups, _, _, tt := newProductionEntityView(t, 720, 480)
	groups.items = []domainvault.Group{{Path: "prod2", Port: 2222}}
	hosts.items[0].Group = "prod2"
	hosts.items[0].Port = 0
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt.Frame()

	if !textsContain(tt, "10.0.0.1:2222") {
		t.Fatalf("the list does not show the inherited port, texts %q", tt.Texts())
	}

	state.BeginEdit("host-1")
	tt.Frame()
	if state.Form().Port != "" {
		t.Fatalf("editor Port = %q, want blank for an inherited port", state.Form().Port)
	}
	scrollToEnd(tt, 720, 480)
	if err := tt.Click(m.VaultHostSave()); err != nil {
		t.Fatalf("click Save: %v", err)
	}
	tt.Frame()
	if hosts.items[0].Port != 0 {
		t.Fatalf("stored Port = %d, want 0 to keep inheriting", hosts.items[0].Port)
	}
	if effective, ok := state.EffectiveHost("host-1"); !ok || effective.Port != 2222 {
		t.Fatalf("effective Port = %d, want 2222", effective.Port)
	}
	if !textsContain(tt, "10.0.0.1:2222") {
		t.Fatalf("the list lost the inherited port, texts %q", tt.Texts())
	}
}

func TestKnownHostsRefreshWhenATrustDecisionIsPersisted(t *testing.T) {
	m := locales.In("en")
	state, _, _, _, known, tt := newProductionEntityView(t, 1100, 720)
	if !textsContain(tt, m.VaultKnownHostsTitle(0)) {
		t.Fatalf("the empty Known Hosts state is missing, texts %q", tt.Texts())
	}

	known.items = []domainconn.KnownHost{{
		ID: "kh-1", Hostname: "example.com", Port: 22, KeyType: "ssh-ed25519",
		Fingerprint: "SHA256:abc",
	}}
	state.InvalidateKnownHosts()
	tt.Frame()

	if !textsContain(tt, m.VaultKnownHostsTitle(1)) || !tt.HasText("example.com:22") {
		t.Fatalf("the trusted Known Host did not appear without a full reload, texts %q", tt.Texts())
	}
}

func TestHostsAreDisplayedUnderTheirGroupAt720x480(t *testing.T) {
	m := locales.In("en")
	state, hosts, groups, _, _, tt := newProductionEntityView(t, 720, 480)
	hosts.items = []domainvault.Host{
		{ID: "grouped", Label: "grouped-1", Hostname: "10.0.0.1", Port: 22,
			Protocol: domainvault.ProtocolSSH, Group: "prod2"},
		{ID: "loose", Label: "loose-1", Hostname: "10.0.0.2", Port: 22,
			Protocol: domainvault.ProtocolSSH},
	}
	groups.items = []domainvault.Group{{Path: "prod2"}}
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt.Frame()

	groupRect, ok := tt.Find("prod2")
	if !ok {
		t.Fatalf("group row missing, texts %q", tt.Texts())
	}
	groupedRect, ok := tt.Find("grouped-1")
	if !ok {
		t.Fatalf("grouped Host row missing, texts %q", tt.Texts())
	}
	rootRect, ok := tt.Find(m.VaultGroupUngrouped())
	if !ok {
		t.Fatalf("Vault root row missing, texts %q", tt.Texts())
	}
	looseRect, ok := tt.Find("loose-1")
	if !ok {
		t.Fatalf("ungrouped Host row missing, texts %q", tt.Texts())
	}
	if groupedRect.Y <= groupRect.Y {
		t.Fatalf("grouped Host is not below its group: group %+v, Host %+v", groupRect, groupedRect)
	}
	if groupedRect.Y >= rootRect.Y {
		t.Fatalf("grouped Host is not kept out of the root section: Host %+v, root %+v",
			groupedRect, rootRect)
	}
	if looseRect.Y <= rootRect.Y {
		t.Fatalf("ungrouped Host is not under the root: root %+v, Host %+v", rootRect, looseRect)
	}
	if groupedRect.X <= groupRect.X {
		t.Fatalf("grouped Host is not indented under its group: group %+v, Host %+v",
			groupRect, groupedRect)
	}
	if !textsContain(tt, m.VaultGroupMemberCount(1)) {
		t.Fatalf("the group row does not show its member count, texts %q", tt.Texts())
	}
	counts := 0
	for _, text := range tt.Texts() {
		if text == m.VaultGroupMemberCount(1) {
			counts++
		}
	}
	if counts < 2 {
		t.Fatalf("the root row does not show its member count, texts %q", tt.Texts())
	}
}

// TestHostSummariesStayAtTwoLinesInProductionChrome renders the Vault inside
// the real sidebar and work Tab strip at the minimum window size. The content
// column is then about 456 DIPs wide, not the window's 720: the width where
// the summary used to wrap to three lines.
func TestHostSummariesStayAtTwoLinesInProductionChrome(t *testing.T) {
	m := locales.In("en")
	state, hosts, _, _, _, tt := newProductionEntityView(t, 720, 480)
	hosts.items = []domainvault.Host{
		{
			ID: "password-host", Label: "alpha",
			Hostname: "127.0.0.1", Port: 2200, Protocol: domainvault.ProtocolSSH,
			Username: "deploy", HasPassword: true,
		},
		{
			ID: "keyboard-host", Label: "beta",
			Hostname: "127.0.0.2", Port: 22, Protocol: domainvault.ProtocolSSH,
			Username: "ops",
		},
	}
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt.Frame()

	if lines := summaryLineCount(t, tt, []string{
		"127.0.0.1:2200", "deploy", m.VaultAuthSummaryPassword(),
	}); lines > 2 {
		t.Fatalf("password Host summary uses %d lines, want at most 2", lines)
	}
	if lines := summaryLineCount(t, tt, []string{
		"127.0.0.2:22", "ops", m.VaultAuthSummaryKeyboard(),
	}); lines > 2 {
		t.Fatalf("keyboard-interactive Host summary uses %d lines, want at most 2", lines)
	}
	assertNoHorizontalOverflow(t, tt, 720, 480)
}

// TestListActionsStayInsideTheProductionWindow checks that the right-aligned
// actions of every list row, including Known Hosts, are fully inside the
// window at the minimum size. The Vault renders inside the real shell chrome,
// so it sees the production content width.
func TestListActionsStayInsideTheProductionWindow(t *testing.T) {
	m := locales.In("en")
	state, hosts, groups, proxies, known, tt := newProductionEntityView(t, 720, 480)
	hosts.items = []domainvault.Host{
		{
			ID: "host-1", Label: "web-1", Hostname: "127.0.0.1", Port: 22,
			Protocol: domainvault.ProtocolSSH, Group: "prod/web",
		},
	}
	groups.items = []domainvault.Group{{Path: "prod"}, {Path: "prod/web"}}
	proxies.items = []domainvault.ProxyProfile{{
		ID: "proxy-1", Label: "office",
		Config: domainvault.ProxyConfig{
			Type: domainvault.ProxySOCKS5, Host: "socks.example.com", Port: 1080,
		},
	}}
	known.items = []domainconn.KnownHost{{
		ID: "kh-1", Hostname: "known-hosts.example.internal", Port: 2222,
		KeyType:     "ssh-ed25519",
		Fingerprint: "SHA256:0123456789abcdefghijklmnopqrstuvwxyzABCDEFGH",
	}}
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt.Frame()

	// Measure the button where there is room, then require the same size at
	// the minimum window: a squeezed row narrows the text's box instead of
	// pushing the button past the edge, which is the clipping users see.
	wide := newProductionTester(t, state, 1100, 720)
	scrollToEnd(wide, 1100, 720)
	wideRemove, ok := wide.Find(m.VaultKnownHostRemove())
	if !ok || wideRemove.W <= 0 {
		t.Fatalf("Remove trust is not laid out at 1100x720, texts %q", wide.Texts())
	}

	// Scroll to the Known Hosts section, which sits at the end of the list.
	scrollToEnd(tt, 720, 480)
	remove, ok := tt.Find(m.VaultKnownHostRemove())
	if !ok {
		t.Fatalf("Remove trust is not rendered, texts %q", tt.Texts())
	}
	if remove.W <= 0 {
		t.Fatalf("Remove trust has no laid-out box: %+v", remove)
	}
	if remove.X < -0.5 || remove.X+remove.W > 720.5 ||
		remove.Y < -0.5 || remove.Y+remove.H > 480.5 {
		t.Fatalf("Remove trust is outside the 720x480 window: %+v", remove)
	}
	if remove.W < wideRemove.W-0.5 || remove.H > wideRemove.H+0.5 {
		t.Fatalf("Remove trust is squeezed at 720x480: %+v, want the %+v it gets at 1100x720",
			remove, wideRemove)
	}
	assertNoHorizontalOverflow(t, tt, 720, 480)

	// The button is not only visible but still clickable at this size.
	if err := tt.Click(m.VaultKnownHostRemove()); err != nil {
		t.Fatalf("click Remove trust: %v", err)
	}
	tt.Frame()
	if !textsContain(tt, "Remove trust for known-hosts.example.internal:2222") {
		t.Fatalf("the confirmation is missing, texts %q", tt.Texts())
	}
}

// TestGroupAndProxyActionsAreNotClipped checks the other list rows at the
// production content width. Each state holds exactly one such row, so the
// first Edit/Delete pair belongs to it and can be compared with the size the
// same button gets when there is room.
func TestGroupAndProxyActionsAreNotClipped(t *testing.T) {
	m := locales.In("en")
	cases := []struct {
		name  string
		setup func(*entityGroups, *entityProxies)
	}{
		{
			name: "group",
			setup: func(groups *entityGroups, _ *entityProxies) {
				groups.items = []domainvault.Group{{
					Path: "production/web/frontend", Username: "deploy", Port: 2222,
				}}
			},
		},
		{
			name: "proxy",
			setup: func(_ *entityGroups, proxies *entityProxies) {
				proxies.items = []domainvault.ProxyProfile{{
					ID: "proxy-1", Label: "office",
					Config: domainvault.ProxyConfig{
						Type: domainvault.ProxySOCKS5,
						Host: "socks-proxy.example.internal", Port: 1080,
					},
				}}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, hosts, groups, proxies, _, tt := newProductionEntityView(t, 720, 480)
			hosts.items = nil
			tc.setup(groups, proxies)
			if err := state.Reload(); err != nil {
				t.Fatalf("Reload: %v", err)
			}
			tt.Frame()

			wide := newProductionTester(t, state, 1100, 720)
			for _, label := range []string{m.VaultHostEdit(), m.VaultHostDelete()} {
				narrow, ok := tt.Find(label)
				if !ok || narrow.W <= 0 {
					t.Fatalf("%s action %q is not laid out at 720x480, texts %q",
						tc.name, label, tt.Texts())
				}
				roomy, ok := wide.Find(label)
				if !ok || roomy.W <= 0 {
					t.Fatalf("%s action %q is not laid out at 1100x720, texts %q",
						tc.name, label, wide.Texts())
				}
				if narrow.X < -0.5 || narrow.X+narrow.W > 720.5 ||
					narrow.Y < -0.5 || narrow.Y+narrow.H > 480.5 {
					t.Fatalf("%s action %q is outside the 720x480 window: %+v",
						tc.name, label, narrow)
				}
				if narrow.W < roomy.W-0.5 || narrow.H > roomy.H+0.5 {
					t.Fatalf("%s action %q is squeezed at 720x480: %+v, want %+v",
						tc.name, label, narrow, roomy)
				}
			}
		})
	}
}

// TestEditorsOpenAtTheTopInProductionChrome reproduces the reported case: the
// list is scrolled, then a different editor opens. It must start at its own
// top instead of inheriting the previous offset, and reopening after the
// editor itself was scrolled must also start at the top.
func TestEditorsOpenAtTheTopInProductionChrome(t *testing.T) {
	m := locales.In("en")
	cases := []struct {
		name  string
		title string
		end   string
		open  func(*vaultstate.State)
	}{
		{
			name: "host", title: m.VaultHostFormEditTitle(), end: m.VaultHostPinned(),
			open: func(s *vaultstate.State) { s.BeginEdit("host-1") },
		},
		{
			name: "group", title: m.VaultGroupFormCreateTitle(), end: m.VaultHostRequiresMFA(),
			open: func(s *vaultstate.State) { s.BeginCreateGroup() },
		},
		{
			name: "proxy", title: m.VaultProxyFormCreateTitle(), end: m.VaultHostSave(),
			open: func(s *vaultstate.State) { s.BeginCreateProxyProfile() },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, hosts, _, _, _, tt := newProductionEntityView(t, 720, 480)
			// Enough rows that the list itself can be scrolled before the
			// editor opens.
			hosts.items = nil
			for i := 0; i < 10; i++ {
				hosts.items = append(hosts.items, domainvault.Host{
					ID:    model.HostID(fmt.Sprintf("host-%d", i)),
					Label: fmt.Sprintf("host-%d", i), Hostname: "10.0.0.1", Port: 22,
					Protocol: domainvault.ProtocolSSH,
				})
			}
			if err := state.Reload(); err != nil {
				t.Fatalf("Reload: %v", err)
			}
			tt.Frame()

			// The editor's position with no prior scrolling is the baseline
			// the reopened editor must match.
			tc.open(state)
			tt.Frame()
			baseline, ok := tt.Find(tc.title)
			if !ok {
				t.Fatalf("%q is not rendered, texts %q", tc.title, tt.Texts())
			}
			if baseline.Y < -0.5 || baseline.Y+baseline.H > 480.5 {
				t.Fatalf("editor title is outside the window on first open: %+v", baseline)
			}
			scrollToEnd(tt, 720, 480)
			assertVisible(t, tt, tc.end, 720, 480)
			if err := tt.Click(m.VaultHostCancel()); err != nil {
				t.Fatalf("close the editor: %v", err)
			}
			tt.Frame()

			// Scroll the list far enough that it has a non-zero offset, then
			// open the editor again. Seeing the section below the Hosts proves
			// the list really moved.
			scrollToEnd(tt, 720, 480)
			assertVisible(t, tt, "host-9", 720, 480)
			tc.open(state)
			tt.Frame()
			title, ok := tt.Find(tc.title)
			if !ok {
				t.Fatalf("%q is not rendered after reopening, texts %q", tc.title, tt.Texts())
			}
			if title.Y < -0.5 || title.Y+title.H > 480.5 {
				t.Fatalf("editor title is outside the window after reopening: %+v", title)
			}
			if delta := title.Y - baseline.Y; delta > 1 || delta < -1 {
				t.Fatalf("editor opened at Y=%v, want the top baseline Y=%v",
					title.Y, baseline.Y)
			}
			assertNoHorizontalOverflow(t, tt, 720, 480)
		})
	}
}

func TestNestedGroupsIndentTheirMembers(t *testing.T) {
	state, hosts, groups, _, _, tt := newProductionEntityView(t, 1100, 720)
	hosts.items = []domainvault.Host{
		{ID: "deep", Label: "deep-1", Hostname: "10.0.0.1", Port: 22,
			Protocol: domainvault.ProtocolSSH, Group: "prod/web"},
	}
	groups.items = []domainvault.Group{{Path: "prod"}, {Path: "prod/web"}}
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt.Frame()

	parent, ok := tt.Find("prod")
	if !ok {
		t.Fatalf("parent group row missing, texts %q", tt.Texts())
	}
	child, ok := tt.Find("prod/web")
	if !ok {
		t.Fatalf("child group row missing, texts %q", tt.Texts())
	}
	host, ok := tt.Find("deep-1")
	if !ok {
		t.Fatalf("Host row missing, texts %q", tt.Texts())
	}
	if child.X <= parent.X {
		t.Fatalf("child group is not indented: parent %+v, child %+v", parent, child)
	}
	if host.X <= child.X {
		t.Fatalf("Host is not indented under its child group: child %+v, Host %+v", child, host)
	}
	if host.Y <= child.Y {
		t.Fatalf("Host is not below its group row: child %+v, Host %+v", child, host)
	}
}

func TestAnImplicitGroupStaysInsideItsParentAndAcceptsDrops(t *testing.T) {
	state, hosts, groups, _, _, tt := newProductionEntityView(t, 1100, 720)
	hosts.items = []domainvault.Host{
		{ID: "web", Label: "web-1", Hostname: "10.0.0.1", Port: 22,
			Protocol: domainvault.ProtocolSSH, Group: "prod/web"},
		{ID: "db", Label: "db-1", Hostname: "10.0.0.2", Port: 22,
			Protocol: domainvault.ProtocolSSH, Group: "prod/web/db"},
		{ID: "loose", Label: "loose-1", Hostname: "10.0.0.3", Port: 22,
			Protocol: domainvault.ProtocolSSH},
	}
	groups.items = []domainvault.Group{{Path: "prod"}, {Path: "other"}}
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt.Frame()

	prod, ok := tt.Find("prod")
	if !ok {
		t.Fatalf("parent group row missing, texts %q", tt.Texts())
	}
	implicit, ok := tt.Find("prod/web")
	if !ok {
		t.Fatalf("implicit group row missing, texts %q", tt.Texts())
	}
	other, ok := tt.Find("other")
	if !ok {
		t.Fatalf("unrelated group row missing, texts %q", tt.Texts())
	}
	if implicit.Y <= prod.Y || implicit.Y >= other.Y {
		t.Fatalf("implicit group is outside its parent's subtree: prod %+v, implicit %+v, other %+v",
			prod, implicit, other)
	}
	if implicit.X <= prod.X {
		t.Fatalf("implicit group is not indented: parent %+v, implicit %+v", prod, implicit)
	}
	// The implicit row counts descendants just like a stored row: prod/web
	// directly holds web-1 and contains db-1's prod/web/db.
	counts := 0
	for _, text := range tt.Texts() {
		if text == "2 Hosts" {
			counts++
		}
	}
	if counts < 2 {
		t.Fatalf("implicit group does not report its recursive member count, texts %q", tt.Texts())
	}

	// A visible group row must also accept a drop, whether or not a record
	// exists for it.
	loose, ok := tt.Find("loose-1")
	if !ok {
		t.Fatalf("ungrouped Host missing, texts %q", tt.Texts())
	}
	tt.Press(loose.X+loose.W/2, loose.Y+loose.H/2)
	tt.Move(implicit.X+implicit.W/2, implicit.Y+implicit.H/2)
	tt.Release(implicit.X+implicit.W/2, implicit.Y+implicit.H/2)
	tt.Frame()
	if hosts.items[2].Group != "prod/web" {
		t.Fatalf("Host Group after dropping on an implicit group = %q, want prod/web",
			hosts.items[2].Group)
	}
}

func TestTheDragGhostLeavesTheDropTargetLabelsVisibleAt720x480(t *testing.T) {
	m := locales.In("en")
	state, hosts, groups, _, _, tt := newProductionEntityView(t, 720, 480)
	groups.items = []domainvault.Group{{Path: "prod2"}}
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt.Frame()

	// Drag onto the group row's middle, not its label, and prove both the
	// target name and the other drop target stay laid out during the drag.
	groupRect := dragTo(t, tt, "web-1", "prod2")
	if !tt.HasText("prod2") || !tt.HasText(m.VaultGroupUngrouped()) {
		t.Fatalf("a drop target label disappeared during the drag, texts %q", tt.Texts())
	}
	tt.Release(float32(720)/2, groupRect.Y+groupRect.H/2)
	tt.Frame()
	if hosts.items[0].Group != "prod2" {
		t.Fatalf("Host Group after dropping on the group = %q, want prod2",
			hosts.items[0].Group)
	}

	// Drag back out; the root label stays visible during the drag and the
	// Host returns to the root.
	rootRect, ok := tt.Find(m.VaultGroupUngrouped())
	if !ok {
		t.Fatalf("root row missing, texts %q", tt.Texts())
	}
	hostRect, ok := tt.Find("web-1")
	if !ok {
		t.Fatalf("grouped Host row missing, texts %q", tt.Texts())
	}
	tt.Press(hostRect.X+hostRect.W/2, hostRect.Y+hostRect.H/2)
	tt.Move(float32(720)/2, rootRect.Y+rootRect.H/2)
	tt.Frame()
	if !tt.HasText(m.VaultGroupUngrouped()) || !tt.HasText("prod2") {
		t.Fatalf("a drop target label disappeared during the drag, texts %q", tt.Texts())
	}
	tt.Release(float32(720)/2, rootRect.Y+rootRect.H/2)
	tt.Frame()
	if hosts.items[0].Group != "" {
		t.Fatalf("Host Group after dropping on the root = %q, want root",
			hosts.items[0].Group)
	}
}

// dragTo presses a Host label and moves the pointer to the target row's
// middle without releasing, leaving a drag active.
func dragTo(t *testing.T, tt *ui.Tester, hostText, targetText string) ui.Rect {
	t.Helper()
	host, ok := tt.Find(hostText)
	if !ok {
		t.Fatalf("%q missing, texts %q", hostText, tt.Texts())
	}
	target, ok := tt.Find(targetText)
	if !ok {
		t.Fatalf("%q missing, texts %q", targetText, tt.Texts())
	}
	tt.Press(host.X+host.W/2, host.Y+host.H/2)
	tt.Frame()
	tt.Move(float32(720)/2, target.Y+target.H/2)
	tt.Frame()
	return target
}

func TestDraggingAHostBackToTheRootUsesTheVisibleRootTarget(t *testing.T) {
	m := locales.In("en")
	state, hosts, groups, _, _, tt := newProductionEntityView(t, 1100, 720)
	hosts.items[0].Group = "prod2"
	groups.items = []domainvault.Group{{Path: "prod2"}}
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt.Frame()

	hostRect, ok := tt.Find("web-1")
	if !ok {
		t.Fatalf("Host row not found, texts %q", tt.Texts())
	}
	rootRect, ok := tt.Find(m.VaultGroupUngrouped())
	if !ok {
		t.Fatalf("root drop target not found, texts %q", tt.Texts())
	}
	tt.Press(hostRect.X+hostRect.W/2, hostRect.Y+hostRect.H/2)
	tt.Move(rootRect.X+rootRect.W/2, rootRect.Y+rootRect.H/2)
	tt.Release(rootRect.X+rootRect.W/2, rootRect.Y+rootRect.H/2)
	tt.Frame()
	if hosts.items[0].Group != "" {
		t.Fatalf("Host Group after dropping on the root = %q, want root", hosts.items[0].Group)
	}
}

func TestHostSummaryHasNoOrphanSeparatorsAt720x480(t *testing.T) {
	state, hosts, _, _, _, tt := newProductionEntityView(t, 720, 480)
	hosts.items[0].Tags = []string{"production", "web", "internal"}
	hosts.items[0].Username = "deploy"
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt.Frame()

	label, ok := tt.Find("web-1")
	if !ok {
		t.Fatalf("Host row not found, texts %q", tt.Texts())
	}
	for _, text := range tt.Texts() {
		if text == "·" || strings.HasPrefix(text, "·") {
			t.Fatalf("summary has an orphan separator %q: %q", text, tt.Texts())
		}
	}
	for _, token := range []string{"10.0.0.1:22", "deploy", "production, web, internal"} {
		rect, ok := tt.Find(token)
		if !ok {
			t.Fatalf("summary token %q is missing, texts %q", token, tt.Texts())
		}
		// At the minimum size the action strip wraps onto its own line below
		// the label, so the summary starts lower than it did when the buttons
		// shared the label line. It must still stay inside this Host row.
		if rect.Y > label.Y+80 {
			t.Fatalf("summary token %q is more than two lines below the label: label %+v, token %+v",
				token, label, rect)
		}
	}
}
