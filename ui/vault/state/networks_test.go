package state

import (
	"context"
	"testing"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type fakeNetworkProfileService struct {
	items   []domainvault.NetworkProfile
	created []domainvault.NetworkProfileDraft
	updated []domainvault.NetworkProfileDraft
	deleted []string
	err     error
	tempID  string
}

func (f *fakeNetworkProfileService) ListNetworkProfiles(context.Context) ([]domainvault.NetworkProfile, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]domainvault.NetworkProfile(nil), f.items...), nil
}

func (f *fakeNetworkProfileService) CreateNetworkProfile(_ context.Context,
	draft domainvault.NetworkProfileDraft) (domainvault.NetworkProfile, error) {
	if f.err != nil {
		return domainvault.NetworkProfile{}, f.err
	}
	f.created = append(f.created, draft)
	id := f.tempID
	if id == "" {
		id = "network-1"
	}
	return domainvault.NetworkProfile{ID: id, Label: draft.Label, Kind: draft.Kind}, nil
}

func (f *fakeNetworkProfileService) UpdateNetworkProfile(_ context.Context, id string,
	draft domainvault.NetworkProfileDraft) (domainvault.NetworkProfile, error) {
	if f.err != nil {
		return domainvault.NetworkProfile{}, f.err
	}
	f.updated = append(f.updated, draft)
	return domainvault.NetworkProfile{ID: id, Label: draft.Label, Kind: draft.Kind}, nil
}

func (f *fakeNetworkProfileService) DeleteNetworkProfile(_ context.Context, id string) error {
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func newNetworkState(profiles *fakeNetworkProfileService) *State {
	return NewState(&fakeHosts{}, WithNetworkProfiles(profiles))
}

func TestNetworkProfileEditorCreatesBothKindsAndRoundTripsTheForm(t *testing.T) {
	service := &fakeNetworkProfileService{}
	state := newNetworkState(service)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	state.BeginCreateNetworkProfile(domainvault.NetworkKindTailscale)
	form := state.NetworkProfileForm()
	if !state.EditingNetworkProfile() || form.Kind != string(domainvault.NetworkKindTailscale) {
		t.Fatalf("create editor = %+v, editing = %v", form, state.EditingNetworkProfile())
	}
	form.Label = "tailnet"
	form.Hostname = "app-node"
	form.AuthKey = "tskey-auth-secret"
	authKey := form.AuthKey
	form.ControlURL = "https://headscale.example.com"
	form.Ephemeral = true
	form.Tags = "tag:prod, tag:ops"
	if err := state.SaveNetworkProfile(); err != nil {
		t.Fatalf("SaveNetworkProfile: %v", err)
	}
	if len(service.created) != 1 {
		t.Fatalf("created = %+v", service.created)
	}
	created := service.created[0]
	if created.Kind != domainvault.NetworkKindTailscale || created.Label != "tailnet" ||
		created.Tailscale == nil || created.Tailscale.AuthKey != authKey ||
		!created.Tailscale.Ephemeral {
		t.Fatalf("created draft = %+v", created)
	}
	if len(created.Tailscale.Tags) != 2 || created.Tailscale.Tags[1] != "tag:ops" {
		t.Fatalf("created tags = %v", created.Tailscale.Tags)
	}
	if state.EditingNetworkProfile() {
		t.Fatal("the editor stayed open after a successful save")
	}

	service.items = []domainvault.NetworkProfile{{
		ID: "network-1", Label: "tailnet", Kind: domainvault.NetworkKindTailscale,
		Tailscale: &domainvault.TailscaleNetwork{
			Hostname: "app-node", ControlURL: "https://headscale.example.com",
			Ephemeral: true, Tags: []string{"tag:prod"}, HasAuthKey: true,
		},
	}}
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEditNetworkProfile("network-1") {
		t.Fatalf("BeginEditNetworkProfile failed: %v", state.Problem())
	}
	edit := state.NetworkProfileForm()
	if edit.Label != "tailnet" || edit.Hostname != "app-node" ||
		edit.ControlURL != "https://headscale.example.com" ||
		!edit.HasAuthKey || edit.AuthKey != "" || !edit.Ephemeral ||
		edit.Tags != "tag:prod" {
		t.Fatalf("edit form = %+v", edit)
	}
	if err := state.SaveNetworkProfile(); err != nil {
		t.Fatalf("SaveNetworkProfile: %v", err)
	}
	if len(service.updated) != 1 || service.updated[0].Tailscale.AuthKey != "" {
		t.Fatalf("updated draft = %+v", service.updated)
	}
}

func TestNetworkProfileEditorParsesWireGuardListsAndNumbers(t *testing.T) {
	service := &fakeNetworkProfileService{}
	state := newNetworkState(service)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	state.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard)
	form := state.NetworkProfileForm()
	if len(form.Peers) != 1 {
		t.Fatalf("a new WireGuard editor opens %d peer rows, want 1", len(form.Peers))
	}
	form.Label = "wg"
	form.PrivateKey = "private"
	form.Addresses = "10.0.0.2/32, fd00::2"
	form.DNS = "10.0.0.1"
	form.MTU = "1280"
	form.Peers[0].PublicKey = "public"
	form.Peers[0].PresharedKey = "preshared"
	form.Peers[0].Endpoint = "vpn.example.com:51820"
	form.Peers[0].AllowedIPs = "10.0.0.0/24, 10.1.0.0/24"
	form.Peers[0].PersistentKeepalive = "25"
	state.AddNetworkPeer()
	form = state.NetworkProfileForm()
	if len(form.Peers) != 2 {
		t.Fatalf("AddNetworkPeer produced %d rows", len(form.Peers))
	}
	state.RemoveNetworkPeer(1)
	if len(state.NetworkProfileForm().Peers) != 1 {
		t.Fatal("RemoveNetworkPeer did not drop the row")
	}

	if err := state.SaveNetworkProfile(); err != nil {
		t.Fatalf("SaveNetworkProfile: %v", err)
	}
	if len(service.created) != 1 {
		t.Fatalf("created = %+v", service.created)
	}
	draft := service.created[0]
	if draft.Kind != domainvault.NetworkKindWireGuard || draft.WireGuard == nil {
		t.Fatalf("draft = %+v", draft)
	}
	if draft.WireGuard.MTU != 1280 || draft.WireGuard.PrivateKey != "private" {
		t.Fatalf("WireGuard draft = %+v", draft.WireGuard)
	}
	if len(draft.WireGuard.Addresses) != 2 || draft.WireGuard.Addresses[1] != "fd00::2" {
		t.Fatalf("addresses = %v", draft.WireGuard.Addresses)
	}
	if len(draft.WireGuard.Peers) != 1 || len(draft.WireGuard.Peers[0].AllowedIPs) != 2 ||
		draft.WireGuard.Peers[0].PersistentKeepalive != 25 ||
		draft.WireGuard.Peers[0].PresharedKey != "preshared" {
		t.Fatalf("peers = %+v", draft.WireGuard.Peers)
	}

	// A malformed number is rejected before the service sees a draft.
	state.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard)
	state.NetworkProfileForm().Label = "wg"
	state.NetworkProfileForm().Addresses = "10.0.0.2/32"
	state.NetworkProfileForm().MTU = "not-a-number"
	state.NetworkProfileForm().Peers[0].PublicKey = "public"
	if err := state.SaveNetworkProfile(); err != nil {
		t.Fatalf("SaveNetworkProfile: %v", err)
	}
	if state.Problem() != ProblemNetworkMTU || len(service.created) != 1 {
		t.Fatalf("malformed MTU problem = %q, created = %d", state.Problem(), len(service.created))
	}
	state.NetworkProfileForm().MTU = "1280"
	state.NetworkProfileForm().Peers[0].PersistentKeepalive = "-1"
	if err := state.SaveNetworkProfile(); err != nil {
		t.Fatalf("SaveNetworkProfile: %v", err)
	}
	if state.Problem() != ProblemNetworkKeepalive || len(service.created) != 1 {
		t.Fatalf("malformed keepalive problem = %q", state.Problem())
	}
}

func TestNetworkProfileWarningsFollowTheControlURL(t *testing.T) {
	state := newNetworkState(&fakeNetworkProfileService{})
	state.BeginCreateNetworkProfile(domainvault.NetworkKindTailscale)
	state.NetworkProfileForm().ControlURL = "http://127.0.0.1:8080"
	warnings := state.NetworkProfileWarnings()
	if len(warnings) != 1 || warnings[0] != domainvault.WarningControlURLInsecure {
		t.Fatalf("warnings = %v", warnings)
	}
	state.NetworkProfileForm().ControlURL = "https://headscale.example.com"
	if warnings := state.NetworkProfileWarnings(); len(warnings) != 0 {
		t.Fatalf("https warnings = %v", warnings)
	}
}

func TestNetworkProfileDeleteConfirmationAndServiceErrors(t *testing.T) {
	service := &fakeNetworkProfileService{items: []domainvault.NetworkProfile{{
		ID: "network-1", Label: "tailnet", Kind: domainvault.NetworkKindTailscale,
	}}}
	state := newNetworkState(service)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if state.NetworkProfileCount() != 1 {
		t.Fatalf("NetworkProfileCount = %d", state.NetworkProfileCount())
	}
	if profile, ok := state.NetworkProfileByID("network-1"); !ok || profile.Label != "tailnet" {
		t.Fatalf("NetworkProfileByID = %+v, %v", profile, ok)
	}

	state.RequestDeleteNetworkProfile("network-1")
	if state.PendingDeleteNetworkProfile() != "network-1" {
		t.Fatalf("pending delete = %q", state.PendingDeleteNetworkProfile())
	}
	if err := state.ConfirmDeleteNetworkProfile(); err != nil {
		t.Fatalf("ConfirmDeleteNetworkProfile: %v", err)
	}
	if len(service.deleted) != 1 || service.deleted[0] != "network-1" {
		t.Fatalf("deleted = %v", service.deleted)
	}

	service.err = &appvault.Error{Kind: appvault.KindValidation}
	state.BeginCreateNetworkProfile(domainvault.NetworkKindTailscale)
	state.NetworkProfileForm().Label = "x"
	if err := state.SaveNetworkProfile(); err != nil {
		t.Fatalf("SaveNetworkProfile: %v", err)
	}
	if state.Problem() != ProblemNetworkProfile {
		t.Fatalf("validation problem = %q, want %q", state.Problem(), ProblemNetworkProfile)
	}
}
