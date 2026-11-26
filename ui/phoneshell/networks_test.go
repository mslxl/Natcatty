package phoneshell

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// fakeNetworkVault is the VaultStore the Networks page tests drive. It embeds
// the page family's Vault fake so the Shell's other lists still load, and adds
// the Network Profile half the application Vault service implements.
type fakeNetworkVault struct {
	*fakeVault

	networks   []domainvault.NetworkProfile
	listErr    error
	createErr  error
	updateErr  error
	deleteErr  error
	attempts   []domainvault.NetworkProfileDraft
	updated    []networkDraftUpdate
	deletedIDs []string
	nextID     int
}

type networkDraftUpdate struct {
	ID    string
	Draft domainvault.NetworkProfileDraft
}

func (f *fakeNetworkVault) ListNetworkProfiles(context.Context) ([]domainvault.NetworkProfile, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]domainvault.NetworkProfile(nil), f.networks...), nil
}

func (f *fakeNetworkVault) CreateNetworkProfile(_ context.Context,
	draft domainvault.NetworkProfileDraft) (domainvault.NetworkProfile, error) {

	if f.createErr != nil {
		return domainvault.NetworkProfile{}, f.createErr
	}
	normalized, err := domainvault.NormalizeNetworkProfileDraft(draft)
	if err != nil {
		return domainvault.NetworkProfile{}, err
	}
	f.attempts = append(f.attempts, normalized)
	f.nextID++
	profile := domainvault.NetworkProfile{
		ID:       fmt.Sprintf("net-%d", f.nextID),
		Label:    normalized.Label,
		Kind:     normalized.Kind,
		OrderKey: int64(f.nextID * 1000),
	}
	applyNetworkDraft(&profile, normalized)
	f.networks = append(f.networks, profile)
	return profile, nil
}

func (f *fakeNetworkVault) UpdateNetworkProfile(_ context.Context, id string,
	draft domainvault.NetworkProfileDraft) (domainvault.NetworkProfile, error) {

	if f.updateErr != nil {
		return domainvault.NetworkProfile{}, f.updateErr
	}
	normalized, err := domainvault.NormalizeNetworkProfileDraft(draft)
	if err != nil {
		return domainvault.NetworkProfile{}, err
	}
	f.updated = append(f.updated, networkDraftUpdate{ID: id, Draft: normalized})
	for index := range f.networks {
		if f.networks[index].ID != id {
			continue
		}
		existing := f.networks[index]
		if existing.Kind != normalized.Kind {
			return domainvault.NetworkProfile{}, errors.New("kind change")
		}
		existing.Label = normalized.Label
		applyNetworkDraft(&existing, normalized)
		f.networks[index] = existing
		return existing, nil
	}
	return domainvault.NetworkProfile{}, errors.New("not found")
}

func (f *fakeNetworkVault) DeleteNetworkProfile(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedIDs = append(f.deletedIDs, id)
	kept := f.networks[:0]
	for _, profile := range f.networks {
		if profile.ID != id {
			kept = append(kept, profile)
		}
	}
	f.networks = kept
	return nil
}

// applyNetworkDraft mirrors the service's non-secret record shape: the
// credential plaintext stays in the draft and only the Has* markers reach the
// profile.
func applyNetworkDraft(profile *domainvault.NetworkProfile, draft domainvault.NetworkProfileDraft) {
	switch draft.Kind {
	case domainvault.NetworkKindTailscale:
		config := draft.Tailscale
		profile.Tailscale = &domainvault.TailscaleNetwork{
			Hostname:   config.Hostname,
			ControlURL: config.ControlURL,
			Ephemeral:  config.Ephemeral,
			Tags:       append([]string(nil), config.Tags...),
			HasAuthKey: config.AuthKey != "",
		}
		profile.WireGuard = nil
	case domainvault.NetworkKindWireGuard:
		config := draft.WireGuard
		network := &domainvault.WireGuardNetwork{
			Addresses:     append([]string(nil), config.Addresses...),
			DNS:           append([]string(nil), config.DNS...),
			MTU:           config.MTU,
			ListenPort:    config.ListenPort,
			HasPrivateKey: config.PrivateKey != "",
		}
		for index, peer := range config.Peers {
			network.Peers = append(network.Peers, domainvault.WireGuardPeer{
				ID:                  fmt.Sprintf("peer-%d", index+1),
				PublicKey:           peer.PublicKey,
				Endpoint:            peer.Endpoint,
				AllowedIPs:          append([]string(nil), peer.AllowedIPs...),
				PersistentKeepalive: peer.PersistentKeepalive,
				HasPresharedKey:     peer.PresharedKey != "",
			})
		}
		profile.WireGuard = network
		profile.Tailscale = nil
	}
}

func openNetworks(t *testing.T, store *fakeNetworkVault, opts ...Option) (*Shell, *ui.Tester) {
	t.Helper()
	options := []Option{WithVault(store)}
	options = append(options, opts...)
	shell := New(options...)
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return shell, openSectionPage(t, shell, Vault, "Networks")
}

func TestNetworksListAddsEditsAndDeletesProfiles(t *testing.T) {
	store := &fakeNetworkVault{fakeVault: &fakeVault{}}
	_, tt := openNetworks(t, store)

	if !tt.HasText("No Network Profiles yet. Profiles added on the desktop appear here.") {
		t.Fatalf("empty state is missing; texts: %v", tt.Texts())
	}

	click(t, tt, "Add Tailscale profile")
	typeInto(t, tt, "Label", "office tailnet")
	typeInto(t, tt, "Hostname", "phone-node")
	typeInto(t, tt, "Auth key", "tskey-auth-123")
	click(t, tt, "Save profile")

	if len(store.networks) != 1 {
		t.Fatalf("stored profiles = %+v, want one", store.networks)
	}
	created := store.networks[0]
	if created.Label != "office tailnet" || created.Kind != domainvault.NetworkKindTailscale {
		t.Fatalf("created profile = %+v", created)
	}
	if created.Tailscale == nil || created.Tailscale.Hostname != "phone-node" ||
		!created.Tailscale.HasAuthKey {
		t.Fatalf("created Tailscale config = %+v", created.Tailscale)
	}
	if len(store.attempts) != 1 || store.attempts[0].Tailscale.AuthKey != "tskey-auth-123" {
		t.Fatalf("the service did not receive the Auth Key draft: %+v", store.attempts)
	}
	if !tt.HasText("office tailnet") || !tt.HasText("Tailscale Net · phone-node") {
		t.Fatalf("created profile is not listed; texts: %v", tt.Texts())
	}

	click(t, tt, "Edit profile office tailnet")
	typeInto(t, tt, "Label", " v2")
	click(t, tt, "Save profile")
	if len(store.networks) != 1 || store.networks[0].Label != "office tailnet v2" {
		t.Fatalf("edited profiles = %+v, want the renamed profile", store.networks)
	}
	if len(store.updated) != 1 {
		t.Fatalf("update calls = %d, want one", len(store.updated))
	}
	// An empty credential field keeps the stored Auth Key; the draft does not
	// resend it.
	if got := store.updated[0].Draft; got.Tailscale == nil || got.Tailscale.AuthKey != "" ||
		got.Tailscale.Hostname != "phone-node" {
		t.Fatalf("update draft = %+v, want the unchanged hostname and no resent key", got)
	}

	click(t, tt, "Delete profile office tailnet v2")
	click(t, tt, "Delete")
	if len(store.networks) != 0 {
		t.Fatalf("profiles after delete = %+v, want none", store.networks)
	}
	if len(store.deletedIDs) != 1 || store.deletedIDs[0] != created.ID {
		t.Fatalf("deleted IDs = %v, want %q", store.deletedIDs, created.ID)
	}
}

// fakeNetworkProvider drives the Tailscale lifecycle for the page tests: it
// records each action and publishes the status a real Provider would report.
type fakeNetworkProvider struct {
	mu        sync.Mutex
	started   int
	cancelled int
	retried   int
	statuses  map[string]vaultstate.NetworkProviderStatus
	// events signals that an action returned. Tests wait on it instead of
	// assuming when the off-thread action finished.
	events chan struct{}
}

func newFakeNetworkProvider() *fakeNetworkProvider {
	return &fakeNetworkProvider{
		statuses: make(map[string]vaultstate.NetworkProviderStatus),
		events:   make(chan struct{}, 16),
	}
}

func (f *fakeNetworkProvider) StartNetworkProvider(_ context.Context, profileID string) error {
	f.mu.Lock()
	f.started++
	f.statuses[profileID] = vaultstate.NetworkProviderStatus{
		ProfileID: profileID,
		Phase:     appconnection.ProviderPhaseNeedsLogin,
		AuthURL:   "https://login.example/abc",
	}
	f.mu.Unlock()
	f.events <- struct{}{}
	return nil
}

func (f *fakeNetworkProvider) CancelNetworkProvider(_ context.Context, profileID string) error {
	f.mu.Lock()
	f.cancelled++
	f.statuses[profileID] = vaultstate.NetworkProviderStatus{
		ProfileID: profileID,
		Phase:     appconnection.ProviderPhaseStopped,
	}
	f.mu.Unlock()
	f.events <- struct{}{}
	return nil
}

func (f *fakeNetworkProvider) RetryNetworkProvider(_ context.Context, profileID string) error {
	f.mu.Lock()
	f.retried++
	f.statuses[profileID] = vaultstate.NetworkProviderStatus{
		ProfileID: profileID,
		Phase:     appconnection.ProviderPhaseNeedsLogin,
		AuthURL:   "https://login.example/retry",
	}
	f.mu.Unlock()
	f.events <- struct{}{}
	return nil
}

func (f *fakeNetworkProvider) NetworkStatus(profileID string) (vaultstate.NetworkProviderStatus, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	status, ok := f.statuses[profileID]
	return status, ok
}

func (f *fakeNetworkProvider) setStatus(profileID string, status vaultstate.NetworkProviderStatus) {
	f.mu.Lock()
	f.statuses[profileID] = status
	f.mu.Unlock()
}

func (f *fakeNetworkProvider) waitForAction(t *testing.T) {
	t.Helper()
	select {
	case <-f.events:
	case <-time.After(3 * time.Second):
		t.Fatal("the provider action did not complete")
	}
}

func (f *fakeNetworkProvider) counts() (started, cancelled, retried int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started, f.cancelled, f.retried
}

func TestNetworksTailscaleShowsStatusAndControlsLogin(t *testing.T) {
	store := &fakeNetworkVault{fakeVault: &fakeVault{}, networks: []domainvault.NetworkProfile{{
		ID: "net-1", Label: "office", Kind: domainvault.NetworkKindTailscale,
		Tailscale: &domainvault.TailscaleNetwork{Hostname: "phone-node"},
	}}}
	provider := newFakeNetworkProvider()
	var opened, copied []string
	_, tt := openNetworks(t, store,
		WithNetworkProviderControl(provider, provider),
		WithNetworkLoginActions(
			func(url string) error { opened = append(opened, url); return nil },
			func(url string) { copied = append(copied, url) },
		),
	)

	if !tt.HasText("Sign in") {
		t.Fatalf("a profile with no status does not offer Sign in; texts: %v", tt.Texts())
	}
	click(t, tt, "Sign in")
	provider.waitForAction(t)
	tt.Frame()

	if started, _, _ := provider.counts(); started != 1 {
		t.Fatalf("started = %d, want one", started)
	}
	if !tt.HasText("Needs login") ||
		!tt.HasText("Sign in at https://login.example/abc") {
		t.Fatalf("the login status and URL are not shown; texts: %v", tt.Texts())
	}

	click(t, tt, "Copy login URL")
	if len(copied) != 1 || copied[0] != "https://login.example/abc" {
		t.Fatalf("copied URLs = %v, want the login URL", copied)
	}
	click(t, tt, "Open login URL")
	if len(opened) != 1 || opened[0] != "https://login.example/abc" {
		t.Fatalf("opened URLs = %v, want the login URL", opened)
	}

	click(t, tt, "Cancel")
	provider.waitForAction(t)
	tt.Frame()
	if _, cancelled, _ := provider.counts(); cancelled != 1 {
		t.Fatalf("cancelled = %d, want one", cancelled)
	}
	if !tt.HasText("Stopped") || !tt.HasText("Retry") {
		t.Fatalf("a cancelled login does not offer Retry; texts: %v", tt.Texts())
	}

	click(t, tt, "Retry")
	provider.waitForAction(t)
	tt.Frame()
	if _, _, retried := provider.counts(); retried != 1 {
		t.Fatalf("retried = %d, want one", retried)
	}

	// A failed provider is retryable too.
	provider.setStatus("net-1", vaultstate.NetworkProviderStatus{
		ProfileID: "net-1",
		Phase:     appconnection.ProviderPhaseError,
		LastError: "tailscale: authorization expired",
	})
	tt.Frame()
	if !tt.HasText("Error") || !tt.HasText("tailscale: authorization expired") {
		t.Fatalf("the provider failure is not shown; texts: %v", tt.Texts())
	}
	click(t, tt, "Retry")
	provider.waitForAction(t)
	tt.Frame()
	if _, _, retried := provider.counts(); retried != 2 {
		t.Fatalf("retried after failure = %d, want two", retried)
	}
}

// fakeConfPicker is the injected system file picker. An empty name is the
// system's cancellation.
type fakeConfPicker struct {
	name    string
	content []byte
	err     error
	calls   int
}

func (f *fakeConfPicker) PickWireGuardConf() (string, []byte, error) {
	f.calls++
	return f.name, f.content, f.err
}

func wireGuardKey(fill byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func sampleWireGuardConf() []byte {
	return []byte(fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = 10.9.0.2/32
DNS = 10.9.0.1
MTU = 1380
ListenPort = 51820

[Peer]
PublicKey = %s
Endpoint = vpn.example.com:51820
AllowedIPs = 10.9.0.0/24
PersistentKeepalive = 25
`, wireGuardKey(1), wireGuardKey(2)))
}

func TestNetworksWireGuardImportsConfThroughPicker(t *testing.T) {
	store := &fakeNetworkVault{fakeVault: &fakeVault{}}
	picker := &fakeConfPicker{name: "/tmp/wg0.conf", content: sampleWireGuardConf()}
	shell, tt := openNetworks(t, store, WithNetworkConfPicker(picker))

	click(t, tt, "Add WireGuard profile")
	click(t, tt, "Import .conf")

	if picker.calls != 1 {
		t.Fatalf("picker calls = %d, want one", picker.calls)
	}
	draft := shell.vault.networks.draft
	if draft == nil {
		t.Fatal("the editor closed after the import")
	}
	if draft.privateKey != wireGuardKey(1) {
		t.Fatalf("imported PrivateKey = %q, want the encoded key", draft.privateKey)
	}
	if draft.addresses != "10.9.0.2/32" || draft.dns != "10.9.0.1" ||
		draft.mtu != "1380" || draft.listenPort != "51820" {
		t.Fatalf("imported interface = %+v", draft)
	}
	if len(draft.peers) != 1 || draft.peers[0].publicKey != wireGuardKey(2) ||
		draft.peers[0].endpoint != "vpn.example.com:51820" ||
		draft.peers[0].allowedIPs != "10.9.0.0/24" ||
		draft.peers[0].keepalive != "25" {
		t.Fatalf("imported peers = %+v", draft.peers)
	}
	if !tt.HasText("Imported wg0.conf. Review the profile and save it.") {
		t.Fatalf("the import notice is missing; texts: %v", tt.Texts())
	}

	typeInto(t, tt, "Label", "imported wg")
	click(t, tt, "Save profile")
	if len(store.networks) != 1 {
		t.Fatalf("stored profiles = %+v, want one", store.networks)
	}
	saved := store.networks[0]
	if saved.Label != "imported wg" || saved.Kind != domainvault.NetworkKindWireGuard {
		t.Fatalf("saved profile = %+v", saved)
	}
	if saved.WireGuard == nil || len(saved.WireGuard.Addresses) != 1 ||
		saved.WireGuard.Addresses[0] != "10.9.0.2/32" ||
		len(saved.WireGuard.Peers) != 1 ||
		saved.WireGuard.Peers[0].PublicKey != wireGuardKey(2) {
		t.Fatalf("saved WireGuard config = %+v", saved.WireGuard)
	}
	if len(store.attempts) != 1 ||
		store.attempts[0].WireGuard.PrivateKey != wireGuardKey(1) {
		t.Fatalf("the service did not receive the imported Private Key: %+v", store.attempts)
	}
}

func TestNetworksWireGuardCancelledImportLeavesEditorUnchanged(t *testing.T) {
	store := &fakeNetworkVault{fakeVault: &fakeVault{}}
	picker := &fakeConfPicker{} // no name: the picker was cancelled
	shell, tt := openNetworks(t, store, WithNetworkConfPicker(picker))

	click(t, tt, "Add WireGuard profile")
	typeInto(t, tt, "Label", "manual wg")
	typeInto(t, tt, "Addresses", "10.5.0.2/32")
	before := *shell.vault.networks.draft

	click(t, tt, "Import .conf")

	if picker.calls != 1 {
		t.Fatalf("picker calls = %d, want one", picker.calls)
	}
	after := shell.vault.networks.draft
	if after == nil {
		t.Fatal("a cancelled picker closed the editor")
	}
	if after.label != before.label || after.addresses != before.addresses ||
		after.privateKey != "" || after.dns != "" || after.mtu != "" ||
		after.listenPort != "" || len(after.peers) != len(before.peers) {
		t.Fatalf("a cancelled picker changed the editor: before %+v, after %+v", before, after)
	}
	if shell.vault.networks.draftProblem != "" || shell.vault.networks.draftNotice != "" {
		t.Fatalf("a cancelled picker reported a problem: %q / %q",
			shell.vault.networks.draftProblem, shell.vault.networks.draftNotice)
	}
}

// TestNetworksKeepsSharedProxyCommandConflict proves the page neither weakens
// nor duplicates the shared rule: a route selecting both a ProxyCommand and a
// Network Profile returns the shared typed conflict sentinel.
func TestNetworksKeepsSharedProxyCommandConflict(t *testing.T) {
	route := appconnection.NetworkRoute{
		ProfileID: "net-1",
		Kind:      string(domainvault.NetworkKindTailscale),
		Label:     "office",
	}
	err := appconnection.ProxyCommandNetworkConflict(route)
	if !errors.Is(err, appconnection.ErrProxyCommandNetworkConflict) {
		t.Fatalf("error = %v, want the shared conflict sentinel", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not the shared typed error", err)
	}
	if typed.Provider == nil || typed.Provider.ProfileID != "net-1" ||
		typed.Provider.Label != "office" {
		t.Fatalf("ProviderInfo = %+v, want the selected profile", typed.Provider)
	}
}
