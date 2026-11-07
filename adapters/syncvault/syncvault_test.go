package syncvault

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	historysync "github.com/mslxl/natcatty/application/historysync"
	appsync "github.com/mslxl/natcatty/application/sync"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type fakeRecords struct {
	hosts    []domainvault.Host
	groups   []domainvault.Group
	snippets []domainvault.Snippet
	keys     []domainvault.Key
	networks []domainvault.NetworkProfile
	applied  []appsync.Payload
	applyErr error
}

func (r *fakeRecords) List(context.Context) ([]domainvault.Host, error) { return r.hosts, nil }
func (r *fakeRecords) ListGroups(context.Context) ([]domainvault.Group, error) {
	return r.groups, nil
}
func (r *fakeRecords) ListKeys(context.Context) ([]domainvault.Key, error) { return r.keys, nil }
func (r *fakeRecords) ListIdentities(context.Context) ([]domainvault.Identity, error) {
	return nil, nil
}
func (r *fakeRecords) ListProxyProfiles(context.Context) ([]domainvault.ProxyProfile, error) {
	return nil, nil
}
func (r *fakeRecords) ListNetworkProfiles(context.Context) ([]domainvault.NetworkProfile, error) {
	return r.networks, nil
}
func (r *fakeRecords) ListForwardingRules(context.Context) ([]domainforward.Rule, error) {
	return nil, nil
}
func (r *fakeRecords) ListSnippets(context.Context) ([]domainvault.Snippet, error) {
	return r.snippets, nil
}
func (r *fakeRecords) ListSnippetPackages(context.Context) ([]domainvault.SnippetPackage, error) {
	return nil, nil
}
func (r *fakeRecords) ListNotes(context.Context) ([]domainvault.Note, error) { return nil, nil }
func (r *fakeRecords) ListNoteGroups(context.Context) ([]domainvault.NoteGroup, error) {
	return nil, nil
}
func (r *fakeRecords) ApplySync(_ context.Context, payload appsync.Payload) error {
	if r.applyErr != nil {
		return r.applyErr
	}
	r.applied = append(r.applied, payload)
	return nil
}

type fakeSecrets struct {
	mode    appvault.SecretMode
	values  map[appvault.SecretRef]string
	getErr  error
	deleted []appvault.SecretRef
	set     []appvault.SecretRef
}

func (s *fakeSecrets) Mode() appvault.SecretMode { return s.mode }
func (s *fakeSecrets) Get(_ context.Context, ref appvault.SecretRef) (string, error) {
	if s.getErr != nil {
		return "", s.getErr
	}
	value, ok := s.values[ref]
	if !ok {
		return "", appvault.ErrSecretNotFound
	}
	return value, nil
}
func (s *fakeSecrets) Set(_ context.Context, ref appvault.SecretRef, value string) error {
	s.set = append(s.set, ref)
	if s.values == nil {
		s.values = map[appvault.SecretRef]string{}
	}
	s.values[ref] = value
	return nil
}
func (s *fakeSecrets) Delete(_ context.Context, ref appvault.SecretRef) error {
	s.deleted = append(s.deleted, ref)
	delete(s.values, ref)
	return nil
}

func hostRef(id string) appvault.SecretRef {
	return appvault.HostSecretRef(model.HostID(id), appvault.FieldPassword)
}

func TestSnapshotCarriesPlaintextSecretsAndNewestTimestamp(t *testing.T) {
	now := time.UnixMilli(1000)
	records := &fakeRecords{
		hosts: []domainvault.Host{{
			ID: "host-1", Label: "h", Hostname: "h", Port: 22, Protocol: domainvault.ProtocolSSH,
			HasPassword: true, CreatedAt: now, UpdatedAt: time.UnixMilli(2000),
		}},
		keys: []domainvault.Key{{ID: "key-1", Label: "k", HasPrivateKey: true, UpdatedAt: time.UnixMilli(3000)}},
	}
	secrets := &fakeSecrets{mode: appvault.SecretModeOS, values: map[appvault.SecretRef]string{
		hostRef("host-1"): "host-password",
		appvault.KeySecretRef(model.KeyID("key-1"), appvault.FieldPrivateKey): "private",
	}}
	payload, err := New(records, secrets).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if payload.SyncedAt != 3000 {
		t.Fatalf("SyncedAt = %d", payload.SyncedAt)
	}
	if len(payload.Secrets) != 2 {
		t.Fatalf("secrets = %+v", payload.Secrets)
	}
	if payload.Secrets[0].Value != "host-password" || payload.Secrets[1].Value != "private" {
		t.Fatalf("secrets = %+v", payload.Secrets)
	}
}

func TestSnapshotNeverCarriesAtuinAccountCredentials(t *testing.T) {
	const token = "fake-session-token-must-not-sync"
	const key = "not-a-real-key-value-for-the-vault-snapshot"
	secrets := &fakeSecrets{mode: appvault.SecretModeOS, values: map[appvault.SecretRef]string{
		appvault.SecretRef(historysync.SessionTokenRef()): token,
		appvault.SecretRef(historysync.MasterKeyRef()):    key,
	}}
	payload, err := New(&fakeRecords{}, secrets).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(payload.Secrets) != 0 {
		t.Fatalf("Vault snapshot carried Atuin credentials: %+v", payload.Secrets)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	if strings.Contains(string(encoded), token) || strings.Contains(string(encoded), key) {
		t.Fatalf("Vault snapshot leaked an Atuin credential: %s", encoded)
	}
}

func TestSnapshotToleratesAStaleSecretFlagButNotAnUnreadableSecret(t *testing.T) {
	records := &fakeRecords{hosts: []domainvault.Host{{ID: "host-1", HasPassword: true}}}
	secrets := &fakeSecrets{mode: appvault.SecretModeOS, values: map[appvault.SecretRef]string{}}
	payload, err := New(records, secrets).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("a missing credential should be omitted, got %v", err)
	}
	if len(payload.Secrets) != 0 {
		t.Fatalf("secrets = %+v", payload.Secrets)
	}

	secrets.getErr = appvault.ErrSecretsLocked
	if _, err := New(records, secrets).Snapshot(context.Background()); err == nil {
		t.Fatal("an unreadable credential was silently dropped")
	}
}

func TestApplyRefusesLockedCredentialsBeforeReplacingRecords(t *testing.T) {
	records := &fakeRecords{}
	secrets := &fakeSecrets{mode: appvault.SecretModePassphraseLocked}
	payload := appsync.Payload{Hosts: []domainvault.Host{{ID: "host-1", Label: "h", Hostname: "h", Port: 22, Protocol: domainvault.ProtocolSSH}}}
	if err := New(records, secrets).Apply(context.Background(), payload); err == nil {
		t.Fatal("locked credentials were accepted")
	}
	if len(records.applied) != 0 {
		t.Fatal("records were replaced before the credential preflight")
	}
}

func TestApplyRejectsInboundAtuinAccountCredentialsBeforeWritingAnything(t *testing.T) {
	const token = "local-session-token-must-survive"
	const key = "local-master-key-must-survive"
	tokenRef := appvault.SecretRef(historysync.SessionTokenRef())
	keyRef := appvault.SecretRef(historysync.MasterKeyRef())
	records := &fakeRecords{}
	settings := &fakeSettings{}
	secrets := &fakeSecrets{mode: appvault.SecretModeOS, values: map[appvault.SecretRef]string{
		tokenRef: token, keyRef: key,
	}}
	payload := appsync.Payload{
		Hosts: []domainvault.Host{{
			ID: "host-1", Label: "h", Hostname: "h", Port: 22,
			Protocol: domainvault.ProtocolSSH, HasPassword: true,
		}},
		Settings: map[string]json.RawMessage{
			"natcatty.preferences": json.RawMessage(`{"appearance":{}}`),
		},
		Secrets: []appsync.SecretValue{
			{EntityType: appvault.EntityHost, EntityID: "host-1", Field: appvault.FieldPassword, Value: "fresh"},
			{EntityType: historysync.SecretEntityType, EntityID: historysync.SecretEntityID,
				Field: "session_token", Value: "attacker-token"},
			{EntityType: historysync.SecretEntityType, EntityID: historysync.SecretEntityID,
				Field: "master_key", Value: "attacker-key"},
		},
	}
	err := New(records, secrets, WithSettings(settings)).Apply(context.Background(), payload)
	if !errors.Is(err, ErrUnsupportedSecretEntity) {
		t.Fatalf("Apply = %v, want ErrUnsupportedSecretEntity", err)
	}
	if secrets.values[tokenRef] != token || secrets.values[keyRef] != key {
		t.Fatalf("inbound payload replaced Atuin credentials: %+v", secrets.values)
	}
	if len(secrets.set) != 0 {
		t.Fatalf("credentials were written after the preflight failed: %+v", secrets.set)
	}
	if len(records.applied) != 0 {
		t.Fatalf("records were replaced after the preflight failed: %+v", records.applied)
	}
	if len(settings.applied) != 0 {
		t.Fatal("portable settings were applied after the preflight failed")
	}
}

func TestApplyStoresIncomingSecretsAndClearsStaleOnes(t *testing.T) {
	records := &fakeRecords{}
	secrets := &fakeSecrets{mode: appvault.SecretModeOS, values: map[appvault.SecretRef]string{
		hostRef("host-1"): "stale",
	}}
	payload := appsync.Payload{
		Hosts: []domainvault.Host{{ID: "host-1", Label: "h", Hostname: "h", Port: 22, Protocol: domainvault.ProtocolSSH, HasPassword: true}},
		Secrets: []appsync.SecretValue{{
			EntityType: appvault.EntityHost, EntityID: "host-1", Field: appvault.FieldPassword, Value: "fresh",
		}},
	}
	if err := New(records, secrets).Apply(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if len(records.applied) != 1 {
		t.Fatal("records were not replaced")
	}
	if secrets.values[hostRef("host-1")] != "fresh" {
		t.Fatalf("secret = %q", secrets.values[hostRef("host-1")])
	}

	// A later payload without the password deletes the stale credential.
	payload.Secrets = nil
	payload.Hosts[0].HasPassword = false
	if err := New(records, secrets).Apply(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := secrets.values[hostRef("host-1")]; ok {
		t.Fatal("stale credential survived")
	}
}

type fakeSettings struct {
	snapshot map[string]json.RawMessage
	applied  map[string]json.RawMessage
}

func (s *fakeSettings) Snapshot(context.Context) (map[string]json.RawMessage, error) {
	return s.snapshot, nil
}

func (s *fakeSettings) Apply(_ context.Context, settings map[string]json.RawMessage) error {
	s.applied = settings
	return nil
}

func TestSnapshotCarriesStableGroupReferencesAndPortableSettings(t *testing.T) {
	records := &fakeRecords{
		hosts: []domainvault.Host{{
			ID: "host-1", Label: "h", Hostname: "h", Port: 22,
			Protocol: domainvault.ProtocolSSH, Group: "Prod/Web",
		}},
		groups: []domainvault.Group{{Path: "Prod/Web", Notes: "web"}},
		snippets: []domainvault.Snippet{{
			ID: "snippet-1", Label: "deploy", TargetGroups: []string{"Prod/Web"},
		}},
	}
	settings := &fakeSettings{snapshot: map[string]json.RawMessage{
		"natcatty.preferences": json.RawMessage(`{"appearance":{}}`),
	}}
	payload, err := New(records, &fakeSecrets{}, WithSettings(settings)).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(payload.Groups) != 2 {
		t.Fatalf("groups = %+v, want the saved group and its implicit parent", payload.Groups)
	}
	pathIDs := map[string]string{}
	for _, group := range payload.Groups {
		if group.ID == "" {
			t.Fatalf("group %q has no stable ID", group.Path)
		}
		pathIDs[group.Path] = group.ID
	}
	webID := pathIDs["Prod/Web"]
	if webID == "" || pathIDs["Prod"] == webID {
		t.Fatalf("group IDs = %v", pathIDs)
	}
	if payload.Hosts[0].GroupID != webID {
		t.Fatalf("Host GroupID = %q, want %q", payload.Hosts[0].GroupID, webID)
	}
	if len(payload.Snippets[0].TargetGroupIDs) != 1 ||
		payload.Snippets[0].TargetGroupIDs[0] != webID {
		t.Fatalf("Snippet target IDs = %v, want %q", payload.Snippets[0].TargetGroupIDs, webID)
	}
	if _, ok := payload.Settings["natcatty.preferences"]; !ok {
		t.Fatalf("portable settings are missing: %+v", payload.Settings)
	}
}

func TestApplyForwardsPortableSettingsToTheSettingsPort(t *testing.T) {
	records := &fakeRecords{}
	settings := &fakeSettings{}
	payload := appsync.Payload{Settings: map[string]json.RawMessage{
		"natcatty.preferences": json.RawMessage(`{"appearance":{}}`),
	}}
	if err := New(records, &fakeSecrets{}, WithSettings(settings)).Apply(context.Background(), payload); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(settings.applied) != 1 {
		t.Fatalf("applied settings = %+v, want the full document map", settings.applied)
	}
	if len(records.applied) != 1 {
		t.Fatal("records were not replaced")
	}
}

func TestApplyPropagatesTheRecordTransactionError(t *testing.T) {
	records := &fakeRecords{applyErr: errors.New("boom")}
	secrets := &fakeSecrets{mode: appvault.SecretModeOS}
	if err := New(records, secrets).Apply(context.Background(), appsync.Payload{}); err == nil {
		t.Fatal("transaction error was swallowed")
	}
	if len(secrets.set) != 0 {
		t.Fatal("credentials were written after a failed record transaction")
	}
}

const (
	testAuthKey    = "tailscale-auth-key-plaintext"
	testPrivateKey = "wireguard-private-key-plaintext"
	testPSK        = "wireguard-preshared-key-plaintext"
)

func networkFixtures() (domainvault.NetworkProfile, domainvault.NetworkProfile) {
	now := time.UnixMilli(5000)
	tailscale := domainvault.NetworkProfile{
		ID: "net-1", Label: "tailnet", Kind: domainvault.NetworkKindTailscale,
		Tailscale: &domainvault.TailscaleNetwork{Hostname: "node", HasAuthKey: true},
		UpdatedAt: now,
	}
	wireguard := domainvault.NetworkProfile{
		ID: "net-2", Label: "wg", Kind: domainvault.NetworkKindWireGuard,
		WireGuard: &domainvault.WireGuardNetwork{
			Addresses: []string{"10.0.0.2/32"}, HasPrivateKey: true,
			Peers: []domainvault.WireGuardPeer{{
				ID: "peer-1", PublicKey: "peer-public", AllowedIPs: []string{"10.0.0.0/24"},
				HasPresharedKey: true,
			}},
		},
		UpdatedAt: now,
	}
	return tailscale, wireguard
}

func networkRefs() (auth, private, psk appvault.SecretRef) {
	return appvault.NetworkProfileSecretRef("net-1", appvault.FieldNetworkAuthKey),
		appvault.NetworkProfileSecretRef("net-2", appvault.FieldNetworkPrivateKey),
		appvault.NetworkPeerSecretRef("peer-1", appvault.FieldNetworkPresharedKey)
}

func TestSnapshotCarriesNetworkProfileSecretsWithoutPlaintextRecords(t *testing.T) {
	tailscale, wireguard := networkFixtures()
	records := &fakeRecords{networks: []domainvault.NetworkProfile{tailscale, wireguard}}
	auth, private, psk := networkRefs()
	secrets := &fakeSecrets{mode: appvault.SecretModeOS, values: map[appvault.SecretRef]string{
		auth: testAuthKey, private: testPrivateKey, psk: testPSK,
	}}
	payload, err := New(records, secrets).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.NetworkProfiles) != 2 {
		t.Fatalf("profiles = %+v", payload.NetworkProfiles)
	}
	if payload.SyncedAt != 5000 {
		t.Fatalf("SyncedAt = %d", payload.SyncedAt)
	}
	if len(payload.Secrets) != 3 {
		t.Fatalf("secrets = %+v", payload.Secrets)
	}
	byRef := map[appvault.SecretRef]string{}
	for _, secret := range payload.Secrets {
		byRef[appvault.SecretRef{
			EntityType: secret.EntityType, EntityID: secret.EntityID, Field: secret.Field,
		}] = secret.Value
	}
	if byRef[auth] != testAuthKey || byRef[private] != testPrivateKey || byRef[psk] != testPSK {
		t.Fatalf("network secrets = %+v", byRef)
	}
	// The profile records themselves never carry credential plaintext; they
	// only advertise the Has* markers.
	encoded, err := json.Marshal(payload.NetworkProfiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, plaintext := range []string{testAuthKey, testPrivateKey, testPSK} {
		if strings.Contains(string(encoded), plaintext) {
			t.Fatalf("profile records contain plaintext %q", plaintext)
		}
	}
	if !strings.Contains(string(encoded), `"hasAuthKey":true`) ||
		!strings.Contains(string(encoded), `"hasPrivateKey":true`) ||
		!strings.Contains(string(encoded), `"hasPresharedKey":true`) {
		t.Fatalf("Has* markers were lost: %s", encoded)
	}
}

// TestSnapshotExcludesRuntimeStateFiles proves cloud sync is built only from
// Vault records and field secrets. A sentinel planted where an embedded
// Tailscale node keeps its state directory must not appear in the payload,
// even though the file sits under the same user-data tree.
func TestSnapshotExcludesRuntimeStateFiles(t *testing.T) {
	userData := t.TempDir()
	stateDir := filepath.Join(userData, "network-providers", "tailscale", "node-0123456789abcdef")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const sentinel = "tailscale-node-state-sentinel"
	if err := os.WriteFile(filepath.Join(stateDir, "tailscaled.state"), []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	tailscale, _ := networkFixtures()
	records := &fakeRecords{networks: []domainvault.NetworkProfile{tailscale}}
	secrets := &fakeSecrets{mode: appvault.SecretModeOS, values: map[appvault.SecretRef]string{}}
	payload, err := New(records, secrets).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{sentinel, "network-providers", "tailscaled.state"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("the sync payload contains runtime state %q: %s", forbidden, encoded)
		}
	}
}

func TestSnapshotOmitsAMissingNetworkSecretButFailsOnAnUnreadableOne(t *testing.T) {
	tailscale, _ := networkFixtures()
	records := &fakeRecords{networks: []domainvault.NetworkProfile{tailscale}}
	secrets := &fakeSecrets{mode: appvault.SecretModeOS, values: map[appvault.SecretRef]string{}}
	payload, err := New(records, secrets).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("a stale HasAuthKey marker should omit the secret: %v", err)
	}
	if len(payload.Secrets) != 0 {
		t.Fatalf("secrets = %+v", payload.Secrets)
	}

	secrets.getErr = appvault.ErrSecretsLocked
	if _, err := New(records, secrets).Snapshot(context.Background()); err == nil {
		t.Fatal("an unreadable network secret was silently dropped")
	}
	secrets.getErr = appvault.ErrSecretUnreadable
	if _, err := New(records, secrets).Snapshot(context.Background()); !errors.Is(err, appvault.ErrSecretUnreadable) {
		t.Fatalf("a damaged network secret error = %v, want ErrSecretUnreadable", err)
	}
}

func TestSnapshotEncodesAnEmptyNetworkFamilyAsExplicitlyEmpty(t *testing.T) {
	payload, err := New(&fakeRecords{}, &fakeSecrets{mode: appvault.SecretModeOS}).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if payload.NetworkProfiles == nil {
		t.Fatal("an empty family must be non-nil so a deletion is distinguishable")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"networkProfiles":[]`) {
		t.Fatalf("empty family did not serialize as []: %s", encoded)
	}
}

func TestApplyRemovesSecretsForDroppedNetworkProfilesAndPeers(t *testing.T) {
	tailscale, wireguard := networkFixtures()
	removedPeer := domainvault.WireGuardPeer{
		ID: "peer-2", PublicKey: "peer-public-2", HasPresharedKey: true,
	}
	wireguard.WireGuard.Peers = append(wireguard.WireGuard.Peers, removedPeer)
	records := &fakeRecords{networks: []domainvault.NetworkProfile{tailscale, wireguard}}
	auth, private, psk := networkRefs()
	removedPSK := appvault.NetworkPeerSecretRef("peer-2", appvault.FieldNetworkPresharedKey)
	secrets := &fakeSecrets{mode: appvault.SecretModeOS, values: map[appvault.SecretRef]string{
		auth: "old-auth", private: "old-private", psk: "old-psk", removedPSK: "old-removed-psk",
	}}

	// The incoming payload keeps net-2 with one peer; net-1 and peer-2 are gone.
	incoming := appsync.Payload{
		NetworkProfiles: []domainvault.NetworkProfile{{
			ID: "net-2", Label: "wg", Kind: domainvault.NetworkKindWireGuard,
			WireGuard: &domainvault.WireGuardNetwork{
				Addresses: []string{"10.0.0.2/32"}, HasPrivateKey: true,
				Peers: []domainvault.WireGuardPeer{{
					ID: "peer-1", PublicKey: "peer-public", HasPresharedKey: true,
				}},
			},
		}},
		Secrets: []appsync.SecretValue{
			{EntityType: appvault.EntityNetworkProfile, EntityID: "net-2", Field: appvault.FieldNetworkPrivateKey, Value: "new-private"},
			{EntityType: appvault.EntityNetworkPeer, EntityID: "peer-1", Field: appvault.FieldNetworkPresharedKey, Value: "new-psk"},
		},
	}
	if err := New(records, secrets).Apply(context.Background(), incoming); err != nil {
		t.Fatal(err)
	}
	if len(records.applied) != 1 || len(records.applied[0].NetworkProfiles) != 1 {
		t.Fatalf("applied network profiles = %+v", records.applied)
	}
	if _, ok := secrets.values[auth]; ok {
		t.Fatal("a dropped profile's Auth Key ciphertext survived")
	}
	if _, ok := secrets.values[removedPSK]; ok {
		t.Fatal("a dropped peer's Preshared Key ciphertext survived")
	}
	if secrets.values[private] != "new-private" || secrets.values[psk] != "new-psk" {
		t.Fatalf("incoming network secrets = %+v", secrets.values)
	}

	// A payload written before the family existed has no opinion about
	// Network Profiles: the local records and credentials survive.
	if err := New(records, secrets).Apply(context.Background(), appsync.Payload{}); err != nil {
		t.Fatal(err)
	}
	lastApplied := records.applied[len(records.applied)-1]
	if len(lastApplied.NetworkProfiles) != 2 {
		t.Fatalf("a family-less payload erased local profiles: %+v", lastApplied.NetworkProfiles)
	}
	for _, ref := range []appvault.SecretRef{private, psk} {
		if _, ok := secrets.values[ref]; !ok {
			t.Fatalf("network secret %+v was cleared by a family-less payload", ref)
		}
	}

	// An explicit empty family from a current writer does delete everything.
	explicitEmpty := appsync.Payload{NetworkProfiles: []domainvault.NetworkProfile{}}
	if err := New(records, secrets).Apply(context.Background(), explicitEmpty); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []appvault.SecretRef{auth, private, psk, removedPSK} {
		if _, ok := secrets.values[ref]; ok {
			t.Fatalf("network secret %+v survived an explicit empty payload", ref)
		}
	}
}
