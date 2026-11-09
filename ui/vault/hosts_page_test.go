package vault

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// newLayoutView renders the Vault with a layout the test owns, so a toggle's
// persisted preference can be read after a click.
func newLayoutView(t *testing.T, state *vaultstate.State, layout *domainpreferences.Layout, width, height int) *ui.Tester {
	t.Helper()
	view := View(state, WithLayout(
		func() domainpreferences.Layout { return *layout },
		func(change func(*domainpreferences.Layout)) { change(layout) },
	))
	return ui.NewTester(view, width, height)
}

func layoutHost(id string, label string) domainvault.Host {
	return domainvault.Host{
		ID: model.HostID(id), Label: label, Hostname: label + ".example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}
}

func TestRootLiftsPinnedAndRecentHostsOutOfTheList(t *testing.T) {
	m := locales.In("en")
	pinned := layoutHost("pin", "pinned-host")
	pinned.Pinned = true
	recent := layoutHost("recent", "recent-host")
	recent.LastConnectedAt = 1_700_000_000_000
	plain := layoutHost("plain", "plain-host")
	repo := &fakeHosts{items: []domainvault.Host{pinned, recent, plain}}
	state := vaultstate.NewState(repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	layout := domainpreferences.Defaults().Layout
	tt := newLayoutView(t, state, &layout, 1000, 700)

	for _, want := range []string{m.VaultHostsPinned(), m.VaultHostsRecent(), "pinned-host", "recent-host", "plain-host"} {
		if !tt.HasText(want) {
			t.Errorf("root does not show %q, texts %q", want, tt.Texts())
		}
	}
	// The main list keeps only the Host neither section took.
	if !tt.HasText(m.VaultHostListTitle(1)) {
		t.Errorf("main Host list = %q, want one Host", tt.Texts())
	}
}

func TestRecentlyConnectedSectionRespectsItsSetting(t *testing.T) {
	m := locales.In("en")
	recent := layoutHost("recent", "recent-host")
	recent.LastConnectedAt = 1_700_000_000_000
	repo := &fakeHosts{items: []domainvault.Host{recent}}
	state := vaultstate.NewState(repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	layout := domainpreferences.Defaults().Layout
	layout.ShowRecentHosts = false
	tt := newLayoutView(t, state, &layout, 1000, 700)
	if tt.HasText(m.VaultHostsRecent()) {
		t.Fatalf("the disabled Recently Connected section is drawn, texts %q", tt.Texts())
	}
	if !tt.HasText("recent-host") {
		t.Fatalf("the Host disappeared with its section, texts %q", tt.Texts())
	}
}

func TestRootCanHideGroupedHosts(t *testing.T) {
	loose := layoutHost("loose", "loose-host")
	grouped := layoutHost("grouped", "grouped-host")
	grouped.Group = "prod"
	repo := &fakeHosts{items: []domainvault.Host{loose, grouped}}
	state := vaultstate.NewState(repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	layout := domainpreferences.Defaults().Layout
	layout.ShowOnlyUngroupedHostsInRoot = true
	tt := newLayoutView(t, state, &layout, 1000, 700)
	if !tt.HasText("loose-host") || tt.HasText("grouped-host") {
		t.Fatalf("root with ungrouped-only shows %q", tt.Texts())
	}
	if !tt.HasText("prod") {
		t.Fatalf("the group card disappeared with its Hosts, texts %q", tt.Texts())
	}

	layout.ShowOnlyUngroupedHostsInRoot = false
	tt = newLayoutView(t, state, &layout, 1000, 700)
	if !tt.HasText("grouped-host") {
		t.Fatalf("root with every Host misses the grouped one, texts %q", tt.Texts())
	}
}

func TestViewSwitchPersistsAndTreeCollapses(t *testing.T) {
	m := locales.In("en")
	web := layoutHost("web", "web-host")
	web.Group = "prod"
	repo := &fakeHosts{items: []domainvault.Host{web}}
	state := vaultstate.NewState(repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	layout := domainpreferences.Defaults().Layout
	tt := newLayoutView(t, state, &layout, 1000, 700)
	if err := tt.Click(m.VaultViewTree()); err != nil {
		t.Fatalf("switch to the tree view: %v", err)
	}
	tt.Frame()
	if layout.HostViewMode != domainpreferences.HostViewTree {
		t.Fatalf("view mode = %q, want tree", layout.HostViewMode)
	}
	if !tt.HasText("prod") || !tt.HasText("web-host") {
		t.Fatalf("tree does not show the group and its Host, texts %q", tt.Texts())
	}

	if err := tt.Click(m.VaultTreeToggle()); err != nil {
		t.Fatalf("collapse the group: %v", err)
	}
	tt.Frame()
	if tt.HasText("web-host") {
		t.Fatalf("a collapsed group still lists its Hosts, texts %q", tt.Texts())
	}
	if !tt.HasText("prod") {
		t.Fatalf("collapsing the group hid the group itself, texts %q", tt.Texts())
	}

	// The row itself toggles too, as the original's tree row does.
	if err := tt.Click("prod"); err != nil {
		t.Fatalf("click the group row: %v", err)
	}
	tt.Frame()
	if !tt.HasText("web-host") {
		t.Fatalf("clicking the row did not expand the group, texts %q", tt.Texts())
	}
}

func TestSortModesReorderTheHostList(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{
		layoutHost("b", "beta-host"),
		layoutHost("a", "alpha-host"),
	}}
	state := vaultstate.NewState(repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	layout := domainpreferences.Defaults().Layout
	layout.HostSortMode = domainpreferences.HostSortAZ
	tt := newLayoutView(t, state, &layout, 1000, 700)
	alpha, beta := textIndex(tt, "alpha-host"), textIndex(tt, "beta-host")
	if alpha < 0 || beta < 0 || alpha > beta {
		t.Fatalf("A-Z order = alpha %d, beta %d, texts %q", alpha, beta, tt.Texts())
	}

	layout.HostSortMode = domainpreferences.HostSortZA
	tt = newLayoutView(t, state, &layout, 1000, 700)
	alpha, beta = textIndex(tt, "alpha-host"), textIndex(tt, "beta-host")
	if alpha < 0 || beta < 0 || beta > alpha {
		t.Fatalf("Z-A order = alpha %d, beta %d, texts %q", alpha, beta, tt.Texts())
	}
}

func TestClickBehaviorConnectsOnOneClickOrTwo(t *testing.T) {
	m := locales.In("en")
	repo := &fakeHosts{items: []domainvault.Host{layoutHost("web", "web-host")}}
	var connected []model.HostID
	state := vaultstate.NewState(repo, vaultstate.WithConnect(func(host domainvault.Host) {
		connected = append(connected, host.ID)
	}))
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	layout := domainpreferences.Defaults().Layout
	tt := newLayoutView(t, state, &layout, 1000, 700)
	if err := tt.Click("web-host"); err != nil {
		t.Fatalf("click the Host: %v", err)
	}
	tt.Frame()
	if len(connected) != 1 || connected[0] != "web" {
		t.Fatalf("connect on one click = %v, want [web]", connected)
	}

	// The card's own controls do not activate it: opening the menu is not a
	// connection.
	if err := tt.Click(m.VaultItemActions("web-host")); err != nil {
		t.Fatalf("open the Host menu: %v", err)
	}
	tt.CloseMenu()
	tt.Frame()
	if len(connected) != 1 {
		t.Fatalf("opening the menu connected again: %v", connected)
	}

	connected = nil
	layout.HostClickBehavior = domainpreferences.HostClickSelect
	tt = newLayoutView(t, state, &layout, 1000, 700)
	if err := tt.Click("web-host"); err != nil {
		t.Fatalf("select the Host: %v", err)
	}
	tt.Frame()
	if len(connected) != 0 {
		t.Fatalf("the first select click connected: %v", connected)
	}
	// A person clicks again after thinking; the tester's back-to-back clicks
	// would otherwise register as one double click.
	tt.Advance(time.Second)
	tt.Frame()
	if err := tt.Click("web-host"); err != nil {
		t.Fatalf("activate the Host: %v", err)
	}
	tt.Frame()
	if len(connected) != 1 {
		t.Fatalf("the second click did not connect: %v", connected)
	}
}

func TestDuplicateOpensTheEditorWithACopy(t *testing.T) {
	m := locales.In("en")
	repo := &fakeHosts{items: []domainvault.Host{layoutHost("web", "web-host")}}
	state := vaultstate.NewState(repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	layout := domainpreferences.Defaults().Layout
	tt := newLayoutView(t, state, &layout, 1000, 700)

	if err := tt.Click(m.VaultItemActions("web-host")); err != nil {
		t.Fatalf("open the Host menu: %v", err)
	}
	tt.Frame()
	menu := tt.Menu()
	if !slices.Contains(menu, m.VaultHostDuplicate()) {
		t.Fatalf("the Host menu has %q, want Duplicate", menu)
	}
	if err := tt.ChooseMenuItem(m.VaultHostDuplicate()); err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	tt.Frame()
	if !tt.HasText(m.VaultHostFormCreateTitle()) {
		t.Fatalf("duplicate did not open the create editor, texts %q", tt.Texts())
	}
	if want := "web-host (" + m.SessionMenuCopy() + ")"; state.Form().Label != want {
		t.Fatalf("duplicate label = %q, want %q", state.Form().Label, want)
	}
	if state.Form().ID != "" {
		t.Fatalf("duplicate kept the source identity %q", state.Form().ID)
	}
}

type menuCredentials struct{ password string }

func (f *menuCredentials) HostCredentials(context.Context, model.HostID) (appvault.HostCredentials, error) {
	return appvault.HostCredentials{Password: f.password}, nil
}

// The Host menu carries the original's Host-level actions: dual-pane SFTP, the
// Host browser, and Copy Credentials.
func TestHostMenuOffersSftpBrowserAndCredentials(t *testing.T) {
	m := locales.In("en")
	hosts := &entityHosts{items: []domainvault.Host{layoutHost("web", "web-host")}}
	groups := &entityGroups{hosts: hosts}
	hosts.groups = groups
	state := vaultstate.NewState(hosts,
		vaultstate.WithGroups(groups),
		vaultstate.WithHostCredentials(&menuCredentials{password: "s3cret"}),
	)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	layout := domainpreferences.Defaults().Layout
	var openedSFTP, openedBrowser domainvault.Host
	var copied string
	view := View(state,
		WithLayout(func() domainpreferences.Layout { return layout },
			func(change func(*domainpreferences.Layout)) { change(&layout) }),
		WithOpenSFTP(func(host domainvault.Host) { openedSFTP = host }, func() bool { return true }),
		WithOpenBrowser(func(host domainvault.Host) { openedBrowser = host }),
		WithClipboard(func(text string) { copied = text }),
	)
	tt := ui.NewTester(view, 1000, 700)
	if err := tt.Click(m.VaultItemActions("web-host")); err != nil {
		t.Fatalf("open the Host menu: %v", err)
	}
	tt.Frame()
	menu := tt.Menu()
	for _, want := range []string{m.VaultHostOpenSftp(), m.VaultHostOpenBrowser(),
		m.VaultHostCopyCredentials(), m.VaultHostCopyHostname(), m.VaultHostDuplicate()} {
		if !slices.Contains(menu, want) {
			t.Fatalf("Host menu = %q, missing %q", menu, want)
		}
	}

	if err := tt.ChooseMenuItem(m.VaultHostCopyCredentials()); err != nil {
		t.Fatalf("choose Copy credentials: %v", err)
	}
	if want := "host: web-host.example.com\nusername: root\npassword: s3cret"; copied != want {
		t.Fatalf("clipboard = %q, want %q", copied, want)
	}

	if err := tt.Click(m.VaultItemActions("web-host")); err != nil {
		t.Fatalf("reopen the Host menu: %v", err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem(m.VaultHostOpenSftp()); err != nil {
		t.Fatalf("choose Open SFTP: %v", err)
	}
	if openedSFTP.ID != "web" {
		t.Fatalf("Open SFTP got %+v, want web", openedSFTP)
	}

	if err := tt.Click(m.VaultItemActions("web-host")); err != nil {
		t.Fatalf("reopen the Host menu: %v", err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem(m.VaultHostOpenBrowser()); err != nil {
		t.Fatalf("choose Open host browser: %v", err)
	}
	if openedBrowser.ID != "web" {
		t.Fatalf("Open host browser got %+v, want web", openedBrowser)
	}
}

func textIndex(tt *ui.Tester, text string) int {
	for index, candidate := range tt.Texts() {
		if candidate == text {
			return index
		}
	}
	return -1
}
