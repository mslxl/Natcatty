package vault

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// fakeHosts is the application seam the Vault view is tested against.
type fakeHosts struct {
	items  []domainvault.Host
	nextID int
}

func (f *fakeHosts) List(context.Context) ([]domainvault.Host, error) {
	return append([]domainvault.Host(nil), f.items...), nil
}

func (f *fakeHosts) Create(_ context.Context, draft domainvault.Draft) (domainvault.Host, error) {
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
		Username: normalized.Username,
		Protocol: normalized.Protocol,
		Group:    normalized.Group,
		Tags:     normalized.Tags,
	}
	f.items = append(f.items, host)
	return host, nil
}

func (f *fakeHosts) Update(_ context.Context, id model.HostID, draft domainvault.Draft) (domainvault.Host, error) {
	normalized, err := domainvault.NormalizeDraft(draft)
	if err != nil {
		return domainvault.Host{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	for i, host := range f.items {
		if host.ID == id {
			host.Label = normalized.Label
			host.Hostname = normalized.Hostname
			host.Port = normalized.Port
			host.Username = normalized.Username
			host.Protocol = normalized.Protocol
			host.Group = normalized.Group
			host.Tags = normalized.Tags
			host.Pinned = normalized.Pinned
			f.items[i] = host
			return host, nil
		}
	}
	return domainvault.Host{}, context.Canceled
}

func (f *fakeHosts) Delete(_ context.Context, id model.HostID) error {
	for i, host := range f.items {
		if host.ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			return nil
		}
	}
	return context.Canceled
}

func newView(t *testing.T, repo *fakeHosts) (*vaultstate.State, *ui.Tester) {
	t.Helper()
	state := vaultstate.NewState(repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return state, ui.NewTester(View(state), 900, 600)
}

func TestVaultViewShowsTheEmptyStateAndOpensTheEditor(t *testing.T) {
	_, tt := newView(t, &fakeHosts{})

	if !tt.HasText(locales.In("en").VaultHostEmpty()) {
		t.Fatalf("Vault does not show its empty state, texts %q", tt.Texts())
	}

	if err := tt.Click(locales.In("en").VaultHostNew()); err != nil {
		t.Fatalf("click New Host: %v", err)
	}
	tt.Frame()

	for _, want := range []string{
		locales.In("en").VaultHostFormCreateTitle(),
		locales.In("en").VaultHostHostname(),
		locales.In("en").VaultHostCredentialsNote(),
	} {
		if !tt.HasText(want) {
			t.Errorf("editor does not show %q, texts %q", want, tt.Texts())
		}
	}
}

func TestVaultViewListsHostDetails(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{
		{
			ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
			Username: "root", Protocol: domainvault.ProtocolSSH,
			Group: "prod/web", Tags: []string{"prod"}, Pinned: true,
		},
		{
			ID: "host-2", Label: "My shell", Protocol: domainvault.ProtocolLocal,
		},
	}}
	_, tt := newView(t, repo)
	m := locales.In("en")

	// A card shows its Host's label and where it connects; the group is a
	// card of its own.
	for _, want := range []string{
		"web", "root@example.com", "prod", m.VaultGroupMemberCount(1),
		"My shell", m.VaultHostLocal(),
	} {
		if !tt.HasText(want) {
			t.Errorf("Vault does not show %q, texts %q", want, tt.Texts())
		}
	}
	// Its menu holds the actions, a pinned Host's offering Unpin.
	if err := tt.Click(m.VaultItemActions("web")); err != nil {
		t.Fatalf("open the card menu: %v", err)
	}
	menu := strings.Join(tt.Menu(), "|")
	for _, want := range []string{m.VaultHostConnect(), m.VaultHostEdit(), m.VaultHostUnpin(), m.VaultHostDelete()} {
		if !strings.Contains(menu, want) {
			t.Errorf("the card menu %q has no %q", menu, want)
		}
	}
}

// TestVaultViewShowsAnOffThreadVaultChangeOnTheNextFrame proves the view
// applies a sync-applied snapshot's invalidation on the UI thread: no manual
// Reload and no restart.
func TestVaultViewShowsAnOffThreadVaultChangeOnTheNextFrame(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{ID: "host-1", Label: "gui-alpha"}}}
	state, tt := newView(t, repo)
	if !tt.HasText("gui-alpha") {
		t.Fatalf("Vault does not list the initial Host, texts %q", tt.Texts())
	}

	repo.items = []domainvault.Host{{ID: "host-cloud", Label: "cloud-beta"}}
	state.InvalidateLists()
	tt.Frame()

	if !tt.HasText("cloud-beta") {
		t.Fatalf("the frame after InvalidateLists does not list the applied snapshot, texts %q", tt.Texts())
	}
	if tt.HasText("gui-alpha") {
		t.Fatalf("the stale Host is still listed, texts %q", tt.Texts())
	}
}

func TestVaultViewShowsTheX11WarningOnlyAfterEnablingForSSH(t *testing.T) {
	m := locales.In("en")
	repo := &fakeHosts{}
	state := vaultstate.NewState(repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// The Host editor is long; a taller frame keeps the X11 controls clickable.
	tt := ui.NewTester(View(state), 900, 1800)
	state.BeginCreate()
	tt.Frame()

	if !tt.HasText(m.VaultHostX11Forwarding()) {
		t.Fatal("the SSH Host editor does not offer X11 forwarding")
	}
	if tt.HasText(m.VaultHostX11Warning()) {
		t.Fatal("the X11 warning was shown before X11 was enabled")
	}
	if tt.HasText(m.VaultHostX11Acknowledge()) {
		t.Fatal("the X11 acknowledgement was shown before X11 was enabled")
	}

	if err := tt.Click(m.VaultHostX11Forwarding()); err != nil {
		t.Fatalf("click X11 forwarding: %v", err)
	}
	tt.Frame()
	if !state.Form().X11Forwarding {
		t.Fatal("the click did not enable X11 on the form")
	}
	if !tt.HasText(m.VaultHostX11Warning()) {
		t.Fatal("enabling X11 did not show the security warning")
	}
	if !tt.HasText(m.VaultHostX11Acknowledge()) {
		t.Fatal("enabling X11 did not show the acknowledgement")
	}

	// A non-SSH protocol cannot use X11, so the editor must not offer it.
	state.Form().Protocol = string(domainvault.ProtocolLocal)
	tt.Frame()
	if tt.HasText(m.VaultHostX11Forwarding()) {
		t.Fatal("the X11 option was shown for a non-SSH Host")
	}
}

func TestVaultViewDeleteConfirmationNeverShowsARawID(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "example.com", Port: 22, Protocol: domainvault.ProtocolSSH},
	}}
	state, tt := newView(t, repo)

	state.RequestDelete("missing-host")
	tt.Frame()

	if !tt.HasText(locales.In("en").VaultHostDeleteConfirmUnknown()) {
		t.Fatalf("Vault does not show the unknown-Host confirmation, texts %q", tt.Texts())
	}
	if tt.HasText("missing-host") {
		t.Fatalf("Vault shows the raw Host id, texts %q", tt.Texts())
	}
}

func TestVaultViewLocalizesValidationProblems(t *testing.T) {
	repo := &fakeHosts{}
	state := vaultstate.NewState(repo)
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}
	state.BeginCreate()
	state.Form().Label = "web"
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(View(state), 900, 600)

	if !tt.HasText("A Host needs a hostname.") {
		t.Fatalf("Vault does not show the English problem, texts %q", tt.Texts())
	}

	tt.SetLocale("ru")
	if !tt.HasText("Хосту нужно имя хоста.") {
		t.Fatalf("Vault does not show the Russian problem, texts %q", tt.Texts())
	}
}

func TestVaultViewOffersNetworkDeviceModeOnlyForSSH(t *testing.T) {
	m := locales.In("en")
	state := vaultstate.NewState(&fakeHosts{})
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt := ui.NewTester(View(state), 900, 1800)
	state.BeginCreate()
	tt.Frame()

	if !tt.HasText(m.VaultHostNetworkDevice()) {
		t.Fatal("the SSH Host editor does not offer network device mode")
	}
	if !tt.HasText(m.VaultHostNetworkDeviceHint()) {
		t.Fatal("network device mode has no explanation")
	}
	if state.Form().NetworkDevice {
		t.Fatal("network device mode was enabled by default")
	}

	if err := tt.Click(m.VaultHostNetworkDevice()); err != nil {
		t.Fatalf("click network device mode: %v", err)
	}
	tt.Frame()
	if !state.Form().NetworkDevice {
		t.Fatal("the click did not enable network device mode")
	}

	state.Form().Protocol = string(domainvault.ProtocolLocal)
	tt.Frame()
	if tt.HasText(m.VaultHostNetworkDevice()) || tt.HasText(m.VaultHostNetworkDeviceHint()) {
		t.Fatal("network device mode was shown for a non-SSH Host")
	}
}

func TestVaultViewOffersMoshAndETFieldsForTheirProtocols(t *testing.T) {
	m := locales.In("en")
	state := vaultstate.NewState(&fakeHosts{})
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt := ui.NewTester(View(state), 900, 1800)
	state.BeginCreate()
	state.Form().Protocol = string(domainvault.ProtocolMosh)
	tt.Frame()

	if !tt.HasText(m.VaultHostMoshServerPath()) || !tt.HasText(m.VaultHostMoshServerPathHint()) {
		t.Fatal("the Mosh Host editor does not offer the server path")
	}
	if !tt.HasText(m.VaultHostMoshRestrictionHint()) {
		t.Fatal("the Mosh Host editor does not explain the route restriction")
	}
	if tt.HasText(m.VaultHostETPort()) || tt.HasText(m.VaultHostETRestrictionHint()) {
		t.Fatal("the ET port was shown for a Mosh Host")
	}

	state.Form().Protocol = string(domainvault.ProtocolET)
	tt.Frame()
	if !tt.HasText(m.VaultHostETPort()) || !tt.HasText(m.VaultHostETPortHint()) {
		t.Fatal("the ET Host editor does not offer the ET port")
	}
	if !tt.HasText(m.VaultHostETRestrictionHint()) {
		t.Fatal("the ET Host editor does not explain the route restriction")
	}
	if tt.HasText(m.VaultHostMoshServerPath()) || tt.HasText(m.VaultHostMoshRestrictionHint()) {
		t.Fatal("the mosh-server path was shown for an ET Host")
	}
}

// viewNetworkProfiles lists Network Profiles for the view's routing fields.
type viewNetworkProfiles struct {
	items []domainvault.NetworkProfile
}

func (f *viewNetworkProfiles) ListNetworkProfiles(context.Context) ([]domainvault.NetworkProfile, error) {
	return append([]domainvault.NetworkProfile(nil), f.items...), nil
}

func (f *viewNetworkProfiles) CreateNetworkProfile(_ context.Context,
	draft domainvault.NetworkProfileDraft) (domainvault.NetworkProfile, error) {
	return domainvault.NetworkProfile{}, nil
}

func (f *viewNetworkProfiles) UpdateNetworkProfile(_ context.Context, id string,
	draft domainvault.NetworkProfileDraft) (domainvault.NetworkProfile, error) {
	return domainvault.NetworkProfile{}, nil
}

func (f *viewNetworkProfiles) DeleteNetworkProfile(context.Context, string) error { return nil }

func newRoutingView(t *testing.T, opts ...vaultstate.Option) (*vaultstate.State, *ui.Tester) {
	t.Helper()
	hosts := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH, Group: "prod/web",
	}}}
	groups := &entityGroups{items: []domainvault.Group{
		{Path: "prod", NetworkProfileID: "net-1"},
		{Path: "prod/web"},
	}}
	profiles := &viewNetworkProfiles{items: []domainvault.NetworkProfile{
		{ID: "net-1", Label: "Tailnet", Kind: domainvault.NetworkKindTailscale},
	}}
	all := append([]vaultstate.Option{
		vaultstate.WithGroups(groups),
		vaultstate.WithNetworkProfiles(profiles),
	}, opts...)
	state := vaultstate.NewState(hosts, all...)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return state, ui.NewTester(View(state), 900, 1800)
}

func TestVaultViewHostEditorShowsTheNetworkProfileAndItsSource(t *testing.T) {
	state, tt := newRoutingView(t)
	m := locales.In("en")

	state.BeginEdit("host-1")
	tt.Frame()
	if !tt.HasText(m.VaultNetworkProfile()) || !tt.HasText(m.VaultHostNetworkProfileHint()) {
		t.Fatalf("the Host editor has no Network Profile selector, texts %q", tt.Texts())
	}
	if !tt.HasText(m.VaultNetworkProfileInherited("prod", "Tailnet")) {
		t.Fatalf("the Host editor does not show the group-inherited source, texts %q", tt.Texts())
	}
	if tt.HasText(m.VaultNetworkProfileSourceHost()) {
		t.Fatal("the Host editor claimed the inherited profile was set on the Host")
	}

	if err := tt.Click("Tailnet"); err != nil {
		t.Fatalf("click the Network Profile radio: %v", err)
	}
	tt.Frame()
	if state.Form().NetworkProfileID != "net-1" {
		t.Fatalf("the radio did not select net-1, form has %q", state.Form().NetworkProfileID)
	}
	if !tt.HasText(m.VaultNetworkProfileSourceHost()) {
		t.Fatalf("selecting a profile did not show the Host source, texts %q", tt.Texts())
	}

	state.BeginEditGroup("prod/web")
	tt.Frame()
	if !tt.HasText(m.VaultGroupNetworkProfileHint()) {
		t.Fatal("the Host Group editor has no Network Profile explanation")
	}
	if !tt.HasText(m.VaultNetworkProfileInherited("prod", "Tailnet")) {
		t.Fatalf("the Host Group editor does not show the parent default, texts %q", tt.Texts())
	}
}

func TestVaultViewNetworkDeleteBannerWarnsAboutAffectedRecords(t *testing.T) {
	state, tt := newRoutingView(t)
	m := locales.In("en")

	state.RequestDeleteNetworkProfile("net-1")
	tt.Frame()
	if !tt.HasText(m.VaultNetworkDeleteAffected(1, 2)) {
		t.Fatalf("the delete banner does not warn about the affected Host and Groups, texts %q", tt.Texts())
	}
	if tt.HasText(m.VaultNetworkDeleteActiveUse()) {
		t.Fatalf("the banner claimed active use without a runtime lookup, texts %q", tt.Texts())
	}
}

func TestVaultViewNetworkDeleteBannerWarnsAboutActiveUse(t *testing.T) {
	state, tt := newRoutingView(t, vaultstate.WithNetworkProfileRuntimeUsage(
		func(id string) bool { return id == "net-1" }))
	m := locales.In("en")

	state.RequestDeleteNetworkProfile("net-1")
	tt.Frame()
	if !tt.HasText(m.VaultNetworkDeleteActiveUse()) {
		t.Fatalf("the delete banner does not warn about active Sessions or leases, texts %q", tt.Texts())
	}

	state.RequestDeleteNetworkProfile("net-other")
	tt.Frame()
	if tt.HasText(m.VaultNetworkDeleteActiveUse()) {
		t.Fatalf("an unused profile showed the active-use warning, texts %q", tt.Texts())
	}
}

func TestVaultViewNetworkRowShowsProviderStatus(t *testing.T) {
	state, tt := newRoutingView(t)
	m := locales.In("en")

	state.PostNetworkProviderStatus(vaultstate.NetworkProviderStatus{
		ProfileID:    "net-1",
		Label:        "Tailnet",
		Kind:         string(domainvault.NetworkKindTailscale),
		Phase:        appconnection.ProviderPhaseNeedsLogin,
		TailscaleIPs: []string{"100.64.0.2"},
		DNSName:      "natcatty-office.tailnet.ts.net",
		AuthURL:      "https://login.tailscale.com/a/example",
		LastError:    "tailscale: machine authorization required",
	})
	tt.Frame()

	for _, want := range []string{
		m.VaultNetworkProviderStatus(m.NetworkProviderPhaseNeedsLogin(),
			"natcatty-office.tailnet.ts.net"),
		m.VaultNetworkLoginRequired("https://login.tailscale.com/a/example"),
		m.VaultNetworkLoginCopy(),
		m.VaultNetworkLoginOpen(),
		m.VaultNetworkLoginCancel(),
		m.VaultNetworkProviderStatusError("tailscale: machine authorization required"),
	} {
		if !tt.HasText(want) {
			t.Fatalf("the Network Profile row does not show %q, texts %q", want, tt.Texts())
		}
	}
}

func TestVaultViewNetworkRowOffersLoginAndRetry(t *testing.T) {
	state, tt := newRoutingView(t)
	m := locales.In("en")

	// A saved Network Profile with no provider status offers the first login.
	if !tt.HasText(m.VaultNetworkLoginStart()) {
		t.Fatalf("the Network Profile row does not offer a login, texts %q", tt.Texts())
	}

	state.PostNetworkProviderStatus(vaultstate.NetworkProviderStatus{
		ProfileID: "net-1",
		Phase:     appconnection.ProviderPhaseError,
		LastError: "tailscale: node failed while waiting for login",
	})
	tt.Frame()
	if !tt.HasText(m.VaultNetworkLoginRetry()) {
		t.Fatalf("a failed Provider does not offer Retry, texts %q", tt.Texts())
	}
	if tt.HasText(m.VaultNetworkLoginCancel()) {
		t.Fatal("a failed Provider offered Cancel instead of Retry")
	}

	state.PostNetworkProviderStatus(vaultstate.NetworkProviderStatus{
		ProfileID: "net-1",
		Phase:     appconnection.ProviderPhaseStarting,
	})
	tt.Frame()
	if !tt.HasText(m.VaultNetworkLoginCancel()) {
		t.Fatalf("a starting Provider does not offer Cancel, texts %q", tt.Texts())
	}
}

func TestVaultViewTailscaleEditorMarksTheControlURLExperimental(t *testing.T) {
	state, tt := newRoutingView(t)
	m := locales.In("en")

	state.BeginCreateNetworkProfile(domainvault.NetworkKindTailscale)
	tt.Frame()
	if !tt.HasText(m.VaultNetworkControlURLExperimental()) {
		t.Fatalf("the Control URL field is not marked experimental, texts %q", tt.Texts())
	}
}

func TestVaultViewTailscaleEditorDisclosesStateAndLogPrivacy(t *testing.T) {
	state, tt := newRoutingView(t)
	m := locales.In("en")

	state.BeginCreateNetworkProfile(domainvault.NetworkKindTailscale)
	tt.Frame()
	for _, want := range []string{
		m.VaultNetworkAuthKeyStateHint(),
		m.VaultNetworkPrivacyNotice(),
	} {
		if !tt.HasText(want) {
			t.Fatalf("the Tailscale editor does not show %q, texts %q", want, tt.Texts())
		}
	}
}

func TestVaultViewNetworkRowShowsWireGuardTunnelDetail(t *testing.T) {
	profiles := &viewNetworkProfiles{items: []domainvault.NetworkProfile{{
		ID:    "wg-1",
		Label: "Office WG",
		Kind:  domainvault.NetworkKindWireGuard,
		WireGuard: &domainvault.WireGuardNetwork{
			Addresses: []string{"10.0.0.2/32"},
			Peers:     []domainvault.WireGuardPeer{{ID: "peer-1", PublicKey: "peer-public"}},
		},
	}}}
	state := vaultstate.NewState(&fakeHosts{}, vaultstate.WithNetworkProfiles(profiles))
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt := ui.NewTester(View(state), 900, 900)
	handshake := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	state.PostNetworkProviderStatus(vaultstate.NetworkProviderStatus{
		ProfileID:            "wg-1",
		Label:                "Office WG",
		Kind:                 string(domainvault.NetworkKindWireGuard),
		Phase:                appconnection.ProviderPhaseRunning,
		WireGuardEndpoint:    "203.0.113.7:51820",
		HandshakeEstablished: true,
		LastHandshake:        handshake,
		RxBytes:              1536,
		TxBytes:              512,
	})
	tt.Frame()

	m := locales.In("en")
	for _, want := range []string{
		m.VaultNetworkProviderStatus(m.NetworkProviderPhaseRunning(), "203.0.113.7:51820"),
		uiconnection.WireGuardDetail(m, true, handshake, 1536, 512),
	} {
		if !tt.HasText(want) {
			t.Fatalf("the WireGuard profile row does not show %q, texts %q", want, tt.Texts())
		}
	}
}

func TestVaultViewWireGuardEditorWarnsWithoutTunnelDNS(t *testing.T) {
	state := vaultstate.NewState(&fakeHosts{})
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt := ui.NewTester(View(state), 900, 1800)
	m := locales.In("en")
	state.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard)
	tt.Frame()
	if !tt.HasText(m.VaultNetworkWarningWireGuardDNS()) {
		t.Fatalf("the WireGuard editor does not warn about the missing tunnel DNS, texts %q", tt.Texts())
	}
	state.NetworkProfileForm().DNS = "10.0.0.1"
	tt.Frame()
	if tt.HasText(m.VaultNetworkWarningWireGuardDNS()) {
		t.Fatalf("the warning remained after a tunnel DNS server was entered, texts %q", tt.Texts())
	}
}
