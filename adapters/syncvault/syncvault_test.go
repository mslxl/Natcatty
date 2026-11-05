package syncvault

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

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
