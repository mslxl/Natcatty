//go:build selftest

package app

// Ticket 20 self-test. Press F11, then 2, then 0. The check proves the Vault's
// Host Groups, group defaults, Proxy Profiles, and Known Hosts through the real
// SQLite store, the real application service, and the real Vault view rendered
// by ui.Tester: it creates a Host and a Host Group, drags the Host into the
// group, shows the group's defaults on the Host, creates and selects a Proxy
// Profile, lists and removes a Known Host, and deletes the group without
// deleting its Host. A layout step renders the same surface at 1100x720 and
// 720x480 and checks that no visible text overflows the window.
//
// Result: %TEMP%\natcatty-selftest-20.json. The steps close their temporary
// store and remove their directory, so F11,2,0 passes twice in one process.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/store"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	vaultui "github.com/mslxl/natcatty/ui/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

const selftest20Ticket = 20

type selftest20 struct {
	dir     string
	store   *store.Store
	service *appvault.Service
	state   *vaultstate.State
	tester  *ui.Tester
}

func (t *selftest20) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest20Ticket,
		Name:   "Vault Host Groups, Proxy Profiles, and Known Hosts",
		Steps: []selftest.Step{
			{Name: "group-defaults-and-drag", Run: t.groupStep},
			{Name: "proxy-profile-selection", Run: t.proxyStep},
			{Name: "known-hosts-trust", Run: t.knownHostStep},
			{Name: "group-delete-keeps-hosts", Run: t.deleteGroupStep},
			{Name: "layout", Run: t.layoutStep},
		},
	}
}

func (t *selftest20) groupStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	m := locales.In("en")

	if err := t.tester.Click(m.VaultGroupNew()); err != nil {
		return t.fail(fmt.Errorf("open the Host Group editor: %w", err))
	}
	t.tester.Frame()
	t.state.GroupForm().Path = "prod/web"
	t.state.GroupForm().Username = "deploy"
	if err := t.state.SaveGroup(); err != nil {
		return t.fail(fmt.Errorf("save Host Group: %w", err))
	}
	t.tester.Frame()
	if t.state.GroupCount() != 1 {
		return t.fail(fmt.Errorf("Host Groups = %d, want 1", t.state.GroupCount()))
	}

	t.state.BeginCreate()
	form := t.state.Form()
	form.Label = "web-1"
	form.Hostname = "10.0.0.1"
	form.Port = "22"
	if err := t.state.Save(); err != nil {
		return t.fail(fmt.Errorf("save Host: %w", err))
	}
	t.tester.Frame()
	if t.state.HostCount() != 1 {
		return t.fail(fmt.Errorf("Hosts = %d, want 1", t.state.HostCount()))
	}

	// Drag the Host row onto the group row: this exercises the view's drag
	// payload and drop target, not just the state method they call.
	hostRect, ok := t.tester.Find("web-1")
	if !ok {
		return t.fail(fmt.Errorf("Host row not found: %v", t.tester.Texts()))
	}
	groupRect, ok := t.tester.Find("prod/web")
	if !ok {
		return t.fail(fmt.Errorf("group row not found: %v", t.tester.Texts()))
	}
	t.tester.Press(hostRect.X+hostRect.W/2, hostRect.Y+hostRect.H/2)
	t.tester.Move(groupRect.X+groupRect.W/2, groupRect.Y+groupRect.H/2)
	t.tester.Release(groupRect.X+groupRect.W/2, groupRect.Y+groupRect.H/2)
	t.tester.Frame()

	raw, err := t.service.List(context.Background())
	if err != nil {
		return t.fail(fmt.Errorf("list Hosts: %w", err))
	}
	if len(raw) != 1 || raw[0].Group != "prod/web" {
		return t.fail(fmt.Errorf("Host after drop = %+v, want group prod/web", raw))
	}
	if t.state.GroupMemberCount("prod/web") != 1 {
		return t.fail(fmt.Errorf("group member count = %d, want 1",
			t.state.GroupMemberCount("prod/web")))
	}

	// The Host has no username of its own; now that it is a member, the group
	// default is the effective value and is shown in the list.
	effective, ok := t.state.EffectiveHost(t.state.Items()[0].ID)
	if !ok || effective.Username != "deploy" {
		return t.fail(fmt.Errorf("effective Host username = %q, want the group default",
			effective.Username))
	}
	if stored := t.state.Items()[0]; stored.Username != "" {
		return t.fail(fmt.Errorf("the group default was written onto the Host: %q",
			stored.Username))
	}
	if !textsContained(t.tester.Texts(), "deploy") {
		return t.fail(fmt.Errorf("the Host row does not show the group default: %v",
			t.tester.Texts()))
	}
	return selftest.Passed(
		"real store: group create, Host create, drag/drop membership, inherited username",
		"appvault.Service + store.Store + vaultui.View + ui.Tester")
}

func (t *selftest20) proxyStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	if err := t.tester.Click(m.VaultProxyNew()); err != nil {
		return t.fail(fmt.Errorf("open the Proxy Profile editor: %w", err))
	}
	t.tester.Frame()
	t.state.ProxyForm().Label = "office"
	t.state.ProxyForm().Type = string(domainvault.ProxySOCKS5)
	t.state.ProxyForm().Host = "socks.example.com"
	t.state.ProxyForm().Port = "1080"
	if err := t.state.SaveProxyProfile(); err != nil {
		return t.fail(fmt.Errorf("save Proxy Profile: %w", err))
	}
	t.tester.Frame()
	if t.state.ProxyProfileCount() != 1 {
		return t.fail(fmt.Errorf("Proxy Profiles = %d, want 1", t.state.ProxyProfileCount()))
	}
	profile := t.state.ProxyProfiles()[0]

	id := model.HostID(t.state.Items()[0].ID)
	t.state.BeginEdit(id)
	t.state.Form().ProxyProfileID = profile.ID
	if err := t.state.Save(); err != nil {
		return t.fail(fmt.Errorf("select the Proxy Profile on the Host: %w", err))
	}
	t.tester.Frame()

	effective, err := t.service.Get(context.Background(), id)
	if err != nil {
		return t.fail(fmt.Errorf("get effective Host: %w", err))
	}
	if effective.Proxy == nil || effective.Proxy.Host != "socks.example.com" ||
		effective.Proxy.Port != 1080 {
		return t.fail(fmt.Errorf("effective Proxy = %+v, want the selected profile",
			effective.Proxy))
	}

	// Deleting the profile clears the Host's soft reference and keeps the Host.
	t.state.RequestDeleteProxyProfile(profile.ID)
	t.tester.Frame()
	if !textsContained(t.tester.Texts(), "Delete") {
		return t.fail(fmt.Errorf("proxy delete confirmation missing: %v", t.tester.Texts()))
	}
	if err := t.tester.Click(m.VaultHostDelete()); err != nil {
		return t.fail(fmt.Errorf("confirm Proxy Profile delete: %w", err))
	}
	t.tester.Frame()
	if t.state.ProxyProfileCount() != 0 || t.state.HostCount() != 1 {
		return t.fail(fmt.Errorf("after profile delete: profiles = %d, Hosts = %d",
			t.state.ProxyProfileCount(), t.state.HostCount()))
	}
	raw, err := t.service.List(context.Background())
	if err != nil {
		return t.fail(fmt.Errorf("list Hosts: %w", err))
	}
	if raw[0].ProxyProfileID != "" {
		return t.fail(fmt.Errorf("Host kept the deleted profile reference %q",
			raw[0].ProxyProfileID))
	}
	return selftest.Passed(
		"real store: profile create, Host selection materialized, delete clears the reference",
		"appvault.Service + store.Store + vaultui.View + ui.Tester")
}

func (t *selftest20) knownHostStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	record := domainconn.KnownHost{
		ID: "kh-selftest-20", Hostname: "known.example.com", Port: 2222,
		KeyType: "ssh-ed25519", PublicKey: "ssh-ed25519 AAAA",
		Fingerprint: "SHA256:selftest20",
	}
	if err := t.store.UpsertKnownHost(context.Background(), record); err != nil {
		return t.fail(fmt.Errorf("record Known Host: %w", err))
	}
	if err := t.state.Reload(); err != nil {
		return t.fail(fmt.Errorf("reload Vault: %w", err))
	}
	t.tester.Frame()
	if t.state.KnownHostCount() != 1 {
		return t.fail(fmt.Errorf("Known Hosts = %d, want 1", t.state.KnownHostCount()))
	}
	if !t.tester.HasText("known.example.com:2222") ||
		!t.tester.HasText(m.VaultKnownHostTrusted()) ||
		!textsContained(t.tester.Texts(), "ssh-ed25519") {
		return t.fail(fmt.Errorf("Known Host row is missing trust state: %v", t.tester.Texts()))
	}

	if err := t.tester.Click(m.VaultKnownHostRemove()); err != nil {
		return t.fail(fmt.Errorf("click Remove trust: %w", err))
	}
	t.tester.Frame()
	if !textsContained(t.tester.Texts(), "known.example.com:2222") {
		return t.fail(fmt.Errorf("remove-trust confirmation is missing: %v", t.tester.Texts()))
	}
	if err := t.tester.Click(m.VaultHostDelete()); err != nil {
		return t.fail(fmt.Errorf("confirm remove trust: %w", err))
	}
	t.tester.Frame()
	if t.state.KnownHostCount() != 0 {
		return t.fail(fmt.Errorf("Known Hosts after remove = %d", t.state.KnownHostCount()))
	}
	if t.state.HostCount() != 1 {
		return t.fail(fmt.Errorf("removing trust deleted a Host"))
	}
	records, err := t.store.ListKnownHosts(context.Background())
	if err != nil {
		return t.fail(fmt.Errorf("list Known Hosts: %w", err))
	}
	if len(records) != 0 {
		return t.fail(fmt.Errorf("store still has %d Known Hosts", len(records)))
	}
	return selftest.Passed(
		"real store: trusted Known Host listed with key state and removed",
		"store.Store + appvault.Service + vaultui.View + ui.Tester")
}

func (t *selftest20) deleteGroupStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	if err := t.tester.Click(m.VaultHostDelete()); err != nil {
		return t.fail(fmt.Errorf("click group Delete: %w", err))
	}
	t.tester.Frame()
	if !textsContained(t.tester.Texts(), "Delete Host Group") {
		return t.fail(fmt.Errorf("group delete confirmation is missing: %v", t.tester.Texts()))
	}
	if err := t.tester.Click(m.VaultHostDelete()); err != nil {
		return t.fail(fmt.Errorf("confirm group Delete: %w", err))
	}
	t.tester.Frame()

	if t.state.GroupCount() != 0 {
		return t.fail(fmt.Errorf("Host Groups after delete = %d", t.state.GroupCount()))
	}
	if t.state.HostCount() != 1 {
		return t.fail(fmt.Errorf("Hosts after group delete = %d, want the Host kept",
			t.state.HostCount()))
	}
	raw, err := t.service.List(context.Background())
	if err != nil {
		return t.fail(fmt.Errorf("list Hosts: %w", err))
	}
	if raw[0].Group != "" {
		return t.fail(fmt.Errorf("Host group after delete = %q, want the Vault root",
			raw[0].Group))
	}
	return selftest.Passed(
		"real store: deleting a group keeps its Hosts and returns them to the Vault root",
		"store.Store + appvault.Service + vaultui.View + ui.Tester")
}

func (t *selftest20) layoutStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	for _, size := range []struct {
		width, height int
	}{{1100, 720}, {720, 480}} {
		t.tester.SetSize(size.width, size.height)
		t.tester.Frame()

		for _, want := range []string{
			m.VaultHostNew(), m.VaultGroupNew(), m.VaultProxyNew(),
			m.VaultKnownHostsTitle(t.state.KnownHostCount()),
			m.VaultKeyListTitle(t.state.KeyCount()),
			m.VaultIdentityListTitle(t.state.IdentityCount()),
			"web-1",
		} {
			if !t.tester.HasText(want) {
				return t.fail(fmt.Errorf("%dx%d: %q is not rendered: %v",
					size.width, size.height, want, t.tester.Texts()))
			}
		}
		// Every laid-out text must stay inside the window. Texts the Scroll
		// has not reached have no geometry yet, so they are not checked.
		for _, text := range t.tester.Texts() {
			rect, ok := t.tester.Find(text)
			if !ok || rect.W <= 0 {
				continue
			}
			if rect.X < -0.5 || rect.X+rect.W > float32(size.width)+0.5 {
				return t.fail(fmt.Errorf("%dx%d: %q overflows horizontally: %+v",
					size.width, size.height, text, rect))
			}
		}
	}
	t.tester.SetSize(1100, 720)
	t.tester.Frame()
	t.cleanup()
	return selftest.Passed(
		"the Vault renders at 1100x720 and 720x480 with no horizontal overflow",
		"vaultui.View + ui.Tester")
}

func (t *selftest20) start() error {
	if t.state != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "natcatty-selftest-20-")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	db, err := store.Open(context.Background(), filepath.Join(dir, "vault.db"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("open store: %w", err)
	}
	service := appvault.NewService(db,
		appvault.WithGroups(db),
		appvault.WithProxyProfiles(db),
		appvault.WithKnownHosts(db),
	)
	state := vaultstate.NewState(service,
		vaultstate.WithGroups(service),
		vaultstate.WithProxyProfiles(service),
		vaultstate.WithKnownHosts(service),
	)
	if err := state.Reload(); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(dir)
		return fmt.Errorf("load the Vault: %w", err)
	}
	t.dir, t.store, t.service, t.state = dir, db, service, state
	t.tester = ui.NewTester(vaultui.View(state), 1100, 720)
	t.tester.SetLocale("en")
	t.tester.Frame()
	return nil
}

func (t *selftest20) fail(err error) selftest.Outcome {
	t.cleanup()
	return selftest.Failed(err)
}

func (t *selftest20) cleanup() {
	if t.store != nil {
		_ = t.store.Close()
	}
	if t.dir != "" {
		_ = os.RemoveAll(t.dir)
	}
	t.dir, t.store, t.service, t.state, t.tester = "", nil, nil, nil, nil
}

func textsContained(texts []string, want string) bool {
	for _, text := range texts {
		if strings.Contains(text, want) {
			return true
		}
	}
	return false
}
