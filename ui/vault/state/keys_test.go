package state

import (
	"context"
	"strconv"
	"testing"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type fakeKeys struct {
	items   []domainvault.Key
	created []domainvault.KeyDraft
	updated []domainvault.KeyDraft
	deleted []model.KeyID
	err     error
	nextID  int
}

func (f *fakeKeys) ListKeys(context.Context) ([]domainvault.Key, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]domainvault.Key(nil), f.items...), nil
}

func (f *fakeKeys) CreateKey(_ context.Context, draft domainvault.KeyDraft) (domainvault.Key, error) {
	if f.err != nil {
		return domainvault.Key{}, f.err
	}
	f.nextID++
	key := domainvault.Key{
		ID:            model.KeyID("key-" + strconv.Itoa(f.nextID)),
		Label:         draft.Label,
		Type:          domainvault.KeyTypeED25519,
		Source:        domainvault.KeySourceImported,
		HasPrivateKey: true,
	}
	f.created = append(f.created, draft)
	f.items = append(f.items, key)
	return key, nil
}

func (f *fakeKeys) UpdateKey(_ context.Context, id model.KeyID, draft domainvault.KeyDraft) (domainvault.Key, error) {
	if f.err != nil {
		return domainvault.Key{}, f.err
	}
	for i, key := range f.items {
		if key.ID != id {
			continue
		}
		key.Label = draft.Label
		key.Certificate = draft.Certificate
		f.items[i] = key
		f.updated = append(f.updated, draft)
		return key, nil
	}
	return domainvault.Key{}, appvault.ErrNotFound
}

func (f *fakeKeys) DeleteKey(_ context.Context, id model.KeyID) error {
	if f.err != nil {
		return f.err
	}
	for i, key := range f.items {
		if key.ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			f.deleted = append(f.deleted, id)
			return nil
		}
	}
	return appvault.ErrNotFound
}

type fakeIdentities struct {
	items   []domainvault.Identity
	created []domainvault.IdentityDraft
	updated []domainvault.IdentityDraft
	deleted []model.IdentityID
	err     error
	nextID  int
}

func (f *fakeIdentities) ListIdentities(context.Context) ([]domainvault.Identity, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]domainvault.Identity(nil), f.items...), nil
}

func (f *fakeIdentities) CreateIdentity(_ context.Context, draft domainvault.IdentityDraft) (domainvault.Identity, error) {
	if f.err != nil {
		return domainvault.Identity{}, f.err
	}
	f.nextID++
	identity := domainvault.Identity{
		ID:         model.IdentityID("identity-" + strconv.Itoa(f.nextID)),
		Label:      draft.Label,
		Username:   draft.Username,
		AuthMethod: draft.AuthMethod,
		KeyID:      draft.KeyID,
	}
	f.created = append(f.created, draft)
	f.items = append(f.items, identity)
	return identity, nil
}

func (f *fakeIdentities) UpdateIdentity(_ context.Context, id model.IdentityID, draft domainvault.IdentityDraft) (domainvault.Identity, error) {
	if f.err != nil {
		return domainvault.Identity{}, f.err
	}
	for i, identity := range f.items {
		if identity.ID != id {
			continue
		}
		identity.Label = draft.Label
		identity.Username = draft.Username
		identity.AuthMethod = draft.AuthMethod
		identity.KeyID = draft.KeyID
		f.items[i] = identity
		f.updated = append(f.updated, draft)
		return identity, nil
	}
	return domainvault.Identity{}, appvault.ErrNotFound
}

func (f *fakeIdentities) DeleteIdentity(_ context.Context, id model.IdentityID) error {
	if f.err != nil {
		return f.err
	}
	for i, identity := range f.items {
		if identity.ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			f.deleted = append(f.deleted, id)
			return nil
		}
	}
	return appvault.ErrNotFound
}

func newKeyState(t *testing.T) (*State, *fakeHosts, *fakeKeys, *fakeIdentities) {
	t.Helper()
	hosts := &fakeHosts{}
	keys := &fakeKeys{}
	identities := &fakeIdentities{}
	state := NewState(hosts, WithKeys(keys), WithIdentities(identities))
	return state, hosts, keys, identities
}

func TestSaveKeySubmitsTheMaterialOnceAndReloads(t *testing.T) {
	state, _, keys, _ := newKeyState(t)

	state.BeginCreateKey()
	if !state.EditingKey() {
		t.Fatal("BeginCreateKey did not open the editor")
	}
	form := state.KeyForm()
	form.Label = "work"
	form.PrivateKey = "PRIVATE MATERIAL"
	form.Passphrase = "key-passphrase"
	form.Certificate = "cert-line"

	if err := state.SaveKey(); err != nil {
		t.Fatalf("SaveKey: %v", err)
	}
	if state.EditingKey() {
		t.Fatal("SaveKey left the editor open")
	}
	if len(keys.created) != 1 {
		t.Fatalf("created = %+v, want one draft", keys.created)
	}
	draft := keys.created[0]
	if draft.Label != "work" || draft.PrivateKey != "PRIVATE MATERIAL" ||
		draft.Passphrase != "key-passphrase" || !draft.SavePassphrase ||
		draft.Certificate != "cert-line" {
		t.Fatalf("draft = %+v", draft)
	}
	if state.KeyCount() != 1 || state.KeyForm().PrivateKey != "" || state.KeyForm().Passphrase != "" {
		t.Fatalf("state after save = %d keys, form %+v", state.KeyCount(), state.KeyForm())
	}
}

func TestEditKeyNeverLoadsStoredMaterialIntoTheForm(t *testing.T) {
	state, _, keys, _ := newKeyState(t)
	keys.items = []domainvault.Key{{
		ID: "key-1", Label: "work", Type: domainvault.KeyTypeRSA,
		HasPrivateKey: true, HasPassphrase: true, Certificate: "cert-line",
	}}
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}

	if !state.BeginEditKey("key-1") {
		t.Fatal("BeginEditKey did not open the editor")
	}
	form := state.KeyForm()
	if form.Label != "work" || !form.HasPrivateKey || !form.HasPassphrase || !form.HasCertificate {
		t.Fatalf("form = %+v", form)
	}
	if form.PrivateKey != "" || form.Passphrase != "" {
		t.Fatalf("stored credential material entered the form: %+v", form)
	}
}

func TestSaveKeyMapsPassphraseAndMaterialValidation(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected Problem
	}{
		{
			name: "passphrase",
			err: &appvault.Error{Kind: appvault.KindValidation, Cause: &domainvault.ValidationError{
				Field: domainvault.FieldPassphrase, Rule: domainvault.RuleInvalid,
			}},
			expected: ProblemKeyPassphrase,
		},
		{
			name: "certificate",
			err: &appvault.Error{Kind: appvault.KindValidation, Cause: &domainvault.ValidationError{
				Field: domainvault.FieldCertificate, Rule: domainvault.RuleInvalid,
			}},
			expected: ProblemKeyCertificate,
		},
		{
			name: "material",
			err: &appvault.Error{Kind: appvault.KindValidation, Cause: &domainvault.ValidationError{
				Field: domainvault.FieldKeyMaterial, Rule: domainvault.RuleInvalid,
			}},
			expected: ProblemKeyMaterial,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, _, keys, _ := newKeyState(t)
			keys.err = test.err
			state.BeginCreateKey()
			state.KeyForm().Label = "work"
			state.KeyForm().PrivateKey = "material"
			if err := state.SaveKey(); err != nil {
				t.Fatalf("SaveKey: %v", err)
			}
			if state.Problem() != test.expected {
				t.Fatalf("problem = %q, want %q", state.Problem(), test.expected)
			}
			if !state.EditingKey() {
				t.Fatal("a failed save closed the editor")
			}
		})
	}
}

func TestDeleteKeyNeedsConfirmationAndLeavesHostsAlone(t *testing.T) {
	state, hosts, keys, _ := newKeyState(t)
	keys.items = []domainvault.Key{{ID: "key-1", Label: "work"}, {ID: "key-2", Label: "other"}}
	hosts.items = []domainvault.Host{{ID: "host-1", Label: "web", KeyID: "key-1"}}
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}

	state.RequestDeleteKey("key-1")
	if state.PendingDeleteKey() != "key-1" {
		t.Fatal("RequestDeleteKey did not queue the confirmation")
	}
	if err := state.ConfirmDeleteKey(); err != nil {
		t.Fatalf("ConfirmDeleteKey: %v", err)
	}
	if len(keys.deleted) != 1 || keys.deleted[0] != "key-1" {
		t.Fatalf("deleted keys = %v", keys.deleted)
	}
	if state.KeyCount() != 1 || state.Keys()[0].ID != "key-2" {
		t.Fatalf("keys = %+v, want key-2", state.Keys())
	}
	if hosts.items[0].KeyID != "key-1" {
		t.Fatal("deleting a Key cascaded to the Host reference")
	}
}

func TestSaveIdentitySubmitsMethodKeyAndPassword(t *testing.T) {
	state, _, _, identities := newKeyState(t)

	state.BeginCreateIdentity()
	form := state.IdentityForm()
	form.Label = "deploy"
	form.Username = "deploy"
	form.AuthMethod = string(domainvault.IdentityAuthKey)
	form.KeyID = "key-1"

	if err := state.SaveIdentity(); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
	if len(identities.created) != 1 {
		t.Fatalf("created = %+v", identities.created)
	}
	draft := identities.created[0]
	if draft.Label != "deploy" || draft.Username != "deploy" ||
		draft.AuthMethod != domainvault.IdentityAuthKey || draft.KeyID != "key-1" {
		t.Fatalf("draft = %+v", draft)
	}
	if state.IdentityCount() != 1 {
		t.Fatalf("identity count = %d, want 1", state.IdentityCount())
	}
}

func TestIdentityKeyValidationMapsToAProblem(t *testing.T) {
	state, _, _, identities := newKeyState(t)
	identities.err = &appvault.Error{Kind: appvault.KindValidation, Cause: &domainvault.ValidationError{
		Field: domainvault.FieldKey, Rule: domainvault.RuleRequired,
	}}
	state.BeginCreateIdentity()
	state.IdentityForm().Label = "deploy"
	state.IdentityForm().AuthMethod = string(domainvault.IdentityAuthKey)
	if err := state.SaveIdentity(); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
	if state.Problem() != ProblemIdentityKeyRequired {
		t.Fatalf("problem = %q, want identity key required", state.Problem())
	}
}

func TestHostEditorPersistsTheAuthSelection(t *testing.T) {
	state, hosts, _, _ := newKeyState(t)

	state.BeginCreate()
	form := state.Form()
	form.Label = "web"
	form.Hostname = "example.com"
	form.Username = "root"
	form.IdentityID = "identity-1"
	form.KeyID = "key-1"
	form.AuthMethod = string(domainvault.HostAuthCertificate)
	form.UseSSHAgent = true
	form.RequiresMFA = true
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(hosts.created) != 1 {
		t.Fatalf("created = %+v", hosts.created)
	}
	draft := hosts.created[0]
	if draft.IdentityID != "identity-1" || draft.KeyID != "key-1" ||
		draft.AuthMethod != domainvault.HostAuthCertificate ||
		!draft.UseSSHAgent || !draft.RequiresMFA {
		t.Fatalf("draft = %+v", draft)
	}
}

func TestBeginEditRestoresTheHostAuthSelection(t *testing.T) {
	state, hosts, _, _ := newKeyState(t)
	hosts.items = []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22, Username: "root",
		Protocol: domainvault.ProtocolSSH, IdentityID: "identity-1", KeyID: "key-1",
		AuthMethod: domainvault.HostAuthKey, UseSSHAgent: true, RequiresMFA: true,
	}}
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}
	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit did not open the editor")
	}
	form := state.Form()
	if form.IdentityID != "identity-1" || form.KeyID != "key-1" ||
		form.AuthMethod != string(domainvault.HostAuthKey) ||
		!form.UseSSHAgent || !form.RequiresMFA {
		t.Fatalf("form = %+v", form)
	}
}
