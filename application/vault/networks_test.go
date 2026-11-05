package vault

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type fakeNetworkProfiles struct {
	items   []domainvault.NetworkProfile
	deleted []string
	err     error
}

func (f *fakeNetworkProfiles) ListNetworkProfiles(context.Context) ([]domainvault.NetworkProfile, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]domainvault.NetworkProfile(nil), f.items...), nil
}

func (f *fakeNetworkProfiles) GetNetworkProfile(_ context.Context, id string) (domainvault.NetworkProfile, error) {
	if f.err != nil {
		return domainvault.NetworkProfile{}, f.err
	}
	for _, profile := range f.items {
		if profile.ID == id {
			return profile, nil
		}
	}
	return domainvault.NetworkProfile{}, ErrNotFound
}

func (f *fakeNetworkProfiles) SaveNetworkProfile(_ context.Context, profile domainvault.NetworkProfile) error {
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

func (f *fakeNetworkProfiles) DeleteNetworkProfile(_ context.Context, id string) error {
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

func testWireGuardKey(b byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

func newNetworkTestService(repo NetworkProfileRepository, secrets FieldSecrets) *Service {
	var profiles, peers int
	return NewService(&fakeHosts{},
		WithSecrets(secrets),
		WithNetworkProfiles(repo),
		WithNetworkProfileIDGenerator(func() string {
			profiles++
			return "network-" + strconv.Itoa(profiles)
		}),
		WithNetworkPeerIDGenerator(func() string {
			peers++
			return "peer-" + strconv.Itoa(peers)
		}),
		WithClock(func() time.Time { return time.Unix(1700000000, 0).UTC() }),
	)
}

func tailscaleNetworkDraft(authKey string) domainvault.NetworkProfileDraft {
	return domainvault.NetworkProfileDraft{
		Label: "tailnet",
		Kind:  domainvault.NetworkKindTailscale,
		Tailscale: &domainvault.TailscaleNetworkDraft{
			Hostname:   "app-node",
			AuthKey:    authKey,
			ControlURL: "https://headscale.example.com",
			Tags:       []string{"tag:prod"},
		},
	}
}

func wireGuardNetworkDraft(privateKey, peerKey, presharedKey string) domainvault.NetworkProfileDraft {
	return domainvault.NetworkProfileDraft{
		Label: "wg",
		Kind:  domainvault.NetworkKindWireGuard,
		WireGuard: &domainvault.WireGuardNetworkDraft{
			PrivateKey: privateKey,
			Addresses:  []string{"10.0.0.2/32"},
			DNS:        []string{"10.0.0.1"},
			Peers: []domainvault.WireGuardPeerDraft{{
				PublicKey:           peerKey,
				PresharedKey:        presharedKey,
				Endpoint:            "vpn.example.com:51820",
				AllowedIPs:          []string{"10.0.0.0/24"},
				PersistentKeepalive: 25,
			}},
		},
	}
}

func TestCreateNetworkProfileKeepsTheAuthKeyOutOfTheRecord(t *testing.T) {
	secrets := newFakeSecrets()
	service := newNetworkTestService(&fakeNetworkProfiles{}, secrets)

	profile, err := service.CreateNetworkProfile(context.Background(), tailscaleNetworkDraft("tskey-auth-secret"))
	if err != nil {
		t.Fatalf("CreateNetworkProfile: %v", err)
	}
	if profile.Kind != domainvault.NetworkKindTailscale || profile.Label != "tailnet" {
		t.Fatalf("created profile = %+v", profile)
	}
	if profile.Tailscale == nil || !profile.Tailscale.HasAuthKey {
		t.Fatalf("HasAuthKey = %+v, want true", profile.Tailscale)
	}
	ref := NetworkProfileSecretRef(profile.ID, FieldNetworkAuthKey)
	if got := secrets.values[ref]; got != "tskey-auth-secret" {
		t.Fatalf("stored auth key = %q", got)
	}
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "tskey-auth-secret") {
		t.Fatalf("profile JSON contains the auth key: %s", data)
	}
	if profile.OrderKey != orderStep || profile.CreatedAt.IsZero() {
		t.Fatalf("profile metadata = %+v", profile)
	}
}

func TestCreateWireGuardProfileStoresEverySecretUnderItsOwnRef(t *testing.T) {
	secrets := newFakeSecrets()
	service := newNetworkTestService(&fakeNetworkProfiles{}, secrets)
	privateKey := testWireGuardKey(1)
	peerKey := testWireGuardKey(2)
	presharedKey := testWireGuardKey(3)

	profile, err := service.CreateNetworkProfile(context.Background(),
		wireGuardNetworkDraft(privateKey, peerKey, presharedKey))
	if err != nil {
		t.Fatalf("CreateNetworkProfile: %v", err)
	}
	if profile.WireGuard == nil || !profile.WireGuard.HasPrivateKey {
		t.Fatalf("HasPrivateKey = %+v, want true", profile.WireGuard)
	}
	if len(profile.WireGuard.Peers) != 1 || !profile.WireGuard.Peers[0].HasPresharedKey {
		t.Fatalf("peers = %+v, want one peer with a saved Preshared Key", profile.WireGuard.Peers)
	}
	peerID := profile.WireGuard.Peers[0].ID
	if peerID == "" {
		t.Fatal("the service did not assign a stable peer ID")
	}
	if got := secrets.values[NetworkProfileSecretRef(profile.ID, FieldNetworkPrivateKey)]; got != privateKey {
		t.Fatalf("stored private key = %q", got)
	}
	if got := secrets.values[NetworkPeerSecretRef(peerID, FieldNetworkPresharedKey)]; got != presharedKey {
		t.Fatalf("stored preshared key = %q", got)
	}
	if len(secrets.values) != 2 {
		t.Fatalf("secret store holds %d values, want 2", len(secrets.values))
	}

	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{privateKey, presharedKey} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("profile JSON contains a key: %s", data)
		}
	}
	if got, err := service.NetworkProfileSecret(context.Background(), profile.ID, FieldNetworkPrivateKey); err != nil || got != privateKey {
		t.Fatalf("NetworkProfileSecret = %q, %v", got, err)
	}
	if got, err := service.NetworkPeerPresharedKey(context.Background(), profile.ID, peerID); err != nil || got != presharedKey {
		t.Fatalf("NetworkPeerPresharedKey = %q, %v", got, err)
	}
}

func TestUpdateNetworkProfileKeepsStoredSecretsAndClearsRemovedPeers(t *testing.T) {
	secrets := newFakeSecrets()
	service := newNetworkTestService(&fakeNetworkProfiles{}, secrets)
	privateKey := testWireGuardKey(1)
	peerA := testWireGuardKey(2)
	peerB := testWireGuardKey(3)
	presharedA := testWireGuardKey(4)
	presharedB := testWireGuardKey(5)

	created, err := service.CreateNetworkProfile(context.Background(),
		wireGuardNetworkDraft(privateKey, peerA, presharedA))
	if err != nil {
		t.Fatalf("CreateNetworkProfile: %v", err)
	}
	peerAID := created.WireGuard.Peers[0].ID

	// An empty PrivateKey keeps the stored key, and the new peer appears with
	// its own stable ID and Preshared Key.
	update := wireGuardNetworkDraft("", peerA, "")
	update.Label = "wg v2"
	update.WireGuard.Peers = []domainvault.WireGuardPeerDraft{
		{
			ID: peerAID, PublicKey: peerA, AllowedIPs: []string{"10.0.0.0/24"},
			Endpoint: "vpn.example.com:51820", PersistentKeepalive: 25,
		},
		{
			PublicKey: peerB, PresharedKey: presharedB, AllowedIPs: []string{"10.1.0.0/24"},
			Endpoint: "other.example.com:51820",
		},
	}
	updated, err := service.UpdateNetworkProfile(context.Background(), created.ID, update)
	if err != nil {
		t.Fatalf("UpdateNetworkProfile: %v", err)
	}
	if updated.Label != "wg v2" || !updated.WireGuard.HasPrivateKey {
		t.Fatalf("updated profile = %+v", updated)
	}
	if got := secrets.values[NetworkProfileSecretRef(created.ID, FieldNetworkPrivateKey)]; got != privateKey {
		t.Fatalf("private key after an empty update = %q", got)
	}
	if got := secrets.values[NetworkPeerSecretRef(peerAID, FieldNetworkPresharedKey)]; got != presharedA {
		t.Fatalf("peer A preshared key after an empty update = %q", got)
	}
	var peerBID string
	for _, peer := range updated.WireGuard.Peers {
		if peer.PublicKey == peerB {
			peerBID = peer.ID
		}
	}
	if peerBID == "" || peerBID == peerAID {
		t.Fatalf("peer B id = %q, want a new stable id", peerBID)
	}
	if got := secrets.values[NetworkPeerSecretRef(peerBID, FieldNetworkPresharedKey)]; got != presharedB {
		t.Fatalf("peer B preshared key = %q", got)
	}

	// Removing peer B deletes only its Preshared Key.
	update = wireGuardNetworkDraft("", peerA, "")
	update.WireGuard.Peers = []domainvault.WireGuardPeerDraft{{
		ID: peerAID, PublicKey: peerA, AllowedIPs: []string{"10.0.0.0/24"},
		Endpoint: "vpn.example.com:51820", PersistentKeepalive: 25,
	}}
	if _, err := service.UpdateNetworkProfile(context.Background(), created.ID, update); err != nil {
		t.Fatalf("UpdateNetworkProfile removing peer B: %v", err)
	}
	if _, ok := secrets.values[NetworkPeerSecretRef(peerBID, FieldNetworkPresharedKey)]; ok {
		t.Fatal("the removed peer's Preshared Key was not deleted")
	}
	if _, ok := secrets.values[NetworkPeerSecretRef(peerAID, FieldNetworkPresharedKey)]; !ok {
		t.Fatal("the kept peer's Preshared Key was deleted")
	}
	if _, ok := secrets.values[NetworkProfileSecretRef(created.ID, FieldNetworkPrivateKey)]; !ok {
		t.Fatal("the Private Key was deleted by an unrelated update")
	}
}

func TestUpdateNetworkProfileReplacesTheAuthKeyWhenSupplied(t *testing.T) {
	secrets := newFakeSecrets()
	service := newNetworkTestService(&fakeNetworkProfiles{}, secrets)
	created, err := service.CreateNetworkProfile(context.Background(), tailscaleNetworkDraft("first-key"))
	if err != nil {
		t.Fatalf("CreateNetworkProfile: %v", err)
	}
	update := tailscaleNetworkDraft("")
	update.Tailscale.Hostname = "renamed"
	updated, err := service.UpdateNetworkProfile(context.Background(), created.ID, update)
	if err != nil {
		t.Fatalf("UpdateNetworkProfile: %v", err)
	}
	if got := secrets.values[NetworkProfileSecretRef(created.ID, FieldNetworkAuthKey)]; got != "first-key" {
		t.Fatalf("auth key after an empty update = %q", got)
	}
	if !updated.Tailscale.HasAuthKey || updated.Tailscale.Hostname != "renamed" {
		t.Fatalf("updated Tailscale config = %+v", updated.Tailscale)
	}

	update = tailscaleNetworkDraft("second-key")
	if _, err := service.UpdateNetworkProfile(context.Background(), created.ID, update); err != nil {
		t.Fatalf("UpdateNetworkProfile replacing the key: %v", err)
	}
	if got := secrets.values[NetworkProfileSecretRef(created.ID, FieldNetworkAuthKey)]; got != "second-key" {
		t.Fatalf("auth key after replacement = %q", got)
	}
}

func TestUpdateNetworkProfileRejectsAKindChange(t *testing.T) {
	cases := []struct {
		name   string
		create domainvault.NetworkProfileDraft
		update domainvault.NetworkProfileDraft
		want   domainvault.NetworkProfileKind
	}{
		{
			name:   "tailscale to wireguard",
			create: tailscaleNetworkDraft("tskey-auth-secret"),
			update: wireGuardNetworkDraft(testWireGuardKey(1), testWireGuardKey(2), ""),
			want:   domainvault.NetworkKindTailscale,
		},
		{
			name:   "wireguard to tailscale",
			create: wireGuardNetworkDraft(testWireGuardKey(1), testWireGuardKey(2), ""),
			update: tailscaleNetworkDraft("tskey-auth-secret"),
			want:   domainvault.NetworkKindWireGuard,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			repo := &fakeNetworkProfiles{}
			service := newNetworkTestService(repo, newFakeSecrets())
			created, err := service.CreateNetworkProfile(context.Background(), test.create)
			if err != nil {
				t.Fatalf("CreateNetworkProfile: %v", err)
			}

			_, err = service.UpdateNetworkProfile(context.Background(), created.ID, test.update)
			var typed *Error
			if !errors.As(err, &typed) || typed.Kind != KindValidation {
				t.Fatalf("error = %v, want KindValidation", err)
			}
			var invalid *domainvault.ValidationError
			if !errors.As(err, &invalid) ||
				invalid.Field != domainvault.FieldNetworkProfile || invalid.Rule != domainvault.RuleInvalid {
				t.Fatalf("error = %v, want a network_profile/invalid ValidationError", err)
			}

			stored, err := repo.GetNetworkProfile(context.Background(), created.ID)
			if err != nil {
				t.Fatalf("GetNetworkProfile: %v", err)
			}
			if stored.Kind != test.want {
				t.Fatalf("stored Kind = %q, want the original %q", stored.Kind, test.want)
			}
			if stored.Label != created.Label || !stored.UpdatedAt.Equal(created.UpdatedAt) {
				t.Fatalf("stored profile changed on a rejected update: %+v", stored)
			}
		})
	}
}

func TestDeleteNetworkProfileRemovesTheRecordAndEverySecret(t *testing.T) {
	secrets := newFakeSecrets()
	repo := &fakeNetworkProfiles{}
	service := newNetworkTestService(repo, secrets)
	profile, err := service.CreateNetworkProfile(context.Background(),
		wireGuardNetworkDraft(testWireGuardKey(1), testWireGuardKey(2), testWireGuardKey(3)))
	if err != nil {
		t.Fatalf("CreateNetworkProfile: %v", err)
	}
	peerID := profile.WireGuard.Peers[0].ID

	if err := service.DeleteNetworkProfile(context.Background(), profile.ID); err != nil {
		t.Fatalf("DeleteNetworkProfile: %v", err)
	}
	if len(repo.deleted) != 1 || repo.deleted[0] != profile.ID {
		t.Fatalf("deleted = %v", repo.deleted)
	}
	for _, ref := range []SecretRef{
		NetworkProfileSecretRef(profile.ID, FieldNetworkPrivateKey),
		NetworkPeerSecretRef(peerID, FieldNetworkPresharedKey),
	} {
		if _, ok := secrets.values[ref]; ok {
			t.Fatalf("secret %+v survived the profile delete", ref)
		}
	}
	if err := service.DeleteNetworkProfile(context.Background(), "missing"); err != ErrNotFound {
		t.Fatalf("deleting a missing profile = %v, want ErrNotFound", err)
	}
}

func TestNetworkProfileValidationFailuresAreTyped(t *testing.T) {
	service := newNetworkTestService(&fakeNetworkProfiles{}, newFakeSecrets())
	_, err := service.CreateNetworkProfile(context.Background(), domainvault.NetworkProfileDraft{
		Label: "x",
		Kind:  domainvault.NetworkKindWireGuard,
		WireGuard: &domainvault.WireGuardNetworkDraft{
			Addresses: []string{"not-an-address"},
			Peers: []domainvault.WireGuardPeerDraft{{
				PublicKey: testWireGuardKey(2), AllowedIPs: []string{"10.0.0.0/24"},
			}},
		},
	})
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindValidation {
		t.Fatalf("error = %v, want a validation error", err)
	}
}

func TestNetworkProfilesUnavailableWithoutARepository(t *testing.T) {
	service := NewService(&fakeHosts{}, WithSecrets(newFakeSecrets()))
	if _, err := service.CreateNetworkProfile(context.Background(), tailscaleNetworkDraft("k")); err == nil {
		t.Fatal("CreateNetworkProfile succeeded without a repository")
	}
	if _, err := service.ListNetworkProfiles(context.Background()); err == nil {
		t.Fatal("ListNetworkProfiles succeeded without a repository")
	}
}

func TestSessionOnlyNetworkSecretsAreNotClaimedByTheRecord(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.mode = SecretModeSession
	service := newNetworkTestService(&fakeNetworkProfiles{}, secrets)
	profile, err := service.CreateNetworkProfile(context.Background(), tailscaleNetworkDraft("session-key"))
	if err != nil {
		t.Fatalf("CreateNetworkProfile: %v", err)
	}
	if profile.Tailscale.HasAuthKey {
		t.Fatal("a session-only Auth Key is advertised as persisted")
	}
	if got := secrets.values[NetworkProfileSecretRef(profile.ID, FieldNetworkAuthKey)]; got != "session-key" {
		t.Fatalf("session store value = %q", got)
	}
}
